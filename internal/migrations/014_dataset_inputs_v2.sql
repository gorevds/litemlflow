-- 014_dataset_inputs_v2: v2.1 T4.17 — new link table between runs and v1.2
-- datasets_v2 rows. Replaces the v0.3 `dataset_inputs` table for new writes.
--
-- The v0.3 tables (`datasets`, `dataset_inputs`, `dataset_input_tags`) are
-- NOT dropped here — they continue to hold legacy data and can be re-enabled
-- as a parallel write path via LITEMLFLOW_ENABLE_DATASETS_V03_WRITES.
--
-- Removal of the legacy tables is deferred to v3.0; backward-compatible
-- reads in GetRunDatasets merge the two sources by (name, digest).

-- UP

CREATE TABLE dataset_inputs_v2 (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      TEXT    NOT NULL,
    dataset_id  INTEGER NOT NULL,
    tags_json   TEXT    NOT NULL DEFAULT '[]',
    FOREIGN KEY (run_id)     REFERENCES runs(id)        ON DELETE CASCADE,
    FOREIGN KEY (dataset_id) REFERENCES datasets_v2(id) ON DELETE CASCADE
);

-- Per-run lookup is the dominant read pattern (`GetRunDatasets`).
CREATE INDEX idx_dataset_inputs_v2_run ON dataset_inputs_v2(run_id);

-- DOWN

DROP INDEX IF EXISTS idx_dataset_inputs_v2_run;
DROP TABLE IF EXISTS dataset_inputs_v2;
