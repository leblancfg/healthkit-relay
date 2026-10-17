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
	Archive      *Archive
	Forwarder    *Forwarder // nil: no forwarding
	Token        string     // the server token: admin, and accepted for uploads
	MaxBodyBytes int64
	Devices      *Devices
	Codes        *Codes
	PublicURL    string // how phones reach the server, for setup links; "" offers what it finds (address.go)
	SetupFrom    string // who may use /setup and pairing: "private" (default), "any", "off"
	Listen       string // the listen address, to tell which local addresses reach the server

	netInfo *netInfo // nil: the real interfaces and tailscale CLI

	sessions sessions

	mu            sync.Mutex
	stats         map[string]int64 // counter name -> value, see writeMetrics
	lastUpload    time.Time
	forwardErrors int64
}

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
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("POST /v1/samples", s.samples)
	mux.HandleFunc("GET /v1/status", s.statusHandler)
	mux.HandleFunc("POST /v1/pair", s.pair)
	mux.HandleFunc("POST /v1/pairing-codes", s.pairingCodes)
	mux.HandleFunc("GET /v1/devices", s.devicesList)
	mux.HandleFunc("DELETE /v1/devices/{id}", s.deviceRevoke)
	mux.HandleFunc("GET /setup", s.setupPage)
	mux.HandleFunc("POST /setup", s.setupLogin)
	mux.HandleFunc("POST /setup/revoke", s.setupRevoke)
	mux.HandleFunc("GET /setup/status", s.setupStatus)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
	})
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintln(w, "ok")
}

func (s *Server) samples(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.caller(r); !ok {
		s.unauthorized(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.MaxBodyBytes)
	var b Batch
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&b); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpError(w, http.StatusRequestEntityTooLarge, "body too large")
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
	recs := make([]Record, 0, len(b.Samples)+len(b.Records)+len(b.Deletions))
	samples := make([]*Sample, 0, len(b.Samples))
	for i, raw := range b.Samples {
		smp, err := parseSample(raw)
		if err != nil {
			s.badRequest(w, fmt.Sprintf("samples[%d]: %v", i, err))
			return
		}
		samples = append(samples, smp)
		rec := Record{Op: "sample", UUID: smp.UUID, Type: smp.Type, Data: raw}
		if v >= 2 {
			rec.V = v
		}
		recs = append(recs, rec)
	}
	records := make([]*RecordIn, 0, len(b.Records))
	for i, raw := range b.Records {
		r, err := parseRecord(raw)
		if err != nil {
			s.badRequest(w, fmt.Sprintf("records[%d]: %v", i, err))
			return
		}
		records = append(records, r)
		recs = append(recs, Record{Op: "record", Type: r.Kind, Key: r.Key, Digest: r.digest, Data: raw})
	}
	for i, raw := range b.Deletions {
		d, err := parseDeletion(raw)
		if err != nil {
			s.badRequest(w, fmt.Sprintf("deletions[%d]: %v", i, err))
			return
		}
		recs = append(recs, Record{Op: "delete", UUID: d.UUID, Type: d.Type, Data: raw})
	}

	// A v2 copy of a sample app 0.1 already sent is archived, but its value was forwarded then.
	forwardedBefore := make(map[string]bool)
	if v >= 2 && s.Forwarder != nil {
		for _, smp := range samples {
			if s.Archive.Has("sample:" + smp.UUID) {
				forwardedBefore[smp.UUID] = true
			}
		}
	}

	stored, err := s.Archive.Append(recs)
	if err != nil {
		log.Printf("archive: %v", err)
		s.count("archive_error")
		httpError(w, http.StatusInternalServerError, "could not store batch")
		return
	}

	// Forward only what was new; the archive stays the source of truth if this fails.
	added := len(stored)
	if s.Forwarder != nil && added > 0 {
		isNew := make(map[string]bool, added)
		for _, rec := range stored {
			switch rec.Op {
			case "sample":
				isNew[rec.UUID] = !forwardedBefore[rec.UUID]
			case "record":
				isNew[rec.id()] = true
			}
		}
		var fresh []*Sample
		for _, smp := range samples {
			if isNew[smp.UUID] {
				fresh = append(fresh, smp)
				delete(isNew, smp.UUID) // a UUID repeated within the batch is sent once
			}
		}
		var freshRecords []*RecordIn
		for _, rec := range records {
			id := (&Record{Op: "record", Type: rec.Kind, Key: rec.Key, Digest: rec.digest}).id()
			if isNew[id] {
				freshRecords = append(freshRecords, rec)
				delete(isNew, id)
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

	resp := Response{Received: len(recs), Stored: added, Duplicates: len(recs) - added, Deletions: len(b.Deletions),
		Records: len(b.Records), Version: WireVersion}
	s.mu.Lock()
	s.lastUpload = time.Now()
	s.mu.Unlock()
	s.count("ok")
	s.add("stored", int64(added))
	s.add("duplicates", int64(resp.Duplicates))
	log.Printf("batch v%d: %d samples, %d records, %d deletions, %d stored, %d duplicates",
		v, len(b.Samples), len(b.Records), len(b.Deletions), added, resp.Duplicates)
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
	fmt.Fprintln(w, "# HELP healthkit_relay_requests_total Uploads (POST /v1/samples) and pairings by result.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_requests_total counter")
	for _, res := range []string{"ok", "unauthorized", "bad_request", "incomplete", "archive_error", "paired", "pair_rejected"} {
		fmt.Fprintf(w, "healthkit_relay_requests_total{result=%q} %d\n", res, s.stats["requests_"+res])
	}
	fmt.Fprintln(w, "# HELP healthkit_relay_records_total Records received, by outcome.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_records_total counter")
	fmt.Fprintf(w, "healthkit_relay_records_total{outcome=\"stored\"} %d\n", s.stats["stored"])
	fmt.Fprintf(w, "healthkit_relay_records_total{outcome=\"duplicate\"} %d\n", s.stats["duplicates"])
	fmt.Fprintln(w, "# HELP healthkit_relay_archive_records Distinct records in the archive.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_archive_records gauge")
	fmt.Fprintf(w, "healthkit_relay_archive_records %d\n", s.Archive.Len())
	fmt.Fprintln(w, "# HELP healthkit_relay_devices Paired devices.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_devices gauge")
	fmt.Fprintf(w, "healthkit_relay_devices %d\n", s.Devices.Len())
	fmt.Fprintln(w, "# HELP healthkit_relay_forward_errors_total Failed metric forwards.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_forward_errors_total counter")
	fmt.Fprintf(w, "healthkit_relay_forward_errors_total %d\n", s.forwardErrors)
	fmt.Fprintln(w, "# HELP healthkit_relay_last_upload_timestamp_seconds Unix time of the last accepted batch (0: none since start).")
	fmt.Fprintln(w, "# TYPE healthkit_relay_last_upload_timestamp_seconds gauge")
	var last int64
	if !s.lastUpload.IsZero() {
		last = s.lastUpload.Unix()
	}
	fmt.Fprintf(w, "healthkit_relay_last_upload_timestamp_seconds %d\n", last)
}
