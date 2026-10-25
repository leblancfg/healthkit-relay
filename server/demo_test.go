package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const demoCode = "ABCD-EFGH-JKLM-NPQR"

func demoServer(t *testing.T, quota int64) (*Server, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	a, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	d, _ := OpenDevices(filepath.Join(dir, "devices.json"))
	demo, err := NewDemo(demoCode, time.Hour, quota)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Archive: a, AdminKey: NewAdminKey(testToken), MaxBodyBytes: 1 << 20, Devices: d, Codes: NewCodes(),
		SetupFrom: "any", PublicURL: "https://demo.example.ts.net", Demo: demo}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts, dir
}

func TestDemoCodePairsManyTimes(t *testing.T) {
	s, ts, _ := demoServer(t, 1<<20)
	var tokens []string
	for _, typed := range []string{demoCode, "abcdefghjklmnpqr", " ABCDEFGHJKLMNPQR "} {
		code, p := pairWith(t, ts.URL, typed, "")
		if code != 200 || p.Token == "" {
			t.Fatalf("pairing with %q: %d", typed, code)
		}
		tokens = append(tokens, p.Token)
	}
	if n := s.Devices.Len(); n != 3 {
		t.Errorf("%d devices, want 3", n)
	}
	if code, _ := pairWith(t, ts.URL, "ABCDEFGHJKLMNPQS", ""); code != http.StatusForbidden {
		t.Errorf("another code: %d", code)
	}
	// Re-pairing keeps the device.
	if code, p := pairWith(t, ts.URL, demoCode, tokens[0]); code != 200 || s.Devices.Len() != 3 || p.Token == tokens[0] {
		t.Errorf("re-pairing: %d, %d devices", code, s.Devices.Len())
	}

	// The setup page with the code shows it, for typing, and the page keeps waiting.
	w := setupRequest(t, s, "GET", "/setup?code="+demoCode, "203.0.113.9:4000", nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "demo server") || !strings.Contains(w.Body.String(), demoCode) ||
		!strings.Contains(w.Body.String(), "https://demo.example.ts.net") {
		t.Errorf("setup page with the demo code: %d\n%s", w.Code, w.Body.String())
	}
	w = setupRequest(t, s, "GET", "/setup/status?code="+demoCode, "203.0.113.9:4000", nil, nil)
	if !strings.Contains(w.Body.String(), `"pending"`) {
		t.Errorf("setup status: %s", w.Body.String())
	}
	if _, err := NewDemo("SHORT", time.Hour, 1); err == nil {
		t.Error("a short demo code is accepted")
	}
}

func TestDemoQuota(t *testing.T) {
	s, ts, _ := demoServer(t, 100)
	_, p := pairWith(t, ts.URL, demoCode, "")
	body := `{"version":2,"samples":[` + hrSample + `]}`
	if resp, r := post(t, ts.URL, p.Token, body); resp.StatusCode != 200 || r.Stored != 1 {
		t.Fatalf("first upload: %d", resp.StatusCode)
	}
	// The first upload crossed the quota (it counts as it is read): the next waits.
	resp, _ := post(t, ts.URL, p.Token, body)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("over quota: %d, Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	s.demoReset(context.Background())
	if resp, _ := post(t, ts.URL, p.Token, body); resp.StatusCode != 200 {
		t.Errorf("after the reset: %d", resp.StatusCode)
	}
}

func TestDemoResetErasesEverything(t *testing.T) {
	s, ts, dir := demoServer(t, 1<<20)
	_, a := pairWith(t, ts.URL, demoCode, "")
	_, b := pairWith(t, ts.URL, demoCode, "")
	post(t, ts.URL, a.Token, `{"version":2,"samples":[`+hrSample+`]}`)
	post(t, ts.URL, b.Token, `{"version":2,"samples":[`+sleepSample+`],"deletions":[`+deletion+`]}`)
	if _, err := s.Archive.Flush(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	post(t, ts.URL, a.Token, `{"version":2,"samples":[`+strings.Replace(hrSample, "1b4e", "9b4e", 1)+`]}`)
	if len(archived(t, dir)) == 0 {
		t.Fatal("nothing archived")
	}
	// A device unseen for over a week is revoked; a recent one stays.
	s.Devices.mu.Lock()
	s.Devices.byHash[hashToken(b.Token)].LastSeen = time.Now().Add(-DemoDeviceTTL - time.Hour)
	s.Devices.mu.Unlock()

	s.demoReset(context.Background())
	if got := archived(t, dir); len(got) != 0 {
		t.Errorf("after the reset the archive holds %v", got)
	}
	if s.Archive.Len() != 0 {
		t.Errorf("record count %d", s.Archive.Len())
	}
	if _, ok := s.Devices.Lookup(a.Token); !ok {
		t.Error("a recent device was revoked")
	}
	if _, ok := s.Devices.Lookup(b.Token); ok {
		t.Error("a device unseen for over a week stays")
	}
	// The same records upload again (the de-duplication index went too).
	if resp, r := post(t, ts.URL, a.Token, `{"version":2,"samples":[`+hrSample+`]}`); resp.StatusCode != 200 || r.Stored != 1 {
		t.Errorf("upload after reset: %d, stored %d", resp.StatusCode, r.Stored)
	}
}
