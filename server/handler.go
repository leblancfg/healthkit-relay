package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Server serves the HTTP API.
type Server struct {
	Archive      *Archive
	Forwarder    *Forwarder // nil: no forwarding
	Token        string
	MaxBodyBytes int64

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
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("POST /v1/samples", s.samples)
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintln(w, "ok")
}

func (s *Server) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) == 1
}

func (s *Server) samples(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		s.count("unauthorized")
		w.Header().Set("WWW-Authenticate", "Bearer")
		httpError(w, http.StatusUnauthorized, "unauthorized")
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
		s.count("bad_request")
		httpError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Validate everything first: a batch is stored whole or not at all, so the client can
	// advance its HealthKit anchor only on success.
	recs := make([]Record, 0, len(b.Samples)+len(b.Deletions))
	samples := make([]*Sample, 0, len(b.Samples))
	for i, raw := range b.Samples {
		smp, err := parseSample(raw)
		if err != nil {
			s.count("bad_request")
			httpError(w, http.StatusBadRequest, fmt.Sprintf("samples[%d]: %v", i, err))
			return
		}
		samples = append(samples, smp)
		recs = append(recs, Record{Op: "sample", UUID: smp.UUID, Type: smp.Type, Data: raw})
	}
	for i, raw := range b.Deletions {
		d, err := parseDeletion(raw)
		if err != nil {
			s.count("bad_request")
			httpError(w, http.StatusBadRequest, fmt.Sprintf("deletions[%d]: %v", i, err))
			return
		}
		recs = append(recs, Record{Op: "delete", UUID: d.UUID, Type: d.Type, Data: raw})
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
			if rec.Op == "sample" {
				isNew[rec.UUID] = true
			}
		}
		var fresh []*Sample
		for _, smp := range samples {
			if isNew[smp.UUID] {
				fresh = append(fresh, smp)
				delete(isNew, smp.UUID) // a UUID repeated within the batch is sent once
			}
		}
		if err := s.Forwarder.Send(r.Context(), s.Forwarder.Lines(fresh)); err != nil {
			log.Printf("forward: %v", err)
			s.mu.Lock()
			s.forwardErrors++
			s.mu.Unlock()
		}
	}

	resp := Response{Received: len(recs), Stored: added, Duplicates: len(recs) - added, Deletions: len(b.Deletions)}
	s.mu.Lock()
	s.lastUpload = time.Now()
	s.mu.Unlock()
	s.count("ok")
	s.add("stored", int64(added))
	s.add("duplicates", int64(resp.Duplicates))
	log.Printf("batch: %d samples, %d deletions, %d stored, %d duplicates", len(b.Samples), len(b.Deletions), added, resp.Duplicates)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
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
	fmt.Fprintln(w, "# HELP healthkit_relay_requests_total POST /v1/samples requests by result.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_requests_total counter")
	for _, res := range []string{"ok", "unauthorized", "bad_request", "archive_error"} {
		fmt.Fprintf(w, "healthkit_relay_requests_total{result=%q} %d\n", res, s.stats["requests_"+res])
	}
	fmt.Fprintln(w, "# HELP healthkit_relay_records_total Records received, by outcome.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_records_total counter")
	fmt.Fprintf(w, "healthkit_relay_records_total{outcome=\"stored\"} %d\n", s.stats["stored"])
	fmt.Fprintf(w, "healthkit_relay_records_total{outcome=\"duplicate\"} %d\n", s.stats["duplicates"])
	fmt.Fprintln(w, "# HELP healthkit_relay_archive_records Distinct records in the archive.")
	fmt.Fprintln(w, "# TYPE healthkit_relay_archive_records gauge")
	fmt.Fprintf(w, "healthkit_relay_archive_records %d\n", s.Archive.Len())
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
