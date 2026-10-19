package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// The setup page, /setup: a QR code and a link that set up the app in one tap. It never
// shows anything to a visitor without either the server token (asked by a form, then kept
// in a session cookie for an hour) or a setup code minted by `healthkit-relay pair`. By
// default it, and pairing, only answer clients on private networks (loopback, RFC 1918,
// unique local and link-local addresses, and 100.64.0.0/10, which Tailscale uses).

const sessionCookie = "hkr_setup"

type sessions struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func (ss *sessions) add() string {
	b := make([]byte, 32)
	rand.Read(b)
	id := hex.EncodeToString(b)
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.m == nil {
		ss.m = map[string]time.Time{}
	}
	now := time.Now()
	for k, exp := range ss.m {
		if now.After(exp) {
			delete(ss.m, k)
		}
	}
	ss.m[id] = now.Add(time.Hour)
	return id
}

func (ss *sessions) valid(id string) bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	exp, ok := ss.m[id]
	return ok && time.Now().Before(exp)
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// clientAddr is the request's source address. Behind a reverse proxy on the same host
// (Tailscale serve, Caddy...) it is the last X-Forwarded-For hop, the one that proxy added.
func clientAddr(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	addr = addr.Unmap()
	if addr.IsLoopback() {
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			hops := strings.Split(xff[len(xff)-1], ",")
			fwd, err := netip.ParseAddr(strings.TrimSpace(hops[len(hops)-1]))
			if err != nil {
				return netip.Addr{}, false
			}
			return fwd.Unmap(), true
		}
	}
	return addr, true
}

func isPrivate(a netip.Addr) bool {
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || cgnat.Contains(a)
}

// setupAllowed applies -setup-from: "private" (default), "any" or "off".
func (s *Server) setupAllowed(r *http.Request) bool {
	switch s.SetupFrom {
	case "any":
		return true
	case "off":
		return false
	}
	a, ok := clientAddr(r)
	return ok && isPrivate(a)
}

func fromLocalProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.Unmap().IsLoopback()
}

type setupView struct {
	Version string
	Nonce   string
	Mode    string // "login", "pair", "used"
	Message string
	Admin   bool
	Code    string
	Link    template.URL // a custom scheme, which html/template would otherwise blank
	QR      template.HTML
	Expires string
	Server  string
	Paired  *Device
	// Addresses to choose from when -public-url is not set; Chosen is the one in the code.
	Addresses  []Address
	Chosen     string
	Configured bool     // -public-url is set
	Devices    []Device // paired apps (upload tokens)
	Tokens     []Device // read tokens, for the query API
	NewToken   string   // a token just created, shown once
	NewName    string
	LoginErr   bool
}

func (s *Server) render(w http.ResponseWriter, code int, v setupView) {
	b := make([]byte, 16)
	rand.Read(b)
	v.Nonce = hex.EncodeToString(b)
	v.Version = version
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY") // frame-ancestors, for browsers without CSP 2
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; "+
		"script-src 'nonce-"+v.Nonce+"'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(code)
	if err := setupTemplate.Execute(w, v); err != nil {
		log.Printf("setup page: %v", err)
	}
}

func (s *Server) setupForbidden(w http.ResponseWriter) {
	http.Error(w, "The setup page only answers on private networks (see -setup-from).", http.StatusForbidden)
}

// setupForm checks a form post to the setup page and reads it. Browsers say where a request
// comes from in Sec-Fetch-Site: only the page itself may post. The session cookie is
// SameSite=Strict, but "site" ignores the port: without this check, any other web app on the
// same host or a sibling subdomain could revoke devices or sign the browser in. Requests without the header (curl, old browsers) pass.
func setupForm(w http.ResponseWriter, r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		http.Error(w, "Cross-origin request refused.", http.StatusForbidden)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form.", http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Server) admin(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && s.sessions.valid(c.Value)
}

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	if !s.setupAllowed(r) {
		s.setupForbidden(w)
		return
	}
	v := setupView{Admin: s.admin(r), Configured: s.PublicURL != ""}
	code := r.URL.Query().Get("code")
	if code == "" && !v.Admin {
		v.Mode = "login"
		s.render(w, http.StatusOK, v)
		return
	}
	var exp time.Time
	if code != "" {
		state, dev, e := s.Codes.State(code)
		switch state {
		case "paired":
			v.Mode, v.Paired = "used", dev
			s.render(w, http.StatusOK, v)
			return
		case "invalid":
			if !v.Admin {
				v.Mode = "login"
				v.Message = "This setup link has expired or was already used. Sign in with the server token, or run healthkit-relay pair again."
				s.render(w, http.StatusGone, v)
				return
			}
			code = ""
		default:
			exp = e
		}
	}
	// The address in the link: -public-url, or the one picked from what was found.
	found := s.addresses(r)
	if len(found) == 0 {
		v.Mode = "noaddress"
		v.Server = requestURL(r)
		s.render(w, http.StatusOK, v)
		return
	}
	public := found[0].URL
	for _, a := range found {
		if a.URL == r.URL.Query().Get("server") {
			public = a.URL
		}
	}
	if code == "" {
		c := s.mintCode(public)
		code, exp = c.Code, c.ExpiresAt
	}
	v.Server = strings.TrimPrefix(strings.TrimPrefix(public, "https://"), "http://")
	v.Chosen = public
	if !v.Configured && len(found) > 1 {
		v.Addresses = found
	}
	q, err := EncodeQR([]byte(PairLink(public, code)))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	v.Mode, v.Code, v.Link, v.QR = "pair", code, template.URL(PairLink(public, code)), template.HTML(q.SVG())
	v.Expires = exp.Local().Format("15:04")
	if v.Admin {
		v.Devices, v.Tokens = s.deviceLists()
	}
	s.render(w, http.StatusOK, v)
}

// deviceLists splits the stored tokens into paired apps and read tokens.
func (s *Server) deviceLists() (apps, read []Device) {
	for _, d := range s.Devices.List() {
		if d.Scope == ScopeRead {
			read = append(read, d)
		} else {
			apps = append(apps, d)
		}
	}
	return apps, read
}

// setupToken creates a read token from the setup page and shows it once.
func (s *Server) setupToken(w http.ResponseWriter, r *http.Request) {
	if !s.setupAllowed(r) {
		s.setupForbidden(w)
		return
	}
	if !setupForm(w, r) {
		return
	}
	if !s.admin(r) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	t, code, err := s.createToken(r.PostFormValue("name"), ScopeRead)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	s.render(w, http.StatusOK, setupView{Mode: "token", Admin: true, NewToken: t.Token, NewName: t.Device.Name})
}

func (s *Server) setupLogin(w http.ResponseWriter, r *http.Request) {
	if !s.setupAllowed(r) {
		s.setupForbidden(w)
		return
	}
	if !setupForm(w, r) {
		return
	}
	if !s.isAdminToken(strings.TrimSpace(r.PostFormValue("token"))) {
		time.Sleep(500 * time.Millisecond) // slows down guessing; tokens are long anyway
		s.count("unauthorized")
		s.render(w, http.StatusUnauthorized, setupView{Mode: "login", LoginErr: true,
			Message: "That is not this server's token."})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: s.sessions.add(), Path: "/setup", MaxAge: 3600,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: strings.HasPrefix(requestURL(r), "https://"),
	})
	http.Redirect(w, r, "/setup", http.StatusSeeOther)
}

func (s *Server) setupRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.setupAllowed(r) {
		s.setupForbidden(w)
		return
	}
	if !setupForm(w, r) {
		return
	}
	if !s.admin(r) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if ok, err := s.Devices.Revoke(r.PostFormValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if ok {
		log.Printf("revoked device %s from the setup page", r.PostFormValue("id"))
	}
	http.Redirect(w, r, "/setup", http.StatusSeeOther)
}

// setupStatus lets the setup page show when the phone has paired.
func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	if !s.setupAllowed(r) {
		s.setupForbidden(w)
		return
	}
	state, dev, _ := s.Codes.State(r.URL.Query().Get("code"))
	out := map[string]string{"state": state}
	if dev != nil {
		out["device"] = dev.Name
		out["model"] = dev.Model
	}
	writeJSON(w, http.StatusOK, out)
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case t.IsZero():
		return "never"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return t.Local().Format("Jan 2")
	}
}

var setupTemplate = template.Must(template.New("setup").Funcs(template.FuncMap{
	"ago":  ago,
	"date": func(t time.Time) string { return t.Local().Format("Jan 2, 2006") },
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="robots" content="noindex">
<meta name="color-scheme" content="light dark">
<title>Health Relay · Setup</title>
<style>
:root { --accent: #ff2d55; --bg: #f2f2f7; --card: #fff; --text: #1c1c1e; --muted: #6e6e73; --line: #e5e5ea; --ok: #34c759; }
@media (prefers-color-scheme: dark) { :root { --bg: #000; --card: #1c1c1e; --text: #f5f5f7; --muted: #98989d; --line: #38383a; } }
* { box-sizing: border-box; }
body { margin: 0; min-height: 100vh; background: var(--bg); color: var(--text); -webkit-font-smoothing: antialiased;
  font: 17px/1.45 -apple-system, BlinkMacSystemFont, "SF Pro Text", "Segoe UI", system-ui, sans-serif;
  display: flex; justify-content: center; padding: max(40px, env(safe-area-inset-top)) 16px 32px; }
main { width: 100%; max-width: 420px; }
header { display: flex; align-items: center; justify-content: center; gap: 10px; margin-bottom: 28px; }
header svg { width: 34px; height: 34px; }
header h1 { font-size: 24px; font-weight: 700; letter-spacing: -0.02em; margin: 0; }
.card { background: var(--card); border-radius: 22px; padding: 28px 24px; margin-bottom: 18px; text-align: center;
  box-shadow: 0 1px 2px rgba(0,0,0,.04), 0 8px 24px rgba(0,0,0,.05); }
h2 { font-size: 22px; font-weight: 700; letter-spacing: -0.01em; margin: 0 0 6px; }
.sub { color: var(--muted); margin: 0 0 22px; font-size: 15px; }
.qr { width: 236px; margin: 0 auto 18px; padding: 10px; background: #fff; border-radius: 16px; border: 1px solid var(--line); }
.qr svg { display: block; width: 100%; height: auto; }
.button { display: block; width: 100%; border: 0; border-radius: 14px; background: var(--accent); color: #fff;
  font: inherit; font-weight: 600; padding: 15px; text-decoration: none; cursor: pointer; }
.button:active { opacity: .8; }
.hint { color: var(--muted); font-size: 14px; margin: 14px 0 0; }
.status { display: inline-flex; align-items: center; gap: 8px; color: var(--muted); font-size: 14px; margin: 18px 0 0; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--accent); animation: pulse 1.6s ease-in-out infinite; }
@keyframes pulse { 50% { opacity: .25; } }
.phone { display: none; }
@media (pointer: coarse) { .phone { display: block; } .desktop { display: none; } }
span.desktop { display: inline; }
@media (pointer: coarse) { span.desktop { display: none; } }
.check { width: 64px; height: 64px; margin: 4px auto 14px; display: block; }
form.login { text-align: left; margin-top: 20px; }
label { display: block; font-size: 13px; font-weight: 600; color: var(--muted); text-transform: uppercase; letter-spacing: .04em; margin: 0 0 6px 4px; }
input[type=password] { width: 100%; font: inherit; padding: 13px 14px; border-radius: 12px; border: 1px solid var(--line);
  background: var(--bg); color: var(--text); margin-bottom: 14px; }
input[type=text] { font: inherit; font-size: 15px; padding: 8px 10px; border-radius: 10px; border: 1px solid var(--line); background: var(--bg); color: var(--text); }
code.token { white-space: normal; word-break: break-all; user-select: all; font-size: 15px; }
input[type=password]:focus { outline: 2px solid var(--accent); outline-offset: -1px; }
.error { color: var(--accent); font-size: 15px; margin: 0 0 14px; }
code { font: 13px ui-monospace, SFMono-Regular, Menlo, monospace; background: var(--bg); padding: 2px 6px; border-radius: 6px; white-space: nowrap; }
.list { text-align: left; padding: 22px 0 8px; }
.list h3 { font-size: 13px; font-weight: 600; color: var(--muted); text-transform: uppercase; letter-spacing: .04em; margin: 0 24px 8px; }
.row { display: flex; align-items: center; gap: 12px; padding: 12px 24px; border-top: 1px solid var(--line); }
.row:first-of-type { border-top: 0; }
.row .what { flex: 1; min-width: 0; }
.row .name { font-weight: 600; }
.row .meta { color: var(--muted); font-size: 13px; }
.link { background: none; border: 0; padding: 6px 0; font: inherit; font-size: 15px; color: var(--accent); cursor: pointer; }
.empty { color: var(--muted); font-size: 15px; padding: 4px 24px 14px; margin: 0; }
.addresses { margin: 20px -8px 0; text-align: left; }
.addresses h3 { font-size: 13px; font-weight: 600; color: var(--muted); text-transform: uppercase; letter-spacing: .04em; margin: 0 8px 8px; }
.chip { display: flex; justify-content: space-between; gap: 10px; padding: 10px 12px; margin: 0 0 6px; border-radius: 12px;
  border: 1px solid var(--line); color: var(--text); text-decoration: none; font-size: 15px; word-break: break-all; }
.chip span { color: var(--muted); font-size: 13px; white-space: nowrap; }
.chip.on { border-color: var(--accent); box-shadow: inset 0 0 0 1px var(--accent); }
.addresses .hint { margin: 8px 8px 0; }
footer { text-align: center; color: var(--muted); font-size: 12px; margin-top: 8px; }
[hidden] { display: none !important; }
</style>
</head>
<body>
<main>
<header>
<svg viewBox="0 0 24 24" aria-hidden="true"><defs><linearGradient id="g" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#ff5e5b"/><stop offset="1" stop-color="#ff2d55"/></linearGradient></defs>
<path fill="url(#g)" d="M12 21.2C12 21.2 2.5 15.4 2.5 8.9 2.5 6 4.8 3.8 7.6 3.8c1.9 0 3.5 1 4.4 2.6.9-1.6 2.5-2.6 4.4-2.6 2.8 0 5.1 2.2 5.1 5.1 0 6.5-9.5 12.3-9.5 12.3z"/></svg>
<h1>Health Relay</h1>
</header>
{{if eq .Mode "login"}}
<section class="card">
  <h2>Set up your iPhone</h2>
  <p class="sub">Enter this server's token to get a setup code or a read token.</p>
  {{if .Message}}<p class="error">{{.Message}}</p>{{end}}
  <form class="login" method="post" action="/setup">
    <label for="token">Server token</label>
    <input id="token" name="token" type="password" autocomplete="current-password" required autofocus>
    <button class="button" type="submit">Continue</button>
  </form>
  <p class="hint">Or run <code>healthkit-relay pair</code> on the server: it shows a code to scan right in the terminal.</p>
</section>
<script nonce="{{.Nonce}}">
// Keeps #read-tokens (from /docs) across signing in.
if (location.hash) document.querySelector("form.login").action = "/setup" + location.hash;
</script>
{{else if eq .Mode "noaddress"}}
<section class="card">
  <h2>No address for your iPhone</h2>
  <p class="sub">This page was opened at {{.Server}}, which an iPhone cannot reach, and the server found no other address it answers on.</p>
  <p class="hint">Start the server with <code>-public-url</code> set to the address your iPhone uses (for example <code>https://health.example.com</code> behind a reverse proxy), or with <code>-listen 0.0.0.0:8080</code> so this page can offer the machine's network addresses.</p>
</section>
{{else if eq .Mode "token"}}
<section class="card">
  <h2>Read token for {{.NewName}}</h2>
  <p class="sub">Copy it now: it is shown only this once. It reads health data through the query API (<code>/docs</code>), and nothing else.</p>
  <p><code class="token">{{.NewToken}}</code></p>
  <a class="button" href="/setup#read-tokens">Done</a>
</section>
{{else if eq .Mode "used"}}
<section class="card">
  <svg class="check" viewBox="0 0 64 64" aria-hidden="true"><circle cx="32" cy="32" r="30" fill="#34c759"/><path d="M19 33l9 9 17-19" fill="none" stroke="#fff" stroke-width="5" stroke-linecap="round" stroke-linejoin="round"/></svg>
  <h2>Already connected</h2>
  <p class="sub">This code set up {{.Paired.Name}}{{if .Paired.Model}} ({{.Paired.Model}}){{end}}. Codes work once; get a new one to set up another iPhone.</p>
</section>
{{else}}
<section class="card" id="pair">
  <h2>Set up your iPhone</h2>
  <div class="desktop">
    <p class="sub">Point the iPhone's Camera at this code, then tap <b>Open in Health Relay</b>.</p>
    <div class="qr">{{.QR}}</div>
  </div>
  <div class="phone">
    <p class="sub">Connects Health Relay on this iPhone to {{.Server}}.</p>
    <a class="button" href="{{.Link}}">Open in Health Relay</a>
  </div>
  {{if .Addresses}}
  <div class="addresses">
    <h3>Address in the code</h3>
    {{range .Addresses}}<a class="chip{{if eq .URL $.Chosen}} on{{end}}" href="/setup?code={{$.Code}}&amp;server={{.URL}}">{{.URL}}<span>{{.Source}}</span></a>{{end}}
    <p class="hint">Found on this server; pick the one your iPhone reaches. To fix it for good, start the server with <code>-public-url</code>.</p>
  </div>
  {{end}}
  <p class="status" id="status"><span class="dot"></span><span id="status-text"><span class="desktop">Waiting for your iPhone · </span>Works once, until {{.Expires}}</span></p>
</section>
<section class="card" id="done" hidden>
  <svg class="check" viewBox="0 0 64 64" aria-hidden="true"><circle cx="32" cy="32" r="30" fill="#34c759"/><path d="M19 33l9 9 17-19" fill="none" stroke="#fff" stroke-width="5" stroke-linecap="round" stroke-linejoin="round"/></svg>
  <h2>iPhone connected</h2>
  <p class="sub" id="done-text">Health Relay asks for access to Health once, then syncs on its own. You can close this page.</p>
</section>
{{if .Admin}}
<section class="card list">
  <h3>Paired iPhones</h3>
  {{range .Devices}}
  <div class="row">
    <div class="what"><div class="name">{{.Name}}</div><div class="meta">{{if .Model}}{{.Model}} · {{end}}paired {{date .CreatedAt}} · seen {{ago .LastSeen}}</div></div>
    <form method="post" action="/setup/revoke"><input type="hidden" name="id" value="{{.ID}}"><button class="link" type="submit">Revoke</button></form>
  </div>
  {{else}}
  <p class="empty">None yet.</p>
  {{end}}
</section>
<section class="card list" id="read-tokens">
  <h3>Read tokens</h3>
  {{range .Tokens}}
  <div class="row">
    <div class="what"><div class="name">{{.Name}}</div><div class="meta">query API · created {{date .CreatedAt}} · used {{ago .LastSeen}}</div></div>
    <form method="post" action="/setup/revoke"><input type="hidden" name="id" value="{{.ID}}"><button class="link" type="submit">Revoke</button></form>
  </div>
  {{end}}
  <form class="row" method="post" action="/setup/tokens">
    <input class="what" name="name" type="text" maxlength="100" placeholder="New token, e.g. grafana" required>
    <button class="link" type="submit">Create</button>
  </form>
</section>
{{end}}
<script nonce="{{.Nonce}}">
(function () {
  var code = {{.Code}}, admin = {{.Admin}};
  var timer = setInterval(function () {
    fetch("/setup/status?code=" + encodeURIComponent(code), {cache: "no-store"}).then(function (r) { return r.json(); }).then(function (s) {
      if (s.state === "paired") {
        clearInterval(timer);
        document.getElementById("pair").hidden = true;
        var done = document.getElementById("done");
        done.hidden = false;
        if (s.device) document.getElementById("done-text").textContent = s.device + (s.model ? " (" + s.model + ")" : "") +
          " is connected. Health Relay asks for access to Health once, then syncs on its own. You can close this page.";
        if (admin) setTimeout(function () { location.replace("/setup"); }, 8000);
      } else if (s.state === "invalid") {
        clearInterval(timer);
        document.getElementById("status-text").textContent = "This code expired.";
        if (admin) location.replace("/setup");
      }
    }).catch(function () {});
  }, 2000);
})();
</script>
{{end}}
<footer>healthkit-relay {{.Version}}</footer>
</main>
</body>
</html>
`))
