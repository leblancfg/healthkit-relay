package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/parquet-go/parquet-go"
)

// Migration from the archive of versions before 0.5: JSON Lines files, one per day of
// receipt (<archive-dir>/YYYY/YYYY-MM-DD.jsonl), one record per line:
//
//	{"op":"sample","uuid":"…","v":2,"type":"…","received_at":"…","data":{…sample as sent…}}
//	{"op":"record","type":"…","key":"…","digest":"…","received_at":"…","data":{…}}
//	{"op":"delete","uuid":"…","type":"…","received_at":"…","data":{…}}
//
// The lines go through the same path as uploads (Ingest, then flush), streamed, so memory
// does not grow with the archive. Samples a v1 client sent and a v2 client sent again are
// kept once, as v2: the first pass stores everything but v1 samples, the second the v1
// samples that have no v2 copy. The JSON Lines files are left in place: delete them once the
// counts are checked.

type legacyLine struct {
	Op         string          `json:"op"`
	UUID       string          `json:"uuid"`
	V          int             `json:"v"`
	Type       string          `json:"type"`
	Key        string          `json:"key"`
	Digest     string          `json:"digest"`
	ReceivedAt time.Time       `json:"received_at"`
	Data       json.RawMessage `json:"data"`
}

type migration struct {
	store    *Store
	uploader string
	every    int64 // flush after this many staged rows
	out      io.Writer

	lines, skipped, invalid, stored, dupes, v1dupes int64
	expected                                        map[string]int64 // dataset/type -> rows
	sinceFlush                                      int64
	batch                                           []Item
	points                                          []string // per batch item: the point partition, if any
	npoints                                         []int64
}

func runMigrate(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("healthkit-relay migrate", flag.ContinueOnError)
	var cfg config
	cfg.register(fs)
	uploader := fs.String("uploader", "", "device ID to attribute the records to (default: the paired device, if there is exactly one; else the server token)")
	every := fs.Int64("flush-rows", 1_000_000, "flush to Parquet every this many staged rows")
	compact := fs.Bool("compact", true, "compact the archive once migrated")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := applyEnv(fs); err != nil {
		return err
	}
	if cfg.archiveDir == "" {
		return errors.New("-archive-dir is required")
	}
	files, err := filepath.Glob(filepath.Join(cfg.archiveDir, "[0-9]*", "*.jsonl"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return fmt.Errorf("no JSON Lines archive in %s", cfg.archiveDir)
	}
	setBy := false
	fs.Visit(func(f *flag.Flag) { setBy = setBy || f.Name == "uploader" })
	if !setBy && os.Getenv("HKR_UPLOADER") == "" {
		devices, err := OpenDevices(filepath.Join(cfg.archiveDir, "devices.json"))
		if err != nil {
			return err
		}
		if list := devices.List(); len(list) == 1 {
			*uploader = list[0].ID
		}
	}
	store, err := OpenStore(cfg.archiveDir)
	if err != nil {
		return err
	}
	defer store.Close()

	m := &migration{store: store, uploader: *uploader, every: *every, out: out, expected: map[string]int64{}}
	fmt.Fprintf(out, "migrating %d files, records attributed to %s\n", len(files), uploaderName(m.uploader))
	start := time.Now()
	ctx := context.Background()
	for pass := 1; pass <= 2; pass++ {
		for _, f := range files {
			if err := m.file(ctx, f, pass); err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
		}
		if err := m.ingest(ctx); err != nil {
			return err
		}
		fmt.Fprintf(out, "pass %d done: %d lines read, %d stored, %d duplicates (%s)\n", pass, m.lines, m.stored, m.dupes, time.Since(start).Round(time.Second))
	}
	if _, err := store.Flush(ctx, true); err != nil {
		return err
	}
	// Every row passed through staging: give the space back (the seen index stays).
	if _, err := store.db.Exec(`VACUUM`); err != nil {
		return err
	}
	fmt.Fprintf(out, "%d lines: %d stored, %d duplicates (%d of them v1 copies of a v2 sample), %d unreadable, %d invalid\n",
		m.lines, m.stored, m.dupes, m.v1dupes, m.skipped, m.invalid)

	got, err := store.ParquetCounts()
	if err != nil {
		return err
	}
	if err := checkCounts(out, m.expected, got); err != nil {
		return err
	}
	if *compact {
		n, err := store.Compact(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "compacted %d partitions\n", n)
	}
	fmt.Fprintf(out, "done in %s; once checked, delete the JSON Lines files: %s\n", time.Since(start).Round(time.Second),
		filepath.Join(cfg.archiveDir, "YYYY", "*.jsonl"))
	return nil
}

// checkCounts compares rows per dataset and type with what was stored, and fails on any
// difference.
func checkCounts(out io.Writer, want, got map[string]int64) error {
	keys := map[string]bool{}
	for k := range want {
		keys[k] = true
	}
	for k := range got {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "dataset/type\tstored\tin parquet\t")
	bad := 0
	for _, k := range sorted {
		mark := ""
		if want[k] != got[k] {
			mark = "  MISMATCH"
			bad++
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\n", k, want[k], got[k], mark)
	}
	tw.Flush()
	if bad > 0 {
		return fmt.Errorf("%d partitions differ: not compacting; the JSON Lines files are untouched", bad)
	}
	fmt.Fprintln(out, "counts match")
	return nil
}

func (m *migration) file(ctx context.Context, path string, pass int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if err := m.line(ctx, line, pass); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
	}
}

func (m *migration) line(ctx context.Context, line []byte, pass int) error {
	var l legacyLine
	if err := json.Unmarshal(line, &l); err != nil {
		if pass == 1 {
			m.skipped++ // a line a crash cut short; the client sent it again
		}
		return nil
	}
	isV1 := l.Op == "sample" && l.V < 2
	if isV1 != (pass == 2) {
		return nil
	}
	m.lines++
	var it Item
	var pointPart string
	var npoints int64
	switch l.Op {
	case "sample":
		smp, err := parseSample(l.Data)
		if err != nil {
			m.invalid++
			return nil
		}
		it = sampleItem(smp, l.Data, max(l.V, 1))
	case "record":
		rec, err := parseRecord(l.Data)
		if err != nil {
			m.invalid++
			return nil
		}
		it = recordItem(rec, l.Data)
		it.Digest = l.Digest // the de-duplication key the record was stored under
		if ds := pointDataset(rec.Series); ds != "" {
			pointPart, npoints = ds+"/"+cmpOr(rec.SampleType, rec.Series), int64(len(rec.Points))
		}
	case "delete":
		d, err := parseDeletion(l.Data)
		if err != nil {
			m.invalid++
			return nil
		}
		it = deletionItem(d, l.Data)
	default:
		m.invalid++
		return nil
	}
	it.ReceivedAt = l.ReceivedAt
	m.batch = append(m.batch, it)
	m.points = append(m.points, pointPart)
	m.npoints = append(m.npoints, npoints)
	if len(m.batch) >= 5000 {
		return m.ingest(ctx)
	}
	return nil
}

func (m *migration) ingest(ctx context.Context) error {
	if len(m.batch) == 0 {
		return nil
	}
	outcomes, err := m.store.Ingest(m.uploader, m.batch)
	if err != nil {
		return err
	}
	for i, o := range outcomes {
		it := &m.batch[i]
		switch {
		case !o.Stored:
			m.dupes++
			if it.Op == "sample" && it.Wire == 1 {
				m.v1dupes++
			}
		case it.Op == "delete":
			m.stored++
		default:
			m.stored++
			m.sinceFlush++
			ds := "samples"
			if it.Op == "record" {
				ds = "records"
			}
			m.expected[ds+"/"+it.Type]++
			if m.points[i] != "" && m.npoints[i] > 0 {
				m.expected[m.points[i]] += m.npoints[i]
			}
		}
	}
	clear(m.batch)
	m.batch, m.points, m.npoints = m.batch[:0], m.points[:0], m.npoints[:0]
	if m.sinceFlush >= m.every {
		// Chunks whose sample comes later in the files wait in staging (no fallback month).
		if _, err := m.store.flushBefore(ctx, math.MinInt64); err != nil {
			return err
		}
		m.sinceFlush = 0
	}
	return nil
}

// ParquetCounts returns the rows in Parquet per dataset and type ("samples/HK…"), from the
// files' footers.
func (s *Store) ParquetCounts() (map[string]int64, error) {
	out := map[string]int64{}
	for _, dataset := range datasets {
		parts, err := s.partitionFiles(dataset)
		if err != nil {
			return nil, err
		}
		for dir, files := range parts {
			typ := unescapePart(strings.TrimPrefix(filepath.Base(filepath.Dir(dir)), "type="))
			for _, path := range files {
				n, err := parquetRows(path)
				if err != nil {
					return nil, err
				}
				if n > 0 { // not a schema file
					out[dataset+"/"+typ] += n
				}
			}
		}
	}
	return out, nil
}

func parquetRows(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	pf, err := parquet.OpenFile(f, st.Size(), parquet.SkipBloomFilters(true), parquet.SkipPageIndex(true))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return pf.NumRows(), nil
}
