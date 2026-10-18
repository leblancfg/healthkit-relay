package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/parquet-go/parquet-go"
)

// Compact merges the files of every partition that has more than one into a single file, and
// drops the rows of deleted samples (and v1 copies superseded by v2) on the way. Partitions
// with one file are left alone: months that received nothing new are never rewritten.
//
// Rows are streamed: memory holds one row group of the output (RowGroupRows rows, compressed)
// and a page per column of the input, whatever the size of the archive.
//
// Crash safety: the merged file is written to a temp file and fsynced; the journal
// (pending_compaction) then names it and its inputs; it is renamed into place; the inputs are
// removed; the journal entry goes last. After a crash, a journaled output that exists means its
// inputs are removed; one that does not means nothing happened (the temp file is discarded).
func (s *Store) Compact(ctx context.Context) (int, error) {
	s.maint.Lock()
	defer s.maint.Unlock()
	n, err := s.compact(ctx)
	if err != nil {
		s.recordFailure("compaction")
		return n, err
	}
	s.recordSuccess("last_compaction", s.now())
	return n, nil
}

func (s *Store) compact(ctx context.Context) (int, error) {
	merged := 0
	for _, dataset := range datasets {
		parts, err := s.partitionFiles(dataset)
		if err != nil {
			return merged, err
		}
		dirs := make([]string, 0, len(parts))
		for dir, files := range parts {
			if len(files) > 1 {
				dirs = append(dirs, dir)
			}
		}
		sort.Strings(dirs)
		for _, dir := range dirs {
			if err := ctx.Err(); err != nil {
				return merged, err
			}
			if err := s.compactPartition(dataset, dir, parts[dir]); err != nil {
				return merged, fmt.Errorf("compact %s: %w", s.relPath(dir), err)
			}
			merged++
		}
	}
	if merged > 0 {
		log.Printf("archive: compacted %d partitions", merged)
	}
	return merged, nil
}

// fileSeq is the flush sequence number in a file name: f123.parquet, c123.parquet, c123-2.parquet.
func fileSeq(path string) int64 {
	name := strings.TrimSuffix(filepath.Base(path), ".parquet")
	if len(name) < 2 {
		return 0
	}
	digits, _, _ := strings.Cut(name[1:], "-")
	n, _ := strconv.ParseInt(digits, 10, 64)
	return n
}

func (s *Store) compactPartition(dataset, dir string, inputs []string) error {
	sort.Slice(inputs, func(i, j int) bool {
		if a, b := fileSeq(inputs[i]), fileSeq(inputs[j]); a != b {
			return a < b
		}
		return inputs[i] < inputs[j]
	})
	var top int64
	for _, in := range inputs {
		top = max(top, fileSeq(in))
	}
	output := filepath.Join(dir, fmt.Sprintf("c%d.parquet", top))
	for k := 2; contains(inputs, output); k++ {
		output = filepath.Join(dir, fmt.Sprintf("c%d-%d.parquet", top, k))
	}

	typ := strings.TrimPrefix(filepath.Base(filepath.Dir(dir)), "type=")
	drop, err := s.dropSet(dataset, typ)
	if err != nil {
		return err
	}
	journal := func() error {
		b, _ := json.Marshal(relPaths(s, inputs))
		if _, err := s.db.Exec(`INSERT OR REPLACE INTO pending_compaction (output, inputs) VALUES (?, ?)`, s.relPath(output), string(b)); err != nil {
			return err
		}
		return s.at("compact-journaled")
	}
	switch dataset {
	case "samples", "records":
		err = mergeFiles(s, output, inputs, journal, func(r *Row) bool {
			return r.UUID != nil && drop.has(*r.UUID, int(r.Wire))
		})
	case "route_points":
		err = mergeFiles(s, output, inputs, journal, func(r *RoutePoint) bool { return drop.has(r.SampleUUID, 2) })
	case "heartbeats":
		err = mergeFiles(s, output, inputs, journal, func(r *Heartbeat) bool { return drop.has(r.SampleUUID, 2) })
	case "ecg_voltages":
		err = mergeFiles(s, output, inputs, journal, func(r *Voltage) bool { return drop.has(r.SampleUUID, 2) })
	case "quantity_series":
		err = mergeFiles(s, output, inputs, journal, func(r *QuantityPoint) bool { return drop.has(r.SampleUUID, 2) })
	default:
		return fmt.Errorf("unknown dataset %q", dataset)
	}
	if err != nil {
		return err
	}
	if err := s.at("compact-renamed"); err != nil {
		return err
	}
	return s.finishCompaction(output, inputs)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func relPaths(s *Store, paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = s.relPath(p)
	}
	return out
}

// finishCompaction removes the inputs a renamed output replaces, then the journal entry.
func (s *Store) finishCompaction(output string, inputs []string) error {
	for _, in := range inputs {
		if in == output {
			continue
		}
		if err := os.Remove(in); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := syncDir(filepath.Dir(output)); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM pending_compaction WHERE output = ?`, s.relPath(output))
	return err
}

func (s *Store) recoverCompactions() error {
	rows, err := s.db.Query(`SELECT output, inputs FROM pending_compaction`)
	if err != nil {
		return err
	}
	type entry struct {
		output string
		inputs []string
	}
	var entries []entry
	for rows.Next() {
		var e entry
		var raw string
		if err := rows.Scan(&e.output, &raw); err != nil {
			rows.Close()
			return err
		}
		json.Unmarshal([]byte(raw), &e.inputs)
		entries = append(entries, e)
	}
	rows.Close()
	for _, e := range entries {
		output := filepath.Join(s.parquet, e.output)
		if _, err := os.Stat(output); err == nil {
			log.Printf("archive: finishing the compaction of %s a restart interrupted", filepath.Dir(e.output))
			inputs := make([]string, len(e.inputs))
			for i, in := range e.inputs {
				inputs[i] = filepath.Join(s.parquet, in)
			}
			if err := s.finishCompaction(output, inputs); err != nil {
				return err
			}
			continue
		}
		if _, err := s.db.Exec(`DELETE FROM pending_compaction WHERE output = ?`, e.output); err != nil {
			return err
		}
	}
	return nil
}

// dropSet holds the deleted UUIDs that apply to one partition: bit 0 set means every copy,
// bit 1 the v1 copy only.
type dropSet map[string]uint8

func (d dropSet) has(uuid string, wire int) bool {
	m := d[uuid]
	return m&1 != 0 || (wire == 1 && m&2 != 0)
}

// dropSet loads the tombstones that can apply to a partition: those of its type (or of no
// known type) for samples and series points, every tombstone for records (a chunk's partition
// is its record kind, not its sample's type). Its size is that of the tombstones, not of the
// archive.
func (s *Store) dropSet(dataset, typ string) (dropSet, error) {
	q, args := `SELECT uuid, wire FROM deleted WHERE type = ? OR type IS NULL`, []any{unescapePart(typ)}
	switch dataset {
	case "records":
		q, args = `SELECT uuid, wire FROM deleted WHERE wire = 0`, nil
	case "route_points", "heartbeats", "ecg_voltages", "quantity_series":
		// A point's partition type is its sample's; tombstones of any type may name it when
		// the app sent no type, so be inclusive.
		q, args = `SELECT uuid, wire FROM deleted WHERE wire = 0 AND (type = ? OR type IS NULL)`, []any{unescapePart(typ)}
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	d := dropSet{}
	for rows.Next() {
		var uuid string
		var wire int
		if err := rows.Scan(&uuid, &wire); err != nil {
			return nil, err
		}
		if wire == 0 {
			d[uuid] |= 1
		} else {
			d[uuid] |= 2
		}
	}
	return d, rows.Err()
}

func unescapePart(v string) string {
	if !strings.Contains(v, "%") {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '%' && i+2 < len(v) {
			if n, err := strconv.ParseUint(v[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(n))
				i += 2
				continue
			}
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// mergeFiles streams the rows of inputs, minus those drop matches, into output. beforeRename
// runs once the output is complete and fsynced, before it is renamed into place.
func mergeFiles[T any](s *Store, output string, inputs []string, beforeRename func() error, drop func(*T) bool) error {
	it := &rowIter[T]{paths: inputs}
	defer it.Close()
	_, err := writeParquetHook(s, output, false, beforeRename, func() (T, error) {
		for {
			row, err := it.Next()
			if err != nil {
				return row, err
			}
			if !drop(&row) {
				return row, nil
			}
		}
	})
	return err
}

// rowIter reads the rows of several Parquet files in turn, a batch at a time.
type rowIter[T any] struct {
	paths []string
	f     *os.File
	r     *parquet.GenericReader[T]
	buf   []T
	n, i  int
}

func (it *rowIter[T]) Next() (T, error) {
	var zero T
	for it.i >= it.n {
		if it.r == nil {
			if len(it.paths) == 0 {
				return zero, io.EOF
			}
			if err := it.open(it.paths[0]); err != nil {
				return zero, err
			}
			it.paths = it.paths[1:]
		}
		clear(it.buf[:it.n])
		n, err := it.r.Read(it.buf)
		it.n, it.i = n, 0
		if errors.Is(err, io.EOF) {
			it.closeFile()
		} else if err != nil {
			return zero, err
		}
	}
	row := it.buf[it.i]
	it.i++
	return row, nil
}

func (it *rowIter[T]) open(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	pf, err := parquet.OpenFile(f, st.Size(), parquet.SkipBloomFilters(true), parquet.SkipPageIndex(true))
	if err != nil {
		f.Close()
		return fmt.Errorf("%s: %w", path, err)
	}
	it.f, it.r = f, parquet.NewGenericReader[T](pf)
	if it.buf == nil {
		it.buf = make([]T, 256)
	}
	return nil
}

func (it *rowIter[T]) closeFile() {
	if it.r != nil {
		it.r.Close()
		it.f.Close()
		it.r, it.f = nil, nil
	}
}

func (it *rowIter[T]) Close() { it.closeFile() }
