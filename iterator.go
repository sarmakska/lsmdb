package lsmdb

import (
	"container/heap"

	"github.com/sarmakska/lsmdb/internal/encoding"
)

// internalIterator is the common shape of every source the merging iterator
// pulls from: the MemTables and each SSTable. All sources yield internal keys
// in ascending order.
type internalIterator interface {
	Valid() bool
	Key() encoding.InternalKey
	Value() []byte
	Next()
	SeekToFirst()
	Seek(target encoding.InternalKey)
}

// mergingIterator merges several sorted internal-key streams into one. It uses a
// min-heap keyed by the internal-key comparator, so the overall stream is
// globally sorted: across user keys ascending, and within a user key newest
// version first. The user-facing Iterator sits on top and collapses versions.
type mergingIterator struct {
	iters []internalIterator
	h     *iterHeap
}

func newMergingIterator(iters []internalIterator) *mergingIterator {
	return &mergingIterator{iters: iters, h: &iterHeap{}}
}

func (m *mergingIterator) SeekToFirst() {
	m.h = &iterHeap{}
	for _, it := range m.iters {
		it.SeekToFirst()
		if it.Valid() {
			m.h.items = append(m.h.items, it)
		}
	}
	heap.Init(m.h)
}

func (m *mergingIterator) Seek(target encoding.InternalKey) {
	m.h = &iterHeap{}
	for _, it := range m.iters {
		it.Seek(target)
		if it.Valid() {
			m.h.items = append(m.h.items, it)
		}
	}
	heap.Init(m.h)
}

func (m *mergingIterator) Valid() bool { return m.h.Len() > 0 }

func (m *mergingIterator) Key() encoding.InternalKey { return m.h.items[0].Key() }

func (m *mergingIterator) Value() []byte { return m.h.items[0].Value() }

func (m *mergingIterator) Next() {
	top := m.h.items[0]
	top.Next()
	if top.Valid() {
		heap.Fix(m.h, 0)
	} else {
		heap.Pop(m.h)
	}
}

// iterHeap orders sources by their current internal key.
type iterHeap struct {
	items []internalIterator
}

func (h iterHeap) Len() int { return len(h.items) }
func (h iterHeap) Less(i, j int) bool {
	return encoding.CompareInternal(h.items[i].Key(), h.items[j].Key()) < 0
}
func (h iterHeap) Swap(i, j int) { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *iterHeap) Push(x any)   { h.items = append(h.items, x.(internalIterator)) }
func (h *iterHeap) Pop() any {
	old := h.items
	n := len(old)
	x := old[n-1]
	h.items = old[:n-1]
	return x
}
