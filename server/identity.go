package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Server identity and address handoff. Each archive has a random server ID, made on first
// start and kept in <archive-dir>/server.json:
//
//	{"server_id": "5f0c2a...", "created_at": "2026-10-25T12:00:00Z"}
//
// It names the archive, not the machine or the admin token: an admin reset keeps it, and an
// archive moved to another machine keeps it too. The app learns it from GET /v1/status and
// each heartbeat's answer, together with the server's preferred address (-public-url, when it
// is https). When the app reaches the server at another address, it checks that the
// preferred one answers GET /v1/server with the same ID over HTTPS, and that its token works
// there, then uses that address from then on.

const serverFile = "server.json"

type serverRecord struct {
	ServerID  string    `json:"server_id"`
	CreatedAt time.Time `json:"created_at"`
}

// loadServerID reads dir/server.json, making it on first start.
func loadServerID(dir string) (string, error) {
	path := filepath.Join(dir, serverFile)
	unlock, err := lockState(path)
	if err != nil {
		return "", err
	}
	defer unlock()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			panic(err) // crypto/rand does not fail on supported platforms
		}
		rec := serverRecord{ServerID: hex.EncodeToString(id), CreatedAt: time.Now().UTC().Truncate(time.Second)}
		out, _ := json.MarshalIndent(rec, "", "  ")
		return rec.ServerID, writeFileAtomic(path, append(out, '\n'))
	} else if err != nil {
		return "", err
	}
	var rec serverRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if len(rec.ServerID) < 16 {
		return "", fmt.Errorf("%s: server_id is missing or too short", path)
	}
	return rec.ServerID, nil
}

// preferredURL is the address the server asks paired apps to use: -public-url, when it is
// https (the app moves only to an https address).
func (s *Server) preferredURL() string {
	u, err := url.Parse(s.PublicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return strings.TrimRight(s.PublicURL, "/")
}

// ServerInfo answers GET /v1/server, without a token: enough for the app to tell whether an
// address reaches this server before it sends its token there.
type ServerInfo struct {
	ServerID    string `json:"server_id"`
	WireVersion int    `json:"wire_version"`
}

func (s *Server) serverInfo(w http.ResponseWriter, r *http.Request) {
	if s.ID == "" {
		httpError(w, http.StatusNotFound, "this server has no server ID")
		return
	}
	writeJSON(w, http.StatusOK, ServerInfo{ServerID: s.ID, WireVersion: WireVersion})
}

// Handoff is the part of GET /v1/status and of a heartbeat's answer that lets the app move to
// the server's preferred address.
type Handoff struct {
	ServerID     string `json:"server_id,omitempty"`
	PreferredURL string `json:"preferred_url,omitempty"`
}

func (s *Server) handoff() Handoff {
	return Handoff{ServerID: s.ID, PreferredURL: s.preferredURL()}
}
