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
	valid := fs.Duration("valid", CodeTTL, "pair: how long the code works, at most 24h (e.g. 2h to send the link to a phone)")
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
		body := map[string]any{"public_url": cfg.publicURL, "valid_seconds": int(valid.Seconds())}
		if err := call(http.MethodPost, "/v1/pairing-codes", body, &c); err != nil {
			return err
		}
		if c.Link == "" {
			return errors.New("the server knows no address a phone could reach (it listens on loopback only and " +
				"found no Tailscale serve name): set -public-url (HKR_PUBLIC_URL) to the address your iPhone uses")
		}
		q, err := EncodeQR([]byte(c.Link))
		if err != nil {
			return err
		}
		until := c.ExpiresAt.Local().Format("15:04")
		if time.Until(c.ExpiresAt) > 20*time.Hour {
			until = c.ExpiresAt.Local().Format("Jan 2 15:04")
		}
		fmt.Fprintf(out, "Scan with the iPhone's Camera to set up Health Relay (works once, until %s):\n\n", until)
		fmt.Fprint(out, q.Terminal())
		fmt.Fprintf(out, "\nOr open this page on the iPhone or a computer:\n  %s\n", c.SetupURL)
		if len(c.Addresses) > 0 {
			fmt.Fprintf(out, "\nThe code points to %s (%s), the first address found. Others:\n", c.Addresses[0].URL, c.Addresses[0].Source)
			for _, a := range c.Addresses[1:] {
				fmt.Fprintf(out, "  %s (%s)\n", a.URL, a.Source)
			}
			if len(c.Addresses) == 1 {
				fmt.Fprintln(out, "  none")
			}
			fmt.Fprintln(out, "Set -public-url (HKR_PUBLIC_URL) to choose.")
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
