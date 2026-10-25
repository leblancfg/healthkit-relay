package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The demo server's contract: what the app and App Review see. The public demo runs as a
// Cloudflare Worker (demo/worker), a separate implementation of this server's demo mode;
// this suite runs the same requests against both, so they cannot drift apart.
//
// By default it runs against this server's demo mode. With HKR_DEMO_URL and HKR_DEMO_CODE it
// runs against that server instead (the Worker under wrangler dev in CI, or the deployed
// one): HKR_DEMO_PUBLIC_URL is the address it names as preferred (default HKR_DEMO_URL), and
// HKR_DEMO_QUOTA and HKR_DEMO_MAX_BODY, when set to the server's (small) limits, add the
// checks that use them up.
//
//	HKR_DEMO_URL=https://health-relay-demo.leblancfg.com HKR_DEMO_CODE=... go test -run TestDemoContract -v
func TestDemoContract(t *testing.T) {
	c := demoContract{t: t, base: os.Getenv("HKR_DEMO_URL"), code: os.Getenv("HKR_DEMO_CODE")}
	if c.base == "" {
		const quota, maxBody = 1 << 20, 128 << 10
		dir := t.TempDir()
		a, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { a.Close() })
		demo, err := NewDemo(dir, demoCode, time.Hour, quota)
		if err != nil {
			t.Fatal(err)
		}
		s := &Server{Archive: a, AdminKey: NewAdminKey(testToken), MaxBodyBytes: maxBody, Codes: NewCodes(), SetupFrom: "any",
			PublicURL: "https://demo.example", ID: "5f0c2a9e41d7b3c86a0e2f4b1d3c5a79", Demo: demo}
		ts := httptest.NewServer(s.Handler())
		t.Cleanup(ts.Close)
		c.base, c.code, c.public, c.quota, c.maxBody = ts.URL, demoCode, s.PublicURL, quota, maxBody
	} else {
		c.base = strings.TrimRight(c.base, "/")
		c.public = cmpOr(os.Getenv("HKR_DEMO_PUBLIC_URL"), c.base)
		c.quota, _ = strconv.Atoi(os.Getenv("HKR_DEMO_QUOTA"))
		c.maxBody, _ = strconv.Atoi(os.Getenv("HKR_DEMO_MAX_BODY"))
		if c.code == "" {
			t.Fatal("HKR_DEMO_URL needs HKR_DEMO_CODE")
		}
	}
	c.run()
}

type demoContract struct {
	t                   *testing.T
	base, public, code  string
	quota, maxBody      int
	token, deviceID, id string
}

type reply struct {
	code   int
	header http.Header
	body   string
}

func (r reply) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("not a JSON object (%d): %q", r.code, r.body)
	}
	return m
}

func (r reply) errorText(t *testing.T) string {
	t.Helper()
	s, _ := r.json(t)["error"].(string)
	return s
}

var noRedirects = &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

func (c *demoContract) do(method, path, token, body string) reply {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, resp.Header, string(b)}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c *demoContract) run() {
	t := c.t
	t.Run("healthz", func(t *testing.T) {
		if r := c.do("GET", "/healthz", "", ""); r.code != 200 || r.body != "ok\n" {
			t.Errorf("GET /healthz: %d %q", r.code, r.body)
		}
	})
	t.Run("server", func(t *testing.T) {
		r := c.do("GET", "/v1/server", "", "")
		m := r.json(t)
		c.id, _ = m["server_id"].(string)
		if r.code != 200 || !regexp.MustCompile(`^[0-9a-f]{16,}$`).MatchString(c.id) || m["wire_version"] != 2.0 ||
			len(m) != 2 || r.header.Get("Cache-Control") != "no-store" {
			t.Errorf("GET /v1/server: %d %s %v", r.code, r.body, r.header)
		}
	})
	t.Run("no token", func(t *testing.T) {
		for _, rq := range [][2]string{{"GET", "/v1/status"}, {"POST", "/v1/samples"}, {"POST", "/v1/heartbeat"}, {"DELETE", "/v1/data"}} {
			for _, token := range []string{"", "demo.AAAAAAAAAAAAAAAAAAAAAA.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "not-a-token"} {
				r := c.do(rq[0], rq[1], token, "{}")
				if r.code != 401 || r.errorText(t) != "unauthorized" || r.header.Get("WWW-Authenticate") != "Bearer" {
					t.Errorf("%s %s with %q: %d %s", rq[0], rq[1], token, r.code, r.body)
				}
			}
		}
	})
	t.Run("pair refused", func(t *testing.T) {
		for _, tc := range []struct {
			body   string
			code   int
			prefix string
		}{
			{`{`, 400, "invalid JSON: "},
			{`[]`, 400, "invalid JSON: "},
			{`{"code": 5}`, 400, "invalid JSON: "},
			{`{"code":"AAAAAAAAAAAAAAAAAAAA"}`, 403, badCodeMessage},
			{`{}`, 403, badCodeMessage},
			{fmt.Sprintf(`{"code":%q,"name":%q}`, c.code, strings.Repeat("n", 101)), 400, "name and model must be at most 100 characters each"},
			{fmt.Sprintf(`{"code":%q,"model":%q}`, c.code, strings.Repeat("é", 51)), 400, "name and model must be at most 100 characters each"},
			{fmt.Sprintf(`{"code":%q,"name":"a\nb"}`, c.code), 400, "name and model must be printable: line breaks, tabs and other control characters are refused"},
			{fmt.Sprintf(`{"code":%q,"name":"x","pad":%q}`, c.code, strings.Repeat("p", 5000)), 400, "invalid JSON: "},
		} {
			r := c.do("POST", "/v1/pair", "", tc.body)
			if got := r.errorText(t); r.code != tc.code || !strings.HasPrefix(got, tc.prefix) {
				t.Errorf("pair %.60s: %d %q, want %d %q", tc.body, r.code, got, tc.code, tc.prefix)
			}
		}
	})
	t.Run("pair", func(t *testing.T) {
		typed := " " + strings.ToLower(c.code) + " "
		r := c.do("POST", "/v1/pair", "", fmt.Sprintf(`{"code":%q,"name":"Reviewer's iPhone","model":"iPhone18,2"}`, typed))
		if r.code != 200 || r.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("pair: %d %s", r.code, r.body)
		}
		m := r.json(t)
		c.token, _ = m["token"].(string)
		if !strings.HasPrefix(c.token, "demo.") || !reflect.DeepEqual(keys(m), []string{"device", "server", "token"}) {
			t.Errorf("pair answer: %s", r.body)
		}
		dev, _ := m["device"].(map[string]any)
		c.deviceID = c.checkDevice(t, dev)
		srv, _ := m["server"].(map[string]any)
		c.checkStatus(t, srv)
	})
	if c.token == "" {
		t.Fatal("no token: the rest needs one")
	}
	t.Run("status", func(t *testing.T) {
		r := c.do("GET", "/v1/status", c.token, "")
		if r.code != 200 || r.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("status: %d %s", r.code, r.body)
		}
		c.checkStatus(t, r.json(t))
	})
	t.Run("uploads accepted", func(t *testing.T) {
		before := c.records(t)
		u1, u2 := "6F1A2B3C-0000-4000-8000-000000000001", "6f1a2b3c-0000-4000-8000-000000000002"
		for _, tc := range []struct {
			name, body string
			want       map[string]float64
		}{
			{"v2", `{"version":2,"device":{"name":"x"},"trigger":"foreground","app_state":"active","samples":[` +
				`{"uuid":"` + u1 + `","type":"HKQuantityTypeIdentifierStepCount","kind":"quantity","start":"2026-10-25T08:00:00Z","end":"2026-10-25T08:10:00.5Z","value":120,"unit":"count","source":{"name":"iPhone","bundle_id":"com.apple.health"}},` +
				`{"uuid":"` + u2 + `","type":"HKWorkoutTypeIdentifier","kind":"workout","start":"2026-10-25T12:00:00-04:00","end":"2026-10-25T16:00:00Z","workout":{"activity_type":37,"duration_s":0,"energy_kcal":null}}],` +
				`"records":[{"kind":"activity_summary","key":"2026-10-25","start":"2026-10-25T00:00:00-04:00","values":{"active_energy":{"value":512,"unit":"kcal"}}},` +
				`{"kind":"route_points","key":"` + u2 + `/0","sample_uuid":"bogus","sample_type":"??","series":"Bad","points":[{"t":"2026-10-25T16:00:00Z","lat":1}]}],` +
				`"deletions":[{"uuid":"` + u1 + `","type":"not a type"}]}`,
				map[string]float64{"received": 5, "stored": 5, "duplicates": 0, "deletions": 1, "records": 2, "version": 2}},
			{"v1", `{"samples":[{"uuid":"` + u1 + `","type":"HKQuantityTypeIdentifierHeartRate","kind":"quantity","start":"2026-10-25T08:00:00.000000001+00:00","end":"2026-10-25T08:00:00.000000001Z","value":61}],"deletions":[]}`,
				map[string]float64{"received": 1, "stored": 1, "duplicates": 0, "deletions": 0, "records": 0, "version": 2}},
			{"keys in another case", `{"Samples":[{"UUID":"` + u1 + `","TYPE":"HKQuantityTypeIdentifierHeartRate","start":"2026-10-25T8:00:00Z","end":"2026-10-26T08:00:00,0+23:60"}]}`,
				map[string]float64{"received": 1, "stored": 1, "duplicates": 0, "deletions": 0, "records": 0, "version": 2}},
			{"trailing data", `{"samples":[]} {"samples":[{"uuid":"x"}]}`, map[string]float64{"received": 0, "stored": 0, "duplicates": 0, "deletions": 0, "records": 0, "version": 2}},
			{"empty", `{}`, map[string]float64{"received": 0, "stored": 0, "duplicates": 0, "deletions": 0, "records": 0, "version": 2}},
			{"null", `null`, map[string]float64{"received": 0, "stored": 0, "duplicates": 0, "deletions": 0, "records": 0, "version": 2}},
			{"nulls", `{"samples":null,"records":null,"deletions":null,"version":null}`, map[string]float64{"received": 0, "stored": 0, "duplicates": 0, "deletions": 0, "records": 0, "version": 2}},
		} {
			r := c.do("POST", "/v1/samples", c.token, tc.body)
			if r.code != 200 || r.header.Get("Content-Type") != "application/json" {
				t.Errorf("%s: %d %s", tc.name, r.code, r.body)
				continue
			}
			got := map[string]float64{}
			json.Unmarshal([]byte(r.body), &got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s: %s, want %v", tc.name, r.body, tc.want)
			}
		}
		// Records on server: everything received this hour, all devices (the count resets hourly).
		if after := c.records(t); after < before+7 && after >= before { // fewer: the hourly reset came in between
			t.Errorf("records on server went from %d to %d after 7 received", before, after)
		}
	})
	t.Run("uploads refused", func(t *testing.T) {
		u := "6F1A2B3C-0000-4000-8000-00000000000A"
		sample := func(fields string) string {
			return `{"samples":[{"uuid":"` + u + `","kind":"quantity"` + fields + `}]}`
		}
		ok := `,"type":"HKQuantityTypeIdentifierStepCount","start":"2026-10-25T08:00:00Z","end":"2026-10-25T08:00:00Z"`
		var parts strings.Builder
		for i := range MaxBatchPartitions + 1 {
			if i > 0 {
				parts.WriteByte(',')
			}
			fmt.Fprintf(&parts, `{"kind":"k%d","key":"x"}`, i)
		}
		for _, tc := range []struct {
			body, want string // want: the error, or its start when it ends with "…"
		}{
			{``, "invalid JSON: EOF"},
			{`{"samples":[{"uuid":"`, "incomplete body"},
			{`{"samples":[`, "incomplete body"},
			{`{"samples":5}`, "invalid JSON: …"},
			{`[]`, "invalid JSON: …"},
			{`{"samples":[]]`, "invalid JSON: …"},
			{`{"version":2.5}`, "invalid JSON: …"},
			{`{"samples":["x"]}`, "samples[0]: …"},
			{`{"samples":[{"uuid":"x"}]}`, `samples[0]: invalid uuid "x"`},
			{`{"samples":[null]}`, `samples[0]: invalid uuid ""`},
			{sample(`,"start":"2026-10-25T08:00:00Z","end":"2026-10-25T08:00:00Z"`), "samples[0]: sample " + u + ": missing type"},
			{sample(`,"type":"Steps"`), "samples[0]: sample " + u + `: invalid type "Steps"`},
			{sample(`,"type":"HK../x"`), "samples[0]: sample " + u + `: invalid type "HK../x"`},
			{sample(`,"type":"HKQuantityTypeIdentifierStepCount","start":"yesterday"`), "samples[0]: sample " + u + `: bad start: parsing time "yesterday"…`},
			{sample(`,"type":"HKQuantityTypeIdentifierStepCount","start":"2026-02-30T00:00:00Z"`), "samples[0]: sample " + u + `: bad start: …`},
			{sample(`,"type":"HKQuantityTypeIdentifierStepCount","start":"2026-10-25 08:00:00Z"`), "samples[0]: sample " + u + `: bad start: …`},
			{sample(`,"type":"HKQuantityTypeIdentifierStepCount","start":"2026-10-25T08:00:00Z","end":"2026-10-25T08:00:00+25:00"`), "samples[0]: sample " + u + `: bad end: …`},
			{sample(`,"type":"HKQuantityTypeIdentifierStepCount","start":"2026-10-25T08:00:00Z"`), "samples[0]: sample " + u + `: bad end: …`},
			{sample(`,"type":"HKQuantityTypeIdentifierStepCount","start":"2026-10-25T08:00:00.000000002Z","end":"2026-10-25T08:00:00.000000001Z"`), "samples[0]: sample " + u + ": end before start"},
			{sample(`,"type":"HKQuantityTypeIdentifierStepCount","start":"2026-10-25T08:00:00-04:00","end":"2026-10-25T08:00:00Z"`), "samples[0]: sample " + u + ": end before start"},
			{sample(ok + `,"value":"12"`), "samples[0]: …"},
			{sample(ok + `,"workout":{"activity_type":1.5}`), "samples[0]: …"},
			{sample(ok + `,"source":"iPhone"`), "samples[0]: …"},
			{`{"samples":[` + strings.TrimSuffix(strings.TrimPrefix(sample(ok), `{"samples":[`), "]}") + `,{"uuid":"nope"}]}`, `samples[1]: invalid uuid "nope"`},
			{`{"records":[{"kind":"Bad","key":"x"}]}`, `records[0]: invalid kind "Bad"`},
			{`{"records":[{"key":"x"}]}`, `records[0]: invalid kind ""`},
			{`{"records":[{"kind":"activity_summary","key":""}]}`, "records[0]: record activity_summary: key must be 1 to 512 bytes"},
			{`{"records":[{"kind":"activity_summary","key":"` + strings.Repeat("é", 257) + `"}]}`, "records[0]: record activity_summary: key must be 1 to 512 bytes"},
			{`{"records":[{"kind":"activity_summary","key":"d","start":"25/10/2026"}]}`, `records[0]: record activity_summary/d: bad start: parsing time "25/10/2026"…`},
			{`{"records":[{"kind":"route_points","key":"r","points":{}}]}`, "records[0]: …"},
			{`{"deletions":[{"uuid":"nope"}]}`, `deletions[0]: invalid uuid "nope"`},
			{`{"records":[` + parts.String() + `]}`, fmt.Sprintf("batch spans %d (type, month) pairs, more than %d", MaxBatchPartitions+1, MaxBatchPartitions)},
		} {
			r := c.do("POST", "/v1/samples", c.token, tc.body)
			got := r.errorText(t)
			want, prefix := strings.CutSuffix(tc.want, "…")
			if r.code != 400 || (prefix && !strings.HasPrefix(got, want)) || (!prefix && got != want) {
				t.Errorf("POST /v1/samples %.80s: %d %q, want 400 %q", tc.body, r.code, got, tc.want)
			}
		}
	})
	t.Run("heartbeat", func(t *testing.T) {
		r := c.do("POST", "/v1/heartbeat", c.token, `{"trigger":"refresh","app_state":"background","protected_data":true,"outcome":"ok",`+
			`"uploaded":3,"seconds":1.5,"background_time_left":-1,"process_age":40,"app_version":"1.0.0 (12)",`+
			`"wakes":{"refresh":{"count":2,"last":"2026-10-25T10:00:00Z"}},"battery":"charging","low_power_mode":false,"extra":[1]}`)
		c.checkHandoff(t, r)
		for _, body := range []string{``, `{"uploaded":"3"}`, `{"uploaded":1.5}`, `{"protected_data":"yes"}`, `{"wakes":{"refresh":{"last":"today"}}}`, `[]`} {
			if r := c.do("POST", "/v1/heartbeat", c.token, body); r.code != 400 || !strings.HasPrefix(r.errorText(t), "invalid JSON: ") {
				t.Errorf("heartbeat %q: %d %s", body, r.code, r.body)
			}
		}
		c.checkHandoff(t, c.do("POST", "/v1/heartbeat", c.token, `null`))
	})
	t.Run("erase", func(t *testing.T) {
		for _, q := range []string{"", "?confirm=erase"} {
			r := c.do("DELETE", "/v1/data"+q, c.token, "")
			if m := r.json(t); r.code != 200 || m["erased"] != 0.0 || m["device"] != c.deviceID || len(m) != 2 {
				t.Errorf("DELETE /v1/data%s: %d %s", q, r.code, r.body)
			}
		}
		if r := c.do("DELETE", "/v1/data?confirm=yes", c.token, ""); r.code != 400 || r.errorText(t) != `confirm must be "erase"; nothing was erased` {
			t.Errorf("confirm=yes: %d %s", r.code, r.body)
		}
		if r := c.do("GET", "/v1/status", c.token, ""); r.code != 200 {
			t.Errorf("the token after an erase: %d", r.code)
		}
	})
	t.Run("admin only", func(t *testing.T) {
		for _, rq := range [][2]string{{"POST", "/v1/pairing-codes"}, {"GET", "/v1/devices"}, {"DELETE", "/v1/devices/" + c.deviceID}, {"POST", "/v1/tokens"}} {
			if r := c.do(rq[0], rq[1], c.token, "{}"); r.code != 401 {
				t.Errorf("%s %s with a demo token: %d %s", rq[0], rq[1], r.code, r.body)
			}
		}
	})
	t.Run("routes", func(t *testing.T) {
		if r := c.do("GET", "/", "", ""); r.code != 303 || r.header.Get("Location") != "/setup" {
			t.Errorf("GET /: %d %v", r.code, r.header)
		}
		if r := c.do("GET", "/nope", "", ""); r.code != 404 {
			t.Errorf("GET /nope: %d", r.code)
		}
		for path, allow := range map[string]string{"/v1/status": "GET, HEAD", "/v1/samples": "POST", "/v1/data": "DELETE", "/setup": "GET, HEAD, POST"} {
			if r := c.do("PUT", path, "", ""); r.code != 405 || r.header.Get("Allow") != allow {
				t.Errorf("PUT %s: %d Allow %q, want %q", path, r.code, r.header.Get("Allow"), allow)
			}
		}
	})
	t.Run("setup page", func(t *testing.T) {
		r := c.do("GET", "/setup?code="+c.code, "", "")
		link := strings.ReplaceAll(PairLink(c.public, c.code), "&", "&amp;")
		for _, want := range []string{"demo server", c.code, c.public, "<svg", "Open in Health Relay"} {
			if !strings.Contains(r.body, want) {
				t.Errorf("setup page with the code: %d, no %q", r.code, want)
			}
		}
		// html/template writes + as &#43; in attributes.
		if body := strings.ReplaceAll(r.body, "&#43;", "+"); !strings.Contains(body, link) {
			t.Errorf("setup page: no link %s", link)
		}
		if r.code != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/html") ||
			!strings.Contains(r.header.Get("Content-Security-Policy"), "frame-ancestors 'none'") || r.header.Get("Cache-Control") != "no-store" {
			t.Errorf("setup page: %d %v", r.code, r.header)
		}
		if r := c.do("GET", "/setup?code=AAAAAAAAAAAAAAAAAAAA", "", ""); r.code != 410 || !strings.Contains(r.body, "expired or was already used") {
			t.Errorf("setup page with another code: %d", r.code)
		}
		if r := c.do("GET", "/setup", "", ""); r.code != 200 || !strings.Contains(r.body, "Admin token") {
			t.Errorf("setup page: %d", r.code)
		}
		for code, state := range map[string]string{c.code: "pending", "AAAAAAAAAAAAAAAAAAAA": "invalid"} {
			if r := c.do("GET", "/setup/status?code="+code, "", ""); r.code != 200 || r.json(t)["state"] != state {
				t.Errorf("setup status: %d %s, want %s", r.code, r.body, state)
			}
		}
	})
	if c.maxBody > 0 {
		t.Run("body too large", func(t *testing.T) {
			r := c.do("POST", "/v1/samples", c.token, `{"device":"`+strings.Repeat("x", c.maxBody)+`","samples":[]}`)
			want := fmt.Sprintf("body too large: this server accepts at most %d bytes (-max-body-bytes)", c.maxBody)
			if r.code != 413 || r.errorText(t) != want {
				t.Errorf("a body over the limit: %d %s", r.code, r.body)
			}
		})
	}
	if c.quota > 0 {
		// Last: it uses up the quota.
		t.Run("quota", func(t *testing.T) {
			body := `{"device":"` + strings.Repeat("x", c.maxBody*3/4) + `","samples":[]}`
			for i := 0; i < 4*c.quota/c.maxBody+4; i++ {
				r := c.do("POST", "/v1/samples", c.token, body)
				switch r.code {
				case 200, 413:
					continue
				case 429:
					if !strings.HasPrefix(r.errorText(t), fmt.Sprintf("this demo server accepts %d MB of uploads per 1h0m0s: try again in ", c.quota>>20)) {
						t.Errorf("over the quota: %s", r.body)
					}
					if s, err := strconv.Atoi(r.header.Get("Retry-After")); err != nil || s < 1 || s > 3601 {
						t.Errorf("Retry-After %q", r.header.Get("Retry-After"))
					}
					return
				default:
					t.Fatalf("upload %d: %d %s", i, r.code, r.body)
				}
			}
			t.Errorf("no 429 after %d bytes", 4*c.quota)
		})
	}
}

// checkDevice checks a demo device and returns its ID.
func (c *demoContract) checkDevice(t *testing.T, dev map[string]any) string {
	t.Helper()
	id, _ := dev["id"].(string)
	created, _ := dev["created_at"].(string)
	if _, err := time.Parse(time.RFC3339, created); err != nil || strings.Contains(created, ".") ||
		!regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(id) || dev["name"] != "Demo iPhone" || dev["scope"] != "upload" ||
		dev["last_seen_at"] != "0001-01-01T00:00:00Z" || len(dev) != 5 {
		t.Errorf("demo device: %v", dev)
	}
	return id
}

func (c *demoContract) checkStatus(t *testing.T, m map[string]any) {
	t.Helper()
	want := []string{"device", "preferred_url", "records", "server_id", "version", "wire_version"}
	if v, _ := m["version"].(string); v == "" || m["wire_version"] != 2.0 || m["server_id"] != c.id ||
		m["preferred_url"] != c.public || !reflect.DeepEqual(keys(m), want) {
		t.Errorf("status: %v", m)
	}
	if n, ok := m["records"].(float64); !ok || n < 0 || n != float64(int(n)) {
		t.Errorf("records: %v", m["records"])
	}
	dev, _ := m["device"].(map[string]any)
	if id := c.checkDevice(t, dev); c.deviceID != "" && id != c.deviceID {
		t.Errorf("status names device %s, the token's is %s", id, c.deviceID)
	}
}

func (c *demoContract) checkHandoff(t *testing.T, r reply) {
	t.Helper()
	if m := r.json(t); r.code != 200 || m["server_id"] != c.id || m["preferred_url"] != c.public || len(m) != 2 {
		t.Errorf("heartbeat: %d %s", r.code, r.body)
	}
}

func (c *demoContract) records(t *testing.T) int {
	t.Helper()
	n, _ := c.do("GET", "/v1/status", c.token, "").json(t)["records"].(float64)
	return int(n)
}
