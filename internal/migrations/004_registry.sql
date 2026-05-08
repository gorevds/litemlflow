-- 004_registry: MLflow Model Registry tables.
-- Covers registered models, model versions, aliases, and per-entity tags.
-- See docs/spec/api-mlflow-compat.md (Model Registry section).

-- UP

CREATE TABLE registered_models (
    name             TEXT    PRIMARY KEY,
    description      TEXT,
    creation_time    INTEGER NOT NULL,   -- unix ms
    last_update_time INTEGER NOT NULL    -- unix ms
);

CREATE TABLE model_versions (
    name              TEXT    NOT NULL,
    version           INTEGER NOT NULL,
    description       TEXT,
    user_id           TEXT,
    current_stage     TEXT    NOT NULL DEFAULT 'None',  -- None|Staging|Production|Archived
    source            TEXT    NOT NULL,                  -- artifact URI
    run_id            TEXT,                              -- nullable
    status            TEXT    NOT NULL DEFAULT 'READY',  -- READY|PENDING|FAILED
    status_message    TEXT,
    creation_time     INTEGER NOT NULL,
    last_update_time  INTEGER NOT NULL,
    PRIMARY KEY (name, version),
    FOREIGN KEY (name) REFERENCES registered_models(name) ON DELETE CASCADE,
    CHECK (current_stage IN ('None','Staging','Production','Archived')),
    CHECK (status IN ('READY','PENDING','FAILED'))
);

CREATE INDEX idx_mv_run ON model_versions(run_id);
CREATE INDEX idx_mv_stage ON model_versions(current_stage);

CREATE TABLE registered_model_tags (
    name  TEXT NOT NULL,
    key   TEXT NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (name, key),
    FOREIGN KEY (name) REFERENCES registered_models(name) ON DELETE CASCADE
);

CREATE TABLE model_version_tags (
    name    TEXT NOT NULL,
    version INTEGER NOT NULL,
    key     TEXT NOT NULL,
    value   TEXT NOT NULL,
    PRIMARY KEY (name, version, key),
    FOREIGN KEY (name, version) REFERENCES model_versions(name, version) ON DELETE CASCADE
);

CREATE TABLE model_aliases (
    name    TEXT NOT NULL,
    alias   TEXT NOT NULL,
    version INTEGER NOT NULL,
    PRIMARY KEY (name, alias),
    FOREIGN KEY (name, version) REFERENCES model_versions(name, version) ON DELETE CASCADE
);

-- DOWN

DROP TABLE IF EXISTS model_aliases;
DROP TABLE IF EXISTS model_version_tags;
DROP TABLE IF EXISTS registered_model_tags;
DROP INDEX IF EXISTS idx_mv_stage;
DROP INDEX IF EXISTS idx_mv_run;
DROP TABLE IF EXISTS model_versions;
DROP TABLE IF EXISTS registered_models;
