package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"path/filepath"
	"time"
)

// Flush moves staged rows to Parquet: one new file per partition present in staging, named
// after the flush (f<seq>.parquet, seq being the last staged row it covers), then deletes
// those rows from staging.
//
// Crash safety: the flush is journaled (pending_flush) before any file is written, files are
// renamed into place only once complete and fsynced, and the staged rows are deleted only after
// every file is in place, in one transaction with the end of the journal. A flush cut short is
// replayed on the next start with the same seq: it writes the same files under the same names,
// replacing any it wrote before, so nothing is lost or duplicated.
//
// Series chunks whose sample is not known yet (a chunk may arrive in a batch before its
// sample's) wait in staging for up to a day, then go to the month they were received in.
// force flushes them at once (migration, tests).
func (s *Store) Flush(ctx context.Context, force bool) (int64, error) {
	cutoff := s.now().Add(-24 * time.Hour).UnixNano()
	if force {
		cutoff = math.MaxInt64
	}
	return s.flushBefore(ctx, cutoff)
}

// flushBefore is Flush, sending chunks still without their sample to their fallback month
// if they were received before cutoff (Unix ns).
func (s *Store) flushBefore(ctx context.Context, cutoff int64) (int64, error) {
	s.maint.Lock()
	defer s.maint.Unlock()
	n, err := s.flush(ctx, cutoff)
	if err != nil {
		if ctx.Err() == nil { // not a shutdown
			s.recordFailure("flush")
		}
		return n, err
	}
	s.recordSuccess("last_flush", s.now())
	return n, nil
}

func (s *Store) flush(ctx context.Context, cutoff int64) (int64, error) {
	// Chunks staged before their sample: their sample's start, now that it is known.
	if _, err := s.db.ExecContext(ctx, `UPDATE staged SET
			parent_start = (SELECT start FROM parents WHERE parents.uuid = staged.parent),
			month = (SELECT strftime('%Y-%m', start / 1000000000, 'unixepoch') FROM parents WHERE parents.uuid = staged.parent)
		WHERE month IS NULL AND parent IN (SELECT uuid FROM parents)`); err != nil {
		return 0, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE staged SET month = fallback_month WHERE month IS NULL AND received_at < ?`, cutoff); err != nil {
		return 0, err
	}
	// Points of a chunk that did not name its sample's type: the sample's, else the series name.
	if _, err := s.db.ExecContext(ctx, `UPDATE staged SET
			ptype = coalesce((SELECT type FROM parents WHERE parents.uuid = staged.parent), series)
		WHERE series IS NOT NULL AND ptype IS NULL AND month IS NOT NULL`); err != nil {
		return 0, err
	}

	var upto sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT max(seq) FROM staged WHERE month IS NOT NULL`).Scan(&upto); err != nil {
		return 0, err
	}
	if !upto.Valid {
		return 0, s.exportDeleted()
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO pending_flush (upto) VALUES (?)`, upto.Int64); err != nil {
		return 0, err
	}
	return s.flushUpTo(ctx, upto.Int64)
}

type partition struct{ dataset, typ, month string }

// flushUpTo writes the staged rows up to seq upto (with a month) to Parquet and removes them
// from staging; see Flush. It is idempotent until it returns.
func (s *Store) flushUpTo(ctx context.Context, upto int64) (int64, error) {
	name := fmt.Sprintf("f%d.parquet", upto)
	var parts, seriesParts []partition
	if err := s.queryPartitions(ctx, &parts, `SELECT DISTINCT dataset, type, month FROM staged WHERE seq <= ? AND month IS NOT NULL`, upto); err != nil {
		return 0, err
	}
	if err := s.queryPartitions(ctx, &seriesParts, `SELECT DISTINCT series, ptype, month FROM staged
		WHERE seq <= ? AND month IS NOT NULL AND series IS NOT NULL`, upto); err != nil {
		return 0, err
	}

	var total int64
	for _, p := range parts {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		rows, err := s.db.QueryContext(ctx, `SELECT uploader, wire, received_at, json FROM staged
			WHERE dataset = ? AND type = ? AND month = ? AND seq <= ? ORDER BY seq`, p.dataset, p.typ, p.month, upto)
		if err != nil {
			return total, err
		}
		n, err := writeParquet(s, filepath.Join(s.partitionDir(p.dataset, p.typ, p.month), name), func() (Row, error) {
			if !rows.Next() {
				if err := rows.Err(); err != nil {
					return Row{}, err
				}
				return Row{}, io.EOF
			}
			var uploader, raw string
			var wire int
			var recv int64
			if err := rows.Scan(&uploader, &wire, &recv, &raw); err != nil {
				return Row{}, err
			}
			return sampleRow(p.dataset, p.typ, uploader, wire, recv, raw), nil
		})
		rows.Close()
		if err != nil {
			return total, fmt.Errorf("flush %s/%s/%s: %w", p.dataset, p.typ, p.month, err)
		}
		total += n
		if err := s.at("flush-file"); err != nil {
			return total, err
		}
	}
	for _, p := range seriesParts {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		dataset := pointDataset(p.dataset) // p.dataset holds the series name here
		path := filepath.Join(s.partitionDir(dataset, p.typ, p.month), name)
		var err error
		switch dataset {
		case "route_points":
			_, err = flushPoints(ctx, s, p, upto, path, routePoints)
		case "heartbeats":
			_, err = flushPoints(ctx, s, p, upto, path, heartbeats)
		case "ecg_voltages":
			_, err = flushPoints(ctx, s, p, upto, path, voltages)
		case "quantity_series":
			_, err = flushPoints(ctx, s, p, upto, path, quantityPoints)
		}
		if err != nil {
			return total, fmt.Errorf("flush %s/%s/%s: %w", dataset, p.typ, p.month, err)
		}
	}
	if err := s.exportDeleted(); err != nil {
		return total, err
	}
	if err := s.at("flush-commit"); err != nil {
		return total, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return total, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM staged WHERE seq <= ? AND month IS NOT NULL`, upto); err != nil {
		return total, err
	}
	if _, err := tx.Exec(`DELETE FROM pending_flush`); err != nil {
		return total, err
	}
	if err := tx.Commit(); err != nil {
		return total, err
	}
	if total > 0 {
		log.Printf("archive: flushed %d rows to %d partitions", total, len(parts))
	}
	return total, nil
}

func (s *Store) queryPartitions(ctx context.Context, out *[]partition, q string, args ...any) error {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p partition
		if err := rows.Scan(&p.dataset, &p.typ, &p.month); err != nil {
			return err
		}
		*out = append(*out, p)
	}
	return rows.Err()
}

// flushPoints writes the points of the staged chunks of one series partition.
func flushPoints[T any](ctx context.Context, s *Store, p partition, upto int64, path string,
	convert func(c chunk, uploader string, recvNs int64, parentStart *int64, out []T) []T) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT uploader, received_at, parent_start, json FROM staged
		WHERE series = ? AND ptype = ? AND month = ? AND seq <= ? ORDER BY seq`, p.dataset, p.typ, p.month, upto)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var pending []T
	return writeParquet(s, path, func() (T, error) {
		var zero T
		for len(pending) == 0 {
			if !rows.Next() {
				if err := rows.Err(); err != nil {
					return zero, err
				}
				return zero, io.EOF
			}
			var uploader, raw string
			var recv int64
			var start sql.NullInt64
			if err := rows.Scan(&uploader, &recv, &start, &raw); err != nil {
				return zero, err
			}
			c, err := parseChunk(raw)
			if err != nil {
				continue // validated on receipt; the record itself is in records either way
			}
			var ps *int64
			if start.Valid {
				ps = &start.Int64
			}
			pending = convert(c, uploader, recv, ps, pending[:0])
		}
		row := pending[0]
		pending = pending[1:]
		return row, nil
	})
}

func pointMaps(c chunk, fn func(i int32, m map[string]json.RawMessage)) {
	for i, raw := range c.Points {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) == nil {
			fn(int32(i), m)
		}
	}
}

func routePoints(c chunk, uploader string, recv int64, _ *int64, out []RoutePoint) []RoutePoint {
	pointMaps(c, func(i int32, m map[string]json.RawMessage) {
		p := RoutePoint{SampleUUID: c.SampleUUID, Chunk: c.Index, I: i, Time: parseTimeMicros(str(m, "t")),
			Lat: num(m, "lat"), Lon: num(m, "lon"), Alt: num(m, "alt"), HAcc: num(m, "h_acc"), VAcc: num(m, "v_acc"),
			Speed: num(m, "speed"), SpeedAcc: num(m, "speed_acc"), Course: num(m, "course"), CourseAcc: num(m, "course_acc"),
			EllipsoidalAlt: num(m, "ellipsoidal_alt"), Uploader: uploader, ReceivedAt: recv / 1000}
		if f := num(m, "floor"); f != nil {
			p.Floor = ptr(int32(*f))
		}
		out = append(out, p)
	})
	return out
}

func heartbeats(c chunk, uploader string, recv int64, start *int64, out []Heartbeat) []Heartbeat {
	pointMaps(c, func(i int32, m map[string]json.RawMessage) {
		var t float64
		if f := num(m, "t"); f != nil {
			t = *f
		}
		var gap bool
		json.Unmarshal(m["gap"], &gap)
		out = append(out, Heartbeat{SampleUUID: c.SampleUUID, Chunk: c.Index, I: i, Time: pointTime(start, t), T: t, Gap: gap,
			Uploader: uploader, ReceivedAt: recv / 1000})
	})
	return out
}

func voltages(c chunk, uploader string, recv int64, start *int64, out []Voltage) []Voltage {
	pointMaps(c, func(i int32, m map[string]json.RawMessage) {
		var t float64
		if f := num(m, "t"); f != nil {
			t = *f
		}
		out = append(out, Voltage{SampleUUID: c.SampleUUID, Chunk: c.Index, I: i, Time: pointTime(start, t), T: t,
			Microvolts: num(m, "v"), Uploader: uploader, ReceivedAt: recv / 1000})
	})
	return out
}

func quantityPoints(c chunk, uploader string, recv int64, _ *int64, out []QuantityPoint) []QuantityPoint {
	pointMaps(c, func(i int32, m map[string]json.RawMessage) {
		out = append(out, QuantityPoint{SampleUUID: c.SampleUUID, Chunk: c.Index, I: i,
			Start: parseTimeMicros(str(m, "start")), End: parseTimeMicros(str(m, "end")), Value: num(m, "value"),
			Unit: optString(cmpOr(str(m, "unit"), c.Unit)), Uploader: uploader, ReceivedAt: recv / 1000})
	})
	return out
}

// exportDeleted rewrites parquet/deleted/deleted.parquet from the deleted table, so that
// DuckDB alone can hide what was deleted but not compacted away yet. The file always exists,
// empty if need be, so queries never fail on a missing file.
func (s *Store) exportDeleted() error {
	rows, err := s.db.Query(`SELECT uuid, wire, type, reason, uploader, received_at, json FROM deleted ORDER BY uuid, wire`)
	if err != nil {
		return err
	}
	defer rows.Close()
	_, err = writeParquetKeep(s, filepath.Join(s.parquet, "deleted", "deleted.parquet"), func() (DeletedRow, error) {
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return DeletedRow{}, err
			}
			return DeletedRow{}, io.EOF
		}
		var d DeletedRow
		var typ, raw sql.NullString
		var recv int64
		if err := rows.Scan(&d.UUID, &d.Wire, &typ, &d.Reason, &d.Uploader, &recv, &raw); err != nil {
			return d, err
		}
		d.ReceivedAt = recv / 1000
		if typ.Valid {
			d.Type = &typ.String
		}
		if raw.Valid {
			d.JSON = &raw.String
		}
		return d, nil
	})
	return err
}
