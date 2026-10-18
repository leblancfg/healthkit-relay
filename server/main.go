// Command healthkit-relay receives Apple Health samples from the companion iPhone app,
// archives every record durably on disk (a SQLite staging database, flushed hourly to
// Parquet files) and optionally forwards numeric samples to a metrics database as InfluxDB
// line protocol.
//
// Every flag can also be set with an environment variable: -archive-dir is HKR_ARCHIVE_DIR,
// and so on (upper case, dashes as underscores, HKR_ prefix). Flags win over the environment.
//
// Subcommands talk to the running server with the same configuration: `pair` prints a
// one-time setup code (QR code and link) for the app, `devices` lists paired devices and
// `revoke <id>` removes one, `token create -name grafana` mints a read token for the query API. Offline commands work on the archive directly, with the server
// stopped: `migrate` converts the JSON Lines archive of versions before 0.5, `flush` and
// `compact` run the hourly and nightly maintenance at once.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	// Memory use is bounded by design (a batch, a row group); a soft limit keeps the garbage
	// collector from letting the heap grow to twice that. GOMEMLIMIT overrides it.
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(128 << 20)
	}
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

// config holds the flags the server and its subcommands share.
type config struct {
	listen, archiveDir, tokenFile, publicURL, setupFrom string
	groupRead                                           bool
}

func (c *config) storeOptions() StoreOptions { return StoreOptions{GroupRead: c.groupRead} }

func (c *config) register(fs *flag.FlagSet) {
	fs.StringVar(&c.listen, "listen", "127.0.0.1:8080", "address to listen on")
	fs.StringVar(&c.archiveDir, "archive-dir", "", "archive directory (staging.db, parquet/) and devices.json (required)")
	fs.StringVar(&c.tokenFile, "token-file", "", "file holding the server token: admin credential, also accepted for uploads (required)")
	fs.StringVar(&c.publicURL, "public-url", "", "URL phones use to reach this server, put in setup links, e.g. https://health.example.com (default: the setup page offers the addresses it finds)")
	fs.StringVar(&c.setupFrom, "setup-from", "private", "who may open /setup and pair: private (loopback, LAN, Tailscale), any, or off")
	fs.BoolVar(&c.groupRead, "group-read", false, "make the archive readable by the service's group (directories 0750, files 0640) for backups or queries; default: owner only (0700, 0600)")
}

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "pair", "devices", "revoke":
			return runCommand(args[0], args[1:], os.Stdout, os.Stderr)
		case "token":
			if len(args) < 2 || args[1] != "create" {
				return errors.New("usage: healthkit-relay token create -name <what it is for> [-scope read|upload]; list and revoke tokens with devices and revoke")
			}
			return runCommand("token create", args[2:], os.Stdout, os.Stderr)
		case "migrate":
			return runMigrate(args[1:], os.Stdout)
		case "flush", "compact":
			return runMaintenance(args[0], args[1:], os.Stdout)
		}
	}
	fs := flag.NewFlagSet("healthkit-relay", flag.ContinueOnError)
	var cfg config
	cfg.register(fs)
	listen, archiveDir, tokenFile := &cfg.listen, &cfg.archiveDir, &cfg.tokenFile
	forwardURL := fs.String("forward-url", "", "InfluxDB line-protocol write URL for numeric samples, e.g. http://127.0.0.1:8428/influx/write (optional)")
	forwardTokenFile := fs.String("forward-token-file", "", "file holding a bearer token for -forward-url (optional)")
	measurement := fs.String("measurement", "healthkit", "line-protocol measurement name; metrics are named <measurement>_<field>")
	maxBody := fs.Int64("max-body-bytes", 32<<20, "largest accepted request body")
	flushEvery := fs.Duration("flush-interval", time.Hour, "how often staged records are written to Parquet")
	compactAt := fs.String("compact-at", "03:30", "local time of the nightly compaction, HH:MM")
	queryDuckDB := fs.String("query-duckdb", "", "duckdb executable for the read-only query API (/v1/types..., /openapi.json, /docs); empty: the query API is off")
	queryDir := fs.String("query-archive-dir", "", "archive the query API reads (its parquet/), e.g. a replica; default: -archive-dir")
	queryTimeout := fs.Duration("query-timeout", 30*time.Second, "longest a query may run, waiting for a free slot included")
	queryMaxRows := fs.Int("query-max-rows", 50000, "most rows one query may return")
	queryMemory := fs.String("query-memory-limit", "512MB", "DuckDB memory_limit per query; queries needing more fail (nothing spills to disk)")
	queryThreads := fs.Int("query-threads", 2, "DuckDB threads per query")
	queryConcurrency := fs.Int("query-concurrency", 2, "queries that may run at once; others wait (within -query-timeout)")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := applyEnv(fs); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	if *archiveDir == "" || *tokenFile == "" {
		return errors.New("-archive-dir and -token-file are required")
	}
	switch cfg.setupFrom {
	case "private", "any", "off":
	default:
		return fmt.Errorf("-setup-from must be private, any or off, not %q", cfg.setupFrom)
	}

	token, err := readSecret(*tokenFile)
	if err != nil {
		return err
	}
	if len(token) < 16 {
		return errors.New("token is shorter than 16 characters")
	}
	if *flushEvery < time.Minute {
		return errors.New("-flush-interval must be at least a minute")
	}
	archive, err := OpenStoreWith(*archiveDir, cfg.storeOptions())
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer archive.Close()
	log.Printf("healthkit-relay %s: archive %s holds %d records (%d staged)", version, *archiveDir, archive.Len(), archive.Staged())
	if legacy := archive.LegacyFiles(); len(legacy) > 0 {
		log.Printf("archive: %d JSON Lines files from before 0.5 are not read: stop the server and run `healthkit-relay migrate`", len(legacy))
	}

	devices, err := OpenDevices(filepath.Join(*archiveDir, "devices.json"))
	if err != nil {
		return fmt.Errorf("open devices: %w", err)
	}
	srv := &Server{Archive: archive, Token: token, MaxBodyBytes: *maxBody, Devices: devices, Codes: NewCodes(),
		PublicURL: strings.TrimRight(cfg.publicURL, "/"), SetupFrom: cfg.setupFrom, Listen: *listen}
	if *queryDuckDB != "" {
		dir := cmpOr(*queryDir, *archiveDir)
		db, err := NewDuckDB(*queryDuckDB, dir, *queryTimeout, *queryMaxRows, *queryMemory, *queryThreads, *queryConcurrency)
		if err != nil {
			return err
		}
		srv.Query = &queryAPI{db: db, now: time.Now}
		log.Printf("query API on: %s reads %s/parquet; /docs, /openapi.json", db.Path, db.ArchiveDir)
		// A broken duckdb or archive path shows now, not at the first query; uploads work regardless.
		if err := db.Query(context.Background(), "SELECT count(*) AS n FROM deleted", func(json.RawMessage) error { return nil }); err != nil {
			log.Printf("query API: the test query failed, queries will too: %v", err)
		}
	}
	if *forwardURL != "" {
		f := &Forwarder{URL: *forwardURL, Measurement: *measurement, Client: &http.Client{}}
		if *forwardTokenFile != "" {
			if f.Token, err = readSecret(*forwardTokenFile); err != nil {
				return err
			}
		}
		srv.Forwarder = f
		log.Printf("forwarding numeric samples to %s", *forwardURL)
	}

	hs := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	waitMaintenance, err := archive.Maintain(ctx, *flushEvery, *compactAt)
	if err != nil {
		return err
	}
	defer func() { stop(); waitMaintenance() }() // runs before the archive closes (deferred earlier)
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Printf("listening on %s; %d paired devices; setup page /setup (%s)", *listen, devices.Len(), cfg.setupFrom)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return hs.Shutdown(shutdown)
}

// runMaintenance runs a flush or a compaction on the archive, with the server stopped.
func runMaintenance(name string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("healthkit-relay "+name, flag.ContinueOnError)
	var cfg config
	cfg.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := applyEnv(fs); err != nil {
		return err
	}
	if cfg.archiveDir == "" {
		return errors.New("-archive-dir is required")
	}
	s, err := OpenStoreWith(cfg.archiveDir, cfg.storeOptions())
	if err != nil {
		return err
	}
	defer s.Close()
	if name == "flush" {
		n, err := s.Flush(context.Background(), true)
		if err == nil {
			fmt.Fprintf(out, "flushed %d rows\n", n)
		}
		return err
	}
	if _, err := s.Flush(context.Background(), false); err != nil {
		return err
	}
	n, err := s.Compact(context.Background())
	if err == nil {
		fmt.Fprintf(out, "compacted %d partitions\n", n)
	}
	return err
}

// applyEnv sets every flag not given on the command line from HKR_<NAME>, if present.
func applyEnv(fs *flag.FlagSet) error {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	var err error
	fs.VisitAll(func(f *flag.Flag) {
		env := "HKR_" + strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))
		if v, ok := os.LookupEnv(env); ok && !given[f.Name] && err == nil {
			if e := f.Value.Set(v); e != nil {
				err = fmt.Errorf("%s: %w", env, e)
			}
		}
	})
	return err
}

func readSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
