package lsmdb_test

import (
	"fmt"
	"os"

	"github.com/sarmakska/lsmdb"
)

// Example demonstrates the full public API: open, write, snapshot, mutate and
// range scan. It is run as part of the test suite, so the output is verified.
func Example() {
	dir, _ := os.MkdirTemp("", "lsmdb-example-*")
	defer os.RemoveAll(dir)

	db, err := lsmdb.Open(dir, lsmdb.Options{})
	if err != nil {
		panic(err)
	}
	defer db.Close()

	_ = db.Put([]byte("apple"), []byte("red"))
	_ = db.Put([]byte("banana"), []byte("yellow"))
	_ = db.Put([]byte("cherry"), []byte("dark red"))

	// A snapshot freezes the view at this point in time.
	snap := db.Snapshot()
	_ = db.Put([]byte("apple"), []byte("green"))

	live, _ := db.Get([]byte("apple"))
	frozen, _ := snap.Get([]byte("apple"))
	fmt.Printf("live apple: %s\n", live)
	fmt.Printf("snapshot apple: %s\n", frozen)

	fmt.Println("ordered scan:")
	it := db.NewIterator()
	for it.SeekToFirst(); it.Valid(); it.Next() {
		fmt.Printf("  %s = %s\n", it.Key(), it.Value())
	}

	// Output:
	// live apple: green
	// snapshot apple: red
	// ordered scan:
	//   apple = green
	//   banana = yellow
	//   cherry = dark red
}
