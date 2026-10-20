package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// Server serves the HTTP API.
type Server struct {
	Archive      *Store
	Forwarder    *Forwarder // nil: no forwarding
	AdminKey     *AdminKey  // the admin token (its hash, admin.json)
	MaxBodyBytes int64
	Devices      *Devices
	Codes        *Codes
	PublicURL    string    // how phones reach the server, for setup links; "" offers what it finds (address.go)
	SetupFrom    string    // who may use /setup and pairing: "private" (default), "any", "off"
	Listen       string    // the listen address, to tell which local addresses reach the server
	Query        *queryAPI // nil: no query API (-query-duckdb not set)

	netInfo *netInfo // nil: the real interfaces and tailscale CLI

	sessions sessions

	mu              sync.Mutex
	stats           map[string]int64 // counter name -> value, see writeMetrics
	lastUpload      time.Time
	lastBatch       map[string]time.Time // trigger -> last accepted batch
	lastHeartbeat   map[string]time.Time // trigger -> last heartbeat
	refusalLog      time.Time            // when a refused token was last logged
	refusalsQuiet   int64                // refused tokens not logged since
	forwardErrors   int64
	adminUploadsLog time.Time // when an upload with the admin token was last logged
}

// MaxBatchPartitions bounds the archive partitions one batch may touch.
const MaxBatchPartitions = 2000

// Response is the body of a successful POST /v1/samples.
type Response struct {
	Received   int `json:"received"`
	Stored     int `json:"stored"`
	Duplicates int `json:"duplicates"`
	Deletions  int `json:"deletions"`
	// Records received (v2). Always present, so a client can tell a v2 server, which stores
	// records, from a v1 server, which would drop them.
	Records int `json:"records"`
	// Version is the newest wire format the server understands.
	Version int `json:"version"`
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

type route struct {
	pattern string
	handler http.HandlerFunc
}

// routes lists every route the server serves; the OpenAPI document must describe the query
// API's (openapi_test.go).
func (s *Server) routes() []route {
	rs := []route{
		{"GET /healthz", s.healthz},
		{"GET /metrics", s.metrics},
		{"POST /v1/samples", s.samples},
		{"POST /v1/heartbeat", s.heartbeat},
		{"GET /v1/status", s.statusHandler},
		{"DELETE /v1/data", s.eraseHandler},
		{"POST /v1/pair", s.pair},
		{"POST /v1/pairing-codes", s.pairingCodes},
		{"GET /v1/devices", s.devicesList},
		{"DELETE /v1/devices/{id}", s.deviceRevoke},
		{"POST /v1/tokens", s.tokensCreate},
		{"GET /setup", s.setupPage},
		{"POST /setup", s.setupLogin},
		{"POST /setup/revoke", s.setupRevoke},
		{"POST /setup/tokens", s.setupToken},
		{"GET /setup/status", s.setupStatus},
		{"GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
		}},
	}
	if s.Query != nil {
		rs = append(rs,
			route{"GET /openapi.json", s.openAPI},
			route{"GET /docs", s.docs},
			route{"GET /docs/{file}", s.docs},
			route{"GET /v1/types", s.queryTypes},
			route{"GET /v1/types/{type}", s.queryType},
			route{"GET /v1/types/{type}/samples", s.querySamples},
			route{"GET /v1/types/{type}/aggregates", s.queryAggregates},
		)
	}
	return rs
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintln(w, "ok")
}

func (s *Server) samples(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorize(w, r, ScopeUpload)
	if !ok {
		return
	}
	s.adminUpload(c, r)
	r.Body = http.MaxBytesReader(w, r.Body, s.MaxBodyBytes)
	var b Batch
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&b); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("body too large: this server accepts at most %d bytes (-max-body-bytes)", s.MaxBodyBytes))
			return
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			// The app went away mid-upload (iOS suspended it): it sends the batch again.
			s.count("incomplete")
			httpError(w, http.StatusBadRequest, "incomplete body")
			return
		}
		s.badRequest(w, "invalid JSON: "+err.Error())
		return
	}

	// Validate everything first: a batch is stored whole or not at all, so the client can
	// advance its HealthKit anchor only on success.
	v := b.version()
	items := make([]Item, 0, len(b.Samples)+len(b.Records)+len(b.Deletions))
	samples := make([]*Sample, 0, len(b.Samples))
	for i, raw := range b.Samples {
		smp, err := parseSample(raw)
		if err != nil {
			s.badRequest(w, fmt.Sprintf("samples[%d]: %v", i, err))
			return
		}
		samples = append(samples, smp)
		items = append(items, sampleItem(smp, raw, v))
	}
	records := make([]*RecordIn, 0, len(b.Records))
	for i, raw := range b.Records {
		r, err := parseRecord(raw)
		if err != nil {
			s.badRequest(w, fmt.Sprintf("records[%d]: %v", i, err))
			return
		}
		records = append(records, r)
		items = append(items, recordItem(r, raw))
	}
	for i, raw := range b.Deletions {
		d, err := parseDeletion(raw)
		if err != nil {
			s.badRequest(w, fmt.Sprintf("deletions[%d]: %v", i, err))
			return
		}
		items = append(items, deletionItem(d, raw))
	}

	// Types and months name directories: a batch spanning more than this is not a phone's.
	parts := map[string]bool{}
	for i := range items {
		if it := &items[i]; it.Op != "delete" {
			parts[it.Op+"/"+it.Type+"/"+month(it.Start)] = true
		}
	}
	if len(parts) > MaxBatchPartitions {
		s.badRequest(w, fmt.Sprintf("batch spans %d (type, month) pairs, more than %d", len(parts), MaxBatchPartitions))
		return
	}

	uploader := ""
	if c.Device != nil {
		uploader = c.Device.ID
	}
	outcomes, err := s.Archive.Ingest(uploader, items)
	if err != nil {
		log.Printf("archive: %v", err)
		s.count("archive_error")
		httpError(w, http.StatusInternalServerError, "the server could not store the batch: its log says why")
		return
	}
	added := 0
	for _, o := range outcomes {
		if o.Stored {
			added++
		}
	}

	// Forward only what was new; the archive stays the source of truth if this fails. A v2
	// copy of a sample app 0.1 already sent is archived, but its value was forwarded then.
	if s.Forwarder != nil && added > 0 {
		var fresh []*Sample
		for i, smp := range samples {
			if o := outcomes[i]; o.Stored && !o.HadV1 {
				fresh = append(fresh, smp)
			}
		}
		var freshRecords []*RecordIn
		for i, rec := range records {
			if outcomes[len(samples)+i].Stored {
				freshRecords = append(freshRecords, rec)
			}
		}
		lines := append(s.Forwarder.Lines(fresh), s.Forwarder.RecordLines(freshRecords)...)
		if err := s.Forwarder.Send(r.Context(), lines); err != nil {
			log.Printf("forward: %v", err)
			s.mu.Lock()
			s.forwardErrors++
			s.mu.Unlock()
		}
	}

	resp := Response{Received: len(items), Stored: added, Duplicates: len(items) - added, Deletions: len(b.Deletions),
		Records: len(b.Records), Version: WireVersion}
	trigger := b.trigger()
	s.mu.Lock()
	s.lastUpload = time.Now()
	if s.lastBatch == nil {
		s.lastBatch = map[string]time.Time{}
	}
	s.lastBatch[trigger] = s.lastUpload
	s.mu.Unlock()
	s.count("ok")
	s.add("stored", int64(added))
	s.add("duplicates", int64(resp.Duplicates))
	s.add("batches_"+trigger, 1)
	log.Printf("batch v%d: %d samples, %d records, %d deletions, %d stored, %d duplicates, trigger %s, app %s",
		v, len(b.Samples), len(b.Records), len(b.Deletions), added, resp.Duplicates, trigger, b.appState())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// badRequest rejects the whole batch. The reason is logged too: the client may not show it,
// and it names fields and keys, not health values.
func (s *Server) badRequest(w http.ResponseWriter, msg string) {
	s.count("bad_request")
	log.Printf("rejected batch: %s", msg)
	httpError(w, http.StatusBadRequest, msg)
}

func (s *Server) count(name string) { s.add("requests_"+name, 1) }

func (s *Server) add(name string, n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stats == nil {
		s.stats = make(map[string]int64)
	}
	s.stats[name] += n
}

// metrics serves Prometheus text format. Counts only: no health data.
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintln(w, "# HELP healthkit_relay_requests_total Uploads (POST /v1/samples), pairings, refused tokens and failed queries by result.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_requests_total counter")
	for _, res := range []string{"ok", "unauthorized", "bad_request", "incomplete", "archive_error", "paired", "pair_rejected", "erased", "forbidden", "query_error", "query_timeout", "query_busy"} {
		fmt.Fprintf(w, "healthkit_relay_requests_total{result=%q} %d\n", res, s.stats["requests_"+res])
	}
	fmt.Fprintln(w, "# HELP healthkit_relay_admin_token_uploads_total Uploads and status requests authenticated with the admin token instead of a paired device's token (deprecated).")
	fmt.Fprintln(w, "# TYPE healthkit_relay_admin_token_uploads_total counter")
	fmt.Fprintf(w, "healthkit_relay_admin_token_uploads_total %d\n", s.stats["admin_token_uploads"])
	fmt.Fprintln(w, "# HELP healthkit_relay_records_total Records received, by outcome.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_records_total counter")
	fmt.Fprintf(w, "healthkit_relay_records_total{outcome=\"stored\"} %d\n", s.stats["stored"])
	fmt.Fprintf(w, "healthkit_relay_records_total{outcome=\"duplicate\"} %d\n", s.stats["duplicates"])
	fmt.Fprintln(w, "# HELP healthkit_relay_batches_total Accepted batches, by what started the app's sync (unknown: app before 0.6.1).")
	fmt.Fprintln(w, "# TYPE healthkit_relay_batches_total counter")
	for _, t := range Triggers {
		fmt.Fprintf(w, "healthkit_relay_batches_total{trigger=%q} %d\n", t, s.stats["batches_"+t])
	}
	fmt.Fprintln(w, "# HELP healthkit_relay_archive_records Distinct records in the archive.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_archive_records gauge")
	fmt.Fprintf(w, "healthkit_relay_archive_records %d\n", s.Archive.Len())
	fmt.Fprintln(w, "# HELP healthkit_relay_staged_records Records received but not flushed to Parquet yet.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_staged_records gauge")
	fmt.Fprintf(w, "healthkit_relay_staged_records %d\n", s.Archive.Staged())
	st := s.Archive.Stats()
	fmt.Fprintln(w, "# HELP healthkit_relay_last_success_timestamp_seconds Unix time of the last successful flush (hourly) or compaction (nightly); 0: never.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_last_success_timestamp_seconds gauge")
	fmt.Fprintf(w, "healthkit_relay_last_success_timestamp_seconds{job=\"flush\"} %d\n", unixOrZero(st.lastFlush))
	fmt.Fprintf(w, "healthkit_relay_last_success_timestamp_seconds{job=\"compaction\"} %d\n", unixOrZero(st.lastCompaction))
	fmt.Fprintln(w, "# HELP healthkit_relay_maintenance_errors_total Failed flushes and compactions since start.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_maintenance_errors_total counter")
	fmt.Fprintf(w, "healthkit_relay_maintenance_errors_total{job=\"flush\"} %d\n", st.flushErrors)
	fmt.Fprintf(w, "healthkit_relay_maintenance_errors_total{job=\"compaction\"} %d\n", st.compactErrs)
	fmt.Fprintln(w, "# HELP healthkit_relay_devices Paired devices.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_devices gauge")
	fmt.Fprintf(w, "healthkit_relay_devices %d\n", s.Devices.Len())
	fmt.Fprintln(w, "# HELP healthkit_relay_forward_errors_total Failed metric forwards.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_forward_errors_total counter")
	fmt.Fprintf(w, "healthkit_relay_forward_errors_total %d\n", s.forwardErrors)
	fmt.Fprintln(w, "# HELP healthkit_relay_last_upload_timestamp_seconds Unix time of the last accepted batch (0: none since start).")
	fmt.Fprintln(w, "# TYPE healthkit_relay_last_upload_timestamp_seconds gauge")
	fmt.Fprintf(w, "healthkit_relay_last_upload_timestamp_seconds %d\n", unixOrZero(s.lastUpload))
	fmt.Fprintln(w, "# HELP healthkit_relay_last_batch_timestamp_seconds Unix time of the last accepted batch, by what started the app's sync (0: none since start).")
	fmt.Fprintln(w, "# TYPE healthkit_relay_last_batch_timestamp_seconds gauge")
	for _, t := range Triggers {
		fmt.Fprintf(w, "healthkit_relay_last_batch_timestamp_seconds{trigger=%q} %d\n", t, unixOrZero(s.lastBatch[t]))
	}
	fmt.Fprintln(w, "# HELP healthkit_relay_heartbeats_total Heartbeats from the app (0.6.2+): one per background run, also when it could not read Health or upload, by what woke it.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_heartbeats_total counter")
	for _, t := range Triggers {
		fmt.Fprintf(w, "healthkit_relay_heartbeats_total{trigger=%q} %d\n", t, s.stats["heartbeats_"+t])
	}
	fmt.Fprintln(w, "# HELP healthkit_relay_last_heartbeat_timestamp_seconds Unix time of the app's last heartbeat, by what woke it (0: none since start).")
	fmt.Fprintln(w, "# TYPE healthkit_relay_last_heartbeat_timestamp_seconds gauge")
	for _, t := range Triggers {
		fmt.Fprintf(w, "healthkit_relay_last_heartbeat_timestamp_seconds{trigger=%q} %d\n", t, unixOrZero(s.lastHeartbeat[t]))
	}
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
