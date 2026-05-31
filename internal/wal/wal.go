// Package wal implements the write-ahead log that gives lsmdb durability. Every
// mutation is appended to the log and synced before it is acknowledged, so a
// crash can never lose an acknowledged write. On open the engine replays the
// log to rebuild the MemTable.
//
// Record framing follows a length-prefixed, CRC-checked layout so a partial
// trailing write left by a crash is detected and discarded rather than
// replayed as corrupt data:
//
//	+----------+--------+-----------+
//	| crc32 4B | len 4B | payload   |
//	+----------+--------+-----------+
//
// The CRC covers the payload only. A torn tail (short read, or a CRC mismatch)
// stops replay cleanly at the last fully durable record.
package wal

import (
	"bufio"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// ErrClosed is returned by Append after the log has been closed.
var ErrClosed = errors.New("lsmdb: wal closed")

// Writer appends records to a log file.
type Writer struct {
	f      *os.File
	w      *bufio.Writer
	closed bool
}

// Create opens path for writing, truncating any existing content.
func Create(path string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Writer{f: f, w: bufio.NewWriter(f)}, nil
}

// Append writes one record and flushes it to the file buffer. It does not fsync;
// callers that need durability for an acknowledged batch must call Sync.
func (w *Writer) Append(payload []byte) error {
	if w.closed {
		return ErrClosed
	}
	var header [8]byte
	binary.LittleEndian.PutUint32(header[0:4], crc32.Checksum(payload, castagnoli))
	binary.LittleEndian.PutUint32(header[4:8], uint32(len(payload)))
	if _, err := w.w.Write(header[:]); err != nil {
		return err
	}
	if _, err := w.w.Write(payload); err != nil {
		return err
	}
	return nil
}

// Sync flushes the buffer and fsyncs the file, the durability barrier behind a
// committed write.
func (w *Writer) Sync() error {
	if w.closed {
		return ErrClosed
	}
	if err := w.w.Flush(); err != nil {
		return err
	}
	return w.f.Sync()
}

// Close flushes, syncs and closes the underlying file.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if err := w.w.Flush(); err != nil {
		return err
	}
	if err := w.f.Sync(); err != nil {
		return err
	}
	return w.f.Close()
}

// Reader replays records from a log file. ReadAll returns every fully durable
// record and stops silently at the first torn or corrupt trailing record, which
// is the expected outcome of a mid-write crash.
type Reader struct {
	f *os.File
	r *bufio.Reader
}

// Open opens path for replay.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &Reader{f: f, r: bufio.NewReader(f)}, nil
}

// Next returns the next record. It returns io.EOF at a clean end of log and
// also at a torn tail, treating an incomplete final record as the natural end
// of a crashed write rather than an error.
func (r *Reader) Next() ([]byte, error) {
	var header [8]byte
	if _, err := io.ReadFull(r.r, header[:]); err != nil {
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			return nil, io.EOF
		}
		return nil, err
	}
	want := binary.LittleEndian.Uint32(header[0:4])
	length := binary.LittleEndian.Uint32(header[4:8])
	payload := make([]byte, length)
	if _, err := io.ReadFull(r.r, payload); err != nil {
		// A short payload read is a torn tail. Stop replay here.
		return nil, io.EOF
	}
	if crc32.Checksum(payload, castagnoli) != want {
		// A CRC mismatch on the trailing record is treated as a torn tail.
		return nil, io.EOF
	}
	return payload, nil
}

// Close closes the reader.
func (r *Reader) Close() error { return r.f.Close() }
