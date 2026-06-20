-- 015_registry_workspace: scope the Model Registry and Prompts to a workspace.
--
-- Independent-review finding 2.1: experiments/runs/datasets/webhooks/peers all
-- carry workspace_id, but the Model Registry (registered_models, model_versions,
-- aliases, tags) and Prompts were global — in multi-tenant mode tenant A's
-- "fraud-v1" collided with / overwrote / was readable by tenant B. This
-- migration adds workspace_id to every registry + prompt table and changes the
-- uniqueness from (name[, version]) to (workspace_id, name[, version]) so the
-- same name can exist independently per workspace.
--
-- All pre-existing rows are assigned to the 'default' workspace.
--
-- Because the registry tables form an FK graph (model_versions/tags/aliases ->
-- registered_models; tags/aliases -> model_versions) and foreign_keys is ON,
-- we cannot DROP a parent table without its ON DELETE CASCADE wiping the
-- children. We therefore: (1) create *_v2 tables whose FKs point at the *_v2
-- graph, (2) copy data parent->child, (3) DROP the originals child->parent (so
-- no cascade fires), (4) RENAME *_v2 to the final names (SQLite rewrites the
-- FK references of dependent tables on rename). Validated with
-- PRAGMA foreign_key_check.

-- UP

-- ---- prompts + prompt_aliases ----------------------------------------------
-- prompt_aliases has an ON DELETE CASCADE FK into prompts, so the two must be
-- rebuilt together with the same child->parent drop order used for the registry
-- graph below; otherwise DROP TABLE prompts would cascade-delete every alias.
CREATE TABLE prompts_v2 (
    name         TEXT    NOT NULL,
    version      INTEGER NOT NULL,
    workspace_id TEXT    NOT NULL DEFAULT 'default'
        REFERENCES workspaces(id) ON DELETE CASCADE,
    content      TEXT    NOT NULL,
    content_hash TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    created_by   TEXT,
    description  TEXT,
    PRIMARY KEY (workspace_id, name, version)
);
CREATE TABLE prompt_aliases_v2 (
    workspace_id TEXT    NOT NULL DEFAULT 'default',
    name         TEXT    NOT NULL,
    alias        TEXT    NOT NULL,
    version      INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, name, alias),
    FOREIGN KEY (workspace_id, name, version) REFERENCES prompts_v2(workspace_id, name, version) ON DELETE CASCADE
);
INSERT INTO prompts_v2(name, version, workspace_id, content, content_hash, created_at, created_by, description)
SELECT name, version, 'default', content, content_hash, created_at, created_by, description FROM prompts;
-- Drop dangling aliases (a pre-015 schema could leave an alias whose prompt
-- version no longer exists); they can't satisfy the new FK.
INSERT INTO prompt_aliases_v2(workspace_id, name, alias, version)
SELECT 'default', pa.name, pa.alias, pa.version FROM prompt_aliases pa
WHERE EXISTS (SELECT 1 FROM prompts_v2 p WHERE p.workspace_id = 'default' AND p.name = pa.name AND p.version = pa.version);
DROP TABLE prompt_aliases;
DROP TABLE prompts;
ALTER TABLE prompts_v2 RENAME TO prompts;
ALTER TABLE prompt_aliases_v2 RENAME TO prompt_aliases;
-- Recreate the content-hash index dropped with the old prompts table (001
-- had idx_prompts_hash ON prompts(content_hash)). Lead with workspace_id +
-- name to cover the now-scoped dedup lookup
-- (WHERE workspace_id=? AND name=? AND content_hash=?).
CREATE INDEX idx_prompts_hash ON prompts(workspace_id, name, content_hash);

-- ---- registry FK graph ------------------------------------------------------
CREATE TABLE registered_models_v2 (
    name             TEXT    NOT NULL,
    workspace_id     TEXT    NOT NULL DEFAULT 'default'
        REFERENCES workspaces(id) ON DELETE CASCADE,
    description      TEXT,
    creation_time    INTEGER NOT NULL,
    last_update_time INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, name)
);

CREATE TABLE model_versions_v2 (
    name              TEXT    NOT NULL,
    version           INTEGER NOT NULL,
    workspace_id      TEXT    NOT NULL DEFAULT 'default',
    description       TEXT,
    user_id           TEXT,
    current_stage     TEXT    NOT NULL DEFAULT 'None',
    source            TEXT    NOT NULL,
    run_id            TEXT,
    status            TEXT    NOT NULL DEFAULT 'READY',
    status_message    TEXT,
    creation_time     INTEGER NOT NULL,
    last_update_time  INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, name, version),
    FOREIGN KEY (workspace_id, name) REFERENCES registered_models_v2(workspace_id, name) ON DELETE CASCADE,
    CHECK (current_stage IN ('None','Staging','Production','Archived')),
    CHECK (status IN ('READY','PENDING','FAILED'))
);

CREATE TABLE registered_model_tags_v2 (
    name         TEXT NOT NULL,
    workspace_id TEXT NOT NULL DEFAULT 'default',
    key          TEXT NOT NULL,
    value        TEXT NOT NULL,
    PRIMARY KEY (workspace_id, name, key),
    FOREIGN KEY (workspace_id, name) REFERENCES registered_models_v2(workspace_id, name) ON DELETE CASCADE
);

CREATE TABLE model_version_tags_v2 (
    name         TEXT NOT NULL,
    version      INTEGER NOT NULL,
    workspace_id TEXT NOT NULL DEFAULT 'default',
    key          TEXT NOT NULL,
    value        TEXT NOT NULL,
    PRIMARY KEY (workspace_id, name, version, key),
    FOREIGN KEY (workspace_id, name, version) REFERENCES model_versions_v2(workspace_id, name, version) ON DELETE CASCADE
);

CREATE TABLE model_aliases_v2 (
    name         TEXT NOT NULL,
    workspace_id TEXT NOT NULL DEFAULT 'default',
    alias        TEXT NOT NULL,
    version      INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, name, alias),
    FOREIGN KEY (workspace_id, name, version) REFERENCES model_versions_v2(workspace_id, name, version) ON DELETE CASCADE
);

-- Copy parent -> child so each FK check passes at insert time. Each child
-- copy filters to rows whose parent was actually carried into the *_v2 table:
-- a pre-015 database can contain orphan child rows (e.g. model_version_tags
-- left behind after a model_version was removed without cascade) that the old
-- schema tolerated but the rebuilt FK graph rejects. Dropping them here is the
-- correct outcome — they reference an entity that no longer exists.
INSERT INTO registered_models_v2(name, workspace_id, description, creation_time, last_update_time)
SELECT name, 'default', description, creation_time, last_update_time FROM registered_models;
INSERT INTO model_versions_v2(name, version, workspace_id, description, user_id, current_stage, source, run_id, status, status_message, creation_time, last_update_time)
SELECT mv.name, mv.version, 'default', mv.description, mv.user_id, mv.current_stage, mv.source, mv.run_id, mv.status, mv.status_message, mv.creation_time, mv.last_update_time FROM model_versions mv
WHERE EXISTS (SELECT 1 FROM registered_models_v2 rm WHERE rm.workspace_id = 'default' AND rm.name = mv.name);
INSERT INTO registered_model_tags_v2(name, workspace_id, key, value)
SELECT rt.name, 'default', rt.key, rt.value FROM registered_model_tags rt
WHERE EXISTS (SELECT 1 FROM registered_models_v2 rm WHERE rm.workspace_id = 'default' AND rm.name = rt.name);
INSERT INTO model_version_tags_v2(name, version, workspace_id, key, value)
SELECT mt.name, mt.version, 'default', mt.key, mt.value FROM model_version_tags mt
WHERE EXISTS (SELECT 1 FROM model_versions_v2 mv WHERE mv.workspace_id = 'default' AND mv.name = mt.name AND mv.version = mt.version);
INSERT INTO model_aliases_v2(name, workspace_id, alias, version)
SELECT ma.name, 'default', ma.alias, ma.version FROM model_aliases ma
WHERE EXISTS (SELECT 1 FROM model_versions_v2 mv WHERE mv.workspace_id = 'default' AND mv.name = ma.name AND mv.version = ma.version);

-- Drop originals child -> parent so no ON DELETE CASCADE fires.
DROP INDEX IF EXISTS idx_mv_stage;
DROP INDEX IF EXISTS idx_mv_run;
DROP TABLE model_aliases;
DROP TABLE model_version_tags;
DROP TABLE registered_model_tags;
DROP TABLE model_versions;
DROP TABLE registered_models;

-- Rename parent first; SQLite rewrites dependents' FK references on rename.
ALTER TABLE registered_models_v2 RENAME TO registered_models;
ALTER TABLE model_versions_v2 RENAME TO model_versions;
ALTER TABLE registered_model_tags_v2 RENAME TO registered_model_tags;
ALTER TABLE model_version_tags_v2 RENAME TO model_version_tags;
ALTER TABLE model_aliases_v2 RENAME TO model_aliases;

CREATE INDEX idx_mv_run ON model_versions(run_id);
CREATE INDEX idx_mv_stage ON model_versions(current_stage);

-- DOWN
--
-- Reverse the scoping. Only rows in the 'default' workspace are preserved;
-- registry entries / prompts created in other workspaces after the upgrade are
-- irrecoverably lost on downgrade (same tradeoff as 004_workspaces). The v2.1
-- rollback guard will flag these DROPs on a populated DB unless --force.

-- prompts + prompt_aliases back to global (name, version) with the original
-- two-column FK. Restored together (child-first drop) so the FK signature
-- matches the restored prompts primary key.
CREATE TABLE prompts_orig (
    name         TEXT    NOT NULL,
    version      INTEGER NOT NULL,
    content      TEXT    NOT NULL,
    content_hash TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    created_by   TEXT,
    description  TEXT,
    PRIMARY KEY (name, version)
);
CREATE TABLE prompt_aliases_orig (
    name    TEXT    NOT NULL,
    alias   TEXT    NOT NULL,
    version INTEGER NOT NULL,
    PRIMARY KEY (name, alias),
    FOREIGN KEY (name, version) REFERENCES prompts_orig(name, version) ON DELETE CASCADE
);
INSERT INTO prompts_orig(name, version, content, content_hash, created_at, created_by, description)
SELECT name, version, content, content_hash, created_at, created_by, description
FROM prompts WHERE workspace_id = 'default';
INSERT INTO prompt_aliases_orig(name, alias, version)
SELECT name, alias, version FROM prompt_aliases WHERE workspace_id = 'default';
DROP TABLE prompt_aliases;
DROP TABLE prompts;
ALTER TABLE prompts_orig RENAME TO prompts;
ALTER TABLE prompt_aliases_orig RENAME TO prompt_aliases;
-- Restore the original content-hash index (001 shape).
CREATE INDEX idx_prompts_hash ON prompts(content_hash);

-- registry back to global, FK graph referencing *_orig then renamed.
CREATE TABLE registered_models_orig (
    name             TEXT    PRIMARY KEY,
    description      TEXT,
    creation_time    INTEGER NOT NULL,
    last_update_time INTEGER NOT NULL
);
CREATE TABLE model_versions_orig (
    name              TEXT    NOT NULL,
    version           INTEGER NOT NULL,
    description       TEXT,
    user_id           TEXT,
    current_stage     TEXT    NOT NULL DEFAULT 'None',
    source            TEXT    NOT NULL,
    run_id            TEXT,
    status            TEXT    NOT NULL DEFAULT 'READY',
    status_message    TEXT,
    creation_time     INTEGER NOT NULL,
    last_update_time  INTEGER NOT NULL,
    PRIMARY KEY (name, version),
    FOREIGN KEY (name) REFERENCES registered_models_orig(name) ON DELETE CASCADE,
    CHECK (current_stage IN ('None','Staging','Production','Archived')),
    CHECK (status IN ('READY','PENDING','FAILED'))
);
CREATE TABLE registered_model_tags_orig (
    name  TEXT NOT NULL,
    key   TEXT NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (name, key),
    FOREIGN KEY (name) REFERENCES registered_models_orig(name) ON DELETE CASCADE
);
CREATE TABLE model_version_tags_orig (
    name    TEXT NOT NULL,
    version INTEGER NOT NULL,
    key     TEXT NOT NULL,
    value   TEXT NOT NULL,
    PRIMARY KEY (name, version, key),
    FOREIGN KEY (name, version) REFERENCES model_versions_orig(name, version) ON DELETE CASCADE
);
CREATE TABLE model_aliases_orig (
    name    TEXT NOT NULL,
    alias   TEXT NOT NULL,
    version INTEGER NOT NULL,
    PRIMARY KEY (name, alias),
    FOREIGN KEY (name, version) REFERENCES model_versions_orig(name, version) ON DELETE CASCADE
);

INSERT INTO registered_models_orig(name, description, creation_time, last_update_time)
SELECT name, description, creation_time, last_update_time FROM registered_models WHERE workspace_id = 'default';
INSERT INTO model_versions_orig(name, version, description, user_id, current_stage, source, run_id, status, status_message, creation_time, last_update_time)
SELECT name, version, description, user_id, current_stage, source, run_id, status, status_message, creation_time, last_update_time FROM model_versions WHERE workspace_id = 'default';
INSERT INTO registered_model_tags_orig(name, key, value)
SELECT name, key, value FROM registered_model_tags WHERE workspace_id = 'default';
INSERT INTO model_version_tags_orig(name, version, key, value)
SELECT name, version, key, value FROM model_version_tags WHERE workspace_id = 'default';
INSERT INTO model_aliases_orig(name, alias, version)
SELECT name, alias, version FROM model_aliases WHERE workspace_id = 'default';

DROP INDEX IF EXISTS idx_mv_stage;
DROP INDEX IF EXISTS idx_mv_run;
DROP TABLE model_aliases;
DROP TABLE model_version_tags;
DROP TABLE registered_model_tags;
DROP TABLE model_versions;
DROP TABLE registered_models;

ALTER TABLE registered_models_orig RENAME TO registered_models;
ALTER TABLE model_versions_orig RENAME TO model_versions;
ALTER TABLE registered_model_tags_orig RENAME TO registered_model_tags;
ALTER TABLE model_version_tags_orig RENAME TO model_version_tags;
ALTER TABLE model_aliases_orig RENAME TO model_aliases;

CREATE INDEX idx_mv_run ON model_versions(run_id);
CREATE INDEX idx_mv_stage ON model_versions(current_stage);
