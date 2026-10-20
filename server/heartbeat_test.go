package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// captureLog collects the server's log lines for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	return &buf
}

func pairedToken(t *testing.T, dir string) (string, Device) {
	t.Helper()
	d, err := OpenDevices(filepath.Join(dir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	token, dev, err := d.Add("al", "iPhone17,5")
	if err != nil {
		t.Fatal(err)
	}
	return token, dev
}

func send(t *testing.T, method, url, token, ua, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func metricsText(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestHeartbeat(t *testing.T) {
	dir := t.TempDir()
	token, dev := pairedToken(t, dir)
	ts, _ := newTestServer(t, dir)
	logs := captureLog(t)

	body := `{"trigger":"refresh","app_state":"background","protected_data":false,"outcome":"locked",
	 "pending":"locked","last_error":"The network\nconnection was lost.","uploaded":0,"seconds":0.2,
	 "background_time_left":27.5,"process_age":5400,"app_version":"0.6.2 (9)",
	 "wakes":{"refresh":{"count":3,"last":"2026-10-20T12:00:00Z"},"observer":{"count":1,"last":"2026-10-20T11:00:00Z"}}}`
	if resp := send(t, http.MethodPost, ts.URL+"/v1/heartbeat", token, "", body); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat: %d", resp.StatusCode)
	}
	line := logs.String()
	for _, want := range []string{"heartbeat: device " + dev.ID, "app 0.6.2 (9)", "trigger refresh", "background",
		"protected data unavailable", "outcome locked", "owes a sync (locked)", `last error "The network connection was lost."`,
		"28s background time left", "process 1h30m0s old", "observer 1 (last ", "refresh 3 (last "} {
		if !strings.Contains(line, want) {
			t.Errorf("log lacks %q:\n%s", want, line)
		}
	}
	if strings.Count(strings.TrimSpace(line), "\n") != 0 {
		t.Errorf("a heartbeat logs one line:\n%s", line)
	}
	m := metricsText(t, ts.URL)
	if !strings.Contains(m, `healthkit_relay_heartbeats_total{trigger="refresh"} 1`) {
		t.Errorf("heartbeat not counted:\n%s", m)
	}
	re := regexp.MustCompile(`healthkit_relay_last_heartbeat_timestamp_seconds\{trigger="refresh"\} (\d+)`)
	if got := re.FindStringSubmatch(m); got == nil || got[1] == "0" {
		t.Errorf("no last heartbeat time:\n%s", m)
	}

	// An unknown trigger keeps the label set fixed.
	send(t, http.MethodPost, ts.URL+"/v1/heartbeat", token, "", `{"trigger":"new-thing"}`)
	if m := metricsText(t, ts.URL); !strings.Contains(m, `healthkit_relay_heartbeats_total{trigger="unknown"} 1`) {
		t.Errorf("unknown trigger:\n%s", m)
	}
	// It needs an upload token.
	if resp := send(t, http.MethodPost, ts.URL+"/v1/heartbeat", "wrong", "", body); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("heartbeat with a wrong token: %d", resp.StatusCode)
	}
	if resp := send(t, http.MethodPost, ts.URL+"/v1/heartbeat", token, "", "{"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid heartbeat: %d", resp.StatusCode)
	}
}

func TestLastBatchByTrigger(t *testing.T) {
	dir := t.TempDir()
	token, _ := pairedToken(t, dir)
	ts, _ := newTestServer(t, dir)
	captureLog(t)
	resp, _ := post(t, ts.URL, token, `{"version":2,"trigger":"observer","app_state":"background","samples":[`+hrSample+`]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: %d", resp.StatusCode)
	}
	m := metricsText(t, ts.URL)
	if regexp.MustCompile(`healthkit_relay_last_batch_timestamp_seconds\{trigger="observer"\} [1-9]`).FindString(m) == "" {
		t.Errorf("no last observer batch:\n%s", m)
	}
	if !strings.Contains(m, `healthkit_relay_last_batch_timestamp_seconds{trigger="refresh"} 0`) {
		t.Errorf("refresh should have none:\n%s", m)
	}
}

func TestRefusedTokensAreLogged(t *testing.T) {
	dir := t.TempDir()
	ts, _ := newTestServer(t, dir)
	logs := captureLog(t)

	send(t, http.MethodPost, ts.URL+"/v1/samples", "not-a-token", "HealthKitRelay/9 CFNetwork/3860 Darwin/25.6.0", `{}`)
	line := logs.String()
	for _, want := range []string{"refused: POST /v1/samples from 127.0.0.1, 401, token sha256 ",
		`user agent "HealthKitRelay/9 CFNetwork/3860 Darwin/25.6.0"`} {
		if !strings.Contains(line, want) {
			t.Errorf("log lacks %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "not-a-token") || strings.Contains(line, hashToken("not-a-token")) {
		t.Errorf("the token or its whole hash is logged:\n%s", line)
	}

	// Within refusalLogEvery, refusals are counted, then summed up on the next line.
	send(t, http.MethodGet, ts.URL+"/v1/status", "", "", "")
	send(t, http.MethodGet, ts.URL+"/v1/status", "", "", "")
	if n := strings.Count(logs.String(), "refused:"); n != 1 {
		t.Fatalf("%d refusal lines, want 1:\n%s", n, logs.String())
	}
	if !strings.Contains(metricsText(t, ts.URL), `healthkit_relay_requests_total{result="unauthorized"} 3`) {
		t.Error("every refusal is still counted")
	}
}

func TestRefusalLogSumsUpQuietOnes(t *testing.T) {
	s := &Server{}
	logs := captureLog(t)
	r, _ := http.NewRequest(http.MethodGet, "http://x/v1/status", nil)
	r.RemoteAddr = "100.64.0.7:5555"
	s.logRefusal(r, "401")
	s.logRefusal(r, "401")
	s.logRefusal(r, "401")
	s.mu.Lock()
	s.refusalLog = time.Now().Add(-refusalLogEvery)
	s.mu.Unlock()
	s.logRefusal(r, "401")
	out := logs.String()
	if strings.Count(out, "refused:") != 2 || !strings.Contains(out, "(2 more refused since the last such line)") ||
		!strings.Contains(out, "from 100.64.0.7, 401, no bearer token") {
		t.Errorf("unexpected:\n%s", out)
	}
}
