package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Erase permanently deletes everything one uploader (a paired device, or "" for the server
// token) sent: its Parquet rows, its staged rows, its tombstones, and its entries in the
// de-duplication index, so that it can upload its history again from scratch. Nothing is kept:
// Parquet files are rewritten without its rows (or removed), SQLite overwrites deleted
// content (secure_delete) and the write-ahead log is truncated.
//
// It is journaled (pending_erase) and idempotent: an erase cut short is finished on the next
// start, before the server accepts uploads again. It returns the number of records erased.
func (s *Store) Erase(ctx context.Context, uploader string) (int64, error) {
	s.ingest.Lock()
	defer s.ingest.Unlock()
	s.maint.Lock()
	defer s.maint.Unlock()
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO pending_erase (uploader) VALUES (?)`, uploader); err != nil {
		return 0, err
	}
	return s.erase(ctx, uploader)
}

func (s *Store) erase(ctx context.Context, uploader string) (int64, error) {
	start := time.Now()
	var erased int64
	if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM seen WHERE uploader = ?) +
		(SELECT count(*) FROM deleted WHERE uploader = ? AND reason = 'deleted')`, uploader, uploader).Scan(&erased); err != nil {
		return 0, err
	}

	removed, rewritten := 0, 0
	for _, dataset := range datasets {
		parts, err := s.partitionFiles(dataset)
		if err != nil {
			return 0, err
		}
		for _, files := range parts {
			for _, path := range files {
				if err := ctx.Err(); err != nil {
					return 0, err
				}
				action, err := s.eraseFile(dataset, path, uploader)
				if err != nil {
					return 0, fmt.Errorf("erase %s: %w", s.relPath(path), err)
				}
				switch action {
				case "removed":
					removed++
				case "rewritten":
					rewritten++
				}
				if err := s.at("erase-file"); err != nil {
					return 0, err
				}
			}
		}
		removeEmptyDirs(filepath.Join(s.parquet, dataset))
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM staged WHERE uploader = ?`,
		`DELETE FROM seen WHERE uploader = ?`,
		`DELETE FROM deleted WHERE uploader = ?`,
		`DELETE FROM parents WHERE uploader = ?`,
		`DELETE FROM pending_erase WHERE uploader = ?`,
	} {
		if _, err := tx.Exec(q, uploader); err != nil {
			return 0, err
		}
	}
	if err := s.recount(tx); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if err := s.exportDeleted(); err != nil {
		return erased, err
	}
	// Deleted pages are zeroed (secure_delete); VACUUM gives the space back, and the log,
	// which may still hold copies, is checkpointed and truncated.
	if _, err := s.db.Exec(`VACUUM`); err != nil {
		return erased, err
	}
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return erased, err
	}
	log.Printf("archive: erased %d records of %s: %d files removed, %d rewritten, in %s",
		erased, uploaderName(uploader), removed, rewritten, time.Since(start).Round(time.Millisecond))
	return erased, nil
}

func uploaderName(u string) string {
	if u == "" {
		return "the server token"
	}
	return "device " + u
}

// eraseFile removes or rewrites one Parquet file without the uploader's rows. Row group
// statistics usually settle it without reading rows: a file only that uploader wrote is
// removed, one it has no rows in is left alone.
func (s *Store) eraseFile(dataset, path, uploader string) (string, error) {
	if n, err := parquetRows(path); err != nil {
		return "", err
	} else if n == 0 {
		return "kept", nil // a schema file
	}
	lo, hi, ok, err := columnRange(path, "uploader")
	if err != nil {
		return "", err
	}
	if ok && lo == uploader && hi == uploader {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		return "removed", syncDir(filepath.Dir(path))
	}
	if ok && (uploader < lo || uploader > hi) {
		return "kept", nil
	}
	switch dataset {
	case "samples", "records":
		err = rewriteWithout(s, path, func(r *Row) bool { return r.Uploader == uploader })
	case "route_points":
		err = rewriteWithout(s, path, func(r *RoutePoint) bool { return r.Uploader == uploader })
	case "heartbeats":
		err = rewriteWithout(s, path, func(r *Heartbeat) bool { return r.Uploader == uploader })
	case "ecg_voltages":
		err = rewriteWithout(s, path, func(r *Voltage) bool { return r.Uploader == uploader })
	case "quantity_series":
		err = rewriteWithout(s, path, func(r *QuantityPoint) bool { return r.Uploader == uploader })
	}
	return "rewritten", err
}

// rewriteWithout replaces a file by a copy without the rows drop matches (atomically: the
// copy is renamed over it). A file left with no rows is removed.
func rewriteWithout[T any](s *Store, path string, drop func(*T) bool) error {
	// The copy goes to a temp file next to it; the original stays intact until the rename.
	return mergeFiles(s, path, []string{path}, nil, drop)
}

// eraseHandler serves DELETE /v1/data: a device erases what it uploaded. The server token
// erases what was uploaded with the server token, or, with ?device=<id>, what that device
// uploaded (also after it was revoked).
func (s *Server) eraseHandler(w http.ResponseWriter, r *http.Request) {
	c, ok := s.caller(r)
	if !ok {
		s.unauthorized(w)
		return
	}
	// confirm=erase guards against an erase by mistake (a bare curl -X DELETE). The app before
	// 0.6 does not send it, so a request without it is still accepted when it comes from the
	// app: a paired device, or iOS's URL loading system (a CFNetwork user agent).
	switch r.URL.Query().Get("confirm") {
	case "erase":
	case "":
		if c.Device == nil && !strings.Contains(r.UserAgent(), "CFNetwork/") {
			httpError(w, http.StatusBadRequest, "add ?confirm=erase to erase; nothing was erased")
			return
		}
		log.Printf("erase: request without confirm=erase (app before 0.6)")
	default:
		httpError(w, http.StatusBadRequest, "confirm must be \"erase\"; nothing was erased")
		return
	}
	uploader := ""
	switch {
	case c.Device != nil:
		uploader = c.Device.ID
	case r.URL.Query().Get("device") != "":
		uploader = r.URL.Query().Get("device")
	}
	// Rewriting files can take a while with a long history and several devices.
	http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Minute))
	n, err := s.Archive.Erase(context.WithoutCancel(r.Context()), uploader)
	if err != nil {
		log.Printf("erase: %v", err)
		httpError(w, http.StatusInternalServerError, "could not erase: "+err.Error())
		return
	}
	s.count("erased")
	writeJSON(w, http.StatusOK, map[string]any{"erased": n, "device": uploader})
}
