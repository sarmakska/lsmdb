// Package memtable is the in-memory write buffer of the engine. It wraps a skip
// list keyed by internal keys and answers point lookups that respect MVCC: a
// lookup at sequence S returns the newest version of a key whose sequence is at
// most S.
package memtable

import (
	"github.com/sarmakska/lsmdb/internal/encoding"
	"github.com/sarmakska/lsmdb/internal/skiplist"
)

// MemTable is an ordered, versioned key-value buffer.
type MemTable struct {
	list *skiplist.SkipList
}

// New returns an empty MemTable.
func New() *MemTable {
	return &MemTable{list: skiplist.New()}
}

// Add inserts a versioned entry. kind distinguishes a set from a tombstone.
func (m *MemTable) Add(seq uint64, kind encoding.Kind, userKey, value []byte) {
	ik := encoding.MakeInternalKey(userKey, seq, kind)
	m.list.Insert(ik, value)
}

// ApproximateSize reports the buffer footprint used to decide when to flush.
func (m *MemTable) ApproximateSize() int64 {
	return m.list.Size()
}

// Get returns the value for userKey visible at snapshot sequence snap. The
// boolean ok is false when no version is visible. When the newest visible
// version is a tombstone, found is false and ok is true, letting the caller
// stop searching older tables.
func (m *MemTable) Get(userKey []byte, snap uint64) (value []byte, found, ok bool) {
	// Seek to the newest possible version of this key at or below snap.
	seekKey := encoding.MakeInternalKey(userKey, snap, encoding.KindSet)
	it := m.list.NewIterator()
	it.Seek(seekKey)
	if !it.Valid() {
		return nil, false, false
	}
	ik := it.Key()
	if string(ik.UserKey()) != string(userKey) {
		return nil, false, false
	}
	// The first match at or below snap is the visible version because higher
	// sequences sort first and were skipped by the seek.
	if ik.Kind() == encoding.KindDelete {
		return nil, false, true
	}
	return it.Value(), true, true
}

// NewIterator exposes the underlying ordered iterator for range scans and flush.
func (m *MemTable) NewIterator() *skiplist.Iterator {
	return m.list.NewIterator()
}
