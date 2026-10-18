package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// peakHeap runs fn and returns the largest heap in use while it ran (sampled every 2 ms),
// with the garbage collector set to keep the heap close to what is live (GOGC=25).
func peakHeap(fn func()) uint64 {
	defer debug.SetGCPercent(debug.SetGCPercent(25))
	runtime.GC()
	var peak atomic.Uint64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var ms runtime.MemStats
		for {
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse > peak.Load() {
				peak.Store(ms.HeapInuse)
			}
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	fn()
	close(stop)
	wg.Wait()
	return peak.Load()
}

// syntheticJSON is a sample about the size of a real v2 heart-rate sample (~700 bytes), with
// enough randomness not to compress away.
func syntheticJSON(r *rand.Rand, i int) string {
	b := make([]byte, 160)
	for j := range b {
		b[j] = byte(r.IntN(256))
	}
	return `{"uuid":"` + strconv.Itoa(i) + `","type":"HKQuantityTypeIdentifierHeartRate","kind":"quantity","value":62,"unit":"count/min",` +
		`"source":{"name":"Apple Watch","bundle_id":"com.apple.health.0000"},"metadata":{"noise":"` + hex.EncodeToString(b) + `"}}`
}

// writeSyntheticPartition writes rows of synthetic samples to files in one partition.
func writeSyntheticPartition(t *testing.T, s *Store, files, rowsPerFile int) {
	t.Helper()
	r := rand.New(rand.NewPCG(1, 2))
	dir := s.partitionDir("samples", "HKQuantityTypeIdentifierHeartRate", "2026-01")
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for f := 0; f < files; f++ {
		i := 0
		_, err := writeParquet(s, filepath.Join(dir, fmt.Sprintf("f%d.parquet", f+1)), func() (Row, error) {
			if i == rowsPerFile {
				return Row{}, io.EOF
			}
			n := f*rowsPerFile + i
			i++
			uuid := fmt.Sprintf("%08X-0000-4000-8000-000000000000", n)
			at := start.Add(time.Duration(n) * time.Second).UnixMicro()
			return Row{UUID: &uuid, Type: "HKQuantityTypeIdentifierHeartRate", Kind: "quantity", Start: &at, End: &at,
				Value: ptr(62.0), Unit: ptr("count/min"), Uploader: "abcd1234", Wire: 2, ReceivedAt: at, JSON: syntheticJSON(r, n)}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func dirSize(dir string) int64 {
	var n int64
	filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// Compaction and flush hold one row group at a time, so their memory does not grow with the
// archive: the peak heap is the same for 4x the rows, and stays under 64 MB for 400 000
// samples (200 MB as received). HKR_MEMORY_ROWS sets the larger size.
func TestMemoryBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a few hundred MB")
	}
	large := 400_000
	if v, err := strconv.Atoi(os.Getenv("HKR_MEMORY_ROWS")); err == nil {
		large = v
	}
	const limit = 64 << 20
	ctx := context.Background()

	compactPeak := func(rows int) uint64 {
		s, err := OpenStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		s.RowGroupRows = 20_000
		writeSyntheticPartition(t, s, 8, rows/8)
		peak := peakHeap(func() {
			if _, err := s.Compact(ctx); err != nil {
				t.Error(err)
			}
		})
		got := rowCount(t, s.dir)["samples/HKQuantityTypeIdentifierHeartRate"]
		if got != int64(rows/8*8) {
			t.Errorf("compacted %d rows, want %d", got, rows)
		}
		t.Logf("compaction of %d rows (%d MB of Parquet): peak heap %d MB", rows, dirSize(s.parquet)>>20, peak>>20)
		return peak
	}
	small, big := compactPeak(large/4), compactPeak(large)
	if big > limit || big > small*3/2+8<<20 {
		t.Errorf("compaction peak heap: %d MB for %d rows, %d MB for %d: not bounded", small>>20, large/4, big>>20, large)
	}

	flushPeak := func(rows int) uint64 {
		s, err := OpenStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		s.RowGroupRows = 20_000
		r := rand.New(rand.NewPCG(3, 4))
		start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		batch := make([]Item, 0, 5000)
		for i := 0; i < rows; i++ {
			at := start.Add(time.Duration(i) * time.Second)
			batch = append(batch, Item{Op: "sample", UUID: fmt.Sprintf("%08X-0000-4000-8000-000000000000", i), Wire: 2,
				Type: "HKQuantityTypeIdentifierHeartRate", Kind: "quantity", Start: at, Data: []byte(syntheticJSON(r, i))})
			if len(batch) == cap(batch) || i == rows-1 {
				if _, err := s.Ingest("abcd1234", batch); err != nil {
					t.Fatal(err)
				}
				batch = batch[:0]
			}
		}
		peak := peakHeap(func() {
			if _, err := s.Flush(ctx, true); err != nil {
				t.Error(err)
			}
		})
		t.Logf("flush of %d staged rows: peak heap %d MB", rows, peak>>20)
		return peak
	}
	small, big = flushPeak(large/8), flushPeak(large/2)
	if big > limit || big > small*3/2+8<<20 {
		t.Errorf("flush peak heap: %d MB for %d rows, %d MB for %d: not bounded", small>>20, large/8, big>>20, large/2)
	}
}

// Series chunks are rows of up to a few MB (the JSON of a route or ECG chunk): batches and
// row groups are bounded by size too, so a month of them merges in the same memory.
func TestMemoryBoundedLargeRows(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a few hundred MB")
	}
	peak := func(rows int) uint64 {
		s, err := OpenStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		r := rand.New(rand.NewPCG(5, 6))
		dir := s.partitionDir("records", "series", "2026-01")
		for f := 0; f < 4; f++ {
			i := 0
			if _, err := writeParquet(s, filepath.Join(dir, fmt.Sprintf("f%d.parquet", f+1)), func() (Row, error) {
				if i == rows/4 {
					return Row{}, io.EOF
				}
				i++
				// ~1 MB: points of a route chunk, with random digits.
				var b strings.Builder
				b.WriteString(`{"kind":"series","points":[`)
				for p := 0; p < 7000; p++ {
					fmt.Fprintf(&b, `{"lat":%.7f,"lon":%.7f,"alt":%.2f,"speed":%.3f},`, r.Float64()*90, r.Float64()*180, r.Float64()*300, r.Float64()*5)
				}
				b.WriteString(`{}]}`)
				return Row{Type: "series", Kind: "record", Uploader: "abcd1234", Wire: 2, JSON: b.String()}, nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		p := peakHeap(func() {
			if _, err := s.Compact(context.Background()); err != nil {
				t.Error(err)
			}
		})
		t.Logf("compaction of %d rows of ~1 MB (%d MB of Parquet): peak heap %d MB", rows, dirSize(s.parquet)>>20, p>>20)
		return p
	}
	small, big := peak(100), peak(400)
	if big > 96<<20 || big > small*3/2+8<<20 {
		t.Errorf("peak heap: %d MB for 100 rows, %d MB for 400: not bounded", small>>20, big>>20)
	}
}

// Migration reads lines of any size (a route chunk is about 0.5 MB) in batches bounded by
// size: memory is the same for 4x the lines.
func TestMigrateMemoryBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a few hundred MB")
	}
	peak := func(chunks int) uint64 {
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, "2026"), 0o750)
		f, err := os.Create(filepath.Join(dir, "2026", "2026-10-17.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		r := rand.New(rand.NewPCG(7, 8))
		for c := 0; c < chunks; c++ {
			uuid := fmt.Sprintf("%08X-0000-4000-8000-000000000000", c)
			var b strings.Builder
			fmt.Fprintf(&b, `{"kind":"series","key":"%s/workout_route/0","series":"workout_route","sample_uuid":"%s","sample_type":"HKWorkoutRouteTypeIdentifier","index":0,"final":true,"points":[`, uuid, uuid)
			for p := 0; p < 4000; p++ {
				if p > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, `{"t":"2026-01-01T10:00:00.000Z","lat":%.7f,"lon":%.7f,"alt":%.2f,"speed":%.3f}`, r.Float64()*90, r.Float64()*180, r.Float64()*300, r.Float64()*5)
			}
			b.WriteString(`]}`)
			fmt.Fprintf(f, `{"op":"record","type":"series","key":"%s/workout_route/0","digest":"d%d","received_at":"2026-10-17T12:00:00Z","data":%s}`+"\n", uuid, c, b.String())
		}
		f.Close()
		var out strings.Builder
		p := peakHeap(func() {
			if err := runMigrate([]string{"-archive-dir", dir, "-uploader", "abcd1234"}, &out); err != nil {
				t.Errorf("migrate: %v\n%s", err, out.String())
			}
		})
		t.Logf("migration of %d lines of ~0.5 MB: peak heap %d MB", chunks, p>>20)
		return p
	}
	small, big := peak(100), peak(400)
	if big > 96<<20 || big > small*3/2+8<<20 {
		t.Errorf("peak heap: %d MB for 100 lines, %d MB for 400: not bounded", small>>20, big>>20)
	}
}
