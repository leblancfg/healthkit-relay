-- DuckDB views over the healthkit-relay Parquet archive.
--
-- Paths are relative to the archive directory (-archive-dir), so start DuckDB there:
--
--   cd /var/lib/healthkit-relay && duckdb -init /path/to/views.sql
--
-- or replace 'parquet/' below with an absolute path. DuckDB only reads the files: it can run
-- while the server does (a flush or compaction swaps whole files in with a rename).
--
-- Each view hides what was deleted but not compacted away yet (compaction runs nightly):
-- samples HealthKit reported as deleted, with their series points (a device's tombstones
-- apply to what that device uploaded), and v1 copies of samples a v2 client sent again. The
-- hive partition columns are `type` and `month` (YYYY-MM of the sample's start, UTC);
-- filtering on them reads only the matching files. Rows received in the last hour may still
-- be in staging.db (flushed hourly).

-- Tombstones: wire 0 hides the sample the same uploader sent, wire 1 only its v1 copy.
CREATE OR REPLACE VIEW deleted AS
SELECT * FROM read_parquet('parquet/deleted/deleted.parquet');

-- Every sample: uuid, type, kind, start, end, value, unit, source_*, device, uploader, wire,
-- received_at, and json, the sample exactly as the app sent it.
CREATE OR REPLACE VIEW samples AS
SELECT s.* FROM read_parquet('parquet/samples/*/*/*.parquet', hive_partitioning = true, union_by_name = true) s
WHERE NOT EXISTS (SELECT 1 FROM deleted d WHERE d.uuid = s.uuid
                  AND ((d.wire = 0 AND d.uploader = s.uploader) OR (d.wire = 1 AND s.wire = 1)));

-- Records without a sample UUID (type is the record kind: activity_summary, characteristics,
-- medication, series and blob chunks...). uuid is the sample a chunk belongs to. A record that
-- changes (today's activity summary) is stored again with each change: see records_latest.
CREATE OR REPLACE VIEW records AS
SELECT r.* FROM read_parquet('parquet/records/*/*/*.parquet', hive_partitioning = true, union_by_name = true) r
WHERE NOT EXISTS (SELECT 1 FROM deleted d WHERE d.wire = 0 AND d.uuid = r.uuid AND d.uploader = r.uploader);

CREATE OR REPLACE VIEW records_latest AS
SELECT * FROM records
QUALIFY row_number() OVER (PARTITION BY type, key ORDER BY received_at DESC) = 1;

-- Series, one row per point; sample_uuid joins samples.uuid, chunk and i give the order.
CREATE OR REPLACE VIEW route_points AS
SELECT p.* FROM read_parquet('parquet/route_points/*/*/*.parquet', hive_partitioning = true, union_by_name = true) p
WHERE NOT EXISTS (SELECT 1 FROM deleted d WHERE d.wire = 0 AND d.uuid = p.sample_uuid AND d.uploader = p.uploader);

CREATE OR REPLACE VIEW heartbeats AS
SELECT p.* FROM read_parquet('parquet/heartbeats/*/*/*.parquet', hive_partitioning = true, union_by_name = true) p
WHERE NOT EXISTS (SELECT 1 FROM deleted d WHERE d.wire = 0 AND d.uuid = p.sample_uuid AND d.uploader = p.uploader);

CREATE OR REPLACE VIEW ecg_voltages AS
SELECT p.* FROM read_parquet('parquet/ecg_voltages/*/*/*.parquet', hive_partitioning = true, union_by_name = true) p
WHERE NOT EXISTS (SELECT 1 FROM deleted d WHERE d.wire = 0 AND d.uuid = p.sample_uuid AND d.uploader = p.uploader);

CREATE OR REPLACE VIEW quantity_series AS
SELECT p.* FROM read_parquet('parquet/quantity_series/*/*/*.parquet', hive_partitioning = true, union_by_name = true) p
WHERE NOT EXISTS (SELECT 1 FROM deleted d WHERE d.wire = 0 AND d.uuid = p.sample_uuid AND d.uploader = p.uploader);
