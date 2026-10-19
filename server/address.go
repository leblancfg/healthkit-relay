package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// Which address goes into setup links. With -public-url, that one. Otherwise the server
// lists what it can find, best first, and the setup page lets the user pick:
//
//  1. the address the setup page was opened with (Host, or a local proxy's
//     X-Forwarded-Host), unless it is loopback or link-local;
//  2. Tailscale serve names that proxy to this server (https://<name>.ts.net[:port]);
//  3. this machine's LAN IPv4 addresses, if the server listens on them;
//  4. its Tailscale IPv4 addresses (100.64.0.0/10), likewise.
//
// A loopback or link-local address is never offered: a phone cannot reach it. Plain-HTTP
// candidates are IP addresses, which the app may reach without TLS; a Tailscale name over
// plain HTTP is not offered, since iOS requires HTTPS for such names.

// Address is a URL phones might reach the server at, and how it was found.
type Address struct {
	URL    string `json:"url"`
	Source string `json:"source"`
}

// netInfo is what the machine says about its addresses; a field of Server so tests can
// replace it.
type netInfo struct {
	interfaces func() []netip.Addr
	tailscale  func() tailscaleInfo
}

type tailscaleInfo struct {
	IPs []netip.Addr
	// Serve maps a served https host[:port] to the local port it proxies to.
	Serve map[string]string
}

// addresses lists where phones may reach the server, best first; r is the setup page's
// request, nil for a command.
func (s *Server) addresses(r *http.Request) []Address {
	if s.PublicURL != "" {
		return []Address{{URL: strings.TrimRight(s.PublicURL, "/"), Source: "-public-url"}}
	}
	var out []Address
	seen := map[string]bool{}
	add := func(u, source string) {
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, Address{URL: u, Source: source})
		}
	}
	if r != nil {
		if u := requestURL(r); reachableURL(u) {
			add(u, "this page's address")
		}
	}

	info := s.net()
	listenHost, port, _ := net.SplitHostPort(s.Listen)
	ts := info.tailscale()
	var served []string
	for host, to := range ts.Serve {
		if to == port {
			served = append(served, host)
		}
	}
	sort.Strings(served)
	for _, host := range served {
		add("https://"+strings.TrimSuffix(host, ":443"), "Tailscale serve")
	}

	listenAll := listenHost == "" || listenHost == "0.0.0.0" || listenHost == "::"
	listenIP, _ := netip.ParseAddr(listenHost)
	var lan, tailnet []netip.Addr
	for _, a := range append(info.interfaces(), ts.IPs...) {
		a = a.Unmap()
		if !a.Is4() || !reachableIP(a) || !(listenAll || a == listenIP) || port == "" {
			continue
		}
		if cgnat.Contains(a) {
			tailnet = append(tailnet, a)
		} else {
			lan = append(lan, a)
		}
	}
	// Private LAN addresses before public ones.
	sort.SliceStable(lan, func(i, j int) bool { return lan[i].IsPrivate() && !lan[j].IsPrivate() })
	for _, a := range lan {
		add("http://"+net.JoinHostPort(a.String(), port), "local network")
	}
	for _, a := range tailnet {
		add("http://"+net.JoinHostPort(a.String(), port), "Tailscale")
	}
	return out
}

// requestURL is the scheme and host the request was made to (a local proxy's
// X-Forwarded-* headers win).
func requestURL(r *http.Request) string {
	scheme, host := "http", r.Host
	if r.TLS != nil {
		scheme = "https"
	}
	if fromLocalProxy(r) {
		if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
			scheme = p
		}
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			host = h
		}
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func reachableIP(a netip.Addr) bool {
	return a.IsValid() && !a.IsLoopback() && !a.IsLinkLocalUnicast() && !a.IsUnspecified() && !a.IsMulticast()
}

// reachableURL rejects URLs whose host a phone cannot reach: localhost, loopback, link-local.
func reachableURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Hostname() == "" {
		return false
	}
	h := strings.ToLower(p.Hostname())
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return false
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return reachableIP(a.Unmap())
	}
	return true
}

func (s *Server) net() netInfo {
	if s.netInfo != nil {
		return *s.netInfo
	}
	return netInfo{interfaces: interfaceAddrs, tailscale: cachedTailscale}
}

func interfaceAddrs() []netip.Addr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(n.IP); ok {
					out = append(out, ip.Unmap())
				}
			}
		}
	}
	return out
}

var tsCache struct {
	sync.Mutex
	at   time.Time
	info tailscaleInfo
}

// cachedTailscale asks the tailscale CLI, if installed, at most once a minute.
func cachedTailscale() tailscaleInfo {
	tsCache.Lock()
	defer tsCache.Unlock()
	if time.Since(tsCache.at) < time.Minute {
		return tsCache.info
	}
	tsCache.at = time.Now()
	tsCache.info = readTailscale()
	return tsCache.info
}

func readTailscale() tailscaleInfo {
	var info tailscaleInfo
	bin, err := exec.LookPath("tailscale")
	if err != nil {
		return info
	}
	run := func(args ...string) []byte {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(ctx, bin, args...).Output()
		return out
	}
	var status struct {
		Self struct{ TailscaleIPs []string }
	}
	if json.Unmarshal(run("status", "--json"), &status) == nil {
		for _, s := range status.Self.TailscaleIPs {
			if a, err := netip.ParseAddr(s); err == nil {
				info.IPs = append(info.IPs, a)
			}
		}
	}
	info.Serve = parseServeStatus(run("serve", "status", "--json"))
	return info
}

// parseServeStatus reads `tailscale serve status --json`: for each served host:port, the
// local port its "/" handler proxies to (loopback targets only).
func parseServeStatus(b []byte) map[string]string {
	var st struct {
		Web map[string]struct {
			Handlers map[string]struct{ Proxy string }
		}
	}
	if json.Unmarshal(b, &st) != nil {
		return nil
	}
	out := map[string]string{}
	for host, web := range st.Web {
		h, ok := web.Handlers["/"]
		if !ok || h.Proxy == "" {
			continue
		}
		target := h.Proxy
		if !strings.Contains(target, "://") {
			target = "http://" + target
		}
		u, err := url.Parse(target)
		if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
			continue
		}
		out[host] = u.Port()
	}
	return out
}
