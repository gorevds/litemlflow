# MLflow API compatibility surface

LiteMLflow targets pragmatic 80% compatibility with the MLflow REST API. This document is the contract: anything listed as **Implemented** must remain wire-compatible with the MLflow Python client (versions 2.10+).

The wire format is JSON over HTTP, request and response shapes match MLflow exactly unless noted.

## Implemented (v0.1)

### Experiments

| Endpoint | Method | Notes |
|---|---|---|
| `/api/2.0/mlflow/experiments/create` | POST | accepts `name`, `artifact_location`, `tags` |
| `/api/2.0/mlflow/experiments/get` | GET | by `experiment_id` |
| `/api/2.0/mlflow/experiments/get-by-name` | GET | by `experiment_name` |
| `/api/2.0/mlflow/experiments/search` | POST | supports basic `filter`, `order_by`, `max_results`, `page_token` |
| `/api/2.0/mlflow/experiments/delete` | POST | sets lifecycle_stage to deleted |
| `/api/2.0/mlflow/experiments/restore` | POST | sets lifecycle_stage to active |
| `/api/2.0/mlflow/experiments/update` | POST | rename |
| `/api/2.0/mlflow/experiments/set-experiment-tag` | POST | upsert experiment tag |

### Runs

| Endpoint | Method | Notes |
|---|---|---|
| `/api/2.0/mlflow/runs/create` | POST | `experiment_id`, `run_name`, `start_time`, `tags` |
| `/api/2.0/mlflow/runs/get` | GET | by `run_id` |
| `/api/2.0/mlflow/runs/update` | POST | status, end_time |
| `/api/2.0/mlflow/runs/delete` | POST | sets lifecycle_stage to deleted |
| `/api/2.0/mlflow/runs/restore` | POST | sets lifecycle_stage to active |
| `/api/2.0/mlflow/runs/search` | POST | supports `experiment_ids`, `filter`, `order_by`, pagination |
| `/api/2.0/mlflow/runs/log-metric` | POST | single metric write |
| `/api/2.0/mlflow/runs/log-parameter` | POST | single param write |
| `/api/2.0/mlflow/runs/log-batch` | POST | batch metrics/params/tags |
| `/api/2.0/mlflow/runs/set-tag` | POST | upsert tag |
| `/api/2.0/mlflow/runs/delete-tag` | POST | remove tag |
| `/api/2.0/mlflow/runs/log-inputs` | POST | dataset linkage (migration 006) |
| `/api/2.0/mlflow/metrics/get-history` | GET | timeseries by `run_id` + `metric_key`; supports `?max_results=N&page_token=...` (paginated) and `?downsample=N` (LTTB server-side downsampling) |

### Artifacts

| Endpoint | Method | Notes |
|---|---|---|
| `/api/2.0/mlflow/artifacts/list` | GET | list a run's artifacts directory |
| `/api/2.0/mlflow-artifacts/artifacts/{path...}` | GET | download |
| `/api/2.0/mlflow-artifacts/artifacts/{path...}` | PUT | upload |
| `/api/2.0/mlflow-artifacts/artifacts/{path...}` | DELETE | delete |

### Model Registry (v0.2)

#### Registered Models

| Endpoint | Method | Notes |
|---|---|---|
| `/api/2.0/mlflow/registered-models/create` | POST | `name`, `description`, `tags` |
| `/api/2.0/mlflow/registered-models/get` | GET | by `name` query param |
| `/api/2.0/mlflow/registered-models/rename` | POST | `name`, `new_name` |
| `/api/2.0/mlflow/registered-models/update` | POST | update `description` |
| `/api/2.0/mlflow/registered-models/delete` | POST | cascades to all versions |
| `/api/2.0/mlflow/registered-models/search` | POST/GET | filter: `name =`, `name LIKE`, `tags.X =` |
| `/api/2.0/mlflow/registered-models/get-latest-versions` | POST/GET | one version per stage (highest version number wins) |
| `/api/2.0/mlflow/registered-models/set-tag` | POST | upsert |
| `/api/2.0/mlflow/registered-models/delete-tag` | POST | |
| `/api/2.0/mlflow/registered-models/alias` | POST | set alias (upsert) |
| `/api/2.0/mlflow/registered-models/alias` | DELETE | by `name`+`alias` query params |
| `/api/2.0/mlflow/registered-models/alias` | GET | resolve alias → version |

#### Model Versions

| Endpoint | Method | Notes |
|---|---|---|
| `/api/2.0/mlflow/model-versions/create` | POST | auto-increments version per name (first = 1); `source` required |
| `/api/2.0/mlflow/model-versions/get` | GET | by `name`+`version` query params |
| `/api/2.0/mlflow/model-versions/update` | POST | update `description` |
| `/api/2.0/mlflow/model-versions/delete` | POST | cascades aliases and version tags |
| `/api/2.0/mlflow/model-versions/search` | POST/GET | filter: `name =`, `name LIKE`, `tags.X =`, `run_id =` |
| `/api/2.0/mlflow/model-versions/get-download-uri` | GET | returns `source` URI registered with the version |
| `/api/2.0/mlflow/model-versions/transition-stage` | POST | `archive_existing_versions=true` moves other Production → Archived |
| `/api/2.0/mlflow/model-versions/set-tag` | POST | upsert |
| `/api/2.0/mlflow/model-versions/delete-tag` | POST | |

**Registry quirks and design notes:**

- **PK rename**: SQLite does not cascade `ON UPDATE` for primary-key changes. `RenameRegisteredModel` acquires a dedicated `sql.Conn`, disables FK enforcement, updates all child table `name` columns, updates the PK, then re-enables FK enforcement before returning the connection to the pool.
- **Version numbering**: versions are `INTEGER` auto-incremented per `(name)`, not global. Concurrent creates under the same name are serialised via `BEGIN IMMEDIATE` inside a transaction.
- **Stage values**: `None` (default), `Staging`, `Production`, `Archived`. Invalid values return `INVALID_PARAMETER_VALUE`.
- **`archive_existing_versions`**: only effective when transitioning to `Production`. Moves all other `Production` versions of the same model to `Archived` atomically.
- **`get-latest-versions`**: returns one version per stage (the highest version number in that stage). If `stages` is empty all stages are included. Versions in `None` stage are included.
- **`get-download-uri`**: returns the `source` field verbatim — no URL rewriting. Clients that need actual byte access should use the artifacts API with the `run_id`.
- **Aliases**: upsert semantics (POST), delete by `name`+`alias` query params (DELETE), resolve by `name`+`alias` (GET).
- **Tags**: all tag operations are upsert. Tags cascade-delete with their parent model/version.
- **Migration**: schema lives in `internal/migrations/003_registry.sql`.

## Metric history downsampling (`?downsample=N`)

The standard `?max_results=N&page_token=...` query parameters are for callers that need the full series paginated (e.g., the MLflow Python client). For visualization, use `?downsample=N` instead.

### How it works

When `?downsample=N` is present, the server:

1. Fetches the complete metric history for the requested `(run_id, metric_key)` pair.
2. Reduces it to at most `N` representative points using **LTTB (Largest-Triangle-Three-Buckets)**, the gold-standard algorithm for visual downsampling that preserves peaks and troughs.
3. Always keeps the first and last points.
4. When the raw series has ≤ N points, returns all of them unchanged.

### Response shape

```json
{
  "metrics": [
    {"key": "loss", "value": 1.23, "timestamp": 1715000000000, "step": 0},
    ...
  ],
  "downsampled_from": 50000
}
```

`"downsampled_from"` is always present in the downsampled response and contains the total raw point count. The standard `"next_page_token"` field is omitted (downsampling returns a single, complete payload, not pages).

### Example

```bash
# Fetch 500 LTTB-representative points from a 1M-point series.
curl "http://localhost:5000/api/2.0/mlflow/metrics/get-history?run_id=abc123&metric_key=loss&downsample=500"
```

### Compatibility note

The `?downsample` parameter is a LiteMLflow extension. The MLflow Python client does not send it; its `get_metric_history()` method uses the paginated path. The two paths are independent and do not interfere.

## `import-mlflow` CLI migration tool

LiteMLflow ships a server-side import command that copies an entire MLflow
tracking server — experiments, runs, metrics, params, tags, and artifacts —
directly into a LiteMLflow data directory without an intermediate HTTP layer:

```bash
litemlflow import-mlflow \
  --from http://my-mlflow-server:5000 \
  --data /var/lib/litemlflow/data
```

The import is **idempotent**: interrupted runs are resumed from a checkpoint
stored in `<data>/.import-state.json`.  Run IDs are preserved verbatim so
existing client code and links continue to work.

Key design choices:

- Uses only the MLflow REST API (no direct DB access to the source), so it
  works against any MLflow deployment (SQLite, Postgres, Databricks).
- The MLflow API subset called: `experiments/search` (paginated),
  `runs/search` (paginated), `metrics/get-history` (paginated, full history),
  `artifacts/list` (recursive), `mlflow-artifacts/artifacts/{run}/{path}` (GET).
- No new module dependencies: only stdlib `net/http`, `encoding/json`, etc.
- `--dry-run` mode prints a summary without writing anything.
- `--include-deleted` mirrors deleted experiments and runs.

See [docs/cookbook.md](../cookbook.md#12-migrate-from-mlflow-to-litemlflow)
for full usage, flag reference, and troubleshooting.

## Deferred to v0.3

(All v0.2 deliverables landed: model registry, log-inputs/datasets, set_experiment auto-create.)

## Explicitly out of scope (use native API instead)

- `/api/2.0/mlflow/projects/run` — we are not an executor
- `/api/2.0/mlflow/gateway/...` — model gateway is a separate product space
- Anything involving `mlflow projects` CLI

## Error codes

We return MLflow's `ErrorCode` enum where applicable:

| Code | HTTP | Meaning |
|---|---|---|
| `RESOURCE_ALREADY_EXISTS` | 400 | duplicate experiment/run/param |
| `RESOURCE_DOES_NOT_EXIST` | 404 | not found |
| `INVALID_PARAMETER_VALUE` | 400 | malformed request |
| `INTERNAL_ERROR` | 500 | unexpected |
| `PERMISSION_DENIED` | 403 | auth required / insufficient |
| `BAD_REQUEST` | 400 | other client errors |

Error response shape:

```json
{
  "error_code": "RESOURCE_DOES_NOT_EXIST",
  "message": "experiment with id=42 does not exist"
}
```

## Run filter operators

`search_runs` supports the following filter operators via `parseRunFilter`:

| Operator | Example | Notes |
|---|---|---|
| `=` | `params.lr = '0.01'` | exact match |
| `!=` | `status != 'RUNNING'` | not equal |
| `<` `<=` `>` `>=` | `metrics.loss > 0.5` | numeric comparison on latest metric value |
| `LIKE` | `attributes.run_name LIKE 'train%'` | SQL LIKE on string fields |
| `IN (...)` | `attributes.run_id IN ('id1','id2')` | set membership; works on `attributes.*`, `params.*`, `tags.*` |
| `BETWEEN x AND y` | `metrics.score BETWEEN 0.5 AND 1.0` | inclusive range on latest metric value; also `attributes.start_time` / `end_time` |
| `AND` | `params.lr = '0.01' AND metrics.loss < 0.5` | chain multiple predicates |

Supported attribute columns for `attributes.*`: `run_id`, `run_name`, `status`, `start_time`, `end_time`.

## Compatibility test matrix

The `compat-test` Make target runs the MLflow Python client (versions 2.10, 2.11, 2.12, 2.13, 2.14) against a running LiteMLflow server and asserts these scripts complete without error:

1. `examples/quickstart.py` — log_metric, log_param, log_artifact
2. `examples/sklearn_example.py` — full sklearn workflow with autologging disabled
3. `examples/search_runs.py` — create N runs, search by filter, paginate
4. `examples/artifact_roundtrip.py` — upload, list, download, delete
