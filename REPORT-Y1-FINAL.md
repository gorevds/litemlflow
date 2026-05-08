# LiteMLflow — Year-1 Final Report

**Date:** 2026-05-08
**Final tag:** `v1.0.0-rc1`
**Live deployment:** https://lmf.gorev.space (running v1.0.0-rc1)

This is the cumulative report for the entire year-1 program — from the first commit to v1.0.0-rc1. Per-wave details are in `REPORT-Y1.md` (v0.2 only) and CHANGELOG.md (per-release).

## Roadmap delivery

The [docs/roadmap-y1.md](docs/roadmap-y1.md) plan had four quarterly themes. All four shipped:

| Quarter | Theme | Tag | Date | Status |
|---|---|---|---|---|
| Q1 | Foundation | v0.1.0 / v0.1.1-deploy | 2026-05-07 | ✅ |
| Q2 | Production hardening | v0.2.0-rc1 | 2026-05-08 | ✅ |
| Q3 | Scale & ergonomics | v0.3.0-rc1 | 2026-05-08 | ✅ |
| Q4 | Polish & distribution | v0.4.0-rc1 | 2026-05-08 | ✅ |
| — | Stabilization | v1.0.0-rc1 | 2026-05-08 | ✅ |

Five releases, 47 commits, 4 tags. Each major release was deployed live and validated against the real MLflow Python client (3.12) before moving to the next.

## What got built

### Server (Go, 19,435 LOC main module + 1,113 LOC operator module)

- **MLflow REST API compatibility** — 31 canonical client checks pass: experiments, runs, metrics, params, tags, artifacts, search with `=`/`!=`/`<`/`>`/`<=`/`>=`/`LIKE`/`IN(...)`/`BETWEEN`, `log-batch`, `log-inputs` (datasets), pagination, `set_experiment` autocreate, view types.
- **MLflow Model Registry** — registered models, model versions, aliases, transitions, tags. Full surface.
- **Native API** for things MLflow doesn't model: traces, prompts (versioned + content-addressed), evals.
- **OTLP receiver** in two transports: HTTP/JSON at `/v1/traces` and gRPC at `--otlp-grpc-addr` (default 4317). Both share the same `Store.InsertSpans` path.
- **Server-side metric downsampling** — LTTB algorithm via `?downsample=N`.
- **Workspaces multi-tenancy** — `X-Workspace` header / cookie, default workspace seeded.
- **RBAC enforcement** — viewer/editor/admin roles via path-prefix → required-role table. Open-mode rule on default workspace with zero members.
- **OIDC auth** — full PKCE flow with nonce, RS256 JWT verification, JWKS caching, HTTPS enforcement on issuer/token-endpoint/JWKS URIs, constant-time nonce comparison.
- **Sessions** — HttpOnly cookies, auto-Secure based on transport, server-side TTL.
- **S3 artifact backend** — pure-Go SigV4 signing (no aws-sdk dep), single PUT for small files, multipart upload >100 MiB threshold, MinIO + AWS S3 + path/virtual-host addressing, bucket name validation.
- **Filesystem artifact backend** (default) — single-binary deployment.
- **Prometheus `/metrics`** — 12 metric families (request counters, latency histograms, runs/metrics counters, sessions, DB size, build info, process metrics).
- **`litemlflow import-mlflow` CLI** — copies experiments + runs + metrics + params + tags + artifacts from a running MLflow tracking server. Resumable via per-run idempotency check + `.import-state.json` checkpoint.

### CLI

`up | migrate | rollback | backup | restore | import-mlflow | version` — single static binary, no runtime deps.

### Distribution

- Docker image (multi-stage, distroless)
- Homebrew formula (macOS + Linux, amd64 + arm64)
- Debian source tree (`dpkg-buildpackage`-ready)
- RPM spec (`rpmbuild`-ready)
- Snap manifest
- Helm chart (StatefulSet + PVC + ServiceMonitor + ingress)
- Kubernetes operator (separate `litemlflow-operator` Go module with CRD `litemlflow.dev/v1alpha1`)
- GitHub Actions release workflow building 4 platform binaries
- `make dist-helm-lint`, `make dist-helm-template`, `make dist-deb`, `make dist-rpm` targets

### Embedded UI (~60 KB CSS+JS, no build step)

- Experiments list, runs list, run detail with metrics charts and trace waterfall
- Real prompts page with version diff (v0.4)
- Runs list bulk-select (Compare / Delete / Export JSON)
- Workspaces list, member-management page (admin-gated)
- Keyboard shortcuts (`?` for help, `j/k` navigation, `g+e/p/h` chord, `cmd+K` palette, `/` for search, `Esc` to close)
- Command palette with debounced experiment search
- Embed mode (`?embed=1` strips header/footer for iframe integration)
- Workspace selector dropdown
- Light/dark theme

### Python SDK (~2,674 LOC)

- `litemlflow.Client` — native HTTP client with `start_run` / `log_metric` / etc. context managers
- `litemlflow[langchain]` — `LiteMLflowCallbackHandler` for LangChain. Spans + token cost from built-in pricing table (GPT-4o, Claude 3.5, Gemini)
- `litemlflow[llamaindex]` — `LiteMLflowEventHandler` for LlamaIndex. Same pricing table via `litemlflow._pricing`

### Testing infrastructure

- 24 Go test files, all green with `-race`
- 4 Go fuzz targets covering parsers + JWT + OTLP
- 5 Go chaos scenarios (build tag `chaos`) — kill mid-write, full disk, corrupt WAL, migration mid-fail, concurrent close
- Mutation-testing scaffolding via `make mutation` (gremlins)
- 35 Python SDK + LangChain + LlamaIndex tests (2 LlamaIndex skipped without llama-index-core)
- 31-check MLflow Python client compat suite — runs against live `lmf.gorev.space` on every release

## Performance

Comparative benchmark vs MLflow + SQLite from `docs/bench-v04.md`:

| Metric | LiteMLflow | MLflow | Ratio |
|---|---:|---:|:---:|
| Cold start | **53 ms** | 7,513 ms | **143× faster** |
| `log_metric` p50 | **1.44 ms** | 21.6 ms | **15× faster** |
| `log_metric` p95 | **1.68 ms** | 30.9 ms | **18× faster** |
| `log_batch` throughput | **24,533 rows/s** | 8,008 rows/s | **3.1× faster** |
| `search_runs` p50 (1k runs) | **45.9 ms** | 46.5 ms | tied |
| Populate 1,000 runs | **3.8 s** | 64.7 s | **17× faster** |

Where MLflow wins: raw sequential scan of 50k metric points (124 ms vs 2.6 ms) — LiteMLflow uses indexed lookups optimized for time-range queries; MLflow's column-scan beats us at full-table reads. Honest reporting in the doc.

## Multi-agent execution model

The project was built end-to-end in 5 waves of specialist agents with worktree isolation:

| Wave | Agents | Scope | Date |
|---|---|---|---|
| W1 | 5 (Auth, Storage, Registry, Tenancy, Compat) | v0.2 production hardening | 2026-05-08 |
| W2 | 2 (Perf, Observability) | v0.2 metric downsampling + Prometheus | 2026-05-08 |
| W3 | 3 (LLM, Migration, Auth) | v0.3 LangChain + import-mlflow + RBAC + OIDC nonce | 2026-05-08 |
| W4 | 3 (Distribution, Frontend, LLM/Perf) | v0.4 packaging + UI v2 + LlamaIndex + bench | 2026-05-08 |
| W5 | 3 (Backend, QA, Platform) | v1.0 multipart S3 + gRPC + fuzz/chaos + operator | 2026-05-08 |
|  | **16 total agent runs** |  |  |

After each wave: integration in main worktree (resolving conflicts), independent code review by a fresh-eyes agent, fixes for legitimate findings, full test suite, deploy to live, tag.

### Independent review findings (all fixed)

- W1: 6 (S3 SigV4 URL encoding, OIDC HTTPS-enforce, S3 unbounded streaming, cookie auto-Secure, header smuggling, validation gaps)
- W3: 3 (nonce constant-time compare, import-mlflow per-run idempotency, skip-with-log on per-run errors)
- W4: 3 (over-broad gitignore on dist/, silent LlamaIndex event drops, bench doc store name)
- W5: 1 (gRPC server DoS hardening — MaxRecvMsgSize + MaxConcurrentStreams)
- **13 fixes total**

## Live state

```
https://lmf.gorev.space → v1.0.0-rc1 (9a517ae, 2026-05-08T10:25:38Z)
schema version 5
seeded with 3 demo experiments, 12 runs (5 with trace waterfall),
8 prompt versions (4 names, 4 aliases), 1 eval run.
```

`/metrics` exposes 12 Prometheus metric families. `/healthz`, `/readyz`, `/version` are public. All MLflow REST endpoints work for the canonical 80% client surface.

## Known limitations carried into v1.0 stable

- Mutation-testing baseline is a placeholder — needs gremlins on a CI runner.
- External pen test pending.
- Astro/Starlight docs site pending (markdown is the current source of truth).
- Terraform provider scaffolded as a recommendation but not built (separate Go module like operator).
- Real-cluster validation of operator: unit tests pass; envtest skipped without `KUBEBUILDER_ASSETS`.
- gRPC OTLP TLS: not natively supported (operators put a sidecar in front).

## Recommendations for v1.0 stable

1. **External pen test** by an independent firm. Bake the findings into v1.0.
2. **Get gremlins running in CI** to capture the mutation-test baseline; gate PRs on >70% mutation score in `internal/store/` and `internal/auth/`.
3. **Spin up a kind cluster in CI**, apply the CRD + operator + a `LiteMLflow` resource, validate the StatefulSet rolls out.
4. **Push to GitHub** (currently local-only) and tag v1.0.0-rc1 publicly.
5. **Public launch**: HN post, MLOps Slack, conference talk submission. The 143× cold-start number is a strong headline.

## Files

- [`docs/roadmap-y1.md`](docs/roadmap-y1.md) — original year plan
- [`CHANGELOG.md`](CHANGELOG.md) — per-release notes
- [`REPORT-Y1.md`](REPORT-Y1.md) — v0.2-only report (W1+W2)
- This file: cumulative Y1 report through v1.0.0-rc1
- [`docs/bench-v04.md`](docs/bench-v04.md) — comparative benchmark vs MLflow
- [`docs/governance.md`](docs/governance.md) — project governance model
- [`tests/integration/mlflow_compat.py`](tests/integration/mlflow_compat.py) — the canonical compat test (31 checks, all green)
- [`scripts/demo/seed.py`](scripts/demo/seed.py) — populate a live instance with realistic content

## Verification commands

```bash
# All Go tests, race detection
go test -race -count=1 ./...

# Operator tests (separate module)
make operator-test

# Python SDK + LangChain + LlamaIndex
.venv/bin/python -m pytest python/tests/

# MLflow client compat against live HTTPS
.venv/bin/python tests/integration/mlflow_compat.py --addr lmf.gorev.space

# Performance benchmark vs MLflow
.venv/bin/python tests/integration/bench.py --runs 1000

# Fuzz testing (20s per target)
make fuzz-short

# Chaos tests
make test-chaos
```

All of these were run on the v1.0.0-rc1 commit. All green.
