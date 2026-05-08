-- 001_init: initial schema for LiteMLflow.
-- Covers experiments, runs, metrics, params, tags, artifacts, traces, prompts, evals.
-- See docs/spec/data-model.md for the canonical reference.

-- UP

CREATE TABLE experiments (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT    NOT NULL UNIQUE,
    artifact_location TEXT    NOT NULL,
    lifecycle_stage   TEXT    NOT NULL DEFAULT 'active',
    creation_time     INTEGER NOT NULL,
    last_update_time  INTEGER NOT NULL,
    CHECK (lifecycle_stage IN ('active','deleted'))
);

CREATE TABLE experiment_tags (
    experiment_id INTEGER NOT NULL,
    key           TEXT    NOT NULL,
    value         TEXT    NOT NULL,
    PRIMARY KEY (experiment_id, key),
    FOREIGN KEY (experiment_id) REFERENCES experiments(id) ON DELETE CASCADE
);

CREATE TABLE runs (
    id              TEXT    PRIMARY KEY,
    experiment_id   INTEGER NOT NULL,
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
    FOREIGN KEY (experiment_id) REFERENCES experiments(id) ON DELETE CASCADE,
    CHECK (status IN ('RUNNING','SCHEDULED','FINISHED','FAILED','KILLED')),
    CHECK (lifecycle_stage IN ('active','deleted')),
    CHECK (run_kind IN ('classic','trace','eval'))
);

CREATE INDEX idx_runs_experiment_status ON runs(experiment_id, status, start_time DESC);
CREATE INDEX idx_runs_lifecycle ON runs(lifecycle_stage);

CREATE TABLE metrics (
    run_id    TEXT    NOT NULL,
    key       TEXT    NOT NULL,
    value     REAL    NOT NULL,
    timestamp INTEGER NOT NULL,
    step      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, key, timestamp, step),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX idx_metrics_run_key ON metrics(run_id, key, step);

CREATE TABLE params (
    run_id TEXT NOT NULL,
    key    TEXT NOT NULL,
    value  TEXT NOT NULL,
    PRIMARY KEY (run_id, key),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE tags (
    run_id TEXT NOT NULL,
    key    TEXT NOT NULL,
    value  TEXT NOT NULL,
    PRIMARY KEY (run_id, key),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE traces (
    id              TEXT PRIMARY KEY,
    trace_id        TEXT NOT NULL,
    parent_id       TEXT,
    run_id          TEXT,
    name            TEXT NOT NULL,
    span_kind       TEXT,
    start_time      INTEGER NOT NULL,
    end_time        INTEGER,
    attributes_json TEXT,
    events_json     TEXT,
    status_code     TEXT,
    status_message  TEXT,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_id) REFERENCES traces(id) ON DELETE CASCADE
);

CREATE INDEX idx_traces_run ON traces(run_id);
CREATE INDEX idx_traces_parent ON traces(parent_id);
CREATE INDEX idx_traces_trace ON traces(trace_id);

CREATE TABLE prompts (
    name         TEXT    NOT NULL,
    version      INTEGER NOT NULL,
    content      TEXT    NOT NULL,
    content_hash TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    created_by   TEXT,
    description  TEXT,
    PRIMARY KEY (name, version)
);

CREATE INDEX idx_prompts_hash ON prompts(content_hash);

CREATE TABLE prompt_aliases (
    name    TEXT    NOT NULL,
    alias   TEXT    NOT NULL,
    version INTEGER NOT NULL,
    PRIMARY KEY (name, alias),
    FOREIGN KEY (name, version) REFERENCES prompts(name, version) ON DELETE CASCADE
);

CREATE TABLE evals (
    run_id          TEXT PRIMARY KEY,
    target_run_ids  TEXT NOT NULL,
    dataset_ref     TEXT,
    score           REAL,
    metrics_json    TEXT,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

-- DOWN

DROP TABLE IF EXISTS evals;
DROP TABLE IF EXISTS prompt_aliases;
DROP TABLE IF EXISTS prompts;
DROP TABLE IF EXISTS traces;
DROP TABLE IF EXISTS tags;
DROP TABLE IF EXISTS params;
DROP TABLE IF EXISTS metrics;
DROP TABLE IF EXISTS runs;
DROP TABLE IF EXISTS experiment_tags;
DROP TABLE IF EXISTS experiments;
