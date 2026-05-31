# Comparisons

Where lsmdb sits next to the obvious alternatives, honestly. The point of this
page is to help you choose the right tool, which sometimes means choosing a
different one. lsmdb is a readable, single-process, embedded LSM engine; it is
not trying to out-feature production stores. See
[Roadmap-and-Limitations](Roadmap-and-Limitations) for the non-goals.

## At a glance

| | lsmdb | LevelDB | RocksDB | Pebble | BoltDB / bbolt |
| --- | --- | --- | --- | --- | --- |
| Data structure | LSM-tree | LSM-tree | LSM-tree | LSM-tree | B+tree |
| Language | Go (stdlib only) | C++ | C++ | Go | Go |
| Durability | fsync per write | WAL, configurable | WAL, configurable | WAL, configurable | fsync per txn (mmap) |
| MVCC snapshots | yes (sequence numbers) | yes | yes | yes | yes (txn) |
| Range scans | yes (merging iterator) | yes | yes | yes | yes |
| Bloom filters | yes (per table) | yes | yes | yes | n/a |
| Compaction | inline, levelled | background, levelled | background, many strategies | background, levelled | n/a (rebalance) |
| Transactions | snapshot reads only | no | optimistic/pessimistic | no | full ACID txns |
| Compression | no | yes (snappy) | yes (many) | yes | no |
| Maturity | portfolio/teaching | production, mature | production, very mature | production (CockroachDB) | production, mature |
| Dependencies | none | none (C++) | several | a few | none |
| Lines of code | small, readable | moderate | very large | large | moderate |

## Versus LevelDB

lsmdb is closest to LevelDB in spirit: the same LSM shape, the same version-edit
manifest, the same per-table bloom filters, the same internal-key trailer idea.
The differences are deliberate simplifications. LevelDB prefix-compresses block
keys and compresses blocks with Snappy; lsmdb stores [whole
keys](Data-Formats) and no compression, for a block reader you can describe in a
paragraph. LevelDB runs compaction in the background; lsmdb runs it
[inline](Design-Decisions) for deterministic, provable recovery. If you want the
LevelDB design rendered in readable Go that you can step through, lsmdb is that.
If you want LevelDB's maturity and space efficiency, use LevelDB.

## Versus RocksDB

RocksDB is LevelDB grown into a configurable monster: column families, multiple
compaction strategies, transactions, tiered storage, dozens of tuning flags,
twenty years of production hardening. lsmdb implements the core LSM ideas RocksDB
is built on, but none of the surface. The honest framing from the README stands:
lsmdb is correct and well tested, but it has not survived production across
thousands of deployments, and I will not pretend it has. Reach for RocksDB (or its
Go bindings) when you need that hardening and feature set. Reach for lsmdb when
you want to understand what RocksDB is doing underneath.

## Versus Pebble

Pebble is CockroachDB's from-scratch Go reimplementation of a RocksDB-compatible
engine. It is the production Go LSM engine. Architecturally it and lsmdb share the
language and the LSM model, but Pebble is a large, mature codebase tuned for a
demanding distributed database. lsmdb shares the ideas at a scale you can read in
an afternoon. If you are shipping a Go service that needs a battle-tested embedded
store, Pebble is the serious choice; lsmdb is the one you read to learn how it
works.

## Versus BoltDB / bbolt

This is the real architectural fork, not just a maturity gap. bbolt is a B+tree in
a single memory-mapped file with full ACID transactions. The trade-offs invert:

- **Writes.** bbolt updates a copy-on-write B+tree per transaction; lsmdb appends
  to a log and a MemTable. lsmdb favours write throughput and sequential I/O; bbolt
  favours read locality and in-place consistency.
- **Reads.** bbolt reads walk a balanced tree with excellent locality; lsmdb reads
  may consult several tables (mitigated by [bloom filters](Bloom-Filter)).
- **Transactions.** bbolt gives you real multi-key read-write transactions; lsmdb
  gives you snapshot reads only, no multi-key atomic commit.
- **Space.** bbolt's file can fragment and is bounded by the largest size it ever
  reached; lsmdb reclaims space through [compaction](Compaction) but holds
  superseded versions until then.

Choose bbolt for read-heavy workloads that need transactions and a single file.
Choose an LSM (lsmdb to learn, Pebble to ship) for write-heavy, append-friendly
workloads.

## Versus a plain file or SQLite

If your data fits in memory and you just need persistence, a JSON file or a
gob-encoded snapshot is simpler than any of these. If you need SQL, secondary
indexes, joins or a query planner, use SQLite; it is a database, not a storage
engine, and lsmdb is the layer SQLite-class systems are built on, not a
replacement for them.

## Where lsmdb genuinely wins

Two cases:

1. **Learning and teaching.** You want to read every line that makes an LSM-tree
   work, with no production cruft, no dependencies, and docs that point at the
   source. That is the entire design brief.
2. **A small embedded ordered store in a Go program** where you control the
   workload, value a tiny auditable dependency footprint (zero external deps), and
   do not need transactions, compression, or proven scale. For that, lsmdb is a
   legitimate, correct choice.

## The SarmaLinux family

If you need a *distributed* key-value store rather than an embedded one, that is a
different project in the same line: raftkv. lsmdb stays single-process on purpose.

## See also

- [Design-Decisions](Design-Decisions) for why lsmdb made its simplifications.
- [Roadmap-and-Limitations](Roadmap-and-Limitations) for the explicit non-goals.
- [Architecture](Architecture) for the LSM model in detail.

---
SarmaLinux . sarmalinux.com . [lsmdb on GitHub](https://github.com/sarmakska/lsmdb)
