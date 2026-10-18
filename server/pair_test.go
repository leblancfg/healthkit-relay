package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func call(t *testing.T, method, url, token string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func mint(t *testing.T, base string) CodeResponse {
	t.Helper()
	resp, b := call(t, "POST", base+"/v1/pairing-codes", testToken, map[string]string{"public_url": "https://health.example.com"})
	var c CodeResponse
	if resp.StatusCode != 200 || json.Unmarshal(b, &c) != nil || c.Code == "" {
		t.Fatalf("mint: %d %s", resp.StatusCode, b)
	}
	return c
}

func pairWith(t *testing.T, base, code, replaces string) (int, PairResponse) {
	t.Helper()
	resp, b := call(t, "POST", base+"/v1/pair", "", PairRequest{Code: code, Name: "iPhone", Model: "iPhone17,1", Replaces: replaces})
	var p PairResponse
	json.Unmarshal(b, &p)
	return resp.StatusCode, p
}

func TestPairingFlow(t *testing.T) {
	dir := t.TempDir()
	ts, _ := newTestServer(t, dir)

	// Only the server token mints codes.
	if resp, _ := call(t, "POST", ts.URL+"/v1/pairing-codes", "", nil); resp.StatusCode != 401 {
		t.Fatalf("mint without token: %d", resp.StatusCode)
	}
	c := mint(t, ts.URL)
	if c.Link != "healthrelay://pair?code="+c.Code+"&server=https%3A%2F%2Fhealth.example.com" ||
		c.SetupURL != "https://health.example.com/setup?code="+c.Code || time.Until(c.ExpiresAt) < 14*time.Minute {
		t.Fatalf("code: %+v", c)
	}

	code, p := pairWith(t, ts.URL, c.Code, "")
	if code != 200 || len(p.Token) < 40 || p.Device.ID == "" || p.Device.Name != "iPhone" || p.Server.WireVersion != 2 {
		t.Fatalf("pair: %d %+v", code, p)
	}
	if code, _ := pairWith(t, ts.URL, c.Code, ""); code != 403 {
		t.Fatalf("second use of a code: %d, want 403", code)
	}
	// Codes are case- and dash-insensitive (typed by hand).
	c2 := mint(t, ts.URL)
	if code, _ := pairWith(t, ts.URL, strings.ToLower(c2.Code[:4])+"-"+c2.Code[4:], ""); code != 200 {
		t.Fatalf("lower-case code: %d", code)
	}

	// The device token uploads and reads its status; it is not an admin.
	if resp, _ := post(t, ts.URL, p.Token, `{"samples":[`+hrSample+`]}`); resp.StatusCode != 200 {
		t.Fatalf("upload with device token: %d", resp.StatusCode)
	}
	resp, b := call(t, "GET", ts.URL+"/v1/status", p.Token, nil)
	var st Status
	if json.Unmarshal(b, &st); resp.StatusCode != 200 || st.Records != 1 || st.Device == nil || st.Device.ID != p.Device.ID {
		t.Fatalf("status: %d %s", resp.StatusCode, b)
	}
	if resp, _ := call(t, "GET", ts.URL+"/v1/devices", p.Token, nil); resp.StatusCode != 401 {
		t.Fatalf("device token listing devices: %d", resp.StatusCode)
	}
	resp, b = call(t, "GET", ts.URL+"/v1/devices", testToken, nil)
	if resp.StatusCode != 200 || strings.Count(string(b), `"id"`) != 2 || strings.Contains(string(b), "token") {
		t.Fatalf("devices: %d %s", resp.StatusCode, b)
	}

	// Stored hashed, private, and reloaded after a restart.
	raw, _ := os.ReadFile(filepath.Join(dir, "devices.json"))
	if strings.Contains(string(raw), p.Token) || !strings.Contains(string(raw), hashToken(p.Token)) {
		t.Fatalf("devices.json: %s", raw)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "devices.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("devices.json mode %v", fi.Mode().Perm())
	}
	ts.Close()
	ts, _ = newTestServer(t, dir)
	if resp, _ := call(t, "GET", ts.URL+"/v1/status", p.Token, nil); resp.StatusCode != 200 {
		t.Fatalf("device token after restart: %d", resp.StatusCode)
	}

	// Revoked: the token stops working at once; the server token still uploads.
	if resp, _ := call(t, "DELETE", ts.URL+"/v1/devices/"+p.Device.ID, testToken, nil); resp.StatusCode != 204 {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if resp, _ := post(t, ts.URL, p.Token, `{"samples":[`+hrSample+`]}`); resp.StatusCode != 401 {
		t.Fatalf("upload after revoke: %d", resp.StatusCode)
	}
	if resp, _ := call(t, "DELETE", ts.URL+"/v1/devices/"+p.Device.ID, testToken, nil); resp.StatusCode != 404 {
		t.Fatalf("revoke twice: %d", resp.StatusCode)
	}
	if resp, _ := post(t, ts.URL, testToken, `{"samples":[`+hrSample+`]}`); resp.StatusCode != 200 {
		t.Fatalf("upload with server token: %d", resp.StatusCode)
	}
}

func TestRepairingReplacesTheOldDevice(t *testing.T) {
	ts, _ := newTestServer(t, t.TempDir())
	_, first := pairWith(t, ts.URL, mint(t, ts.URL).Code, "")
	_, second := pairWith(t, ts.URL, mint(t, ts.URL).Code, first.Token)
	if resp, _ := call(t, "GET", ts.URL+"/v1/status", first.Token, nil); resp.StatusCode != 401 {
		t.Errorf("replaced token: %d", resp.StatusCode)
	}
	// Same device, same ID (an erase finds what it uploaded before), new token.
	if second.Device.ID != first.Device.ID || second.Token == first.Token {
		t.Errorf("re-paired: %+v, first %+v", second.Device, first.Device)
	}
	if resp, _ := call(t, "GET", ts.URL+"/v1/status", second.Token, nil); resp.StatusCode != 200 {
		t.Errorf("new token: %d", resp.StatusCode)
	}
	// The server token is never revoked that way.
	pairWith(t, ts.URL, mint(t, ts.URL).Code, testToken)
	_, b := call(t, "GET", ts.URL+"/v1/devices", testToken, nil)
	if strings.Count(string(b), `"id"`) != 2 || !strings.Contains(string(b), second.Device.ID) {
		t.Errorf("devices: %s", b)
	}
}

func TestCodesExpire(t *testing.T) {
	c := NewCodes()
	now := time.Now()
	c.now = func() time.Time { return now }
	code, _ := c.Mint()
	if state, _, _ := c.State(code); state != "pending" {
		t.Fatalf("state %s", state)
	}
	now = now.Add(CodeTTL + time.Second)
	if state, _, _ := c.State(code); state != "invalid" {
		t.Fatalf("expired code: %s", state)
	}
	// Longer codes for links sent to a phone, at most a day.
	if _, exp := c.MintFor(2 * time.Hour); exp.Sub(now) != 2*time.Hour {
		t.Errorf("2 h code expires %v", exp.Sub(now))
	}
	if _, exp := c.MintFor(30 * 24 * time.Hour); exp.Sub(now) != MaxCodeTTL {
		t.Errorf("month-long code expires %v", exp.Sub(now))
	}
	if _, err := c.Redeem(code, func() (Device, error) { return Device{}, nil }); err != errBadCode {
		t.Fatalf("redeem expired: %v", err)
	}
}

func setupRequest(t *testing.T, s *Server, method, target, remote string, header http.Header, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, target, body)
	r.RemoteAddr = remote
	for k, v := range header {
		r.Header[k] = v
	}
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func testServer(t *testing.T) *Server {
	dir := t.TempDir()
	a, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	d, _ := OpenDevices(filepath.Join(dir, "devices.json"))
	return &Server{Archive: a, Token: testToken, MaxBodyBytes: 1 << 20, Devices: d, Codes: NewCodes()}
}

func TestSetupPageNeedsTokenOrCode(t *testing.T) {
	s := testServer(t)
	const lan = "192.168.1.20:5000"

	w := setupRequest(t, s, "GET", "/setup", lan, nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `name="token"`) || strings.Contains(w.Body.String(), "healthrelay://") {
		t.Fatalf("anonymous: %d\n%s", w.Code, w.Body)
	}
	if w := setupRequest(t, s, "GET", "/setup?code=NOTACODE", lan, nil, nil); w.Code != 410 || strings.Contains(w.Body.String(), "healthrelay://") {
		t.Fatalf("bogus code: %d", w.Code)
	}
	if w := setupRequest(t, s, "POST", "/setup", lan, nil, url.Values{"token": {"wrong-token-0123456789"}}); w.Code != 401 || len(w.Result().Cookies()) > 0 {
		t.Fatalf("wrong token: %d %v", w.Code, w.Result().Cookies())
	}

	// The token signs in: a session cookie, then a page with a fresh code and the devices.
	w = setupRequest(t, s, "POST", "/setup", lan, nil, url.Values{"token": {testToken}})
	cookies := w.Result().Cookies()
	if w.Code != 303 || len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("sign in: %d %v", w.Code, cookies)
	}
	h := http.Header{"Cookie": {cookies[0].String()}}
	w = setupRequest(t, s, "GET", "/setup", lan, h, nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "<svg") || !strings.Contains(body, `href="healthrelay://pair?code=`) ||
		!strings.Contains(body, "Paired iPhones") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("signed in: %d\n%s", w.Code, body)
	}
	// The link points where the browser reached the server.
	if !strings.Contains(body, "server=http%3a%2f%2fexample.com") && !strings.Contains(body, "server=http%3A%2F%2Fexample.com") {
		t.Errorf("link server: %s", body)
	}

	// A code from `healthkit-relay pair` opens the page without the token, and only for it.
	c := s.mintCode("https://health.example.com")
	w = setupRequest(t, s, "GET", "/setup?code="+c.Code, lan, nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "code="+c.Code) || strings.Contains(w.Body.String(), "Paired iPhones") {
		t.Fatalf("code page: %d", w.Code)
	}
	if w := setupRequest(t, s, "GET", "/setup/status?code="+c.Code, lan, nil, nil); !strings.Contains(w.Body.String(), `"pending"`) {
		t.Fatalf("status: %s", w.Body)
	}
	s.Codes.Redeem(c.Code, func() (Device, error) { return Device{ID: "abc", Name: "iPhone"}, nil })
	if w := setupRequest(t, s, "GET", "/setup/status?code="+c.Code, lan, nil, nil); !strings.Contains(w.Body.String(), `"paired"`) {
		t.Fatalf("status after pairing: %s", w.Body)
	}
	if w := setupRequest(t, s, "GET", "/setup?code="+c.Code, lan, nil, nil); !strings.Contains(w.Body.String(), "Already connected") ||
		strings.Contains(w.Body.String(), "healthrelay://") {
		t.Fatalf("used code page: %s", w.Body)
	}
}

func TestSetupOnlyOnPrivateNetworks(t *testing.T) {
	s := testServer(t)
	for _, c := range []struct {
		remote, xff string
		ok          bool
	}{
		{"127.0.0.1:1", "", true},
		{"10.0.0.5:1", "", true},
		{"100.88.12.34:1", "", true}, // Tailscale
		{"[fd7a:115c:a1e0::1]:1", "", true},
		{"203.0.113.9:1", "", false},
		{"127.0.0.1:1", "100.88.12.34", true}, // Tailscale serve
		{"127.0.0.1:1", "203.0.113.9", false}, // a public reverse proxy (or Funnel)
		{"127.0.0.1:1", "100.64.0.1, 203.0.113.9", false},
		{"203.0.113.9:1", "10.0.0.1", false}, // forwarded headers only count from loopback
	} {
		var h http.Header
		if c.xff != "" {
			h = http.Header{"X-Forwarded-For": {c.xff}}
		}
		w := setupRequest(t, s, "GET", "/setup", c.remote, h, nil)
		if (w.Code == 200) != c.ok {
			t.Errorf("%s via %q: %d", c.remote, c.xff, w.Code)
		}
		w = setupRequest(t, s, "POST", "/v1/pair", c.remote, h, nil)
		if (w.Code == 403) == c.ok {
			t.Errorf("pair from %s via %q: %d", c.remote, c.xff, w.Code)
		}
	}
	s.SetupFrom = "any"
	if w := setupRequest(t, s, "GET", "/setup", "203.0.113.9:1", nil, nil); w.Code != 200 {
		t.Errorf("-setup-from any: %d", w.Code)
	}
	s.SetupFrom = "off"
	if w := setupRequest(t, s, "GET", "/setup", "127.0.0.1:1", nil, nil); w.Code != 403 {
		t.Errorf("-setup-from off: %d", w.Code)
	}
}

// The real shape of `tailscale serve status --json` (names changed).
const serveStatus = `{"TCP":{"8180":{"HTTPS":true},"9428":{"TCPForward":"127.0.0.1:9428"}},
 "Web":{"box.tailnet.ts.net:8180":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:8180"}}},
        "box.tailnet.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3000"}}},
        "box.tailnet.ts.net:8443":{"Handlers":{"/":{"Proxy":"127.0.0.1:8180"}}},
        "box.tailnet.ts.net:9000":{"Handlers":{"/":{"Proxy":"http://10.0.0.9:8180"}}}}}`

func fakeNet(s *Server, listen string, addrs []string, serve string) {
	var ips []netip.Addr
	for _, a := range addrs {
		ips = append(ips, netip.MustParseAddr(a))
	}
	s.Listen = listen
	s.netInfo = &netInfo{
		interfaces: func() []netip.Addr { return ips },
		tailscale:  func() tailscaleInfo { return tailscaleInfo{Serve: parseServeStatus([]byte(serve))} },
	}
}

func urls(as []Address) string {
	var out []string
	for _, a := range as {
		out = append(out, a.URL)
	}
	return strings.Join(out, " ")
}

func TestAddressesNeverLoopback(t *testing.T) {
	s := testServer(t)
	machine := []string{"127.0.0.1", "::1", "169.254.10.1", "fe80::1", "203.0.113.7", "192.168.1.20", "100.88.12.34", "fd7a:115c:a1e0::1"}
	req := func(host, remote string) *http.Request {
		r := httptest.NewRequest("GET", "http://"+host+"/setup", nil)
		r.RemoteAddr = remote
		return r
	}

	// Listening everywhere, page opened on localhost: the LAN addresses (private first),
	// then Tailscale; never loopback, link-local or IPv6.
	fakeNet(s, "0.0.0.0:8080", machine, "")
	if got := urls(s.addresses(req("127.0.0.1:8080", "127.0.0.1:5"))); got != "http://192.168.1.20:8080 http://203.0.113.7:8080 http://100.88.12.34:8080" {
		t.Errorf("listening everywhere, opened on localhost: %s", got)
	}
	// Opened by name from the LAN: that name first.
	if got := urls(s.addresses(req("relay.local:8080", "192.168.1.30:5"))); !strings.HasPrefix(got, "http://relay.local:8080 http://192.168.1.20:8080") {
		t.Errorf("opened by name: %s", got)
	}
	// Loopback only: what a local proxy says, and Tailscale serve names proxying to the port.
	fakeNet(s, "127.0.0.1:8180", machine, serveStatus)
	r := req("127.0.0.1:8180", "127.0.0.1:5")
	if got := urls(s.addresses(r)); got != "https://box.tailnet.ts.net:8180 https://box.tailnet.ts.net:8443" {
		t.Errorf("loopback with Tailscale serve: %s", got)
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "health.example.com")
	if got := s.addresses(r); len(got) == 0 || got[0].URL != "https://health.example.com" || got[0].Source != "this page's address" {
		t.Errorf("behind a local proxy: %+v", got)
	}
	// Loopback only, nothing else: no address at all.
	fakeNet(s, "127.0.0.1:8080", machine, "")
	for _, host := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080", "169.254.1.1:8080"} {
		if got := s.addresses(req(host, "127.0.0.1:5")); len(got) != 0 {
			t.Errorf("opened at %s: %+v", host, got)
		}
	}
	// A specific listen address: only that one.
	fakeNet(s, "192.168.1.20:8080", machine, "")
	if got := urls(s.addresses(req("192.168.1.20:8080", "192.168.1.30:5"))); got != "http://192.168.1.20:8080" {
		t.Errorf("listening on one address: %s", got)
	}
	// -public-url wins, alone.
	s.PublicURL = "https://set.example.com/"
	if got := s.addresses(r); len(got) != 1 || got[0].URL != "https://set.example.com" {
		t.Errorf("configured: %+v", got)
	}
}

func TestSetupPageAddressChoice(t *testing.T) {
	s := testServer(t)
	fakeNet(s, "0.0.0.0:8080", []string{"192.168.1.20", "100.88.12.34"}, "")
	c := s.mintCode("")
	if c.Link != "" || c.SetupURL != "" {
		t.Fatalf("no address, yet a link: %+v", c)
	}
	page := func(query string) string {
		return setupRequest(t, s, "GET", "http://127.0.0.1:8080/setup?code="+c.Code+query, "127.0.0.1:5", nil, nil).Body.String()
	}
	body := page("")
	if !strings.Contains(body, "server=http%3a%2f%2f192.168.1.20%3a8080") || !strings.Contains(body, "Address in the code") ||
		strings.Contains(body, "127.0.0.1") {
		t.Fatalf("default choice:\n%s", body)
	}
	if body := page("&server=http://100.88.12.34:8080"); !strings.Contains(strings.ToLower(body),
		strings.ToLower(`href="healthrelay://pair?code=`+c.Code+`&amp;server=http%3a%2f%2f100.88.12.34%3a8080"`)) {
		t.Errorf("picked address not in the link:\n%s", body)
	}
	// Only found addresses can be put in a link.
	if body := page("&server=https://evil.example.com"); strings.Contains(body, "evil") {
		t.Errorf("arbitrary server accepted")
	}
	// Nothing reachable: no code at all, and what to do.
	fakeNet(s, "127.0.0.1:8080", []string{"192.168.1.20"}, "")
	body = page("")
	if strings.Contains(body, "healthrelay://") || !strings.Contains(body, "No address for your iPhone") {
		t.Errorf("loopback only:\n%s", body)
	}
	// With -public-url, no choice is offered.
	s.PublicURL = "https://health.example.com"
	if body := page(""); strings.Contains(body, "Address in the code") || !strings.Contains(body, "health.example.com") {
		t.Errorf("configured:\n%s", body)
	}
}

func TestPairCommand(t *testing.T) {
	ts, _ := newTestServer(t, t.TempDir())
	tok := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tok, []byte(testToken+"\n"), 0o600)
	var out bytes.Buffer
	if err := runCommand("pair", []string{"-server", ts.URL, "-token-file", tok, "-public-url", "https://health.example.com"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "https://health.example.com/setup?code=") || !strings.Contains(out.String(), "█") ||
		!strings.Contains(out.String(), "until") ||
		strings.Contains(out.String(), "Others") {
		t.Fatalf("pair output:\n%s", out.String())
	}
	out.Reset()
	if err := runCommand("pair", []string{"-server", ts.URL, "-token-file", tok, "-public-url", "https://h.example", "-valid", "3h"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	// The test server listens on loopback only: no address to offer, so no code.
	if err := runCommand("pair", []string{"-server", ts.URL, "-token-file", tok}, &out); err == nil || !strings.Contains(err.Error(), "-public-url") ||
		strings.Contains(out.String(), "127.0.0.1") {
		t.Fatalf("pair without any reachable address: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := runCommand("devices", []string{"-server", ts.URL, "-token-file", tok}, &out); err != nil || !strings.Contains(out.String(), "No paired devices") {
		t.Fatalf("devices: %v %s", err, out.String())
	}
	if err := runCommand("revoke", []string{"-server", ts.URL, "-token-file", tok, "nope"}, &out); err == nil {
		t.Fatal("revoke of an unknown device should fail")
	}
}

func TestSetupSessionCookieWorksThroughBrowser(t *testing.T) {
	ts, _ := newTestServer(t, t.TempDir())
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.PostForm(ts.URL+"/setup", url.Values{"token": {testToken}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// Signed in: the page for the admin (here without a link: the test server is on loopback).
	if resp.StatusCode != 200 || strings.Contains(string(b), `name="token"`) || !strings.Contains(string(b), "No address for your iPhone") {
		t.Fatalf("after sign-in redirect: %d", resp.StatusCode)
	}
}

func TestIncompleteUploadIsNotABadRequest(t *testing.T) {
	ts, _ := newTestServer(t, t.TempDir())
	post(t, ts.URL, testToken, `{"version":2,"samples":[`+hrSample[:40])
	_, b := call(t, "GET", ts.URL+"/metrics", "", nil)
	if !strings.Contains(string(b), `result="incomplete"} 1`) || !strings.Contains(string(b), `result="bad_request"} 0`) {
		t.Fatalf("metrics:\n%s", b)
	}
}
