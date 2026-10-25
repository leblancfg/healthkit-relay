package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Demo mode (-demo-code-file): a public server for trying the app, such as the one App Review
// uses. It keeps nothing about the people who use it:
//
//   - Health data: each batch is checked like on any server, answered as stored, then
//     dropped. The server counts records received, all devices together, for the app's
//     "Records on server"; the count goes back to zero every -demo-reset (an hour).
//   - Devices: pairing with the shared code returns a signed token (an HMAC with a key in
//     <archive-dir>/demo-key) that is valid for DemoTokenLifetime. The server checks the
//     signature and stores no device list, name, model or last-seen time; every device is
//     "Demo iPhone".
//   - Logs: no per-request lines, only an hourly summary of counts.
//
// Between two resets it accepts at most -demo-quota bytes of uploads, DemoUploads uploads at
// once and DemoPairings pairings, answering 429 beyond that; the app retries later on its own.
type Demo struct {
	codeHash   string // SHA-256 of the normalized shared code
	resetEvery time.Duration
	quota      int64
	key        []byte // signs demo tokens
	uploads    chan struct{}

	mu        sync.Mutex
	used      int64
	pairings  int
	batches   int
	received  int // records received since the last reset, all devices
	nextReset time.Time
}

const (
	// DemoTokenLifetime is how long a demo token works.
	DemoTokenLifetime = 30 * 24 * time.Hour
	// DemoPairings bounds pairings with the shared code between two resets.
	DemoPairings = 500
	// DemoUploads bounds uploads read at once.
	DemoUploads = 4
	// minDemoCode is the shortest shared code accepted: 16 characters of A-Z2-7, 80 bits.
	minDemoCode = 16
	demoKeyFile = "demo-key"
	demoPrefix  = "demo."
)

// NewDemo checks the shared code, loads (or makes) the token key in dir and returns the demo
// settings.
func NewDemo(dir, code string, resetEvery time.Duration, quota int64) (*Demo, error) {
	code = normalizeCode(code)
	if len(code) < minDemoCode {
		return nil, fmt.Errorf("the demo setup code must be at least %d characters (letters A-Z and digits 2-7 make it easy to type)", minDemoCode)
	}
	if resetEvery < time.Minute {
		return nil, errors.New("-demo-reset must be at least a minute")
	}
	key, err := demoKey(dir)
	if err != nil {
		return nil, fmt.Errorf("demo token key: %w", err)
	}
	return &Demo{codeHash: hashToken(code), resetEvery: resetEvery, quota: quota, key: key,
		uploads: make(chan struct{}, DemoUploads), nextReset: time.Now().Add(resetEvery)}, nil
}

// demoKey reads dir/demo-key, making it on first start. Replacing the file signs out every
// demo device.
func demoKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, demoKeyFile)
	unlock, err := lockState(path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			panic(err) // crypto/rand does not fail on supported platforms
		}
		return key, writeFileAtomic(path, []byte(hex.EncodeToString(key)+"\n"))
	} else if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) < 32 {
		return nil, fmt.Errorf("%s is not a 32-byte hex key", path)
	}
	return key, nil
}

// isCode tells whether code is the shared demo code, in constant time.
func (d *Demo) isCode(code string) bool {
	if d == nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hashToken(normalizeCode(code))), []byte(d.codeHash)) == 1
}

// mint returns a new demo token and the device it stands for.
func (d *Demo) mint(now time.Time) (string, Device) {
	payload := make([]byte, 16)
	if _, err := rand.Read(payload[:8]); err != nil {
		panic(err)
	}
	binary.BigEndian.PutUint64(payload[8:], uint64(now.Add(DemoTokenLifetime).Unix()))
	token := demoPrefix + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(d.sign(payload))
	return token, demoDevice(payload, now)
}

func (d *Demo) sign(payload []byte) []byte {
	m := hmac.New(sha256.New, d.key)
	m.Write(payload)
	return m.Sum(nil)
}

// lookup checks a demo token's signature and lifetime.
func (d *Demo) lookup(token string, now time.Time) (Device, bool) {
	rest, ok := strings.CutPrefix(token, demoPrefix)
	if d == nil || !ok {
		return Device{}, false
	}
	p, s, ok := strings.Cut(rest, ".")
	payload, err1 := base64.RawURLEncoding.DecodeString(p)
	sig, err2 := base64.RawURLEncoding.DecodeString(s)
	if !ok || err1 != nil || err2 != nil || len(payload) != 16 || !hmac.Equal(sig, d.sign(payload)) {
		return Device{}, false
	}
	if now.Unix() > int64(binary.BigEndian.Uint64(payload[8:])) {
		return Device{}, false
	}
	return demoDevice(payload, now), true
}

func demoDevice(payload []byte, now time.Time) Device {
	return Device{ID: hex.EncodeToString(payload[:4]), Name: "Demo iPhone", Scope: ScopeUpload,
		CreatedAt: now.UTC().Truncate(time.Second)}
}

// admitPairing counts a pairing with the shared code; false once DemoPairings is reached.
func (d *Demo) admitPairing() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pairings >= DemoPairings {
		return false
	}
	d.pairings++
	return true
}

// discard counts a validated batch of n items and drops it.
func (d *Demo) discard(n int) []Outcome {
	d.mu.Lock()
	d.batches++
	d.received += n
	d.mu.Unlock()
	out := make([]Outcome, n)
	for i := range out {
		out[i].Stored = true
	}
	return out
}

// records is the number of records received since the last reset, all devices.
func (d *Demo) records() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.received
}

// demoAdmit lets an upload in when the quota has room and fewer than DemoUploads are read at
// once; the body may then use what is left of the quota, at most. It answers 429 otherwise.
// ok is false when it has answered; done releases the upload's slot.
func (s *Server) demoAdmit(w http.ResponseWriter, r *http.Request) (done func(), ok bool) {
	if s.Demo == nil {
		return func() {}, true
	}
	d := s.Demo
	refuse := func(wait time.Duration, msg string) (func(), bool) {
		s.count("demo_quota")
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		httpError(w, http.StatusTooManyRequests, msg)
		return nil, false
	}
	select {
	case d.uploads <- struct{}{}:
	default:
		return refuse(10*time.Second, "this demo server is busy: try again in a few seconds")
	}
	d.mu.Lock()
	left, wait := d.quota-d.used, max(time.Until(d.nextReset), time.Second)
	d.mu.Unlock()
	if left <= 0 {
		<-d.uploads
		return refuse(wait, fmt.Sprintf("this demo server accepts %d MB of uploads per %s: try again in %s",
			d.quota>>20, d.resetEvery, wait.Round(time.Minute)))
	}
	r.Body = countingReader{http.MaxBytesReader(w, r.Body, min(left, s.MaxBodyBytes)), d}
	return func() { <-d.uploads }, true
}

// countingReader counts the bytes of an upload toward the demo quota.
type countingReader struct {
	r io.ReadCloser
	d *Demo
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.d.mu.Lock()
	c.d.used += int64(n)
	c.d.mu.Unlock()
	return n, err
}

func (c countingReader) Close() error { return c.r.Close() }

// runDemoResets starts a new period every resetEvery until ctx ends.
func (s *Server) runDemoResets(ctx context.Context) {
	t := time.NewTicker(s.Demo.resetEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.demoReset()
		}
	}
}

// demoReset zeroes the counts and logs what the period saw, in counts only.
func (s *Server) demoReset() {
	d := s.Demo
	d.mu.Lock()
	used, pairings, batches, received := d.used, d.pairings, d.batches, d.received
	d.used, d.pairings, d.batches, d.received = 0, 0, 0, 0
	d.nextReset = time.Now().Add(d.resetEvery)
	d.mu.Unlock()
	log.Printf("demo: last %s: %d pairings, %d batches, %d records received and dropped, %d bytes",
		d.resetEvery, pairings, batches, received, used)
}
