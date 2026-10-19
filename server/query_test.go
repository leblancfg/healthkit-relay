package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The query API on a tiny synthetic archive. Needs the duckdb command (or $DUCKDB); the
// tests that run queries are skipped without it.

func duckdbPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("DUCKDB"); p != "" {
		return p
	}
	p, err := exec.LookPath("duckdb")
	if err != nil {
		t.Skip("duckdb not installed")
	}
	return p
}

var (
	watchSrc  = `"source":{"name":"Watch","bundle_id":"com.apple.health.W","product_type":"Watch6,14"},"device":{"name":"Apple Watch","model":"Watch"}`
	phoneSrc  = `"source":{"name":"iPhone","bundle_id":"com.apple.health.P","product_type":"iPhone17,5"},"device":{"name":"iPhone","model":"iPhone"}`
	otherSrc  = `"source":{"name":"Strava","bundle_id":"com.strava"}`
	fixtureN  = 0
	fixtureTZ = "America/Toronto" // UTC-4 in October 2026
)

func fixtureSample(typ, kind, start, end string, value float64, unit, src string) string {
	fixtureN++
	u := ""
	if unit != "" {
		u = fmt.Sprintf(`"unit":%q,`, unit)
	}
	return fmt.Sprintf(`{"uuid":"%08x-0000-4000-8000-000000000000","type":%q,"kind":%q,"start":%q,"end":%q,"value":%v,%s%s}`,
		fixtureN, typ, kind, start, end, value, u, src)
}

const (
	stepsType = "HKQuantityTypeIdentifierStepCount"
	hrType    = "HKQuantityTypeIdentifierHeartRate"
	sleepType = "HKCategoryTypeIdentifierSleepAnalysis"
)

type queryFixture struct {
	url     string
	srv     *Server
	read    string // a read token
	upload  string // a paired device's token
	deleted string // uuid of a deleted step sample
}

// newQueryFixture builds an archive with steps, heart rate and sleep from a watch, a phone
// and another app, flushed to Parquet, and serves it with the query API on.
func newQueryFixture(t *testing.T) *queryFixture {
	t.Helper()
	duckdb := duckdbPath(t)
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	devices, _ := OpenDevices(filepath.Join(dir, "devices.json"))
	db, err := NewDuckDB(duckdb, dir, 30*time.Second, 1000, "256MB", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 12, 16, 0, 0, 0, time.UTC)
	s := &Server{Archive: store, AdminKey: NewAdminKey(testToken), MaxBodyBytes: 1 << 20, Devices: devices, Codes: NewCodes(),
		Query: &queryAPI{db: db, now: func() time.Time { return now }}}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	f := &queryFixture{url: ts.URL, srv: s}
	f.read, _, _ = devices.AddScoped("grafana", "", ScopeRead)
	f.upload, _, _ = devices.Add("iPhone", "")

	q := func(typ, start, end string, v float64, unit, src string) string {
		kind := "quantity"
		if strings.HasPrefix(typ, "HKCategory") {
			kind = "category"
		}
		return fixtureSample(typ, kind, start, end, v, unit, src)
	}
	samples := []string{
		// Steps on Oct 10 (local): the watch counts 600 in 10 minutes; the phone 1000 in 20
		// minutes that cover those 10, so half of it is the watch's time: 500 more.
		q(stepsType, "2026-10-10T14:00:00Z", "2026-10-10T14:10:00Z", 600, "count", watchSrc),
		q(stepsType, "2026-10-10T14:00:00Z", "2026-10-10T14:20:00Z", 1000, "count", phoneSrc),
		// Across local midnight: 60 on each day.
		q(stepsType, "2026-10-11T03:30:00Z", "2026-10-11T04:30:00Z", 120, "count", phoneSrc),
		// Another app, overlapping nothing: counts in full.
		q(stepsType, "2026-10-11T15:00:00Z", "2026-10-11T15:30:00Z", 300, "count", otherSrc),
		// Heart rate: 60, 70, 80 on Oct 10, 50 on Oct 11.
		q(hrType, "2026-10-10T12:00:00Z", "2026-10-10T12:00:00Z", 60, "count/min", watchSrc),
		q(hrType, "2026-10-10T13:00:00Z", "2026-10-10T13:00:00Z", 70, "count/min", watchSrc),
		q(hrType, "2026-10-10T14:00:00Z", "2026-10-10T14:00:00Z", 80, "count/min", phoneSrc),
		q(hrType, "2026-10-11T14:00:00Z", "2026-10-11T14:00:00Z", 50, "count/min", watchSrc),
		// Sleep: core 23:00-01:00 local and deep 01:00-02:00 from the watch; in bed from the
		// phone 22:30-06:00; a second app's core 00:00-01:00, which the watch's covers.
		q(sleepType, "2026-10-11T03:00:00Z", "2026-10-11T05:00:00Z", 3, "", watchSrc),
		q(sleepType, "2026-10-11T05:00:00Z", "2026-10-11T06:00:00Z", 4, "", watchSrc),
		q(sleepType, "2026-10-11T02:30:00Z", "2026-10-11T10:00:00Z", 0, "", phoneSrc),
		q(sleepType, "2026-10-11T04:00:00Z", "2026-10-11T05:00:00Z", 3, "", otherSrc),
	}
	deleted := q(stepsType, "2026-10-10T18:00:00Z", "2026-10-10T18:05:00Z", 5000, "count", watchSrc)
	var del struct{ UUID string }
	json.Unmarshal([]byte(deleted), &del)
	f.deleted = strings.ToUpper(del.UUID)
	mustPost(t, ts.URL, testToken, `{"version":2,"samples":[`+strings.Join(append(samples, deleted), ",")+`],"records":[],"deletions":[]}`)
	mustPost(t, ts.URL, testToken, `{"version":2,"samples":[],"records":[],"deletions":[{"uuid":"`+del.UUID+`"}]}`)
	if _, err := store.Flush(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *queryFixture) get(t *testing.T, token, path string, v any) int {
	t.Helper()
	if a, ok := v.(*aggResp); ok {
		*a = aggResp{} // omitted fields must not keep the previous response's values
	}
	resp, body := call(t, http.MethodGet, f.url+path, token, nil)
	if v != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, v); err != nil {
			t.Fatalf("%s: %v\n%s", path, err, body)
		}
	}
	if resp.StatusCode != http.StatusOK && v != nil {
		t.Logf("%s: %d %s", path, resp.StatusCode, body)
	}
	return resp.StatusCode
}

type aggResp struct {
	Type, Kind, Interval, TZ, Dedup, Start, End string
	Buckets                                     []struct {
		Start, End, Date, Unit string
		Sum, Avg, Min, Max     *float64
		Samples                int64
		Sources                []string
		Values                 []CategoryBucket
	}
}

func near(a *float64, b float64) bool { return a != nil && math.Abs(*a-b) < 1e-6 }

func TestQueryAggregates(t *testing.T) {
	f := newQueryFixture(t)
	base := "/v1/types/" + stepsType + "/aggregates?tz=" + url.QueryEscape(fixtureTZ) + "&start=2026-10-10&end=2026-10-12"

	var a aggResp
	if code := f.get(t, f.read, base, &a); code != 200 {
		t.Fatal(code)
	}
	if len(a.Buckets) != 2 || a.Buckets[0].Date != "2026-10-10" || a.Buckets[0].Start != "2026-10-10T00:00:00-04:00" ||
		a.Dedup != DedupPriority || a.Interval != "day" {
		t.Fatalf("%+v", a)
	}
	if b := a.Buckets[0]; !near(b.Sum, 1160) || b.Unit != "count" || b.Samples != 3 || strings.Join(b.Sources, ",") != "Watch,iPhone" {
		t.Errorf("Oct 10: %+v sum %v", b, *b.Sum)
	}
	if b := a.Buckets[1]; !near(b.Sum, 360) || b.Samples != 2 {
		t.Errorf("Oct 11: %+v sum %v", b, *b.Sum)
	}
	// Without dedup everything adds up; the deleted sample never counts.
	if f.get(t, f.read, base+"&dedup=none", &a); !near(a.Buckets[0].Sum, 1660) || !near(a.Buckets[1].Sum, 360) {
		t.Errorf("dedup=none: %v %v", *a.Buckets[0].Sum, *a.Buckets[1].Sum)
	}
	// Hourly in UTC: the overlap is in the 14:00 hour; an empty hour is 0.
	f.get(t, f.read, "/v1/types/"+stepsType+"/aggregates?interval=hour&start=2026-10-10T13:00:00Z&end=2026-10-10T15:00:00Z", &a)
	if len(a.Buckets) != 2 || !near(a.Buckets[0].Sum, 0) || a.Buckets[0].Samples != 0 || !near(a.Buckets[1].Sum, 1100) || a.TZ != "UTC" {
		t.Errorf("hourly: %+v", a)
	}

	// Discrete: average, min, max per day; an empty day has none.
	f.get(t, f.read, "/v1/types/"+hrType+"/aggregates?tz="+fixtureTZ+"&start=2026-10-09&end=2026-10-12", &a)
	if len(a.Buckets) != 3 || a.Buckets[0].Avg != nil || a.Buckets[0].Samples != 0 ||
		!near(a.Buckets[1].Avg, 70) || !near(a.Buckets[1].Min, 60) || !near(a.Buckets[1].Max, 80) || a.Buckets[1].Samples != 3 ||
		!near(a.Buckets[2].Avg, 50) || a.Dedup != "" {
		t.Errorf("heart rate: %+v", a)
	}

	// Categories: seconds per value inside each day; the second app's core sleep is covered.
	f.get(t, f.read, "/v1/types/"+sleepType+"/aggregates?tz="+fixtureTZ+"&start=2026-10-10&end=2026-10-12", &a)
	got := map[string]map[string]float64{}
	for _, b := range a.Buckets {
		got[b.Date] = map[string]float64{}
		for _, v := range b.Values {
			got[b.Date][v.Label] = v.Seconds
			if v.Label == "asleepCore" && (v.Samples != 1 || strings.Join(v.Sources, ",") != "Watch") {
				t.Errorf("%s core: %+v", b.Date, v)
			}
		}
	}
	want := map[string]map[string]float64{
		"2026-10-10": {"asleepCore": 3600, "inBed": 5400},
		"2026-10-11": {"asleepCore": 3600, "asleepDeep": 3600, "inBed": 21600},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("sleep:\n got %v\nwant %v", got, want)
	}
	f.get(t, f.read, "/v1/types/"+sleepType+"/aggregates?tz="+fixtureTZ+"&start=2026-10-11&end=2026-10-12&dedup=none", &a)
	for _, v := range a.Buckets[0].Values {
		if v.Label == "asleepCore" && (v.Seconds != 7200 || v.Samples != 2) {
			t.Errorf("dedup=none core: %+v", v)
		}
	}

	// Defaults: the last 7 local days, today included.
	f.get(t, f.read, "/v1/types/"+stepsType+"/aggregates?tz="+fixtureTZ, &a)
	if len(a.Buckets) != 7 || a.Buckets[6].Date != "2026-10-12" || a.Buckets[0].Date != "2026-10-06" {
		t.Errorf("default range: %d buckets, %s..%s", len(a.Buckets), a.Buckets[0].Date, a.Buckets[len(a.Buckets)-1].Date)
	}
}

func TestQueryBucketsAcrossDST(t *testing.T) {
	loc, _ := time.LoadLocation(fixtureTZ)
	bs, err := buckets(time.Date(2026, 11, 1, 0, 0, 0, 0, loc), time.Date(2026, 11, 2, 0, 0, 0, 0, loc), "day", loc)
	if err != nil || len(bs) != 1 || bs[0].end.Sub(bs[0].start) != 25*time.Hour {
		t.Errorf("fall back day: %v %v", bs, err)
	}
	bs, _ = buckets(time.Date(2026, 11, 1, 0, 0, 0, 0, loc), time.Date(2026, 11, 1, 3, 0, 0, 0, loc), "hour", loc)
	if len(bs) != 4 { // 00, 01 (EDT), 01 (EST), 02
		t.Errorf("fall back hours: %d", len(bs))
	}
	kolkata, _ := time.LoadLocation("Asia/Kolkata")
	bs, _ = buckets(time.Date(2026, 10, 10, 5, 45, 0, 0, kolkata), time.Date(2026, 10, 10, 7, 0, 0, 0, kolkata), "hour", kolkata)
	if len(bs) != 2 || bs[0].start.Minute() != 0 || bs[0].start.UTC().Minute() != 30 {
		t.Errorf("half-hour zone: %v", bs)
	}
	if _, err := buckets(time.Date(2025, 1, 1, 0, 0, 0, 0, loc), time.Date(2026, 6, 1, 0, 0, 0, 0, loc), "day", loc); err == nil {
		t.Error("17 months of days accepted")
	}
}

func TestQuerySamples(t *testing.T) {
	f := newQueryFixture(t)
	type page struct {
		Count      int
		Samples    []SampleOut
		NextCursor string `json:"next_cursor"`
		TZ, Order  string
	}
	base := "/v1/types/" + stepsType + "/samples?start=2026-10-01&end=2026-10-12&limit=2"
	var p page
	if code := f.get(t, f.read, base, &p); code != 200 || p.Count != 2 || p.NextCursor == "" {
		t.Fatalf("%d %+v", code, p)
	}
	seen := map[string]bool{}
	starts := []string{}
	for range 5 {
		for _, s := range p.Samples {
			seen[s.UUID] = true
			starts = append(starts, s.Start)
		}
		if p.NextCursor == "" {
			break
		}
		next := page{}
		f.get(t, f.read, base+"&cursor="+p.NextCursor, &next)
		p = next
	}
	if len(seen) != 4 || seen[f.deleted] || starts[0] != "2026-10-10T14:00:00Z" || starts[3] != "2026-10-11T15:00:00Z" {
		t.Errorf("pages: %d samples %v, starts %v", len(seen), seen, starts)
	}
	// Newest first, in a time zone, with the raw sample.
	f.get(t, f.read, "/v1/types/"+stepsType+"/samples?start=2026-10-01&end=2026-10-12&order=desc&limit=1&raw=true&tz="+fixtureTZ, &p)
	s := p.Samples[0]
	if s.Start != "2026-10-11T11:00:00-04:00" || s.Source.Name != "Strava" || !strings.Contains(string(s.Raw), `"bundle_id":"com.strava"`) || p.Order != "desc" {
		t.Errorf("desc: %+v %s", s, s.Raw)
	}
	// A cursor works only with its order.
	if code := f.get(t, f.read, "/v1/types/"+stepsType+"/samples?start=2026-10-01&end=2026-10-12&cursor="+p.NextCursor, nil); code != 400 {
		t.Errorf("cursor of the other order: %d", code)
	}
	// Categories carry their label; the watch's product type is there.
	f.get(t, f.read, "/v1/types/"+sleepType+"/samples?start=2026-10-11T05:00:00Z&end=2026-10-11T05:00:01Z", &p)
	if len(p.Samples) != 1 || p.Samples[0].ValueLabel != "asleepDeep" || p.Samples[0].Source.Product != "Watch6,14" || p.Samples[0].Source.Device != "Apple Watch" {
		t.Errorf("sleep sample: %+v", p.Samples)
	}
}

func TestQueryCatalog(t *testing.T) {
	f := newQueryFixture(t)
	var list struct{ Types []TypeInfo }
	if code := f.get(t, f.read, "/v1/types?present=true", &list); code != 200 || len(list.Types) != 3 {
		t.Fatalf("%d %+v", code, list.Types)
	}
	byID := map[string]TypeInfo{}
	for _, ti := range list.Types {
		byID[ti.Identifier] = ti
	}
	steps := byID[stepsType]
	if steps.Data.Samples != 4 || steps.Data.First == nil || !steps.Data.First.Equal(time.Date(2026, 10, 10, 14, 0, 0, 0, time.UTC)) || !steps.Aggregates {
		t.Errorf("steps: %+v", steps)
	}
	f.get(t, f.read, "/v1/types?kind=category", &list)
	for _, ti := range list.Types {
		if ti.Kind != "category" {
			t.Errorf("kind=category gave %s", ti.Identifier)
		}
	}
	if len(list.Types) < 50 {
		t.Errorf("only %d category types", len(list.Types))
	}
	var one TypeInfo
	if f.get(t, f.read, "/v1/types/"+sleepType, &one); one.Data.Samples != 4 || len(one.Values) != 6 {
		t.Errorf("sleep: %+v", one)
	}
	var kinds struct {
		Kinds []struct {
			Name  string
			Types int
		}
	}
	if f.get(t, f.read, "/v1/kinds", &kinds); len(kinds.Kinds) != len(Kinds) || kinds.Kinds[0].Name != "quantity" || kinds.Kinds[0].Types < 100 {
		t.Errorf("kinds: %+v", kinds)
	}
}

// Anything not in the catalog, or not a valid value, is refused before any SQL is built.
func TestQueryRejectsBadParameters(t *testing.T) {
	f := newQueryFixture(t)
	for path, want := range map[string]int{
		"/v1/types/HKQuantityTypeIdentifierStepCount'%20OR%201=1--/aggregates":                 404,
		"/v1/types/HKQuantityTypeIdentifierNope/samples":                                       404,
		"/v1/types/HKWorkoutTypeIdentifier/aggregates":                                         400,
		"/v1/types/" + stepsType + "/aggregates?tz=Mars/Olympus":                               400,
		"/v1/types/" + stepsType + "/aggregates?tz=../../etc/passwd":                           400,
		"/v1/types/" + stepsType + "/aggregates?tz=Local":                                      400,
		"/v1/types/" + stepsType + "/aggregates?interval=week":                                 400,
		"/v1/types/" + stepsType + "/aggregates?dedup=max":                                     400,
		"/v1/types/" + stepsType + "/aggregates?start=yesterday":                               400,
		"/v1/types/" + stepsType + "/aggregates?start=2026-10-12&end=2026-10-11":               400,
		"/v1/types/" + stepsType + "/aggregates?start=2020-01-01&end=2026-01-01":               400,
		"/v1/types/" + stepsType + "/aggregates?interval=hour&start=2026-01-01&end=2026-03-01": 400,
		"/v1/types/" + stepsType + "/samples?limit=0":                                          400,
		"/v1/types/" + stepsType + "/samples?limit=100000":                                     400,
		"/v1/types/" + stepsType + "/samples?order=random":                                     400,
		"/v1/types/" + stepsType + "/samples?cursor=bm9wZQ":                                    400,
		"/v1/types/" + stepsType + "/samples?raw=yes":                                          400,
		"/v1/types?kind=nope":     400,
		"/v1/types?present=maybe": 400,
	} {
		resp, body := call(t, http.MethodGet, f.url+path, f.read, nil)
		if resp.StatusCode != want || !strings.Contains(string(body), `"error"`) {
			t.Errorf("%s: %d %s, want %d", path, resp.StatusCode, body, want)
		}
	}
}

// Read tokens and the server token may query; upload tokens, no token or a wrong one may not.
func TestQueryScopes(t *testing.T) {
	f := newQueryFixture(t)
	path := "/v1/types/" + stepsType + "/aggregates?start=2026-10-10&end=2026-10-11"
	for _, c := range []struct {
		name, token string
		want        int
	}{
		{"read", f.read, 200},
		{"admin", testToken, 200},
		{"upload", f.upload, 403},
		{"none", "", 401},
		{"wrong", "not-a-token-at-all-0123", 401},
	} {
		if got := f.get(t, c.token, path, nil); got != c.want {
			t.Errorf("%s token: %d, want %d", c.name, got, c.want)
		}
	}
	// The docs and the document need no token.
	for _, p := range []string{"/openapi.json", "/docs", "/docs/swagger-ui-bundle.js", "/docs/init.js"} {
		if got := f.get(t, "", p, nil); got != 200 {
			t.Errorf("%s: %d", p, got)
		}
	}
	for _, p := range []string{"/docs/SHA256SUMS", "/docs/index.html", "/docs/..%2fopenapi.go", "/docs/LICENSE"} {
		if got := f.get(t, "", p, nil); got != 404 {
			t.Errorf("%s: %d, want 404", p, got)
		}
	}
}

// Without -query-duckdb nothing of the query API is served.
func TestQueryOffByDefault(t *testing.T) {
	ts, _ := newTestServer(t, t.TempDir())
	for _, p := range []string{"/v1/types", "/v1/kinds", "/openapi.json", "/docs", "/v1/types/" + stepsType + "/aggregates"} {
		if resp, _ := call(t, http.MethodGet, ts.URL+p, testToken, nil); resp.StatusCode != 404 {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
}

// A fake duckdb that sleeps: the query is killed at the deadline and the client gets 504;
// while it runs, a second query waits for the only slot and gets 503.
func TestQueryTimeout(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "duckdb")
	sh, err1 := exec.LookPath("sh")
	sleep, err2 := exec.LookPath("sleep")
	if err1 != nil || err2 != nil {
		t.Skip("no sh or sleep")
	}
	// duckdb runs with an empty environment: no PATH.
	os.WriteFile(fake, []byte("#!"+sh+"\nexec "+sleep+" 30\n"), 0o755)
	db, err := NewDuckDB(fake, dir, time.Second, 100, "64MB", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{AdminKey: NewAdminKey(testToken), Query: &queryAPI{db: db, now: time.Now}}
	path := "/v1/types/" + stepsType + "/aggregates"
	start := time.Now()
	codes := make(chan int, 2)
	for range 2 {
		go func() {
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			codes <- w.Code
		}()
	}
	got := []int{<-codes, <-codes}
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("took %s: the query was not killed", el)
	}
	if !(got[0] == 504 && got[1] == 503 || got[0] == 503 && got[1] == 504) {
		t.Errorf("codes %v, want a 504 and a 503", got)
	}
}

// DuckDB refuses anything but reading the archive's Parquet files.
func TestQueryLockdown(t *testing.T) {
	duckdb := duckdbPath(t)
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	db, err := NewDuckDB(duckdb, dir, 10*time.Second, 10, "64MB", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	ok := func(sql string) error {
		return db.Query(context.Background(), sql, func(json.RawMessage) error { return nil })
	}
	if err := ok("SELECT count(*) FROM samples"); err != nil {
		t.Fatalf("the views: %v", err)
	}
	for _, sql := range []string{
		"SELECT * FROM read_csv('/etc/passwd')",
		"SELECT * FROM read_blob('" + filepath.Join(dir, "parquet", "..", "staging.db") + "')",
		"SELECT * FROM glob('/*')",
		"SELECT getenv('HOME')",
		"SET enable_external_access = true",
		"SET memory_limit = '64GB'",
		"ATTACH '" + filepath.Join(dir, "x.db") + "'",
		"INSTALL httpfs",
		"COPY (SELECT 1) TO '" + filepath.Join(dir, "out.csv") + "'",
	} {
		if err := ok(sql); err == nil {
			t.Errorf("allowed: %s", sql)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "out.csv")); err == nil {
		t.Error("COPY wrote a file")
	}
}
