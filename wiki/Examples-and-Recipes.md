# Examples and Recipes

Copy-paste patterns for using lsmdb. Each one is built only from the seven-method
public API ([API-Reference](API-Reference)) and runs against the engine as
shipped. The verified runnable example is `example_test.go`; the end-to-end driver
is `cmd/lsmdb-demo/main.go`.

## Open, write, read, close

```go
db, err := lsmdb.Open("./data", lsmdb.Options{})
if err != nil {
    log.Fatal(err)
}
defer db.Close()

if err := db.Put([]byte("greeting"), []byte("hello")); err != nil {
    log.Fatal(err)
}

v, err := db.Get([]byte("greeting"))
if err != nil {
    log.Fatal(err)
}
fmt.Printf("%s\n", v) // hello
```

`defer db.Close()` is not optional: `Close` flushes the active MemTable to an
SSTable, so without it the last unflushed writes survive only in the WAL and are
recovered (correctly) on the next open, but you skip the clean shutdown.

## Distinguishing absent from error

```go
v, err := db.Get(key)
switch {
case errors.Is(err, lsmdb.ErrNotFound):
    // the key has no live version (never written, or deleted)
case err != nil:
    log.Fatal(err) // a real failure
default:
    use(v)
}
```

Always use `errors.Is`; do not compare with `==` in case the error is ever
wrapped.

## Delete, and the empty-value distinction

```go
db.Put([]byte("k"), []byte("v"))
db.Delete([]byte("k"))
_, err := db.Get([]byte("k")) // ErrNotFound

db.Put([]byte("e"), nil)      // a LIVE, empty value
v, err := db.Get([]byte("e")) // err == nil, len(v) == 0
```

`Delete` writes a tombstone; `Put(key, nil)` writes a present empty value. They
read back differently. See [Internal-Key-and-MVCC](Internal-Key-and-MVCC).

## Snapshot isolation

```go
db.Put([]byte("apple"), []byte("red"))

snap := db.Snapshot()                  // freeze the view here
db.Put([]byte("apple"), []byte("green")) // a later write

live, _ := db.Get([]byte("apple"))     // green
old, _  := snap.Get([]byte("apple"))   // red
```

The snapshot keeps reading `red` no matter how many times the live key changes.
This is the verified `Example` in `example_test.go`. Keep snapshots short-lived;
they do not pin storage (see [Troubleshooting](Troubleshooting)).

## Full ordered scan

```go
it := db.NewIterator()
for it.SeekToFirst(); it.Valid(); it.Next() {
    fmt.Printf("%s = %s\n", it.Key(), it.Value())
}
```

Keys come back in ascending byte order, each once, with the newest visible value,
skipping deleted keys. `Key()` and `Value()` return slices owned by the iterator;
copy them if you keep them past the next `Next`.

## Range scan over a bound

Pass the half-open interval straight to `NewIteratorWith`. The iterator seeks to
the inclusive `LowerBound` and stops at the exclusive `UpperBound`, so you do not
write the bound check by hand:

```go
it := db.NewIteratorWith(lsmdb.IterOptions{
    LowerBound: []byte("user:0000"),
    UpperBound: []byte("user:0100"),
})
for it.SeekToFirst(); it.Valid(); it.Next() {
    process(it.Key(), it.Value())
}
```

Either bound may be omitted: leave `UpperBound` nil to scan from a start key to
the end, or leave `LowerBound` nil to scan from the beginning up to a limit.

## Prefix scan

A prefix scan is a bounded scan from the prefix to the first key past it. Compute
the exclusive upper bound by incrementing the last non-`0xff` byte of the prefix:

```go
func prefixUpperBound(prefix []byte) []byte {
    end := append([]byte(nil), prefix...)
    for i := len(end) - 1; i >= 0; i-- {
        if end[i] != 0xff {
            end[i]++
            return end[:i+1]
        }
    }
    return nil // prefix is all 0xff: scan to the end
}

func prefixScan(db *lsmdb.DB, prefix []byte, fn func(k, v []byte)) {
    it := db.NewIteratorWith(lsmdb.IterOptions{
        LowerBound: prefix,
        UpperBound: prefixUpperBound(prefix),
    })
    for it.SeekToFirst(); it.Valid(); it.Next() {
        fn(it.Key(), it.Value())
    }
}
```

Design your keys so prefixes are meaningful, for example `user:0042:profile`.
Ordered keys make this efficient: the scan touches only the relevant range.

## Snapshot iterator for a consistent scan

A live `NewIterator` may observe writes that land ahead of its cursor. For a fully
consistent scan, iterate a snapshot:

```go
snap := db.Snapshot()
it := snap.NewIterator()
for it.SeekToFirst(); it.Valid(); it.Next() {
    // every key as of the snapshot, immune to concurrent writes
}
```

See [Merging-Iterator](Merging-Iterator) on live-versus-snapshot consistency.

## Counter pattern (read-modify-write)

There are no atomic multi-key transactions, but a single-key read-modify-write is
safe if your application serialises access to that key (lsmdb itself does not
provide compare-and-swap):

```go
v, err := db.Get(key)
n := 0
if err == nil {
    n, _ = strconv.Atoi(string(v))
} else if !errors.Is(err, lsmdb.ErrNotFound) {
    return err
}
n++
return db.Put(key, []byte(strconv.Itoa(n)))
```

If multiple goroutines do this on the same key concurrently, guard it with your
own mutex; the engine's lock makes each call safe, not the read-then-write pair.

## Bulk load

For loading a large dataset, raise `MemTableSize` so flushes are infrequent, and
accept that each `Put` still fsyncs:

```go
db, _ := lsmdb.Open(dir, lsmdb.Options{MemTableSize: 64 * 1024 * 1024})
for _, kv := range items {
    db.Put(kv.Key, kv.Value)
}
db.Close()
```

The fsync per write bounds throughput; a batched-write path is the planned fix
(see [Roadmap-and-Limitations](Roadmap-and-Limitations) and
[Performance-and-Benchmarks](Performance-and-Benchmarks)).

## Run the demo

```sh
go run ./cmd/lsmdb-demo
```

`cmd/lsmdb-demo/main.go` opens a temp database, writes 1,000 ordered keys, takes a
snapshot, overwrites and deletes through it, and prints the snapshot still seeing
the old state, then a short range scan. It is a smoke test you can read and run.

## See also

- [API-Reference](API-Reference) for exact signatures and ownership rules.
- [Configuration-and-Tuning](Configuration-and-Tuning) for the Options.
- [Read-Path](Read-Path) for snapshot semantics.

---
SarmaLinux . sarmalinux.com . [lsmdb on GitHub](https://github.com/sarmakska/lsmdb)
