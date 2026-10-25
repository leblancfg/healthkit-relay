package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerIDIsKept(t *testing.T) {
	dir := t.TempDir()
	id, err := loadServerID(dir)
	if err != nil || len(id) != 32 {
		t.Fatalf("first start: %q, %v", id, err)
	}
	again, err := loadServerID(dir)
	if err != nil || again != id {
		t.Fatalf("second start: %q, %v; want %q", again, err, id)
	}
	// An admin reset leaves it alone.
	if _, err := newAdminToken(dir); err != nil {
		t.Fatal(err)
	}
	if again, _ := loadServerID(dir); again != id {
		t.Errorf("after admin reset: %q, want %q", again, id)
	}
	if fi, err := os.Stat(filepath.Join(dir, serverFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("server.json: %v, %v", fi, err)
	}
	os.WriteFile(filepath.Join(dir, serverFile), []byte(`{"server_id":"x"}`), 0o600)
	if _, err := loadServerID(dir); err == nil {
		t.Error("a short server ID is accepted")
	}
}

func TestHandoffInStatusAndHeartbeat(t *testing.T) {
	dir := t.TempDir()
	token, dev := pairedToken(t, dir)
	a, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	d, _ := OpenDevices(filepath.Join(dir, "devices.json"))
	s := &Server{Archive: a, AdminKey: NewAdminKey(testToken), MaxBodyBytes: 1 << 20, Devices: d, Codes: NewCodes(),
		ID: "5f0c2a9e5f0c2a9e5f0c2a9e5f0c2a9e", PublicURL: "https://health.example.ts.net"}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	resp, body := call(t, "GET", ts.URL+"/v1/status", token, nil)
	var st Status
	if resp.StatusCode != 200 || json.Unmarshal(body, &st) != nil {
		t.Fatalf("status: %d %s", resp.StatusCode, body)
	}
	if st.ServerID != s.ID || st.PreferredURL != "https://health.example.ts.net" || st.Device == nil || st.Device.ID != dev.ID {
		t.Errorf("status: %s", body)
	}

	resp, body = call(t, "POST", ts.URL+"/v1/heartbeat", token, map[string]any{"trigger": "refresh"})
	var h Handoff
	if resp.StatusCode != 200 || json.Unmarshal(body, &h) != nil || h.ServerID != s.ID || h.PreferredURL != st.PreferredURL {
		t.Errorf("heartbeat: %d %s", resp.StatusCode, body)
	}

	// GET /v1/server answers without a token, with the ID only.
	resp, body = call(t, "GET", ts.URL+"/v1/server", "", nil)
	var info map[string]any
	if resp.StatusCode != 200 || json.Unmarshal(body, &info) != nil || info["server_id"] != s.ID || len(info) != 2 {
		t.Errorf("server info: %d %s", resp.StatusCode, body)
	}

	// Only an https -public-url is offered: the app moves only to https.
	for _, u := range []string{"http://192.168.1.10:8080", "", "https://"} {
		s.PublicURL = u
		if got := s.preferredURL(); got != "" {
			t.Errorf("preferred URL for -public-url %q: %q", u, got)
		}
	}
	s.PublicURL = "https://health.example.com/"
	if got := s.preferredURL(); got != "https://health.example.com" {
		t.Errorf("trailing slash: %q", got)
	}
}

func TestFunnelRequestsArePublic(t *testing.T) {
	s := testServer(t)
	funnel := http.Header{"X-Forwarded-For": {"100.101.102.103"}, "Tailscale-Funnel-Request": {"?1"}}
	tailnet := http.Header{"X-Forwarded-For": {"100.101.102.103"}}

	if w := setupRequest(t, s, "GET", "/setup", "127.0.0.1:40000", tailnet, nil); w.Code != http.StatusOK {
		t.Errorf("setup page from the tailnet: %d", w.Code)
	}
	if w := setupRequest(t, s, "GET", "/setup", "127.0.0.1:40000", funnel, nil); w.Code != http.StatusForbidden {
		t.Errorf("setup page through Funnel: %d", w.Code)
	}
	if w := setupRequest(t, s, "POST", "/v1/pair", "127.0.0.1:40000", funnel, nil); w.Code != http.StatusForbidden {
		t.Errorf("pairing through Funnel: %d", w.Code)
	}
	if w := setupRequest(t, s, "GET", "/metrics", "127.0.0.1:40000", funnel, nil); w.Code != http.StatusForbidden {
		t.Errorf("metrics through Funnel: %d", w.Code)
	}
	if w := setupRequest(t, s, "GET", "/metrics", "203.0.113.9:40000", nil, nil); w.Code != http.StatusForbidden {
		t.Errorf("metrics from the internet: %d", w.Code)
	}
	if w := setupRequest(t, s, "GET", "/metrics", "127.0.0.1:40000", nil, nil); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "healthkit_relay_archive_records") {
		t.Errorf("metrics from this computer: %d", w.Code)
	}
	// The header from anywhere else only takes rights away.
	if w := setupRequest(t, s, "GET", "/setup", "192.168.1.20:5000", http.Header{"Tailscale-Funnel-Request": {"?1"}}, nil); w.Code != http.StatusOK {
		t.Errorf("setup page from the LAN with the header: %d", w.Code)
	}
}
