// Package lsmdb is a log-structured merge-tree storage engine written in Go with
// the standard library only. It provides ordered key-value storage with a
// write-ahead log for durability, a skip-list MemTable, immutable block-based
// SSTables with per-table bloom filters, levelled compaction, and MVCC snapshot
// reads backed by monotonic sequence numbers.
//
// The public surface is small: Open, Close, Put, Delete, Get, NewIterator and
// Snapshot. Everything below those methods (the WAL, the table format, the
// merging iterator and compaction) lives in this package and the internal
// packages.
package lsmdb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/sarmakska/lsmdb/internal/encoding"
	"github.com/sarmakska/lsmdb/internal/memtable"
	"github.com/sarmakska/lsmdb/internal/sstable"
	"github.com/sarmakska/lsmdb/internal/wal"
)

// ErrNotFound is returned by Get when no live version of a key is visible.
var ErrNotFound = errors.New("lsmdb: key not found")

// ErrClosed is returned by operations on a closed database.
var ErrClosed = errors.New("lsmdb: database closed")

// Options configures a database. The zero value is usable; Open fills defaults.
type Options struct {
	// MemTableSize is the byte threshold at which the active MemTable is frozen
	// and flushed to an L0 table. Defaults to 4 MiB.
	MemTableSize int64
	// BloomFalsePositiveRate sets the target false positive rate for per-table
	// bloom filters. Defaults to 0.01 (one percent).
	BloomFalsePositiveRate float64
	// L0CompactionTrigger is the number of L0 tables that triggers a compaction
	// into L1. Defaults to 4.
	L0CompactionTrigger int
	// LevelSizeMultiplier sets how much larger each level is than the one above.
	// Defaults to 10.
	LevelSizeMultiplier int
	// DisableAutoCompaction stops the engine from compacting automatically. The
	// test suite uses this to drive compaction deterministically.
	DisableAutoCompaction bool
}

func (o *Options) withDefaults() {
	if o.MemTableSize <= 0 {
		o.MemTableSize = 4 * 1024 * 1024
	}
	if o.BloomFalsePositiveRate <= 0 {
		o.BloomFalsePositiveRate = 0.01
	}
	if o.L0CompactionTrigger <= 0 {
		o.L0CompactionTrigger = 4
	}
	if o.LevelSizeMultiplier <= 0 {
		o.LevelSizeMultiplier = 10
	}
}

// numLevels is the fixed depth of the level hierarchy.
const numLevels = 7

// DB is the storage engine handle. It is safe for concurrent use by multiple
// goroutines.
type DB struct {
	dir  string
	opts Options

	mu sync.RWMutex

	mem    *memtable.MemTable // active write buffer
	imm    *memtable.MemTable // MemTable being flushed, read-only
	log    *wal.Writer        // WAL for the active MemTable
	logNum uint64

	levels [numLevels][]*sstable.Reader // live tables per level, L0 newest last

	manifest    *manifest
	nextFileNum uint64
	lastSeq     uint64

	closed bool
}

// Open opens or creates a database rooted at dir. On open it replays the
// manifest to rebuild the level layout, then replays the write-ahead log to
// recover any writes that were acknowledged but not yet flushed.
func Open(dir string, opts Options) (*DB, error) {
	opts.withDefaults()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	db := &DB{
		dir:  dir,
		opts: opts,
		mem:  memtable.New(),
	}

	tables, nextFile, lastSeq, err := loadManifest(db.manifestPath())
	if err != nil {
		return nil, err
	}
	db.nextFileNum = nextFile
	db.lastSeq = lastSeq

	// Open every live table named in the manifest and slot it into its level.
	metas := make([]tableMeta, 0, len(tables))
	for _, m := range tables {
		metas = append(metas, m)
	}
	for _, m := range metas {
		r, err := sstable.Open(db.tablePath(m.FileNum))
		if err != nil {
			return nil, fmt.Errorf("open table %d: %w", m.FileNum, err)
		}
		db.levels[m.Level] = append(db.levels[m.Level], r)
	}
	db.sortLevels()

	mf, err := openManifest(db.manifestPath())
	if err != nil {
		return nil, err
	}
	db.manifest = mf

	if err := db.recoverLog(); err != nil {
		return nil, err
	}

	// Start a fresh WAL for the active MemTable.
	db.logNum = db.allocFileNum()
	w, err := wal.Create(db.logPath(db.logNum))
	if err != nil {
		return nil, err
	}
	db.log = w

	return db, nil
}

// recoverLog replays every pre-existing WAL file in sequence-number order back
// into the active MemTable, restoring acknowledged writes that had not yet been
// flushed. A torn trailing record from a crash is dropped by the WAL reader, so
// only fully durable writes survive, exactly the durability contract.
func (db *DB) recoverLog() error {
	entries, err := os.ReadDir(db.dir)
	if err != nil {
		return err
	}
	var logNums []uint64
	for _, e := range entries {
		var n uint64
		if _, err := fmt.Sscanf(e.Name(), "%06d.log", &n); err == nil {
			logNums = append(logNums, n)
		}
	}
	sort.Slice(logNums, func(i, j int) bool { return logNums[i] < logNums[j] })

	for _, n := range logNums {
		r, err := wal.Open(db.logPath(n))
		if err != nil {
			return err
		}
		for {
			rec, err := r.Next()
			if err != nil {
				break // io.EOF, clean or torn tail
			}
			seq, kind, key, value, ok := decodeRecord(rec)
			if !ok {
				continue
			}
			db.mem.Add(seq, kind, key, value)
			if seq > db.lastSeq {
				db.lastSeq = seq
			}
		}
		r.Close()
		_ = os.Remove(db.logPath(n))
	}

	// If recovery rebuilt a non-trivial MemTable, flush it so the recovered
	// state lands durably in an SSTable before normal operation resumes.
	if db.mem.ApproximateSize() > 0 {
		if err := db.flushMemtableLocked(db.mem); err != nil {
			return err
		}
		db.mem = memtable.New()
	}
	return nil
}

// Put stores value under key. The write is appended to the WAL and synced
// before it returns, so a successful Put is durable across a crash.
func (db *DB) Put(key, value []byte) error {
	return db.write(encoding.KindSet, key, value)
}

// Delete removes key by writing a tombstone. The tombstone shadows older
// versions until compaction reclaims them.
func (db *DB) Delete(key []byte) error {
	return db.write(encoding.KindDelete, key, nil)
}

func (db *DB) write(kind encoding.Kind, key, value []byte) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}

	seq := db.lastSeq + 1

	// Durability barrier: append to the WAL and fsync before touching memory or
	// acknowledging. If the process dies after this returns, recovery replays
	// the record.
	rec := encodeRecord(seq, kind, key, value)
	if err := db.log.Append(rec); err != nil {
		return err
	}
	if err := db.log.Sync(); err != nil {
		return err
	}

	db.lastSeq = seq
	db.mem.Add(seq, kind, key, value)

	if db.mem.ApproximateSize() >= db.opts.MemTableSize {
		if err := db.rotateMemtableLocked(); err != nil {
			return err
		}
	}
	return nil
}

// rotateMemtableLocked freezes the active MemTable, flushes it to L0, starts a
// fresh MemTable and WAL, and then considers a compaction. It runs inline under
// the lock for deterministic behaviour, which keeps the durability and
// recovery semantics simple to reason about and to test.
func (db *DB) rotateMemtableLocked() error {
	frozen := db.mem
	oldLogNum := db.logNum

	if err := db.flushMemtableLocked(frozen); err != nil {
		return err
	}

	// New WAL and MemTable for subsequent writes.
	newLogNum := db.allocFileNum()
	w, err := wal.Create(db.logPath(newLogNum))
	if err != nil {
		return err
	}
	if err := db.log.Close(); err != nil {
		return err
	}
	_ = os.Remove(db.logPath(oldLogNum))
	db.log = w
	db.logNum = newLogNum
	db.mem = memtable.New()

	if !db.opts.DisableAutoCompaction {
		return db.maybeCompactLocked()
	}
	return nil
}

// flushMemtableLocked writes a MemTable to a new L0 SSTable and records the new
// table in the manifest. The MemTable iterator yields keys in internal-key
// order, which the SSTable writer requires.
func (db *DB) flushMemtableLocked(mt *memtable.MemTable) error {
	fileNum := db.allocFileNum()
	path := db.tablePath(fileNum)
	w, err := sstable.NewWriter(path, db.opts.BloomFalsePositiveRate)
	if err != nil {
		return err
	}

	it := mt.NewIterator()
	for it.SeekToFirst(); it.Valid(); it.Next() {
		if err := w.Add(it.Key(), it.Value()); err != nil {
			w.Abort()
			return err
		}
	}
	if w.Count() == 0 {
		w.Abort()
		return nil
	}
	if err := w.Finish(); err != nil {
		return err
	}

	r, err := sstable.Open(path)
	if err != nil {
		return err
	}
	meta := tableMeta{
		FileNum:  fileNum,
		Level:    0,
		Smallest: append([]byte(nil), r.Smallest()...),
		Largest:  append([]byte(nil), r.Largest()...),
		Count:    r.Count(),
	}
	if err := db.manifest.append(manifestEdit{
		Added:       []tableMeta{meta},
		NextFileNum: db.nextFileNum,
		LastSeq:     db.lastSeq,
	}); err != nil {
		return err
	}
	db.levels[0] = append(db.levels[0], r)
	return nil
}

// Get returns the value for key as visible to the latest committed sequence.
func (db *DB) Get(key []byte) ([]byte, error) {
	return db.getAt(key, encoding.MaxSequence)
}

// getAt resolves a key at a snapshot sequence by consulting sources newest
// first: the active MemTable, the immutable MemTable, then each level. The
// first source that holds any version of the key (live or tombstone) decides
// the result, because a newer version always shadows older ones.
func (db *DB) getAt(key []byte, snap uint64) ([]byte, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return nil, ErrClosed
	}

	if v, found, ok := db.mem.Get(key, snap); ok {
		return finish(v, found)
	}
	if db.imm != nil {
		if v, found, ok := db.imm.Get(key, snap); ok {
			return finish(v, found)
		}
	}

	// L0 tables can overlap, so scan them newest first (appended last).
	for i := len(db.levels[0]) - 1; i >= 0; i-- {
		if v, found, ok := db.levels[0][i].Get(key, snap); ok {
			return finish(v, found)
		}
	}
	// L1 and below hold non-overlapping tables, so at most one can match.
	for lvl := 1; lvl < numLevels; lvl++ {
		tbls := db.levels[lvl]
		r := db.findTable(tbls, key)
		if r == nil {
			continue
		}
		if v, found, ok := r.Get(key, snap); ok {
			return finish(v, found)
		}
	}
	return nil, ErrNotFound
}

func finish(v []byte, found bool) ([]byte, error) {
	if !found {
		return nil, ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

// findTable binary searches a sorted, non-overlapping level for the table whose
// user-key range contains key.
func (db *DB) findTable(tbls []*sstable.Reader, key []byte) *sstable.Reader {
	lo, hi := 0, len(tbls)
	for lo < hi {
		mid := (lo + hi) / 2
		if encoding.CompareInternal(tbls[mid].Largest(), encoding.MakeInternalKey(key, encoding.MaxSequence, encoding.KindSet)) < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo >= len(tbls) {
		return nil
	}
	t := tbls[lo]
	if encoding.CompareBytes(key, t.Smallest().UserKey()) < 0 {
		return nil
	}
	return t
}

// sortLevels orders each non-L0 level by smallest key so binary search works,
// and orders L0 by file number so newer tables come last.
func (db *DB) sortLevels() {
	for lvl := 1; lvl < numLevels; lvl++ {
		sort.Slice(db.levels[lvl], func(i, j int) bool {
			return encoding.CompareInternal(db.levels[lvl][i].Smallest(), db.levels[lvl][j].Smallest()) < 0
		})
	}
	sort.Slice(db.levels[0], func(i, j int) bool {
		return fileNumOf(db.levels[0][i]) < fileNumOf(db.levels[0][j])
	})
}

// Close flushes the active MemTable, closes the WAL and manifest, and releases
// table handles. After Close the handle is unusable.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil
	}
	db.closed = true

	if db.mem.ApproximateSize() > 0 {
		if err := db.flushMemtableLocked(db.mem); err != nil {
			return err
		}
	}
	if err := db.log.Close(); err != nil {
		return err
	}
	_ = os.Remove(db.logPath(db.logNum))
	return db.manifest.Close()
}

// allocFileNum returns the next monotonic file number.
func (db *DB) allocFileNum() uint64 {
	n := db.nextFileNum
	db.nextFileNum++
	return n
}

func (db *DB) manifestPath() string      { return filepath.Join(db.dir, "MANIFEST") }
func (db *DB) tablePath(n uint64) string { return filepath.Join(db.dir, fmt.Sprintf("%06d.sst", n)) }
func (db *DB) logPath(n uint64) string   { return filepath.Join(db.dir, fmt.Sprintf("%06d.log", n)) }
