package bloom

import (
	"encoding/binary"
	"testing"
)

// TestNoFalseNegatives checks the defining property of a bloom filter: a key
// that was added is always reported present.
func TestNoFalseNegatives(t *testing.T) {
	b := NewBuilder(0.01)
	keys := make([][]byte, 10000)
	for i := range keys {
		k := make([]byte, 8)
		binary.BigEndian.PutUint64(k, uint64(i))
		keys[i] = k
		b.Add(k)
	}
	f := b.Build()
	for _, k := range keys {
		if !f.MayContain(k) {
			t.Fatalf("false negative for key %x", k)
		}
	}
}

// TestFalsePositiveBound checks the observed false positive rate stays within a
// small multiple of the configured target. Probing keys never added, we expect
// roughly one percent positives and assert well under three percent.
func TestFalsePositiveBound(t *testing.T) {
	const n = 20000
	b := NewBuilder(0.01)
	for i := 0; i < n; i++ {
		k := make([]byte, 8)
		binary.BigEndian.PutUint64(k, uint64(i))
		b.Add(k)
	}
	f := b.Build()

	var falsePositives int
	const probes = 20000
	for i := n; i < n+probes; i++ {
		k := make([]byte, 8)
		binary.BigEndian.PutUint64(k, uint64(i))
		if f.MayContain(k) {
			falsePositives++
		}
	}
	rate := float64(falsePositives) / float64(probes)
	if rate > 0.03 {
		t.Fatalf("false positive rate %.4f exceeds bound 0.03", rate)
	}
	t.Logf("observed false positive rate %.4f for target 0.01", rate)
}

// TestEncodeRoundTrip checks a filter survives serialisation unchanged.
func TestEncodeRoundTrip(t *testing.T) {
	b := NewBuilder(0.01)
	for i := 0; i < 1000; i++ {
		k := make([]byte, 4)
		binary.BigEndian.PutUint32(k, uint32(i))
		b.Add(k)
	}
	f := b.Build()
	g := Decode(f.Encode())
	for i := 0; i < 1000; i++ {
		k := make([]byte, 4)
		binary.BigEndian.PutUint32(k, uint32(i))
		if !g.MayContain(k) {
			t.Fatalf("decoded filter lost key %x", k)
		}
	}
}
