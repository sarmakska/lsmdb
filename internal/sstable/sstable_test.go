package sstable

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/sarmakska/lsmdb/internal/encoding"
)

func key(i int) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(i))
	return b
}

// buildTable writes n keys spanning multiple data blocks and returns a reader.
func buildTable(t *testing.T, n int) *Reader {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.sst")
	w, err := NewWriter(path, 0.01)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		ik := encoding.MakeInternalKey(key(i), uint64(i+1), encoding.KindSet)
		val := []byte(fmt.Sprintf("value-%d", i))
		if err := w.Add(ik, val); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestGetRoundTrip checks every written key reads back across block boundaries.
func TestGetRoundTrip(t *testing.T) {
	const n = 5000 // large enough to span many 4 KiB blocks
	r := buildTable(t, n)
	for i := 0; i < n; i++ {
		v, found, ok := r.Get(key(i), encoding.MaxSequence)
		if !ok || !found {
			t.Fatalf("key %d missing (ok=%v found=%v)", i, ok, found)
		}
		want := fmt.Sprintf("value-%d", i)
		if string(v) != want {
			t.Fatalf("key %d = %q, want %q", i, v, want)
		}
	}
}

// TestAbsentKey checks a key never written is reported absent.
func TestAbsentKey(t *testing.T) {
	r := buildTable(t, 1000)
	if _, _, ok := r.Get(key(999999), encoding.MaxSequence); ok {
		t.Fatal("expected absent key to be reported missing")
	}
}

// TestOrderedScan checks the iterator yields keys in ascending order.
func TestOrderedScan(t *testing.T) {
	const n = 3000
	r := buildTable(t, n)
	it := r.NewIterator()
	count := 0
	var prev encoding.InternalKey
	for it.SeekToFirst(); it.Valid(); it.Next() {
		if prev != nil && encoding.CompareInternal(prev, it.Key()) >= 0 {
			t.Fatalf("scan out of order at %d", count)
		}
		prev = append(encoding.InternalKey(nil), it.Key()...)
		count++
	}
	if count != n {
		t.Fatalf("scanned %d keys, want %d", count, n)
	}
}

// TestSeek checks Seek lands on the first key at or after the target.
func TestSeek(t *testing.T) {
	r := buildTable(t, 2000)
	it := r.NewIterator()
	target := encoding.MakeInternalKey(key(1234), encoding.MaxSequence, encoding.KindSet)
	it.Seek(target)
	if !it.Valid() {
		t.Fatal("seek landed past end")
	}
	got := binary.BigEndian.Uint64(it.Key().UserKey())
	if got != 1234 {
		t.Fatalf("seek landed on key %d, want 1234", got)
	}
}

// TestUnsortedRejected checks the writer enforces sorted input.
func TestUnsortedRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.sst")
	w, _ := NewWriter(path, 0.01)
	_ = w.Add(encoding.MakeInternalKey(key(5), 1, encoding.KindSet), []byte("a"))
	if err := w.Add(encoding.MakeInternalKey(key(2), 1, encoding.KindSet), []byte("b")); err != ErrUnsortedKey {
		t.Fatalf("expected ErrUnsortedKey, got %v", err)
	}
	w.Abort()
}
