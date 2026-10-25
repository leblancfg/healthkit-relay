package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Demo mode (-demo-code-file): a public server for trying the app, such as the one App Review
// uses. Pairing works with one shared setup code, any number of times; every -demo-reset
// (an hour by default) the server erases all records and revokes devices unseen for a week;
// and it accepts at most -demo-quota bytes of uploads between two resets, answering 429 with
// Retry-After beyond that. The app retries later on its own.
type Demo struct {
	codeHash   string // SHA-256 of the normalized shared code
	resetEvery time.Duration
	quota      int64

	mu        sync.Mutex
	used      int64
	nextReset time.Time
}

// DemoDeviceTTL is how long a device on a demo server may go unseen before it is revoked.
const DemoDeviceTTL = 7 * 24 * time.Hour

// minDemoCode is the shortest shared code accepted: 16 characters of A-Z2-7, 80 bits.
const minDemoCode = 16

// NewDemo checks the shared code and returns the demo settings.
func NewDemo(code string, resetEvery time.Duration, quota int64) (*Demo, error) {
	code = normalizeCode(code)
	if len(code) < minDemoCode {
		return nil, fmt.Errorf("the demo setup code must be at least %d characters (letters A-Z and digits 2-7 make it easy to type)", minDemoCode)
	}
	if resetEvery < time.Minute {
		return nil, errors.New("-demo-reset must be at least a minute")
	}
	return &Demo{codeHash: hashToken(code), resetEvery: resetEvery, quota: quota, nextReset: time.Now().Add(resetEvery)}, nil
}

// isCode tells whether code is the shared demo code, in constant time.
func (d *Demo) isCode(code string) bool {
	if d == nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hashToken(normalizeCode(code))), []byte(d.codeHash)) == 1
}

// admit tells whether uploads are still accepted, and when not, how long until the next
// reset. Bytes count as they are read, so the upload that crosses the quota still goes in.
func (d *Demo) admit() (time.Duration, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.used < d.quota {
		return 0, true
	}
	return max(time.Until(d.nextReset), time.Second), false
}

func (d *Demo) use(n int64) {
	d.mu.Lock()
	d.used += n
	d.mu.Unlock()
}

// countingReader counts the bytes of an upload toward the demo quota.
type countingReader struct {
	r io.ReadCloser
	d *Demo
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.d.use(int64(n))
	return n, err
}

func (c countingReader) Close() error { return c.r.Close() }

// demoAdmit answers 429 when the demo quota is used up; ok is false when it has answered.
func (s *Server) demoAdmit(w http.ResponseWriter, r *http.Request) bool {
	if s.Demo == nil {
		return true
	}
	wait, ok := s.Demo.admit()
	if !ok {
		s.count("demo_quota")
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		httpError(w, http.StatusTooManyRequests, fmt.Sprintf("this demo server accepts %d MB of uploads per %s: try again in %s",
			s.Demo.quota>>20, s.Demo.resetEvery, wait.Round(time.Minute)))
		return false
	}
	r.Body = countingReader{r.Body, s.Demo}
	return true
}

// runDemoResets erases the demo server's data every resetEvery until ctx ends.
func (s *Server) runDemoResets(ctx context.Context) {
	t := time.NewTicker(s.Demo.resetEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.demoReset(ctx)
		}
	}
}

// demoReset erases every record, revokes devices unseen for DemoDeviceTTL and starts a new
// quota period.
func (s *Server) demoReset(ctx context.Context) {
	start := time.Now()
	uploaders, err := s.Archive.Uploaders()
	if err != nil {
		log.Printf("demo reset: %v", err)
	}
	var erased int64
	for _, u := range uploaders {
		n, err := s.Archive.Erase(ctx, u)
		if err != nil {
			log.Printf("demo reset: erase %s: %v", uploaderName(u), err)
			continue
		}
		erased += n
	}
	revoked := 0
	for _, dev := range s.Devices.List() {
		if start.Sub(dev.LastSeen) > DemoDeviceTTL {
			if ok, err := s.Devices.Revoke(dev.ID); err != nil {
				log.Printf("demo reset: revoke %s: %v", dev.ID, err)
			} else if ok {
				revoked++
			}
		}
	}
	s.Demo.mu.Lock()
	used := s.Demo.used
	s.Demo.used, s.Demo.nextReset = 0, start.Add(s.Demo.resetEvery)
	s.Demo.mu.Unlock()
	log.Printf("demo reset: erased %d records of %d uploaders, revoked %d devices unseen for %s, %d bytes uploaded since the last reset, in %s",
		erased, len(uploaders), revoked, DemoDeviceTTL, used, time.Since(start).Round(time.Millisecond))
}
