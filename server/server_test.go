package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testToken = "test-token-0123456789"

const hrSample = `{"uuid":"1b4e28ba-2fa1-11d2-883f-0016d3cca427","type":"HKQuantityTypeIdentifierHeartRate","kind":"quantity",
 "start":"2026-01-02T03:04:05.123Z","end":"2026-01-02T03:04:05.123Z","value":62,"unit":"count/min",
 "source":{"name":"Watch","bundle_id":"com.apple.health"},"future_field":{"kept":true}}`

const sleepSample = `{"uuid":"2b4e28ba-2fa1-11d2-883f-0016d3cca427","type":"HKCategoryTypeIdentifierSleepAnalysis","kind":"category",
 "start":"2026-01-02T01:00:00Z","end":"2026-01-02T01:30:00Z","value":4,"value_label":"asleepDeep","source":{"name":"Watch"}}`

const deletion = `{"uuid":"3b4e28ba-2fa1-11d2-883f-0016d3cca427","type":"HKQuantityTypeIdentifierStepCount"}`

type fakeMetrics struct {
	mu    sync.Mutex
	lines []string
}

func (f *fakeMetrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			f.lines = append(f.lines, l)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// liveServer is an httptest server and its store; Close closes both (the store locks dir).
type liveServer struct {
	*httptest.Server
	store *Store
	once  sync.Once
}

func (ts *liveServer) Close() {
	ts.once.Do(func() {
		ts.Server.Close()
		ts.store.Close()
	})
}

func newTestServer(t *testing.T, dir string) (*liveServer, *fakeMetrics) {
	t.Helper()
	a, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	fm := &fakeMetrics{}
	metrics := httptest.NewServer(fm)
	t.Cleanup(metrics.Close)
	devices, err := OpenDevices(filepath.Join(dir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Archive: a, Token: testToken, MaxBodyBytes: 1 << 20, Devices: devices, Codes: NewCodes(),
		Forwarder: &Forwarder{URL: metrics.URL, Measurement: "healthkit", Client: metrics.Client()}}
	ts := &liveServer{Server: httptest.NewServer(s.Handler()), store: a}
	t.Cleanup(ts.Close)
	return ts, fm
}

func post(t *testing.T, url, token, body string) (*http.Response, Response) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/samples", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r Response
	json.NewDecoder(resp.Body).Decode(&r)
	return resp, r
}

// stored is a record as archived, wherever it is (staged, in Parquet, or a tombstone).
type stored struct {
	Op, UUID, Type, Uploader string
	Wire                     int
	Data                     string
}

// archived lists what the archive in dir holds, in the order received (staged rows, then
// tombstones), plus the rows already in Parquet.
func archived(t *testing.T, dir string) []stored {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "staging.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var out []stored
	rows, err := db.Query(`SELECT dataset, type, uploader, wire, json FROM staged ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var r stored
		var ds string
		rows.Scan(&ds, &r.Type, &r.Uploader, &r.Wire, &r.Data)
		r.Op = map[string]string{"samples": "sample", "records": "record"}[ds]
		if r.Op == "sample" {
			var v struct{ UUID string }
			json.Unmarshal([]byte(r.Data), &v)
			r.UUID = strings.ToUpper(v.UUID)
		}
		out = append(out, r)
	}
	rows.Close()
	for _, ds := range []string{"samples", "records"} {
		files, _ := filepath.Glob(filepath.Join(dir, "parquet", ds, "*", "*", "*.parquet"))
		for _, f := range files {
			if err := readParquet(f, func(r *Row) error {
				st := stored{Op: map[string]string{"samples": "sample", "records": "record"}[ds], Type: r.Type,
					Uploader: r.Uploader, Wire: int(r.Wire), Data: r.JSON}
				if r.UUID != nil && ds == "samples" {
					st.UUID = *r.UUID
				}
				out = append(out, st)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	rows, err = db.Query(`SELECT uuid, coalesce(type, ''), uploader, coalesce(json, '') FROM deleted WHERE reason = 'deleted' ORDER BY received_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		r := stored{Op: "delete"}
		rows.Scan(&r.UUID, &r.Type, &r.Uploader, &r.Data)
		out = append(out, r)
	}
	return out
}

func TestHealthz(t *testing.T) {
	ts, _ := newTestServer(t, t.TempDir())
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", resp, err)
	}
}

func TestAuth(t *testing.T) {
	ts, _ := newTestServer(t, t.TempDir())
	for _, tok := range []string{"", "wrong-token-0123456789"} {
		if resp, _ := post(t, ts.URL, tok, `{"samples":[]}`); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("token %q: got %d, want 401", tok, resp.StatusCode)
		}
	}
}

func TestStoreDedupeAndForward(t *testing.T) {
	dir := t.TempDir()
	ts, fm := newTestServer(t, dir)
	body := `{"device":{"model":"iPhone"},"samples":[` + hrSample + `,` + sleepSample + `,` + hrSample + `],"deletions":[` + deletion + `]}`

	resp, r := post(t, ts.URL, testToken, body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if r.Stored != 3 || r.Duplicates != 1 || r.Deletions != 1 {
		t.Fatalf("first post: %+v", r)
	}
	// A retry stores nothing new and forwards nothing.
	if _, r = post(t, ts.URL, testToken, body); r.Stored != 0 || r.Duplicates != 4 {
		t.Fatalf("retry: %+v", r)
	}

	recs := archived(t, dir)
	if len(recs) != 3 {
		t.Fatalf("archive has %d records, want 3", len(recs))
	}
	if recs[2].Op != "delete" || recs[2].UUID != "3B4E28BA-2FA1-11D2-883F-0016D3CCA427" {
		t.Errorf("tombstone: %+v", recs[2])
	}
	if !strings.Contains(recs[0].Data, `"future_field"`) {
		t.Errorf("unknown fields not kept: %s", recs[0].Data)
	}

	want := []string{
		"healthkit,source=Watch,unit=count/min heart_rate=62 1767323045123000000",
		"healthkit,source=Watch,value=asleepDeep sleep_analysis_seconds=1800 1767315600000000000",
	}
	if strings.Join(fm.lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("forwarded:\n%s\nwant:\n%s", strings.Join(fm.lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestDedupeSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	ts, _ := newTestServer(t, dir)
	post(t, ts.URL, testToken, `{"samples":[`+hrSample+`]}`)
	ts.Close()

	ts2, _ := newTestServer(t, dir)
	if _, r := post(t, ts2.URL, testToken, `{"samples":[`+hrSample+`,`+sleepSample+`]}`); r.Stored != 1 || r.Duplicates != 1 {
		t.Fatalf("after restart: %+v", r)
	}
	// Flushed to Parquet, the samples are still known.
	if _, err := ts2.store.Flush(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	ts2.Close()
	ts3, _ := newTestServer(t, dir)
	if _, r := post(t, ts3.URL, testToken, `{"samples":[`+hrSample+`,`+sleepSample+`]}`); r.Stored != 0 || r.Duplicates != 2 {
		t.Fatalf("after flush and restart: %+v", r)
	}
	if recs := archived(t, dir); len(recs) != 2 {
		t.Fatalf("archive: %+v", recs)
	}
}

func TestOneProcessPerArchive(t *testing.T) {
	dir := t.TempDir()
	newTestServer(t, dir)
	if _, err := OpenStore(dir); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("second open: %v", err)
	}
}

func TestRejectsInvalidBatchWhole(t *testing.T) {
	dir := t.TempDir()
	ts, _ := newTestServer(t, dir)
	bad := `{"uuid":"nope","type":"X","start":"2026-01-01T00:00:00Z","end":"2026-01-01T00:00:00Z"}`
	if resp, _ := post(t, ts.URL, testToken, `{"samples":[`+hrSample+`,`+bad+`]}`); resp.StatusCode != 400 {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
	if recs := archived(t, dir); len(recs) != 0 {
		t.Fatalf("partial batch stored: %d records", len(recs))
	}
	if resp, _ := post(t, ts.URL, testToken, `not json`); resp.StatusCode != 400 {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
}

func TestBodyLimit(t *testing.T) {
	ts, _ := newTestServer(t, t.TempDir())
	big := `{"samples":[` + strings.Repeat(hrSample+",", 5000) + hrSample + `]}`
	if resp, _ := post(t, ts.URL, testToken, big); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", resp.StatusCode)
	}
}

func TestWorkoutLines(t *testing.T) {
	smp, err := parseSample([]byte(`{"uuid":"4b4e28ba-2fa1-11d2-883f-0016d3cca427","type":"HKWorkoutTypeIdentifier","kind":"workout",
	 "start":"2026-01-02T10:00:00Z","end":"2026-01-02T10:30:00Z","source":{"name":"My Watch"},
	 "workout":{"activity_type":37,"activity_name":"running","duration_s":1800,"energy_kcal":300.5,"distance_m":5000}}`))
	if err != nil {
		t.Fatal(err)
	}
	f := &Forwarder{Measurement: "healthkit"}
	got := string(f.Lines([]*Sample{smp}))
	want := "healthkit,activity=running,source=My\\ Watch workout_duration_seconds=1800 1767348000000000000\n" +
		"healthkit,activity=running,source=My\\ Watch workout_energy_kcal=300.5 1767348000000000000\n" +
		"healthkit,activity=running,source=My\\ Watch workout_distance_meters=5000 1767348000000000000\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMetricName(t *testing.T) {
	for in, want := range map[string]string{
		"HKQuantityTypeIdentifierHeartRateVariabilitySDNN":      "heart_rate_variability_sdnn",
		"HKQuantityTypeIdentifierVO2Max":                        "vo2_max",
		"HKQuantityTypeIdentifierStepCount":                     "step_count",
		"HKCategoryTypeIdentifierSleepAnalysis":                 "sleep_analysis",
		"HKQuantityTypeIdentifierOxygenSaturation":              "oxygen_saturation",
		"HKQuantityTypeIdentifierAppleSleepingWristTemperature": "apple_sleeping_wrist_temperature",
	} {
		if got := metricName(in); got != want {
			t.Errorf("metricName(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestEnvConfig(t *testing.T) {
	t.Setenv("HKR_LISTEN", "127.0.0.1:0")
	t.Setenv("HKR_ARCHIVE_DIR", t.TempDir())
	tok := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tok, []byte("short\n"), 0o600)
	t.Setenv("HKR_TOKEN_FILE", tok)
	if err := run(nil); err == nil || !strings.Contains(err.Error(), "shorter") {
		t.Fatalf("want short-token error, got %v", err)
	}
}

const ecgV2 = `{"uuid":"5b4e28ba-2fa1-11d2-883f-0016d3cca427","type":"HKDataTypeIdentifierElectrocardiogram","kind":"electrocardiogram",
 "start":"2026-01-02T09:00:00Z","end":"2026-01-02T09:00:30Z","source":{"name":"Watch"},"classification":{"raw":1,"name":"sinusRhythm"}}`

const ecgChunk = `{"kind":"series","key":"5B4E28BA-2FA1-11D2-883F-0016D3CCA427/electrocardiogram/0","series":"electrocardiogram",
 "sample_uuid":"5B4E28BA-2FA1-11D2-883F-0016D3CCA427","index":0,"final":true,"unit":"mcV","points":[{"t":0,"v":12.5}]}`

const summary = `{"kind":"activity_summary","key":"2026-01-02","date":"2026-01-02","start":"2026-01-02T05:00:00Z",
 "values":{"active_energy_burned":{"value":512,"unit":"kcal"},"apple_stand_hours":{"value":10,"unit":"count"}}}`

func TestV2RecordsStoredByContent(t *testing.T) {
	dir := t.TempDir()
	ts, fm := newTestServer(t, dir)
	body := `{"version":2,"device":{"model":"iPhone"},"samples":[` + ecgV2 + `],"records":[` + ecgChunk + `,` + summary + `],"deletions":[]}`
	resp, r := post(t, ts.URL, testToken, body)
	if resp.StatusCode != 200 || r.Stored != 3 || r.Records != 2 || r.Version != 2 {
		t.Fatalf("first post: %d %+v", resp.StatusCode, r)
	}
	// Same content: duplicates. A changed summary for the same day: a new record.
	if _, r = post(t, ts.URL, testToken, body); r.Stored != 0 || r.Duplicates != 3 {
		t.Fatalf("retry: %+v", r)
	}
	changed := strings.Replace(summary, `"value":512`, `"value":600`, 1)
	if _, r = post(t, ts.URL, testToken, `{"version":2,"samples":[],"records":[`+changed+`]}`); r.Stored != 1 {
		t.Fatalf("changed summary: %+v", r)
	}

	recs := archived(t, dir)
	if len(recs) != 4 {
		t.Fatalf("archive has %d records, want 4", len(recs))
	}
	if recs[0].Op != "sample" || recs[0].Wire != 2 || recs[1].Op != "record" || recs[1].Type != "series" {
		t.Errorf("records: %+v / %+v", recs[0], recs[1])
	}

	// ECGs and series are not metrics; the summary is, both times it changed.
	want := []string{
		"healthkit,unit=kcal activity_summary_active_energy_burned=512 1767330000000000000",
		"healthkit,unit=count activity_summary_apple_stand_hours=10 1767330000000000000",
		"healthkit,unit=kcal activity_summary_active_energy_burned=600 1767330000000000000",
		"healthkit,unit=count activity_summary_apple_stand_hours=10 1767330000000000000",
	}
	if strings.Join(fm.lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("forwarded:\n%s\nwant:\n%s", strings.Join(fm.lines, "\n"), strings.Join(want, "\n"))
	}

	// The index is rebuilt from the archive, records included.
	ts.Close()
	ts2, _ := newTestServer(t, dir)
	if _, r = post(t, ts2.URL, testToken, body); r.Stored != 0 {
		t.Fatalf("after restart: %+v", r)
	}
}

func TestV2CopyOfV1Sample(t *testing.T) {
	dir := t.TempDir()
	ts, fm := newTestServer(t, dir)
	post(t, ts.URL, testToken, `{"samples":[`+hrSample+`]}`)
	// The app re-sends history in v2: the fuller copy is archived once, but not forwarded
	// again (its number is already in the metrics database).
	v2 := strings.Replace(hrSample, `"future_field"`, `"device":{"model":"Watch7,1"},"future_field"`, 1)
	for i := 0; i < 2; i++ {
		if _, r := post(t, ts.URL, testToken, `{"version":2,"samples":[`+v2+`],"records":[]}`); r.Stored != 1-i {
			t.Fatalf("v2 post %d: %+v", i, r)
		}
	}
	if recs := archived(t, dir); len(recs) != 2 || recs[1].Wire != 2 {
		t.Fatalf("archive: %+v", recs)
	}
	if len(fm.lines) != 1 {
		t.Errorf("forwarded %d lines, want 1: %v", len(fm.lines), fm.lines)
	}
}

func TestV2RejectsBadRecord(t *testing.T) {
	dir := t.TempDir()
	ts, _ := newTestServer(t, dir)
	for _, rec := range []string{`{"kind":"Bad Kind","key":"x"}`, `{"kind":"series","key":""}`, `{"kind":"activity_summary","key":"d","start":"yesterday"}`} {
		if resp, _ := post(t, ts.URL, testToken, `{"version":2,"samples":[`+hrSample+`],"records":[`+rec+`]}`); resp.StatusCode != 400 {
			t.Errorf("%s: status %d, want 400", rec, resp.StatusCode)
		}
	}
	if recs := archived(t, dir); len(recs) != 0 {
		t.Fatalf("partial batch stored: %d records", len(recs))
	}
}

func TestV1ResponseStillCompatible(t *testing.T) {
	ts, _ := newTestServer(t, t.TempDir())
	resp, r := post(t, ts.URL, testToken, `{"samples":[`+hrSample+`],"deletions":[`+deletion+`]}`)
	if resp.StatusCode != 200 || r.Stored != 2 || r.Deletions != 1 || r.Records != 0 {
		t.Fatalf("v1 post: %d %+v", resp.StatusCode, r)
	}
}

// Characteristics carry a date string and enum objects under "values": a v2 app sends one
// with every full sync, and rejecting it stopped medications and activity summaries too.
const characteristics = `{"kind":"characteristics","key":"characteristics","values":{"date_of_birth":"1990-01-02",
 "biological_sex":{"raw":2,"name":"male"},"blood_type":{"raw":0,"name":"notSet"}},"not_read":["HKCharacteristicTypeIdentifierX"]}`

func TestV2RecordValuesOfAnyShape(t *testing.T) {
	dir := t.TempDir()
	ts, fm := newTestServer(t, dir)
	odd := strings.Replace(summary, `"apple_stand_hours":{"value":10,"unit":"count"}`,
		`"apple_stand_hours":{"value":10,"unit":"count"},"paused":true,"move_mode":{"raw":1,"name":"activeEnergy"},"goal":{"value":null,"unit":"min"},"note":"text"`, 1)
	other := `{"kind":"future_kind","key":"k","values":[1,"two"]}`
	resp, r := post(t, ts.URL, testToken, `{"version":2,"samples":[],"records":[`+characteristics+`,`+odd+`,`+other+`]}`)
	if resp.StatusCode != 200 || r.Stored != 3 || r.Records != 3 {
		t.Fatalf("post: %d %+v", resp.StatusCode, r)
	}
	// Only number-valued {value, unit} entries become metrics.
	want := []string{
		"healthkit,unit=kcal activity_summary_active_energy_burned=512 1767330000000000000",
		"healthkit,unit=count activity_summary_apple_stand_hours=10 1767330000000000000",
	}
	if strings.Join(fm.lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("forwarded:\n%s\nwant:\n%s", strings.Join(fm.lines, "\n"), strings.Join(want, "\n"))
	}
}
