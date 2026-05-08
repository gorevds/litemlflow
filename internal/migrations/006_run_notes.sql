-- 006_run_notes: run notes table for markdown annotations.
-- Solo-MLE ergonomics: multi-line markdown notes live in a dedicated table
-- rather than a tag because tags are immutable-by-contract in MLflow semantics
-- (the set-tag endpoint is upsert, but the tag namespace is user-visible and
-- expected to hold short k=v pairs). Storing markdown blobs there would pollute
-- search filters and break clients that enumerate tags. A separate table also
-- lets us track updated_at and updated_by without encoding metadata in the value.

-- UP

CREATE TABLE run_notes (
    run_id      TEXT NOT NULL,
    content     TEXT NOT NULL,
    updated_at  INTEGER NOT NULL,
    updated_by  TEXT,
    PRIMARY KEY (run_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

-- DOWN

DROP TABLE IF EXISTS run_notes;
