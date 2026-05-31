# SSTable Format

An SSTable (sorted string table) is an immutable, on-disk file holding a sorted
run of internal keys and their values. Once written it is never modified, which
is what makes the read path lock-free over tables and makes compaction a matter
of writing new files and atomically swapping them in. The code is in
`internal/sstable`: `format.go`, `writer.go` and `reader.go`.

## File layout

```
+-----------------------+
| data block 0          |  sorted entries, length-prefixed
| data block 1          |
| ...                   |
+-----------------------+
| bloom filter          |  membership filter over all user keys
+-----------------------+
| index block           |  first-key -> block handle, one per data block
+-----------------------+
| properties block      |  entry count, smallest key, largest key
+-----------------------+
| footer (56 bytes)     |  bloom handle, index handle, props handle, magic
+-----------------------+
```

A reader opens a table by reading the fixed-size footer at the end of the file.
The footer holds three block handles (offset and length pairs) pointing at the
bloom filter, the index and the properties, plus an 8-byte magic number that
guards against reading a truncated or foreign file as a table.

```go
const (
    footerSize = 56
    magic uint64 = 0x6c736d6462303031 // "lsmdb001"
)
```

## Data blocks

A data block is a sequence of entries, each framed as:

```
varint(keyLen) | internal key | varint(valueLen) | value
```

The internal key includes the 8-byte MVCC trailer, so a block carries every
version of every key it holds, in sorted order. The writer starts a new block
once the current one reaches `blockTargetSize` (4 KiB by default), which bounds
how much a reader must scan after locating a block.

Keys are stored whole rather than prefix-compressed. This keeps the reader
simple and the format easy to reason about, at a modest space cost. Prefix
compression is a natural future optimisation and would not change the index or
footer.

## The sparse block index

The index has one entry per data block: the first key of the block and a handle
to it. To find a key, the reader binary searches the index for the last block
whose first key is less than or equal to the target, then scans that block. This
is why the index is called sparse: it indexes blocks, not keys, so it stays
small even for a large table.

```go
lo, hi := 0, len(idx)
for lo < hi {
    mid := (lo + hi) / 2
    if encoding.CompareInternal(idx[mid].firstKey, target) <= 0 {
        lo = mid + 1
    } else {
        hi = mid
    }
}
start := lo - 1
```

The table iterator's `Seek` uses exactly this search, then scans forward across
block boundaries until it reaches a key greater than or equal to the target.

## The bloom filter

The filter (`internal/bloom`) is built over the user keys as they are added and
serialised into its own region. It uses double hashing: two 32-bit halves of a
64-bit FNV-1a-derived hash generate `k` probe positions, where `k` is chosen to
minimise the false positive rate for the configured bits-per-key. The
bits-per-key in turn comes from the target false positive rate using the
standard result `m/n = -ln(p) / ln(2)^2`.

The filter is consulted before any data block is read, so a key that is
definitely absent costs no block read. A false positive costs exactly one block
read and then a miss, which is why the rate matters: at one percent, ninety-nine
of a hundred absent-key lookups skip the table entirely.

## The properties block

The properties block stores the entry count and the smallest and largest
internal keys:

```
varint(count) | varint(len) smallest | varint(len) largest
```

Storing these means a reader learns a table's key bounds and size on open without
scanning every entry. The engine uses the bounds to decide table overlap during
compaction and to binary search a level, and uses the count to estimate level
sizes. Before this block existed the reader scanned the whole table on open,
which made opening many tables expensive; the properties block removes that cost.

## Writing a table

The writer enforces that keys arrive in strictly ascending internal-key order:

```go
if w.lastKey != nil && encoding.CompareInternal(key, w.lastKey) <= 0 {
    return ErrUnsortedKey
}
```

Both callers, the MemTable flush and compaction, feed from a sorted source (the
skip list iterator or the merging iterator), so this invariant holds by
construction and the check is a guard against bugs. `Finish` flushes the final
data block, writes the bloom filter, the index, the properties and the footer,
then fsyncs and closes the file so the table is durable before it is recorded in
the manifest.

## Reading a table

`Open` reads the whole file into memory and parses the footer, bloom filter,
index and properties. Tables are bounded by the flush size and the compaction
target, so reading a table fully keeps the hot read path allocation-light while
remaining simple. A reader exposes `Get` for point lookups (bloom filter, then
index, then block scan) and `NewIterator` for ordered iteration used by
compaction and range scans.

## See also

- [Read-Path](Read-Path) for how tables are consulted during a Get.
- [Compaction](Compaction) for how tables are merged and replaced.
- [Recovery](Recovery) for how the magic number and bounds help on restart.

---
SarmaLinux . sarmalinux.com . [lsmdb on GitHub](https://github.com/sarmakska/lsmdb)
