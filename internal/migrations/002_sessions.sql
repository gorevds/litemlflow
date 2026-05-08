-- 002_sessions: session store for cookie-based authentication.
-- Used by both basic-auth and OIDC flows. Sessions expire and are GC'd.

-- UP

CREATE TABLE sessions (
    id          TEXT    PRIMARY KEY,          -- 32-byte hex, opaque random token
    user_id     TEXT    NOT NULL,             -- email or OIDC "sub" claim
    user_email  TEXT,
    user_name   TEXT,
    auth_method TEXT    NOT NULL,             -- 'basic' | 'oidc'
    created_at  INTEGER NOT NULL,             -- unix ms
    expires_at  INTEGER NOT NULL,             -- unix ms
    last_seen   INTEGER NOT NULL              -- unix ms
);

CREATE INDEX idx_sessions_expires ON sessions(expires_at);

-- DOWN

DROP INDEX IF EXISTS idx_sessions_expires;
DROP TABLE IF EXISTS sessions;
