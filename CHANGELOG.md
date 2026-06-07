# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Skip-list MemTable as the in-memory write buffer, keyed by versioned internal
  keys for MVCC ordering.
- Write-ahead log with CRC-checked, length-prefixed records and crash recovery
  on open. A torn trailing record left by a crash is detected and dropped.
- Block-based SSTable format with a sparse block index, a per-table bloom filter
  and a properties block holding key bounds and entry count.
- Levelled compaction from L0 to LN with overlap-driven merges, newest-version
  selection and bottom-level tombstone dropping for space reclamation.
- MVCC via monotonic sequence numbers, snapshot reads and a heap-based merging
  iterator for ordered range scans across the MemTable and every level.
- Public API: Open, Close, Put, Delete, Get, NewIterator and Snapshot, plus an
  append-only manifest that records the live table set durably.
- Bounded range scans through NewIteratorWith and IterOptions, taking a
  half-open [LowerBound, UpperBound) interval so a prefix or sub-range scan
  seeks straight to the lower bound and stops at the upper bound. Available on
  both the database and a snapshot.
- Command-line demo under cmd/lsmdb-demo and a runnable Example test.
- Test suite covering write and read roundtrips, durability and crash recovery,
  compaction correctness and space reclamation, the bloom filter false positive
  bound, MVCC snapshot isolation and ordered range scans across levels.
- Write-throughput and read-throughput benchmarks.

[Unreleased]: https://github.com/sarmakska/lsmdb/commits/main
