// Package bloom implements a classic bloom filter tuned for SSTable membership
// checks. Each SSTable carries one filter so that a Get can skip a table whose
// filter reports the key as definitely absent, avoiding a block read and the
// disk seek behind it.
package bloom

import (
	"encoding/binary"
	"math"
)

// Filter is an immutable, serialisable bloom filter. The bit array is stored as
// a byte slice with the number of hash probes appended as a trailing byte, so a
// reader can reconstruct the filter without separate metadata.
type Filter struct {
	bits []byte
	k    uint32 // number of hash probes
}

// Builder accumulates hashes and produces a Filter sized for a target false
// positive rate.
type Builder struct {
	hashes     []uint64
	bitsPerKey int
	probeCount uint32
}

// bitsPerKeyForRate returns the bits-per-key needed to achieve the requested
// false positive probability. The standard result m/n = -ln(p)/ln(2)^2.
func bitsPerKeyForRate(p float64) int {
	if p <= 0 {
		p = 1e-9
	}
	bits := math.Ceil(-math.Log(p) / (math.Ln2 * math.Ln2))
	if bits < 1 {
		bits = 1
	}
	return int(bits)
}

// optimalProbes returns the probe count k = (m/n) * ln(2), the value that
// minimises the false positive rate for a given bits-per-key.
func optimalProbes(bitsPerKey int) uint32 {
	k := uint32(math.Round(float64(bitsPerKey) * math.Ln2))
	if k < 1 {
		k = 1
	}
	if k > 30 {
		k = 30
	}
	return k
}

// NewBuilder creates a Builder targeting the supplied false positive rate, for
// example 0.01 for one percent.
func NewBuilder(falsePositiveRate float64) *Builder {
	bpk := bitsPerKeyForRate(falsePositiveRate)
	return &Builder{
		bitsPerKey: bpk,
		probeCount: optimalProbes(bpk),
	}
}

// Add records a key. Only the hash is retained, never the key bytes.
func (b *Builder) Add(key []byte) {
	b.hashes = append(b.hashes, hash(key))
}

// Reset clears accumulated keys so the builder can be reused for the next table.
func (b *Builder) Reset() {
	b.hashes = b.hashes[:0]
}

// Build materialises the filter. The double-hashing scheme derives k probe
// positions from two 32-bit halves of a single 64-bit hash, which is both fast
// and statistically sound for bloom filters.
func (b *Builder) Build() *Filter {
	n := len(b.hashes)
	if n == 0 {
		return &Filter{bits: []byte{0}, k: b.probeCount}
	}
	nbits := uint32(n * b.bitsPerKey)
	if nbits < 64 {
		nbits = 64
	}
	// Round up to a whole number of bytes.
	nbytes := (nbits + 7) / 8
	nbits = nbytes * 8
	bits := make([]byte, nbytes)
	for _, h := range b.hashes {
		h1 := uint32(h)
		h2 := uint32(h >> 32)
		for i := uint32(0); i < b.probeCount; i++ {
			pos := (h1 + i*h2) % nbits
			bits[pos/8] |= 1 << (pos % 8)
		}
	}
	return &Filter{bits: bits, k: b.probeCount}
}

// MayContain reports whether the key might be present. A false result is
// definitive: the key was never added. A true result may be a false positive.
func (f *Filter) MayContain(key []byte) bool {
	if len(f.bits) == 0 {
		return true
	}
	nbits := uint32(len(f.bits)) * 8
	h := hash(key)
	h1 := uint32(h)
	h2 := uint32(h >> 32)
	for i := uint32(0); i < f.k; i++ {
		pos := (h1 + i*h2) % nbits
		if f.bits[pos/8]&(1<<(pos%8)) == 0 {
			return false
		}
	}
	return true
}

// Encode serialises the filter: the bit array followed by a 4-byte probe count.
func (f *Filter) Encode() []byte {
	out := make([]byte, len(f.bits)+4)
	copy(out, f.bits)
	binary.LittleEndian.PutUint32(out[len(f.bits):], f.k)
	return out
}

// Decode reconstructs a filter from its encoded form.
func Decode(data []byte) *Filter {
	if len(data) < 4 {
		return &Filter{bits: []byte{0}, k: 1}
	}
	k := binary.LittleEndian.Uint32(data[len(data)-4:])
	bits := make([]byte, len(data)-4)
	copy(bits, data[:len(data)-4])
	return &Filter{bits: bits, k: k}
}

// hash is a 64-bit FNV-1a variant. It is deterministic across processes, which
// matters because filters are written to disk and read back later.
func hash(key []byte) uint64 {
	const (
		offset = 1469598103934665603
		prime  = 1099511628211
	)
	h := uint64(offset)
	for _, c := range key {
		h ^= uint64(c)
		h *= prime
	}
	// Final mix to spread the low bits, which the double-hashing scheme relies
	// on for an even distribution of probe positions.
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	return h
}
