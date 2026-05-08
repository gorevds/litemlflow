-- 008_webhooks: webhook configuration table for run-event notifications.
-- Webhooks fire on run status transitions (started, finished, failed, killed).
-- Delivery is async; last_status/last_attempt track most-recent outcome.

-- UP

CREATE TABLE webhooks (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL,
    url           TEXT    NOT NULL,
    events        TEXT    NOT NULL,    -- comma-separated: run_finished,run_failed,run_started
    experiment_id INTEGER,              -- nullable; NULL = all experiments in workspace
    workspace_id  TEXT    NOT NULL DEFAULT 'default',
    secret        TEXT,                 -- HMAC-SHA256 signing key
    created_at    INTEGER NOT NULL,
    last_status   INTEGER,              -- HTTP status of last delivery
    last_attempt  INTEGER,              -- unix ms
    enabled       INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX idx_webhooks_workspace ON webhooks(workspace_id, enabled);

-- DOWN

DROP INDEX IF EXISTS idx_webhooks_workspace;
DROP TABLE IF EXISTS webhooks;
