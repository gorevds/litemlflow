# Upgrading from v1.x to v2.0

This document captures the v2.0 cleanup plan as agreed during the
v1.2 deep-review (see `docs/reports/2026-05-08-deep-review.md`). Several
items were deferred from Tier 4 of that plan because they require migration
machinery that's safer to ship as a single coordinated v2.0 release rather
than mid-stream during v1.x.

The deferred items are documented here so a future reader (or future me)
knows exactly what was decided and why, and what the implementation
checklist looks like.

## Status as of v1.2

| ID | Item | Status in v1.2 | Activates at |
|---|---|---|---|
| T4.17 | Drop v0.3 `dataset_inputs` dual-write | Soft escape hatch via `LITEMLFLOW_DISABLE_DATASETS_V03_MIRROR=1` | v2.0 (default off → on) |
| T4.18 | Squash migrations 001-009 → `001_v2_baseline.sql` | Plan documented; baseline file present but not activated | v2.0 (`upgrade-to-v2` CLI) |
| T4.19 | Drop legacy MLflow endpoints (`POST/GET experiments/list`, `DELETE registered-models/delete`, `DELETE model-versions/delete`) | Deprecation headers (RFC 8594) added; routes still live | v2.0 (route removal) |
| T4.20 | Tag bag table collapse (`experiment_tags` + `tags` + `registered_model_tags` + `model_version_tags` + `workspace_tags` + `dataset_input_tags` → single `attributes`) | Plan documented; not implemented | v2.0 (migration 100) |
| T4.22 | Workspaces+RBAC UI behind `--enable-multi-tenant` | Implemented in v1.2 (default off) | already |

## T4.17 — drop v0.3 dataset_inputs dual-write

### Current behaviour (v1.x)

`MlflowClient.log_input(...)` writes to two places inside one transaction:

1. The v0.3 tables (`datasets`, `dataset_inputs`, `dataset_input_tags`) for
   byte-identical MLflow compat.
2. A mirror row in `datasets_v2` (the v1.2 content-addressed table) so the
   new Datasets UI sees the input as a versioned row.

The mirror is idempotent on `(workspace_id, name, content_hash)` and uses
a SAVEPOINT + retry to handle concurrent log_input races (see fix in
v1.2.0-rc1 review).

### Plan for v2.0

1. Default `LITEMLFLOW_DISABLE_DATASETS_V03_MIRROR=1` (mirror **off**).
2. Rewrite `LogInputs` to write **only** to `datasets_v2` + a new
   `dataset_inputs_v2(run_id, dataset_id, tags_json)` link table.
3. Migration 100 drops `datasets`, `dataset_inputs`, `dataset_input_tags`.
4. `GetRunDatasets` reads from `dataset_inputs_v2` joined with `datasets_v2`.
5. The MLflow compat suite stays green because the wire response
   preserves `name`, `digest`, `source_type`, `source`, `schema`, `profile`
   (all of which can be carried as `dataset_inputs_v2` columns or in
   `datasets_v2.schema_json`).

### Compatibility note

This is a backwards-incompatible change for any external scraper that
queried `dataset_inputs` directly via SQL. There are no such known
consumers; the change is reversible (re-enable the mirror, restore from
backup) for the first 12 months after v2.0 GA.

## T4.18 — migration squash + upgrade-to-v2 CLI

### Current behaviour (v1.x)

11 migrations (`001_init.sql` through `011_datasets_v2.sql`) are applied
on every fresh install. Migrations 003-009 had to recreate large tables
(`experiments_v2`, `runs_no_lineage`, etc.) to add columns or constraints —
the standard SQLite pattern. Net effect: a 12K-line schema spread across
~700 lines of UP / DOWN SQL.

### Plan for v2.0

1. **Generate `001_v2_baseline.sql`** — the consolidated CREATE TABLE
   statements with every column added through migration 011, in dependency
   order. Indexes and triggers included.
2. **Add `litemlflow upgrade-to-v2` CLI**. Detect `schema_migrations.version`:
   - `0` → fresh install: apply baseline, set version to `100`.
   - `1..11` (active v1.x deploys): error with "run v1.x to current first
     (your version is X), then upgrade-to-v2".
   - `≥100` → no-op.
3. **Keep migrations 001-011 in `internal/migrations/_v1/`** so the
   `upgrade-to-v2` CLI can still apply them when a deployment that's been
   sitting on a stale v1.x release upgrades directly.
4. **The migration runner** in `internal/migrations` chooses between
   "v1 path" and "v2 baseline path" at startup based on a sentinel file
   (`schema_v2.marker`) created by `upgrade-to-v2`.

### Risks

- Existing live deploys must run `upgrade-to-v2` before starting the v2.0
  binary. This is a hard release-note item.
- The baseline must be **byte-equivalent** to the result of applying
  001..011 in sequence; off-by-one differences would cause silent data
  corruption. A test (`TestSquashEquivalence`) verifies this.

## T4.20 — collapse the six tag-bag tables

### Current schema

```
experiment_tags     (experiment_id, key, value)
tags                (run_id,        key, value)        -- run tags
registered_model_tags (name, key, value)
model_version_tags  (name, version, key, value)        -- composite PK
workspace_tags      (workspace_id, key, value)
dataset_input_tags  (dataset_input_id, key, value)
```

Six near-identical (entity, key, value) tables, each with its own
getter/setter pair (~80 LOC of duplicate Go code).

### Why deferred

The Storage architect specifically flagged this as **defer to v2.0 with the
squash**. The blocker is the composite PK on `model_version_tags` (`name,
version`) — collapsing all six into a single `attributes(scope, scope_pk,
key, value)` table forces `scope_pk` to be a string, which loses the FK
CASCADE on `(model_versions.name, model_versions.version)`. The CASCADE
is load-bearing: deleting a model deletes its tags.

### Plan for v2.0

Three options, listed in order of preference:

1. **Collapse with a discriminator + per-scope FK trigger**. Use
   `attributes(scope TEXT, scope_pk TEXT, key TEXT, value TEXT)` and write
   `AFTER DELETE` triggers on each scoped table that delete the matching
   `attributes` rows. Same effect as FK CASCADE; explicit and auditable.
2. **Keep model_version_tags separate, collapse the other five**. Saves
   80% of the duplication while preserving the canonical FK.
3. **Stay with six tables, deduplicate the Go code only**. Storage
   architect's "if option 1 turns out hairy, this is the consolation prize."

### Acceptance for v2.0

- All six tags-tests pass.
- DROP TABLE on a parent (run, experiment, workspace, model, model_version)
  removes its attribute rows in the same transaction.
- Net Go LOC saved: ~80 (six get/set pairs → one).

## T4.19 — legacy endpoints removed

The five routes flagged for removal (deprecated in v1.2 with RFC 8594
headers):

| Route | Removed at | Replacement |
|---|---|---|
| `POST /api/2.0/mlflow/experiments/list` | v2.0 | `POST /api/2.0/mlflow/experiments/search` |
| `GET /api/2.0/mlflow/experiments/list` | v2.0 | `GET /api/2.0/mlflow/experiments/search` |
| `DELETE /api/2.0/mlflow/registered-models/delete` | v2.0 | `POST /api/2.0/mlflow/registered-models/delete` |
| `DELETE /api/2.0/mlflow/model-versions/delete` | v2.0 | `POST /api/2.0/mlflow/model-versions/delete` |
| `POST /v1/traces` (OTLP shim) | v2.0 (telemetry-driven decision) | `POST /api/v1/traces` (native) |

Clients calling deprecated routes already see:

```
HTTP/1.1 200 OK
Deprecation: true
Link: </docs/upgrade-to-v2.md>; rel="deprecation"; type="text/markdown"
X-LiteMLflow-Removed-At: v2.0
```

## Sign-off + ownership

The plans in this document were the audit output of six independent
reviewers (storage, API, frontend, product, security/SRE, quality);
their consolidated recommendation was that v2.0 = consolidated cut and
v1.x = additive cleanup only. The Tier 4 items above respect that boundary.

Open questions for v2.0 implementers:

- Do we ship a one-shot "v1 → v2 dry run" mode that emits a diff of what
  would change without applying?
- Do we support hot-swapping the `attributes` table via a feature flag,
  or is a maintenance window required for the migration?

These are explicitly out of scope for v1.x and should be answered when
v2.0 implementation starts.
