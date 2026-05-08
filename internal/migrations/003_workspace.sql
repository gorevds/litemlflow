-- 003_workspace: workspace support (scaffolded for v0.2, no-op tables).
-- Reserved slot so migration numbering is contiguous when 004_registry lands.

-- UP

CREATE TABLE IF NOT EXISTS workspaces (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE,
    created_at  INTEGER NOT NULL
);

-- DOWN

DROP TABLE IF EXISTS workspaces;
