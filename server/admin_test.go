package main

import (
	"bytes"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fileServer is a server whose state lives in dir, like the real one: admin.json (holding
// testToken's hash), devices.json and codes.json.
func fileServer(t *testing.T, dir string) *Server {
	t.Helper()
	a, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	if err := writeAdminToken(dir, testToken); err != nil {
		t.Fatal(err)
	}
	k, err := requireAdminKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	d, err := OpenDevices(filepath.Join(dir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := OpenCodes(filepath.Join(dir, "codes.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Archive: a, AdminKey: k, MaxBodyBytes: 1 << 20, Devices: d, Codes: c, PublicURL: "https://health.example.com"}
}

var printedToken = regexp.MustCompile(`Admin token: (\S+)`)

// assertNotStored fails if any file under dir holds one of the secrets.
func assertNotStored(t *testing.T, dir string, secrets ...string) {
	t.Helper()
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := os.ReadFile(path)
		for _, s := range secrets {
			if s != "" && bytes.Contains(b, []byte(s)) {
				t.Errorf("%s holds a secret in clear", path)
			}
		}
		return nil
	})
}

func TestFirstStartGeneratesAdminToken(t *testing.T) {
	dir := t.TempDir()
	var logs bytes.Buffer
	k, err := openAdmin(dir, "", "https://health.example.com/setup", log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	m := printedToken.FindStringSubmatch(logs.String())
	if m == nil || len(m[1]) != 43 || !strings.Contains(logs.String(), "Sign in at:  https://health.example.com/setup") ||
		!strings.Contains(logs.String(), "shown only this once") {
		t.Fatalf("first start printed:\n%s", logs.String())
	}
	token := m[1]
	if !k.Check(token) || k.Check(token+"x") || k.Check("") {
		t.Fatal("the key does not check the printed token")
	}
	fi, err := os.Stat(filepath.Join(dir, adminFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("admin.json: %v %v", fi, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, adminFile))
	if !strings.Contains(string(b), hashToken(token)) {
		t.Errorf("admin.json does not hold the hash:\n%s", b)
	}
	assertNotStored(t, dir, token)

	// The next start reads the hash and prints nothing.
	logs.Reset()
	k, err = openAdmin(dir, "", "", log.New(&logs, "", 0))
	if err != nil || !k.Check(token) || logs.Len() != 0 {
		t.Fatalf("second start: %v, printed %q", err, logs.String())
	}
}

func TestAdminResetSignsOutTheOldToken(t *testing.T) {
	dir := t.TempDir()
	s := fileServer(t, dir)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	const lan = "192.168.1.20:5000"
	w := setupRequest(t, s, "POST", "/setup", lan, nil, url.Values{"token": {testToken}})
	cookie := http.Header{"Cookie": {w.Result().Cookies()[0].String()}}
	if body := setupRequest(t, s, "GET", "/setup", lan, cookie, nil).Body.String(); !strings.Contains(body, "Paired iPhones") {
		t.Fatal("not signed in")
	}

	var out bytes.Buffer
	if err := runCommand("admin reset", []string{"-archive-dir", dir, "-public-url", "https://health.example.com"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	m := printedToken.FindStringSubmatch(out.String())
	if m == nil || !strings.Contains(out.String(), "https://health.example.com/setup") {
		t.Fatalf("admin reset printed:\n%s", out.String())
	}
	token := m[1]
	// The running server takes the new token at once, and only it.
	if r, _ := call(t, http.MethodGet, ts.URL+"/v1/devices", testToken, nil); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("old token after reset: %d", r.StatusCode)
	}
	if r, _ := call(t, http.MethodGet, ts.URL+"/v1/devices", token, nil); r.StatusCode != http.StatusOK {
		t.Errorf("new token after reset: %d", r.StatusCode)
	}
	if w := setupRequest(t, s, "POST", "/setup", lan, nil, url.Values{"token": {testToken}}); w.Code != http.StatusUnauthorized {
		t.Errorf("sign-in with the old token: %d", w.Code)
	}
	// A browser signed in with the old token is signed out.
	if body := setupRequest(t, s, "GET", "/setup", lan, cookie, nil).Body.String(); !strings.Contains(body, `name="token"`) {
		t.Error("a session from the old token still works")
	}
	assertNotStored(t, dir, token)
}

func TestTokenFileMigration(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tokenFile, []byte(testToken+"\n"), 0o600)
	var logs bytes.Buffer
	k, err := openAdmin(dir, tokenFile, "", log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if !k.Check(testToken) || !strings.Contains(logs.String(), "deprecated") || !strings.Contains(logs.String(), "delete "+tokenFile) ||
		strings.Contains(logs.String(), testToken) {
		t.Fatalf("import: %v\n%s", k.Check(testToken), logs.String())
	}
	assertNotStored(t, dir, testToken)

	// Still configured on the next start: the same hash, the same warning.
	logs.Reset()
	if k, err = openAdmin(dir, tokenFile, "", log.New(&logs, "", 0)); err != nil || !k.Check(testToken) || !strings.Contains(logs.String(), "deprecated") {
		t.Fatalf("second start: %v\n%s", err, logs.String())
	}
	// After a reset, admin.json wins over the file.
	if _, err := newAdminToken(dir); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	if k, err = openAdmin(dir, tokenFile, "", log.New(&logs, "", 0)); err != nil || k.Check(testToken) || !strings.Contains(logs.String(), "is ignored") {
		t.Fatalf("after reset: %v %v\n%s", err, k.Check(testToken), logs.String())
	}

	// A missing token file (the Docker image's default path) is skipped.
	logs.Reset()
	fresh := t.TempDir()
	if _, err = openAdmin(fresh, filepath.Join(fresh, "nope"), "", log.New(&logs, "", 0)); err != nil || printedToken.FindString(logs.String()) == "" {
		t.Fatalf("missing token file: %v\n%s", err, logs.String())
	}

	// A token file too short to be a credential is refused.
	os.WriteFile(tokenFile, []byte("short\n"), 0o600)
	if _, err := openAdmin(t.TempDir(), tokenFile, "", log.New(io.Discard, "", 0)); err == nil {
		t.Error("a 5-character token was imported")
	}
}

// Uploads with the admin token (apps set up by hand) still work, logged at most hourly and
// counted in /metrics.
func TestAdminTokenUploadsAreDeprecated(t *testing.T) {
	ts, _ := newTestServer(t, t.TempDir())
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for range 3 {
		if r, _ := post(t, ts.URL, testToken, `{"version":2,"samples":[`+hrSample+`]}`); r.StatusCode != http.StatusOK {
			t.Fatalf("upload with the admin token: %d", r.StatusCode)
		}
	}
	if r, _ := call(t, http.MethodGet, ts.URL+"/v1/status", testToken, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("status with the admin token: %d", r.StatusCode)
	}
	if n := strings.Count(logs.String(), "used the admin token"); n != 1 {
		t.Errorf("logged %d times:\n%s", n, logs.String())
	}
	_, b := call(t, "GET", ts.URL+"/metrics", "", nil)
	if !strings.Contains(string(b), "healthkit_relay_admin_token_uploads_total 4") {
		t.Errorf("metrics:\n%s", b)
	}
}

// Every secret the server hands out (admin token, read tokens, device tokens, setup codes)
// is stored as a hash only.
func TestStateDirHoldsOnlyHashes(t *testing.T) {
	dir := t.TempDir()
	var logs bytes.Buffer
	k, err := openAdmin(dir, "", "", log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	admin := printedToken.FindStringSubmatch(logs.String())[1]
	s := fileServer(t, dir)
	s.AdminKey = k
	read, _, _ := s.Devices.AddScoped("grafana", "", ScopeRead)
	upload, _, _ := s.Devices.Add("iPhone", "")
	code, _, _ := s.Codes.Mint()
	assertNotStored(t, dir, admin, read, upload, code)
}

func TestSetupPageCopyButton(t *testing.T) {
	s := testServer(t)
	s.PublicURL = "https://health.example.com"
	const lan = "192.168.1.20:5000"
	w := setupRequest(t, s, "POST", "/setup", lan, nil, url.Values{"token": {testToken}})
	h := http.Header{"Cookie": {w.Result().Cookies()[0].String()}}
	w = setupRequest(t, s, "POST", "/setup/tokens", lan, h, url.Values{"name": {"grafana"}})
	body := w.Body.String()
	for _, want := range []string{
		`<code class="token" id="new-token">`, `<button class="copy" type="button" data-copy="new-token"`, "<svg",
		"navigator.clipboard", "shown only this once", "password manager", "Bearer", "Revoke it",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("token page lacks %q", want)
		}
	}
	// The copy script runs under the page's CSP: it carries the nonce.
	csp := w.Header().Get("Content-Security-Policy")
	nonce := csp[strings.Index(csp, "'nonce-")+7:]
	nonce = nonce[:strings.Index(nonce, "'")]
	script := body[:strings.Index(body, `querySelectorAll("button.copy")`)]
	if !strings.HasPrefix(script[strings.LastIndex(script, "<script"):], `<script nonce="`+nonce+`">`) {
		t.Error("the copy script has no nonce")
	}
	// The pairing page offers the setup link to copy.
	page := setupRequest(t, s, "GET", "/setup", lan, h, nil).Body.String()
	if !strings.Contains(page, `data-copy="setup-link"`) || !strings.Contains(page, "https://health.example.com/setup?code=") {
		t.Error("the pairing page has no setup link to copy")
	}
}
