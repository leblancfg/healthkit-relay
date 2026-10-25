package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	demo, err := NewDemo(dir, demoCode, time.Hour, quota)
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
	s, ts, dir := demoServer(t, 1<<20)
	tokens := map[string]bool{}
	for _, typed := range []string{demoCode, "abcdefghjklmnpqr", " ABCDEFGHJKLMNPQR "} {
		code, p := pairWith(t, ts.URL, typed, "")
		if code != 200 || p.Token == "" || tokens[p.Token] {
			t.Fatalf("pairing with %q: %d", typed, code)
		}
		tokens[p.Token] = true
		if p.Device.Name != "Demo iPhone" || p.Device.Model != "" {
			t.Errorf("a demo device keeps what it sent: %+v", p.Device)
		}
		if resp, _ := call(t, "GET", ts.URL+"/v1/status", p.Token, nil); resp.StatusCode != 200 {
			t.Errorf("status with a demo token: %d", resp.StatusCode)
		}
	}
	// Nothing about the devices is stored.
	if n := s.Devices.Len(); n != 0 {
		t.Errorf("%d devices stored", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "devices.json")); err == nil {
		t.Error("devices.json written")
	}
	if code, _ := pairWith(t, ts.URL, "ABCDEFGHJKLMNPQS", ""); code != http.StatusForbidden {
		t.Errorf("another code: %d", code)
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

	// Pairings are bounded per period.
	s.Demo.pairings = DemoPairings
	if code, _ := pairWith(t, ts.URL, demoCode, ""); code != http.StatusTooManyRequests {
		t.Errorf("pairing beyond the limit: %d", code)
	}
	s.demoReset()
	if code, _ := pairWith(t, ts.URL, demoCode, ""); code != 200 {
		t.Errorf("pairing after the reset: %d", code)
	}
	if _, err := NewDemo(t.TempDir(), "SHORT", time.Hour, 1); err == nil {
		t.Error("a short demo code is accepted")
	}
}

func TestDemoTokens(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDemo(dir, demoCode, time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token, dev := d.mint(now)
	if got, ok := d.lookup(token, now.Add(time.Hour)); !ok || got.ID != dev.ID {
		t.Fatalf("lookup: %+v %v", got, ok)
	}
	if _, ok := d.lookup(token, now.Add(DemoTokenLifetime+time.Minute)); ok {
		t.Error("an expired token works")
	}
	for _, bad := range []string{"", "demo.", "demo.x.y", token + "A", strings.Replace(token, "demo.", "demo.A", 1), testToken} {
		if _, ok := d.lookup(bad, now); ok {
			t.Errorf("token %q works", bad)
		}
	}
	// The key survives a restart; another key signs out every token.
	again, _ := NewDemo(dir, demoCode, time.Hour, 1)
	if _, ok := again.lookup(token, now); !ok {
		t.Error("the token stops working after a restart")
	}
	other, _ := NewDemo(t.TempDir(), demoCode, time.Hour, 1)
	if _, ok := other.lookup(token, now); ok {
		t.Error("a token from another key works")
	}
	if fi, err := os.Stat(filepath.Join(dir, demoKeyFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("demo-key: %v %v", fi, err)
	}
}

func TestDemoQuota(t *testing.T) {
	s, ts, _ := demoServer(t, 100)
	_, p := pairWith(t, ts.URL, demoCode, "")
	body := `{"version":2,"samples":[` + hrSample + `]}`
	// The body may use what is left of the quota, at most.
	if resp, _ := post(t, ts.URL, p.Token, body); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the remaining quota: %d", resp.StatusCode)
	}
	resp, _ := post(t, ts.URL, p.Token, body)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("quota used up: %d, Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	s.demoReset()
	s.Demo.quota = 1 << 20
	if resp, r := post(t, ts.URL, p.Token, body); resp.StatusCode != 200 || r.Stored != 1 {
		t.Errorf("after the reset: %d", resp.StatusCode)
	}

	// At most DemoUploads uploads are read at once.
	for range DemoUploads {
		s.Demo.uploads <- struct{}{}
	}
	if resp, _ := post(t, ts.URL, p.Token, body); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("busy: %d", resp.StatusCode)
	}
	for range DemoUploads {
		<-s.Demo.uploads
	}
}

func TestDemoKeepsNothing(t *testing.T) {
	s, ts, dir := demoServer(t, 1<<20)
	logs := captureLog(t)
	_, a := pairWith(t, ts.URL, demoCode, "")
	_, b := pairWith(t, ts.URL, demoCode, "")
	var wg sync.WaitGroup
	for _, tok := range []string{a.Token, b.Token} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			post(t, ts.URL, tok, `{"version":2,"samples":[`+hrSample+`,`+sleepSample+`],"deletions":[`+deletion+`]}`)
		}()
	}
	wg.Wait()
	// Checked like on any server: a bad batch is refused, and its content is not logged.
	if resp, _ := post(t, ts.URL, a.Token, `{"version":2,"samples":[{"uuid":"secret-uuid-value"}]}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid batch: %d", resp.StatusCode)
	}
	send(t, http.MethodPost, ts.URL+"/v1/heartbeat", a.Token, "", `{"trigger":"refresh","app_version":"1.0.0"}`)
	send(t, http.MethodGet, ts.URL+"/v1/status", "wrong-token", "probe/1.0", "")
	if got := archived(t, dir); len(got) != 0 || s.Archive.Len() != 0 {
		t.Errorf("the demo archive holds %v", got)
	}
	_, body := call(t, "GET", ts.URL+"/v1/status", a.Token, nil)
	if !strings.Contains(string(body), `"records":6`) {
		t.Errorf("status counts what was received: %s", body)
	}
	if _, body := call(t, "DELETE", ts.URL+"/v1/data?confirm=erase", a.Token, nil); !strings.Contains(string(body), `"erased":0`) {
		t.Errorf("erase: %s", body)
	}
	for _, leak := range []string{"secret-uuid-value", "HKQuantityTypeIdentifierHeartRate", "probe/1.0", "1.0.0", "127.0.0.1", a.Device.ID} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("the log holds %q:\n%s", leak, logs.String())
		}
	}
	s.demoReset()
	if s.Demo.records() != 0 {
		t.Errorf("counts after the reset: %d", s.Demo.records())
	}
	if !bytes.Contains(logs.Bytes(), []byte("demo: last 1h0m0s: 2 pairings, 2 batches, 6 records")) {
		t.Errorf("summary:\n%s", logs.String())
	}
}
