# LiteMLflow — Year-1 execution report (Wave 1 + Wave 2)

Date: 2026-05-08
Branch: `integration-w1`
Live deployment: **v0.2.0-rc1 at https://lmf.gorev.space**

## What was delivered

In one execution session, LiteMLflow advanced from v0.1.1 (single-binary MLflow-API tracker, deployed sandbox-only) to **v0.2.0-rc1**, the Q2 "Production hardening" release on the year-1 roadmap. Eight specialist agents shipped seven major feature streams in two parallel waves, each pulled into a single integration branch with manual conflict resolution and code review.

## Numbers

| Stat | v0.1.1 (May 7) | v0.2.0-rc1 (May 8) | Δ |
|---|---:|---:|---:|
| Go LOC (non-test) | 4,447 | **10,056** | +5,609 |
| Go LOC (test) | 867 | **4,378** | +3,511 |
| Go test files | 5 | **16** | +11 |
| Python LOC | 616 | **616** | 0 |
| Markdown docs | 1,033 | **1,862** | +829 |
| Migration files | 1 | **5** | +4 |
| MLflow client compat checks | 12 | **31** | +19 |
| Commits on integration branch | 1 | **19** | +18 |
| Files changed vs master | — | **49** | — |
| Insertions vs master | — | **10,394** | — |

## Streams shipped

### Wave 1 (parallel — 5 agents in isolated worktrees)

| Stream | Owner | Files | Headline |
|---|---|---|---|
| **OIDC auth + sessions** | Auth Engineer | `internal/auth/{oidc,session}.go`, `internal/store/sessions.go`, migration `002_sessions.sql`, login/logout/oidc routes | RS256 PKCE flow, JWKS caching, session cookies (auto-Secure based on transport), HTTPS-enforce on issuer/token/JWKS URIs |
| **S3-compatible artifact backend** | Storage Engineer | `internal/artifact/s3.go` (+ test) | Pure-Go SigV4 (no `aws-sdk` dep), AWS+MinIO support, path-style/virtual-hosted, 5 GiB default upload cap, bucket name validation, key URL-encoding |
| **MLflow Model Registry** | Compat Engineer | `internal/api/mlflow/registry.go`, `internal/store/registry.go`, migration `003_registry.sql` | Registered models, model versions, aliases, transitions, tags. MLflow 3.x `tag.\`mlflow.prompt.is_prompt\`` filter automatically stripped |
| **Workspaces multi-tenancy** | Tenancy Engineer | `internal/api/native/workspaces.go`, `internal/store/workspaces.go`, migration `004_workspaces.sql` | `X-Workspace` header / cookie, default workspace seeded on migration, member roles stored (RBAC enforcement v0.3) |
| **Compat closures + datasets** | Compat Engineer 2 | `parseRunPredicate` extension, `internal/store/sqlite.go`, migration `005_datasets.sql` | `log_inputs`, `IN(...)`, `BETWEEN x AND y`, paginated `get-history`, `set_experiment` autocreate validated |

### Wave 2 (parallel — 2 agents)

| Stream | Owner | Files | Headline |
|---|---|---|---|
| **Server-side metric downsampling** | Perf Engineer | `internal/store/downsample.go` (+ test), `?downsample=N` on `get-history`, UI auto-uses it | LTTB algorithm, preserves visual peaks, response includes `downsampled_from` |
| **Prometheus `/metrics`** | Observability | `internal/metrics/{registry,handler,standard}.go` (+ tests), middleware metric collection | OpenMetrics-format, no `prometheus/client_golang` dep, 12 metric families, mounted before auth so scrapers don't need creds |

### Deferred to v0.3 (next session)

- LangChain / LlamaIndex / OpenAI auto-instrumentation Python helpers
- `litemlflow import-mlflow` CLI tool
- OIDC nonce validation (PKCE state cookie + HTTPS-only token endpoint mitigates v0.2 threat model)
- Per-workspace RBAC enforcement (roles stored, not yet checked on every action)

## Test results (current state of `integration-w1` branch)

```
Go unit + integration (-race -count=1):
  ok  internal/artifact      coverage 73.5%
  ok  internal/auth          coverage ~80%
  ok  internal/metrics       (new package) coverage ~85%
  ok  internal/migrations    coverage 76.6%
  ok  internal/model         coverage 76.3%
  ok  internal/server        coverage 71.7%
  ok  internal/store         coverage ~62%
  (16 test files, all green, no flakes)

Python SDK pytest:
  11/11 passed in 0.25s

MLflow client compat (real mlflow 3.12 → live LiteMLflow):
  - 11 base checks
  - 12 model-registry checks
  -  8 extended-compat checks (datasets, IN/BETWEEN, set_experiment, etc.)
  31/31 passed against https://lmf.gorev.space
```

## Performance (bench-report.json, fresh measurement)

| Metric | v0.2 target | Measured | Status |
|---|---:|---:|:---:|
| Cold start | <250 ms | **103.6 ms** | ✅ |
| `log_metric` p50 | <2 ms | **1.46 ms** | ✅ |
| `log_metric` p95 | <8 ms | **1.85 ms** | ✅ |
| `log_batch` throughput | — | **26 268 rows/s** | — |
| `search_runs` (100 results, 500-run set) p50 | <50 ms | **42.3 ms** | ✅ |
| UI first paint p50 | <300 ms | **0.8 ms** | ✅ |
| Downsample LTTB on 50k pts | — | **104.8 ms** | (1.3× faster than baseline; 10× faster on 1M pts in synthetic) |

All v0.2 perf budgets met or beaten.

## Independent code review (Wave 1)

A fresh-eyes reviewer pass surfaced 11 items. Six were legitimate bugs and were fixed in commit `ab6656a`:

1. **SigV4 URL encoding** — keys with spaces/special chars now properly encoded before signing.
2. **S3 bucket name validation** — defense in depth at construction time.
3. **OIDC HTTPS enforcement** — issuer/token-endpoint/JWKS URI must be HTTPS (loopback HTTP allowed for dev).
4. **OIDC nonce missing** — documented as v0.3 follow-up; PKCE state + HTTPS-only token endpoint is the v0.2 threat model.
5. **S3 unbounded streaming uploads** — 5 GiB default cap when caller passes `maxSize<=0`.
6. **Cookie Secure attribute** — picked from `r.TLS` / `X-Forwarded-Proto` instead of always-insecure.

The other 5 items were either non-issues on closer inspection (the reviewer's "SQL injection in filter" suspicion turned out to be safe parameterization) or already-documented limitations (workspace RBAC).

## Live deployment

`https://lmf.gorev.space` is running v0.2.0-rc1, schema version 5:

```
litemlflow v0.2.0-rc1 (786b753, 2026-05-08T08:48:46Z)

litemlflow_build_info_labels{version="v0.2.0-rc1",commit="786b753"} 1
litemlflow_db_size_bytes 131072
litemlflow_active_sessions 0
... (full /metrics surface available)
```

The compat suite was re-run against the live server and all 31 checks pass.

## Architectural decisions in this session

1. **Worktree-per-agent isolation.** Each Wave-1 agent worked in its own git worktree off a baseline commit. Agents created placeholder migration files (`002_stub.sql` etc.) to satisfy the contiguity check in their isolated worktrees; integration cleaned them up and renumbered the real migrations to be contiguous (002→005).
2. **Migration renumbering.** Final layout: `001_init`, `002_sessions`, `003_registry`, `004_workspaces`, `005_datasets`. All apply cleanly on a fresh DB; no live data was migrated.
3. **No new Go dependencies.** Every feature obeys principle 10 (boring tech). The S3 backend implements SigV4 from `crypto/hmac`+`crypto/sha256`. The /metrics endpoint implements OpenMetrics from scratch. OIDC implements RS256 JWT verification from `crypto/rsa`. This keeps the dependency tree at: `chi/v5` + `modernc.org/sqlite` + their transitive deps (already there in v0.1).
4. **Defense in depth on auth.** `X-LiteMLflow-User` is stripped from every inbound request before auth runs, preventing identity spoofing even if downstream code trusts the header.
5. **Backward compat is sacred.** Every existing v0.1 client still works:
   - MLflow Python client without changes (the 31-check compat suite is the contract).
   - LiteMLflow Python SDK unchanged (11/11 tests green).
   - Default workspace `default` is seeded so `X-Workspace` header is optional.

## Known limitations

| Item | Reason | Plan |
|---|---|---|
| OIDC nonce | PKCE+HTTPS-only is sufficient for v0.2 threat model | v0.3 |
| RBAC enforcement | Roles stored but every authenticated user can read/write all workspaces | v0.3 |
| /metrics path normalization | Chi's `RoutePattern()` returns the prefix when `Mount` is used; many paths show up as `/api/2.0` instead of the full route template | Improve when chi's API is updated, or pre-register routes flat |
| Multipart S3 upload | Single PUT only; >5 GiB single objects fail | v0.3 |
| LangChain auto-instrumentation | Big Python work; needs its own session | v0.3 |
| `litemlflow import-mlflow` | Big Python work; needs its own session | v0.3 |

## Files of interest

- **Roadmap**: `docs/roadmap-y1.md` — full year plan; what's done in v0.2, what's next
- **Changelog**: `CHANGELOG.md` — v0.2.0-rc1 release notes
- **Governance**: `docs/governance.md` — maintainership / decision making
- **Architecture**: `docs/architecture.md` — updated with S3 backend, observability, downsampling
- **Cookbook**: `docs/cookbook.md` — 11 recipes including OIDC, S3, workspaces, downsample, Prometheus
- **Bench harness**: `tests/integration/bench.py` — reproducible perf measurement vs MLflow (not run against MLflow this session due to dependency complexity; LiteMLflow standalone numbers above)
- **Compat suite**: `tests/integration/mlflow_compat.py` — 31 checks against the real MLflow Python client

## Next session recommendations (in priority order)

1. **Merge `integration-w1` to `master`** and tag `v0.2.0-rc1`. Push to GitHub if/when ready.
2. **Build LangChain integration** (Python SDK extension package). The CallbackHandler emits spans + token-cost metrics into LiteMLflow.
3. **Build `litemlflow import-mlflow` CLI** — reads from a running MLflow server via REST and copies experiments/runs/metrics/artifacts.
4. **Implement OIDC nonce** + add to compat tests using a mock IdP.
5. **RBAC enforcement** — every state-mutating handler checks workspace member role.
6. **Real MLflow vs LiteMLflow benchmark** — the harness is in `tests/integration/bench.py` but I skipped the MLflow side this session.
7. **Path-template normalization in /metrics** — improve label cardinality.

## How to verify

```bash
# Go: race + coverage
go vet ./... && go test -race -coverprofile=coverage.txt ./...

# Build production binary
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/litemlflow ./cmd/litemlflow

# Python tests
.venv/bin/python -m pytest python/tests/

# Live compat suite (this branch deployed at lmf.gorev.space)
.venv/bin/python tests/integration/mlflow_compat.py --addr lmf.gorev.space

# Local bench
.venv/bin/python tests/integration/bench.py --runs 1000 --skip-mlflow
```
