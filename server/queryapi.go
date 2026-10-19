package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // tz names are checked the same way everywhere, with or without a system tz database
)

// The query API: typed, read-only endpoints under /v1 over the Parquet archive, described by
// /openapi.json (openapi.go). Read tokens and the admin token may call it; upload tokens may
// not. Rows still in staging (received in the last hour, before the hourly flush) are not
// visible to it.

// Limits of a request.
const (
	defaultSampleLimit = 100
	maxDayBuckets      = 366
	maxHourBuckets     = 31 * 24
	statsTTL           = 5 * time.Minute
	// Samples that start this long before a range are read too: a sample overlapping the
	// range start counts for its part inside, and covers lower-priority sources there.
	overlapMargin = 24 * time.Hour
)

// Dedup rules for aggregates of cumulative quantities and categories.
const (
	DedupPriority = "priority"
	DedupNone     = "none"
)

// queryAPI holds the query API's state; nil on a Server without -query-duckdb.
type queryAPI struct {
	db  *DuckDB
	now func() time.Time

	mu      sync.Mutex
	stats   map[string]TypeStats
	statsAt time.Time
}

// TypeStats is what the archive holds of one type.
type TypeStats struct {
	Samples int64      `json:"samples"`
	First   *time.Time `json:"first_start,omitempty"`
	Last    *time.Time `json:"last_start,omitempty"`
}

// TypeInfo is a catalog entry with what the archive holds of it.
type TypeInfo struct {
	*CatalogType
	Aggregates bool      `json:"aggregates"`
	Data       TypeStats `json:"data"`
}

// apiError is an error with the status to answer.
type apiError struct {
	code int
	msg  string
}

func (e *apiError) Error() string { return e.msg }

func badParam(format string, a ...any) error {
	return &apiError{http.StatusBadRequest, fmt.Sprintf(format, a...)}
}

func (s *Server) queryError(w http.ResponseWriter, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
		httpError(w, ae.code, ae.msg)
	case errors.Is(err, errQueryTimeout):
		s.count("query_timeout")
		httpError(w, http.StatusGatewayTimeout, fmt.Sprintf("the query took longer than %s: ask for a shorter range", s.Query.db.Timeout))
	case errors.Is(err, errQueryBusy):
		s.count("query_busy")
		w.Header().Set("Retry-After", "5")
		httpError(w, http.StatusServiceUnavailable, "too many queries at once: retry shortly")
	case errors.Is(err, errQueryTooBig):
		httpError(w, http.StatusUnprocessableEntity, "the result is too large: lower limit, ask for a shorter range, or leave out raw")
	case errors.Is(err, context.Canceled):
		// the client went away
	default:
		s.count("query_error")
		log.Printf("query: %v", err)
		httpError(w, http.StatusInternalServerError, "the query failed")
	}
}

// --- parameters

var tzRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+){0,2}$`)

func parseTZ(v string) (*time.Location, error) {
	if v == "" || v == "UTC" {
		return time.UTC, nil
	}
	if !tzRE.MatchString(v) || v == "Local" {
		return nil, badParam("tz %q is not a time zone name such as America/Toronto", v)
	}
	loc, err := time.LoadLocation(v)
	if err != nil {
		return nil, badParam("tz %q is not in the time zone database", v)
	}
	return loc, nil
}

// parseInstant reads an RFC 3339 timestamp, or a date (YYYY-MM-DD: its midnight in loc).
func parseInstant(name, v string, loc *time.Location) (time.Time, bool, error) {
	if v == "" {
		return time.Time{}, false, nil
	}
	if len(v) == len("2006-01-02") {
		d, err := time.ParseInLocation("2006-01-02", v, loc)
		if err != nil {
			return time.Time{}, false, badParam("%s %q: want a date (2026-10-11) or an RFC 3339 timestamp (2026-10-11T08:00:00-04:00)", name, v)
		}
		return d, true, nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, false, badParam("%s %q: want an RFC 3339 timestamp with an offset (2026-10-11T08:00:00Z) or a date (2026-10-11)", name, v)
	}
	if t.Year() < 1970 || t.Year() > 9999 {
		return time.Time{}, false, badParam("%s %q is out of range", name, v)
	}
	return t, true, nil
}

func parseBool(name, v string) (bool, error) {
	switch v {
	case "", "false", "0":
		return false, nil
	case "true", "1":
		return true, nil
	}
	return false, badParam("%s must be true or false", name)
}

// sqlTime is an instant as a DuckDB TIMESTAMPTZ literal.
func sqlTime(t time.Time) string {
	return "TIMESTAMPTZ '" + t.UTC().Format("2006-01-02 15:04:05.000000") + "+00'"
}

// partitionFilter selects the type's partitions, and the months (of the sample start, UTC)
// from lo to hi, so DuckDB reads only those files.
func partitionFilter(t *CatalogType, lo, hi time.Time) string {
	return fmt.Sprintf("type = %s AND month BETWEEN %s AND %s AND start >= %s AND start < %s",
		sqlString(t.Identifier), sqlString(lo.UTC().Format("2006-01")), sqlString(hi.UTC().Format("2006-01")), sqlTime(lo), sqlTime(hi))
}

func (s *Server) pathType(r *http.Request) (*CatalogType, error) {
	t, ok := lookupType(r.PathValue("type"))
	if !ok {
		return nil, &apiError{http.StatusNotFound, fmt.Sprintf("unknown type %q: GET /v1/types lists them", r.PathValue("type"))}
	}
	return t, nil
}

func formatTime(us int64, loc *time.Location) string {
	return time.UnixMicro(us).In(loc).Format(time.RFC3339Nano)
}

// --- catalog

func (q *queryAPI) typeStats(ctx context.Context) (map[string]TypeStats, error) {
	q.mu.Lock()
	defer q.mu.Unlock() // one refresh at a time; the others wait for it
	if q.stats != nil && q.now().Sub(q.statsAt) < statsTTL {
		return q.stats, nil
	}
	stats := map[string]TypeStats{}
	err := q.db.Query(ctx, `SELECT type, count(*) AS n, epoch_us(min(start)) AS first, epoch_us(max(start)) AS last FROM samples GROUP BY type`,
		func(row json.RawMessage) error {
			var r struct {
				Type        string
				N           int64
				First, Last *int64
			}
			if err := json.Unmarshal(row, &r); err != nil {
				return err
			}
			st := TypeStats{Samples: r.N}
			if r.First != nil && r.Last != nil {
				f, l := time.UnixMicro(*r.First).UTC(), time.UnixMicro(*r.Last).UTC()
				st.First, st.Last = &f, &l
			}
			stats[r.Type] = st
			return nil
		})
	if err != nil {
		return nil, err
	}
	q.stats, q.statsAt = stats, q.now()
	return stats, nil
}

func aggregatable(kind string) bool { return kind == "quantity" || kind == "category" }

func (s *Server) queryKinds(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorize(w, r, ScopeRead); !ok {
		return
	}
	type kindInfo struct {
		Kind
		Types int `json:"types"`
	}
	out := make([]kindInfo, len(Kinds))
	for i, k := range Kinds {
		out[i].Kind = k
		for _, t := range catalogTypes {
			if t.Kind == k.Name {
				out[i].Types++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"kinds": out})
}

func (s *Server) queryTypes(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorize(w, r, ScopeRead); !ok {
		return
	}
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind != "" && !aggregatable(kind) {
		found := false
		for _, k := range Kinds {
			found = found || k.Name == kind
		}
		if !found {
			s.queryError(w, badParam("kind %q is not one of %s", kind, strings.Join(kindNames(), ", ")))
			return
		}
	}
	present, err := parseBool("present", q.Get("present"))
	if err != nil {
		s.queryError(w, err)
		return
	}
	stats, err := s.Query.typeStats(r.Context())
	if err != nil {
		s.queryError(w, err)
		return
	}
	out := []TypeInfo{}
	for i := range catalogTypes {
		t := &catalogTypes[i]
		if (kind != "" && t.Kind != kind) || (present && stats[t.Identifier].Samples == 0) {
			continue
		}
		out = append(out, TypeInfo{CatalogType: t, Aggregates: aggregatable(t.Kind), Data: stats[t.Identifier]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": out})
}

func (s *Server) queryType(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorize(w, r, ScopeRead); !ok {
		return
	}
	t, err := s.pathType(r)
	if err != nil {
		s.queryError(w, err)
		return
	}
	stats, err := s.Query.typeStats(r.Context())
	if err != nil {
		s.queryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, TypeInfo{CatalogType: t, Aggregates: aggregatable(t.Kind), Data: stats[t.Identifier]})
}

// --- samples

// SampleOut is one sample as the API returns it.
type SampleOut struct {
	UUID       string          `json:"uuid"`
	Start      string          `json:"start"`
	End        string          `json:"end"`
	Value      *float64        `json:"value"`
	Unit       string          `json:"unit,omitempty"`
	ValueLabel string          `json:"value_label,omitempty"`
	Source     SourceOut       `json:"source"`
	Raw        json.RawMessage `json:"raw,omitempty"`
}

// SourceOut is the app or device that wrote a sample.
type SourceOut struct {
	Name     string `json:"name,omitempty"`
	BundleID string `json:"bundle_id,omitempty"`
	Device   string `json:"device,omitempty"`
	Product  string `json:"product_type,omitempty"`
}

// A cursor is where the previous page stopped: the last sample's start and UUID, and the order.
type cursor struct {
	start int64
	uuid  string
	desc  bool
}

func (c cursor) String() string {
	o := "a"
	if c.desc {
		o = "d"
	}
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("1:%s:%d:%s", o, c.start, c.uuid)))
}

func parseCursor(v string) (cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(v)
	parts := strings.Split(string(b), ":")
	if err != nil || len(parts) != 4 || parts[0] != "1" || (parts[1] != "a" && parts[1] != "d") {
		return cursor{}, badParam("cursor is not one this server returned")
	}
	start, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || !uuidRE.MatchString(parts[3]) {
		return cursor{}, badParam("cursor is not one this server returned")
	}
	return cursor{start: start, uuid: strings.ToUpper(parts[3]), desc: parts[1] == "d"}, nil
}

func (s *Server) querySamples(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorize(w, r, ScopeRead); !ok {
		return
	}
	if err := s.samplesPage(w, r); err != nil {
		s.queryError(w, err)
	}
}

func (s *Server) samplesPage(w http.ResponseWriter, r *http.Request) error {
	t, err := s.pathType(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	loc, err := parseTZ(q.Get("tz"))
	if err != nil {
		return err
	}
	now := s.Query.now()
	end, ok, err := parseInstant("end", q.Get("end"), loc)
	if err != nil {
		return err
	} else if !ok {
		end = now.Add(time.Minute)
	}
	start, ok, err := parseInstant("start", q.Get("start"), loc)
	if err != nil {
		return err
	} else if !ok {
		start = end.Add(-24 * time.Hour)
	}
	if !start.Before(end) {
		return badParam("start must be before end")
	}
	maxLimit := min(10000, s.Query.db.MaxRows-1)
	limit := defaultSampleLimit
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > maxLimit {
			return badParam("limit must be 1 to %d", maxLimit)
		}
	}
	desc := false
	switch q.Get("order") {
	case "", "asc":
	case "desc":
		desc = true
	default:
		return badParam("order must be asc or desc")
	}
	raw, err := parseBool("raw", q.Get("raw"))
	if err != nil {
		return err
	}
	var after *cursor
	if v := q.Get("cursor"); v != "" {
		c, err := parseCursor(v)
		if err != nil {
			return err
		}
		if c.desc != desc {
			return badParam("cursor was made for the other order")
		}
		after = &c
	}

	cols := `uuid, epoch_us(start) AS st, epoch_us("end") AS en, value, unit, source_name, source_bundle, device,
		coalesce(json_extract_string(json, '$.source.product_type'), json_extract_string(json, '$.device.model')) AS product`
	if raw {
		cols += ", json"
	}
	where := partitionFilter(t, start, end)
	dir, cmp := "ASC", ">"
	if desc {
		dir, cmp = "DESC", "<"
	}
	if after != nil {
		ts := sqlTime(time.UnixMicro(after.start))
		where += fmt.Sprintf(" AND (start %s %s OR (start = %s AND uuid %s %s))", cmp, ts, ts, cmp, sqlString(after.uuid))
	}
	sql := fmt.Sprintf("SELECT %s FROM samples WHERE %s ORDER BY start %s, uuid %s LIMIT %d", cols, where, dir, dir, limit+1)

	out := []SampleOut{}
	var last cursor
	more := false
	err = s.Query.db.Query(r.Context(), sql, func(row json.RawMessage) error {
		if len(out) == limit {
			more = true
			return nil
		}
		var v struct {
			UUID         string
			St, En       int64
			Value        *float64
			Unit, Device *string
			SourceName   *string `json:"source_name"`
			SourceBundle *string `json:"source_bundle"`
			Product      *string
			JSON         *string
		}
		if err := json.Unmarshal(row, &v); err != nil {
			return err
		}
		smp := SampleOut{UUID: v.UUID, Start: formatTime(v.St, loc), End: formatTime(v.En, loc), Value: v.Value, Unit: deref(v.Unit),
			Source: SourceOut{Name: deref(v.SourceName), BundleID: deref(v.SourceBundle), Device: deref(v.Device), Product: deref(v.Product)}}
		if t.Kind == "category" && v.Value != nil {
			smp.ValueLabel = t.label(int(*v.Value))
		}
		if v.JSON != nil {
			smp.Raw = json.RawMessage(*v.JSON)
		}
		out = append(out, smp)
		last = cursor{start: v.St, uuid: v.UUID, desc: desc}
		return nil
	})
	if err != nil {
		return err
	}
	resp := map[string]any{"type": t.Identifier, "start": start.In(loc).Format(time.RFC3339Nano), "end": end.In(loc).Format(time.RFC3339Nano),
		"tz": loc.String(), "order": strings.ToLower(dir), "count": len(out), "samples": out}
	if more {
		resp["next_cursor"] = last.String()
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// --- aggregates

type bucket struct{ start, end time.Time }

// buckets splits [start, end) into days or hours of loc: start rounds down and end up to a
// boundary, so a day is a local calendar day (23 or 25 hours across a DST change).
func buckets(start, end time.Time, interval string, loc *time.Location) ([]bucket, error) {
	var out []bucket
	start, end = start.In(loc), end.In(loc)
	switch interval {
	case "day":
		d := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
		for d.Before(end) {
			next := time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, loc)
			out = append(out, bucket{d, next})
			if len(out) > maxDayBuckets {
				return nil, badParam("at most %d days per request: ask for a shorter range", maxDayBuckets)
			}
			d = next
		}
	case "hour":
		h := time.Date(start.Year(), start.Month(), start.Day(), start.Hour(), 0, 0, 0, loc)
		for h.Before(end) {
			next := h.Add(time.Hour)
			out = append(out, bucket{h, next})
			if len(out) > maxHourBuckets {
				return nil, badParam("at most %d hours per request: ask for a shorter range or interval=day", maxHourBuckets)
			}
			h = next
		}
	default:
		return nil, badParam("interval must be day or hour")
	}
	return out, nil
}

// sourcePriority ranks a sample's source: Apple Watch 0, iPhone 1, anything else 2. The
// product type (Watch6,14, iPhone17,5) is in the sample's source; older apps sent only the
// device.
const sourcePriority = `CASE
  WHEN coalesce(json_extract_string(json, '$.source.product_type'), json_extract_string(json, '$.device.model'), device, '') ILIKE '%watch%' THEN 0
  WHEN coalesce(json_extract_string(json, '$.source.product_type'), json_extract_string(json, '$.device.model'), device, '') ILIKE '%iphone%' THEN 1
  ELSE 2 END`

// aggregateSQL builds the query for an aggregate. Every value in it is the server's: the
// catalog identifier, bucket boundaries, the dedup rule.
func aggregateSQL(t *CatalogType, bs []bucket, dedup string) string {
	var b strings.Builder
	lo, hi := bs[0].start, bs[len(bs)-1].end
	b.WriteString("WITH b(i, bs, be) AS (VALUES ")
	for i, bk := range bs {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "(%d, %d, %d)", i, bk.start.UnixMicro(), bk.end.UnixMicro())
	}
	b.WriteString(")")
	if t.Kind == "quantity" && t.Aggregation != AggCumulative {
		// Discrete: every sample counts, in the bucket it starts in.
		mean := "avg(value)"
		if t.Aggregation == AggDiscreteEquivalentL {
			mean = "10 * log10(avg(pow(10, value / 10)))" // sound levels average as energy
		}
		fmt.Fprintf(&b, `,
s AS (SELECT epoch_us(start) AS st, value, unit, source_name AS sname FROM samples WHERE %s AND value IS NOT NULL)
SELECT b.i, s.unit, count(*) AS n, %s AS avg, min(s.value) AS min, max(s.value) AS max, list(DISTINCT s.sname ORDER BY s.sname) AS sources
FROM s JOIN b ON s.st >= b.bs AND s.st < b.be GROUP BY b.i, s.unit ORDER BY b.i, s.unit`, partitionFilter(t, lo, hi), mean)
		return b.String()
	}
	// Cumulative quantities and categories: a sample counts in every bucket it overlaps, for
	// the part inside it. With dedup=priority, a sample counts only for the part of its time
	// that no sample of a higher-ranked source covers (of the same value, for categories).
	fmt.Fprintf(&b, `,
r AS (SELECT uuid, epoch_us(start) AS st, epoch_us("end") AS en, greatest(epoch_us("end"), epoch_us(start) + 1) AS en2,
  value, unit, source_name AS sname, coalesce(source_bundle, source_name, '') AS src, %s AS prio
  FROM samples WHERE %s AND value IS NOT NULL)`, sourcePriority, partitionFilter(t, lo.Add(-overlapMargin), hi))
	if dedup == DedupPriority {
		sameValue := ""
		if t.Kind == "category" {
			sameValue = " AND h.value = a.value"
		}
		fmt.Fprintf(&b, `,
rk AS (SELECT *, dense_rank() OVER (ORDER BY prio, src) AS rank FROM r),
hs AS (SELECT uuid, st, en2, rank, value, unnest(range(st // 3600000000, (en2 - 1) // 3600000000 + 1)) AS hr FROM rk),
pairs AS (SELECT DISTINCT a.uuid, h.uuid AS by_uuid, least(a.en2, h.en2) - greatest(a.st, h.st) AS ov
  FROM hs a JOIN hs h ON a.hr = h.hr AND h.rank < a.rank AND h.st < a.en2 AND h.en2 > a.st%s),
cov AS (SELECT uuid, sum(ov) AS covered FROM pairs GROUP BY uuid),
k AS (SELECT rk.*, greatest(0, 1 - coalesce(cov.covered, 0) / (rk.en2 - rk.st)) AS w FROM rk LEFT JOIN cov USING (uuid))`, sameValue)
	} else {
		b.WriteString(`,
k AS (SELECT *, 1.0 AS w FROM r)`)
	}
	if t.Kind == "category" {
		b.WriteString(`
SELECT b.i, CAST(k.value AS BIGINT) AS value, count(*) FILTER (WHERE k.w > 0) AS n,
  sum((least(k.en, b.be) - greatest(k.st, b.bs)) * k.w) / 1e6 AS seconds, list(DISTINCT k.sname ORDER BY k.sname) FILTER (WHERE k.w > 0) AS sources
FROM k JOIN b ON k.st < b.be AND k.en2 > b.bs GROUP BY b.i, CAST(k.value AS BIGINT) ORDER BY b.i, 2`)
	} else {
		b.WriteString(`
SELECT b.i, k.unit, count(*) FILTER (WHERE k.w > 0) AS n,
  sum(CASE WHEN k.en > k.st THEN k.value * (least(k.en, b.be) - greatest(k.st, b.bs)) / (k.en - k.st) ELSE k.value END * k.w) AS sum,
  list(DISTINCT k.sname ORDER BY k.sname) FILTER (WHERE k.w > 0) AS sources
FROM k JOIN b ON k.st < b.be AND k.en2 > b.bs GROUP BY b.i, k.unit ORDER BY b.i, k.unit`)
	}
	return b.String()
}

// BucketOut is one interval of an aggregate.
type BucketOut struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Date  string `json:"date,omitempty"` // interval=day: the local date
	// Quantities.
	Unit string   `json:"unit,omitempty"`
	Sum  *float64 `json:"sum,omitempty"` // cumulative
	Avg  *float64 `json:"avg,omitempty"` // discrete
	Min  *float64 `json:"min,omitempty"`
	Max  *float64 `json:"max,omitempty"`
	// Categories.
	Values *[]CategoryBucket `json:"values,omitempty"`
	// Samples that counted (with dedup, those not entirely covered by a higher-ranked source).
	Samples int64    `json:"samples"`
	Sources []string `json:"sources"`
}

// CategoryBucket is one category value within a bucket.
type CategoryBucket struct {
	Value   int      `json:"value"`
	Label   string   `json:"label,omitempty"`
	Samples int64    `json:"samples"`
	Seconds float64  `json:"seconds"`
	Sources []string `json:"sources"`
}

func (s *Server) queryAggregates(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorize(w, r, ScopeRead); !ok {
		return
	}
	if err := s.aggregates(w, r); err != nil {
		s.queryError(w, err)
	}
}

func (s *Server) aggregates(w http.ResponseWriter, r *http.Request) error {
	t, err := s.pathType(r)
	if err != nil {
		return err
	}
	if !aggregatable(t.Kind) {
		return badParam("%s is a %s type: aggregates exist for quantity and category types; read its samples instead", t.Identifier, t.Kind)
	}
	q := r.URL.Query()
	loc, err := parseTZ(q.Get("tz"))
	if err != nil {
		return err
	}
	interval := cmpOr(q.Get("interval"), "day")
	dedup := cmpOr(q.Get("dedup"), DedupPriority)
	if dedup != DedupPriority && dedup != DedupNone {
		return badParam("dedup must be priority or none")
	}
	now := s.Query.now()
	end, ok, err := parseInstant("end", q.Get("end"), loc)
	if err != nil {
		return err
	} else if !ok {
		end = now
	}
	start, ok, err := parseInstant("start", q.Get("start"), loc)
	if err != nil {
		return err
	} else if !ok {
		if interval == "hour" {
			start = end.Add(-23 * time.Hour)
		} else {
			start = end.In(loc).AddDate(0, 0, -6)
		}
	}
	if !start.Before(end) {
		return badParam("start must be before end")
	}
	bs, err := buckets(start, end, interval, loc)
	if err != nil {
		return err
	}
	out := make([]BucketOut, 0, len(bs))
	for _, bk := range bs {
		bo := BucketOut{Start: bk.start.Format(time.RFC3339), End: bk.end.Format(time.RFC3339), Sources: []string{}}
		if interval == "day" {
			bo.Date = bk.start.Format("2006-01-02")
		}
		if t.Kind == "category" {
			bo.Values = &[]CategoryBucket{}
		} else if t.Aggregation == AggCumulative {
			zero := 0.0
			bo.Sum = &zero
		}
		out = append(out, bo)
	}
	// A bucket with samples in several units gets one entry per unit (rare: the app sends
	// each type in one unit).
	extra := map[int][]BucketOut{}
	err = s.Query.db.Query(r.Context(), aggregateSQL(t, bs, dedup), func(row json.RawMessage) error {
		var v struct {
			I                  int
			Unit               *string
			N                  int64
			Sum, Avg, Min, Max *float64
			Value              int
			Seconds            float64
			Sources            []string
		}
		if err := json.Unmarshal(row, &v); err != nil {
			return err
		}
		if v.I < 0 || v.I >= len(out) {
			return fmt.Errorf("bucket %d out of range", v.I)
		}
		if v.Sources == nil {
			v.Sources = []string{}
		}
		bo := &out[v.I]
		if t.Kind == "category" {
			*bo.Values = append(*bo.Values, CategoryBucket{Value: v.Value, Label: t.label(v.Value), Samples: v.N, Seconds: v.Seconds, Sources: v.Sources})
			bo.Samples += v.N
			bo.Sources = mergeSources(bo.Sources, v.Sources)
			return nil
		}
		if bo.Unit != "" {
			cp := *bo
			bo = &cp
		}
		bo.Unit, bo.Samples, bo.Sources = deref(v.Unit), v.N, v.Sources
		if t.Aggregation == AggCumulative {
			bo.Sum = v.Sum
		} else {
			bo.Avg, bo.Min, bo.Max = v.Avg, v.Min, v.Max
		}
		if bo != &out[v.I] {
			extra[v.I] = append(extra[v.I], *bo)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(extra) > 0 {
		var all []BucketOut
		for i, bo := range out {
			all = append(append(all, bo), extra[i]...)
		}
		out = all
	}
	resp := map[string]any{"type": t.Identifier, "kind": t.Kind, "interval": interval, "tz": loc.String(),
		"start": bs[0].start.Format(time.RFC3339), "end": bs[len(bs)-1].end.Format(time.RFC3339), "buckets": out}
	if t.Kind == "quantity" {
		resp["aggregation"] = t.Aggregation
	}
	if t.Kind == "category" || t.Aggregation == AggCumulative {
		resp["dedup"] = dedup
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func mergeSources(a, b []string) []string {
	for _, x := range b {
		found := false
		for _, y := range a {
			found = found || x == y
		}
		if !found {
			a = append(a, x)
		}
	}
	return a
}
