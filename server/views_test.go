package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The DuckDB views (sql/views.sql) read the archive and hide deleted and superseded rows.
// Needs the duckdb command (or $DUCKDB); skipped without it.
func TestDuckDBViews(t *testing.T) {
	duckdb := os.Getenv("DUCKDB")
	if duckdb == "" {
		var err error
		if duckdb, err = exec.LookPath("duckdb"); err != nil {
			t.Skip("duckdb not installed")
		}
	}
	views, err := filepath.Abs("sql/views.sql")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ts, _ := newTestServer(t, dir)
	// hrSample in v1 then v2 (the v1 copy is superseded), a deleted sample, series of each kind.
	mustPost(t, ts.URL, testToken, `{"samples":[`+hrSample+`,`+sleepSample+`]}`)
	v2 := strings.Replace(hrSample, `"future_field"`, `"device":{"name":"Apple Watch"},"future_field"`, 1)
	mustPost(t, ts.URL, testToken, `{"version":2,"samples":[`+v2+`,`+hbSample+`,`+ecgV2+`,`+routeSample+`],
		"records":[`+hbChunk+`,`+ecgChunk+`,`+routeChunk+`,`+stepsChunk+`,`+summary+`,`+characteristics+`],"deletions":[]}`)
	changed := strings.Replace(summary, `"value":512`, `"value":600`, 1)
	mustPost(t, ts.URL, testToken, `{"version":2,"samples":[],"records":[`+changed+`],
		"deletions":[{"uuid":"2b4e28ba-2fa1-11d2-883f-0016d3cca427"},{"uuid":"`+hbUUID+`"}]}`)
	if _, err := ts.store.Flush(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	query := func(sql string) string {
		t.Helper()
		cmd := exec.Command(duckdb, "-csv", "-noheader", "-init", views, "-c", sql)
		cmd.Dir = dir
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil || strings.Contains(stderr.String(), "Error") {
			t.Fatalf("duckdb %q: %v\n%s%s", sql, err, out, stderr.String())
		}
		return strings.TrimSpace(string(out))
	}
	for sql, want := range map[string]string{
		// The v1 heart rate (superseded), the sleep sample and the heartbeat series (deleted) are hidden.
		`SELECT string_agg(type || ':' || wire, ' ' ORDER BY type) FROM samples`:                                               "HKDataTypeIdentifierElectrocardiogram:2 HKQuantityTypeIdentifierHeartRate:2 HKWorkoutRouteTypeIdentifier:2",
		`SELECT month, device, value FROM samples WHERE type = 'HKQuantityTypeIdentifierHeartRate'`:                            "2026-01,Apple Watch,62.0",
		`SELECT count(*) FROM samples WHERE json_extract(json, '$.future_field.kept')::bool`:                                   "1",
		`SELECT count(*) FROM heartbeats`:                                                                                      "0",
		`SELECT count(*) FROM route_points`:                                                                                    "2",
		`SELECT count(*) FROM ecg_voltages`:                                                                                    "1",
		`SELECT sum(value) FROM quantity_series`:                                                                               "42.0",
		`SELECT count(*) FROM records WHERE type = 'series'`:                                                                   "3",
		`SELECT json_extract(json, '$.values.active_energy_burned.value') FROM records_latest WHERE type = 'activity_summary'`: "600",
		`SELECT count(*) FROM deleted`:                                                                                         "3",
	} {
		if got := query(sql); got != want {
			t.Errorf("%s\n got: %s\nwant: %s", sql, got, want)
		}
	}
}
