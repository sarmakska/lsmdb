# Design Decisions

Every interesting choice in lsmdb came with a road I did not take. This page
collects those forks, the alternative I rejected, and why. It is the longer
version of the README's design section, with the trade-offs spelled out and the
code pointed at. The theme throughout: I optimised for an engine I can hold in my
head and prove correct, and I was willing to pay throughput for that.

## Skip-list MemTable, not a sorted slice or a B-tree

The write buffer needs ordered iteration (the flush feeds a sorted stream to the
SSTable writer), fast insert, and concurrent reads while one writer appends.

- **Sorted slice.** Iterates beautifully and is cache-friendly, but every insert
  is O(n) to shift elements. For a buffer taking thousands of writes before each
  flush, that is fatal.
- **Balanced B-tree.** Right asymptotics, but rebalancing rotates nodes, which
  makes lock-light concurrent reads genuinely hard: a reader can land mid-rotation.
- **Skip list (chosen).** Expected O(log n) insert and search, and a reader can
  walk forward along level-0 pointers while a single writer splices new nodes in.
  That is exactly this access pattern.

Code: `internal/skiplist/skiplist.go`, discussed in
[Skip-List-and-MemTable](Skip-List-and-MemTable).

## Append-only manifest, not a rewritten snapshot file

The live table set has to be recorded durably and updated atomically on every
flush and compaction.

- **Rewritten snapshot.** Write the whole live set to a new file, fsync, rename
  in. Simple to read back, but every compaction rewrites the entire list, and the
  rename is not the only thing that must be atomic relative to file deletes.
- **Append-only edit log (chosen).** A log of small deltas, the version-edit
  design from LevelDB and RocksDB. Each edit is tiny, and one fsynced edit is the
  natural atomic commit point for a compaction: it both adds the outputs and
  deletes the inputs. Kept as newline-delimited JSON so you can `cat` the history.

Code: `manifest.go`, discussed in [Manifest-and-Versioning](Manifest-and-Versioning).

## Inline flush and compaction, not a background goroutine

The textbook LSM runs compaction on its own goroutine so writers never block. I
deliberately did not, at least not yet.

- **Background goroutine.** Higher write throughput, no compaction stall. But it
  introduces concurrent mutation of the level layout, which means every recovery
  and correctness test has to reason about races, and the durability semantics get
  harder to prove.
- **Inline under the write lock (chosen).** A flush or compaction runs to
  completion before the next write proceeds. Nothing races the level layout, so
  every test is deterministic and the durability and recovery semantics are
  trivial to reason about. The cost is a brief writer stall during a flush or
  compaction. I chose correctness I could prove over throughput I could not.

The [manifest](Manifest-and-Versioning) design already supports moving this to a
background goroutine, and it is the first thing I would change for a real
workload. See [Architecture](Architecture) on the concurrency model and
[Roadmap-and-Limitations](Roadmap-and-Limitations).

## Whole keys in data blocks, not prefix compression

LevelDB prefix-compresses keys within a block to save space, using restart points
the reader must track.

- **Prefix compression.** Smaller tables. But the block reader gains
  restart-point bookkeeping, and the format stops being describable in a
  paragraph.
- **Whole keys (chosen).** Each entry stores its full internal key. Costs disk,
  but the block reader is a straight scan and the format is simple. Prefix
  compression touches neither the index nor the footer, so it is a clean later
  addition if space ever matters.

Code: `internal/sstable/writer.go`, format in
[Data-Formats](Data-Formats).

## fsync per write, not group commit by default

- **Group commit by default.** Batch writes behind one fsync for throughput. But
  then a "successful" `Put` is not actually durable until the next sync, which
  changes the contract and surprises callers.
- **fsync per write (chosen).** A `Put` that returns nil is on disk. The contract
  is the strongest possible and the easiest to reason about. The
  [WAL](Write-Ahead-Log) keeps `Append` and `Sync` separate so an explicit,
  opt-in batched path can be added without changing the default contract.

See [Performance-and-Benchmarks](Performance-and-Benchmarks) for the cost.

## A side map for values, not values in the skip-list node

- **Value in the node.** One allocation per entry, no map. But it bloats the node
  struct and complicates the comparator, which only ever needs the key.
- **`map[*node][]byte` (chosen).** Keeps nodes small (key plus `next` slice) and
  the comparator key-only. The map is guarded by the same lock. A minor
  allocation cost for a cleaner structure.

Code: `internal/skiplist/skiplist.go`. A future optimisation could inline values,
but it has not earned the complexity.

## CRC-32C framing on the WAL, not a checksum-free log

- **No checksum.** Smaller records, faster append. But a torn or corrupt tail is
  indistinguishable from valid data, so a crash could replay garbage as a
  committed write.
- **CRC-32C per record (chosen).** A short read or a CRC mismatch is treated as
  the clean end of a crashed write, so recovery stops at the last fully durable
  record. The hardware CRC-32C instruction makes this nearly free. This is the
  whole reason the durability contract holds.

Code: `internal/wal/wal.go`, discussed in [Recovery](Recovery).

## Fixed seven levels and fixed block size, not options

- **Expose everything.** Maximum flexibility. But each knob widens the surface and
  adds a configuration no test exercises.
- **Fixed constants (chosen).** `numLevels = 7`, `blockTargetSize = 4 KiB`, and
  the skip-list shape are constants, not options. Seven levels with a 10x
  multiplier span a huge dataset; 4 KiB blocks are a sound default. The
  workload-shaping knobs that matter are options
  ([Configuration-and-Tuning](Configuration-and-Tuning)); the rest stay constants
  to keep the engine readable.

## Standard library only, no dependencies

- **Pull in a fast hash, an mmap library, a sync primitive.** Save some code. But
  every dependency is a thing to audit, version and trust, and it undercuts the
  goal of an engine you can read top to bottom.
- **`go.mod` with zero requires (chosen).** Everything is `encoding/binary`,
  `hash/crc32`, `container/heap`, `os`, `sync`, `math`. The whole engine is
  auditable in an afternoon, which is the point.

## What these choices add up to

A reader can point at the exact lines that make a write durable, decide which
table a read touches, and drop a tombstone at the right moment. The engine trades
peak throughput and some disk space for that legibility and for provable
correctness. Where a trade is reversible later (background compaction, batched
writes, prefix compression, snapshot pinning), the design leaves the door open.
Where reversing it would change the contract (fsync-per-write as the default,
CRC framing), it is fixed on purpose.

## See also

- [Comparisons](Comparisons) for how these choices read against other engines.
- [Architecture](Architecture) for the invariants the choices preserve.
- [Roadmap-and-Limitations](Roadmap-and-Limitations) for the doors left open.

---
SarmaLinux . sarmalinux.com . [lsmdb on GitHub](https://github.com/sarmakska/lsmdb)
