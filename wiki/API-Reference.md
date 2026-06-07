# API Reference

The complete public surface of `github.com/sarmakska/lsmdb`. Everything here is
declared in `db.go` and `public_iterator.go`. Anything not on this page is an
implementation detail in the package or the `internal/` tree and is not part of
the contract.

## Package

```go
import "github.com/sarmakska/lsmdb"
```

A log-structured merge-tree storage engine: ordered key-value storage with a
write-ahead log for durability, a skip-list MemTable, immutable block-based
SSTables with per-table bloom filters, levelled compaction, and MVCC snapshot
reads over monotonic sequence numbers.

## Errors

```go
var ErrNotFound = errors.New("lsmdb: key not found")
var ErrClosed   = errors.New("lsmdb: database closed")
```

- `ErrNotFound` is returned by `Get` when no live version of a key is visible
  (either the key was never written, or its newest visible version is a
  tombstone). Compare with `errors.Is(err, lsmdb.ErrNotFound)`.
- `ErrClosed` is returned by operations on a closed database.

## Options

```go
type Options struct {
    MemTableSize           int64   // flush threshold in bytes; default 4 MiB
    BloomFalsePositiveRate float64 // target FP rate; default 0.01
    L0CompactionTrigger    int     // L0 table count that triggers compaction; default 4
    LevelSizeMultiplier    int     // each level larger than the one above; default 10
    DisableAutoCompaction  bool    // stop automatic compaction; default false
}
```

The zero value is usable. `Open` fills defaults via `withDefaults`, so
`Options{}` is the standard call. Field-by-field guidance is in
[Configuration-and-Tuning](Configuration-and-Tuning).

## Opening and closing

### Open

```go
func Open(dir string, opts Options) (*DB, error)
```

Opens or creates a database rooted at `dir` (created with mode 0755 if missing).
On open it replays the [manifest](Manifest-and-Versioning) to rebuild the level
layout, opens every live SSTable, then replays the [write-ahead
log](Write-Ahead-Log) to recover acknowledged but unflushed writes. A torn
trailing WAL record from a crash is dropped. See [Recovery](Recovery).

Returns an error if the directory cannot be created, the manifest references a
missing or invalid table, or a log or table cannot be opened.

### Close

```go
func (db *DB) Close() error
```

Flushes the active MemTable to an L0 SSTable, closes the WAL and the manifest,
removes the active log, and releases table handles. After `Close` the handle is
unusable. Calling `Close` twice is safe; the second call returns nil. Always
`defer db.Close()`.

`*DB` is safe for concurrent use by multiple goroutines.

## Writes

### Put

```go
func (db *DB) Put(key, value []byte) error
```

Stores `value` under `key`. The write is appended to the WAL and fsynced before
`Put` returns, so a successful `Put` is durable across a crash. An overwrite is a
new version that shadows the old one; the old version is reclaimed by compaction.
`Put(key, nil)` stores a live, empty value, which is distinct from `Delete`.

`key` and `value` are copied into the engine; the caller may reuse the slices
after the call returns.

### Delete

```go
func (db *DB) Delete(key []byte) error
```

Removes `key` by writing a tombstone. The tombstone shadows older versions until
compaction reclaims them at the bottom level. Deleting an absent key is not an
error; it writes a tombstone that simply shadows nothing.

Both `Put` and `Delete` return `ErrClosed` on a closed database, or a non-nil
error if the WAL append or fsync fails (for example a full disk). A non-nil
return means the write is not durable and must be retried.

## Reads

### Get

```go
func (db *DB) Get(key []byte) ([]byte, error)
```

Returns the value for `key` as visible at the latest committed sequence. Returns
`ErrNotFound` if no live version is visible, or `ErrClosed` on a closed database.
The returned slice is a fresh copy owned by the caller. See [Read-Path](Read-Path).

## Snapshots

### Snapshot

```go
func (db *DB) Snapshot() *Snapshot
```

Captures the current committed sequence number and returns a read-only view at
that point in time. Reads through the snapshot never observe writes made after it
was created. Releasing a snapshot is a no-op; the engine retains versions until
compaction. For a long-lived snapshot, keep write volume bounded so the versions
it needs are not compacted away (see [Troubleshooting](Troubleshooting)).

```go
type Snapshot struct { /* unexported */ }

func (s *Snapshot) Get(key []byte) ([]byte, error)
func (s *Snapshot) NewIterator() *Iterator
func (s *Snapshot) NewIteratorWith(opts IterOptions) *Iterator
```

`Snapshot.Get` resolves a key at the snapshot's sequence. `Snapshot.NewIterator`
and `Snapshot.NewIteratorWith` return range iterators that observe the snapshot.

## Iteration

### NewIterator and NewIteratorWith

```go
func (db *DB) NewIterator() *Iterator
func (db *DB) NewIteratorWith(opts IterOptions) *Iterator
```

`NewIterator` returns a range iterator over the whole key space at the latest
committed state. `NewIteratorWith` restricts the scan to a half-open interval. A
`Snapshot`'s iterators observe the snapshot instead.

```go
type IterOptions struct {
    LowerBound []byte // inclusive start, optional
    UpperBound []byte // exclusive end, optional
}
```

Both bounds are optional and compared by raw user-key bytes. `LowerBound` is
inclusive: the iterator seeks straight to it and never yields a smaller key,
including when `Seek(target)` is called with a `target` below the bound, which is
clamped up. `UpperBound` is exclusive: the iterator stops before any key greater
than or equal to it. A `[LowerBound, UpperBound)` interval therefore selects
exactly the keys in that range and only touches the tables and blocks that
overlap it, which keeps a prefix or sub-range scan cheap. The zero value scans
the whole key space, identical to `NewIterator`.

```go
type Iterator struct { /* unexported */ }

func (it *Iterator) SeekToFirst()
func (it *Iterator) Seek(target []byte)
func (it *Iterator) Next()
func (it *Iterator) Valid() bool
func (it *Iterator) Key() []byte
func (it *Iterator) Value() []byte
```

- `SeekToFirst` positions at the first visible key.
- `Seek(target)` positions at the first visible key greater than or equal to
  `target`.
- `Next` advances to the next distinct visible user key.
- `Valid` reports whether the cursor points at a live key.
- `Key` and `Value` return the current user key and value. The returned slices
  are owned by the iterator and are overwritten by the next `Next`; copy them if
  you need to retain them past the next advance.

The iterator walks user keys in ascending order, returns the newest version
visible at its sequence, and skips keys whose newest visible version is a
tombstone. See [Merging-Iterator](Merging-Iterator).

## Canonical usage

```go
db, err := lsmdb.Open("./data", lsmdb.Options{})
if err != nil {
    log.Fatal(err)
}
defer db.Close()

if err := db.Put([]byte("k"), []byte("v")); err != nil {
    log.Fatal(err)
}

v, err := db.Get([]byte("k"))
switch {
case errors.Is(err, lsmdb.ErrNotFound):
    // absent
case err != nil:
    log.Fatal(err)
default:
    fmt.Printf("%s\n", v)
}

it := db.NewIterator()
for it.SeekToFirst(); it.Valid(); it.Next() {
    fmt.Printf("%s = %s\n", it.Key(), it.Value())
}

// Bounded scan over [from, to): seeks to the lower bound, stops at the upper.
r := db.NewIteratorWith(lsmdb.IterOptions{
    LowerBound: []byte("user:1000"),
    UpperBound: []byte("user:2000"),
})
for r.SeekToFirst(); r.Valid(); r.Next() {
    fmt.Printf("%s = %s\n", r.Key(), r.Value())
}
```

More patterns, including snapshot isolation and prefix scans, are in
[Examples-and-Recipes](Examples-and-Recipes). The runnable `Example` in
`example_test.go` is verified by the test suite.

## Stability

lsmdb is pre-1.0. The eight-method surface above is stable in intent, but
signatures may change before a 1.0 tag. The `Options` struct may gain fields
(the zero value will keep working). The on-disk format is versioned by the
SSTable magic number `0x6c736d6462303031`; a format change will bump it. See
[Roadmap-and-Limitations](Roadmap-and-Limitations).

## See also

- [Configuration-and-Tuning](Configuration-and-Tuning) for the Options fields.
- [Read-Path](Read-Path) and [Write-Path](Write-Path) for what each call does.
- [Examples-and-Recipes](Examples-and-Recipes) for copy-paste patterns.

---
SarmaLinux . sarmalinux.com . [lsmdb on GitHub](https://github.com/sarmakska/lsmdb)
