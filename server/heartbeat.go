package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

// AppHeartbeat is the body of POST /v1/heartbeat (app 0.6.2): the app sends one at the end of
// each background run, also when it could not read Health (phone locked) or upload. It holds
// no health data, only what the app did and why, so a sync that stops can be diagnosed from
// the server's log and /metrics alone.
type AppHeartbeat struct {
	// Trigger is what woke the app, see Triggers.
	Trigger string `json:"trigger"`
	// AppState is "active", "inactive" or "background".
	AppState string `json:"app_state"`
	// ProtectedData is false while the phone is locked: Health cannot be read then.
	ProtectedData bool `json:"protected_data"`
	// Outcome of the run: "ok", "locked", "cancelled" (out of background time), "failed"...
	Outcome string `json:"outcome"`
	// Error is the run's error, LastError the last error the app saw before it (for example a
	// heartbeat that could not be sent).
	Error     string `json:"error,omitempty"`
	LastError string `json:"last_error,omitempty"`
	// Pending is the full sync still owed: "locked", "failed" or "".
	Pending  string  `json:"pending,omitempty"`
	Uploaded int     `json:"uploaded"`
	Seconds  float64 `json:"seconds"`
	// BackgroundTimeLeft is what iOS still allowed the app at the end, in seconds (-1: in the
	// foreground, unlimited).
	BackgroundTimeLeft float64 `json:"background_time_left"`
	// ProcessAge is how long the app's process has existed, in seconds: a small value after
	// a background wake means iOS launched it, a large one that it resumed it.
	ProcessAge int    `json:"process_age"`
	AppVersion string `json:"app_version"`
	// Wakes counts this process's wakes by trigger, with the last one's time.
	Wakes map[string]WakeCount `json:"wakes,omitempty"`
	// What iOS weighs before it wakes the app (app 0.6.4): Battery is "charging", "full",
	// "battery" (unplugged) or "unknown"; BackgroundRefresh is "available", "denied" (Background
	// App Refresh off for the app) or "restricted". Absent from older apps.
	Battery           string `json:"battery,omitempty"`
	LowPowerMode      bool   `json:"low_power_mode,omitempty"`
	BackgroundRefresh string `json:"background_refresh,omitempty"`
}

// power describes AppHeartbeat's power fields for the log line, "" when the app sent none.
func (hb AppHeartbeat) power() string {
	var parts []string
	switch hb.Battery {
	case "":
	case "charging", "full":
		parts = append(parts, "plugged in")
	case "battery":
		parts = append(parts, "on battery")
	default:
		parts = append(parts, "power "+clean(hb.Battery, 20))
	}
	if hb.LowPowerMode {
		parts = append(parts, "low power mode")
	}
	if hb.BackgroundRefresh != "" && hb.BackgroundRefresh != "available" {
		parts = append(parts, "background app refresh "+clean(hb.BackgroundRefresh, 20))
	}
	return strings.Join(parts, ", ")
}

// WakeCount is one trigger's line in AppHeartbeat.Wakes.
type WakeCount struct {
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

func knownTrigger(t string) string {
	for _, k := range Triggers {
		if t == k {
			return t
		}
	}
	return "unknown"
}

// clean makes app-supplied text safe for one log line: printable, at most n bytes.
func clean(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "") + "…"
	}
	return s
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorize(w, r, ScopeUpload)
	if !ok {
		return
	}
	var hb AppHeartbeat
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&hb); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	trigger := knownTrigger(hb.Trigger)
	now := time.Now()
	s.mu.Lock()
	if s.lastHeartbeat == nil {
		s.lastHeartbeat = map[string]time.Time{}
	}
	s.lastHeartbeat[trigger] = now
	s.mu.Unlock()
	s.add("heartbeats_"+trigger, 1)

	if s.Demo != nil {
		// A demo server logs no app details; the count above is all it keeps.
		writeJSON(w, http.StatusOK, s.handoff())
		return
	}
	device := "admin token"
	if c.Device != nil {
		device = "device " + c.Device.ID
	}
	var b strings.Builder
	fmt.Fprintf(&b, "heartbeat: %s, app %s, trigger %s, %s, protected data %s, outcome %s",
		device, clean(hb.AppVersion, 40), trigger, clean(cmpOr(hb.AppState, "unknown"), 20),
		map[bool]string{true: "available", false: "unavailable"}[hb.ProtectedData], clean(cmpOr(hb.Outcome, "unknown"), 40))
	if p := hb.power(); p != "" {
		fmt.Fprintf(&b, ", %s", p)
	}
	fmt.Fprintf(&b, ", %d uploaded in %.1fs", hb.Uploaded, hb.Seconds)
	if hb.BackgroundTimeLeft >= 0 {
		fmt.Fprintf(&b, ", %.0fs background time left", hb.BackgroundTimeLeft)
	}
	if hb.Pending != "" {
		fmt.Fprintf(&b, ", owes a sync (%s)", clean(hb.Pending, 20))
	}
	if hb.Error != "" {
		fmt.Fprintf(&b, ", error %q", clean(hb.Error, 300))
	}
	if hb.LastError != "" {
		fmt.Fprintf(&b, ", last error %q", clean(hb.LastError, 300))
	}
	fmt.Fprintf(&b, ", process %s old", (time.Duration(hb.ProcessAge) * time.Second).String())
	if len(hb.Wakes) > 0 {
		keys := make([]string, 0, len(hb.Wakes))
		for k := range hb.Wakes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys[:min(len(keys), 10)] {
			wc := hb.Wakes[k]
			parts = append(parts, fmt.Sprintf("%s %d (last %s)", clean(k, 20), wc.Count, wc.Last.Local().Format("01-02 15:04:05")))
		}
		fmt.Fprintf(&b, "; wakes: %s", strings.Join(parts, ", "))
	}
	log.Print(b.String())
	// The answer tells the app where the server prefers to be reached (identity.go); apps
	// before 1.0 read only the status code.
	writeJSON(w, http.StatusOK, s.handoff())
}

// refusalLogEvery bounds how often refused tokens are logged, so a client retrying in a loop
// (or someone guessing) cannot flood the log; the lines in between are counted.
const refusalLogEvery = 10 * time.Second

// logRefusal logs a request refused for its token: what was asked, from where, by what
// client, and which token (a short prefix of its SHA-256, enough to tell two tokens apart and
// to match a token one holds; nothing to guess it from).
func (s *Server) logRefusal(r *http.Request, why string) {
	if s.Demo != nil {
		return // a demo server logs no addresses or tokens; the counters keep the totals
	}
	s.mu.Lock()
	now := time.Now()
	if now.Sub(s.refusalLog) < refusalLogEvery {
		s.refusalsQuiet++
		s.mu.Unlock()
		return
	}
	quiet := s.refusalsQuiet
	s.refusalLog, s.refusalsQuiet = now, 0
	s.mu.Unlock()

	from := "unknown address"
	if a, ok := clientAddr(r); ok {
		from = a.String()
	}
	token := "no bearer token"
	if t := bearer(r); t != "" {
		sum := sha256.Sum256([]byte(t))
		token = "token sha256 " + hex.EncodeToString(sum[:3]) + "…"
	}
	more := ""
	if quiet > 0 {
		more = fmt.Sprintf(" (%d more refused since the last such line)", quiet)
	}
	log.Printf("refused: %s %s from %s, %s, %s, user agent %q%s", r.Method, r.URL.Path, from, why, token,
		clean(r.UserAgent(), 200), more)
}
