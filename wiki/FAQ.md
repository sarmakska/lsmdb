# FAQ

Short answers with links to the detail. If your question is a symptom rather than
a concept, try [Troubleshooting](Troubleshooting) first.

## Is a write durable when Put returns?

Yes. `Put` and `Delete` append to the [write-ahead log](Write-Ahead-Log) and
fsync before returning. A successful call survives a crash. That is the strongest
contract the engine offers and the default; there is no weaker mode today. See
[Write-Path](Write-Path).

## How fast are writes?

Each durable write costs one fsync, dominated by your device's fsync latency
(around 2.8 ms on a quiet Apple M3 Pro). It is disk-bound, not engine-bound. A
batched-write path that syncs once per batch is the planned way to lift this; see
[Performance-and-Benchmarks](Performance-and-Benchmarks) and
[Roadmap-and-Limitations](Roadmap-and-Limitations).

## Can I use it from multiple goroutines?

Yes. `*DB` is safe for concurrent use. Internally a single `sync.RWMutex`
serialises writes and admits concurrent reads; the skip list supports concurrent
reads with one writer. CI runs the suite under `-race`. See
[Architecture](Architecture).

## Does it support transactions?

No multi-key atomic transactions. A `Snapshot` gives a consistent read view (real
snapshot isolation for reads), but there is no read-modify-write transaction
across keys and no multi-key atomic commit. This is an explicit non-goal; for a
single-key read-modify-write pattern see [Examples-and-Recipes](Examples-and-Recipes).

## How do snapshots work?

A `Snapshot` captures the current committed sequence number. Reads through it skip
versions newer than that sequence, so it never sees later writes. It is a number,
not a copy, so taking one is cheap. See [Internal-Key-and-MVCC](Internal-Key-and-MVCC)
and [Read-Path](Read-Path).

## Why did my long-lived snapshot start returning wrong data?

Snapshots do not pin storage in this implementation. The engine keeps old versions
only until [compaction](Compaction) reclaims them, so a snapshot held across heavy
writes can lose the versions it needed. Keep snapshots short, or bound writes
while one is open. Snapshot pinning is on the [roadmap](Roadmap-and-Limitations).

## What is the difference between Put(key, nil) and Delete(key)?

`Put(key, nil)` stores a live, empty value, which reads back as a present
zero-length slice. `Delete(key)` writes a tombstone, which reads back as
`ErrNotFound`. See [Internal-Key-and-MVCC](Internal-Key-and-MVCC).

## Why is my data directory bigger than my live data?

Normal for an LSM-tree between compactions. Overwrites and deletes leave
superseded versions and tombstones in tables until compaction reclaims them, and
L0 holds whole MemTable snapshots that may overlap. Compaction, triggered by
writes, reclaims the space. See [Troubleshooting](Troubleshooting) and
[Compaction](Compaction).

## Are keys ordered? Can I range scan?

Yes, keys are kept in ascending byte order and `NewIterator` walks them in order.
There is no built-in `[lo, hi)` method, but `Seek` plus a manual upper-bound check
gives a range or prefix scan. See [Examples-and-Recipes](Examples-and-Recipes).

## Is there compression?

No. lsmdb stores whole keys and uncompressed values, on purpose, to keep the
block reader simple. Compression is a clean later addition that would not change
the index or footer. See [Design-Decisions](Design-Decisions).

## What is the maximum key or value size?

A WAL record payload is bounded at ~4 GiB by the 32-bit length field, far above
any practical key-value pair. In practice keys and values are limited by memory:
the MemTable holds them, and a value lives in the skip list's value map until
flushed. There is no fixed small cap.

## What happens on a crash mid-compaction?

The database stays consistent. The [manifest](Manifest-and-Versioning) edit is the
atomic commit point, fsynced before any input file is deleted. A crash before the
edit leaves the old tables live; a crash after leaves the new tables live and the
inputs as orphaned files. Orphaned `.sst` files are harmless and ignored on open;
an automatic sweep is on the roadmap. See [Recovery](Recovery).

## Can I open the same directory from two processes?

No. lsmdb is single-process. There is no file lock guarding against a second
opener, and concurrent processes writing the same directory will corrupt it.
Behaviour when files are modified by another process is out of scope per
`SECURITY.md`.

## Why Go with only the standard library?

So the whole engine is auditable in an afternoon and has no dependency to trust,
version or vet. Every primitive comes from `encoding/binary`, `hash/crc32`,
`container/heap`, `os`, `sync` and `math`. See [Design-Decisions](Design-Decisions).

## Should I use this in production?

It is correct and well tested, but it has not survived years of production across
many deployments the way RocksDB or Pebble have, and the docs will not pretend
otherwise. Use it to learn, or as a small embedded store in a Go program where you
control the workload and do not need transactions, compression or proven scale.
See [Comparisons](Comparisons) and [Roadmap-and-Limitations](Roadmap-and-Limitations).

## Why is the test suite slow?

Several tests write thousands of fsynced keys to exercise durability honestly. The
time is fsync latency, not CPU. Run a single test with `go test -run TestName ./`
to iterate faster. See [Troubleshooting](Troubleshooting).

## See also

- [Home](Home) for the full page index.
- [API-Reference](API-Reference) for the method contracts.
- [Troubleshooting](Troubleshooting) for symptom-based help.

---
SarmaLinux . sarmalinux.com . [lsmdb on GitHub](https://github.com/sarmakska/lsmdb)
