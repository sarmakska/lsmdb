# Data Formats

Every byte lsmdb writes to disk, documented. There are four on-disk artefacts:
the write-ahead log, the SSTable, the manifest, and the in-key MVCC trailer that
threads through all of them. This page is the reference for tooling, debugging,
and anyone porting a reader. Endianness is called out per field because the
engine mixes little-endian framing with big-endian keys for a deliberate reason
(see [Internal-Key-and-MVCC](Internal-Key-and-MVCC)).

## Files in a database directory

```
data/
  MANIFEST          append-only edit log (JSON lines)
  000001.log        write-ahead log for the active MemTable
  000002.sst        immutable sorted table
  000005.sst        ...
```

File numbers are six-digit zero-padded (`%06d`), allocated monotonically by
`allocFileNum` in `db.go`. `.log` and `.sst` share the number space. Paths are
built by `manifestPath`, `tablePath` and `logPath`.

## The internal key

The unit every format stores. User key bytes, then an 8-byte trailer:

```
+------------------+--------------------------------+
| user key (N B)   | trailer (8 B, big-endian)      |
+------------------+--------------------------------+
                   | seq (high 56 bits) | kind (8 b)|
```

```
trailer = (seq << 8) | kind
kind: 0 = Delete (tombstone), 1 = Set (live value)
```

Big-endian so a bytewise comparison orders by sequence then kind. Code:
`MakeInternalKey`, `PackTrailer` in `internal/encoding/encoding.go`.

## Write-ahead log

A `.log` file is a sequence of framed records. Framing (`internal/wal/wal.go`):

```
+--------------+-----------+--------------------+
| crc32c 4B LE | len 4B LE | payload (len B)    |
+--------------+-----------+--------------------+
```

- `crc32c`: CRC-32 Castagnoli over the payload only.
- `len`: payload length, little-endian uint32 (max ~4 GiB).
- The CRC and length let [recovery](Recovery) detect a torn tail and stop.

The payload is one mutation, encoded by `encodeRecord` in `record.go`:

```
+----------+--------+----------------+--------+------------------+---------+
| seq 8B LE| kind 1B| varint keyLen  | key    | varint valueLen  | value   |
+----------+--------+----------------+--------+------------------+---------+
```

Note the seq here is little-endian (it is a framing field the engine owns),
distinct from the big-endian seq inside an internal key. A delete has `kind = 0`
and `valueLen = 0`.

## SSTable

An `.sst` file (`internal/sstable`, layout documented in `format.go`):

```
+-----------------------+  offset 0
| data block 0          |
| data block 1          |
| ...                   |
+-----------------------+
| bloom filter          |
+-----------------------+
| index block           |
+-----------------------+
| properties block      |
+-----------------------+
| footer (56 bytes)     |  end of file
+-----------------------+
```

A reader starts at the footer.

### Footer (fixed 56 bytes)

```
+-----------------+-----------------+-----------------+-------------+
| bloom handle    | index handle    | props handle    | magic 8B LE |
| 16B             | 16B             | 16B             |             |
+-----------------+-----------------+-----------------+-------------+
```

A block handle is two little-endian uint64s: `offset` then `length`. The magic is
`0x6c736d6462303031` ("lsmdb001"), checked on open to reject a foreign or
truncated file (`ErrBadTable`).

### Data block

A run of entries, each:

```
+----------------+--------------+------------------+----------+
| varint keyLen  | internal key | varint valueLen  | value    |
+----------------+--------------+------------------+----------+
```

The key is a full internal key (user key plus the 8-byte trailer), stored whole,
not prefix-compressed. The writer starts a new block once the current one reaches
`blockTargetSize` (4 KiB). Entries are in ascending `CompareInternal` order; the
writer rejects out-of-order input with `ErrUnsortedKey`.

### Bloom filter block

```
+------------------+----------------+
| bit array (M B)  | probe count 4B |
|                  | LE uint32      |
+------------------+----------------+
```

Self-describing: the probe count `k` is recovered from the trailing four bytes.
Sizing and probing are in [Bloom-Filter](Bloom-Filter). Code: `Filter.Encode`,
`Decode` in `internal/bloom/bloom.go`.

### Index block (sparse, one entry per data block)

```
+----------------+   then per entry:
| varint count   |   +----------------+--------------+-------------+
+----------------+   | varint keyLen  | first key    | handle 16B  |
                     +----------------+--------------+-------------+
```

The `first key` is the first internal key of that data block; the handle points
at the block. A reader binary searches this to find the candidate block. Code:
`Writer.Finish`, `Reader.parseIndex`.

### Properties block

```
+----------------+----------------+----------+----------------+----------+
| varint count   | varint slen    | smallest | varint llen    | largest  |
+----------------+----------------+----------+----------------+----------+
```

The entry count and the smallest and largest internal keys, so a reader learns a
table's size and bounds on open without scanning it. The engine uses the bounds
for overlap and binary search, and the count for level sizing. Code:
`Writer.Finish`, `Reader.parseProps`.

## Manifest

Newline-delimited JSON, one `manifestEdit` per line (`manifest.go`):

```json
{"added":[{"file":2,"level":0,"smallest":"BASE64-ISH-BYTES","largest":"...","count":812}],"next_file":3,"last_seq":812}
{"deleted":[2,3,4],"added":[{"file":5,"level":1,...}],"next_file":6,"last_seq":2400}
```

Fields (all `omitempty`):

| JSON key | Go type | Meaning |
| --- | --- | --- |
| `added` | `[]tableMeta` | tables now live |
| `deleted` | `[]uint64` | file numbers no longer live |
| `next_file` | `uint64` | next file number to allocate |
| `last_seq` | `uint64` | highest committed sequence |

`tableMeta`: `file` (uint64), `level` (int), `smallest`/`largest` (the raw
internal-key bytes, JSON-encoded as Go marshals `[]byte`, base64), `count` (int).
Replaying the lines folds them into the live set; a torn final line stops replay.
See [Manifest-and-Versioning](Manifest-and-Versioning).

## Endianness summary

| Field | Endianness | Why |
| --- | --- | --- |
| Internal-key trailer | big-endian | bytewise compare orders by sequence |
| WAL CRC, length | little-endian | framing fields, native to the writer |
| WAL payload seq | little-endian | engine-owned mutation field |
| SSTable block handles, magic | little-endian | framing fields |
| Bloom probe count | little-endian | framing field |
| varints (lengths, counts) | LEB128 | `encoding/binary` Uvarint |

## Inspecting a database by hand

```sh
# Read the table history
cat data/MANIFEST

# Confirm a file is an lsmdb table: last 8 bytes should be the magic
tail -c 8 data/000002.sst | xxd
# expect ... 31 30 30 62 64 6d 73 6c  (little-endian 0x6c736d6462303031)
```

The magic at the file tail is the quickest check that a `.sst` is a valid lsmdb
table and not truncated.

## See also

- [Write-Ahead-Log](Write-Ahead-Log) for the record framing in context.
- [SSTable-Format](SSTable-Format) for how the blocks are read and written.
- [Manifest-and-Versioning](Manifest-and-Versioning) for the edit log.
- [Internal-Key-and-MVCC](Internal-Key-and-MVCC) for the trailer.

---
SarmaLinux . sarmalinux.com . [lsmdb on GitHub](https://github.com/sarmakska/lsmdb)
