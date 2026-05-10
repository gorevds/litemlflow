-- 013_events: append-only event log for time-travel (v1.5).
--
-- Mirrors mutations on runs (status, end_time, name, lifecycle, parent_run_id)
-- and run-level tag set/delete so the lineage handler can reconstruct
-- "what did this run look like at timestamp T" via replay.
--
-- Design: pure append, row per mutation, payload JSON. Reads are cheap
-- because the table is indexed on (entity_type, entity_id, ts_ms). For a
-- single run the typical replay reads <100 rows.
--
-- Out of scope for v1.5-rc1: experiment events, metric/param events
-- (metrics/params are already append-only at write time and have native
-- timestamps that make as-of filtering free — no event row needed).

-- UP

CREATE TABLE events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts_ms       INTEGER NOT NULL,             -- unix ms when the mutation happened
    kind        TEXT    NOT NULL,             -- run_update | run_lifecycle | run_parent | tag_set | tag_delete
    entity_type TEXT    NOT NULL,             -- 'run' for v1.5; reserved for 'experiment' etc.
    entity_id   TEXT    NOT NULL,             -- the run.id (or future experiment_id as text)
    payload     TEXT    NOT NULL DEFAULT '{}',-- JSON; shape varies by kind
    CHECK (kind IN ('run_update','run_lifecycle','run_parent','tag_set','tag_delete'))
);

-- Composite index for the canonical replay query:
--   SELECT * FROM events
--   WHERE entity_type = ? AND entity_id = ? AND ts_ms <= ?
--   ORDER BY ts_ms ASC, id ASC
CREATE INDEX idx_events_entity_ts
    ON events(entity_type, entity_id, ts_ms, id);

-- Audit-friendly lookup: "all events at exactly time T across the system"
-- is rare but cheap with this index when needed.
CREATE INDEX idx_events_ts ON events(ts_ms);

-- DOWN

DROP INDEX IF EXISTS idx_events_ts;
DROP INDEX IF EXISTS idx_events_entity_ts;
DROP TABLE IF EXISTS events;
