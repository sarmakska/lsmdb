package lsmdb

import (
	"github.com/sarmakska/lsmdb/internal/encoding"
	"github.com/sarmakska/lsmdb/internal/skiplist"
	"github.com/sarmakska/lsmdb/internal/sstable"
)

// tableIter adapts an SSTable iterator to the internalIterator interface.
type tableIter struct{ it *sstable.Iterator }

func (t *tableIter) Valid() bool                 { return t.it.Valid() }
func (t *tableIter) Key() encoding.InternalKey   { return t.it.Key() }
func (t *tableIter) Value() []byte               { return t.it.Value() }
func (t *tableIter) Next()                       { t.it.Next() }
func (t *tableIter) SeekToFirst()                { t.it.SeekToFirst() }
func (t *tableIter) Seek(k encoding.InternalKey) { t.it.Seek(k) }

// memIter adapts a skip-list iterator to the internalIterator interface.
type memIter struct{ it *skiplist.Iterator }

func (m *memIter) Valid() bool                 { return m.it.Valid() }
func (m *memIter) Key() encoding.InternalKey   { return m.it.Key() }
func (m *memIter) Value() []byte               { return m.it.Value() }
func (m *memIter) Next()                       { m.it.Next() }
func (m *memIter) SeekToFirst()                { m.it.SeekToFirst() }
func (m *memIter) Seek(k encoding.InternalKey) { m.it.Seek(k) }

// Snapshot is a read-only view of the database at a fixed sequence number. Reads
// taken through a snapshot never observe writes made after the snapshot was
// created, which is how lsmdb provides snapshot isolation.
type Snapshot struct {
	db  *DB
	seq uint64
}

// Snapshot captures the current committed sequence number. Releasing a snapshot
// is a no-op because the engine retains versions until compaction; for a
// long-lived snapshot the application should keep writes bounded.
func (db *DB) Snapshot() *Snapshot {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return &Snapshot{db: db, seq: db.lastSeq}
}

// Get resolves a key as visible at the snapshot's sequence number.
func (s *Snapshot) Get(key []byte) ([]byte, error) {
	return s.db.getAt(key, s.seq)
}

// NewIterator returns a range iterator that observes the snapshot.
func (s *Snapshot) NewIterator() *Iterator {
	return s.db.newIteratorAt(s.seq)
}

// Iterator is the public range-scan cursor. It walks user keys in ascending
// order and exposes, for each key, the newest version visible at the iterator's
// snapshot sequence, skipping keys whose newest visible version is a tombstone.
type Iterator struct {
	merged *mergingIterator
	seq    uint64

	key   []byte
	value []byte
	valid bool
}

// NewIterator returns an iterator over the latest committed state.
func (db *DB) NewIterator() *Iterator {
	return db.newIteratorAt(encoding.MaxSequence)
}

// newIteratorAt builds a merging iterator over every live source. The sources
// are snapshotted under the read lock; the SSTables are immutable and the
// MemTables are appended to but never mutated in place, so the iterator sees a
// stable view for the keys it has already passed.
func (db *DB) newIteratorAt(seq uint64) *Iterator {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var iters []internalIterator
	iters = append(iters, &memIter{it: db.mem.NewIterator()})
	if db.imm != nil {
		iters = append(iters, &memIter{it: db.imm.NewIterator()})
	}
	for _, t := range db.levels[0] {
		iters = append(iters, &tableIter{it: t.NewIterator()})
	}
	for lvl := 1; lvl < numLevels; lvl++ {
		for _, t := range db.levels[lvl] {
			iters = append(iters, &tableIter{it: t.NewIterator()})
		}
	}
	return &Iterator{merged: newMergingIterator(iters), seq: seq}
}

// SeekToFirst positions the iterator at the first visible key.
func (it *Iterator) SeekToFirst() {
	it.merged.SeekToFirst()
	it.advanceToVisible(nil)
}

// Seek positions the iterator at the first visible key greater than or equal to
// target.
func (it *Iterator) Seek(target []byte) {
	it.merged.Seek(encoding.MakeInternalKey(target, encoding.MaxSequence, encoding.KindSet))
	it.advanceToVisible(nil)
}

// Next advances to the next distinct visible user key.
func (it *Iterator) Next() {
	if !it.valid {
		return
	}
	prev := append([]byte(nil), it.key...)
	it.advanceToVisible(prev)
}

// advanceToVisible scans the merged stream for the next user key whose newest
// version at or below the snapshot is a live value. It skips versions newer
// than the snapshot, older duplicate versions, tombstoned keys, and the
// previously yielded key.
func (it *Iterator) advanceToVisible(skipKey []byte) {
	for it.merged.Valid() {
		ik := it.merged.Key()
		uk := ik.UserKey()

		if skipKey != nil && encoding.CompareBytes(uk, skipKey) == 0 {
			it.merged.Next()
			continue
		}
		// Skip versions not visible at the snapshot.
		if ik.Sequence() > it.seq {
			it.merged.Next()
			continue
		}
		// This is the newest visible version of uk. Decide on its kind, then
		// skip the rest of this user key's versions.
		if ik.Kind() == encoding.KindDelete {
			it.skipUserKey(uk)
			skipKey = append(skipKey[:0], uk...)
			continue
		}
		it.key = append(it.key[:0], uk...)
		it.value = append(it.value[:0], it.merged.Value()...)
		it.valid = true
		// Position the merged cursor past this key so Next starts fresh.
		return
	}
	it.valid = false
}

// skipUserKey advances the merged cursor past every remaining version of uk.
func (it *Iterator) skipUserKey(uk []byte) {
	for it.merged.Valid() && encoding.CompareBytes(it.merged.Key().UserKey(), uk) == 0 {
		it.merged.Next()
	}
}

// Valid reports whether the iterator points at a live key.
func (it *Iterator) Valid() bool { return it.valid }

// Key returns the current user key.
func (it *Iterator) Key() []byte { return it.key }

// Value returns the current value.
func (it *Iterator) Value() []byte { return it.value }
