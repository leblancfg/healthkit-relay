package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A devices.json from before scopes keeps working unchanged: its devices are upload tokens,
// and saving it again adds no field to them.
func TestDevicesWithoutScope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.json")
	old := `{
  "devices": [
    {
      "id": "0a1b2c3d",
      "name": "iPhone",
      "model": "iPhone17,5",
      "created_at": "2026-10-01T12:00:00Z",
      "last_seen_at": "2026-10-17T12:00:00Z",
      "token_sha256": "` + hashToken("old-device-token-0123") + `"
    }
  ]
}
`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDevices(path)
	if err != nil {
		t.Fatal(err)
	}
	dev, ok := d.Lookup("old-device-token-0123")
	if !ok || dev.Scope != ScopeUpload || !(Caller{Device: &dev}).Allows(ScopeUpload) || (Caller{Device: &dev}).Allows(ScopeRead) {
		t.Fatalf("old device: %+v %v", dev, ok)
	}
	if _, _, err := d.AddScoped("grafana", "", ScopeRead); err != nil {
		t.Fatal(err)
	}
	var file struct{ Devices []map[string]any }
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &file); err != nil || len(file.Devices) != 2 {
		t.Fatalf("%s: %v", b, err)
	}
	if _, has := file.Devices[0]["scope"]; has {
		t.Errorf("the old device gained a scope field: %s", b)
	}
	if file.Devices[1]["scope"] != ScopeRead {
		t.Errorf("the read token is not marked read: %s", b)
	}
	// And it reads back the same.
	d2, err := OpenDevices(path)
	if err != nil {
		t.Fatal(err)
	}
	list := d2.List()
	if len(list) != 2 || list[0].Scope != ScopeUpload || list[1].Scope != ScopeRead || list[1].TokenHash != "" {
		t.Errorf("reloaded: %+v", list)
	}
}

// Read tokens may not upload, read the status or erase; tokens are minted by the admin only.
func TestReadTokenCannotUpload(t *testing.T) {
	ts, _ := newTestServer(t, t.TempDir())
	resp, body := call(t, http.MethodPost, ts.URL+"/v1/tokens", testToken, TokenRequest{Name: "grafana"})
	var tr TokenResponse
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &tr) != nil || tr.Token == "" || tr.Device.Scope != ScopeRead {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	read := tr.Token
	if r, _ := post(t, ts.URL, read, `{"samples":[`+hrSample+`]}`); r.StatusCode != http.StatusForbidden {
		t.Errorf("upload with a read token: %d", r.StatusCode)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/status"},
		{http.MethodDelete, "/v1/data?confirm=erase"},
	} {
		if r, b := call(t, c.method, ts.URL+c.path, read, nil); r.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with a read token: %d %s", c.method, c.path, r.StatusCode, b)
		}
	}
	// Admin routes stay admin-only.
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/v1/tokens", TokenRequest{Name: "x"}},
		{http.MethodGet, "/v1/devices", nil},
		{http.MethodPost, "/v1/pairing-codes", nil},
		{http.MethodDelete, "/v1/devices/" + tr.Device.ID, nil},
	} {
		if r, _ := call(t, c.method, ts.URL+c.path, read, c.body); r.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s with a read token: %d", c.method, c.path, r.StatusCode)
		}
	}
	for _, bad := range []TokenRequest{{Name: ""}, {Name: "x", Scope: "admin"}, {Name: "a\x1b[2Jb"}, {Name: strings.Repeat("n", 101)}} {
		if r, b := call(t, http.MethodPost, ts.URL+"/v1/tokens", testToken, bad); r.StatusCode != http.StatusBadRequest {
			t.Errorf("create %+v: %d %s", bad, r.StatusCode, b)
		}
	}
	// Re-pairing with a read token as "replaces" makes a new upload device; the read token
	// keeps its scope.
	code := mint(t, ts.URL)
	status, pr := pairWith(t, ts.URL, code.Code, read)
	if status != http.StatusOK || pr.Device.ID == tr.Device.ID || pr.Device.Scope != ScopeUpload {
		t.Fatalf("pair replacing a read token: %d %+v", status, pr.Device)
	}
	if r, _ := post(t, ts.URL, pr.Token, `{"samples":[`+hrSample+`]}`); r.StatusCode != http.StatusOK {
		t.Errorf("upload with the new device token: %d", r.StatusCode)
	}
	_, body = call(t, http.MethodGet, ts.URL+"/v1/devices", testToken, nil)
	if !bytes.Contains(body, []byte(`"scope":"read"`)) || !bytes.Contains(body, []byte(`"scope":"upload"`)) {
		t.Errorf("devices list shows no scopes: %s", body)
	}
	// Revoked, it stops working.
	if r, _ := call(t, http.MethodDelete, ts.URL+"/v1/devices/"+tr.Device.ID, testToken, nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", r.StatusCode)
	}
	if r, _ := call(t, http.MethodGet, ts.URL+"/v1/status", read, nil); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked token: %d", r.StatusCode)
	}
}

func TestTokenCreateCommand(t *testing.T) {
	dir := t.TempDir()
	ts, _ := newTestServer(t, dir)
	tok := filepath.Join(dir, "token")
	os.WriteFile(tok, []byte(testToken+"\n"), 0o600)
	var out, errOut bytes.Buffer
	if err := runCommand("token create", []string{"-server", ts.URL, "-token-file", tok, "-name", "grafana"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(out.String())
	if len(token) < 40 || strings.Contains(token, " ") || !strings.Contains(errOut.String(), "read token") {
		t.Fatalf("stdout %q, stderr %q", out.String(), errOut.String())
	}
	out.Reset()
	if err := runCommand("devices", []string{"-server", ts.URL, "-token-file", tok}, &out, &errOut); err != nil ||
		!strings.Contains(out.String(), "SCOPE") || !strings.Contains(out.String(), "read") {
		t.Fatalf("devices: %v\n%s", err, out.String())
	}
	if err := runCommand("token create", []string{"-server", ts.URL, "-token-file", tok, "-name", "x", "-scope", "admin"}, &out, &errOut); err == nil {
		t.Error("scope admin accepted")
	}
}

// The setup page lists read tokens apart from paired apps, creates one and shows it once.
func TestSetupPageReadTokens(t *testing.T) {
	s := testServer(t)
	const lan = "192.168.1.20:5000"
	if w := setupRequest(t, s, "POST", "/setup/tokens", lan, nil, url.Values{"name": {"grafana"}}); w.Code != 303 || s.Devices.Len() != 0 {
		t.Fatalf("without signing in: %d, %d tokens", w.Code, s.Devices.Len())
	}
	w := setupRequest(t, s, "POST", "/setup", lan, nil, url.Values{"token": {testToken}})
	h := http.Header{"Cookie": {w.Result().Cookies()[0].String()}}
	s.Devices.Add("Phone of mine", "")
	w = setupRequest(t, s, "POST", "/setup/tokens", lan, h, url.Values{"name": {"grafana"}})
	body := w.Body.String()
	list := s.Devices.List()
	if w.Code != 200 || len(list) != 2 || list[1].Scope != ScopeRead || !strings.Contains(body, "Read token for grafana") {
		t.Fatalf("create: %d %+v\n%s", w.Code, list, body)
	}
	i := strings.Index(body, `<code class="token">`)
	token := body[i+len(`<code class="token">`):]
	token = token[:strings.Index(token, "<")]
	if dev, ok := s.Devices.Lookup(token); !ok || dev.Scope != ScopeRead {
		t.Fatalf("the shown token %q is not the read token", token)
	}
	page := setupRequest(t, s, "GET", "/setup", lan, h, nil).Body.String()
	apps, tokens := strings.Index(page, "Paired iPhones"), strings.Index(page, "Read tokens")
	if apps < 0 || tokens < apps || !strings.Contains(page[apps:tokens], "Phone of mine") || !strings.Contains(page[tokens:], "grafana") ||
		strings.Contains(page[apps:tokens], "grafana") || strings.Contains(page, token) {
		t.Errorf("setup page lists:\n%s", page)
	}
}
