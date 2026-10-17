package main

import (
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

func newTestServer(t *testing.T, dir string) (*httptest.Server, *fakeMetrics) {
	t.Helper()
	a, err := OpenArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	fm := &fakeMetrics{}
	metrics := httptest.NewServer(fm)
	t.Cleanup(metrics.Close)
	s := &Server{Archive: a, Token: testToken, MaxBodyBytes: 1 << 20,
		Forwarder: &Forwarder{URL: metrics.URL, Measurement: "healthkit", Client: metrics.Client()}}
	ts := httptest.NewServer(s.Handler())
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

func archiveLines(t *testing.T, dir string) []Record {
	t.Helper()
	var out []Record
	files, _ := filepath.Glob(filepath.Join(dir, "*", "*.jsonl"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var r Record
			if err := json.Unmarshal([]byte(l), &r); err != nil {
				t.Fatalf("bad archive line %q: %v", l, err)
			}
			out = append(out, r)
		}
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

	recs := archiveLines(t, dir)
	if len(recs) != 3 {
		t.Fatalf("archive has %d records, want 3", len(recs))
	}
	if recs[2].Op != "delete" || recs[2].UUID != "3B4E28BA-2FA1-11D2-883F-0016D3CCA427" {
		t.Errorf("tombstone: %+v", recs[2])
	}
	if !strings.Contains(string(recs[0].Data), `"future_field"`) {
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

	// Simulate a crash that left a partial line behind.
	files, _ := filepath.Glob(filepath.Join(dir, "*", "*.jsonl"))
	f, _ := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"op":"sample","uu`)
	f.Close()

	ts2, _ := newTestServer(t, dir)
	if _, r := post(t, ts2.URL, testToken, `{"samples":[`+hrSample+`,`+sleepSample+`]}`); r.Stored != 1 || r.Duplicates != 1 {
		t.Fatalf("after restart: %+v", r)
	}
	b, _ := os.ReadFile(files[0])
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[2], `{"op":"sample","uuid":"2B4E`) {
		t.Fatalf("archive after partial line:\n%s", b)
	}
}

func TestRejectsInvalidBatchWhole(t *testing.T) {
	dir := t.TempDir()
	ts, _ := newTestServer(t, dir)
	bad := `{"uuid":"nope","type":"X","start":"2026-01-01T00:00:00Z","end":"2026-01-01T00:00:00Z"}`
	if resp, _ := post(t, ts.URL, testToken, `{"samples":[`+hrSample+`,`+bad+`]}`); resp.StatusCode != 400 {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
	if recs := archiveLines(t, dir); len(recs) != 0 {
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
