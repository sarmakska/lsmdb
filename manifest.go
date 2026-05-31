package lsmdb

import (
	"bufio"
	"encoding/json"
	"os"
)

// tableMeta records a table's identity and key range so the engine can decide
// which tables overlap a lookup or a compaction without opening them.
type tableMeta struct {
	FileNum  uint64 `json:"file"`
	Level    int    `json:"level"`
	Smallest []byte `json:"smallest"` // smallest internal key
	Largest  []byte `json:"largest"`  // largest internal key
	Count    int    `json:"count"`
}

// manifestEdit is one durable change to the table set. The manifest is an
// append-only log of edits; replaying it on open reconstructs the level layout.
// This mirrors the LevelDB and RocksDB version-edit design, kept deliberately
// small here as newline-delimited JSON so the format is easy to inspect.
type manifestEdit struct {
	Added       []tableMeta `json:"added,omitempty"`
	Deleted     []uint64    `json:"deleted,omitempty"`
	NextFileNum uint64      `json:"next_file,omitempty"`
	LastSeq     uint64      `json:"last_seq,omitempty"`
}

// manifest is the append-only edit log.
type manifest struct {
	f *os.File
	w *bufio.Writer
}

func openManifest(path string) (*manifest, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &manifest{f: f, w: bufio.NewWriter(f)}, nil
}

// append writes one edit and fsyncs it. The manifest is the source of truth for
// which tables are live, so each edit must be durable before the engine acts on
// it (for example before deleting the input files of a compaction).
func (m *manifest) append(e manifestEdit) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := m.w.Write(b); err != nil {
		return err
	}
	if err := m.w.Flush(); err != nil {
		return err
	}
	return m.f.Sync()
}

func (m *manifest) Close() error {
	if err := m.w.Flush(); err != nil {
		return err
	}
	return m.f.Close()
}

// loadManifest replays the edit log into the running state: the live tables,
// the next file number and the last allocated sequence.
func loadManifest(path string) (tables map[uint64]tableMeta, nextFile, lastSeq uint64, err error) {
	tables = make(map[uint64]tableMeta)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return tables, 1, 0, nil
		}
		return nil, 0, 0, err
	}
	defer f.Close()

	nextFile = 1
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e manifestEdit
		if err := json.Unmarshal(line, &e); err != nil {
			// A torn final edit from a crash is ignored, matching WAL semantics.
			break
		}
		for _, t := range e.Added {
			tables[t.FileNum] = t
		}
		for _, d := range e.Deleted {
			delete(tables, d)
		}
		if e.NextFileNum > nextFile {
			nextFile = e.NextFileNum
		}
		if e.LastSeq > lastSeq {
			lastSeq = e.LastSeq
		}
	}
	return tables, nextFile, lastSeq, sc.Err()
}
