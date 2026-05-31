package lsmdb

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func mustOpen(t *testing.T, dir string, opts Options) *DB {
	t.Helper()
	db, err := Open(dir, opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db
}

// TestPutGetDelete exercises the basic API on the MemTable path.
func TestPutGetDelete(t *testing.T) {
	db := mustOpen(t, t.TempDir(), Options{})
	defer db.Close()

	if err := db.Put([]byte("name"), []byte("sarma")); err != nil {
		t.Fatal(err)
	}
	v, err := db.Get([]byte("name"))
	if err != nil || string(v) != "sarma" {
		t.Fatalf("get = %q err %v", v, err)
	}
	if err := db.Delete([]byte("name")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Get([]byte("name")); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

// TestRoundTripAcrossFlush writes enough data to force several flushes and then
// reads every key back, exercising the MemTable plus L0 read path.
func TestRoundTripAcrossFlush(t *testing.T) {
	dir := t.TempDir()
	// Small MemTable so a few hundred writes trigger flushes.
	db := mustOpen(t, dir, Options{MemTableSize: 16 * 1024})
	const n = 3000
	for i := 0; i < n; i++ {
		k := []byte(fmt.Sprintf("key-%06d", i))
		v := []byte(fmt.Sprintf("val-%06d", i))
		if err := db.Put(k, v); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		k := []byte(fmt.Sprintf("key-%06d", i))
		v, err := db.Get(k)
		if err != nil {
			t.Fatalf("missing %s: %v", k, err)
		}
		if want := fmt.Sprintf("val-%06d", i); string(v) != want {
			t.Fatalf("key %s = %q, want %q", k, v, want)
		}
	}
	db.Close()
}

// TestDurabilityAndRecovery is the crash-recovery test. It writes committed
// data, then simulates a crash by abandoning the handle without Close (the WAL
// is already synced per write). A fresh Open must recover every committed key.
func TestDurabilityAndRecovery(t *testing.T) {
	dir := t.TempDir()
	db := mustOpen(t, dir, Options{MemTableSize: 1 << 30}) // no flush, all in WAL
	const n = 2000
	for i := 0; i < n; i++ {
		k := []byte(fmt.Sprintf("k%05d", i))
		if err := db.Put(k, []byte(fmt.Sprintf("v%05d", i))); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a crash: do NOT call Close. The data lives only in the synced
	// WAL, not yet flushed to any SSTable.
	db = nil

	// Reopen and verify recovery replayed every committed write.
	db2 := mustOpen(t, dir, Options{})
	defer db2.Close()
	for i := 0; i < n; i++ {
		k := []byte(fmt.Sprintf("k%05d", i))
		v, err := db2.Get(k)
		if err != nil {
			t.Fatalf("recovery lost key %s: %v", k, err)
		}
		if want := fmt.Sprintf("v%05d", i); string(v) != want {
			t.Fatalf("recovered %s = %q, want %q", k, v, want)
		}
	}
}

// TestRecoveryDropsTornTail writes good records, then appends a corrupt trailing
// record directly to the WAL to mimic a half-written entry left by a crash.
// Recovery must keep the good records and drop the torn tail.
func TestRecoveryDropsTornTail(t *testing.T) {
	dir := t.TempDir()
	db := mustOpen(t, dir, Options{MemTableSize: 1 << 30})
	_ = db.Put([]byte("good1"), []byte("v1"))
	_ = db.Put([]byte("good2"), []byte("v2"))
	logNum := db.logNum
	// Abandon without Close to keep the WAL file in place.

	logPath := filepath.Join(dir, fmt.Sprintf("%06d.log", logNum))
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	// Append garbage that cannot form a valid CRC-checked record.
	_, _ = f.Write([]byte{0xde, 0xad, 0xbe, 0xef, 0x10, 0x00, 0x00, 0x00, 0x01, 0x02})
	f.Close()

	db2 := mustOpen(t, dir, Options{})
	defer db2.Close()
	if v, err := db2.Get([]byte("good1")); err != nil || string(v) != "v1" {
		t.Fatalf("good1 = %q err %v", v, err)
	}
	if v, err := db2.Get([]byte("good2")); err != nil || string(v) != "v2" {
		t.Fatalf("good2 = %q err %v", v, err)
	}
}

// TestCompactionCorrectnessAndReclamation forces many flushes and compactions,
// overwrites and deletes keys, then verifies the final visible state is correct
// and that compaction reclaimed superseded versions (fewer files than flushes).
func TestCompactionCorrectnessAndReclamation(t *testing.T) {
	dir := t.TempDir()
	db := mustOpen(t, dir, Options{
		MemTableSize:        8 * 1024,
		L0CompactionTrigger: 3,
	})

	const n = 1500
	// Write each key three times so compaction has superseded versions to drop.
	for round := 0; round < 3; round++ {
		for i := 0; i < n; i++ {
			k := []byte(fmt.Sprintf("key-%05d", i))
			v := []byte(fmt.Sprintf("round-%d-val-%05d", round, i))
			if err := db.Put(k, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Delete a quarter of the keys.
	for i := 0; i < n; i += 4 {
		if err := db.Delete([]byte(fmt.Sprintf("key-%05d", i))); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	db2 := mustOpen(t, dir, Options{MemTableSize: 8 * 1024, L0CompactionTrigger: 3})
	defer db2.Close()

	for i := 0; i < n; i++ {
		k := []byte(fmt.Sprintf("key-%05d", i))
		v, err := db2.Get(k)
		if i%4 == 0 {
			if err != ErrNotFound {
				t.Fatalf("deleted key %s still present: %q", k, v)
			}
			continue
		}
		if err != nil {
			t.Fatalf("key %s lost: %v", k, err)
		}
		want := fmt.Sprintf("round-2-val-%05d", i)
		if string(v) != want {
			t.Fatalf("key %s = %q, want latest %q", k, v, want)
		}
	}

	// Space reclamation: total live entries across all levels must be far below
	// the raw number of writes (3*n puts plus n/4 deletes), proving superseded
	// versions were dropped during compaction.
	var liveEntries int
	for lvl := 0; lvl < numLevels; lvl++ {
		for _, tbl := range db2.levels[lvl] {
			liveEntries += tbl.Count()
		}
	}
	rawWrites := 3*n + n/4
	if liveEntries >= rawWrites {
		t.Fatalf("no space reclaimed: %d live entries vs %d raw writes", liveEntries, rawWrites)
	}
	t.Logf("reclamation: %d live entries from %d raw writes", liveEntries, rawWrites)
}

// TestMVCCSnapshotIsolation checks a snapshot taken before later writes never
// observes those writes, and observes deletes as the pre-delete value.
func TestMVCCSnapshotIsolation(t *testing.T) {
	db := mustOpen(t, t.TempDir(), Options{})
	defer db.Close()

	_ = db.Put([]byte("x"), []byte("v1"))
	_ = db.Put([]byte("y"), []byte("y1"))

	snap := db.Snapshot()

	// Mutate after the snapshot.
	_ = db.Put([]byte("x"), []byte("v2"))
	_ = db.Delete([]byte("y"))
	_ = db.Put([]byte("z"), []byte("z1"))

	if v, err := snap.Get([]byte("x")); err != nil || string(v) != "v1" {
		t.Fatalf("snapshot x = %q err %v, want v1", v, err)
	}
	if v, err := snap.Get([]byte("y")); err != nil || string(v) != "y1" {
		t.Fatalf("snapshot y = %q err %v, want y1", v, err)
	}
	if _, err := snap.Get([]byte("z")); err != ErrNotFound {
		t.Fatalf("snapshot should not see z, got err %v", err)
	}

	// The live view sees the latest state.
	if v, _ := db.Get([]byte("x")); string(v) != "v2" {
		t.Fatalf("live x = %q, want v2", v)
	}
	if _, err := db.Get([]byte("y")); err != ErrNotFound {
		t.Fatalf("live y should be deleted, got %v", err)
	}
}

// TestOrderedRangeScanAcrossLevels writes data that ends up spread across the
// MemTable, L0 and deeper levels, then checks a full scan returns every live
// key in sorted order with the newest value, skipping deletes.
func TestOrderedRangeScanAcrossLevels(t *testing.T) {
	dir := t.TempDir()
	db := mustOpen(t, dir, Options{MemTableSize: 4 * 1024, L0CompactionTrigger: 2})

	const n = 1500
	for i := 0; i < n; i++ {
		k := []byte(fmt.Sprintf("k%05d", i))
		_ = db.Put(k, []byte(fmt.Sprintf("old-%05d", i)))
	}
	// Overwrite evens, delete multiples of five.
	for i := 0; i < n; i += 2 {
		_ = db.Put([]byte(fmt.Sprintf("k%05d", i)), []byte(fmt.Sprintf("new-%05d", i)))
	}
	for i := 0; i < n; i += 5 {
		_ = db.Delete([]byte(fmt.Sprintf("k%05d", i)))
	}

	it := db.NewIterator()
	var prev []byte
	seen := 0
	for it.SeekToFirst(); it.Valid(); it.Next() {
		k := append([]byte(nil), it.Key()...)
		if prev != nil && bytes.Compare(prev, k) >= 0 {
			t.Fatalf("scan out of order: %s after %s", k, prev)
		}
		prev = k

		idx := -1
		fmt.Sscanf(string(k), "k%05d", &idx)
		if idx%5 == 0 {
			t.Fatalf("deleted key %s appeared in scan", k)
		}
		var want string
		if idx%2 == 0 {
			want = fmt.Sprintf("new-%05d", idx)
		} else {
			want = fmt.Sprintf("old-%05d", idx)
		}
		if string(it.Value()) != want {
			t.Fatalf("key %s = %q, want %q", k, it.Value(), want)
		}
		seen++
	}

	// Expected count: all keys minus those divisible by 5.
	expected := 0
	for i := 0; i < n; i++ {
		if i%5 != 0 {
			expected++
		}
	}
	if seen != expected {
		t.Fatalf("scan saw %d live keys, want %d", seen, expected)
	}
	db.Close()
}

// TestSeekIterator checks the iterator can position at an arbitrary key.
func TestSeekIterator(t *testing.T) {
	db := mustOpen(t, t.TempDir(), Options{MemTableSize: 4 * 1024})
	defer db.Close()
	for i := 0; i < 1000; i++ {
		_ = db.Put([]byte(fmt.Sprintf("k%04d", i)), []byte("v"))
	}
	it := db.NewIterator()
	it.Seek([]byte("k0500"))
	if !it.Valid() || string(it.Key()) != "k0500" {
		t.Fatalf("seek landed on %q, want k0500", it.Key())
	}
}
