package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	kzstd "github.com/klauspost/compress/zstd"
	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"
)

// Parquet layout, under <archive-dir>/parquet:
//
//	samples/type=<HealthKit type>/month=YYYY-MM/*.parquet         every sample (Row)
//	records/type=<record kind>/month=YYYY-MM/*.parquet            records without a UUID (Row)
//	route_points/type=…/month=…/*.parquet                         one row per workout route location
//	heartbeats/type=…/month=…/*.parquet                           one row per heartbeat-series beat
//	ecg_voltages/type=…/month=…/*.parquet                         one row per ECG voltage
//	quantity_series/type=…/month=…/*.parquet                      one row per value of a quantity series
//	deleted/deleted.parquet                                       tombstones (DeletedRow)
//
// The month is that of the sample's start (UTC); series points go with their sample. Files
// are written once (temp, fsync, rename) and replaced only by compaction or an erase.

// Row is the schema of samples and records: the fields worth querying, and the record exactly
// as received in json, so nothing is lost.
type Row struct {
	UUID          *string  `parquet:"uuid,optional"` // records: the sample a chunk belongs to
	Type          string   `parquet:"type,dict"`
	Kind          string   `parquet:"kind,dict"`
	Key           *string  `parquet:"key,optional"` // records
	Start         *int64   `parquet:"start,optional,timestamp(microsecond)"`
	End           *int64   `parquet:"end,optional,timestamp(microsecond)"`
	Value         *float64 `parquet:"value,optional"`
	Unit          *string  `parquet:"unit,optional,dict"`
	SourceName    *string  `parquet:"source_name,optional,dict"`
	SourceBundle  *string  `parquet:"source_bundle,optional,dict"`
	SourceVersion *string  `parquet:"source_version,optional,dict"`
	Device        *string  `parquet:"device,optional,dict"`
	Uploader      string   `parquet:"uploader,dict"` // paired device that uploaded it ("": server token)
	Wire          int32    `parquet:"wire"`
	ReceivedAt    int64    `parquet:"received_at,timestamp(microsecond)"`
	JSON          string   `parquet:"json"`
}

// RoutePoint is one location of a workout route.
type RoutePoint struct {
	SampleUUID     string   `parquet:"sample_uuid,dict"`
	Chunk          int32    `parquet:"chunk"`
	I              int32    `parquet:"i"`
	Time           *int64   `parquet:"time,optional,timestamp(microsecond)"`
	Lat            *float64 `parquet:"lat,optional"`
	Lon            *float64 `parquet:"lon,optional"`
	Alt            *float64 `parquet:"alt,optional"`
	HAcc           *float64 `parquet:"h_acc,optional"`
	VAcc           *float64 `parquet:"v_acc,optional"`
	Speed          *float64 `parquet:"speed,optional"`
	SpeedAcc       *float64 `parquet:"speed_acc,optional"`
	Course         *float64 `parquet:"course,optional"`
	CourseAcc      *float64 `parquet:"course_acc,optional"`
	EllipsoidalAlt *float64 `parquet:"ellipsoidal_alt,optional"`
	Floor          *int32   `parquet:"floor,optional"`
	Uploader       string   `parquet:"uploader,dict"`
	ReceivedAt     int64    `parquet:"received_at,timestamp(microsecond)"`
}

// Heartbeat is one beat of a heartbeat series.
type Heartbeat struct {
	SampleUUID string  `parquet:"sample_uuid,dict"`
	Chunk      int32   `parquet:"chunk"`
	I          int32   `parquet:"i"`
	Time       *int64  `parquet:"time,optional,timestamp(microsecond)"` // sample start + t
	T          float64 `parquet:"t"`                                    // seconds since the sample's start
	Gap        bool    `parquet:"gap"`                                  // preceded by a gap in measurement
	Uploader   string  `parquet:"uploader,dict"`
	ReceivedAt int64   `parquet:"received_at,timestamp(microsecond)"`
}

// Voltage is one voltage measurement of an ECG (lead I, microvolts).
type Voltage struct {
	SampleUUID string   `parquet:"sample_uuid,dict"`
	Chunk      int32    `parquet:"chunk"`
	I          int32    `parquet:"i"`
	Time       *int64   `parquet:"time,optional,timestamp(microsecond)"`
	T          float64  `parquet:"t"`
	Microvolts *float64 `parquet:"microvolts,optional"`
	Uploader   string   `parquet:"uploader,dict"`
	ReceivedAt int64    `parquet:"received_at,timestamp(microsecond)"`
}

// QuantityPoint is one value of a quantity series sample.
type QuantityPoint struct {
	SampleUUID string   `parquet:"sample_uuid,dict"`
	Chunk      int32    `parquet:"chunk"`
	I          int32    `parquet:"i"`
	Start      *int64   `parquet:"start,optional,timestamp(microsecond)"`
	End        *int64   `parquet:"end,optional,timestamp(microsecond)"`
	Value      *float64 `parquet:"value,optional"`
	Unit       *string  `parquet:"unit,optional,dict"`
	Uploader   string   `parquet:"uploader,dict"`
	ReceivedAt int64    `parquet:"received_at,timestamp(microsecond)"`
}

// DeletedRow is a tombstone, or a v1 sample a v2 copy superseded.
type DeletedRow struct {
	UUID       string  `parquet:"uuid"`
	Wire       int32   `parquet:"wire"` // 0: every copy; 1: the v1 copy only
	Type       *string `parquet:"type,optional,dict"`
	Reason     string  `parquet:"reason,dict"` // deleted or superseded
	Uploader   string  `parquet:"uploader,dict"`
	ReceivedAt int64   `parquet:"received_at,timestamp(microsecond)"`
	JSON       *string `parquet:"json,optional"`
}

// Datasets and the series they hold.
var datasets = []string{"samples", "records", "route_points", "heartbeats", "ecg_voltages", "quantity_series"}

func pointDataset(series string) string {
	switch series {
	case "workout_route":
		return "route_points"
	case "heartbeat":
		return "heartbeats"
	case "electrocardiogram":
		return "ecg_voltages"
	case "quantity":
		return "quantity_series"
	}
	return ""
}

// partitionDir is the directory of one partition. Types and kinds are validated identifiers
// (HealthKit identifiers, [a-z0-9_] kinds), but are escaped anyway: they become paths.
func (s *Store) partitionDir(dataset, typ, month string) string {
	return filepath.Join(s.parquet, dataset, "type="+escapePart(typ), "month="+escapePart(month))
}

func escapePart(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	if b.Len() == 0 || b.String() == "." || b.String() == ".." {
		return "_"
	}
	return b.String()
}

const tmpSuffix = ".tmp"

// Memory bounds of reading and writing Parquet, besides RowGroupRows: rows in flight in a
// batch, and the size of a row group (as received; it is held compressed until written).
const (
	maxBatchBytes    = 4 << 20
	maxRowGroupBytes = 16 << 20
)

// rowBytes estimates the memory a row takes.
func rowBytes(row any) int64 {
	if r, ok := row.(*Row); ok {
		return int64(len(r.JSON)) + 256
	}
	return 128
}

// batchRows is how many rows to read or write at once: few for rows with a json column
// (a series chunk can be a few MB), more for the small rows of the point datasets.
func batchRows[T any]() int {
	var zero T
	if _, ok := any(&zero).(*Row); ok {
		return 16
	}
	return 512
}

// pageZstd is the ZSTD codec with a window the size of a page. Each page is compressed on its
// own, so a larger window (the library's default is 8 MB, with history buffers twice that per
// encoder) only costs memory.
type pageZstd struct {
	once sync.Once
	enc  *kzstd.Encoder
	dec  *kzstd.Decoder
	err  error
}

func (c *pageZstd) init() {
	c.once.Do(func() {
		c.enc, c.err = kzstd.NewWriter(nil, kzstd.WithEncoderConcurrency(1), kzstd.WithEncoderLevel(kzstd.SpeedDefault),
			kzstd.WithWindowSize(1<<19), kzstd.WithLowerEncoderMem(true), kzstd.WithZeroFrames(true), kzstd.WithEncoderCRC(false))
		if c.err == nil {
			c.dec, c.err = kzstd.NewReader(nil, kzstd.WithDecoderConcurrency(1), kzstd.WithDecoderLowmem(true))
		}
	})
}

func (c *pageZstd) String() string                            { return "ZSTD" }
func (c *pageZstd) CompressionCodec() format.CompressionCodec { return format.Zstd }

func (c *pageZstd) Encode(dst, src []byte) ([]byte, error) {
	if c.init(); c.err != nil {
		return nil, c.err
	}
	return c.enc.EncodeAll(src, dst[:0]), nil
}

func (c *pageZstd) Decode(dst, src []byte) ([]byte, error) {
	if c.init(); c.err != nil {
		return nil, c.err
	}
	return c.dec.DecodeAll(src, dst[:0])
}

var zstdCodec = &pageZstd{}

func (s *Store) writerOptions() []parquet.WriterOption {
	return []parquet.WriterOption{
		parquet.Compression(zstdCodec),
		parquet.MaxRowsPerRowGroup(s.RowGroupRows),
		parquet.SkipPageBounds("json"),
		parquet.CreatedBy("healthkit-relay", version, ""),
	}
}

// writeParquet writes the rows next() yields to path, atomically: a temp file next to it,
// fsynced, renamed, and the directory fsynced. It returns the rows written; with none, no file
// is created (and an existing one is removed). next returns io.EOF at the end.
func writeParquet[T any](s *Store, path string, next func() (T, error)) (int64, error) {
	return writeParquetHook(s, path, false, nil, next)
}

// writeParquetKeep is writeParquet, but writes a file (with the schema only) even if empty.
func writeParquetKeep[T any](s *Store, path string, next func() (T, error)) (int64, error) {
	return writeParquetHook(s, path, true, nil, next)
}

// writeParquetHook is writeParquet with options: keepEmpty writes a file even without rows;
// beforeRename, if set, runs once the temp file is complete and fsynced.
func writeParquetHook[T any](s *Store, path string, keepEmpty bool, beforeRename func() error, next func() (T, error)) (int64, error) {
	if err := mkdirAllSync(filepath.Dir(path), s.dirMode); err != nil {
		return 0, err
	}
	tmp := path + tmpSuffix
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, s.fileMode)
	if err != nil {
		return 0, err
	}
	if err := f.Chmod(s.fileMode); err != nil { // whatever the umask
		f.Close()
		return 0, err
	}
	defer os.Remove(tmp) // a no-op once renamed
	defer f.Close()
	w := parquet.NewGenericWriter[T](f, s.writerOptions()...)
	// Rows are written in batches, and row groups closed, by count and by size: a series
	// chunk carries up to a few MB of JSON, a heart-rate sample a few hundred bytes.
	buf := make([]T, 0, batchRows[T]())
	var n, bufBytes, groupBytes int64
	write := func() error {
		if _, err := w.Write(buf); err != nil {
			return err
		}
		n += int64(len(buf))
		groupBytes += bufBytes
		clear(buf)
		buf, bufBytes = buf[:0], 0
		if groupBytes >= maxRowGroupBytes {
			groupBytes = 0
			return w.Flush()
		}
		return nil
	}
	for {
		row, err := next()
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return 0, err
		}
		buf = append(buf, row)
		bufBytes += rowBytes(&row)
		if len(buf) == cap(buf) || bufBytes >= maxBatchBytes {
			if err := write(); err != nil {
				return 0, err
			}
		}
	}
	if len(buf) > 0 {
		if err := write(); err != nil {
			return 0, err
		}
	}
	if err := w.Close(); err != nil {
		return 0, err
	}
	if n == 0 && !keepEmpty {
		f.Close()
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return 0, err
		}
		return 0, syncDir(filepath.Dir(path))
	}
	if err := f.Sync(); err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	if beforeRename != nil {
		if err := beforeRename(); err != nil {
			return 0, err
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, err
	}
	return n, syncDir(filepath.Dir(path))
}

// readParquet calls fn for each row of the file at path, streaming (a page per column at a time).
func readParquet[T any](path string, fn func(*T) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	pf, err := parquet.OpenFile(f, st.Size(), parquet.SkipBloomFilters(true), parquet.SkipPageIndex(true))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	r := parquet.NewGenericReader[T](pf)
	defer r.Close()
	buf := make([]T, batchRows[T]())
	for {
		n, err := r.Read(buf)
		for i := range buf[:n] {
			if ferr := fn(&buf[i]); ferr != nil {
				return ferr
			}
		}
		clear(buf[:n])
		if errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
}

// columnRange returns the smallest and largest value of a string column in a file, from its
// row group statistics; ok is false when a row group has none.
func columnRange(path, column string) (lo, hi string, ok bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", "", false, err
	}
	pf, err := parquet.OpenFile(f, st.Size(), parquet.SkipBloomFilters(true), parquet.SkipPageIndex(true))
	if err != nil {
		return "", "", false, fmt.Errorf("%s: %w", path, err)
	}
	leaf, found := pf.Schema().Lookup(column)
	if !found {
		return "", "", false, nil
	}
	first := true
	for _, rg := range pf.Metadata().RowGroups {
		if rg.NumRows == 0 {
			continue
		}
		stats := rg.Columns[leaf.ColumnIndex].MetaData.Statistics
		if stats.MinValue == nil || stats.MaxValue == nil || stats.NullCount > 0 {
			return "", "", false, nil
		}
		mn, mx := string(stats.MinValue), string(stats.MaxValue)
		if first || mn < lo {
			lo = mn
		}
		if first || mx > hi {
			hi = mx
		}
		first = false
	}
	return lo, hi, !first, nil
}

// partitionFiles lists the Parquet files of every partition of a dataset, by directory.
func (s *Store) partitionFiles(dataset string) (map[string][]string, error) {
	out := map[string][]string{}
	root := filepath.Join(s.parquet, dataset)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && path == root {
			return filepath.SkipDir
		}
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".parquet") {
			out[filepath.Dir(path)] = append(out[filepath.Dir(path)], path)
		}
		return nil
	})
	return out, err
}

// removeTemps deletes the temp files a crash left behind (never renamed, so never part of
// the archive).
func removeTemps(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, tmpSuffix) {
			return os.Remove(path)
		}
		return nil
	})
}

// mkdirAllSync creates dir and its parents, fsyncing each new directory's parent so the
// entry survives a crash.
func mkdirAllSync(dir string, mode os.FileMode) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	parent := filepath.Dir(dir)
	if parent != dir {
		if err := mkdirAllSync(parent, mode); err != nil {
			return err
		}
	}
	if err := os.Mkdir(dir, mode); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if err := os.Chmod(dir, mode); err != nil { // whatever the umask
		return err
	}
	return syncDir(parent)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// removeEmptyDirs removes the empty partition directories under a dataset after an erase.
func removeEmptyDirs(root string) {
	var dirs []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i]) // fails, as intended, unless empty
	}
}

// Conversions from received JSON to rows.

func micros(t time.Time) int64 { return t.UnixMicro() }

func ptr[T any](v T) *T { return &v }

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func parseTimeMicros(s string) *int64 {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	return ptr(t.UnixMicro())
}

// sampleRow builds the row of a stored sample or record from its staged JSON.
func sampleRow(dataset, typ, uploader string, wire int, receivedNs int64, raw string) Row {
	r := Row{Type: typ, Uploader: uploader, Wire: int32(wire), ReceivedAt: receivedNs / 1000, JSON: raw}
	var v struct {
		UUID       string   `json:"uuid"`
		Kind       string   `json:"kind"`
		Key        string   `json:"key"`
		SampleUUID string   `json:"sample_uuid"`
		Start      string   `json:"start"`
		End        string   `json:"end"`
		Value      *float64 `json:"value"`
		Unit       string   `json:"unit"`
		Source     struct {
			Name     string `json:"name"`
			BundleID string `json:"bundle_id"`
			Version  string `json:"version"`
			Device   string `json:"device"`
		} `json:"source"`
		Device json.RawMessage `json:"device"`
	}
	_ = json.Unmarshal([]byte(raw), &v) // validated on receipt; a field of an odd type stays nil
	var device struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(v.Device, &device)
	r.Kind = v.Kind
	r.Start, r.End = parseTimeMicros(v.Start), parseTimeMicros(v.End)
	r.Unit = optString(v.Unit)
	r.SourceName, r.SourceBundle, r.SourceVersion = optString(v.Source.Name), optString(v.Source.BundleID), optString(v.Source.Version)
	r.Device = optString(cmpOr(device.Name, v.Source.Device))
	if dataset == "samples" {
		r.UUID = optString(strings.ToUpper(v.UUID))
		r.Value = v.Value
	} else {
		r.Kind = "record"
		r.Key = optString(v.Key)
		r.UUID = optString(strings.ToUpper(v.SampleUUID))
		r.Value = nil
	}
	return r
}

// chunk is a series record, as the app sends it.
type chunk struct {
	SampleUUID string            `json:"sample_uuid"`
	Index      int32             `json:"index"`
	Unit       string            `json:"unit"`
	Points     []json.RawMessage `json:"points"`
}

func parseChunk(raw string) (chunk, error) {
	var c chunk
	err := json.Unmarshal([]byte(raw), &c)
	c.SampleUUID = strings.ToUpper(c.SampleUUID)
	return c, err
}

// num reads a JSON number, or nil for null, absent or not a number.
func num(m map[string]json.RawMessage, k string) *float64 {
	var f *float64
	if raw, ok := m[k]; ok && json.Unmarshal(raw, &f) == nil {
		return f
	}
	return nil
}

func str(m map[string]json.RawMessage, k string) string {
	var s string
	if raw, ok := m[k]; ok {
		json.Unmarshal(raw, &s)
	}
	return s
}

// pointTime is the absolute time of a point: start (ns) plus t seconds, if both are known.
func pointTime(parentStart *int64, t float64) *int64 {
	if parentStart == nil || math.IsNaN(t) || math.Abs(t) > 1e8 { // a series lasts minutes
		return nil
	}
	return ptr((*parentStart + int64(t*1e9)) / 1000)
}
