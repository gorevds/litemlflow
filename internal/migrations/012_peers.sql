-- 012_peers: federation peers (Y2 Q3, v1.3).
--
-- Each row is one peer LiteMLflow instance this server can query.
-- Auth between peers is mutual HMAC: a per-peer 32-byte secret is shared
-- with the remote and signs every outbound request (header
-- `X-LiteMLflow-Federate-Sig`); the remote validates against its mirror
-- of the same secret. We use HMAC over JWT to keep dependency surface
-- minimal — same crypto/hmac primitive already proven by the webhook
-- HMAC code path.
--
-- Status lifecycle:
--   - 'pending'   — added by an operator; not yet reachable
--   - 'connected' — last echo succeeded
--   - 'error'     — last echo failed (last_error has the reason)
-- The federated-search fan-out skips peers in 'error' state; operators
-- re-trigger the echo via POST /api/v1/federate/peers/{id}/echo.
--
-- Why workspace_id: federation rows live per workspace so a multi-tenant
-- deploy can register different peer sets per team. Solo deploys (the
-- common case) just see them under workspace_id='default'.

-- UP

CREATE TABLE peers (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    url          TEXT    NOT NULL,
    secret       TEXT    NOT NULL,         -- HMAC secret (hex 64 chars)
    workspace_id TEXT    NOT NULL DEFAULT 'default',
    added_at     INTEGER NOT NULL,
    last_seen    INTEGER,
    status       TEXT    NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','connected','error')),
    last_error   TEXT,
    UNIQUE (workspace_id, name)
);

CREATE INDEX idx_peers_workspace ON peers(workspace_id, status);

-- DOWN

DROP INDEX IF EXISTS idx_peers_workspace;
DROP TABLE IF EXISTS peers;
