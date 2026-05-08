# Architecture

## Overview

LiteMLflow is a single Go binary that embeds:

- An HTTP/2 server (`net/http` + `chi` router)
- A SQLite database (pure-Go via `modernc.org/sqlite`)
- An optional DuckDB attachment for analytical queries
- A static UI bundle (vanilla HTML/CSS/JS)
- Storage backends for artifacts (filesystem in core; S3-compatible via plugin)
- An auth layer (no-op / basic / OIDC / mTLS)
- A migration runner

```
                +-------------------------------------------------+
                |                  litemlflow                     |
                |                                                 |
                |  +-----------------+   +-----------------+      |
                |  | HTTP/2 server   |   | go:embed UI     |      |
                |  | (chi router)    |   | (HTML/CSS/JS)   |      |
                |  +--------+--------+   +-----------------+      |
                |           |                                     |
                |  +--------+----------+  +------------------+    |
                |  | api/mlflow        |  | api/native       |    |
                |  | (compat surface)  |  | (LLM, prompts)   |    |
                |  +--------+----------+  +--------+---------+    |
                |           |                      |              |
                |       +---v----------------------v---+          |
                |       |       service layer          |          |
                |       |  experiments / runs / traces |          |
                |       |  metrics / params / artifacts|          |
                |       +---+--------------------------+          |
                |           |                                     |
                |  +--------v---------+   +------------------+    |
                |  | store (SQLite)   |   | artifact store   |    |
                |  | WAL, indexes     |   | fs / s3 plugin   |    |
                |  +------------------+   +------------------+    |
                +-------------------------------------------------+
                          |                          |
                  $DATA/litemlflow.db        $DATA/artifacts/
```

## Process and concurrency model

- One process. No agents, no workers, no queues in v1.
- Goroutines per request via `net/http` defaults.
- Single SQLite writer at any time (WAL mode + `BEGIN IMMEDIATE` for write transactions).
- A pool of read-only connections for concurrent reads.
- Long-running operations (e.g., artifact upload) stream directly to disk without buffering full payloads.

## Data flow: a typical `log_metric`

1. Python `mlflow.log_metric("loss", 0.42)` → POST `/api/2.0/mlflow/runs/log-metric`.
2. `internal/api/mlflow` decodes the body into a service-layer DTO.
3. Service layer validates the run exists and is `RUNNING` or `FINISHED`.
4. SQLite insert with `(run_id, key, value, timestamp, step)` PK.
5. Response: `{}` (MLflow protocol).

For batches (`log-batch`), all rows are inserted inside one transaction.

## Storage layout

```
$DATA/
├── litemlflow.db         # SQLite WAL mode (the database)
├── litemlflow.db-wal     # WAL log (auto-managed)
├── litemlflow.db-shm     # Shared memory (auto-managed)
├── artifacts/
│   └── <run-id>/
│       └── <relative-path>   # uploaded files
└── plugins/                  # plugin sockets and config (future)
```

A `litemlflow backup` is just `tar -czf snap.tgz $DATA`.

## Schema versioning

- Schema is migrated by an embedded migration runner using sequentially numbered SQL files: `001_init.sql`, `002_add_traces.sql`, etc.
- Each migration has an `up` block and a `down` block separated by `-- DOWN`.
- A `schema_migrations` table records the current version.
- The server refuses to start if it detects a future schema (downgrade requires explicit `--allow-downgrade`).

## Performance budgets

| Operation | p50 | p95 | p99 |
|---|---|---|---|
| `log_metric` (single) | < 2 ms | < 8 ms | < 25 ms |
| `log_batch` (1000 rows) | < 30 ms | < 80 ms | < 200 ms |
| `search_runs` (100 results, indexed) | < 20 ms | < 60 ms | < 150 ms |
| First paint (UI list, 5k runs) | < 300 ms (cold) | < 500 ms | < 800 ms |
| Cold start (binary up) | < 200 ms | < 500 ms | — |

These are tested by the perf-regression CI; >5% regression on any line blocks merge.

## Security boundaries

- Only the configured listener accepts requests; no other ports are opened.
- All artifact serving uses `Content-Disposition: attachment` and never `Content-Type: text/html` to defeat HTML smuggling.
- All artifact paths are confined to `$DATA/artifacts/` via `filepath.Clean` + prefix check.
- Auth runs before any data-mutating endpoint.
- Plugin processes communicate over a UNIX socket with capabilities (storage / auth / notify).
- Secrets (e.g., S3 keys, OIDC client secret) are read from env vars or a config file with mode `0600`.

## API surfaces

- `/api/2.0/mlflow/...` — MLflow REST compatibility (see [api-mlflow-compat.md](api-mlflow-compat.md)).
- `/api/v1/...` — LiteMLflow native API (see [api-native.md](api-native.md)).
- `/v1/traces` — OpenTelemetry/OTLP-compatible trace ingest (HTTP/JSON form; gRPC OTLP is post-1.0).
- `/healthz`, `/readyz`, `/version` — operational endpoints.
- `/ui/*` — static SPA assets (served from `go:embed`).
- `/` — redirects to `/ui/`.

## Dependency policy

Only the following classes of third-party dependencies are allowed in core:

- The Go standard library
- A pure-Go SQLite driver (`modernc.org/sqlite`)
- A small HTTP router (`go-chi/chi`)
- `golang.org/x/...` modules where appropriate

Any other dependency requires an ADR (`docs/adr/NNNN-add-<dep>.md`) approved by maintainers.
