package lsmdb

import (
	"encoding/binary"
	"fmt"
	"path/filepath"

	"github.com/sarmakska/lsmdb/internal/encoding"
	"github.com/sarmakska/lsmdb/internal/sstable"
)

// encodeRecord serialises a single mutation for the write-ahead log. The layout
// is: seq (8B), kind (1B), keyLen (varint), key, valueLen (varint), value. The
// WAL framing layer adds the CRC and overall length, so this layer only needs
// to be self-describing for the fields it owns.
func encodeRecord(seq uint64, kind encoding.Kind, key, value []byte) []byte {
	buf := make([]byte, 0, 9+len(key)+len(value)+8)
	var hdr [9]byte
	binary.LittleEndian.PutUint64(hdr[0:8], seq)
	hdr[8] = byte(kind)
	buf = append(buf, hdr[:]...)
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], uint64(len(key)))
	buf = append(buf, tmp[:n]...)
	buf = append(buf, key...)
	n = binary.PutUvarint(tmp[:], uint64(len(value)))
	buf = append(buf, tmp[:n]...)
	buf = append(buf, value...)
	return buf
}

// decodeRecord reverses encodeRecord. ok is false when the record is malformed.
func decodeRecord(rec []byte) (seq uint64, kind encoding.Kind, key, value []byte, ok bool) {
	if len(rec) < 9 {
		return 0, 0, nil, nil, false
	}
	seq = binary.LittleEndian.Uint64(rec[0:8])
	kind = encoding.Kind(rec[8])
	pos := 9
	klen, n := binary.Uvarint(rec[pos:])
	if n <= 0 {
		return 0, 0, nil, nil, false
	}
	pos += n
	if pos+int(klen) > len(rec) {
		return 0, 0, nil, nil, false
	}
	key = rec[pos : pos+int(klen)]
	pos += int(klen)
	vlen, n := binary.Uvarint(rec[pos:])
	if n <= 0 {
		return 0, 0, nil, nil, false
	}
	pos += n
	if pos+int(vlen) > len(rec) {
		return 0, 0, nil, nil, false
	}
	value = rec[pos : pos+int(vlen)]
	return seq, kind, key, value, true
}

// fileNumOf extracts the numeric file id from a table's path, used to order L0
// tables by age.
func fileNumOf(r *sstable.Reader) uint64 {
	base := filepath.Base(r.Path())
	var n uint64
	_, _ = fmt.Sscanf(base, "%06d.sst", &n)
	return n
}
