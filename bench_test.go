package lsmdb

import (
	"fmt"
	"testing"
)

// BenchmarkPutSync measures write throughput with the default durability
// setting, where every Put fsyncs the write-ahead log before returning. This is
// the honest cost of a crash-safe write and is dominated by the fsync latency
// of the underlying device.
func BenchmarkPutSync(b *testing.B) {
	db, err := Open(b.TempDir(), Options{MemTableSize: 8 * 1024 * 1024})
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	val := make([]byte, 100)
	b.ResetTimer()
	b.SetBytes(int64(len(val) + 16))
	for i := 0; i < b.N; i++ {
		key := []byte(fmt.Sprintf("key-%012d", i))
		if err := db.Put(key, val); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGetMemTable measures point-read throughput when the working set fits
// in the MemTable, the fastest read path.
func BenchmarkGetMemTable(b *testing.B) {
	db, err := Open(b.TempDir(), Options{MemTableSize: 64 * 1024 * 1024})
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	const n = 20000
	for i := 0; i < n; i++ {
		_ = db.Put([]byte(fmt.Sprintf("key-%012d", i)), []byte("value"))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := []byte(fmt.Sprintf("key-%012d", i%n))
		if _, err := db.Get(key); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGetSSTable measures point-read throughput when data has been flushed
// to SSTables, exercising the bloom filter and block index on every lookup.
func BenchmarkGetSSTable(b *testing.B) {
	dir := b.TempDir()
	db, err := Open(dir, Options{MemTableSize: 64 * 1024})
	if err != nil {
		b.Fatal(err)
	}
	const n = 20000
	for i := 0; i < n; i++ {
		_ = db.Put([]byte(fmt.Sprintf("key-%012d", i)), []byte("value"))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := []byte(fmt.Sprintf("key-%012d", i%n))
		if _, err := db.Get(key); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	db.Close()
}
