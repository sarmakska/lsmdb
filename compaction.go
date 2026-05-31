package lsmdb

import (
	"os"

	"github.com/sarmakska/lsmdb/internal/encoding"
	"github.com/sarmakska/lsmdb/internal/sstable"
)

// targetTableSize bounds the size of a table produced by compaction. Output is
// split into multiple tables once a writer reaches this many entries, which
// keeps individual tables small enough to map and to compact again later.
const compactionMaxEntries = 100000

// maybeCompactLocked picks and runs at most one compaction. It is called after
// every flush. The policy is intentionally simple but correct: if L0 has
// accumulated enough tables, merge all of L0 into L1; otherwise find the first
// level that exceeds its size budget and merge one of its tables down.
func (db *DB) maybeCompactLocked() error {
	for {
		level, inputs, ok := db.pickCompaction()
		if !ok {
			return nil
		}
		if err := db.runCompaction(level, inputs); err != nil {
			return err
		}
	}
}

// pickCompaction selects a source level and the input tables to merge. It
// returns the source level, the tables from that level and the overlapping
// tables from the next level combined, and whether work was found.
func (db *DB) pickCompaction() (level int, inputs []*sstable.Reader, ok bool) {
	// L0 to L1: trigger on table count because L0 tables overlap arbitrarily.
	if len(db.levels[0]) >= db.opts.L0CompactionTrigger {
		inputs = append(inputs, db.levels[0]...)
		smallest, largest := keyRange(inputs)
		inputs = append(inputs, db.overlapping(1, smallest, largest)...)
		return 0, inputs, true
	}
	// L1 and below: trigger on byte budget, approximated by entry count.
	budget := int64(db.opts.L0CompactionTrigger) * 1000
	for lvl := 1; lvl < numLevels-1; lvl++ {
		if levelEntries(db.levels[lvl]) <= budget {
			budget *= int64(db.opts.LevelSizeMultiplier)
			continue
		}
		// Compact the first table of this level into the next.
		src := db.levels[lvl][0]
		inputs = append(inputs, src)
		inputs = append(inputs, db.overlapping(lvl+1, src.Smallest(), src.Largest())...)
		return lvl, inputs, true
	}
	return 0, nil, false
}

// overlapping returns the tables in level whose key range overlaps [smallest,
// largest] on the user-key axis.
func (db *DB) overlapping(level int, smallest, largest encoding.InternalKey) []*sstable.Reader {
	var out []*sstable.Reader
	us, ul := smallest.UserKey(), largest.UserKey()
	for _, t := range db.levels[level] {
		ts, tl := t.Smallest().UserKey(), t.Largest().UserKey()
		if encoding.CompareBytes(tl, us) < 0 || encoding.CompareBytes(ts, ul) > 0 {
			continue // disjoint
		}
		out = append(out, t)
	}
	return out
}

// runCompaction merges inputs into one or more output tables on the target
// level, then atomically swaps the input tables for the outputs in the manifest
// and deletes the input files to reclaim space.
//
// Tombstone handling: a deletion is dropped only when the compaction reaches
// the bottom level, because no older version can exist below it. Above the
// bottom level the tombstone is retained so it continues to shadow versions in
// deeper levels. Within one user key only the newest version (the first the
// merging iterator yields) is kept; superseded versions are discarded, which is
// where space is reclaimed.
func (db *DB) runCompaction(srcLevel int, inputs []*sstable.Reader) error {
	targetLevel := srcLevel + 1
	isBottom := targetLevel >= numLevels-1

	iters := make([]internalIterator, 0, len(inputs))
	for _, t := range inputs {
		iters = append(iters, &tableIter{it: t.NewIterator()})
	}
	merged := newMergingIterator(iters)
	merged.SeekToFirst()

	var (
		added       []tableMeta
		writer      *sstable.Writer
		fileNum     uint64
		lastUserKey []byte
		haveLast    bool
	)

	openWriter := func() error {
		fileNum = db.allocFileNum()
		w, err := sstable.NewWriter(db.tablePath(fileNum), db.opts.BloomFalsePositiveRate)
		if err != nil {
			return err
		}
		writer = w
		return nil
	}
	closeWriter := func() error {
		if writer == nil || writer.Count() == 0 {
			if writer != nil {
				writer.Abort()
				writer = nil
			}
			return nil
		}
		if err := writer.Finish(); err != nil {
			return err
		}
		r, err := sstable.Open(db.tablePath(fileNum))
		if err != nil {
			return err
		}
		added = append(added, tableMeta{
			FileNum:  fileNum,
			Level:    targetLevel,
			Smallest: append([]byte(nil), r.Smallest()...),
			Largest:  append([]byte(nil), r.Largest()...),
			Count:    r.Count(),
		})
		db.levels[targetLevel] = append(db.levels[targetLevel], r)
		writer = nil
		return nil
	}

	for ; merged.Valid(); merged.Next() {
		key := merged.Key()
		uk := key.UserKey()

		// Skip superseded versions: the merging iterator yields the newest
		// version of a user key first, so any later entry with the same user
		// key is older and dropped.
		if haveLast && encoding.CompareBytes(uk, lastUserKey) == 0 {
			continue
		}
		lastUserKey = append(lastUserKey[:0], uk...)
		haveLast = true

		// Drop a tombstone outright when this is the bottom level.
		if key.Kind() == encoding.KindDelete && isBottom {
			continue
		}

		if writer == nil {
			if err := openWriter(); err != nil {
				return err
			}
		}
		if err := writer.Add(key, merged.Value()); err != nil {
			return err
		}
		if writer.Count() >= compactionMaxEntries {
			if err := closeWriter(); err != nil {
				return err
			}
		}
	}
	if err := closeWriter(); err != nil {
		return err
	}

	// Record the swap durably before deleting input files.
	deleted := make([]uint64, 0, len(inputs))
	for _, t := range inputs {
		deleted = append(deleted, fileNumOf(t))
	}
	if err := db.manifest.append(manifestEdit{
		Added:       added,
		Deleted:     deleted,
		NextFileNum: db.nextFileNum,
		LastSeq:     db.lastSeq,
	}); err != nil {
		return err
	}

	// Remove the input tables from the in-memory level layout and delete files.
	db.removeTables(srcLevel, inputs)
	db.removeTables(targetLevel, inputs)
	for _, t := range inputs {
		_ = os.Remove(t.Path())
	}
	db.sortLevels()
	return nil
}

// removeTables drops the given tables from a level's slice by file number.
func (db *DB) removeTables(level int, remove []*sstable.Reader) {
	toRemove := make(map[uint64]bool, len(remove))
	for _, t := range remove {
		toRemove[fileNumOf(t)] = true
	}
	kept := db.levels[level][:0]
	for _, t := range db.levels[level] {
		if toRemove[fileNumOf(t)] {
			continue
		}
		kept = append(kept, t)
	}
	db.levels[level] = kept
}

func keyRange(tables []*sstable.Reader) (smallest, largest encoding.InternalKey) {
	for i, t := range tables {
		if i == 0 {
			smallest = t.Smallest()
			largest = t.Largest()
			continue
		}
		if encoding.CompareInternal(t.Smallest(), smallest) < 0 {
			smallest = t.Smallest()
		}
		if encoding.CompareInternal(t.Largest(), largest) > 0 {
			largest = t.Largest()
		}
	}
	return smallest, largest
}

func levelEntries(tables []*sstable.Reader) int64 {
	var n int64
	for _, t := range tables {
		n += int64(t.Count())
	}
	return n
}
