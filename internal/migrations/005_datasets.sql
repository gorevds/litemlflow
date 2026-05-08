-- 006_datasets: dataset linkage tables for log_inputs support.
-- See docs/spec/api-mlflow-compat.md for the REST contract.

-- UP

CREATE TABLE datasets (
    name        TEXT NOT NULL,
    digest      TEXT NOT NULL,
    source_type TEXT,
    source      TEXT,
    schema      TEXT,
    profile     TEXT,
    PRIMARY KEY (name, digest)
);

CREATE TABLE dataset_inputs (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id    TEXT NOT NULL,
    name      TEXT NOT NULL,
    digest    TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY (name, digest) REFERENCES datasets(name, digest)
);

CREATE INDEX idx_dataset_inputs_run ON dataset_inputs(run_id);

CREATE TABLE dataset_input_tags (
    dataset_input_id INTEGER NOT NULL,
    key   TEXT NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (dataset_input_id, key),
    FOREIGN KEY (dataset_input_id) REFERENCES dataset_inputs(id) ON DELETE CASCADE
);

-- DOWN

DROP TABLE IF EXISTS dataset_input_tags;
DROP TABLE IF EXISTS dataset_inputs;
DROP TABLE IF EXISTS datasets;
