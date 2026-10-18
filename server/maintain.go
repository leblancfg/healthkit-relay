package main

import (
	"context"
	"fmt"
	"log"
	"time"
)

// Maintain flushes staged rows every flushEvery and compacts the archive every day at
// compactAt (local time, "HH:MM"), until ctx is done. Failures are logged and counted
// (healthkit_relay_maintenance_errors_total) and retried at the next tick; the work is
// re-runnable, so nothing is lost meanwhile.
func (s *Store) Maintain(ctx context.Context, flushEvery time.Duration, compactAt string) error {
	at, err := time.Parse("15:04", compactAt)
	if err != nil {
		return fmt.Errorf("-compact-at must be HH:MM: %w", err)
	}
	go func() {
		flush := time.NewTicker(flushEvery)
		defer flush.Stop()
		compact := time.NewTimer(time.Until(nextAt(s.now(), at)))
		defer compact.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-flush.C:
				if _, err := s.Flush(ctx, false); err != nil && ctx.Err() == nil {
					log.Printf("archive: flush failed: %v", err)
				}
			case <-compact.C:
				// Flush first, so the day's last rows are merged too.
				if _, err := s.Flush(ctx, false); err != nil && ctx.Err() == nil {
					log.Printf("archive: flush failed: %v", err)
				}
				if _, err := s.Compact(ctx); err != nil && ctx.Err() == nil {
					log.Printf("archive: compaction failed: %v", err)
				}
				compact.Reset(time.Until(nextAt(s.now(), at)))
			}
		}
	}()
	return nil
}

// nextAt is the next time of day at (hour and minute) after now, in now's location.
func nextAt(now time.Time, at time.Time) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), at.Hour(), at.Minute(), 0, 0, now.Location())
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}
