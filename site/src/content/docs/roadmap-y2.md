---
title: Roadmap Year 2
---
# LiteMLflow — Year-2 roadmap

Date authored: 2026-05-08
Editorial scope: from v1.0.0-rc1 (deployed) through v2.0 at end of Y2.
Predecessor: [docs/roadmap-y1.md](roadmap-y1.md).

## State at start of Year 2

- v1.0.0-rc1 live on `https://lmf.gorev.space`.
- All four Y1 quarterly themes shipped: foundation, production hardening, scale & ergonomics, polish & distribution.
- 31/31 MLflow client compat, 35 Python tests, 24 Go test files, 47 commits, 4 tagged releases.
- Distribution: Docker, Homebrew, Debian, RPM, Snap, Helm, K8s operator, Terraform provider (Y1.W6).
- Performance benchmarks: 143× cold start, 15× log_metric, 3.1× log_batch vs MLflow + SQLite.
- Reports: [REPORT-Y1-FINAL.md](../REPORT-Y1-FINAL.md), [docs/bench-v04.md](bench-v04.md).

## Y1-stable carry-over (v1.0.0 final)

Before Q1 Y2 starts, v1.0.0-rc1 → v1.0.0 needs:

1. **External pen test** — independent firm, no P0/P1 findings open.
2. **Mutation-test baseline in CI** — gremlins on a CI runner, gate PRs at ≥70% on `internal/store/` and `internal/auth/`.
3. **Real-cluster operator validation** — kind cluster in CI, apply CRD + `LiteMLflow` CR, verify StatefulSet rolls out.
4. **Public push** — repo on GitHub, HN/Lobsters/MLOps Slack, conference CFP submitted.

These take ~2 weeks each but are sequential not parallel (security firm scheduling, CI infra changes, marketing windows). Expect v1.0.0 final ~Q1 of Y2.

## Year-2 quarterly themes

| Version | Quarter | Theme | Headline |
|---|---|---|---|
| v1.0.0 | Q1 Y2 | Stabilization → final | External pen test, mutation baseline, kind-cluster CI, public launch |
| v1.1 | Q1 Y2 | Analytics primitives | DuckDB attach for cross-experiment OLAP, materialized views, query API |
| v1.2 | Q2 Y2 | Dataset versioning | First-class datasets API (not just `log_inputs`) — content-addressed, tracked, queryable |
| v1.3 | Q3 Y2 | Federated multi-server | UI federation across multiple LiteMLflow instances; one pane of glass for distributed teams |
| v1.4 | Q4 Y2 | Time-travel + lineage graph | "Show me runs as of last Tuesday" + parent/child lineage between runs |
| v2.0 | end Q4 Y2 | LTS | API v2 freeze, deprecation notice for v1 endpoints, perf and security hardening pass |

## Quarterly plans

### Q1 Y2 — v1.1 "Analytics primitives"

**Goal:** answer questions like "across all 12,000 runs in this workspace, which combination of `optimizer + lr` gave the best `eval/f1` last quarter?" without exporting to a notebook.

| Stream | Deliverable | Owner |
|---|---|---|
| OLAP | DuckDB attached to the SQLite file via `ATTACH 'litemlflow.db' AS lmf (TYPE SQLITE)`. Read-only analytical queries. | Storage |
| API | `POST /api/v1/analytics/query` — accepts a parameterised SQL-ish DSL (whitelisted tables + columns) and returns rows. NOT raw SQL — it's templated. | API |
| UI | New "Analytics" page with a query builder (no SQL knowledge required) and saved queries. | Frontend |
| Materialized views | A `materialized_metrics_latest` table refreshed via SQL trigger or background job, indexed for hot queries. | Storage |
| Docs | Cookbook: "Find the best run across an experiment in one line". | Docs |

**Acceptance:** the analytics query "best `eval/f1` per `optimizer` last 30 days, grouped by `params.model`" runs in < 200 ms on a 100k-run dataset.

### Q2 Y2 — v1.2 "Dataset versioning"

**Goal:** datasets become first-class objects — content-addressed, tagged, queryable, reusable across runs without duplication.

| Stream | Deliverable | Owner |
|---|---|---|
| Schema | New `datasets_v2` table superseding the v0.3 inputs table. Each dataset has stable hash + version + lineage. | Storage |
| Storage | DVC-style chunk store (filesystem or S3) with content-addressed paths. Re-uploads are deduplicated. | Storage |
| API | `POST /api/v1/datasets`, `GET /api/v1/datasets/{name}@{version}`, `GET /api/v1/datasets/{name}/lineage`. | API |
| MLflow compat | `MlflowClient.log_input(...)` continues to work, internally writing to `datasets_v2`. | Compat |
| UI | "Datasets" tab with a name → version-tree visualisation. | Frontend |
| Migration | One-time backfill of v0.3 `dataset_inputs` rows into `datasets_v2`. | Migration |

**Acceptance:** a 5 GB dataset uploaded twice (different runs) ends up with a single physical copy + two pointer rows.

### Q3 Y2 — v1.3 "Federated multi-server"

**Goal:** an org running 5 LiteMLflow instances (one per team, one for prod, etc.) gets a single UI showing all their experiments without copying data.

| Stream | Deliverable | Owner |
|---|---|---|
| Federation protocol | `/api/v1/federate/` endpoints that one server uses to query another. Auth via mutual JWT. | Auth + API |
| UI | "Connected instances" panel; experiments can be tagged with their origin instance. Search + filter spans all of them. | Frontend |
| Discovery | A small "federation registry" — central server lists peer instances, each pulls peers as needed. | Platform |
| Cache | Federated query results cached server-side for 30s to avoid hammering peers. | Perf |

**Acceptance:** 3 LiteMLflow instances behind a single UI; searching "model = 'gpt-4o-mini'" returns experiments from all three.

### Q4 Y2 — v1.4 "Time-travel + lineage"

**Goal:** "show me what the project looked like on March 1st" and "trace a run's full ancestry".

| Stream | Deliverable | Owner |
|---|---|---|
| Time-travel | Append-only event log + snapshot table; queries can be qualified `?as_of=<timestamp>`. | Storage |
| Lineage | New `run_parents` table; `mlflow.log_input(run_id=parent_run.id)` records explicit parent. UI renders a DAG. | Storage + UI |
| API | `GET /api/v1/runs/{id}/lineage?direction=upstream|downstream`. | API |
| UI | Lineage view: graph of run → run + run → dataset relationships. Zoom, expand. | Frontend |

**Acceptance:** a run with 3 ancestors and 2 descendants renders correctly; clicking on a node opens the run page.

### v2.0 — end Q4 Y2

| Item | Target |
|---|---|
| API freeze | `/api/v2/...` paths defined; `/api/2.0/mlflow/...` continues to work via compat shim with deprecation header |
| Deprecation policy | v1 endpoints supported for 12 months after v2.0 |
| Migration | `litemlflow upgrade-to-v2` CLI subcommand for v1 → v2 schema migration |
| Security | Second external pen test on the federation protocol |
| Performance | Latency budgets re-validated at 1M-run scale |
| Distribution | All Y1 distribution channels still working |

## Stretch goals (likely Y3)

- **Built-in vector store** for embedding-based dataset retrieval.
- **Web-based admin console** for users / quotas / instance management.
- **Hosted SaaS** ("LiteMLflow Cloud") — revisit the year-1 deferred decision.
- **Mobile-friendly UI** — current UI is desktop-first.
- **GraphQL gateway** in front of the REST APIs.
- **Event-sourcing rewrite** of the storage layer.

## Y2 KPI targets

| KPI | end Q1 | end Q2 | end Q3 | end Q4 |
|---|---|---|---|---|
| GitHub stars | 5,000 | 10,000 | 20,000 | 30,000 |
| External contributors w/ merged PRs | 60 | 100 | 150 | 200 |
| Known production instances (opt-in telemetry) | 2,000 | 5,000 | 10,000 | 20,000 |
| MLflow client compat coverage | 98% | 98% | 99% | 99% |
| Cold-start p50 | <100 ms | <100 ms | <80 ms | <80 ms |
| 3rd-party integrations | 10 | 14 | 18 | 22 |

## Y2 risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Federation feature scope creep into "managed product" | Medium | Medium | Hard scope cuts; federation = read-only by default |
| DuckDB version churn | Low | Low | Pin DuckDB; vendor it if needed |
| Pen test reveals fundamental issue | Low | High | v0.4 SECURITY.md threat model is the contract; budget 4 weeks of fixes after pen test |
| Open-source maintainer burnout | Medium | High | Year-1 governance.md is the safety net; rotate release manager every quarter |

## How a session continues this plan

If you (or another Claude session) pick this up cold:

1. Open this file. Find sections marked `[in-progress]` — those are live.
2. `git status && make test` to confirm the baseline.
3. Check `CHANGELOG.md` for the most recent shipped version.
4. The next concrete starter task is in [REPORT-Y1-FINAL.md](../REPORT-Y1-FINAL.md) "Recommendations for v1.0 stable".
5. After v1.0.0 final, start the Q1 Y2 stream: DuckDB OLAP attach.
