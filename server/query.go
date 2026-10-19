package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The query API runs each query in a fresh duckdb process (the CLI, so the server stays pure
// Go), in memory, over the Parquet archive through the views in sql/views.sql. Every
// statement is built here from validated values: a catalog type, timestamps, integers. No
// text from a request reaches the SQL.
//
// The process is locked down before the query runs: external access is off except reading
// <archive>/parquet/ (allowed_directories), extensions are neither installed nor loaded,
// nothing spills to disk, memory and threads are capped, and the configuration is locked so
// later statements cannot change any of it. getenv() and every other file is refused. It
// runs with an empty environment, as the server's user, and is killed at the deadline.

//go:embed sql/views.sql
var viewsSQL string

// DuckDB runs queries with the duckdb command.
type DuckDB struct {
	Path        string        // the duckdb executable
	ArchiveDir  string        // absolute; its parquet/ is read
	Timeout     time.Duration // per query, waiting for a slot included
	MaxRows     int           // the most rows a query may return
	MemoryLimit string        // DuckDB memory_limit, e.g. 512MB
	Threads     int
	// MaxOutputBytes bounds what one query may print (default 64 MB).
	MaxOutputBytes int

	slots chan struct{} // concurrent queries
}

var memoryLimitRE = regexp.MustCompile(`^[0-9]{1,6}(\.[0-9]{1,3})?(KB|MB|GB|KiB|MiB|GiB)$`)

// NewDuckDB checks the settings; concurrency is how many queries may run at once.
func NewDuckDB(path, archiveDir string, timeout time.Duration, maxRows int, memoryLimit string, threads, concurrency int) (*DuckDB, error) {
	switch {
	case timeout < time.Second:
		return nil, errors.New("-query-timeout must be at least 1s")
	case maxRows < 1:
		return nil, errors.New("-query-max-rows must be positive")
	case !memoryLimitRE.MatchString(memoryLimit):
		return nil, fmt.Errorf("-query-memory-limit %q: want a size such as 512MB or 1GiB", memoryLimit)
	case threads < 1 || threads > 64:
		return nil, errors.New("-query-threads must be 1 to 64")
	case concurrency < 1 || concurrency > 64:
		return nil, errors.New("-query-concurrency must be 1 to 64")
	}
	bin, err := exec.LookPath(path)
	if err != nil {
		return nil, fmt.Errorf("-query-duckdb: %w", err)
	}
	dir, err := filepath.Abs(archiveDir)
	if err != nil {
		return nil, err
	}
	return &DuckDB{Path: bin, ArchiveDir: dir, Timeout: timeout, MaxRows: maxRows, MemoryLimit: memoryLimit,
		Threads: threads, slots: make(chan struct{}, concurrency)}, nil
}

// sqlString quotes s as an SQL string literal. Only for values the server made or checked.
func sqlString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// prelude locks the process down and defines the views.
func (q *DuckDB) prelude() string {
	parquet := filepath.Join(q.ArchiveDir, "parquet") + "/"
	var b strings.Builder
	for _, s := range []string{
		"SET memory_limit = " + sqlString(q.MemoryLimit),
		"SET threads = " + strconv.Itoa(q.Threads),
		"SET temp_directory = ''", // no spilling to disk: past memory_limit a query fails
		"SET preserve_insertion_order = false",
		"SET autoinstall_known_extensions = false",
		"SET autoload_known_extensions = false",
		"SET allowed_directories = [" + sqlString(parquet) + "]",
		"SET enable_external_access = false",
		"SET lock_configuration = true",
	} {
		b.WriteString(s + ";\n")
	}
	// The views read parquet/... relative to the archive; here, absolute paths.
	b.WriteString(strings.ReplaceAll(viewsSQL, "'parquet/", "'"+strings.ReplaceAll(parquet, "'", "''")))
	b.WriteString("\n")
	return b.String()
}

// Query errors a client can act on.
var (
	errQueryTimeout = errors.New("the query took too long")
	errQueryBusy    = errors.New("too many queries at once")
	errQueryTooBig  = errors.New("the result is too large")
)

// Query runs one SELECT and calls row for each result row (a JSON object), at most maxRows
// times; more is an error, so callers put a LIMIT in the SQL.
func (q *DuckDB) Query(ctx context.Context, sql string, row func(json.RawMessage) error) error {
	ctx, cancel := context.WithTimeout(ctx, q.Timeout)
	defer cancel()
	select {
	case q.slots <- struct{}{}:
		defer func() { <-q.slots }()
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errQueryBusy
		}
		return ctx.Err()
	}
	start := time.Now()
	cmd := exec.CommandContext(ctx, q.Path, "-no-init", "-bail", "-jsonlines")
	cmd.Dir = q.ArchiveDir
	cmd.Env = []string{"HOME=/nonexistent", "TZ=UTC"}
	cmd.Stdin = strings.NewReader(q.prelude() + sql + ";\n")
	cmd.WaitDelay = 2 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &limitedBuffer{buf: &stderr, max: 8 << 10}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start duckdb: %w", err)
	}
	maxOut := q.MaxOutputBytes
	if maxOut == 0 {
		maxOut = 64 << 20
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	rows, size := 0, 0
	var rowErr error
	for sc.Scan() {
		line := sc.Bytes()
		size += len(line)
		rows++
		if rows > q.MaxRows || size > maxOut {
			rowErr = errQueryTooBig
			break
		}
		if err := row(json.RawMessage(line)); err != nil {
			rowErr = err
			break
		}
	}
	if rowErr == nil && sc.Err() != nil {
		rowErr = errQueryTooBig // a row over 16 MB
	}
	if rowErr != nil {
		cancel() // stop duckdb; its exit status no longer matters
	}
	waitErr := cmd.Wait()
	switch {
	case rowErr != nil:
		return rowErr
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		log.Printf("query: timed out after %s", time.Since(start).Round(time.Millisecond))
		return errQueryTimeout
	case ctx.Err() != nil:
		return ctx.Err()
	case waitErr != nil:
		// DuckDB's message names paths and SQL: logged, not sent to the client.
		return fmt.Errorf("duckdb: %v: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// limitedBuffer keeps the first max bytes written to it.
type limitedBuffer struct {
	buf *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
