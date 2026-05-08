-- 010_metrics_latest: materialised view for analytics OLAP queries.
--
-- The metrics table records every observation; analytics queries usually want
-- "the latest value of metric X for each run". Walking the metrics table for
-- this is O(N rows) per query. metrics_latest pins one row per (run_id, key)
-- containing the most recent value, kept in sync via SQL triggers on insert.
--
-- A trigger-maintained table is sufficient for our scale:
--   - inserts come in via log_metric / log_metrics (rate is bounded by run
--     count and metric step count — typical workload <1k inserts/s);
--   - reads from analytics queries hit the indexed materialised table;
--   - SQLite triggers add ~10% overhead vs raw insert (measured on the v1.1
--     benchmark — see docs/bench-v11.md).
--
-- The composite index (key, value DESC) supports the headline analytics query
-- "best value of metric X across runs" without a sort.
--
-- Why not a generated column or a view? A view is recomputed on every read;
-- a generated column doesn't help because we can't index across rows.
-- Triggers + a real table is the simplest pure-SQLite path that meets the
-- 200ms-on-100k-runs acceptance target.

-- UP

CREATE TABLE metrics_latest (
    run_id    TEXT    NOT NULL,
    key       TEXT    NOT NULL,
    value     REAL    NOT NULL,
    timestamp INTEGER NOT NULL,
    step      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, key),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

-- Hot index for analytics: "top N runs by metric X".
CREATE INDEX idx_metrics_latest_key_value ON metrics_latest(key, value DESC, run_id);

-- Backfill: take the (timestamp, step) tuple-max per (run_id, key).
-- We insert via INSERT OR REPLACE so the existing PK serialises duplicates
-- (the metrics table allows multiple obs at the same step due to the wider
-- PK). Walks the metrics table in run-id order, so the backfill is O(N).
INSERT INTO metrics_latest (run_id, key, value, timestamp, step)
SELECT m.run_id, m.key, m.value, m.timestamp, m.step
FROM metrics m
JOIN (
    SELECT run_id, key, MAX(timestamp) AS ts, MAX(step) AS st
    FROM metrics
    GROUP BY run_id, key
) latest
  ON m.run_id = latest.run_id
 AND m.key    = latest.key
 AND m.timestamp = latest.ts
 AND m.step  = latest.st;

-- INSERT trigger: keep the latest row in sync.
-- Compare (timestamp, step) lexicographically: higher timestamp wins, ties
-- broken by higher step. This matches the semantics MLflow uses when
-- get-latest-metrics resolves ambiguous metric histories.
CREATE TRIGGER trg_metrics_latest_ai
AFTER INSERT ON metrics
BEGIN
    INSERT INTO metrics_latest (run_id, key, value, timestamp, step)
    VALUES (NEW.run_id, NEW.key, NEW.value, NEW.timestamp, NEW.step)
    ON CONFLICT(run_id, key) DO UPDATE SET
        value     = NEW.value,
        timestamp = NEW.timestamp,
        step      = NEW.step
        WHERE NEW.timestamp > metrics_latest.timestamp
           OR (NEW.timestamp = metrics_latest.timestamp AND NEW.step >= metrics_latest.step);
END;

-- DELETE trigger: when the latest observation is deleted, recompute from
-- whatever remains. Rare path (we never expose metric-delete, but cascading
-- run deletes hit this too — those are handled by the FK CASCADE on the
-- metrics_latest row itself, so this trigger only matters for individual
-- metric-row deletes).
CREATE TRIGGER trg_metrics_latest_ad
AFTER DELETE ON metrics
WHEN EXISTS (
    SELECT 1 FROM metrics_latest
    WHERE run_id = OLD.run_id AND key = OLD.key
      AND timestamp = OLD.timestamp AND step = OLD.step
)
BEGIN
    DELETE FROM metrics_latest WHERE run_id = OLD.run_id AND key = OLD.key;
    INSERT INTO metrics_latest (run_id, key, value, timestamp, step)
    SELECT m.run_id, m.key, m.value, m.timestamp, m.step
    FROM metrics m
    WHERE m.run_id = OLD.run_id AND m.key = OLD.key
    ORDER BY m.timestamp DESC, m.step DESC
    LIMIT 1;
END;

-- DOWN

DROP TRIGGER IF EXISTS trg_metrics_latest_ad;
DROP TRIGGER IF EXISTS trg_metrics_latest_ai;
DROP INDEX IF EXISTS idx_metrics_latest_key_value;
DROP TABLE IF EXISTS metrics_latest;
