package sstable

import (
	"encoding/binary"
	"errors"
	"os"

	"github.com/sarmakska/lsmdb/internal/bloom"
	"github.com/sarmakska/lsmdb/internal/encoding"
)

// ErrBadTable is returned when a file is not a valid lsmdb table.
var ErrBadTable = errors.New("lsmdb: not a valid sstable")

// Reader provides point lookups and ordered iteration over an immutable table.
// The whole file is mapped into memory on open. Tables are bounded by the flush
// size and compaction target, so this keeps the read path allocation-free on
// the hot path while remaining simple.
type Reader struct {
	path     string
	data     []byte
	filter   *bloom.Filter
	index    []indexEntry
	count    int
	smallest encoding.InternalKey
	largest  encoding.InternalKey
}

// Open reads and parses a table file.
func Open(path string) (*Reader, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < footerSize {
		return nil, ErrBadTable
	}
	footer := data[len(data)-footerSize:]
	if binary.LittleEndian.Uint64(footer[48:56]) != magic {
		return nil, ErrBadTable
	}
	bloomHandle := decodeHandle(footer[0:16])
	indexHandle := decodeHandle(footer[16:32])
	propsHandle := decodeHandle(footer[32:48])

	r := &Reader{path: path, data: data}
	r.filter = bloom.Decode(data[bloomHandle.offset : bloomHandle.offset+bloomHandle.length])
	if err := r.parseIndex(data[indexHandle.offset : indexHandle.offset+indexHandle.length]); err != nil {
		return nil, err
	}
	if err := r.parseProps(data[propsHandle.offset : propsHandle.offset+propsHandle.length]); err != nil {
		return nil, err
	}
	return r, nil
}

// parseProps reads the cheap properties block: entry count and key bounds.
func (r *Reader) parseProps(p []byte) error {
	count, n := binary.Uvarint(p)
	if n <= 0 {
		return ErrBadTable
	}
	pos := n
	r.count = int(count)
	slen, n := binary.Uvarint(p[pos:])
	if n <= 0 {
		return ErrBadTable
	}
	pos += n
	r.smallest = append(encoding.InternalKey(nil), p[pos:pos+int(slen)]...)
	pos += int(slen)
	llen, n := binary.Uvarint(p[pos:])
	if n <= 0 {
		return ErrBadTable
	}
	pos += n
	r.largest = append(encoding.InternalKey(nil), p[pos:pos+int(llen)]...)
	return nil
}

func (r *Reader) parseIndex(idx []byte) error {
	n, off := binary.Uvarint(idx)
	if off <= 0 {
		return ErrBadTable
	}
	pos := off
	for i := uint64(0); i < n; i++ {
		klen, k := binary.Uvarint(idx[pos:])
		if k <= 0 {
			return ErrBadTable
		}
		pos += k
		key := idx[pos : pos+int(klen)]
		pos += int(klen)
		handle := decodeHandle(idx[pos : pos+16])
		pos += 16
		r.index = append(r.index, indexEntry{firstKey: key, handle: handle})
	}
	return nil
}

// Smallest returns the smallest internal key in the table.
func (r *Reader) Smallest() encoding.InternalKey { return r.smallest }

// Largest returns the largest internal key in the table.
func (r *Reader) Largest() encoding.InternalKey { return r.largest }

// Count returns the number of entries in the table.
func (r *Reader) Count() int { return r.count }

// Path returns the file path backing this table.
func (r *Reader) Path() string { return r.path }

// MayContain consults the bloom filter for a user key.
func (r *Reader) MayContain(userKey []byte) bool {
	return r.filter.MayContain(userKey)
}

// Get returns the value for userKey visible at snapshot snap. The semantics
// match the MemTable: ok reports whether a version was found at all, found
// reports whether that version is live (a tombstone yields found false, ok
// true). The bloom filter is consulted first to skip absent keys.
func (r *Reader) Get(userKey []byte, snap uint64) (value []byte, found, ok bool) {
	if !r.filter.MayContain(userKey) {
		return nil, false, false
	}
	seekKey := encoding.MakeInternalKey(userKey, snap, encoding.KindSet)
	it := r.NewIterator()
	it.Seek(seekKey)
	if !it.Valid() {
		return nil, false, false
	}
	ik := it.Key()
	if string(ik.UserKey()) != string(userKey) {
		return nil, false, false
	}
	if ik.Kind() == encoding.KindDelete {
		return nil, false, true
	}
	return it.Value(), true, true
}

// Iterator scans a table in ascending internal-key order. It walks the sparse
// index to find the right data block, then decodes entries within it.
type Iterator struct {
	r        *Reader
	blockIdx int
	block    []byte
	pos      int
	key      encoding.InternalKey
	value    []byte
	valid    bool
}

// NewIterator returns a table iterator.
func (r *Reader) NewIterator() *Iterator {
	return &Iterator{r: r}
}

// Valid reports whether the cursor points at an entry.
func (it *Iterator) Valid() bool { return it.valid }

// Key returns the internal key at the cursor.
func (it *Iterator) Key() encoding.InternalKey { return it.key }

// Value returns the value at the cursor.
func (it *Iterator) Value() []byte { return it.value }

func (it *Iterator) loadBlock(i int) bool {
	if i < 0 || i >= len(it.r.index) {
		return false
	}
	h := it.r.index[i].handle
	it.blockIdx = i
	it.block = it.r.data[h.offset : h.offset+h.length]
	it.pos = 0
	return true
}

// decodeCurrent reads the entry at the current position within the loaded block.
func (it *Iterator) decodeCurrent() bool {
	if it.pos >= len(it.block) {
		return false
	}
	klen, n := binary.Uvarint(it.block[it.pos:])
	it.pos += n
	it.key = encoding.InternalKey(it.block[it.pos : it.pos+int(klen)])
	it.pos += int(klen)
	vlen, n := binary.Uvarint(it.block[it.pos:])
	it.pos += n
	it.value = it.block[it.pos : it.pos+int(vlen)]
	it.pos += int(vlen)
	return true
}

// SeekToFirst positions at the first entry of the table.
func (it *Iterator) SeekToFirst() {
	if !it.loadBlock(0) {
		it.valid = false
		return
	}
	it.valid = it.decodeCurrent()
}

// Seek positions at the first entry whose key is greater than or equal to
// target. It binary searches the sparse index for the candidate block, then
// scans forward, advancing across block boundaries if needed.
func (it *Iterator) Seek(target encoding.InternalKey) {
	idx := it.r.index
	// Find the last block whose first key is <= target. Entries after that
	// block start strictly greater, so the target, if present, is in this block
	// or earlier.
	lo, hi := 0, len(idx)
	for lo < hi {
		mid := (lo + hi) / 2
		if encoding.CompareInternal(encoding.InternalKey(idx[mid].firstKey), target) <= 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	start := lo - 1
	if start < 0 {
		start = 0
	}
	if !it.loadBlock(start) {
		it.valid = false
		return
	}
	for {
		if !it.decodeCurrent() {
			// Exhausted this block, move to the next.
			if !it.loadBlock(it.blockIdx + 1) {
				it.valid = false
				return
			}
			continue
		}
		if encoding.CompareInternal(it.key, target) >= 0 {
			it.valid = true
			return
		}
	}
}

// Next advances the cursor, crossing into the next block when the current one is
// exhausted.
func (it *Iterator) Next() {
	if !it.valid {
		return
	}
	if !it.decodeCurrent() {
		if !it.loadBlock(it.blockIdx + 1) {
			it.valid = false
			return
		}
		it.valid = it.decodeCurrent()
	}
}
