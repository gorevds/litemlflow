-- 005_workspaces: multi-tenant workspace support.
-- Adds workspaces, workspace_members, workspace_tags tables and scopes
-- experiments to a workspace. All pre-existing experiments are assigned
-- to the 'default' workspace seeded by this migration.
--
-- NOTE: The experiments table's UNIQUE constraint is changed from (name) to
-- (workspace_id, name) so that the same experiment name can exist in different
-- workspaces. This requires recreating the experiments table.

-- UP

CREATE TABLE workspaces (
    id               TEXT    PRIMARY KEY,
    name             TEXT    NOT NULL,
    description      TEXT,
    creation_time    INTEGER NOT NULL,
    last_update_time INTEGER NOT NULL
);

INSERT INTO workspaces(id, name, creation_time, last_update_time)
VALUES ('default', 'Default', strftime('%s','now')*1000, strftime('%s','now')*1000);

-- Recreate experiments table with workspace_id and updated UNIQUE constraint.
-- The original schema had UNIQUE(name); we now enforce UNIQUE(workspace_id, name)
-- so experiments with the same name can exist in different workspaces.
CREATE TABLE experiments_v2 (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT    NOT NULL,
    artifact_location TEXT    NOT NULL,
    lifecycle_stage   TEXT    NOT NULL DEFAULT 'active',
    creation_time     INTEGER NOT NULL,
    last_update_time  INTEGER NOT NULL,
    workspace_id      TEXT    NOT NULL DEFAULT 'default'
        REFERENCES workspaces(id) ON DELETE CASCADE,
    CHECK (lifecycle_stage IN ('active','deleted')),
    UNIQUE (workspace_id, name)
);

INSERT INTO experiments_v2(id, name, artifact_location, lifecycle_stage, creation_time, last_update_time, workspace_id)
SELECT id, name, artifact_location, lifecycle_stage, creation_time, last_update_time, 'default'
FROM experiments;

DROP TABLE experiments;
ALTER TABLE experiments_v2 RENAME TO experiments;

CREATE INDEX idx_experiments_workspace ON experiments(workspace_id, lifecycle_stage);

CREATE TABLE workspace_members (
    workspace_id TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    role         TEXT NOT NULL DEFAULT 'editor',
    PRIMARY KEY (workspace_id, user_id),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    CHECK (role IN ('viewer','editor','admin'))
);

CREATE TABLE workspace_tags (
    workspace_id TEXT NOT NULL,
    key          TEXT NOT NULL,
    value        TEXT NOT NULL,
    PRIMARY KEY (workspace_id, key),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
);

-- DOWN

DROP TABLE IF EXISTS workspace_tags;
DROP TABLE IF EXISTS workspace_members;
DROP INDEX IF EXISTS idx_experiments_workspace;

-- Restore experiments table without workspace_id, with original UNIQUE(name).
-- Only rows from the 'default' workspace are preserved; experiments created in
-- other workspaces after the upgrade are irrecoverably lost on downgrade.
CREATE TABLE experiments_orig (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT    NOT NULL UNIQUE,
    artifact_location TEXT    NOT NULL,
    lifecycle_stage   TEXT    NOT NULL DEFAULT 'active',
    creation_time     INTEGER NOT NULL,
    last_update_time  INTEGER NOT NULL,
    CHECK (lifecycle_stage IN ('active','deleted'))
);

INSERT INTO experiments_orig(id, name, artifact_location, lifecycle_stage, creation_time, last_update_time)
SELECT id, name, artifact_location, lifecycle_stage, creation_time, last_update_time
FROM experiments
WHERE workspace_id = 'default';

DROP TABLE experiments;
ALTER TABLE experiments_orig RENAME TO experiments;

DROP TABLE IF EXISTS workspaces;
