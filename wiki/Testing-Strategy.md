# Testing Strategy

For a storage engine, the tests are the proof of correctness, so this page treats
them as a first-class part of the design. lsmdb is tested at two levels: each
internal package has unit tests for its own contract, and the top-level package
has integration tests that exercise the engine end to end, including crash
recovery. CI runs everything with the race detector. The relevant files are
`db_test.go`, `example_test.go`, `bench_test.go`, and the `_test.go` files under
each `internal/` package.

## What each invariant maps to

The [Architecture](Architecture) page lists five invariants. Each has a test that
would fail if it broke:

| Invariant | Test | File |
| --- | --- | --- |
| Durability (acknowledged write survives a crash) | `TestDurabilityAndRecovery` | `db_test.go` |
| Torn write is dropped, never replayed | `TestRecoveryDropsTornTail`, `TestTornTailIgnored`, `TestCorruptCrcStops` | `db_test.go`, `internal/wal/wal_test.go` |
| Newest version wins, deletes hide values | `TestPutGetDelete`, `TestMVCCSnapshotIsolation` | `db_test.go` |
| Compaction preserves visible state and reclaims space | `TestCompactionCorrectnessAndReclamation` | `db_test.go` |
| Ordered, de-duplicated range scan across levels | `TestOrderedRangeScanAcrossLevels` | `db_test.go` |
| Bloom filter never false-negatives | `TestNoFalseNegatives` | `internal/bloom/bloom_test.go` |

## The crash-recovery test

The most important test in the suite. It does not mock a crash; it causes one:

```go
db := mustOpen(t, dir, Options{MemTableSize: 1 << 30}) // 1 GiB, so nothing flushes
for i := 0; i < n; i++ {
    db.Put([]byte(fmt.Sprintf("k%05d", i)), ...)
}
db = nil               // abandon the handle, no Close: a crash
db2 := mustOpen(t, dir, Options{})  // reopen
// assert every committed key returns
```

The 1 GiB MemTable is the trick: it guarantees nothing is flushed, so at "crash"
time the only durable copy of all 2,000 writes is the synced [WAL](Write-Ahead-Log).
Reopening must recover them all from the log alone. This tests the exact failure
the durability contract exists for. See [Recovery](Recovery).

## The torn-tail tests

Three tests prove the other half of durability: a half-written record must be
dropped, never replayed.

- `TestRecoveryDropsTornTail` (`db_test.go`) appends raw garbage bytes to a live
  WAL, then reopens and checks the good records survive and the garbage is gone.
- `TestTornTailIgnored` (`internal/wal/wal_test.go`) truncates inside the last
  record's payload to mimic a partial write.
- `TestCorruptCrcStops` (`internal/wal/wal_test.go`) flips a payload byte so the
  CRC fails, and checks replay stops there.

Together they cover the three stop conditions in `Reader.Next`: short header,
short payload, bad CRC.

## The compaction test

`TestCompactionCorrectnessAndReclamation` is the one that proves space is actually
reclaimed, not just that reads are correct:

```go
// write each key three times, then delete a quarter
// reopen, then:
var liveEntries int
for lvl := 0; lvl < numLevels; lvl++ {
    for _, tbl := range db2.levels[lvl] {
        liveEntries += tbl.Count()
    }
}
rawWrites := 3*n + n/4
if liveEntries >= rawWrites {
    t.Fatalf("no space reclaimed: %d vs %d", liveEntries, rawWrites)
}
```

It reaches into the unexported `levels` (the test is in package `lsmdb`) to count
live entries across every level and asserts the count fell far below the raw
number of writes. Three writes per key plus deletes go in; after compaction drops
superseded versions and bottom-level tombstones, far fewer entries remain. It
also verifies every live key reads its newest value and every deleted key is
gone. See [Compaction](Compaction).

## The MVCC test

`TestMVCCSnapshotIsolation` takes a snapshot, then overwrites a key, deletes
another, and inserts a third. It asserts the snapshot still reads the pre-snapshot
values and does not see the new key, while the live view sees all the changes.
This is the snapshot isolation contract from [Read-Path](Read-Path) made concrete.

## The range-scan test

`TestOrderedRangeScanAcrossLevels` forces data across the MemTable, L0 and deeper
levels with a tiny `MemTableSize`, overwrites evens, deletes multiples of five,
then scans. It checks the scan is in strict ascending order, never repeats a key,
never shows a deleted key, and returns the newest value for each. It also counts
the survivors against the expected count. This exercises the
[merging iterator](Merging-Iterator) and the visibility filter together.

## Internal package tests

- **`internal/sstable/sstable_test.go`** builds tables spanning many 4 KiB blocks
  and checks round-trip Get, absent-key reporting, ordered scan, Seek landing on
  the right key, and that the writer rejects unsorted input (`ErrUnsortedKey`).
- **`internal/bloom/bloom_test.go`** checks no false negatives over 10,000 keys,
  the observed false-positive rate stays under 0.03 against a 0.01 target over
  20,000 probes, and encode/decode round-trips. See [Bloom-Filter](Bloom-Filter).
- **`internal/wal/wal_test.go`** covers the framing and the torn-tail behaviour
  described above.

## The runnable example

`example_test.go` is a Go `Example` with an `// Output:` block, so the test suite
runs it and verifies the printed output matches exactly. It doubles as
documentation that cannot drift from the code, because if the API changes the
example fails to compile or the output mismatches. See
[Examples-and-Recipes](Examples-and-Recipes).

## CI and the race detector

`.github/workflows/ci.yml` runs on every push and pull request to `main`:

1. `gofmt -l .` must report nothing (formatting gate).
2. `go vet ./...`.
3. `go build ./...`.
4. `go test -race ./...` (the full suite under the race detector).
5. `go test -run '^$' -bench . -benchtime=10x ./...` (a benchmark smoke run).

The race detector matters here because the skip list supports concurrent reads
with a single writer; `-race` catches any unsynchronised access. The benchmark
smoke run (10 iterations) just confirms the benchmarks still compile and run, not
a real measurement.

## Running them yourself

```sh
go test ./...                       # the whole suite
go test -run TestDurabilityAndRecovery -v ./   # one test, verbose
go test -race ./...                 # what CI runs
go test -run '^$' -bench . ./       # benchmarks only
```

Several tests write thousands of fsynced keys, so the suite takes tens of seconds;
that time is fsync latency, not CPU. See [Troubleshooting](Troubleshooting) if the
slowness surprises you.

## Gaps I am honest about

There is no fuzzer over the SSTable or WAL parsers yet, no property-based test
generating random operation sequences against a reference map, and no fault
injection at the syscall level (forcing `Sync` to fail mid-run). These are the
tests a production engine would add; they are not here yet. The existing suite
covers the contracts deterministically, which is the right first layer.

## See also

- [Recovery](Recovery) for the durability contract the tests prove.
- [Compaction](Compaction) for the reclamation the tests measure.
- [Writing-an-Extension](Writing-an-Extension) for adding tests with a change.

---
SarmaLinux . sarmalinux.com . [lsmdb on GitHub](https://github.com/sarmakska/lsmdb)
