-- 009_dashboards: per-project widget config for the Dashboards page (W8.5).
--
-- A dashboard is a list of widgets (config JSON) attached to a "project" —
-- which itself is just a value of the lmf.project experiment tag. There is at
-- most one dashboard per (workspace, project) pair; widgets are stored
-- inline as a JSON array so the wire format and UI render path are identical.
--
-- Why one row per project rather than one row per widget: the UI fetches the
-- whole dashboard atomically when navigating, edits it client-side, and PUTs
-- it back. A row-per-widget table would force tracking widget IDs and
-- deletions; the JSON blob is simpler and small (typical dashboards have
-- under 10 widgets).

-- UP

CREATE TABLE dashboards (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id TEXT    NOT NULL DEFAULT 'default',
    project      TEXT    NOT NULL,
    widgets      TEXT    NOT NULL DEFAULT '[]',  -- JSON array of widget configs
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    UNIQUE (workspace_id, project)
);

CREATE INDEX idx_dashboards_workspace ON dashboards(workspace_id);

-- DOWN

DROP INDEX IF EXISTS idx_dashboards_workspace;
DROP TABLE IF EXISTS dashboards;
