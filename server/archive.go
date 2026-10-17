package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Record is one line of the archive.
type Record struct {
	// Op is "sample" for a received sample and "delete" for a tombstone.
	Op         string          `json:"op"`
	UUID       string          `json:"uuid"`
	Type       string          `json:"type,omitempty"`
	ReceivedAt time.Time       `json:"received_at"`
	Data       json.RawMessage `json:"data"`
}

// Archive is an append-only store of records: one JSON Lines file per UTC day of receipt,
// <dir>/YYYY/YYYY-MM-DD.jsonl. Each (op, uuid) pair is stored at most once, so retried and
// re-sent batches are not duplicated. Files are never rewritten.
type Archive struct {
	dir string
	now func() time.Time

	mu   sync.Mutex
	seen map[string]struct{} // op + ":" + uuid
}

// OpenArchive creates dir if needed and indexes the records already in it.
func OpenArchive(dir string) (*Archive, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	a := &Archive{dir: dir, now: time.Now, seen: make(map[string]struct{})}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return err
		}
		return a.index(path)
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Archive) index(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for n := 1; ; n++ {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var rec struct{ Op, UUID string }
			if jerr := json.Unmarshal(line, &rec); jerr != nil || rec.UUID == "" {
				// A crash mid-write can leave a partial last line: skip it, the client re-sends.
				log.Printf("archive: %s:%d: skipping unreadable line", path, n)
			} else {
				a.seen[rec.Op+":"+rec.UUID] = struct{}{}
			}
		}
		if err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
	}
}

// Len is the number of distinct records in the archive.
func (a *Archive) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.seen)
}

// Append durably writes the records that are not yet archived (fsync before returning) and
// returns them. Either all new records are written or none are.
func (a *Archive) Append(recs []Record) (added []Record, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	var buf bytes.Buffer
	batch := make(map[string]struct{})
	now := a.now().UTC()
	for _, r := range recs {
		key := r.Op + ":" + r.UUID
		if _, dup := a.seen[key]; dup {
			continue
		}
		if _, dup := batch[key]; dup {
			continue
		}
		batch[key] = struct{}{}
		r.ReceivedAt = now
		line, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		added = append(added, r)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if len(added) == 0 {
		return nil, nil
	}
	if err := a.write(now, buf.Bytes()); err != nil {
		return nil, err
	}
	for k := range batch {
		a.seen[k] = struct{}{}
	}
	return added, nil
}

func (a *Archive) write(day time.Time, data []byte) error {
	sub := filepath.Join(a.dir, day.Format("2006"))
	if err := os.MkdirAll(sub, 0o750); err != nil {
		return err
	}
	path := filepath.Join(sub, day.Format("2006-01-02")+".jsonl")
	_, statErr := os.Stat(path)
	created := os.IsNotExist(statErr)

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	// Start on a fresh line if a previous write was cut short.
	if size > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, size-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			data = append([]byte{'\n'}, data...)
		}
	}
	if _, err := f.Write(data); err != nil {
		f.Truncate(size)
		return fmt.Errorf("archive write: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Truncate(size)
		return fmt.Errorf("archive fsync: %w", err)
	}
	if created {
		syncDir(sub)
		syncDir(a.dir)
	}
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
