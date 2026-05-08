-- 007_run_lineage: adds parent_run_id to the runs table for lineage tracking.
-- parent_run_id is a nullable self-reference so nested/child runs can be
-- walked up the tree. The indexed column enables efficient subtree queries.

-- UP

ALTER TABLE runs ADD COLUMN parent_run_id TEXT REFERENCES runs(id) ON DELETE SET NULL;
CREATE INDEX idx_runs_parent ON runs(parent_run_id);

-- DOWN

-- SQLite does not support DROP COLUMN directly; recreate the table without it.
-- Mirror the pattern from migration 004 (workspaces table recreation).
CREATE TABLE runs_no_lineage (
    id              TEXT    PRIMARY KEY,
    experiment_id   INTEGER NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
    name            TEXT,
    status          TEXT    NOT NULL DEFAULT 'RUNNING',
    start_time      INTEGER NOT NULL,
    end_time        INTEGER,
    artifact_uri    TEXT    NOT NULL,
    lifecycle_stage TEXT    NOT NULL DEFAULT 'active',
    user_id         TEXT,
    source_type     TEXT,
    source_name     TEXT,
    run_kind        TEXT    NOT NULL DEFAULT 'classic',
    CHECK (status IN ('RUNNING','SCHEDULED','FINISHED','FAILED','KILLED')),
    CHECK (lifecycle_stage IN ('active','deleted'))
);

INSERT INTO runs_no_lineage(id, experiment_id, name, status, start_time, end_time, artifact_uri, lifecycle_stage, user_id, source_type, source_name, run_kind)
SELECT id, experiment_id, name, status, start_time, end_time, artifact_uri, lifecycle_stage, user_id, source_type, source_name, run_kind
FROM runs;

DROP INDEX IF EXISTS idx_runs_parent;
DROP TABLE runs;
ALTER TABLE runs_no_lineage RENAME TO runs;
