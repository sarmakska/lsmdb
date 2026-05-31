// Package skiplist implements a concurrent-read, single-writer skip list keyed
// by internal keys. It is the in-memory data structure behind the MemTable.
// Skip lists give logarithmic search and insert with simple, lock-light reads,
// which suits an LSM write buffer where one goroutine appends and many readers
// scan.
package skiplist

import (
	"math/rand"
	"sync"

	"github.com/sarmakska/lsmdb/internal/encoding"
)

const (
	maxHeight = 12
	branching = 4 // 1-in-4 probability of promoting a node a level up
)

type node struct {
	key  encoding.InternalKey
	next []*node // length is the node's height
}

// SkipList is an ordered map from internal keys to nothing: the value travels
// inside the internal key trailer and an associated value slice stored beside
// the key. To keep the structure compact the value is appended to the node.
type SkipList struct {
	mu     sync.RWMutex
	head   *node
	height int
	rng    *rand.Rand
	size   int64 // approximate bytes of keys plus values, for flush sizing
	values map[*node][]byte
}

// New returns an empty skip list.
func New() *SkipList {
	return &SkipList{
		head:   &node{next: make([]*node, maxHeight)},
		height: 1,
		rng:    rand.New(rand.NewSource(0xC0FFEE)),
		values: make(map[*node][]byte),
	}
}

func (s *SkipList) randomHeight() int {
	h := 1
	for h < maxHeight && s.rng.Intn(branching) == 0 {
		h++
	}
	return h
}

// Size returns the approximate memory footprint in bytes.
func (s *SkipList) Size() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.size
}

// findGreaterOrEqual locates the first node whose key is greater than or equal
// to the target, recording the predecessors at each level into prev for splice.
func (s *SkipList) findGreaterOrEqual(key encoding.InternalKey, prev []*node) *node {
	x := s.head
	level := s.height - 1
	for {
		nxt := x.next[level]
		if nxt != nil && encoding.CompareInternal(nxt.key, key) < 0 {
			x = nxt
			continue
		}
		if prev != nil {
			prev[level] = x
		}
		if level == 0 {
			return nxt
		}
		level--
	}
}

// Insert adds an internal key with its value. The caller guarantees keys are
// unique (a fresh sequence number per write makes this hold), so Insert always
// splices a new node rather than overwriting.
func (s *SkipList) Insert(key encoding.InternalKey, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	prev := make([]*node, maxHeight)
	s.findGreaterOrEqual(key, prev)

	h := s.randomHeight()
	if h > s.height {
		for i := s.height; i < h; i++ {
			prev[i] = s.head
		}
		s.height = h
	}

	n := &node{key: key, next: make([]*node, h)}
	for i := 0; i < h; i++ {
		n.next[i] = prev[i].next[i]
		prev[i].next[i] = n
	}
	s.values[n] = value
	s.size += int64(len(key) + len(value) + 8)
}

// Iterator walks the skip list in ascending internal-key order.
type Iterator struct {
	list *SkipList
	n    *node
}

// NewIterator returns an iterator positioned before the first element.
func (s *SkipList) NewIterator() *Iterator {
	return &Iterator{list: s}
}

// Valid reports whether the iterator is positioned at a live node.
func (it *Iterator) Valid() bool { return it.n != nil }

// Key returns the internal key at the cursor.
func (it *Iterator) Key() encoding.InternalKey { return it.n.key }

// Value returns the value at the cursor.
func (it *Iterator) Value() []byte {
	it.list.mu.RLock()
	defer it.list.mu.RUnlock()
	return it.list.values[it.n]
}

// SeekToFirst moves to the smallest key.
func (it *Iterator) SeekToFirst() {
	it.list.mu.RLock()
	defer it.list.mu.RUnlock()
	it.n = it.list.head.next[0]
}

// Seek positions at the first key greater than or equal to target.
func (it *Iterator) Seek(target encoding.InternalKey) {
	it.list.mu.RLock()
	defer it.list.mu.RUnlock()
	it.n = it.list.findGreaterOrEqual(target, nil)
}

// Next advances the cursor by one.
func (it *Iterator) Next() {
	it.list.mu.RLock()
	defer it.list.mu.RUnlock()
	if it.n != nil {
		it.n = it.n.next[0]
	}
}
