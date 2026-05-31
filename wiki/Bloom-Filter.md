# Bloom Filter

Each SSTable carries one bloom filter over its user keys. Before a point lookup
reads any data block, it asks the filter whether the key could be present. A
"no" is definitive, so the table is skipped without touching disk; a "yes" might
be a false positive that costs one block read. This is what keeps read
amplification low when a key is absent from most tables. The code is
`internal/bloom/bloom.go`, tested in `internal/bloom/bloom_test.go`.

## What a bloom filter buys you

An LSM read may have to consult many tables: all of L0 plus one table per level
below. Most of those tables do not hold the key. Without a filter, each would
cost a block read to discover the miss. The filter turns a likely miss into a
single bit-vector probe in memory. At the default one-percent false positive
rate, ninety-nine of a hundred absent-key lookups skip the table entirely. See
[Read-Path](Read-Path) for where the filter sits in a Get.

## Sizing from a target rate

The builder is constructed with a target false positive probability (default
0.01, set by `Options.BloomFalsePositiveRate`). Two standard results turn that
probability into a filter shape:

```go
// bits per key: m/n = -ln(p) / ln(2)^2
func bitsPerKeyForRate(p float64) int {
    bits := math.Ceil(-math.Log(p) / (math.Ln2 * math.Ln2))
    ...
}

// probe count: k = (m/n) * ln(2)
func optimalProbes(bitsPerKey int) uint32 {
    k := uint32(math.Round(float64(bitsPerKey) * math.Ln2))
    ...
}
```

For p = 0.01 this gives about 10 bits per key and 7 probes. The probe count is
clamped to the range 1..30 so a pathological rate cannot ask for an absurd number
of hash evaluations.

| Target p | bits/key (m/n) | probes (k) |
| --- | --- | --- |
| 0.1 | 5 | 3 |
| 0.01 | 10 | 7 |
| 0.001 | 15 | 10 |

## One hash, two halves, k probes

The filter never stores keys, only bits. `Build` derives `k` probe positions from
a single 64-bit hash using double hashing: the low 32 bits seed the position, the
high 32 bits are the stride.

```go
for _, h := range b.hashes {
    h1 := uint32(h)
    h2 := uint32(h >> 32)
    for i := uint32(0); i < b.probeCount; i++ {
        pos := (h1 + i*h2) % nbits
        bits[pos/8] |= 1 << (pos % 8)
    }
}
```

`MayContain` runs the same arithmetic and returns false the instant any probed
bit is zero. Double hashing is both fast (one hash, then arithmetic) and
statistically sound for bloom filters, which is why it is the standard technique.

### The hash

The hash is a 64-bit FNV-1a variant with a final avalanche mix:

```go
func hash(key []byte) uint64 {
    const offset = 1469598103934665603
    const prime  = 1099511628211
    h := uint64(offset)
    for _, c := range key {
        h ^= uint64(c)
        h *= prime
    }
    h ^= h >> 33
    h *= 0xff51afd7ed558ccd
    h ^= h >> 33
    return h
}
```

The mix matters: plain FNV-1a leaves correlated low bits, and the double-hashing
scheme leans on both halves being well spread. The hash is deterministic across
processes, which is non-negotiable because the filter is written to disk in one
process and read back in another. See [Data-Formats](Data-Formats) for the
on-disk encoding.

## Serialisation

A filter is self-describing: the bit array followed by the 4-byte probe count.

```go
func (f *Filter) Encode() []byte {
    out := make([]byte, len(f.bits)+4)
    copy(out, f.bits)
    binary.LittleEndian.PutUint32(out[len(f.bits):], f.k)
    return out
}
```

The reader recovers `k` from the trailing four bytes, so the filter needs no
separate metadata in the table footer beyond its block handle. An empty filter
(no keys added) encodes as a single zero byte plus the probe count; `MayContain`
on an empty filter returns true, which is the safe default (it never produces a
false negative).

## The defining guarantee, and how it is tested

A bloom filter must never produce a false negative: a key that was added must
always report present. `TestNoFalseNegatives` adds 10,000 keys and asserts every
one reads back as a maybe-present. If this ever failed, the engine could lose a
key it actually holds, so it is the most important property of the package.

`TestFalsePositiveBound` adds 20,000 keys, then probes 20,000 keys that were
never added, and asserts the observed false-positive rate stays under 0.03
against a 0.01 target. The bound is loose on purpose: it leaves headroom for
hash variance so the test is not flaky, while still catching a filter that is
badly undersized or mis-probed.

`TestEncodeRoundTrip` checks a filter survives `Encode` then `Decode` with every
added key still reporting present, which guards the on-disk format.

## Worked example

Suppose a Get for `user:9999` reaches an L2 table that does not hold it. The
reader computes one FNV-1a hash, splits it into two halves, probes seven bit
positions, and finds at least one of them zero. It returns
`(nil, false, false)` without reading a single data block, and the read moves to
the next level. Had the table held the key, all seven bits would be set and the
reader would do the binary search and block scan to fetch the value. The filter
converts a probable disk read into seven memory accesses.

## Failure modes and limits

- **Filter too small for the data.** The builder enforces a 64-bit floor
  (`if nbits < 64 { nbits = 64 }`) so a tiny table still gets a usable filter. A
  filter is never undersized relative to its key count because it is sized from
  that count at build time.
- **A very high target rate.** Setting `BloomFalsePositiveRate` near 1 produces a
  tiny filter that rarely skips a table, raising read amplification. Setting it
  very low grows the filter and the table. One percent is a good default for
  most workloads; see [Configuration-and-Tuning](Configuration-and-Tuning).
- **No range support.** A bloom filter answers point membership only. Range scans
  cannot use it, so an [iterator](Merging-Iterator) reads every table in range
  regardless.

## See also

- [Read-Path](Read-Path) for where MayContain gates a block read.
- [SSTable-Format](SSTable-Format) for where the filter sits in the table.
- [Configuration-and-Tuning](Configuration-and-Tuning) for choosing the rate.
- [Data-Formats](Data-Formats) for the encoded bytes.

---
SarmaLinux . sarmalinux.com . [lsmdb on GitHub](https://github.com/sarmakska/lsmdb)
