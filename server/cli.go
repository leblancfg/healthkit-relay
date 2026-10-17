package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"
)

// runCommand runs a subcommand against the running server, authenticated with the server
// token, using the same flags and HKR_ variables as the server itself.
func runCommand(name string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("healthkit-relay "+name, flag.ContinueOnError)
	var cfg config
	cfg.register(fs)
	server := fs.String("server", "", "URL of the running server (default: http://<-listen>)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := applyEnv(fs); err != nil {
		return err
	}
	if cfg.tokenFile == "" {
		return errors.New("-token-file is required: the server's token file (HKR_TOKEN_FILE)")
	}
	token, err := readSecret(cfg.tokenFile)
	if err != nil {
		return err
	}
	base := strings.TrimRight(*server, "/")
	if base == "" {
		base = "http://" + loopback(cfg.listen)
	}
	call := func(method, path string, body any, v any) error {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, base+path, rd)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err != nil {
			return fmt.Errorf("is the server running? %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && method != http.MethodDelete {
			return fmt.Errorf("%s answered 404: it is older than this command; update it", base)
		}
		if resp.StatusCode >= 300 {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			return fmt.Errorf("%s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
		}
		if v != nil {
			return json.NewDecoder(resp.Body).Decode(v)
		}
		return nil
	}

	switch name {
	case "pair":
		var c CodeResponse
		if err := call(http.MethodPost, "/v1/pairing-codes", map[string]string{"public_url": cfg.publicURL}, &c); err != nil {
			return err
		}
		q, err := EncodeQR([]byte(c.Link))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Scan with the iPhone's Camera to set up Health Relay (works once, until %s):\n\n",
			c.ExpiresAt.Local().Format("15:04"))
		fmt.Fprint(out, q.Terminal())
		fmt.Fprintf(out, "\nOr open this page on the iPhone or a computer:\n  %s\n", c.SetupURL)
		if u, err := url.Parse(c.SetupURL); err == nil && isLoopbackHost(u.Hostname()) {
			fmt.Fprintf(out, "\nWarning: this points to %s, which a phone cannot reach. Set -public-url (HKR_PUBLIC_URL) "+
				"to the address phones use, on the server or for this command.\n", u.Host)
		}
	case "devices":
		var list struct{ Devices []Device }
		if err := call(http.MethodGet, "/v1/devices", nil, &list); err != nil {
			return err
		}
		if len(list.Devices) == 0 {
			fmt.Fprintln(out, "No paired devices. Pair one with: healthkit-relay pair")
			return nil
		}
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tMODEL\tPAIRED\tLAST SEEN")
		for _, d := range list.Devices {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", d.ID, d.Name, d.Model, d.CreatedAt.Local().Format("2006-01-02 15:04"), ago(d.LastSeen))
		}
		tw.Flush()
	case "revoke":
		if fs.NArg() != 1 {
			return errors.New("usage: healthkit-relay revoke <device id> (see healthkit-relay devices)")
		}
		if err := call(http.MethodDelete, "/v1/devices/"+url.PathEscape(fs.Arg(0)), nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "Revoked %s: its token no longer works.\n", fs.Arg(0))
	}
	return nil
}

// loopback turns a listen address into one to connect to: 0.0.0.0:8080 -> 127.0.0.1:8080.
func loopback(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
