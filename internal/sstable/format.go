// Package sstable implements the immutable on-disk sorted table format. A table
// is a sequence of data blocks, each holding a run of sorted internal keys, a
// bloom filter covering every key, a sparse index that maps the first key of
// each block to its offset, and a fixed-size footer that ties it together.
//
// File layout:
//
//	[data block 0]
//	[data block 1]
//	...
//	[bloom filter]
//	[index block]      first-key -> block handle, one entry per data block
//	[footer 40B]       bloom handle, index handle, magic
//
// Keys inside a block are length-prefixed and stored whole. This keeps the
// reader simple and the format easy to reason about, at a modest space cost
// relative to prefix-compressed blocks. The sparse index means a Get binary
// searches the index, reads one data block, then scans it.
package sstable

import "encoding/binary"

const (
	// blockTargetSize is the soft cap on a data block before it is flushed.
	blockTargetSize = 4 * 1024
	// footerSize is the fixed footer length: three 16-byte block handles (bloom,
	// index, properties) plus an 8-byte magic number.
	footerSize = 56
	// magic identifies an lsmdb table and guards against reading a truncated or
	// foreign file as a table.
	magic uint64 = 0x6c736d6462303031 // "lsmdb001"
)

// blockHandle points at a region of the file by offset and length.
type blockHandle struct {
	offset uint64
	length uint64
}

func (h blockHandle) encode(dst []byte) {
	binary.LittleEndian.PutUint64(dst[0:8], h.offset)
	binary.LittleEndian.PutUint64(dst[8:16], h.length)
}

func decodeHandle(src []byte) blockHandle {
	return blockHandle{
		offset: binary.LittleEndian.Uint64(src[0:8]),
		length: binary.LittleEndian.Uint64(src[8:16]),
	}
}

// putUvarint appends a varint-encoded length to dst.
func putUvarint(dst []byte, v uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(dst, tmp[:n]...)
}
