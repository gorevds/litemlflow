# LiteMLflow — execution report

Date: 2026-05-07
Scope: full v0.1 build executed in one session.

## What was built

A working, tested, documented v0.1 of LiteMLflow: a single-binary, MLflow-API-compatible experiment tracker with native LLM trace support. The result is a 16 MB Go binary plus a Python SDK plus an embedded UI plus a 12-check compatibility test suite that verifies the real official MLflow Python client works against it.

### Stats

| | Lines | Files |
|---|---:|---:|
| Go (non-test) | 4,447 | 16 |
| Go (test) | 867 | 5 |
| Python | 616 | 4 |
| UI (HTML/CSS/JS + embed.go) | 646 | 4 |
| Markdown documentation | 1,033 | 16 |
| **Total content** | **~7,600** | **45+** |

Binary size: 16 MB (Go 1.22, pure-Go SQLite via `modernc.org/sqlite`, embedded UI assets).

## Phases delivered

| Phase | Outcome |
|---|---|
| P0. Skeleton + LICENSE + governance | LICENSE (Apache 2.0), NOTICE, .gitignore, README, CONTRIBUTING, CODE_OF_CONDUCT, SECURITY, Makefile |
| P1. Specifications | vision.md, architecture.md, data-model.md, api-mlflow-compat.md, api-native.md, threat-model.md, ADR 0001 |
| P2a. Go core (store, models, migrations) | SQLite WAL store, embedded migration runner with tested `up`/`down`, full domain model |
| P2b. Go server (HTTP, MLflow compat, native API) | chi router, middleware stack (request-id, logging, recovery, body-limit, auth), MLflow REST surface, native API, OTLP/JSON |
| P2c. Python SDK | `litemlflow` package with `Client`, `Run`, `Span`; idiomatic context managers; SDK-against-server pytest suite |
| P2d. Embedded UI | vanilla HTML/CSS/JS SPA: experiments list, runs list, run detail with metrics charts and trace waterfall |
| P3. Tests | Go unit + integration tests; Python SDK tests; the official MLflow Python client compat suite |
| P4. Independent code review + fixes | 6 legitimate findings fixed: ctx-key bug, artifact orphan prevention, restore mode sanitization, auth header smuggling defense, error propagation, validation gaps |
| P5. Documentation | README, quickstart, cookbook (8 recipes), API specs, ADR, threat model |
| P6. Distribution | Multi-stage distroless Dockerfile, GitHub Actions CI (Go/Python/lint/Docker), GitHub Actions release workflow (linux+darwin × amd64+arm64) |
| P7. Final integration | E2E backup/restore round-trip preserving metadata + artifacts |

## Test results

```
=== Go unit + integration ===
ok  internal/artifact   coverage 73.5%
ok  internal/migrations coverage 76.6%
ok  internal/model      coverage 76.3%
ok  internal/server     coverage 71.7%
ok  internal/store      coverage 56.4%
(all run with -race; no flaky tests)

=== Python SDK ===
11 passed in 0.22s

=== MLflow client compat suite (the headline test) ===
[ok] create_experiment
[ok] get_experiment_by_name
[ok] start_run + log_param + log_metric
[ok] get_run + get_metric_history (history len=5)
[ok] log_batch (50 metrics + 20 params + 1 tag)
[ok] metric_history after batch len=50
[ok] search_runs filter='params.lr = 0.01'
[ok] log_artifact + list_artifacts
[ok] rename_experiment
[ok] delete + restore experiment
[ok] delete + restore run
All MLflow compat checks passed.

=== End-to-end backup/restore ===
[1]  created experiment id=1
[2]  created run
[3]  logged metric loss=0.42
[4]  uploaded artifact
[5]  UI index served
[6]  server stopped
[7]  backup created (3.8 KB)
[8]  restored into fresh dir
[9]  metric survived restore
[10] artifact survived restore
=== E2E PASSED ===
```

## What works today

- **MLflow REST API compat**: experiments (create/get/get-by-name/search/delete/restore/update/set-tag), runs (create/get/update/delete/restore/search/log-metric/log-parameter/log-batch/set-tag/delete-tag), metric history, artifacts (list/upload/download/delete via `mlflow-artifacts` API).
- **Native API**: traces (manual ingest), prompts (versioned, content-addressed, aliases), evals, OTLP/JSON receiver.
- **Embedded UI**: experiments → runs → run detail with metrics charts and trace waterfall, light/dark theme.
- **CLI**: `up`, `migrate`, `rollback`, `backup`, `restore`, `version`.
- **Auth**: anonymous, basic (with constant-time hash compare); OIDC scaffolded for v0.2.
- **Storage**: SQLite WAL with concurrent readers, single writer, foreign keys enforced; filesystem artifacts with path-traversal defense.
- **Operational**: signal-handled graceful shutdown, JSON structured logs, request IDs, `/healthz`/`/readyz`/`/version`.

## What was deliberately deferred

| Item | Reason | Target |
|---|---|---|
| OIDC | v0.1 focused on MLflow compat; OIDC has its own threat-model surface | v0.2 |
| Built-in TLS via Let's Encrypt | Caddy/Traefik solves it externally well; depends on autocert design | v0.2 |
| Plugin host (S3/GCS) | Out-of-process gRPC plugins are non-trivial; filesystem is sufficient for hero user | v0.2 |
| gRPC OTLP | OTLP/JSON covers the interactive usage; gRPC adds proto pipeline | v0.2 |
| Workspaces (multi-tenant UI) | API scaffolded, single-workspace works for hero user | v0.2 |
| MLflow Model Registry | Significant new API surface; doesn't block compat for tracking-only users | v0.2 |
| Server-side metric downsampling | Client-side cap (1000 points) sufficient for current scale | v0.3 |

## Key engineering decisions

1. **Pure-Go SQLite (`modernc.org/sqlite`)** — pays a 10–30% perf cost vs CGO, gains: trivial cross-compilation, single static binary on every platform, no glibc dependency. ADR 0001 records this.
2. **Model types as DTOs** for the native API — cleaner than maintaining parallel structs; MLflow compat handlers use dedicated DTOs because of MLflow-specific aliases (`run_uuid`).
3. **Header-based identity passthrough** instead of a shared context-key package — middleware sets `X-LiteMLflow-User` after stripping any client-supplied value, handlers read it. Avoids a circular import path between `server` and `api/native`.
4. **Idempotent metric writes** — `INSERT … ON CONFLICT DO NOTHING` so retries are safe. Param writes are also idempotent for identical values; differing values raise `RESOURCE_ALREADY_EXISTS`.
5. **One transaction per migration** — `BEGIN IMMEDIATE`, all DDL inside; rollback if any statement fails. `down` blocks tested in CI.
6. **Backup as `tar.gz`** — copies SQLite WAL files verbatim, so even a hot backup is at least crash-consistent (matches a power-loss snapshot).

## Code review fixes applied

The independent reviewer flagged 11 items. Acted on the legitimate 6:

1. **`Whoami` context-key bug** — was using `struct{}{}` as key. Fixed via header-based identity (also defends against header smuggling).
2. **Artifact upload to non-existent run** — added explicit `GetRun` check in the artifacts router; 404 with `RESOURCE_DOES_NOT_EXIST`.
3. **Restore file-mode sanitization** — strips setuid/world-writable bits from archive entries; never restores anything beyond `0o644`.
4. **`Content-Disposition` defense in depth** — escape quotes in artifact filenames even though path validation should make this impossible.
5. **`GetRunData` swallowing sub-fetch errors** — every step now propagates errors instead of returning a partial response.
6. **`DeleteTag` input validation** — explicit checks for empty `run_id`/`key`.

The other items were either documentation (race comments on `CreatePrompt`, parser behavior on multiple operators) or correct-as-designed.

## Things I would do next (post-v0.1)

In priority order:

1. **`mlflow.set_experiment` autocreate semantics** — quick win, MLflow auto-creates experiments by name on `set_experiment`; we probably want to mirror this exactly.
2. **`mlflow.log_artifact` with `artifact_path` prefix** — current implementation handles the simple case; nested paths via subdir need more testing.
3. **OIDC** with PKCE, refresh tokens, and group→workspace mapping.
4. **Built-in TLS via `golang.org/x/crypto/acme/autocert`** — eliminates the Caddy dependency for solo deployments.
5. **Server-sent events for metric streaming** so the UI updates live during a training run.
6. **DuckDB attached over the SQLite file** for analytical queries (e.g., aggregations across thousands of runs).

## Files of interest for reviewers

- `internal/store/sqlite.go` — the heart of the system
- `internal/api/mlflow/handlers.go` — MLflow compat layer (read alongside `docs/spec/api-mlflow-compat.md`)
- `internal/api/native/handlers.go` — native API + OTLP receiver
- `tests/integration/mlflow_compat.py` — the canonical compat test
- `cmd/litemlflow/main.go` — CLI surface
- `docs/architecture.md` — design overview
- `docs/spec/data-model.md` — the unified ML+LLM graph
- `docs/spec/threat-model.md` — security boundaries

## How to verify

```bash
# Go: lint + race + coverage
go vet ./... && go test -race -coverprofile=coverage.txt ./...

# Build the binary
make build  # → bin/litemlflow

# Python SDK tests (require the binary)
pip install -e python/[dev] mlflow-skinny
pytest python/tests/

# The headline integration: real MLflow Python client against LiteMLflow
python tests/integration/mlflow_compat.py
```

All four steps pass on this commit.
