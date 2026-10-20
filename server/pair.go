package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Pairing. Whoever holds the admin token (admin.go) mints one-time setup codes (the setup
// page, or `healthkit-relay pair` on the server). The app exchanges a code for a token of its
// own; the server keeps only its SHA-256, in <archive-dir>/devices.json, and can revoke it.
//
// Each stored token has a scope: "upload" (paired apps: upload, status, erase their data) or
// "read" (query clients: the query API only). Entries without one, from before scopes, are
// upload tokens. The admin token manages pairing and tokens, may use the query API and erase
// any device's data. Uploads with it are deprecated (adminUpload): this release still
// accepts them, for apps set up by hand with the server token of earlier versions.

// Token scopes.
const (
	ScopeUpload = "upload"
	ScopeRead   = "read"
)

// Device is a paired app.
type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Model     string    `json:"model,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen_at,omitempty"`
	TokenHash string    `json:"token_sha256,omitempty"`
	// Scope is ScopeUpload or ScopeRead; empty (devices.json before scopes) is ScopeUpload.
	Scope string `json:"scope,omitempty"`
}

// scope is the device's scope, ScopeUpload when unset.
func (dev *Device) scope() string { return cmpOr(dev.Scope, ScopeUpload) }

// Devices is the list of paired apps, persisted as JSON. It rereads the file when another
// process (a subcommand) changed it.
type Devices struct {
	path string
	now  func() time.Time

	mu     sync.Mutex
	list   []*Device
	byHash map[string]*Device
	stamp  stamp
}

// OpenDevices loads the device list at path; a missing file is an empty list.
func OpenDevices(path string) (*Devices, error) {
	d := &Devices{path: path, now: time.Now, byHash: map[string]*Device{}}
	if err := d.load(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Devices) load() error {
	d.stamp.set(d.path)
	b, err := os.ReadFile(d.path)
	d.list, d.byHash = nil, map[string]*Device{}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var file struct{ Devices []*Device }
	if err := json.Unmarshal(b, &file); err != nil {
		return fmt.Errorf("%s: %w", d.path, err)
	}
	for _, dev := range file.Devices {
		d.list = append(d.list, dev)
		d.byHash[dev.TokenHash] = dev
	}
	return nil
}

// refresh reads the file again if it changed on disk; d.mu is held.
func (d *Devices) refresh() {
	if changed, _ := d.stamp.changed(d.path); changed {
		if err := d.load(); err != nil {
			log.Printf("devices: %v", err)
		}
	}
}

// update runs fn on the list as it is on disk, under the archive's lock, and saves the list
// if fn returns true. When saving fails, the list is read again from disk. d.mu is held.
func (d *Devices) update(fn func() bool) error {
	unlock, err := lockState(d.path)
	if err != nil {
		return err
	}
	defer unlock()
	d.refresh()
	if !fn() {
		return nil
	}
	if err := d.save(); err != nil {
		d.stamp = stamp{}
		d.load()
		return err
	}
	return nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Add pairs a new device and returns its token, which is not stored.
func (d *Devices) Add(name, model string) (string, Device, error) {
	return d.AddScoped(name, model, ScopeUpload)
}

// AddScoped adds a token with the given scope and returns it; only its hash is stored.
func (d *Devices) AddScoped(name, model, scope string) (string, Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	token := randomString(32)
	var dev *Device
	err := d.update(func() bool {
		dev = &Device{Name: name, Model: model, CreatedAt: d.now().UTC().Truncate(time.Second), TokenHash: hashToken(token), Scope: scope}
		dev.LastSeen = dev.CreatedAt
		for dev.ID == "" || d.find(dev.ID) >= 0 {
			b := make([]byte, 4)
			rand.Read(b)
			dev.ID = hex.EncodeToString(b)
		}
		d.list = append(d.list, dev)
		d.byHash[dev.TokenHash] = dev
		return true
	})
	if err != nil {
		return "", Device{}, err
	}
	return token, dev.public(), nil
}

// Rekey gives the device a token belongs to a new token, name and model, keeping its ID (and
// so the records it uploaded); the old token stops working. ok is false for an unknown token.
func (d *Devices) Rekey(oldToken, name, model string) (token string, dev Device, ok bool, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	err = d.update(func() bool {
		cur, found := d.byHash[hashToken(oldToken)]
		if !found || cur.scope() != ScopeUpload {
			return false // a read token is never turned into an app's
		}
		ok = true
		token = randomString(32)
		delete(d.byHash, cur.TokenHash)
		cur.Name, cur.Model, cur.TokenHash = name, model, hashToken(token)
		cur.LastSeen = d.now().UTC().Truncate(time.Second)
		d.byHash[cur.TokenHash] = cur
		dev = cur.public()
		return true
	})
	if err != nil || !ok {
		return "", Device{}, ok, err
	}
	return token, dev, true, nil
}

func (d *Devices) find(id string) int {
	for i, dev := range d.list {
		if dev.ID == id {
			return i
		}
	}
	return -1
}

// Revoke removes a device; its token stops working at once.
func (d *Devices) Revoke(id string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	found := false
	err := d.update(func() bool {
		i := d.find(id)
		if i < 0 {
			return false
		}
		found = true
		dev := d.list[i]
		d.list = append(d.list[:i:i], d.list[i+1:]...)
		delete(d.byHash, dev.TokenHash)
		return true
	})
	return found, err
}

// Lookup returns the device a token belongs to, and records that it was seen (to the
// nearest ten minutes, to keep writes rare).
func (d *Devices) Lookup(token string) (Device, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.refresh()
	h := hashToken(token)
	dev, ok := d.byHash[h]
	if !ok {
		return Device{}, false
	}
	if now := d.now().UTC(); now.Sub(dev.LastSeen) > 10*time.Minute {
		// A lost last-seen time is not worth failing an upload for.
		_ = d.update(func() bool {
			if dev, ok = d.byHash[h]; ok {
				dev.LastSeen = now.Truncate(time.Second)
			}
			return ok
		})
		if !ok {
			return Device{}, false // revoked meanwhile
		}
	}
	return dev.public(), true
}

// List returns the devices, oldest first, without token hashes.
func (d *Devices) List() []Device {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.refresh()
	out := make([]Device, 0, len(d.list))
	for _, dev := range d.list {
		out = append(out, dev.public())
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (d *Devices) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.refresh()
	return len(d.list)
}

func (dev *Device) public() Device {
	c := *dev
	c.TokenHash = ""
	c.Scope = dev.scope()
	return c
}

func (d *Devices) save() error {
	b, err := json.MarshalIndent(struct {
		Devices []*Device `json:"devices"`
	}{d.list}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(d.path, append(b, '\n')); err != nil {
		return err
	}
	d.stamp.set(d.path)
	return nil
}

// Setup codes: single use, valid for CodeTTL (or what `pair -valid` asks, up to MaxCodeTTL).
// The server keeps their hashes in <archive-dir>/codes.json, where `healthkit-relay pair`
// adds the codes it prints.

const (
	CodeTTL    = 15 * time.Minute
	MaxCodeTTL = 24 * time.Hour
)

type setupCode struct {
	Hash    string    `json:"code_sha256"`
	Minted  time.Time `json:"minted_at"`
	Expires time.Time `json:"expires_at"`
	Device  *Device   `json:"device,omitempty"` // set once used, without its token hash
}

// Codes holds the setup codes not yet expired.
type Codes struct {
	path string // "": in memory only
	now  func() time.Time

	mu    sync.Mutex
	m     map[string]*setupCode // by SHA-256 of the code
	stamp stamp
}

// NewCodes holds codes in memory only.
func NewCodes() *Codes { return &Codes{now: time.Now, m: map[string]*setupCode{}} }

// OpenCodes holds codes in the file at path (a missing file has none).
func OpenCodes(path string) (*Codes, error) {
	c := &Codes{path: path, now: time.Now, m: map[string]*setupCode{}}
	return c, c.load()
}

func (c *Codes) load() error {
	c.stamp.set(c.path)
	c.m = map[string]*setupCode{}
	b, err := os.ReadFile(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var file struct{ Codes []*setupCode }
	if err := json.Unmarshal(b, &file); err != nil {
		return fmt.Errorf("%s: %w", c.path, err)
	}
	for _, sc := range file.Codes {
		c.m[sc.Hash] = sc
	}
	return nil
}

func (c *Codes) save() error {
	list := make([]*setupCode, 0, len(c.m))
	for _, sc := range c.m {
		list = append(list, sc)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Minted.Before(list[j].Minted) })
	b, err := json.MarshalIndent(struct {
		Codes []*setupCode `json:"codes"`
	}{list}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(c.path, append(b, '\n')); err != nil {
		return err
	}
	c.stamp.set(c.path)
	return nil
}

// update runs fn on the codes as they are on disk, after pruning expired ones, under the
// file's lock, and saves them if anything changed. c.mu is held.
func (c *Codes) update(fn func(now time.Time) (changed bool, err error)) error {
	if c.path == "" {
		now := c.now()
		c.prune(now)
		_, err := fn(now)
		return err
	}
	unlock, err := lockState(c.path)
	if err != nil {
		return err
	}
	defer unlock()
	c.refresh()
	now := c.now()
	pruned := c.prune(now)
	changed, err := fn(now)
	if err != nil {
		return err
	}
	if changed || pruned {
		if err := c.save(); err != nil {
			c.load()
			return err
		}
	}
	return nil
}

func (c *Codes) refresh() {
	if c.path == "" {
		return
	}
	if changed, _ := c.stamp.changed(c.path); changed {
		if err := c.load(); err != nil {
			log.Printf("setup codes: %v", err)
		}
	}
}

var codeEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Mint returns a new code (120 random bits, 24 characters of A-Z2-7).
func (c *Codes) Mint() (string, time.Time, error) { return c.MintFor(CodeTTL) }

// MintFor returns a code valid for ttl, clamped to 1 minute..MaxCodeTTL.
func (c *Codes) MintFor(ttl time.Duration) (string, time.Time, error) {
	ttl = min(max(ttl, time.Minute), MaxCodeTTL)
	b := make([]byte, 15)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	code := codeEncoding.EncodeToString(b)
	c.mu.Lock()
	defer c.mu.Unlock()
	var exp time.Time
	err := c.update(func(now time.Time) (bool, error) {
		// Bound the number of live codes: a setup page left open and reloaded mints one each time.
		for len(c.m) >= 50 {
			var oldest string
			for k, v := range c.m {
				if oldest == "" || v.Minted.Before(c.m[oldest].Minted) {
					oldest = k
				}
			}
			delete(c.m, oldest)
		}
		exp = now.Add(ttl)
		h := hashToken(code)
		c.m[h] = &setupCode{Hash: h, Minted: now.UTC(), Expires: exp.UTC()}
		return true, nil
	})
	return code, exp, err
}

// prune drops expired codes and tells whether there were any.
func (c *Codes) prune(now time.Time) bool {
	n := len(c.m)
	for k, v := range c.m {
		if now.After(v.Expires) {
			delete(c.m, k)
		}
	}
	return len(c.m) != n
}

func normalizeCode(code string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
}

// Redeem marks a code used by the device pair() creates, if the code is valid; the code
// stays known (as used) until it expires, for the setup page to show the result.
func (c *Codes) Redeem(code string, pair func() (Device, error)) (Device, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var dev Device
	err := c.update(func(time.Time) (bool, error) {
		sc, ok := c.m[hashToken(normalizeCode(code))]
		if !ok || sc.Device != nil {
			return false, errBadCode
		}
		d, err := pair()
		if err != nil {
			return false, err
		}
		dev, sc.Device = d, &d
		return true, nil
	})
	return dev, err
}

var errBadCode = errors.New("setup code unknown, expired or already used")

// badCodeMessage answers POST /v1/pair with an unknown, expired or used code; the app shows it.
const badCodeMessage = "This setup code expired or was already used. Get a new one from the server's setup page, " +
	"or run healthkit-relay pair on the server."

// State is "pending", "paired" (with the device) or "invalid".
func (c *Codes) State(code string) (string, *Device, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refresh()
	sc, ok := c.m[hashToken(normalizeCode(code))]
	switch {
	case !ok || c.now().After(sc.Expires):
		return "invalid", nil, time.Time{}
	case sc.Device != nil:
		d := *sc.Device
		return "paired", &d, sc.Expires
	default:
		return "pending", nil, sc.Expires
	}
}

// AppScheme is the URL scheme the Health Relay app registers.
const AppScheme = "healthrelay"

// PairLink is the URL that sets up the app in one tap: healthrelay://pair?server=…&code=….
func PairLink(server, code string) string {
	v := url.Values{"server": {server}, "code": {code}}
	return AppScheme + "://pair?" + v.Encode()
}

// Caller is who sent a request: the admin (admin token) or a paired device.
type Caller struct {
	Admin  bool
	Device *Device
}

// Allows tells whether the caller may use what needs scope: the admin may use everything,
// a stored token what its own scope covers.
func (c Caller) Allows(scope string) bool {
	return c.Admin || (c.Device != nil && c.Device.scope() == scope)
}

func bearer(r *http.Request) string {
	got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return strings.TrimSpace(got)
}

func (s *Server) isAdminToken(token string) bool { return s.AdminKey.Check(token) }

// adminUpload counts an upload-scope request made with the admin token, and logs one at most
// hourly. Apps set up by hand with the server token of earlier versions upload this way;
// this release still accepts it.
func (s *Server) adminUpload(c Caller, r *http.Request) {
	if !c.Admin {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stats == nil {
		s.stats = make(map[string]int64)
	}
	s.stats["admin_token_uploads"]++
	if now := time.Now(); now.Sub(s.adminUploadsLog) >= time.Hour {
		s.adminUploadsLog = now
		log.Printf("WARNING: %s %s used the admin token (%d such requests since start). Uploads with the admin token are deprecated "+
			"and will stop working in a future release: pair the iPhone again from the setup page (/setup), and it gets a token of its own.",
			r.Method, r.URL.Path, s.stats["admin_token_uploads"])
	}
}

// caller authenticates a request; ok is false without a valid token.
func (s *Server) caller(r *http.Request) (Caller, bool) {
	token := bearer(r)
	if token == "" {
		return Caller{}, false
	}
	if s.isAdminToken(token) {
		return Caller{Admin: true}, true
	}
	if s.Devices != nil {
		if dev, ok := s.Devices.Lookup(token); ok {
			return Caller{Device: &dev}, true
		}
	}
	return Caller{}, false
}

// authorize authenticates a request and checks its scope: without a valid token it answers
// 401, with a token of another scope 403. ok is false when it has answered.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, scope string) (Caller, bool) {
	c, ok := s.caller(r)
	if !ok {
		s.unauthorized(w, r)
		return Caller{}, false
	}
	if !c.Allows(scope) {
		s.count("forbidden")
		s.logRefusal(r, "403 (token of another scope)")
		msg := "this is a paired device's token, which only uploads: the query API needs a read token " +
			"(create one on the setup page, or with healthkit-relay token create -name grafana)"
		if scope == ScopeUpload {
			msg = "this is a read token, which only reads through the query API: uploads need a paired device's token"
		}
		httpError(w, http.StatusForbidden, msg)
		return Caller{}, false
	}
	return c, true
}

func (s *Server) unauthorized(w http.ResponseWriter, r *http.Request) {
	s.count("unauthorized")
	s.logRefusal(r, "401")
	w.Header().Set("WWW-Authenticate", "Bearer")
	httpError(w, http.StatusUnauthorized, "unauthorized")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// PairRequest is the body of POST /v1/pair.
type PairRequest struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Model string `json:"model"`
	// Replaces is the token the app used so far, if it was paired before: the device keeps its
	// ID (and the records it uploaded, which an erase finds by that ID) with a new token, so
	// re-pairing leaves no stale entry.
	Replaces string `json:"replaces,omitempty"`
}

// PairResponse answers POST /v1/pair.
type PairResponse struct {
	Token  string `json:"token"`
	Device Device `json:"device"`
	Server Status `json:"server"`
}

// Status answers GET /v1/status.
type Status struct {
	Version     string  `json:"version"`
	WireVersion int     `json:"wire_version"`
	Records     int     `json:"records"`
	Device      *Device `json:"device,omitempty"`
}

func (s *Server) status(dev *Device) Status {
	return Status{Version: version, WireVersion: WireVersion, Records: s.Archive.Len(), Device: dev}
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	if !s.setupAllowed(r) {
		// The app tells this answer apart by "private networks".
		msg := "This server pairs iPhones only on private networks: its local network or Tailscale. " +
			"Connect the iPhone to the same network as the server, then try again."
		if s.SetupFrom == "off" {
			msg = "Pairing is turned off on this server (-setup-from off)."
		}
		httpError(w, http.StatusForbidden, msg)
		return
	}
	var req PairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "iPhone"
	}
	if len(name) > 100 || len(req.Model) > 100 {
		httpError(w, http.StatusBadRequest, "name and model must be at most 100 characters each")
		return
	}
	// Names end up in logs, the setup page and `healthkit-relay devices` in a terminal: no
	// escape sequences or line breaks from whoever holds a code.
	if !printable(name) || !printable(req.Model) {
		httpError(w, http.StatusBadRequest, "name and model must be printable: line breaks, tabs and other control characters are refused")
		return
	}
	var token string
	model := strings.TrimSpace(req.Model)
	dev, err := s.Codes.Redeem(req.Code, func() (Device, error) {
		if req.Replaces != "" && !s.isAdminToken(req.Replaces) {
			t, d, ok, err := s.Devices.Rekey(req.Replaces, name, model)
			if ok {
				token = t
				return d, err
			}
		}
		t, d, err := s.Devices.Add(name, model)
		token = t
		return d, err
	})
	if errors.Is(err, errBadCode) {
		s.count("pair_rejected")
		httpError(w, http.StatusForbidden, badCodeMessage)
		return
	} else if err != nil {
		log.Printf("pair: %v", err) // names server paths: not for an unauthenticated caller
		httpError(w, http.StatusInternalServerError, "the server could not save the paired device: its log says why")
		return
	}
	s.count("paired")
	log.Printf("paired device %s (%s, %s)", dev.ID, dev.Name, dev.Model)
	writeJSON(w, http.StatusOK, PairResponse{Token: token, Device: dev, Server: s.status(&dev)})
}

func printable(s string) bool {
	return !strings.ContainsFunc(s, unicode.IsControl) // JSON decoding already made it valid UTF-8
}

func (s *Server) statusHandler(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorize(w, r, ScopeUpload)
	if !ok {
		return
	}
	s.adminUpload(c, r)
	writeJSON(w, http.StatusOK, s.status(c.Device))
}

// CodeResponse answers POST /v1/pairing-codes.
type CodeResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
	// Link sets up the app in one tap (or as a QR code); SetupURL is the page showing both.
	// Both are empty when no address a phone could reach is known (see address.go).
	Link     string `json:"link,omitempty"`
	SetupURL string `json:"setup_url,omitempty"`
	// Addresses found when -public-url is not set; Link uses the first.
	Addresses []Address `json:"addresses,omitempty"`
}

func (s *Server) mintCode(publicURL string) (CodeResponse, error) {
	return s.mintCodeFor(publicURL, CodeTTL)
}

func (s *Server) mintCodeFor(publicURL string, ttl time.Duration) (CodeResponse, error) {
	code, exp, err := s.Codes.MintFor(ttl)
	if err != nil {
		return CodeResponse{}, err
	}
	c := CodeResponse{Code: code, ExpiresAt: exp.UTC().Truncate(time.Second)}
	if publicURL != "" {
		c.Link, c.SetupURL = PairLink(publicURL, code), publicURL+"/setup?code="+code
	}
	return c, nil
}

func (s *Server) pairingCodes(w http.ResponseWriter, r *http.Request) {
	if c, ok := s.caller(r); !ok || !c.Admin {
		s.unauthorized(w, r)
		return
	}
	var req struct {
		PublicURL string `json:"public_url"`
		// How long the code works, in seconds (default 15 minutes, at most a day).
		ValidSeconds int `json:"valid_seconds"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) // the body is optional
	var found []Address
	public := strings.TrimRight(s.PublicURL, "/")
	if public == "" && req.PublicURL != "" {
		public = strings.TrimRight(req.PublicURL, "/")
	}
	if public == "" {
		if found = s.addresses(r); len(found) > 0 {
			public = found[0].URL
		}
	}
	ttl := CodeTTL
	if req.ValidSeconds > 0 {
		ttl = time.Duration(req.ValidSeconds) * time.Second
	}
	c, err := s.mintCodeFor(public, ttl)
	if err != nil {
		log.Printf("pairing codes: %v", err)
		httpError(w, http.StatusInternalServerError, "the server could not save the setup code: its log says why")
		return
	}
	c.Addresses = found
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) devicesList(w http.ResponseWriter, r *http.Request) {
	if c, ok := s.caller(r); !ok || !c.Admin {
		s.unauthorized(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]Device{"devices": s.Devices.List()})
}

// TokenRequest is the body of POST /v1/tokens.
type TokenRequest struct {
	Name  string `json:"name"`
	Scope string `json:"scope"` // ScopeRead (default) or ScopeUpload
}

// TokenResponse answers POST /v1/tokens. The token is shown this once; only its hash is kept.
type TokenResponse struct {
	Token  string `json:"token"`
	Device Device `json:"device"`
}

// createToken mints a token for a client that is not the app (a dashboard, an agent): read
// tokens use the query API only. It is listed and revoked with the paired devices.
func (s *Server) createToken(name, scope string) (TokenResponse, int, error) {
	name = strings.TrimSpace(name)
	scope = cmpOr(scope, ScopeRead)
	switch {
	case scope != ScopeRead && scope != ScopeUpload:
		return TokenResponse{}, http.StatusBadRequest, fmt.Errorf("scope must be %q or %q", ScopeRead, ScopeUpload)
	case name == "":
		return TokenResponse{}, http.StatusBadRequest, errors.New("a name is required: what the token is for, e.g. grafana")
	case len(name) > 100 || !printable(name):
		return TokenResponse{}, http.StatusBadRequest, errors.New("the name must be at most 100 characters, without line breaks or tabs")
	}
	token, dev, err := s.Devices.AddScoped(name, "", scope)
	if err != nil {
		log.Printf("tokens: %v", err)
		return TokenResponse{}, http.StatusInternalServerError, errors.New("the server could not save the token: its log says why")
	}
	log.Printf("created %s token %s (%s)", scope, dev.ID, dev.Name)
	return TokenResponse{Token: token, Device: dev}, http.StatusCreated, nil
}

func (s *Server) tokensCreate(w http.ResponseWriter, r *http.Request) {
	if c, ok := s.caller(r); !ok || !c.Admin {
		s.unauthorized(w, r)
		return
	}
	var req TokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	resp, code, err := s.createToken(req.Name, req.Scope)
	if err != nil {
		httpError(w, code, err.Error())
		return
	}
	writeJSON(w, code, resp)
}

func (s *Server) deviceRevoke(w http.ResponseWriter, r *http.Request) {
	if c, ok := s.caller(r); !ok || !c.Admin {
		s.unauthorized(w, r)
		return
	}
	ok, err := s.Devices.Revoke(r.PathValue("id"))
	switch {
	case err != nil:
		httpError(w, http.StatusInternalServerError, err.Error())
	case !ok:
		httpError(w, http.StatusNotFound, "no paired device or read token has this ID: GET /v1/devices lists them")
	default:
		log.Printf("revoked device %s", r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	}
}
