package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Phone-supplied strings cannot add or shift fields, tags or lines in line protocol.
func TestLinesEscapeClientStrings(t *testing.T) {
	f := &Forwarder{Measurement: "healthkit"}
	v := 62.0
	for _, source := range []string{`Watch\`, "Watch\nhealthkit evil=1 1", "Watch\t\x00", `a\,unit=x`} {
		s := &Sample{Kind: "quantity", Type: "HKQuantityTypeIdentifierHeartRate", Unit: "count/min", Value: &v,
			start: time.Unix(1700000000, 0)}
		s.Source.Name = source
		got := strings.TrimSuffix(string(f.Lines([]*Sample{s})), "\n")
		if strings.ContainsAny(got, "\n\r\t\x00") {
			t.Errorf("source %q: control character in %q", source, got)
		}
		// Unescaped, the line splits into exactly measurement+tags, fields, timestamp.
		if parts := splitUnescaped(got, ' '); len(parts) != 3 || parts[1] != "heart_rate=62" || parts[2] != "1700000000000000000" {
			t.Errorf("source %q: line %q splits into %q", source, got, parts)
		}
		if tags := splitUnescaped(splitUnescaped(got, ' ')[0], ','); len(tags) != 3 {
			t.Errorf("source %q: tags of %q: %q", source, got, tags)
		}
	}
	// Record value names become field names.
	recs := []*RecordIn{{Kind: "activity_summary", Values: []byte(`{"x\\":{"value":1,"unit":"kcal"}}`), start: time.Unix(1, 0)}}
	if got := string(f.RecordLines(recs)); len(splitUnescaped(strings.TrimSpace(got), ' ')) != 3 {
		t.Errorf("record line %q", got)
	}
}

// splitUnescaped splits s at sep not preceded by an escaping backslash, the way line-protocol
// parsers read it.
func splitUnescaped(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case sep:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func TestPairRejectsControlCharactersInNames(t *testing.T) {
	ts, _ := newTestServer(t, t.TempDir())
	for _, name := range []string{"iPhone\x1b]0;pwned\x07", "iPhone\nrevoked device x", "iPhone\u0085\u009b31m"} {
		c := mint(t, ts.URL)
		resp, b := call(t, "POST", ts.URL+"/v1/pair", "", PairRequest{Code: c.Code, Name: name})
		if resp.StatusCode != 400 {
			t.Errorf("name %q: %d %s", name, resp.StatusCode, b)
		}
		resp, b = call(t, "POST", ts.URL+"/v1/pair", "", PairRequest{Code: c.Code, Name: "iPhone", Model: name})
		if resp.StatusCode != 400 {
			t.Errorf("model %q: %d %s", name, resp.StatusCode, b)
		}
	}
	// Names people give phones still work.
	c := mint(t, ts.URL)
	if code, p := pairWith(t, ts.URL, c.Code, ""); code != 200 {
		t.Fatalf("plain name: %d %+v", code, p)
	}
	c = mint(t, ts.URL)
	if resp, b := call(t, "POST", ts.URL+"/v1/pair", "", PairRequest{Code: c.Code, Name: "Zoë’s iPhone 📱"}); resp.StatusCode != 200 {
		t.Fatalf("unicode name: %d %s", resp.StatusCode, b)
	}
}

func TestSetupFormsRefuseCrossOrigin(t *testing.T) {
	s := testServer(t)
	const lan = "192.168.1.20:5000"
	w := setupRequest(t, s, "POST", "/setup", lan, nil, url.Values{"token": {testToken}})
	cookie := w.Result().Cookies()[0].String()
	_, dev, _ := s.Devices.Add("iPhone", "")

	for _, site := range []string{"cross-site", "same-site"} {
		h := http.Header{"Sec-Fetch-Site": {site}}
		if w := setupRequest(t, s, "POST", "/setup", lan, h, url.Values{"token": {testToken}}); w.Code != 403 || len(w.Result().Cookies()) > 0 {
			t.Errorf("%s sign-in: %d", site, w.Code)
		}
		h.Set("Cookie", cookie)
		if w := setupRequest(t, s, "POST", "/setup/revoke", lan, h, url.Values{"id": {dev.ID}}); w.Code != 403 {
			t.Errorf("%s revoke: %d", site, w.Code)
		}
	}
	if s.Devices.Len() != 1 {
		t.Fatal("a cross-origin post revoked the device")
	}
	// The page's own forms still work.
	h := http.Header{"Sec-Fetch-Site": {"same-origin"}, "Cookie": {cookie}}
	if w := setupRequest(t, s, "POST", "/setup/revoke", lan, h, url.Values{"id": {dev.ID}}); w.Code != 303 || s.Devices.Len() != 0 {
		t.Fatalf("same-origin revoke: %d, %d devices", w.Code, s.Devices.Len())
	}
	// Huge form bodies are refused before parsing.
	big := url.Values{"token": {strings.Repeat("x", 1<<20)}}
	if w := setupRequest(t, s, "POST", "/setup", lan, nil, big); w.Code != 400 {
		t.Errorf("1 MB form: %d", w.Code)
	}
	if got := setupRequest(t, s, "GET", "/setup", lan, nil, nil).Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options %q", got)
	}
}
