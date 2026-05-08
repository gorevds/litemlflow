# Architecture

## Overview

LiteMLflow is a single Go binary that embeds:

- An HTTP/2 server (`net/http` + `chi` router)
- A SQLite database (pure-Go via `modernc.org/sqlite`)
- An optional DuckDB attachment for analytical queries
- A static UI bundle (vanilla HTML/CSS/JS)
- Storage backends for artifacts: **filesystem** (default) and **S3-compatible** (built-in, no extra dependency)
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

### Filesystem backend (default)

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

### S3-compatible backend

When `--artifact-backend s3` is set, the SQLite database remains on local disk
while artifacts are stored in an S3-compatible object store (AWS S3, MinIO,
Garage, Ceph, etc.). The object key layout mirrors the filesystem layout:

```
<prefix>artifacts/<run-id>/<relative-path>
```

All requests are signed with **AWS Signature Version 4** implemented in pure Go
(`crypto/hmac`, `crypto/sha256`, `encoding/hex`) — no SDK dependency is added.
Addressing defaults to **path-style** for non-amazonaws.com endpoints (required
by MinIO) and to **virtual-hosted style** for amazonaws.com.

The backend is selected in `internal/server/server.go` at the artifact-store
construction point (marked `// STORAGE-S3`) and implements the same `artifact.Store`
interface as `FilesystemStore`, making it fully transparent to all API handlers.

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
- `/metrics` — Prometheus/OpenMetrics scrape endpoint (see Observability section).
- `/ui/*` — static SPA assets (served from `go:embed`).
- `/` — redirects to `/ui/`.

## Observability

LiteMLflow exposes a `GET /metrics` endpoint that returns metrics in the
[OpenMetrics text format](https://openmetrics.io/) (content-type:
`text/plain; version=0.0.4`), which all current Prometheus versions accept.

### Implementation

The metrics layer lives in `internal/metrics/`. It is implemented without
external dependencies — no `prometheus/client_golang` — using straightforward
string-building in `registry.go`. The registry supports three metric types:

| Type | Description |
|---|---|
| Counter | Monotonically increasing float64, keyed by an optional label set |
| Gauge | Scalar float64 (no labels); set/add in place |
| Histogram | Distribution of float64 values across fixed upper-bound buckets |

Label sets are stored as a `map[string]float64` keyed by a null-delimited
string (`"k1=v1\x00k2=v2"`), built from declared key names in construction
order. This avoids sorting on every observation.

`internal/metrics/standard.go` pre-defines the application metric set and is
wired into `buildRouter` in `internal/server/server.go`.

### Exposed metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `litemlflow_http_requests_total` | counter | `method`, `path`, `status` | HTTP requests, by method, route template, and status code |
| `litemlflow_http_request_duration_seconds` | histogram | `method`, `path` | Request latency (11 default buckets: 5 ms–10 s) |
| `litemlflow_runs_created_total` | counter | — | Experiment runs created |
| `litemlflow_metrics_logged_total` | counter | — | Metric data-points logged (single + batch) |
| `litemlflow_active_sessions` | gauge | — | Active user sessions in the session store |
| `litemlflow_db_size_bytes` | gauge | — | SQLite database file size (refreshed per scrape) |
| `litemlflow_build_info` | gauge (=1) | — | Always 1; signals binary is alive |
| `litemlflow_build_info_labels` | counter (=1) | `version`, `commit` | Build version/commit for dashboard grouping |
| `litemlflow_process_cpu_seconds_total` | gauge | — | User+system CPU (from `/proc/self/stat`; HZ=100 assumed) |
| `litemlflow_process_resident_memory_bytes` | gauge | — | RSS from `/proc/self/status` (Linux) or `runtime.Sys` |
| `litemlflow_process_open_fds` | gauge | — | Open file descriptors (`/proc/self/fd` count) |
| `litemlflow_process_goroutines` | gauge | — | `runtime.NumGoroutine()` |

### Path-template normalization

The `metricsMiddleware` in `internal/server/middleware.go` reads the matched
route pattern from `chi.RouteContext(r.Context()).RoutePattern()` after the
handler returns. This returns the registered pattern (e.g.
`/api/v1/prompts/{name}`) rather than the concrete URL path
(`/api/v1/prompts/my-prompt`), preventing cardinality explosion from
per-entity IDs and run UUIDs. For unmatched routes (404s), the path is
truncated to the first two segments.

### Auth bypass

`/metrics` is listed in `isPublicPath` so the auth middleware skips it
entirely. Prometheus scrapers do not send credentials by default, and
exposing the scrape endpoint publicly does not leak user data (only aggregate
server statistics).

## Dependency policy

Only the following classes of third-party dependencies are allowed in core:

- The Go standard library
- A pure-Go SQLite driver (`modernc.org/sqlite`)
- A small HTTP router (`go-chi/chi`)
- `golang.org/x/...` modules where appropriate

Any other dependency requires an ADR (`docs/adr/NNNN-add-<dep>.md`) approved by maintainers.
