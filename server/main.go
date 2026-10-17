// Command healthkit-relay receives Apple Health samples from the companion iPhone app,
// archives every record durably on disk and optionally forwards numeric samples to a
// metrics database as InfluxDB line protocol.
//
// Every flag can also be set with an environment variable: -archive-dir is HKR_ARCHIVE_DIR,
// and so on (upper case, dashes as underscores, HKR_ prefix). Flags win over the environment.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("healthkit-relay", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "address to listen on")
	archiveDir := fs.String("archive-dir", "", "directory of the append-only archive (required)")
	tokenFile := fs.String("token-file", "", "file holding the bearer token clients must send (required)")
	forwardURL := fs.String("forward-url", "", "InfluxDB line-protocol write URL for numeric samples, e.g. http://127.0.0.1:8428/influx/write (optional)")
	forwardTokenFile := fs.String("forward-token-file", "", "file holding a bearer token for -forward-url (optional)")
	measurement := fs.String("measurement", "healthkit", "line-protocol measurement name; metrics are named <measurement>_<field>")
	maxBody := fs.Int64("max-body-bytes", 32<<20, "largest accepted request body")
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

	token, err := readSecret(*tokenFile)
	if err != nil {
		return err
	}
	if len(token) < 16 {
		return errors.New("token is shorter than 16 characters")
	}
	archive, err := OpenArchive(*archiveDir)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	log.Printf("healthkit-relay %s: archive %s holds %d records", version, *archiveDir, archive.Len())

	srv := &Server{Archive: archive, Token: token, MaxBodyBytes: *maxBody}
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
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Printf("listening on %s", *listen)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return hs.Shutdown(shutdown)
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
