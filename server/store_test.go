package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const hbUUID = "6B4E28BA-2FA1-11D2-883F-0016D3CCA427"

const hbSample = `{"uuid":"6b4e28ba-2fa1-11d2-883f-0016d3cca427","type":"HKDataTypeIdentifierHeartbeatSeries","kind":"heartbeat_series",
 "start":"2025-12-31T23:59:00Z","end":"2025-12-31T23:59:30Z","source":{"name":"Watch"},"count":2}`

const hbChunk = `{"kind":"series","key":"6B4E28BA-2FA1-11D2-883F-0016D3CCA427/heartbeat/0","series":"heartbeat",
 "sample_uuid":"6B4E28BA-2FA1-11D2-883F-0016D3CCA427","sample_type":"HKDataTypeIdentifierHeartbeatSeries","index":0,"final":true,
 "points":[{"t":0.5,"gap":false},{"t":1.5,"gap":true}]}`

const routeSample = `{"uuid":"7b4e28ba-2fa1-11d2-883f-0016d3cca427","type":"HKWorkoutRouteTypeIdentifier","kind":"workout_route",
 "start":"2026-02-03T10:00:00Z","end":"2026-02-03T10:30:00Z","source":{"name":"Watch"}}`

const routeChunk = `{"kind":"series","key":"7B4E28BA-2FA1-11D2-883F-0016D3CCA427/workout_route/0","series":"workout_route",
 "sample_uuid":"7B4E28BA-2FA1-11D2-883F-0016D3CCA427","sample_type":"HKWorkoutRouteTypeIdentifier","index":0,"final":true,
 "points":[{"t":"2026-02-03T10:00:00.000Z","lat":45.5,"lon":-73.6,"alt":30,"h_acc":5,"v_acc":3,"speed":2.5,"speed_acc":0.5,
  "course":90,"course_acc":10,"ellipsoidal_alt":12},{"t":"2026-02-03T10:00:01.000Z","lat":45.50001,"lon":-73.60001,"alt":31,"floor":2}]}`

const stepsChunk = `{"kind":"series","key":"8B4E28BA-2FA1-11D2-883F-0016D3CCA427/quantity/0","series":"quantity","unit":"count",
 "sample_uuid":"8B4E28BA-2FA1-11D2-883F-0016D3CCA427","sample_type":"HKQuantityTypeIdentifierStepCount","index":0,"final":true,
 "points":[{"start":"2026-03-01T10:00:00.000Z","end":"2026-03-01T10:01:00.000Z","value":12},{"start":"2026-03-01T10:01:00.000Z","end":"2026-03-01T10:02:00.000Z","value":30}]}`

func mustPost(t *testing.T, url, token, body string) Response {
	t.Helper()
	resp, r := post(t, url, token, body)
	if resp.StatusCode != 200 {
		t.Fatalf("post: %d", resp.StatusCode)
	}
	return r
}

// files lists the Parquet files under dir/parquet, relative to it.
func files(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.Walk(filepath.Join(dir, "parquet"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			rel, _ := filepath.Rel(filepath.Join(dir, "parquet"), p)
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func readAll[T any](t *testing.T, dir, glob string) []T {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(dir, "parquet", glob))
	sort.Strings(paths)
	var out []T
	for _, p := range paths {
		if err := readParquet(p, func(r *T) error { out = append(out, *r); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestFlushLayout(t *testing.T) {
	dir := t.TempDir()
	ts, _ := newTestServer(t, dir)
	// The heartbeat chunk comes a batch before its sample.
	mustPost(t, ts.URL, testToken, `{"version":2,"samples":[],"records":[`+hbChunk+`]}`)
	r := mustPost(t, ts.URL, testToken, `{"version":2,"samples":[`+hrSample+`,`+hbSample+`,`+ecgV2+`,`+routeSample+`],
		"records":[`+ecgChunk+`,`+routeChunk+`,`+stepsChunk+`,`+summary+`,`+characteristics+`],"deletions":[`+deletion+`]}`)
	if r.Stored != 10 {
		t.Fatalf("post: %+v", r)
	}
	n, err := ts.store.Flush(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 10 || ts.store.Staged() != 0 {
		t.Fatalf("flushed %d, %d left staged", n, ts.store.Staged())
	}
	thisMonth := time.Now().UTC().Format("2006-01")
	want := []string{
		"deleted/deleted.parquet",
		// The ECG chunk names no sample_type: its points take their sample's.
		"ecg_voltages/type=HKDataTypeIdentifierElectrocardiogram/month=2026-01/",
		"heartbeats/type=HKDataTypeIdentifierHeartbeatSeries/month=2025-12/",
		"quantity_series/type=HKQuantityTypeIdentifierStepCount/month=2026-03/",
		"records/type=activity_summary/month=2026-01/",
		"records/type=characteristics/month=" + thisMonth + "/",
		"records/type=series/month=2025-12/",
		"records/type=series/month=2026-01/",
		"records/type=series/month=2026-02/",
		"records/type=series/month=2026-03/",
		"route_points/type=HKWorkoutRouteTypeIdentifier/month=2026-02/",
		"samples/type=HKDataTypeIdentifierElectrocardiogram/month=2026-01/",
		"samples/type=HKDataTypeIdentifierHeartbeatSeries/month=2025-12/",
		"samples/type=HKQuantityTypeIdentifierHeartRate/month=2026-01/",
		"samples/type=HKWorkoutRouteTypeIdentifier/month=2026-02/",
	}
	sort.Strings(want)
	got := files(t, dir)
	if len(got) != len(want) {
		t.Fatalf("files:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) || !strings.HasSuffix(got[i], ".parquet") {
			t.Errorf("file %d: %s, want %s…", i, got[i], want[i])
		}
	}

	hr := readAll[Row](t, dir, "samples/type=HKQuantityTypeIdentifierHeartRate/*/*.parquet")
	if len(hr) != 1 || *hr[0].UUID != "1B4E28BA-2FA1-11D2-883F-0016D3CCA427" || *hr[0].Value != 62 || *hr[0].Unit != "count/min" ||
		*hr[0].SourceName != "Watch" || *hr[0].SourceBundle != "com.apple.health" || hr[0].Wire != 2 || hr[0].Kind != "quantity" ||
		*hr[0].Start != time.Date(2026, 1, 2, 3, 4, 5, 123e6, time.UTC).UnixMicro() || !strings.Contains(hr[0].JSON, `"future_field"`) {
		t.Errorf("heart rate row: %+v", hr)
	}
	beats := readAll[Heartbeat](t, dir, "heartbeats/*/*/*.parquet")
	start := time.Date(2025, 12, 31, 23, 59, 0, 0, time.UTC)
	if len(beats) != 2 || beats[1].SampleUUID != hbUUID || beats[1].T != 1.5 || !beats[1].Gap || beats[1].I != 1 ||
		*beats[1].Time != start.Add(1500*time.Millisecond).UnixMicro() {
		t.Errorf("beats: %+v", beats)
	}
	route := readAll[RoutePoint](t, dir, "route_points/*/*/*.parquet")
	if len(route) != 2 || *route[0].Lat != 45.5 || *route[0].Course != 90 || route[0].Floor != nil || *route[1].Floor != 2 ||
		*route[1].Time != time.Date(2026, 2, 3, 10, 0, 1, 0, time.UTC).UnixMicro() {
		t.Errorf("route: %+v", route)
	}
	steps := readAll[QuantityPoint](t, dir, "quantity_series/*/*/*.parquet")
	if len(steps) != 2 || *steps[1].Value != 30 || *steps[1].Unit != "count" {
		t.Errorf("steps: %+v", steps)
	}
	ecg := readAll[Voltage](t, dir, "ecg_voltages/*/*/*.parquet")
	if len(ecg) != 1 || *ecg[0].Microvolts != 12.5 || *ecg[0].Time != time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC).UnixMicro() {
		t.Errorf("ecg: %+v", ecg)
	}
	chunks := readAll[Row](t, dir, "records/type=series/*/*.parquet")
	if len(chunks) != 4 || chunks[0].Kind != "record" || chunks[0].UUID == nil || chunks[0].Key == nil || !strings.Contains(chunks[0].JSON, `"points"`) {
		t.Errorf("series records: %+v", chunks)
	}
	del := readAll[DeletedRow](t, dir, "deleted/deleted.parquet")
	if len(del) != 1 || del[0].UUID != "3B4E28BA-2FA1-11D2-883F-0016D3CCA427" || del[0].Wire != 0 || del[0].Reason != "deleted" {
		t.Errorf("deleted: %+v", del)
	}
}

func TestChunkWaitsForItsSample(t *testing.T) {
	dir := t.TempDir()
	ts, _ := newTestServer(t, dir)
	mustPost(t, ts.URL, testToken, `{"version":2,"samples":[],"records":[`+hbChunk+`]}`)
	if n, _ := ts.store.Flush(context.Background(), false); n != 0 || ts.store.Staged() != 1 {
		t.Fatalf("flushed %d, staged %d: the chunk should wait for its sample", n, ts.store.Staged())
	}
	// A day later, it goes to the month it was received in, without absolute times.
	ts.store.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if n, _ := ts.store.Flush(context.Background(), false); n != 1 || ts.store.Staged() != 0 {
		t.Fatalf("after a day: flushed %d, staged %d", n, ts.store.Staged())
	}
	beats := readAll[Heartbeat](t, dir, "heartbeats/type=HKDataTypeIdentifierHeartbeatSeries/month="+time.Now().UTC().Format("2006-01")+"/*.parquet")
	if len(beats) != 2 || beats[0].Time != nil {
		t.Fatalf("beats: %+v", beats)
	}
}

var errCrash = errors.New("simulated crash")

// crashAt makes the store fail at the nth time it reaches stage.
func crashAt(s *Store, stage string, nth int) {
	s.hook = func(st string) error {
		if st == stage {
			if nth--; nth == 0 {
				return errCrash
			}
		}
		return nil
	}
}

func rowCount(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	s := &Store{parquet: filepath.Join(dir, "parquet")}
	n, err := s.ParquetCounts()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func batchOf(n int, start time.Time, typ string) string {
	var b strings.Builder
	b.WriteString(`{"version":2,"samples":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		ts := start.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
		b.WriteString(`{"uuid":"` + uuidN(start, i) + `","type":"` + typ + `","kind":"quantity","start":"` + ts + `","end":"` + ts +
			`","value":` + string(rune('0'+i%10)) + `,"unit":"count","source":{"name":"Watch"}}`)
	}
	b.WriteString(`],"records":[]}`)
	return b.String()
}

// uuidN is a distinct UUID per (start, i).
func uuidN(start time.Time, i int) string {
	s := []byte("00000000-0000-4000-8000-000000000000")
	v := uint64(start.Unix())<<20 | uint64(i)
	for p := len(s) - 1; p >= 0 && v > 0; p-- {
		if s[p] == '-' {
			continue
		}
		s[p] = "0123456789abcdef"[v%16]
		v /= 16
	}
	return string(s)
}

func TestFlushCrashSafe(t *testing.T) {
	for stage, nth := range map[string]int{"flush-file": 2, "flush-commit": 1} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			ts, _ := newTestServer(t, dir)
			// 100 hourly samples: five days in January, and a type in February.
			mustPost(t, ts.URL, testToken, batchOf(100, time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC), "HKQuantityTypeIdentifierStepCount"))
			mustPost(t, ts.URL, testToken, batchOf(10, time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC), "HKQuantityTypeIdentifierHeartRate"))
			crashAt(ts.store, stage, nth)
			if _, err := ts.store.Flush(context.Background(), false); !errors.Is(err, errCrash) {
				t.Fatalf("flush: %v", err)
			}
			ts.Close()

			ts2, _ := newTestServer(t, dir) // replays the flush
			got := rowCount(t, dir)
			if got["samples/HKQuantityTypeIdentifierStepCount"] != 100 || got["samples/HKQuantityTypeIdentifierHeartRate"] != 10 || ts2.store.Staged() != 0 {
				t.Fatalf("after recovery: %v, %d staged", got, ts2.store.Staged())
			}
			if r := mustPost(t, ts2.URL, testToken, batchOf(100, time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC), "HKQuantityTypeIdentifierStepCount")); r.Stored != 0 {
				t.Fatalf("re-sent after recovery: %+v", r)
			}
			if n, err := ts2.store.Flush(context.Background(), false); n != 0 || err != nil {
				t.Fatalf("second flush: %d %v", n, err)
			}
		})
	}
}

func TestCompaction(t *testing.T) {
	dir := t.TempDir()
	ts, _ := newTestServer(t, dir)
	ctx := context.Background()
	jan := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	// A sample sent in v1, then again in v2: the v1 copy goes at compaction.
	mustPost(t, ts.URL, testToken, `{"samples":[`+hrSample+`,`+sleepSample+`]}`)
	ts.store.Flush(ctx, false)
	mustPost(t, ts.URL, testToken, strings.Replace(batchOf(3, jan, "HKQuantityTypeIdentifierHeartRate"), `"samples":[`, `"samples":[`+hrSample+`,`, 1))
	ts.store.Flush(ctx, false)
	mustPost(t, ts.URL, testToken, batchOf(3, jan.Add(48*time.Hour), "HKQuantityTypeIdentifierHeartRate"))
	// One of the first three is deleted.
	mustPost(t, ts.URL, testToken, `{"version":2,"samples":[],"records":[],"deletions":[{"uuid":"`+uuidN(jan, 1)+`","type":"HKQuantityTypeIdentifierHeartRate"}]}`)
	ts.store.Flush(ctx, false)

	hrDir := filepath.Join(dir, "parquet", "samples", "type=HKQuantityTypeIdentifierHeartRate", "month=2026-01")
	sleepFile, _ := filepath.Glob(filepath.Join(dir, "parquet", "samples", "type=HKCategoryTypeIdentifierSleepAnalysis", "*", "*.parquet"))
	before, _ := os.Stat(sleepFile[0])
	if in, _ := filepath.Glob(filepath.Join(hrDir, "*.parquet")); len(in) != 3 {
		t.Fatalf("heart rate files before: %v", in)
	}

	if n, err := ts.store.Compact(ctx); err != nil || n != 1 {
		t.Fatalf("compact: %d %v", n, err)
	}
	out, _ := filepath.Glob(filepath.Join(hrDir, "*.parquet"))
	if len(out) != 1 || !strings.HasPrefix(filepath.Base(out[0]), "c") {
		t.Fatalf("heart rate files after: %v", out)
	}
	rows := readAll[Row](t, dir, "samples/type=HKQuantityTypeIdentifierHeartRate/*/*.parquet")
	if len(rows) != 6 { // 1 (v2 only) + 3 - 1 deleted + 3
		t.Fatalf("rows: %d", len(rows))
	}
	for _, r := range rows {
		if r.Wire == 1 || *r.UUID == strings.ToUpper(uuidN(jan, 1)) {
			t.Errorf("kept %s wire %d", *r.UUID, r.Wire)
		}
	}
	// The sleep month had one file: not rewritten.
	after, err := os.Stat(sleepFile[0])
	if err != nil || !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("untouched partition rewritten: %v", err)
	}
	if n, _ := ts.store.Compact(ctx); n != 0 {
		t.Errorf("second compaction merged %d partitions", n)
	}
}

func TestCompactionCrashSafe(t *testing.T) {
	for _, stage := range []string{"compact-journaled", "compact-renamed"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			ts, _ := newTestServer(t, dir)
			ctx := context.Background()
			jan := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
			for i := 0; i < 3; i++ {
				mustPost(t, ts.URL, testToken, batchOf(5, jan.Add(time.Duration(i)*24*time.Hour), "HKQuantityTypeIdentifierHeartRate"))
				ts.store.Flush(ctx, false)
			}
			crashAt(ts.store, stage, 1)
			if _, err := ts.store.Compact(ctx); !errors.Is(err, errCrash) {
				t.Fatalf("compact: %v", err)
			}
			ts.Close()
			ts2, _ := newTestServer(t, dir)
			if got := rowCount(t, dir)["samples/HKQuantityTypeIdentifierHeartRate"]; got != 15 {
				t.Fatalf("rows after recovery: %d", got)
			}
			for _, f := range files(t, dir) {
				if strings.HasSuffix(f, tmpSuffix) {
					t.Errorf("temp file left: %s", f)
				}
			}
			ts2.store.Compact(ctx)
			hr, _ := filepath.Glob(filepath.Join(dir, "parquet", "samples", "type=HKQuantityTypeIdentifierHeartRate", "*", "*.parquet"))
			if len(hr) != 1 || rowCount(t, dir)["samples/HKQuantityTypeIdentifierHeartRate"] != 15 {
				t.Fatalf("after compaction: %v", hr)
			}
		})
	}
}

func pairDevice(t *testing.T, base string) PairResponse {
	t.Helper()
	code, p := pairWith(t, base, mint(t, base).Code, "")
	if code != 200 {
		t.Fatalf("pair: %d", code)
	}
	return p
}

func TestErase(t *testing.T) {
	dir := t.TempDir()
	ts, _ := newTestServer(t, dir)
	ctx := context.Background()
	a, b := pairDevice(t, ts.URL), pairDevice(t, ts.URL)
	marker := `"source":{"name":"MarkerSourceForEraseTest"}`
	mine := strings.ReplaceAll(batchOf(20, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "HKQuantityTypeIdentifierHeartRate"), `"source":{"name":"Watch"}`, marker)
	mustPost(t, ts.URL, a.Token, mine)
	mustPost(t, ts.URL, a.Token, `{"version":2,"samples":[`+hbSample+`],"records":[`+hbChunk+`],"deletions":[`+deletion+`]}`)
	mustPost(t, ts.URL, b.Token, batchOf(10, time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), "HKQuantityTypeIdentifierHeartRate"))
	mustPost(t, ts.URL, testToken, batchOf(5, time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC), "HKQuantityTypeIdentifierHeartRate"))
	ts.store.Flush(ctx, false)
	// And some still staged.
	mustPost(t, ts.URL, a.Token, `{"version":2,"samples":[`+hrSample+`],"records":[]}`)
	total := ts.store.Len()

	resp, body := call(t, "DELETE", ts.URL+"/v1/data", a.Token, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"erased":24`) || !strings.Contains(string(body), `"device":"`+a.Device.ID+`"`) {
		t.Fatalf("erase: %d %s", resp.StatusCode, body)
	}
	if ts.store.Len() != total-24 || ts.store.Staged() != 0 {
		t.Fatalf("after erase: %d records (was %d), %d staged", ts.store.Len(), total, ts.store.Staged())
	}
	got := rowCount(t, dir)
	if got["samples/HKQuantityTypeIdentifierHeartRate"] != 15 || got["heartbeats/HKDataTypeIdentifierHeartbeatSeries"] != 0 ||
		got["records/series"] != 0 || len(readAll[DeletedRow](t, dir, "deleted/deleted.parquet")) != 0 {
		t.Fatalf("parquet after erase: %v", got)
	}
	for _, r := range readAll[Row](t, dir, "samples/*/*/*.parquet") {
		if r.Uploader == a.Device.ID {
			t.Fatalf("row of the erased device left: %+v", r)
		}
	}
	// Nothing of it left on disk, not even in free pages or the log.
	for _, f := range []string{"staging.db", "staging.db-wal"} {
		raw, _ := os.ReadFile(filepath.Join(dir, f))
		if bytes.Contains(raw, []byte("MarkerSourceForEraseTest")) {
			t.Errorf("%s still holds erased data", f)
		}
	}
	// The device uploads its history again from scratch.
	if r := mustPost(t, ts.URL, a.Token, mine); r.Stored != 20 {
		t.Fatalf("re-upload: %+v", r)
	}
	// The server token erases what it uploaded, or a device's.
	if resp, body := call(t, "DELETE", ts.URL+"/v1/data", testToken, nil); resp.StatusCode != 200 || !strings.Contains(string(body), `"erased":5`) {
		t.Fatalf("admin erase: %d %s", resp.StatusCode, body)
	}
	if resp, body := call(t, "DELETE", ts.URL+"/v1/data?device="+b.Device.ID, testToken, nil); resp.StatusCode != 200 || !strings.Contains(string(body), `"erased":10`) {
		t.Fatalf("admin erase of a device: %d %s", resp.StatusCode, body)
	}
	if resp, _ := call(t, "DELETE", ts.URL+"/v1/data", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("erase without token: %d", resp.StatusCode)
	}
}

func TestEraseResumesAfterCrash(t *testing.T) {
	dir := t.TempDir()
	ts, _ := newTestServer(t, dir)
	ctx := context.Background()
	a := pairDevice(t, ts.URL)
	for i := 0; i < 3; i++ {
		mustPost(t, ts.URL, a.Token, batchOf(5, time.Date(2026, time.Month(1+i), 1, 0, 0, 0, 0, time.UTC), "HKQuantityTypeIdentifierHeartRate"))
	}
	mustPost(t, ts.URL, testToken, batchOf(5, time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC), "HKQuantityTypeIdentifierHeartRate"))
	ts.store.Flush(ctx, false)
	crashAt(ts.store, "erase-file", 2)
	if _, err := ts.store.Erase(ctx, a.Device.ID); !errors.Is(err, errCrash) {
		t.Fatalf("erase: %v", err)
	}
	ts.Close()
	ts2, _ := newTestServer(t, dir) // finishes the erase before serving
	if got := rowCount(t, dir)["samples/HKQuantityTypeIdentifierHeartRate"]; got != 5 || ts2.store.Len() != 5 {
		t.Fatalf("after recovery: %d rows, %d records", got, ts2.store.Len())
	}
	if r := mustPost(t, ts2.URL, a.Token, batchOf(5, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "HKQuantityTypeIdentifierHeartRate")); r.Stored != 5 {
		t.Fatalf("re-upload: %+v", r)
	}
}

func TestMigrate(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "2026"), 0o750)
	recv := `"received_at":"2026-10-17T12:00:00Z"`
	digest := func(raw string) string {
		r, err := parseRecord([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return r.digest
	}
	hrV2 := strings.Replace(hrSample, `"future_field"`, `"device":{"name":"Apple Watch"},"future_field"`, 1)
	lines := []string{
		`{"op":"sample","uuid":"1B4E28BA-2FA1-11D2-883F-0016D3CCA427","type":"HKQuantityTypeIdentifierHeartRate",` + recv + `,"data":` + hrSample + `}`,
		`{"op":"sample","uuid":"2B4E28BA-2FA1-11D2-883F-0016D3CCA427","type":"HKCategoryTypeIdentifierSleepAnalysis",` + recv + `,"data":` + sleepSample + `}`,
		`{"op":"record","type":"series","key":"6B4E28BA-2FA1-11D2-883F-0016D3CCA427/heartbeat/0","digest":"d1",` + recv + `,"data":` + hbChunk + `}`,
		`{"op":"sample","uuid":"1B4E28BA-2FA1-11D2-883F-0016D3CCA427","v":2,"type":"HKQuantityTypeIdentifierHeartRate",` + recv + `,"data":` + hrV2 + `}`,
		`{"op":"sample","uuid":"6B4E28BA-2FA1-11D2-883F-0016D3CCA427","v":2,"type":"HKDataTypeIdentifierHeartbeatSeries",` + recv + `,"data":` + hbSample + `}`,
		`{"op":"record","type":"activity_summary","key":"2026-01-02","digest":"` + digest(summary) + `",` + recv + `,"data":` + summary + `}`,
		`{"op":"delete","uuid":"3B4E28BA-2FA1-11D2-883F-0016D3CCA427","type":"HKQuantityTypeIdentifierStepCount",` + recv + `,"data":` + deletion + `}`,
		`{"op":"sample","uu`, // cut short by a crash
	}
	for i := range lines {
		lines[i] = strings.ReplaceAll(lines[i], "\n", "")
	}
	os.WriteFile(filepath.Join(dir, "2026", "2026-10-17.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o640)
	os.WriteFile(filepath.Join(dir, "devices.json"), []byte(`{"devices":[{"id":"abcd1234","name":"iPhone","token_sha256":"x"}]}`), 0o600)

	var out bytes.Buffer
	if err := runMigrate([]string{"-archive-dir", dir, "-flush-rows", "2"}, &out); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "counts match") || !strings.Contains(out.String(), "1 of them v1 copies") {
		t.Errorf("output:\n%s", out.String())
	}
	hr := readAll[Row](t, dir, "samples/type=HKQuantityTypeIdentifierHeartRate/*/*.parquet")
	sleep := readAll[Row](t, dir, "samples/type=HKCategoryTypeIdentifierSleepAnalysis/*/*.parquet")
	if len(hr) != 1 || hr[0].Wire != 2 || *hr[0].Device != "Apple Watch" || hr[0].Uploader != "abcd1234" ||
		hr[0].ReceivedAt != time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC).UnixMicro() {
		t.Errorf("heart rate: %+v", hr)
	}
	if len(sleep) != 1 || sleep[0].Wire != 1 {
		t.Errorf("sleep (v1 only): %+v", sleep)
	}
	beats := readAll[Heartbeat](t, dir, "heartbeats/type=HKDataTypeIdentifierHeartbeatSeries/month=2025-12/*.parquet")
	if len(beats) != 2 || beats[0].Time == nil {
		t.Errorf("beats: %+v", beats)
	}
	// Uploads after the migration are de-duplicated against it.
	ts, _ := newTestServer(t, dir)
	if r := mustPost(t, ts.URL, testToken, `{"samples":[`+hrSample+`,`+sleepSample+`]}`); r.Stored != 0 {
		t.Errorf("v1 after migration: %+v", r)
	}
	if r := mustPost(t, ts.URL, testToken, `{"version":2,"samples":[`+hrV2+`],"records":[`+summary+`]}`); r.Stored != 0 {
		t.Errorf("v2 after migration: %+v", r)
	}
}
