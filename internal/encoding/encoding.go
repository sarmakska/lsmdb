// Package encoding holds the on-disk and in-memory key encoding primitives
// shared across the storage engine. The internal key layout is the backbone
// of MVCC in lsmdb: every user key is paired with a monotonic sequence number
// and a value kind so that newer versions sort ahead of older ones.
package encoding

import (
	"encoding/binary"
	"errors"
)

// Kind distinguishes a live value from a deletion tombstone.
type Kind uint8

const (
	// KindDelete marks a key as deleted at a given sequence number.
	KindDelete Kind = 0
	// KindSet marks a key as holding a live value.
	KindSet Kind = 1
)

// MaxSequence is the largest sequence number a snapshot may carry. A read at
// MaxSequence observes every committed write.
const MaxSequence uint64 = (1 << 56) - 1

// ErrCorruptKey is returned when an internal key cannot be decoded.
var ErrCorruptKey = errors.New("lsmdb: corrupt internal key")

// InternalKey is the sortable representation of a versioned key. On disk it is
// laid out as: user key bytes, followed by an 8-byte trailer. The trailer packs
// the 56-bit sequence number in the high bits and the value kind in the low 8
// bits, stored big-endian so that a plain bytewise comparison orders keys
// correctly. Within one user key, a larger sequence number sorts first, which
// means an iterator naturally lands on the newest version.
type InternalKey []byte

// PackTrailer combines a sequence number and kind into the 8-byte trailer.
func PackTrailer(seq uint64, kind Kind) uint64 {
	return (seq << 8) | uint64(kind)
}

// MakeInternalKey builds an internal key from a user key, sequence and kind.
func MakeInternalKey(userKey []byte, seq uint64, kind Kind) InternalKey {
	ik := make([]byte, len(userKey)+8)
	copy(ik, userKey)
	binary.BigEndian.PutUint64(ik[len(userKey):], PackTrailer(seq, kind))
	return ik
}

// UserKey returns the user-visible portion of an internal key.
func (ik InternalKey) UserKey() []byte {
	if len(ik) < 8 {
		return nil
	}
	return ik[:len(ik)-8]
}

// Trailer returns the packed sequence-and-kind trailer.
func (ik InternalKey) Trailer() uint64 {
	if len(ik) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(ik[len(ik)-8:])
}

// Sequence returns the sequence number encoded in the key.
func (ik InternalKey) Sequence() uint64 {
	return ik.Trailer() >> 8
}

// Kind returns the value kind encoded in the key.
func (ik InternalKey) Kind() Kind {
	return Kind(ik.Trailer() & 0xff)
}

// Valid reports whether the internal key is well formed.
func (ik InternalKey) Valid() bool {
	return len(ik) >= 8
}

// CompareInternal orders two internal keys. User keys are compared first in
// ascending byte order. When user keys are equal the trailer is compared in
// descending order so that the newest version (highest sequence) sorts ahead.
func CompareInternal(a, b InternalKey) int {
	ua, ub := a.UserKey(), b.UserKey()
	if c := compareBytes(ua, ub); c != 0 {
		return c
	}
	ta, tb := a.Trailer(), b.Trailer()
	switch {
	case ta > tb:
		return -1
	case ta < tb:
		return 1
	default:
		return 0
	}
}

// CompareBytes is a bytewise comparator equivalent to bytes.Compare, exported
// for callers that compare user keys directly.
func CompareBytes(a, b []byte) int { return compareBytes(a, b) }

// compareBytes is a small bytewise comparator equivalent to bytes.Compare. It
// is inlined here so the hot comparison path avoids an extra package import.
func compareBytes(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}
