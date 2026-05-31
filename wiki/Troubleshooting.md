# Troubleshooting

This page lists symptoms you might hit while running or integrating lsmdb, what
causes them, and how to resolve them. If your problem is not here, open an issue
with the steps to reproduce.

## Writes feel slow

**Symptom.** Each Put takes on the order of a millisecond or more, and write
throughput is far below what the CPU could manage.

**Cause.** Every Put appends to the write-ahead log and fsyncs before returning.
This is intentional: it is the cost of a crash-safe write, and fsync latency on
the underlying device dominates. The `BenchmarkPutSync` benchmark measures
exactly this.

**Resolution.** This is expected behaviour, not a defect. If your workload can
tolerate losing the last few milliseconds of writes on a crash, batch several
writes between fsyncs. The WAL `Append` and `Sync` are separate calls, so
grouping writes behind one `Sync` is a small change. Faster storage (an NVMe SSD)
also reduces fsync latency directly.

## Get returns ErrNotFound for a key I wrote

**Symptom.** A key you believe you wrote reads back as `ErrNotFound`.

**Causes and checks.**

1. **It was deleted.** A `Delete` writes a tombstone that shadows the value.
   Check whether anything deleted the key after writing it.
2. **A snapshot taken too early.** If you read through a `Snapshot`, it only sees
   writes that committed before the snapshot was taken. Reading the key through
   the live `db.Get` instead confirms whether this is the cause.
3. **Byte-exact keys.** Keys are compared byte for byte. A trailing newline,
   different encoding, or whitespace makes a different key. Print the key bytes on
   both the write and read side to compare.

## Open fails with "open table N" or "not a valid sstable"

**Symptom.** `Open` returns an error mentioning a table number or
`not a valid sstable`.

**Cause.** A table file referenced by the manifest is missing, truncated, or not
a valid lsmdb table. This usually means the data directory was modified by
something other than lsmdb, or a file was lost.

**Resolution.** Restore the data directory from a backup if you have one. The
manifest is the source of truth for which tables must exist; if a referenced
table is gone, the engine cannot safely continue. Do not hand-edit the manifest
or table files. If you are experimenting and the data is disposable, delete the
data directory and start fresh.

## Disk usage is higher than the live data

**Symptom.** The data directory holds more bytes than the sum of your live
key-value pairs.

**Cause.** This is normal for an LSM-tree between compactions. Overwrites and
deletes leave superseded versions and tombstones in tables until compaction
reclaims them. Level 0 in particular holds whole MemTable snapshots that may
overlap.

**Resolution.** Compaction reclaims the space as it runs, triggered by writes.
The `TestCompactionCorrectnessAndReclamation` test demonstrates the live entry
count dropping far below the raw number of writes after compaction. If you have
disabled automatic compaction through `Options.DisableAutoCompaction`, space will
not be reclaimed until you re-enable it.

## Orphaned .sst files after a crash

**Symptom.** After a crash you see table files in the data directory that the
manifest does not reference.

**Cause.** A crash during compaction can leave output files that were written but
not yet committed to the manifest, or input files that were committed as deleted
but not yet unlinked. This is by design: the manifest edit is the atomic commit
point, fsynced before any file is deleted.

**Resolution.** The database is consistent: `Open` uses the manifest to decide
which tables are live and ignores the rest. The orphaned files are harmless. A
future cleanup pass could remove unreferenced files on open; for now they can be
left in place or removed manually while the database is closed, by deleting any
`NNNNNN.sst` file whose number does not appear in the manifest.

## A long-lived snapshot reads stale or missing data

**Symptom.** A snapshot held for a long time starts returning unexpected results.

**Cause.** Snapshots in this implementation do not pin storage. The engine
retains old versions only until compaction reclaims them. A snapshot older than
the versions it depends on can lose access to those versions once they are
compacted away.

**Resolution.** Keep snapshots short-lived, or keep write volume bounded for the
duration of a long snapshot so the versions it needs survive. For workloads that
need long-lived snapshots, snapshot pinning (holding a minimum sequence that
compaction must not drop below) is the standard extension and fits the existing
sequence-number design.

## Tests are slow on my machine

**Symptom.** `go test ./...` takes tens of seconds.

**Cause.** Several tests write thousands of keys, each fsynced, to exercise the
durability, flush and compaction paths honestly. The time is fsync latency, not
CPU work.

**Resolution.** This is expected. To iterate faster on a single area, run one
test with `go test -run TestName ./`. The race detector run in CI
(`go test -race ./...`) is slower still and is meant for CI, not the inner loop.

## See also

- [Recovery](Recovery) for crash and restart behaviour.
- [Compaction](Compaction) for space reclamation.
- [Write-Path](Write-Path) for the fsync cost of durable writes.
