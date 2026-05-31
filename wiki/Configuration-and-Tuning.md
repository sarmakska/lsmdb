# Configuration and Tuning

Everything tunable lives in one struct, `Options` in `db.go`. The zero value is
usable; `Open` fills defaults in `withDefaults`. This page explains each field,
what it trades off, and how to pick it for a workload. The full LSM behaviour the
fields control is covered in [Compaction](Compaction) and [Write-Path](Write-Path).

## The struct

```go
type Options struct {
    MemTableSize           int64   // default 4 MiB
    BloomFalsePositiveRate float64 // default 0.01
    L0CompactionTrigger    int     // default 4
    LevelSizeMultiplier    int     // default 10
    DisableAutoCompaction  bool    // default false
}
```

```go
func (o *Options) withDefaults() {
    if o.MemTableSize <= 0 { o.MemTableSize = 4 * 1024 * 1024 }
    if o.BloomFalsePositiveRate <= 0 { o.BloomFalsePositiveRate = 0.01 }
    if o.L0CompactionTrigger <= 0 { o.L0CompactionTrigger = 4 }
    if o.LevelSizeMultiplier <= 0 { o.LevelSizeMultiplier = 10 }
}
```

Any non-positive value falls back to the default, so you can set just the fields
you care about and leave the rest at zero.

## MemTableSize

The byte threshold at which the active MemTable freezes and flushes to an L0
table. Measured by the skip list's approximate size accounting (see
[Skip-List-and-MemTable](Skip-List-and-MemTable)).

| Larger | Smaller |
| --- | --- |
| Fewer, larger L0 tables | More, smaller L0 tables |
| Fewer flushes and compactions | More frequent flushes |
| More resident memory | Less resident memory |
| Longer recovery (more WAL to replay) | Shorter recovery |
| Bigger stall when a flush does fire | Smaller, more frequent stalls |

**Pick it by** your memory budget and write rate. A few MiB is a sane default. The
demo (`cmd/lsmdb-demo`) uses 64 KiB to force flushes quickly for illustration;
the durability test uses 1 GiB to keep everything in the WAL and never flush. For
a real service, 4 to 64 MiB is the usual range. Remember resident memory runs
above this figure because it does not count skip-list pointer or Go map overhead.

## BloomFalsePositiveRate

The target false-positive rate for per-table [bloom filters](Bloom-Filter). The
builder turns it into bits-per-key (`m/n = -ln(p)/ln(2)^2`) and a probe count.

| Lower rate (e.g. 0.001) | Higher rate (e.g. 0.1) |
| --- | --- |
| Fewer wasted block reads on misses | More wasted block reads |
| Larger filters, larger tables | Smaller filters |
| More probes per lookup | Fewer probes |

**Pick it by** your read pattern. If reads frequently hit absent keys (a cache
front-end, a write-mostly log), a lower rate pays for itself by skipping more
tables. If reads almost always hit present keys, the filter rarely matters and a
higher rate saves space. One percent is a good general default and is what the
benchmarks and tests use.

## L0CompactionTrigger

The number of L0 tables that triggers an L0-to-L1 compaction. It also seeds the
byte budget for deeper levels: `budget = L0CompactionTrigger * 1000` entries for
L1, multiplied by `LevelSizeMultiplier` each level down (see `pickCompaction` in
`compaction.go`).

| Higher trigger | Lower trigger |
| --- | --- |
| L0 grows larger before merging | L0 stays small |
| Fewer, larger compactions | More frequent compactions |
| Higher read amplification (more L0 tables scanned newest-first) | Lower read amplification |
| Less write amplification | More write amplification |

**Pick it by** the classic LSM read-versus-write-amplification trade. A read
scans every L0 table, so a large L0 slows reads; frequent compaction slows writes
by rewriting data more often. Four is a balanced default. Three is used in the
compaction tests to drive merges sooner.

## LevelSizeMultiplier

How much larger each level is than the one above (ten by default). It sets the
shape of the tree: with a multiplier of ten, seven levels span seven orders of
magnitude, so the tree stays shallow even for large datasets.

| Larger multiplier | Smaller multiplier |
| --- | --- |
| Fewer levels for the same data | More levels |
| More data merged per compaction | Less per compaction |
| Higher write amplification per level, but fewer levels | Lower per-level amplification, more levels |

**Pick it by** leaving it at ten unless you have a specific reason. Ten is the
LevelDB and RocksDB default and balances the number of levels (which bounds read
amplification below L0) against the work each compaction does.

## DisableAutoCompaction

Stops the engine from compacting automatically. The test suite uses this to drive
compaction deterministically. In production you almost never want this on:
without compaction, L0 grows without bound, reads slow as they scan more tables,
and space is never reclaimed (see [Troubleshooting](Troubleshooting)). It exists
for tests and for tooling that wants to control merge timing explicitly.

## The fixed constants

Some shape is not configurable, on purpose, to keep the engine readable:

| Constant | Value | Where | Meaning |
| --- | --- | --- | --- |
| `numLevels` | 7 | `db.go` | depth of the level hierarchy |
| `blockTargetSize` | 4 KiB | `internal/sstable/format.go` | soft cap on a data block |
| `compactionMaxEntries` | 100000 | `compaction.go` | split a compaction output past this many entries |
| `maxHeight` | 12 | `internal/skiplist/skiplist.go` | skip-list level cap |
| `branching` | 4 | `internal/skiplist/skiplist.go` | skip-list promotion probability |

If you need to change these, change the constant and rebuild; they are not
options because exposing them would widen the surface without a workload that
needs the knob. See [Design-Decisions](Design-Decisions) on keeping the surface
small.

## Worked profiles

**Write-heavy ingest, occasional reads.** Large `MemTableSize` (32 to 64 MiB) to
amortise flushes, default trigger, default bloom rate. Expect the per-write fsync
to dominate throughput; a batched-write path (roadmap) is the real fix.

**Read-heavy, point lookups on present keys.** Default `MemTableSize`, default
trigger, bloom rate 0.01 or even 0.05 since misses are rare. Keep auto-compaction
on so L0 stays small and reads touch few tables.

**Read-heavy with many absent-key lookups.** Lower `BloomFalsePositiveRate` to
0.001 so the filter skips more tables, accept slightly larger tables.

**Deterministic test of compaction.** `DisableAutoCompaction: true`, small
`MemTableSize`, then call the engine in a way that flushes, and drive compaction
explicitly. This is how the engine's own tests exercise the merge.

## See also

- [API-Reference](API-Reference) for the exact field types.
- [Compaction](Compaction) for what the trigger and multiplier control.
- [Performance-and-Benchmarks](Performance-and-Benchmarks) for measured effects.
- [Skip-List-and-MemTable](Skip-List-and-MemTable) for the size accounting.

---
SarmaLinux . sarmalinux.com . [lsmdb on GitHub](https://github.com/sarmakska/lsmdb)
