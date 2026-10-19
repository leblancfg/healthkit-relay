package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
)

// runCommand runs a subcommand on the archive directory's state files (devices.json,
// codes.json, admin.json), using the same flags and HKR_ variables as the server itself. It
// runs as the server's user or root, with the server running or not: the server rereads the
// files when they change.
func runCommand(name string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("healthkit-relay "+name, flag.ContinueOnError)
	var cfg config
	cfg.register(fs)
	valid := fs.Duration("valid", CodeTTL, "pair: how long the setup code works, at most 24h, e.g. 2h to send its link to a phone")
	tokenName := fs.String("name", "", "token create: what the token is for, e.g. grafana (required)")
	tokenScope := fs.String("scope", ScopeRead, "token create: read (reads through the query API) or upload (uploads, like a paired device's token)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := applyEnv(fs); err != nil {
		return err
	}
	dir := cfg.archiveDir
	if dir == "" {
		return errors.New("-archive-dir is required: the folder where the server keeps your data, the same as the server's (or set HKR_ARCHIVE_DIR)")
	}
	if err := checkStateOwner(dir); err != nil {
		return err
	}
	devices, err := OpenDevices(filepath.Join(dir, "devices.json"))
	if err != nil {
		return err
	}
	s := &Server{Devices: devices, PublicURL: strings.TrimRight(cfg.publicURL, "/"), Listen: cfg.listen}

	switch name {
	case "pair":
		codes, err := OpenCodes(filepath.Join(dir, "codes.json"))
		if err != nil {
			return err
		}
		s.Codes = codes
		found := s.addresses(nil)
		if len(found) == 0 {
			return errors.New("found no server address an iPhone can reach: set -public-url (HKR_PUBLIC_URL) to the address " +
				"your iPhone uses, e.g. http://192.168.1.10:8080, or have the server listen on the network with -listen 0.0.0.0:8080")
		}
		c, err := s.mintCodeFor(found[0].URL, *valid)
		if err != nil {
			return err
		}
		if s.PublicURL == "" {
			c.Addresses = found
		}
		q, err := EncodeQR([]byte(c.Link))
		if err != nil {
			return err
		}
		until := c.ExpiresAt.Local().Format("15:04")
		if time.Until(c.ExpiresAt) > 20*time.Hour {
			until = c.ExpiresAt.Local().Format("Jan 2 15:04")
		}
		fmt.Fprintf(out, "Scan with the iPhone's Camera, then tap Open in Health Relay. This setup code works once, until %s:\n\n", until)
		fmt.Fprint(out, q.Terminal())
		fmt.Fprintf(out, "\nOr open this link on the iPhone (on a computer, it shows this QR code):\n  %s\n", c.SetupURL)
		if len(c.Addresses) > 0 {
			fmt.Fprintf(out, "\nThe setup code holds the server address %s (%s), the first one found.\n", c.Addresses[0].URL, c.Addresses[0].Source)
			if len(c.Addresses) > 1 {
				fmt.Fprintln(out, "Other addresses found:")
				for _, a := range c.Addresses[1:] {
					fmt.Fprintf(out, "  %s (%s)\n", a.URL, a.Source)
				}
			}
			fmt.Fprintln(out, "To choose the address, set -public-url (HKR_PUBLIC_URL).")
		}
	case "devices":
		list := devices.List()
		if len(list) == 0 {
			fmt.Fprintln(out, "No paired devices or read tokens yet. To pair an iPhone, run: healthkit-relay pair")
			return nil
		}
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tMODEL\tSCOPE\tCREATED\tLAST SEEN")
		for _, d := range list {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.Name, d.Model, d.scope(), d.CreatedAt.Local().Format("2006-01-02 15:04"), ago(d.LastSeen))
		}
		tw.Flush()
	case "token create":
		if fs.NArg() != 0 {
			return errors.New("usage: healthkit-relay token create -name <what it is for> [-scope read|upload]")
		}
		t, _, err := s.createToken(*tokenName, *tokenScope)
		if err != nil {
			return err
		}
		// The token alone on stdout, so that it can go straight to a file.
		fmt.Fprintln(out, t.Token)
		fmt.Fprintf(errOut, "Created %s token %s (%s). It is shown only this once: save it in a password manager. "+
			"To revoke it: healthkit-relay revoke %s\n", t.Device.Scope, t.Device.ID, t.Device.Name, t.Device.ID)
	case "revoke":
		if fs.NArg() != 1 {
			return errors.New("usage: healthkit-relay revoke <id>, with an ID from healthkit-relay devices")
		}
		ok, err := devices.Revoke(fs.Arg(0))
		if err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("no paired device or read token has the ID %s: healthkit-relay devices lists them", fs.Arg(0))
		}
		fmt.Fprintf(out, "Revoked %s: its token no longer works.\n", fs.Arg(0))
	case "admin reset":
		if fs.NArg() != 0 {
			return errors.New("usage: healthkit-relay admin reset")
		}
		token, err := newAdminToken(dir)
		if err != nil {
			return err
		}
		printAdminToken(out, token, setupURL(cfg.publicURL, cfg.listen), "Made a new admin token: the previous one no longer works.")
		if cfg.tokenFile != "" {
			fmt.Fprintf(errOut, "The server now ignores -token-file %s: delete that file and remove -token-file (HKR_TOKEN_FILE).\n", cfg.tokenFile)
		}
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
