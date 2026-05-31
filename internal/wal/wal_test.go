package wal

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRoundTrip checks records read back in order, intact.
func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{[]byte("alpha"), []byte("beta"), []byte("gamma")}
	for _, p := range want {
		if err := w.Append(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := 0; ; i++ {
		rec, err := r.Next()
		if err != nil {
			if i != len(want) {
				t.Fatalf("read %d records, want %d", i, len(want))
			}
			break
		}
		if string(rec) != string(want[i]) {
			t.Fatalf("record %d = %q, want %q", i, rec, want[i])
		}
	}
}

// TestTornTailIgnored simulates a crash mid-write by truncating the file inside
// the last record's payload. Replay must return the earlier records and stop
// cleanly at the torn tail.
func TestTornTailIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "torn.log")
	w, _ := Create(path)
	_ = w.Append([]byte("durable-one"))
	_ = w.Append([]byte("durable-two"))
	_ = w.Append([]byte("this-record-will-be-truncated"))
	_ = w.Close()

	// Chop the last few bytes to simulate a partial write.
	fi, _ := os.Stat(path)
	if err := os.Truncate(path, fi.Size()-5); err != nil {
		t.Fatal(err)
	}

	r, _ := Open(path)
	defer r.Close()
	var got []string
	for {
		rec, err := r.Next()
		if err != nil {
			break
		}
		got = append(got, string(rec))
	}
	if len(got) != 2 || got[0] != "durable-one" || got[1] != "durable-two" {
		t.Fatalf("recovered %v, want the two durable records only", got)
	}
}

// TestCorruptCrcStops checks a flipped payload byte stops replay rather than
// returning corrupt data.
func TestCorruptCrcStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crc.log")
	w, _ := Create(path)
	_ = w.Append([]byte("good"))
	_ = w.Append([]byte("corruptme"))
	_ = w.Close()

	data, _ := os.ReadFile(path)
	data[len(data)-1] ^= 0xff // flip the final payload byte
	_ = os.WriteFile(path, data, 0o644)

	r, _ := Open(path)
	defer r.Close()
	rec, err := r.Next()
	if err != nil || string(rec) != "good" {
		t.Fatalf("first record = %q err %v, want good", rec, err)
	}
	if _, err := r.Next(); err == nil {
		t.Fatal("expected replay to stop at the corrupt record")
	}
}
