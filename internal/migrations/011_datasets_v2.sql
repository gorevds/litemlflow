-- 011_datasets_v2: first-class versioned datasets with content-addressed
-- storage and explicit lineage edges (Y2 Q2, v1.2).
--
-- Why a second table instead of evolving v0.3 `datasets`:
--   * v0.3 keyed datasets by (name, digest) — the digest was *client-supplied*
--     and a name+digest pair could refer to entirely different content with
--     no version sequence between them. We need a server-verified content
--     hash plus a per-name version sequence so users can talk about
--     "rag-train v3" without needing to remember the digest.
--   * The MLflow compat path still writes to v0.3 tables (to keep
--     `MlflowClient.log_input` working byte-identically); the new path
--     writes to datasets_v2 *additionally*. After v2.0 we drop the v0.3
--     tables.
--   * Lineage is a separate edge table so a dataset can have multiple
--     parents (e.g. derived from a join of several upstream datasets) and
--     so we can render a DAG without parsing JSON.

-- UP

CREATE TABLE datasets_v2 (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT NOT NULL,
    version       INTEGER NOT NULL,        -- per-name auto-incrementing 1..N
    content_hash  TEXT NOT NULL,           -- server-verified sha256 hex of bytes
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    schema_json   TEXT,                    -- optional JSON schema / column types
    description   TEXT,
    workspace_id  TEXT NOT NULL DEFAULT 'default',
    created_at    INTEGER NOT NULL,
    created_by    TEXT,
    lifecycle_stage TEXT NOT NULL DEFAULT 'active' CHECK (lifecycle_stage IN ('active','deleted')),
    UNIQUE (workspace_id, name, version)
);

CREATE INDEX idx_datasets_v2_hash ON datasets_v2(content_hash);
CREATE INDEX idx_datasets_v2_workspace_name ON datasets_v2(workspace_id, name, version DESC);

-- Each row is one parent→child edge. A child can have many parents.
-- Cycles are rejected at write time in store code (we don't want to
-- depend on a recursive CTE for every read).
CREATE TABLE dataset_lineage (
    child_id  INTEGER NOT NULL,
    parent_id INTEGER NOT NULL,
    PRIMARY KEY (child_id, parent_id),
    FOREIGN KEY (child_id)  REFERENCES datasets_v2(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_id) REFERENCES datasets_v2(id) ON DELETE CASCADE
);

CREATE INDEX idx_dataset_lineage_parent ON dataset_lineage(parent_id);

-- Backfill from v0.3 datasets / dataset_inputs.
--
-- Each (name, digest) pair in v0.3 becomes one datasets_v2 row. The
-- per-name version sequence is computed by ROW_NUMBER() ordered by the
-- earliest run linked to that pair (so version 1 is the dataset that was
-- first observed in the workspace). Workspace is inferred from the first
-- experiment that referenced the (name, digest) pair; falls back to
-- 'default' when no run linked. size_bytes is 0 for legacy rows because
-- v0.3 didn't track it.
INSERT INTO datasets_v2 (name, version, content_hash, size_bytes, workspace_id, created_at, lifecycle_stage)
SELECT
    src.name,
    ROW_NUMBER() OVER (PARTITION BY src.name ORDER BY src.created_at, src.digest) AS version,
    src.digest AS content_hash,
    0 AS size_bytes,
    src.workspace_id,
    src.created_at,
    'active'
FROM (
    -- Per (name, digest), pick a representative workspace + earliest
    -- linked run's start_time as the surrogate created_at. If no run
    -- ever referenced the pair, fall back to 0 (epoch).
    SELECT
        d.name,
        d.digest,
        COALESCE(
            (SELECT e.workspace_id
             FROM dataset_inputs di
             JOIN runs r ON r.id = di.run_id
             JOIN experiments e ON e.id = r.experiment_id
             WHERE di.name = d.name AND di.digest = d.digest
             LIMIT 1),
            'default'
        ) AS workspace_id,
        COALESCE(
            (SELECT MIN(r.start_time)
             FROM dataset_inputs di
             JOIN runs r ON r.id = di.run_id
             WHERE di.name = d.name AND di.digest = d.digest),
            0
        ) AS created_at
    FROM datasets d
) src;

-- DOWN

DROP INDEX IF EXISTS idx_dataset_lineage_parent;
DROP TABLE IF EXISTS dataset_lineage;
DROP INDEX IF EXISTS idx_datasets_v2_workspace_name;
DROP INDEX IF EXISTS idx_datasets_v2_hash;
DROP TABLE IF EXISTS datasets_v2;
