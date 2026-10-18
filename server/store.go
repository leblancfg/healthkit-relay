package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

// The archive has two parts, both under -archive-dir:
//
//   - staging.db, a SQLite database: the batches received since the last flush (staged), the
//     ID of every record ever stored (seen, for de-duplication in constant memory), the
//     tombstones (deleted) and the start time of samples that series refer to (parents).
//   - parquet/, the archive proper: Parquet files, hive-partitioned by dataset, type and
//     month of the sample (see parquet.go). Flush moves staged rows there every hour;
//     compaction merges each month's files into one every night.
//
// A batch is acknowledged once it is committed to staging.db (fsynced), so the phone can move
// on; everything after that is crash-safe and re-runnable (flush.go, compact.go, erase.go).

// Item is one thing received: a sample, a record or a tombstone.
type Item struct {
	Op   string // "sample", "record" or "delete"
	UUID string // samples and tombstones; records: the sample a series or blob chunk belongs to
	// Wire is the wire format a sample came in (1 or 2). Samples are unique per (wire, UUID):
	// a v2 copy of a sample a v1 client sent supersedes it.
	Wire       int
	Type       string // HealthKit type of samples and tombstones; kind of records
	Kind       string // sample kind (quantity, category, workout...)
	Key        string // records
	Digest     string // records
	Series     string // records of kind series: route, heartbeat, electrocardiogram, quantity...
	SeriesType string // records of kind series: the HealthKit type of the sample
	Start      time.Time
	ReceivedAt time.Time // zero: now
	Data       json.RawMessage
}

// id is the de-duplication key of an item.
func (it *Item) id() string {
	switch {
	case it.Op == "record":
		return "record:" + it.Type + ":" + it.Key + ":" + it.Digest
	case it.Op == "sample" && it.Wire >= 2:
		return fmt.Sprintf("sample@%d:%s", it.Wire, it.UUID)
	default:
		return it.Op + ":" + it.UUID
	}
}

// seenKey is what the seen table stores for an id: 16 bytes of its SHA-256.
func seenKey(id string) []byte {
	sum := sha256.Sum256([]byte(id))
	return sum[:16]
}

// Outcome tells what Ingest did with an item.
type Outcome struct {
	Stored bool // false: a duplicate
	// HadV1 is set for a stored v2 sample whose v1 copy was stored before (and forwarded then).
	HadV1 bool
}

// Store is the archive: staging database plus Parquet files.
type Store struct {
	dir     string // -archive-dir
	parquet string // dir/parquet
	db      *sql.DB
	lock    *os.File
	now     func() time.Time

	// RowGroupRows caps the rows per Parquet row group, and so the memory flush and
	// compaction use (one row group is buffered, compressed, at a time).
	RowGroupRows int64

	ingest sync.Mutex // one batch at a time; erase holds it too
	maint  sync.Mutex // flush, compaction and erase, one at a time

	mu    sync.Mutex
	count int64 // distinct records, see Len
	stats maintStats

	dirMode, fileMode os.FileMode // 0700 and 0600, or 0750 and 0640 with GroupRead

	hook func(stage string) error // tests: fail (as a crash would stop) at a stage
}

// StoreOptions configure OpenStoreWith.
type StoreOptions struct {
	// GroupRead makes the archive readable by the service's group (directories 0750, files
	// 0640), for a backup job or a query tool running as another user in that group. By
	// default only the service's user can read it (0700, 0600).
	GroupRead bool
}

// at lets tests stop maintenance at a given stage, as a crash would.
func (s *Store) at(stage string) error {
	if s.hook != nil {
		return s.hook(stage)
	}
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS staged (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	dataset TEXT NOT NULL,     -- samples or records
	type TEXT NOT NULL,        -- partition: HealthKit type, or record kind
	month TEXT,                -- partition: YYYY-MM of the sample's start; NULL until known
	series TEXT,               -- series chunks: the point dataset they also go to
	ptype TEXT,                -- series chunks: the HealthKit type of their sample
	parent TEXT,               -- series chunks: their sample's UUID
	parent_start INTEGER,      -- series chunks: their sample's start, Unix ns
	fallback_month TEXT,       -- series chunks: month to use if the sample never arrives
	uploader TEXT NOT NULL,
	wire INTEGER NOT NULL,
	received_at INTEGER NOT NULL,
	json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS staged_partition ON staged (dataset, type, month, seq);
CREATE INDEX IF NOT EXISTS staged_series ON staged (series, ptype, month, seq) WHERE series IS NOT NULL;
CREATE INDEX IF NOT EXISTS staged_uploader ON staged (uploader);
CREATE TABLE IF NOT EXISTS seen (
	id BLOB PRIMARY KEY,       -- seenKey(Item.id())
	uploader TEXT NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS deleted (
	uuid TEXT NOT NULL,
	wire INTEGER NOT NULL,     -- 0: every copy (a HealthKit tombstone); 1: the v1 copy, superseded by v2
	type TEXT,
	reason TEXT NOT NULL,      -- deleted or superseded
	uploader TEXT NOT NULL,
	received_at INTEGER NOT NULL,
	json TEXT,
	applied INTEGER NOT NULL DEFAULT 0, -- 1: its rows were removed from every Parquet file
	-- A device's tombstones apply to what that device uploaded (superseded v1 copies: to all).
	PRIMARY KEY (uuid, wire, uploader)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS parents (
	uuid TEXT PRIMARY KEY,
	start INTEGER NOT NULL,
	type TEXT NOT NULL,
	uploader TEXT NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value) WITHOUT ROWID;
-- Journals of maintenance steps in progress, replayed after a crash.
CREATE TABLE IF NOT EXISTS pending_flush (upto INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS pending_compaction (output TEXT PRIMARY KEY, inputs TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS pending_erase (uploader TEXT PRIMARY KEY) WITHOUT ROWID;
`

// OpenStore opens (creating if needed) the archive in dir, finishes maintenance a crash
// interrupted, and locks dir against a second process.
func OpenStore(dir string) (*Store, error) { return OpenStoreWith(dir, StoreOptions{}) }

// OpenStoreWith is OpenStore with options.
func OpenStoreWith(dir string, opt StoreOptions) (*Store, error) {
	dirMode, fileMode := os.FileMode(0o700), os.FileMode(0o600)
	if opt.GroupRead {
		dirMode, fileMode = 0o750, 0o640
	}
	if err := os.MkdirAll(filepath.Join(dir, "parquet"), dirMode); err != nil {
		return nil, err
	}
	// The archive directory, or parquet/ in it, may be a symlink (systemd's DynamicUser makes
	// /var/lib/<name> one; parquet/ may live on another disk). Directory walks do not follow
	// a symlinked root, so both are resolved once here.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	parquetDir, err := filepath.EvalSymlinks(filepath.Join(dir, "parquet"))
	if err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, fileMode)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("%s is in use by another healthkit-relay process (stop the server first): %w", dir, err)
	}
	// SQLite gives its log files the database's mode.
	dbPath := filepath.Join(dir, "staging.db")
	if f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, fileMode); err == nil {
		f.Close()
	}
	// FULL: a commit is on disk before the batch is acknowledged. secure_delete: erased
	// records are overwritten, not left in free pages. journal_size_limit: the log shrinks
	// back after a large migration or flush.
	dsn := "file:" + dbPath + "?_txlock=immediate" +
		"&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(30000)" +
		"&_pragma=secure_delete(ON)&_pragma=cache_size(-32768)&_pragma=journal_size_limit(67108864)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		lock.Close()
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &Store{dir: dir, parquet: parquetDir, db: db, lock: lock, now: time.Now, RowGroupRows: 100_000,
		dirMode: dirMode, fileMode: fileMode}
	if err := s.init(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init() error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("staging.db: %w", err)
	}
	// Databases from 0.5.0: deleted.applied.
	var applied int
	if err := s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('deleted') WHERE name = 'applied'`).Scan(&applied); err != nil {
		return err
	}
	if applied == 0 {
		if _, err := s.db.Exec(`ALTER TABLE deleted ADD COLUMN applied INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	// Databases from 0.5.0 and 0.5.1: tombstones keyed by (uuid, wire) only.
	var scoped int
	if err := s.db.QueryRow(`SELECT pk FROM pragma_table_info('deleted') WHERE name = 'uploader'`).Scan(&scoped); err != nil {
		return err
	}
	if scoped == 0 {
		if err := s.rekeyDeleted(); err != nil {
			return fmt.Errorf("staging.db: upgrade deleted: %w", err)
		}
	}
	// Distinct records are counted as they are stored, so opening never scans the archive.
	var n sql.NullInt64
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'records'`).Scan(&n); errors.Is(err, sql.ErrNoRows) {
		if err := s.recount(s.db); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		s.count = n.Int64
	}
	s.loadStats()
	if err := s.recover(); err != nil {
		return fmt.Errorf("recover: %w", err)
	}
	if err := s.ensureSchemas(); err != nil {
		return err
	}
	return s.chmodAll()
}

// chmodAll sets the modes of what the store keeps in the archive directory (written by an
// earlier version, or with another -group-read): the directory, staging.db and its logs, the
// Parquet tree and the JSON Lines files from before 0.5. Other files there, such as a token
// file or devices.json, keep their own (owner-only) modes.
func (s *Store) chmodAll() error {
	if err := os.Chmod(s.dir, s.dirMode); err != nil {
		return err
	}
	for _, name := range []string{".lock", "staging.db", "staging.db-wal", "staging.db-shm", "staging.db-journal"} {
		if err := os.Chmod(filepath.Join(s.dir, name), s.fileMode); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	roots, _ := filepath.Glob(filepath.Join(s.dir, "[0-9][0-9][0-9][0-9]")) // JSON Lines, by year
	roots = append(roots, s.parquet)
	for _, root := range roots {
		if err := s.chmodTree(root); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) chmodTree(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode := s.fileMode
		if d.IsDir() {
			mode = s.dirMode
		} else if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Mode().Perm() == mode {
			return err
		}
		return os.Chmod(path, mode)
	})
}

// ensureSchemas gives every dataset a file with its schema and no rows
// (<dataset>/type=_/month=_/schema.parquet), and writes deleted.parquet if missing, so that
// queries over a dataset with no data yet (DuckDB's read_parquet fails on a glob matching
// nothing) find empty tables rather than errors.
func (s *Store) ensureSchemas() error {
	for _, ds := range datasets {
		path := filepath.Join(s.partitionDir(ds, "", ""), "schema.parquet")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		var err error
		switch ds {
		case "samples", "records":
			_, err = writeParquetKeep(s, path, func() (Row, error) { return Row{}, io.EOF })
		case "route_points":
			_, err = writeParquetKeep(s, path, func() (RoutePoint, error) { return RoutePoint{}, io.EOF })
		case "heartbeats":
			_, err = writeParquetKeep(s, path, func() (Heartbeat, error) { return Heartbeat{}, io.EOF })
		case "ecg_voltages":
			_, err = writeParquetKeep(s, path, func() (Voltage, error) { return Voltage{}, io.EOF })
		case "quantity_series":
			_, err = writeParquetKeep(s, path, func() (QuantityPoint, error) { return QuantityPoint{}, io.EOF })
		}
		if err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(s.parquet, "deleted", "deleted.parquet")); err != nil {
		return s.exportDeleted()
	}
	return nil
}

func (s *Store) rekeyDeleted() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`CREATE TABLE deleted_new (uuid TEXT NOT NULL, wire INTEGER NOT NULL, type TEXT, reason TEXT NOT NULL,
			uploader TEXT NOT NULL, received_at INTEGER NOT NULL, json TEXT, applied INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (uuid, wire, uploader)) WITHOUT ROWID`,
		`INSERT INTO deleted_new SELECT uuid, wire, type, reason, uploader, received_at, json, applied FROM deleted`,
		`DROP TABLE deleted`,
		`ALTER TABLE deleted_new RENAME TO deleted`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LegacyFiles are the JSON Lines files of versions before 0.5 in the archive directory.
func (s *Store) LegacyFiles() []string {
	files, _ := filepath.Glob(filepath.Join(s.dir, "[0-9]*", "*.jsonl"))
	return files
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// recount sets the record count from the tables (after an erase, or on a new database).
func (s *Store) recount(db execer) error {
	var n int64
	err := db.QueryRow(`SELECT (SELECT count(*) FROM seen) + (SELECT count(*) FROM deleted WHERE reason = 'deleted')`).Scan(&n)
	if err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES ('records', ?)`, n); err != nil {
		return err
	}
	s.mu.Lock()
	s.count = n
	s.mu.Unlock()
	return nil
}

// recover replays the maintenance journals: a flush, compaction or erase a crash cut short.
func (s *Store) recover() error {
	if err := removeTemps(s.parquet); err != nil {
		return err
	}
	if err := s.recoverCompactions(); err != nil {
		return err
	}
	var upto sql.NullInt64
	if err := s.db.QueryRow(`SELECT max(upto) FROM pending_flush`).Scan(&upto); err != nil {
		return err
	}
	if upto.Valid {
		log.Printf("archive: finishing a flush a restart interrupted")
		if _, err := s.flushUpTo(context.Background(), upto.Int64); err != nil {
			return err
		}
	}
	rows, err := s.db.Query(`SELECT uploader FROM pending_erase`)
	if err != nil {
		return err
	}
	var erases []string
	for rows.Next() {
		var u string
		rows.Scan(&u)
		erases = append(erases, u)
	}
	rows.Close()
	for _, u := range erases {
		log.Printf("archive: finishing an erase a restart interrupted")
		if _, err := s.erase(context.Background(), u); err != nil {
			return err
		}
	}
	return nil
}

// Close releases the database and the directory lock.
func (s *Store) Close() error {
	err := s.db.Close()
	if s.lock != nil {
		s.lock.Close()
	}
	return err
}

// Len is the number of distinct records stored (samples, records and tombstones).
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int(s.count)
}

// Staged is the number of rows waiting for the next flush.
func (s *Store) Staged() int64 {
	var n int64
	s.db.QueryRow(`SELECT count(*) FROM staged`).Scan(&n)
	return n
}

func month(t time.Time) string { return t.UTC().Format("2006-01") }

// unixNano is t in Unix nanoseconds, clamped to the years int64 nanoseconds can hold
// (1678-2262): HealthKit dates can be anything a writing app chose.
func unixNano(t time.Time) int64 {
	switch {
	case t.Before(minNanoTime):
		return minNanoTime.UnixNano()
	case t.After(maxNanoTime):
		return maxNanoTime.UnixNano()
	}
	return t.UnixNano()
}

var minNanoTime, maxNanoTime = time.Unix(0, math.MinInt64+1), time.Unix(0, math.MaxInt64-1)

// Ingest durably stores the items that are new, in one transaction, and tells what it did
// with each. uploader is the paired device that sent them ("" for the server token).
func (s *Store) Ingest(uploader string, items []Item) ([]Outcome, error) {
	s.ingest.Lock()
	defer s.ingest.Unlock()
	out := make([]Outcome, len(items))
	now := s.now().UTC()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	prep := func(q string) *sql.Stmt {
		if err != nil {
			return nil
		}
		var st *sql.Stmt
		st, err = tx.Prepare(q)
		return st
	}
	insSeen := prep(`INSERT OR IGNORE INTO seen (id, uploader) VALUES (?, ?)`)
	hasSeen := prep(`SELECT 1 FROM seen WHERE id = ?`)
	insStaged := prep(`INSERT INTO staged (dataset, type, month, series, ptype, parent, parent_start, fallback_month, uploader, wire, received_at, json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	insDeleted := prep(`INSERT OR IGNORE INTO deleted (uuid, wire, type, reason, uploader, received_at, json) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	insParent := prep(`INSERT OR REPLACE INTO parents (uuid, start, type, uploader) VALUES (?, ?, ?, ?)`)
	getParent := prep(`SELECT start, type FROM parents WHERE uuid = ?`)
	if err != nil {
		return nil, err
	}
	seen := func(id string) bool {
		var one int
		return hasSeen.QueryRow(seenKey(id)).Scan(&one) == nil
	}
	var added int64
	for i := range items {
		it := &items[i]
		recv := it.ReceivedAt
		if recv.IsZero() {
			recv = now
		}
		recvNs := recv.UTC().UnixNano()

		if it.Op == "delete" {
			res, err := insDeleted.Exec(it.UUID, 0, nullString(it.Type), "deleted", uploader, recvNs, string(it.Data))
			if err != nil {
				return nil, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				out[i].Stored = true
				added++
			}
			continue
		}

		wire := max(it.Wire, 1)
		if it.Op == "sample" && wire == 1 && seen((&Item{Op: "sample", Wire: 2, UUID: it.UUID}).id()) {
			continue // a v1 copy of a sample already stored in full
		}
		res, err := insSeen.Exec(seenKey(it.id()), uploader)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // a duplicate
		}
		out[i].Stored = true
		added++

		var row struct {
			dataset, typ                                string
			month, series, ptype, parent, fallbackMonth sql.NullString
			parentStart                                 sql.NullInt64
		}
		switch it.Op {
		case "sample":
			row.dataset, row.typ = "samples", it.Type
			row.month = sql.NullString{String: month(it.Start), Valid: true}
			if wire >= 2 && seen((&Item{Op: "sample", Wire: 1, UUID: it.UUID}).id()) {
				out[i].HadV1 = true
				if _, err := insDeleted.Exec(it.UUID, 1, nullString(it.Type), "superseded", uploader, recvNs, nil); err != nil {
					return nil, err
				}
			}
			if it.Kind != "quantity" && it.Kind != "category" {
				// Few samples have series (workouts, routes, heartbeats, ECGs...): remember when
				// they start, for the chunks that arrive before or after them.
				if _, err := insParent.Exec(it.UUID, unixNano(it.Start), it.Type, uploader); err != nil {
					return nil, err
				}
			}
		case "record":
			row.dataset, row.typ = "records", it.Type
			seriesType := it.SeriesType
			switch {
			case it.UUID != "":
				// A chunk belongs to the month of its sample, if the sample is known yet.
				var start int64
				var ptype string
				if err := getParent.QueryRow(it.UUID).Scan(&start, &ptype); err == nil {
					seriesType = cmpOr(seriesType, ptype)
					row.parentStart = sql.NullInt64{Int64: start, Valid: true}
					row.month = sql.NullString{String: month(time.Unix(0, start)), Valid: true}
				} else if !it.Start.IsZero() {
					// Points with their own time (routes, quantity series): the first one's.
					row.month = sql.NullString{String: month(it.Start), Valid: true}
				}
				row.parent = sql.NullString{String: it.UUID, Valid: true}
				row.fallbackMonth = sql.NullString{String: month(recv), Valid: true}
			case !it.Start.IsZero():
				row.month = sql.NullString{String: month(it.Start), Valid: true}
			default:
				row.month = sql.NullString{String: month(recv), Valid: true}
			}
			if it.Series != "" && pointDataset(it.Series) != "" {
				// The points' partition is their sample's type; NULL until known (see flush).
				row.series = sql.NullString{String: it.Series, Valid: true}
				row.ptype = nullString(seriesType)
			}
		}
		if _, err := insStaged.Exec(row.dataset, row.typ, row.month, row.series, row.ptype, row.parent, row.parentStart,
			row.fallbackMonth, uploader, wire, recvNs, string(it.Data)); err != nil {
			return nil, err
		}
	}
	if added > 0 {
		if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('records', ?)
			ON CONFLICT (key) DO UPDATE SET value = value + excluded.value`, added); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.count += added
	s.mu.Unlock()
	return out, nil
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Maintenance status, kept in meta so that alerts survive restarts.

type maintStats struct {
	lastFlush, lastCompaction time.Time
	flushErrors, compactErrs  int64
}

func (s *Store) loadStats() {
	get := func(k string) time.Time {
		var ns int64
		if s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, k).Scan(&ns) == nil {
			return time.Unix(0, ns)
		}
		return time.Time{}
	}
	s.stats.lastFlush, s.stats.lastCompaction = get("last_flush"), get("last_compaction")
}

func (s *Store) recordSuccess(key string, t time.Time) {
	s.db.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)`, key, t.UnixNano())
	s.mu.Lock()
	defer s.mu.Unlock()
	switch key {
	case "last_flush":
		s.stats.lastFlush = t
	case "last_compaction":
		s.stats.lastCompaction = t
	}
}

func (s *Store) recordFailure(job string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job == "flush" {
		s.stats.flushErrors++
	} else {
		s.stats.compactErrs++
	}
}

// Stats returns the maintenance status, for /metrics.
func (s *Store) Stats() maintStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// relPath is a path relative to the Parquet root, for logs and journals.
func (s *Store) relPath(p string) string {
	r, err := filepath.Rel(s.parquet, p)
	if err != nil || strings.HasPrefix(r, "..") {
		return p
	}
	return r
}
