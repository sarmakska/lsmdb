package sstable

import (
	"bufio"
	"encoding/binary"
	"errors"
	"os"

	"github.com/sarmakska/lsmdb/internal/bloom"
	"github.com/sarmakska/lsmdb/internal/encoding"
)

// ErrUnsortedKey is returned when keys are added out of order. The writer
// requires a globally sorted stream because the format relies on it.
var ErrUnsortedKey = errors.New("lsmdb: sstable keys added out of order")

// Writer builds a single SSTable. Keys must be added in ascending internal-key
// order, which the flush and compaction paths guarantee by feeding from a
// merging iterator.
type Writer struct {
	f          *os.File
	w          *bufio.Writer
	offset     uint64
	block      []byte // current data block buffer
	index      []indexEntry
	pendingKey []byte // first key of the current block, for the index
	lastKey    []byte
	bloom      *bloom.Builder
	count      int
	firstKey   []byte
}

type indexEntry struct {
	firstKey []byte
	handle   blockHandle
}

// NewWriter creates a table file at path with the given bloom false positive
// rate.
func NewWriter(path string, falsePositiveRate float64) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Writer{
		f:     f,
		w:     bufio.NewWriter(f),
		bloom: bloom.NewBuilder(falsePositiveRate),
	}, nil
}

// Add appends one internal key and its value. Entries within a block are framed
// as: varint(keyLen) key varint(valueLen) value.
func (w *Writer) Add(key encoding.InternalKey, value []byte) error {
	if w.lastKey != nil && encoding.CompareInternal(key, w.lastKey) <= 0 {
		return ErrUnsortedKey
	}
	if len(w.block) == 0 {
		w.pendingKey = append([]byte(nil), key...)
	}
	if w.firstKey == nil {
		w.firstKey = append([]byte(nil), key...)
	}
	w.block = putUvarint(w.block, uint64(len(key)))
	w.block = append(w.block, key...)
	w.block = putUvarint(w.block, uint64(len(value)))
	w.block = append(w.block, value...)

	w.bloom.Add(key.UserKey())
	w.lastKey = append(w.lastKey[:0], key...)
	w.count++

	if len(w.block) >= blockTargetSize {
		return w.flushBlock()
	}
	return nil
}

func (w *Writer) flushBlock() error {
	if len(w.block) == 0 {
		return nil
	}
	handle := blockHandle{offset: w.offset, length: uint64(len(w.block))}
	if _, err := w.w.Write(w.block); err != nil {
		return err
	}
	w.offset += uint64(len(w.block))
	w.index = append(w.index, indexEntry{
		firstKey: append([]byte(nil), w.pendingKey...),
		handle:   handle,
	})
	w.block = w.block[:0]
	w.pendingKey = nil
	return nil
}

// Count returns the number of entries written so far.
func (w *Writer) Count() int { return w.count }

// Finish flushes the final data block, then writes the bloom filter, the index
// block and the footer, and syncs the file to disk.
func (w *Writer) Finish() error {
	if err := w.flushBlock(); err != nil {
		return err
	}

	// Bloom filter region.
	bloomBytes := w.bloom.Build().Encode()
	bloomHandle := blockHandle{offset: w.offset, length: uint64(len(bloomBytes))}
	if _, err := w.w.Write(bloomBytes); err != nil {
		return err
	}
	w.offset += uint64(len(bloomBytes))

	// Index block: count, then per entry varint(keyLen) key offset length.
	var idx []byte
	idx = putUvarint(idx, uint64(len(w.index)))
	for _, e := range w.index {
		idx = putUvarint(idx, uint64(len(e.firstKey)))
		idx = append(idx, e.firstKey...)
		var h [16]byte
		e.handle.encode(h[:])
		idx = append(idx, h[:]...)
	}
	indexHandle := blockHandle{offset: w.offset, length: uint64(len(idx))}
	if _, err := w.w.Write(idx); err != nil {
		return err
	}
	w.offset += uint64(len(idx))

	// Properties block: count, smallest key, largest key. Storing these lets a
	// reader learn a table's bounds and size without scanning every entry on
	// open, which keeps Open and compaction cheap.
	var props []byte
	props = putUvarint(props, uint64(w.count))
	if w.firstKey == nil {
		w.firstKey = []byte{}
	}
	if w.lastKey == nil {
		w.lastKey = []byte{}
	}
	props = putUvarint(props, uint64(len(w.firstKey)))
	props = append(props, w.firstKey...)
	props = putUvarint(props, uint64(len(w.lastKey)))
	props = append(props, w.lastKey...)
	propsHandle := blockHandle{offset: w.offset, length: uint64(len(props))}
	if _, err := w.w.Write(props); err != nil {
		return err
	}
	w.offset += uint64(len(props))

	// Footer.
	var footer [footerSize]byte
	bloomHandle.encode(footer[0:16])
	indexHandle.encode(footer[16:32])
	propsHandle.encode(footer[32:48])
	binary.LittleEndian.PutUint64(footer[48:56], magic)
	if _, err := w.w.Write(footer[:]); err != nil {
		return err
	}

	if err := w.w.Flush(); err != nil {
		return err
	}
	if err := w.f.Sync(); err != nil {
		return err
	}
	return w.f.Close()
}

// Abort closes and removes a partially written file.
func (w *Writer) Abort() {
	name := w.f.Name()
	_ = w.f.Close()
	_ = os.Remove(name)
}
