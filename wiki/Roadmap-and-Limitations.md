# Roadmap and Limitations

A real project says what it is not. lsmdb is a single-process, embedded,
ordered key-value engine that I built to be readable end to end. This page is the
honest account of where it stops and where it is going.

## What lsmdb is not

- **Not networked or multi-node.** There is no server, no replication, no
  sharding. If you need a distributed key-value store, that is a different
  project; the SarmaLinux line has one (raftkv).
- **Not a transaction manager.** There is no multi-key atomic commit and no
  serialisable isolation across keys. A `Snapshot` gives a consistent read view;
  it does not give read-modify-write transactions.
- **Not a RocksDB or Pebble replacement.** Those have survived years of
  production across thousands of deployments. lsmdb is correct and well tested,
  but it has not been through that fire, and I will not claim otherwise.

## Known limitations

**Snapshots do not pin storage.** The engine keeps old versions only until
compaction reclaims them. A snapshot held across heavy write volume can lose the
versions it depended on. Keep snapshots short, or bound writes while one is open.
See [Troubleshooting](Troubleshooting) for the symptom and the workaround.

**Writers stall during flush and compaction.** Both run inline under the write
lock so the durability and recovery semantics stay deterministic and testable
(see [Architecture](Architecture)). A flush or compaction therefore briefly
blocks writers. This is a deliberate trade made for provable correctness, and it
is first on the list to change.

**Every Put fsyncs.** Durable writes cost one fsync each, dominated by device
latency (around 2.8 ms on a quiet Apple M3 Pro; see
[Performance-and-Benchmarks](Performance-and-Benchmarks)). There is no
batched-write path yet, so write-heavy bulk loads pay the fsync per record.

**A crash mid-compaction can leave orphaned `.sst` files.** The database stays
consistent because the manifest decides which tables are live, but unreferenced
files are not yet swept on open. They are harmless and can be removed manually
while the database is closed.

## Roadmap

In rough priority order, what I intend to add:

1. **Batched writes behind one fsync.** A `WriteBatch` for applications that can
   trade a few milliseconds of durability for a large throughput gain. The WAL
   already separates `Append` from `Sync`, so the plumbing is in place.
2. **Background flush and compaction.** Move both off the write lock onto a
   dedicated goroutine, once I have a concurrency test harness I trust to catch a
   regression in the recovery semantics. The manifest design already supports it.
3. **Snapshot pinning.** A retained minimum sequence that compaction must not
   drop below, so a long-lived snapshot keeps the versions it needs. This fits
   cleanly on top of the existing sequence-number design.
4. **Orphaned-file sweep on open.** Remove `.sst` files whose numbers do not
   appear in the manifest, cleaning up after a crash mid-compaction.

## What I will not add

A network layer, a query language, and pluggable comparators are all off the
table. Each would pull lsmdb away from being a readable, single-purpose engine,
which is the entire reason it exists. The brief is an LSM-tree you can hold in
your head, and growing surface area is the fastest way to lose that.

## See also

- [Architecture](Architecture) for the design decisions behind these trade-offs.
- [Design-Decisions](Design-Decisions) for the rejected alternatives in full.
- [Writing-an-Extension](Writing-an-Extension) for how the roadmap items would land.
- [Comparisons](Comparisons) for what the non-goals mean against other engines.
- [Recovery](Recovery) for the durability contract that constrains the roadmap.
- [Troubleshooting](Troubleshooting) for working around the current limitations.

---
SarmaLinux . sarmalinux.com . [lsmdb on GitHub](https://github.com/sarmakska/lsmdb)
