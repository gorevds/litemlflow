# Changelog

All notable changes to LiteMLflow are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/) starting at v1.0.

## [v1.0.0-rc1] — 2026-05-08

The year-1 stabilization release. All Q1–Q4 roadmap streams delivered. See [docs/roadmap-y1.md](docs/roadmap-y1.md).

### Added

- **Multipart S3 upload** — large artifacts (default >100 MiB) are streamed in 100-MiB parts via S3's CreateMultipartUpload / UploadPart / CompleteMultipartUpload. Aborts cleanly on error. Threshold configurable via `--s3-multipart-threshold` / `LITEMLFLOW_S3_MULTIPART_THRESHOLD`.
- **gRPC OTLP receiver** — `--otlp-grpc-addr 127.0.0.1:4317` enables the OTel SDK's standard gRPC export path alongside the existing HTTP/JSON. Hardened with `MaxRecvMsgSize=64MiB` and `MaxConcurrentStreams=1024` belt-and-suspenders against DoS. Same `Store.InsertSpans` path as HTTP OTLP. ADR `docs/adr/0002-grpc-otlp-deps.md` justifies the new `grpc` + `otlp` deps.
- **Stability hardening** — Go native fuzz tests on the SQL filter parser (`FuzzParseRunPredicate`, `FuzzParseRunFilter`, `FuzzParseExperimentFilter`, `FuzzSplitOnAnd`), JWT validation (`FuzzVerifyIDToken`, `FuzzVerifyIDToken_SignatureCorruption`), OTLP ingest (`FuzzIngestOTLP`, `FuzzIngestTraces`). Chaos tests behind `chaos` build tag: `TestChaos_KillMidWrite` (kill DB mid-write), `TestChaos_FullDisk` (skipped without CAP_SYS_ADMIN), `TestChaos_CorruptWAL`, `TestChaos_MigrationCrashMidway`, `TestChaos_ConcurrentClose`. Mutation-testing scaffolding via `make mutation` (gremlins). New `make fuzz-short`, `make test-chaos`, `make mutation` targets. Threat model doc updated to reference the active fuzz coverage as a mitigation.
- **Kubernetes operator** at `operator/` (separate Go module `github.com/litemlflow/litemlflow-operator`) — controller-runtime v0.18.4, CRD `litemlflow.dev/v1alpha1` `LiteMLflow`, reconciler manages StatefulSet+Service+optional basic-auth secret. 8 unit tests pass (envtest skip is documented when KUBEBUILDER_ASSETS is absent). Recommendation: extract to standalone `litemlflow-operator` repo eventually.
- **Admin UI for workspace member management** — new routes `#/workspaces` and `#/workspaces/{id}/members` in `ui/static/app.js`. Add/remove members, change roles. Gated by 403 message when caller is not `admin`.

### Fixed (independent review pass)

- gRPC server hardening: `MaxRecvMsgSize=64MiB`, `MaxConcurrentStreams=1024` defense-in-depth against unauthenticated trace floods.

### Stats (cumulative since v0.1)

- Go LOC (main module): **~19,435** (+~3,300 vs v0.4)
- Go LOC (operator module): **~1,113** (new)
- Python LOC: **~2,674** (+~170 vs v0.4)
- Markdown docs: **~2,980** (+~510 vs v0.4)
- Go test files: 24 (incl. 3 fuzz, 1 chaos)
- Python tests: 35 + 2 skipped
- MLflow client compat: 31/31 still passing on live `https://lmf.gorev.space`
- Tagged releases: v0.2.0-rc1, v0.3.0-rc1, v0.4.0-rc1, v1.0.0-rc1
- Total commits since v0.1.1-deploy: 47

### Known limitations carried into v1.0 stable

- Operator + admin UI member management need real-cluster validation before declaring v1.0 stable.
- Mutation-testing baseline is a placeholder — needs first CI run.
- External pen test still pending.
- Astro/Starlight docs site still pending.
- Terraform provider still pending (separate Go module, similar structure to operator).

## [v0.4.0-rc1] — 2026-05-08

The Q4 "Polish & distribution" release. See [docs/roadmap-y1.md](docs/roadmap-y1.md).

### Added

- **Distribution artifacts under `dist/`**: Homebrew formula, Debian package source tree, RPM spec, Snap manifest, Helm chart with StatefulSet + PersistentVolumeClaim + ServiceMonitor + ingress. `make dist-helm-lint`, `make dist-deb`, `make dist-rpm` targets glue them to local CI. Documented in `dist/README.md` and `docs/quickstart.md` "Install via package manager".
- **UI v2 polish**: keyboard shortcuts (`?` for help, `j/k` to navigate lists, `g e/p/h` for global jumps, `Enter` to open, `Esc` to dismiss, `cmd+K`/`ctrl+K` for command palette, `/` to focus search). Command palette with debounced experiment search. Real prompts page with version history and side-by-side diff. Runs-list bulk-select with Compare / Delete / Export JSON. Embed mode (`?embed=1`) for iframe integration. Workspace selector dropdown in the header.
- **LlamaIndex auto-instrumentation**: `pip install 'litemlflow[llamaindex]'` and `LiteMLflowEventHandler` records query/retrieval/synthesis/LLM/chat/embed events as spans. Shares the pricing table with the LangChain handler via `litemlflow._pricing`. Stack-based parent tracking matches LlamaIndex's depth-first event order.
- **Comparative MLflow benchmark** at `docs/bench-v04.md` with raw JSON in `bench-v04.json`. Headline: **143× faster cold start, 15× faster log_metric p50, 3.1× faster log_batch throughput** vs MLflow + SQLite. Where MLflow wins (raw sequential metric scans), reported honestly.
- **Demo seeder** at `scripts/demo/seed.py` populates a live instance with realistic content: 3 experiments, 12 runs with traces, 8 prompt versions across 4 names with 4 aliases, 1 eval run.

### Fixed (independent review pass)

- `.gitignore`: rules `/dist/` and `dist/` (Python build) over-matched our distribution-artifacts directory `dist/`. Replaced with scoped `python/dist/` so `dist/{homebrew,debian,rpm,snap,helm}/` are tracked.
- `LiteMLflowEventHandler`: unrecognized event class names now log a warning (one per class name per handler instance) instead of silently dropping spans. Helps detect llama-index-core upgrades that rename events.
- `docs/bench-v04.md`: incorrectly described LiteMLflow's store as "bbolt"; corrected to SQLite (modernc.org/sqlite, pure Go).

### Stats

- Go LOC (non-test): 11,129 (unchanged — v0.4 is mostly distribution + UI + Python)
- Go test files: 18 — all green with `-race`
- Python LOC: ~2,500 (+~820 vs v0.3 — LlamaIndex handler + tests + bench harness extensions)
- UI bundle: 59.6 KB (CSS + JS, +40 KB vs v0.3)
- Markdown docs: 2,470 (+341 vs v0.3 — bench doc, dist/README, roadmap updates, cookbook recipes for shortcuts/LlamaIndex)
- 31/31 MLflow compat checks pass against live `https://lmf.gorev.space`
- 35 Python tests pass (LlamaIndex live tests skip when llama-index-core absent)

### Deferred to v1.0 / post-Y1

- Kubernetes operator (CRD + reconciler) — Helm chart covers the common case; operator would manage many instances
- Terraform provider — relies on a stable HTTP-only management API
- External pen test
- Public docs site (Astro/Starlight at docs.litemlflow.dev)
- Multipart S3 upload (still single PUT)
- gRPC OTLP receiver
- OIDC nonce was already added in v0.3; remaining auth work is RBAC for non-default workspaces (already enforced) and admin UI for member management

## [v0.3.0-rc1] — 2026-05-08

The Q3 "Scale & ergonomics" release. See [docs/roadmap-y1.md](docs/roadmap-y1.md).

### Added

- **LangChain auto-instrumentation.** `pip install 'litemlflow[langchain]'` and pass `LiteMLflowCallbackHandler` to any chain — every chain, LLM, chat-model, tool, retriever call is recorded as a span. Token usage and USD cost computed from a built-in pricing table for OpenAI / Anthropic / Google models.
- **`litemlflow import-mlflow` CLI.** Reads from a running MLflow tracking server via REST and copies experiments, runs, metrics (full history), params, tags, artifacts into a LiteMLflow data dir. Resumable via per-run idempotency check + `.import-state.json` checkpoint; per-run errors are logged and skipped rather than aborting the whole import.
- **Workspace RBAC enforcement.** `viewer` / `editor` / `admin` roles are now enforced on every request, gated by a path-prefix → required-role table (`internal/server/rbac.go`). Open-mode rule: the `default` workspace with zero configured members allows full access — fresh installs need no setup.
- **OIDC nonce.** PKCE flow now generates a random nonce, includes it in the auth URL, and validates `claims["nonce"]` on token exchange via constant-time comparison. Backward-compatible with in-flight v0.2 sessions (state cookies missing the nonce field skip the check with a logged warning).
- **Server-side metric downsampling (LTTB).** `?downsample=N` on `get-history` returns at most N points selected by Largest-Triangle-Three-Buckets, preserving visual peaks. Response includes `downsampled_from`. UI auto-uses it for charts.
- **Prometheus `/metrics` endpoint.** OpenMetrics-format exposition without `client_golang` dep. 12 metric families including request counters, latency histograms, runs/metrics created counters, active sessions gauge, DB size gauge, and standard process metrics. Public path — Prometheus scrapes without credentials.

### Fixed (independent review pass)

- OIDC nonce comparison now uses `subtle.ConstantTimeCompare` (was plain `!=`).
- `litemlflow import-mlflow` checkpoint race resolved by adding per-run DB lookup before insert (idempotent re-runs and concurrent imports both safe).
- Python SDK editable install: added `[tool.hatch.build.dev-mode-dirs]` so `pip install -e python/.` generates the required `.pth` file. Without this the SDK was importable only from `python/` directory.
- `.gitignore`: narrowed `litemlflow` (binary) pattern from global to `/litemlflow` so `python/litemlflow/...` is not silently ignored.

### Stats

- Go LOC (non-test): 11,129 (+1,073 vs v0.2)
- Go LOC (test): 5,595 (+1,217 vs v0.2)
- Python LOC: 1,680 (+1,064 vs v0.2 — LangChain handler + tests)
- Markdown docs: 2,129 (+267 vs v0.2)
- 22 files changed, +4,039 insertions, 9 commits since v0.2.0-rc1
- 31/31 MLflow compat checks pass against live `https://lmf.gorev.space`
- 23/23 Python SDK + LangChain tests pass
- 18 Go test files all green with `-race`

### Deferred to v0.4 (Q4)

- Helm chart, k8s operator, Terraform provider
- Multipart S3 upload (currently single PUT only)
- Astro/Starlight docs site
- Full benchmark vs MLflow (harness exists; not run in this session)
- LlamaIndex / OpenAI direct-client auto-instrumentation
- Prompt diff UI (side-by-side)
- gRPC OTLP receiver

## [v0.2.0-rc1] — 2026-05-08

The Q2 "Production hardening" release. See [docs/roadmap-y1.md](docs/roadmap-y1.md).

### Added

- **OIDC authentication + sessions.** Built-in PKCE flow with RS256 ID-token verification (no `oauth2` dep), JWKS caching, session cookies (HttpOnly + auto-Secure based on transport), `/api/v1/auth/{login,logout,oidc/start,oidc/callback,whoami}`. Migration `002_sessions.sql`. HTTPS is enforced for the issuer / token endpoint / JWKS URI (loopback HTTP is allowed for dev).
- **S3-compatible artifact backend.** `--artifact-backend s3` plus `--s3-{endpoint,bucket,region,access-key,secret-key,prefix}`. Pure-Go SigV4 signing (no `aws-sdk` dep), works against AWS S3 and MinIO. Bucket name is validated, key paths are properly URL-encoded, uploads are capped at 5 GiB by default to prevent unbounded memory use.
- **MLflow Model Registry.** Full surface: registered-models, model-versions, aliases, transitions, tags. Migration `003_registry.sql`. MLflow 3.x's automatic `tag.\`mlflow.prompt.is_prompt\` != 'true'` filter clause is stripped before parsing (we don't have the concept of MLflow "prompts" in our registry). Both POST and DELETE HTTP methods are accepted on delete endpoints.
- **Workspaces multi-tenancy.** New `workspaces` table, scope on experiments, `X-Workspace` header / cookie, member roles (`viewer`/`editor`/`admin` — stored, enforcement is v0.3). Default workspace `default` exists from migration so existing clients keep working unchanged. Migration `004_workspaces.sql`.
- **MLflow compat closures.** `log_inputs` (datasets, migration `005_datasets.sql`), `set_experiment` autocreate flow validated, `IN (...)` and `BETWEEN x AND y` filter operators, `?max_results=N&page_token=...` pagination on metric history, `view_type` query string on search-experiments.
- **Prometheus metrics endpoint.** Coming in this RC (perf engineer agent shipping).
- **Server-side metric downsampling.** Coming in this RC (LTTB algorithm; perf engineer agent shipping).
- **Year-1 roadmap doc** at `docs/roadmap-y1.md`.
- **Project governance** at `docs/governance.md`.
- **Reproducible benchmark harness** at `tests/integration/bench.py` (LiteMLflow vs MLflow).

### Compatibility coverage

The `tests/integration/mlflow_compat.py` suite went from 12 checks (v0.1) to 31 checks against the live MLflow Python client (v3.12), all green.

### Security fixes from independent review

- SigV4 URL encoding now properly handles keys with spaces/special chars.
- S3 bucket names are validated at construction.
- OIDC discovery enforces HTTPS for issuer/token-endpoint/JWKS URI (loopback HTTP allowed for dev).
- Session and OIDC-state cookies pick the `Secure` attribute based on `r.TLS` / `X-Forwarded-Proto` instead of always-insecure.
- Inbound `X-LiteMLflow-User` header is stripped before auth so it cannot be spoofed.
- S3 upload has a 5 GiB default cap even when caller passes `maxSize=0`.

### Known limitations carried into v0.3

- OIDC nonce validation is not implemented; PKCE state cookie + HTTPS-only token endpoint mitigate the relevant attacks for the v0.2 threat model. Deferred to v0.3.
- Per-workspace member-role enforcement (RBAC) — roles are stored, but every authenticated user can still read/write all workspaces. Enforcement lands in v0.3 alongside the OIDC group-claim mapper.
- LangChain / OpenAI auto-instrumentation Python helpers — deferred to v0.3.
- `litemlflow import-mlflow` migration tool — deferred to v0.3.

### Deferred to v0.3 (next quarter)

OIDC nonce, RBAC enforcement, OTLP gRPC, LangChain/LlamaIndex/OpenAI auto-instrumentation Python helpers, `litemlflow import-mlflow` migration command, multipart upload for S3 backend.

## [v0.1.1-deploy] — 2026-05-07

Live deployment release for lmf.gorev.space. The two changes were discovered only when running the real MLflow Python client against an HTTPS-fronted server (the local-only compat suite couldn't catch them because client and server share a filesystem).

### Fixed

- `artifact_uri` returned by `runs/create` and `runs/get` is now `mlflow-artifacts:/<run_id>` instead of a server-local filesystem path. The MLflow client recognizes the `mlflow-artifacts:` scheme and routes uploads/downloads through the server's HTTP API instead of attempting to write to a path that only exists on the server.
- New endpoint `GET /api/2.0/mlflow-artifacts/artifacts?path=<path>` returns the proxy-list shape `{"files": [...]}` that `MlflowArtifactsRepository.list_artifacts` calls.

## [v0.1.0] — 2026-05-07

Initial release. Single Go binary, MLflow REST API compatibility for ~80% of canonical client usage, native API for LLM traces / prompts / evals, embedded UI, basic auth, backup/restore. See [REPORT.md](REPORT.md).
