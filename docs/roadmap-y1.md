# LiteMLflow — Year-1 roadmap

Date authored: 2026-05-08
Editorial scope: from v0.1.1 (deployed at lmf.gorev.space) through v1.0.

This document is the canonical year-one plan. It is a living doc — when work lands, the corresponding sections move to the [CHANGELOG](../CHANGELOG.md) and the rows here become "[done]".

## Guiding principles

The 10 engineering principles in [CONTRIBUTING.md](../CONTRIBUTING.md) take precedence over any specific feature listed here. If a feature on this roadmap requires breaking one of those principles, the feature gets cut, not the principle.

## Release cadence

| Version | Quarter | Theme | Headline features |
|---|---|---|---|
| v0.1 | Q1 (done) | Foundation | MLflow compat 80%, native API, embedded UI, single-binary distro, Apache 2.0 |
| **v0.2** | **Q2** | **Production hardening** | **OIDC + sessions, S3 backend, model registry, workspaces, set_experiment autocreate, log_inputs** |
| v0.3 | Q3 | Scale & ergonomics | Server-side metric downsampling, Prometheus `/metrics`, gRPC OTLP, LangChain auto-instrumentation, `litemlflow import-mlflow` |
| v0.4 | Q4 | Polish & distribution | Helm chart, k8s operator, Terraform provider, Astro/Starlight docs site, performance benchmarks vs MLflow, prompt diff UI v2 |
| v1.0 | end of Q4 | Stability | API freeze, no new features, polish, public launch |

The cadence is intentionally one minor every quarter. New features land throughout the quarter; the last 3 weeks are RC + freeze + release.

## Team & responsibilities (year-1 scope)

The project starts as a small team simulated by specialized agent roles. Real-world equivalents and full-time-equivalent estimates are in parentheses.

| Role | Headcount | Charter |
|---|---|---|
| **Tech lead / orchestrator** | 1 (you) | Roadmap, integration, code review, decisions, release management |
| **Auth engineer** | 1 (BE) | OIDC, sessions, basic auth hardening, mTLS, audit log |
| **Storage engineer** | 1 (BE) | S3/GCS/B2 artifact backends, plugin host, DuckDB analytics |
| **MLflow compat engineer** | 1 (BE) | Model registry, datasets/log_inputs, autologging hooks, edge endpoints |
| **Tenancy engineer** | 1 (BE) | Workspaces, RBAC, multi-tenant isolation, audit trail |
| **Performance engineer** | 1 (BE) | Server-side downsampling, query optimization, benchmarks vs MLflow |
| **Observability engineer** | 1 (BE) | Prometheus `/metrics`, self-tracing, healthcheck v2, structured query logs |
| **LLM/integrations engineer** | 1 (SDK) | LangChain/LlamaIndex/OpenAI auto-instrumentation, prompt features, eval matrix |
| **Frontend engineer** | 1 (FE) | UI features (downsampling-aware charts, prompt diff, eval matrix, search bar) |
| **Migration / DX engineer** | 0.5 | `import-mlflow` CLI, Wandb importer (v0.4), CLI ergonomics |
| **DevOps / release engineer** | 0.5 | brew/apt/snap/helm/operator, CI/CD, signed releases, SBOM, supply chain |
| **Tech writer** | 0.5 | Docs site, cookbook, recipes, video tutorials, governance docs |
| **QA / security engineer** | 0.5 | Fuzz, mutation tests, chaos, integration test matrix, threat model updates |
| **Community / DX lead** | 0.25 | Issue triage, contributor onboarding, RFC process, CoC enforcement |

Total ~9 FTE for the full year. Initially much smaller; the org grows toward this through the year as features and contributor base demand.

## Quarterly plans

### Q2 — v0.2 "Production hardening"

**Goal:** every solo MLE can deploy LiteMLflow, point a small team at it, expose to the open internet safely, and store artifacts at S3 scale.

| Stream | Deliverable | Owner | Acceptance |
|---|---|---|---|
| Auth | OIDC PKCE flow + session cookies | Auth | A user logs in via Auth0/Keycloak; session persists; logout works; CSRF safe |
| Auth | Sessions table + middleware | Auth | Existing basic auth keeps working; sessions expire on TTL; revocation works |
| Auth | Audit log v1 (append-only JSON Lines on disk) | Auth | Every state-mutating call recorded with user/req-id/run-id |
| Storage | S3-compatible artifact backend (filesystem stays default) | Storage | MinIO test in CI; large artifact streams without buffering |
| Storage | `--artifact-backend=fs|s3|...` config | Storage | Switching backend doesn't require schema migration |
| Compat | Model Registry: registered_models, model_versions, aliases, transitions | Compat | MLflow `MlflowClient.create_registered_model` etc. all green |
| Compat | `set_experiment` auto-create | Compat 2 | `mlflow.set_experiment("foo")` creates if missing |
| Compat | `log_inputs` (datasets) | Compat 2 | MLflow's dataset linkage roundtrips |
| Compat | Run-name conflict resolution + `set_experiment_tag` | Compat 2 | Existing edge gaps closed |
| Tenancy | Workspaces table + scope on experiments | Tenancy | Two workspaces, two users, isolation enforced |
| Tenancy | Workspace CRUD API + workspace selector in UI | Tenancy + FE | UI shows current workspace; switching works |
| Tenancy | Per-workspace ACL (role: viewer/editor/admin) | Tenancy | Viewer cannot mutate; editor can; admin can manage workspace |
| FE | UI shows registered models page | FE | Lists models, versions, aliases; clicking jumps to source run |
| FE | Charts handle downsampled data shape | FE | Chart shows downsample marker when active |
| Docs | OIDC setup recipe + S3 setup recipe + workspaces guide | Docs | Three new cookbook recipes |
| QA | Compat-test suite expanded (~25 checks) | QA | All canonical Python MLflow code paths covered |

### Q3 — v0.3 "Scale & ergonomics"

**Goal:** LiteMLflow is the obvious choice for someone running ML + LLM observability on a single host with 100k–1M runs.

| Stream | Deliverable | Owner | Acceptance |
|---|---|---|---|
| Perf | Server-side metric downsampling (LTTB or stride) | Perf | UI loads 1M-point metric in <300ms |
| Perf | Query plan inspection in dev mode | Perf | Slow queries logged with EXPLAIN |
| Perf | Index review for search_runs hot path | Perf | search_runs on 100k runs <50ms p95 |
| Obs | Prometheus `/metrics` endpoint | Obs | Standard Go runtime + domain metrics |
| Obs | Healthcheck v2 (returns store + artifact backend status) | Obs | k8s probes can rely on it |
| Obs | Server self-emits OTLP traces for own requests | Obs | Tracing of LiteMLflow under heavy load |
| LLM | gRPC OTLP receiver | LLM | OTel SDK works without HTTP shim |
| LLM | LangChain callback handler emits spans + metrics | LLM | RAG pipeline visible end-to-end in UI |
| LLM | LlamaIndex callback handler | LLM | Same, for LlamaIndex |
| LLM | OpenAI direct-client tracing helper | LLM | Wrap OpenAI client, get cost + token traces |
| LLM | Prompt diff UI (inline + side-by-side) | LLM + FE | Two prompt versions visualizable |
| LLM | Eval run matrix UI (sortable models × datasets) | LLM + FE | Compare 3+ runs at a glance |
| Migration | `litemlflow import-mlflow` CLI | Migration | Imports from running MLflow REST or SQL dump |
| Compat | Autologging adapters (sklearn / PyTorch / Keras) | Compat | One-line `litemlflow.autolog()` works for major frameworks |
| QA | Fuzz testing of OTLP receiver and JSON parsers | QA | OSS-Fuzz integration in CI |
| Docs | Update cookbook with downsampling, OTLP, autolog recipes | Docs | Cookbook >= 18 recipes |

### Q4 — v0.4 "Polish & distribution"

**Goal:** an enterprise-curious team can adopt LiteMLflow without a custom setup. A new user finds beautiful docs and a clear "why us" pitch.

| Stream | Deliverable | Owner | Acceptance |
|---|---|---|---|
| Distro | Homebrew tap (`brew install litemlflow/litemlflow/litemlflow`) | Release | Installs on macOS arm64 + amd64 |
| Distro | Debian + RPM packages with systemd unit | Release | apt/dnf install works on Ubuntu 22.04, Fedora 40 |
| Distro | Snap package | Release | snap install works |
| Distro | Helm chart | Release | `helm install lmf litemlflow/litemlflow` deploys to k3s |
| Distro | Kubernetes operator (CRD: `LiteMLflow`) | Release | Operator manages instance lifecycle |
| Distro | Terraform provider (resources: experiment, run, prompt) | Release | TF can spin up an instance and create initial experiments |
| Docs | Astro/Starlight docs site at https://docs.litemlflow.dev | Docs | Sub-1s page loads, search, dark mode |
| Docs | "Why LiteMLflow" landing page with benchmarks | Docs | Front-page benchmark table beats MLflow on cold-start, install time, memory |
| Perf | Reproducible benchmark suite (cold start, log_metric, log_batch, search_runs, UI) | Perf | CI runs nightly; results posted to docs |
| FE | UI v2 polish: keyboard shortcuts, command palette, bulk actions | FE | `?` shows shortcut help; `cmd-K` opens palette |
| FE | Embed mode (UI in iframe with JWT auth) | FE | Notebook integrations possible |
| Sec | External pen test (real money) | Sec | Independent firm; no P0 findings before v1.0 |
| Sec | SLSA L3 build provenance, sigstore signing | Release + Sec | Releases verifiable |

### v1.0 — Stabilization (end Q4)

| Item | Target |
|---|---|
| API freeze | All public APIs documented; semver promised from v1.0 |
| Breaking change blockers | None |
| Documentation | Complete, with at least 3 production-grade case studies |
| Performance | All v0.4 benchmark targets met or beaten |
| Security | External pen test, no P0 / P1 open |
| Distribution | Available via brew, apt, dnf, snap, docker, helm, terraform |
| Marketing | HN launch, Lobsters launch, MLOps Slack post, conf talk submitted |

## Stretch goals (likely deferred to year-2)

- **DuckDB OLAP attach over the SQLite file** for cross-experiment analytics
- **Web-based admin UI** for users/workspaces/quotas
- **Dataset versioning** (we lean on DVC/lakeFS today)
- **Built-in vector store** (we recommend external — Qdrant, PGVector)
- **Time-travel queries** ("show me runs as of last Tuesday")
- **Federated multi-server** (one UI across multiple LiteMLflow instances)

These have real demand but each is a major architectural commitment. Better to ship v1.0 stable, then revisit.

## Quarterly KPI targets

| KPI | v0.2 (Q2) | v0.3 (Q3) | v0.4 (Q4) | v1.0 |
|---|---|---|---|---|
| GitHub stars | 500 | 2,000 | 5,000 | 10,000 |
| External contributors w/ merged PRs | 5 | 15 | 30 | 60 |
| MLflow client compat coverage | 85% | 92% | 97% | 98% |
| Cold-start p50 (single binary on small VPS) | <250ms | <200ms | <150ms | <150ms |
| Public-facing instances (opt-in telemetry) | 50 | 200 | 800 | 2,000 |
| Critical security findings open | 0 | 0 | 0 | 0 |
| 3rd-party integrations (LangChain, LlamaIndex, etc.) | 1 | 4 | 7 | 10 |

## Risks & mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| MLflow trademark challenge | Low | High | NOTICE file disclaims affiliation; eventually rename if pushed |
| MLflow client API breaks compat | Medium | Medium | Pin tested versions in CI matrix; subscribe to releases |
| Solo-MLE adoption stalls because no SaaS | Medium | High | v0.4 brings k8s/helm; year-2 may revisit hosted offering |
| Single-binary perf cap (modernc.org/sqlite) hits a wall | Low | Medium | Profile + DuckDB attach for analytics; CGO build as last resort |
| Burnout / contributor churn | Medium | High | Documented governance, RFC process, async-first comms |
| LLM-tracing space gets dominated by Langfuse | High | Medium | Lean into the unified-graph differentiator; keep MLflow compat as moat |

## Public artifact cadence

- **Weekly devlog post** every Friday (community lead) starting Q2 — major sustained-attention amplifier
- **Monthly RFC summary** posted to GitHub Discussions
- **Quarterly release blog post** with benchmarks + 3 customer stories from beta users
- **Conference submissions**: PyCon US (Q3 CFP), KubeCon EU (Q4 CFP), MLOps World (Q4)

## How a session continues this plan

If you're an agent or human picking this up cold:

1. Open this file. Find sections marked `[in-progress]` or "[done]" — those are the only meaningful state.
2. Run `git status` and `make test` to confirm the working state.
3. Check `CHANGELOG.md` for the most recent shipped version.
4. The work breakdown is in TaskList (sessions inside Claude Code) or in GitHub Projects (sessions outside).
5. Pick an unstarted item, claim it (assign yourself), do it, PR.

The roadmap is a contract with the future. If circumstances change (e.g., a competitor releases something we need to react to, a contributor proposes a bigger refactor), update this doc *first*, then code.
