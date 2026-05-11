# Changelog

All notable changes to LiteMLflow are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/) starting at v1.0.

## [v2.0.0] — 2026-05-11

LTS stable. Promotes v2.0.0-rc1 with four pre-stable fixes from independent review:

### Fixed
- **C1**: Sunset header day-of-week was `Sat, 11 May 2027` but 2027-05-11 is a **Tuesday**. RFC 7231 strict parsers reject the mismatched IMF-fixdate or parse it as the following Saturday. Corrected to `Tue, 11 May 2027 00:00:00 GMT` and updated the assertion in `internal/server/v2_alias_test.go`.
- **H2**: CHANGELOG entries for v1.3 / v1.4 / v1.5 backfilled below.
- **H6**: `apiV2AliasMiddleware` now rewrites `EscapedPath()` (raw, still encoded) and re-decodes after, so percent-encoded segments in `/api/v2/...` paths route to the same prompt/run as `/api/v1/...`. Without this, `%2F` in a prompt name was silently decoded to a path separator. New test `TestV2AliasPreservesPercentEncoding`.
- **M13**: ADR table promised deprecation headers on `DELETE registered-models/delete` and `DELETE model-versions/delete` — verified that both routes (already wrapped with the `deprecated()` helper) emit the corrected Sunset. New test `TestMlflowDeprecatedRoutesEmitSunset` covers all four flagged routes.
- **M11**: ADR softened — `Store` interface lives in `internal/store`, which is not a Go public API. Removed the "embedders pin to major" claim; the LTS contract is HTTP-wire-only.
- **M12**: README and `docs/upgrade-to-v2.md` now state explicitly that federation has not had an external pen test and should be deployed in single-trust-domain environments only.

### Tests
`go test -count=1 -race ./...` — all 13 packages green. New: `TestV2AliasPreservesRouteVariables`, `TestV2AliasPreservesPercentEncoding`. Updated: `TestMlflowDeprecatedRoutesEmitSunset` (now covers 4 routes × Deprecation+Sunset+Link assertion).

## [v2.0.0-rc1] — 2026-05-11

End-of-Y2 release. Theme: **LTS** — the v1 wire contract is frozen for at least 12 months, the v2 namespace is now a stable alias, and the MLflow-compat sunset clock is set.

### Added

- **`/api/v2/...` namespace** as an alias to `/api/v1/...`. Mounted via a request-rewrite middleware so handlers don't fork. Responses through `/api/v2/...` carry `X-API-Version: 2`. See [ADR 0003 — v2.0 LTS charter](docs/adr/0003-v2-lts-charter.md).
- **Concrete `Sunset` date** (`Sat, 11 May 2027 00:00:00 GMT` per RFC 7231) on the deprecated MLflow-compat aliases (`/api/2.0/mlflow/experiments/list`). 12 months after the v2.0 GA target.

### Stability contract (v2.x → v3.0)

Frozen:
- HTTP wire shape of `/api/v1/...` and `/api/v2/...`. Additive changes only (new optional fields/params/endpoints).
- MLflow client compat at the 31/31 test-suite level.
- Migration ordering — strictly append.
- Single-binary, no-CGO distribution.

May change without a major bump:
- Internal Go package layout (`internal/...`).
- The `Store` interface (additive for the concrete store; embedders pin to major).
- UI layout, JS bundle structure, tracing/logging/metrics shapes.
- Default values of env-var configs (with a CHANGELOG note).

### Deferred to v2.1+

- T4.17: stop writing v0.3 `dataset_inputs` rows by default. The forward-fill mirror is already in v1.x; removing the v0.3 write entirely deserves a cross-version client test matrix before flipping the default.
- T4.18: migration squash (`001_v2_baseline.sql`).
- T4.20: tag-bag table consolidation (six tables → `attributes`).
- External pen test on the federation protocol.

### Tests

`go test -count=1 -race ./...` — all 13 packages green. New: `TestV2AliasResolvesToV1`, `TestMlflowDeprecatedRoutesEmitSunset`.

### Upgrade

Drop-in binary upgrade. Existing clients keep working unchanged. Pin to `/api/v2/...` in new code if you want the explicit LTS namespace stamp.

Full report: [docs/adr/0003-v2-lts-charter.md](docs/adr/0003-v2-lts-charter.md).

## [v1.5.0] — 2026-05-11

Y2 Q4 release (time-travel half — the lineage half shipped in v1.4). Theme: **read-side time-travel** via an append-only event log.

### Added
- Migration `013_events.sql`: `events(ts_ms, kind, entity_type, entity_id, payload)` with composite index for replay queries.
- `Store.GetRunAsOf` / `GetRunAsOfInWorkspace` / `GetLatestMetricsAsOf` — reconstruct a run's state at any unix-ms.
- All run mutations (`UpdateRun`, `SetRunLifecycle`, `SetTag`, `SetTags`, `DeleteTag`, `setParentRunID`, `syncParentRunIDFromTag`, `ArchiveStaleRuns`) mirror to the event log via `tryWriteRunEvent` (logs failures via `slog.Warn`).
- `GET /api/2.0/mlflow/runs/get?as_of=<ms>` and `metrics/get-history?as_of=<ms>` (free filter — metrics are append-only).
- `POST /api/2.0/mlflow/runs/search?as_of=<ms>` and `GET /api/v1/runs/{id}/data?as_of=<ms>` (added in v1.5.0 stable).
- UI date-picker on the run detail page (datetime-local input + snapshot banner).
- `LITEMLFLOW_EVENTS_RETENTION` env var + janitor sweep for the events table.
- Replay row cap `MaxEventsPerReplay = 50_000` (returns `ErrReplayLimitExceeded`).

### Pre-tag fixes from independent review
- **C1**: per-key metric reduction was done by filtering the already-collapsed `GetLatestMetrics` list — dropped keys whose latest postdated as_of. New `GetLatestMetricsAsOf` does the SQL reduction.
- **C2**: `setParentRunID` and `ArchiveStaleRuns` inserted tags via raw SQL bypassing the event log. Both now capture before-state and write events.
- **H3**: `GetRunAsOf` was workspace-blind — same exfiltration vector as v1.4 lineage. New `GetRunAsOfInWorkspace` gates the lookup.
- **H1**: `SetTags` bulk path wrote zero events.
- **M1/M3/M4**: future-as_of rejection, 50k replay cap, retention janitor.

Report: [docs/reports/2026-05-11-v1-5-stable.md](docs/reports/2026-05-11-v1-5-stable.md).

## [v1.4.0] — 2026-05-10

Y2 Q4 release (lineage half). Theme: **lineage DAG** with directional walks and dataset edges. Time-travel deferred to v1.5.

### Added
- `GET /api/v1/runs/{id}/lineage?direction=upstream|downstream|both&depth=N&fanout=N`. New fields in the response: `datasets[]` (run→dataset edges) and `truncated`.
- SVG layered DAG view at `#/experiments/{id}/runs/{id}/lineage` — ancestors top, current middle, descendants bottom, dataset chips below the current node. Click + keyboard navigation, depth/direction/fanout toolbar.
- Inline lineage card on the run detail page now includes dataset chips and a "View full DAG" link.

### Pre-tag fixes from independent review
- **C1 + C2**: workspace isolation on every walk and on dataset edges. `parent_run_id` is a user-settable tag, so without the JOIN on `experiments.workspace_id` an editor in ws-B could exfiltrate ws-A run names/timing/users via lineage queries. New `getRunInWorkspace` helper; dataset join filters by workspace_id too (the legacy `dataset_inputs ⨝ datasets_v2` would otherwise row-explode across workspaces with the same `(name, digest)` pair).

Report: [docs/reports/2026-05-10-v1-4-rc1.md](docs/reports/2026-05-10-v1-4-rc1.md).

## [v1.3.0] — 2026-05-10

Y2 Q3 release. Theme: **federated multi-server** — multiple LiteMLflow instances behind one UI.

### Added
- Mutual HMAC-SHA256 federation (canonical `method\npath\nts\nbody`), bounded TTL response cache (256×30s default).
- Migration `012_peers.sql`: peers table with name, URL, secret, workspace_id, status, last_seen, last_error.
- Native API: peer CRUD + echo probe + peer-callable `/federate/{echo,search}` (HMAC is the auth — exempt from session middleware).
- `GET /api/v1/search?federated=1` fan-out, origin-tagged hits, partial-failure surface.
- UI: `#/federation` page with add-peer modal (one-time secret reveal + clipboard copy); Cmd+K palette toggle with origin pills.
- `LITEMLFLOW_FEDERATION_NAME` + `LITEMLFLOW_ENABLE_MULTI_TENANT` env vars.

### Pre-tag fixes from independent review
- **C1**: RBAC bypass on peer CRUD — admin-only gate added for non-GET `/api/v1/federate/peers*`.
- **C2**: unbounded `io.ReadAll` on peer responses → 8 MiB cap via `io.LimitReader`.
- **H1**: cache TTL eviction could wipe fresh entries after re-Put — eager FIFO key removal.
- **H2**: auth-error shape leaked whether a peer name was registered — collapsed to opaque 401 body.

Report: [docs/reports/2026-05-08-v1-3-rc1.md](docs/reports/2026-05-08-v1-3-rc1.md).

## [v1.2.0-rc1] — 2026-05-08

Y2 Q2 release. Theme: **dataset versioning** — first-class versioned, content-addressed datasets with explicit lineage.

### Added

- **Content-addressed dataset store** (`internal/datasets`). Filesystem layout `<root>/<aa>/<bb…>` with 2-char hex shard prefix; atomic `.part`-rename writes; streaming SHA-256. Pure-Go, no CGO.
- **`datasets_v2` table** (migration 011) — per-(workspace, name, version) row with server-verified `content_hash`, `size_bytes`, optional `schema_json`/`description`, and `lifecycle_stage`. Per-name version sequence is auto-incremented inside a transaction so concurrent uploads of the same name never collide.
- **`dataset_lineage` edge table** for parent → child relationships. A child can have many parents; edges are validated at write time (parent must be in same workspace, no self-references, no cross-workspace leakage).
- **REST API**:
  - `POST /api/v1/datasets/{name}/versions` — multipart upload (`file` + `meta` JSON for description/schema/parents), 5 GiB cap.
  - `GET /api/v1/datasets` — latest active version per name.
  - `GET /api/v1/datasets/{name}` — all versions.
  - `GET /api/v1/datasets/{name}/versions/{v}` — version metadata + parent IDs.
  - `GET .../{v}/content` — stream the bytes (Content-Disposition + ETag = content-hash).
  - `GET .../{v}/lineage` — ancestors + descendants (BFS with visited set + 256-depth cap).
  - `DELETE .../{v}` — soft-delete (CAS bytes stay until offline GC).
- **UI Datasets page** (`#/datasets`, `#/datasets/{name}`) with version table, lineage view, upload modal with live progress (XMLHttpRequest progress events), and per-row Download / Delete actions.
- **MLflow compat shim**: `MlflowClient.log_input(...)` now also writes a row into `datasets_v2` inside the same transaction (idempotent on `(workspace, name, content_hash)`), so MLflow-driven dataset references show up on the new Datasets page automatically.
- **Body-limit middleware exemption** for `POST /api/v1/datasets/.../versions` and the existing MLflow artifact upload route via a new `isLargeUploadPath` allowlist. The dataset handler installs its own `MaxBytesReader` capped at 5 GiB.

### Performance / acceptance

- **Dedup acceptance met**: integration test `TestDatasetDedup` uploads the same bytes under two different names and confirms the CAS root contains exactly **1 physical file** while `datasets_v2` has **2 rows** referencing the same `content_hash`. The 5 GiB scenario in the roadmap is qualitatively the same — limited only by upload time.

### Backfill

- Migration 011 backfills `datasets_v2` from v0.3 `datasets` + `dataset_inputs`: each (name, digest) pair becomes one row; per-name version sequence is computed by `ROW_NUMBER() OVER (PARTITION BY name ORDER BY created_at, digest)`; workspace is inferred from the first run linked to the pair (falls back to `"default"`); `size_bytes=0` for legacy rows because v0.3 didn't track it.

### Cumulative test coverage

- 7 new unit tests on the CAS (dedup, race-safe rename, shard layout, hash validation rejects path-traversal attempts, nil-reader, missing-hash).
- 7 new HTTP integration tests on the dataset endpoints (upload+get, dedup, versioning, lineage, invalid-parent rejection, soft-delete, list-after-delete).
- All 12 Go packages green with `-race`.



## [v1.1.0-rc1] — 2026-05-08

First Y2 release. Theme: **analytics primitives** — cross-experiment OLAP queries without exporting to a notebook.

### Added

- **Analytics page + API.** `POST /api/v1/analytics/query` accepts a templated DSL (allowlisted aggregations, group-by dimensions, where clauses) and returns rows with `agg_value`, `run_count`, `best_run_id`. NO raw SQL is exposed to clients. UI at `#/analytics` provides a query builder, results table, bar chart for grouped queries, and saved queries (per-workspace `localStorage`).
- **`metrics_latest` materialised view** (migration 010) — one row per (run_id, metric_key), kept in sync via `AFTER INSERT` / `AFTER DELETE` triggers on the `metrics` table. Indexed by `(key, value DESC, run_id)` so "best metric across runs" is an index range scan.
- **Drag-and-drop dashboard widget reordering.** When a project dashboard is in edit mode, widgets are HTML5-draggable; the existing ↑/↓/✕ buttons stay as fallbacks for keyboard / accessibility.

### Performance

- **Headline benchmark met.** "Best `eval/f1` per `params.model`, last 30 days, status FINISHED" on a synthetic 100,000-run database completes in **~165 ms** wall-clock after `ANALYZE` (target was <200 ms). The execution path is one indexed GROUP BY scan + one indexed `LIMIT 1` lookup per result group.
- Two-step strategy chosen over a single window-function pass after benchmarking: window functions materialised the full filtered partition in temp B-tree memory, costing ~3x more wall time on the 100k-run dataset.

### Roadmap deviation

- **Dropped DuckDB attach.** The Y2 plan called for `ATTACH 'litemlflow.db' AS lmf (TYPE SQLITE)` via DuckDB. We dropped it because DuckDB's Go bindings require CGO, which would break the project's "single binary, no CGO" guarantee. Pure-SQLite with a triggered materialised view + composite index meets the 200 ms budget without the trade-off.

### Coverage

- **Mutation testing on `internal/webhooks` raised from 53% → 70% efficacy.** New `echo_test.go` covers the in-process echo ring buffer (capacity, eviction, body truncation, concurrent record/list); new `sync_test.go` covers `SyncDelivery` for both the lmf:// echo path and a real HTTP roundtrip with HMAC headers. CI threshold raised from 50 → 65.

### Docs

- New cookbook recipe **"Analytics — find the best run across thousands"** with API examples and the agg/group-by/where reference.
- `docs/roadmap-y2.md` updated to reflect the actual delivery (DuckDB → SQLite materialised view) with the rationale.



## [v1.0.0] — 2026-05-08

**Stable v1.0.** Closes the year-1 roadmap. Production-ready under the hero use cases (solo MLE / small team) documented in `docs/vision.md`.

### Added — security & supply chain hardening

- **Self-pen-test baseline:** govulncheck (stdlib + deps), gosec (with a CI-enforced accept-list of triaged false positives), and semgrep (security-audit + golang rulesets) now run on every push and weekly cron via `.github/workflows/security.yml`. Full audit doc at `docs/security-audit.md` lists every finding, fix, and accepted residual. The CI gate fails any PR that introduces a new HIGH gosec finding outside the documented accept-list.
- **Mutation-testing CI gate.** `.github/workflows/mutation.yml` runs gremlins nightly with thresholds derived from the v1.0 baseline:
  - `internal/auth` — **89% efficacy** (CI threshold 80%)
  - `internal/store` — **75% efficacy** (CI threshold 70%)
  - `internal/webhooks` — **53% efficacy** (CI threshold 50%)
- **Kind cluster CI smoke** at `.github/workflows/kind.yml`: builds server + operator images, loads them into a kind cluster, applies the CRD + RBAC + manager, creates a `LiteMLflow` CR, and confirms `/healthz` answers from the in-cluster pod. Closes the kind-cluster deferred item from rc1.

### Fixed — security audit pass

- **govulncheck — bumped Go toolchain to 1.26.3 (in both modules) and `golang.org/x/net` to v0.53.0**, eliminating GO-2026-{4982, 4980, 4971, 4918} (XSS in `html/template`, NUL panic in `net.Dialer`, infinite loop in HTTP/2 transport). Re-scan: 0 vulnerabilities.
- **Open-redirect via OIDC `return_to`** — new `safeReturnTo` rejects everything that isn't a single-leading-slash absolute path: blocks `//evil.com`, CRLF / NUL injection, length > 2 KiB. Covered by `TestSafeReturnTo`.
- **Decompression-bomb defense on `litemlflow restore`** — wraps the gzip reader in `io.LimitReader` capped at 200 GiB by default; override via `LITEMLFLOW_RESTORE_MAX_GIB`.
- **Goroutine-leak guard** on session-touch — the detached lifetime is intentional, but a 5-second timeout now caps it so a wedged DB cannot leak goroutines.
- **File modes tightened** — directories `0o755 → 0o750`, artifact files `0o644 → 0o640`, internal checkpoints `0o644 → 0o600`. Tar restore mask widened to drop world-writable + setuid + setgid.

### Stats — Y1 cumulative

- 4 quarterly themes delivered (foundation → hardening → ergonomics → distribution + polish)
- 8 release tags from v0.2 → v1.0.0 across the year
- ~24 K LOC Go (main + operator), ~3 K LOC Python, ~3.5 K LOC docs
- 11 Go packages × `-race` ✓; 35 Python tests ✓; 31 MLflow client compat ✓
- v1.0 self-audit: 4 govulncheck CVEs fixed, 6 gosec HIGH/MEDIUM fixes, 1 open-redirect fix; 29 false positives accepted with per-finding justification

### Live demo

[lmf.gorev.space](https://lmf.gorev.space) runs v1.0.0 with seeded demo data (3 projects with dashboards, 8 prompts, 1 echo webhook with sample deliveries).

## [v1.0.0-rc4] — 2026-05-08

Wave 8 polish and feature-completeness pass driven by direct user feedback against rc3.

### Added

- **Custom dashboards per project** — every project has a board at `#/dashboards/{project}` with four widget types: run-count tile, latest-best-run, run leaderboard (top N by metric), metric-trend chart (inline SVG). Edit mode lets you add/reorder/delete widgets; layout is persisted server-side per (workspace, project) via new `PUT /api/v1/dashboards/{project}` (migration 009).
- **Run timeline view** — Gantt-style alternative to the runs list on each experiment page. Toggle between **List** and **Timeline**; bars are coloured by run status and link to run details.
- **Webhook echo demo** — pseudo-scheme `lmf://echo` routes deliveries to an in-process ring buffer instead of HTTP, with a "Recent deliveries" panel on the Webhooks page. One-click "+ Try the demo" creates an echo webhook and fires a synthetic event so first-time users see the lifecycle without setting up an external receiver.
- **Explicit project UX** — toolbar `+ New project` button (with multi-experiment assignment), per-row **Move…** / **+ Project** buttons, and a project-chip filter row above the experiments list. `lmf.project` tag is unchanged on the wire; only the UI affordances are new.
- **Prompts list endpoint** — `GET /api/v1/prompts` returns latest version per name (previously the UI probed names from `localStorage`). New "+ New prompt" UI modal registers a prompt without leaving the page.
- **examples/quickstart.ipynb** — single Jupyter notebook end-to-end tour: experiments+projects, runs+notes, traces, prompts+aliases, lineage, model registry, search/compare, webhook echo, dashboards.

### Changed

- **Dark theme by default** — flips `data-theme="light"` → `"dark"` on the root element. Existing users with a stored theme preference keep their choice; new visitors see dark.
- **Repo cleanup** — `REPORT*.md` files moved to `docs/reports/` (kept for history, out of root). `bench-report.json` removed and added to `.gitignore`.

### Fixed

- (W7 review carryover) `validateWebhookURL` now allows `lmf://` prefix without DNS resolution, scoped to the in-process echo target.

## [v1.0.0-rc1] — 2026-05-08

The year-1 stabilization release. All Q1–Q4 roadmap streams delivered. See [docs/roadmap-y1.md](docs/roadmap-y1.md).

### Added

- **Multipart S3 upload** — large artifacts (default >100 MiB) are streamed in 100-MiB parts via S3's CreateMultipartUpload / UploadPart / CompleteMultipartUpload. Aborts cleanly on error. Threshold configurable via `--s3-multipart-threshold` / `LITEMLFLOW_S3_MULTIPART_THRESHOLD`.
- **gRPC OTLP receiver** — `--otlp-grpc-addr 127.0.0.1:4317` enables the OTel SDK's standard gRPC export path alongside the existing HTTP/JSON. Hardened with `MaxRecvMsgSize=64MiB` and `MaxConcurrentStreams=1024` belt-and-suspenders against DoS. Same `Store.InsertSpans` path as HTTP OTLP. ADR `docs/adr/0002-grpc-otlp-deps.md` justifies the new `grpc` + `otlp` deps.
- **Stability hardening** — Go native fuzz tests on the SQL filter parser (`FuzzParseRunPredicate`, `FuzzParseRunFilter`, `FuzzParseExperimentFilter`, `FuzzSplitOnAnd`), JWT validation (`FuzzVerifyIDToken`, `FuzzVerifyIDToken_SignatureCorruption`), OTLP ingest (`FuzzIngestOTLP`, `FuzzIngestTraces`). Chaos tests behind `chaos` build tag: `TestChaos_KillMidWrite` (kill DB mid-write), `TestChaos_FullDisk` (skipped without CAP_SYS_ADMIN), `TestChaos_CorruptWAL`, `TestChaos_MigrationCrashMidway`, `TestChaos_ConcurrentClose`. Mutation-testing scaffolding via `make mutation` (gremlins). New `make fuzz-short`, `make test-chaos`, `make mutation` targets. Threat model doc updated to reference the active fuzz coverage as a mitigation.
- **Kubernetes operator** at `operator/` (separate Go module `github.com/gorevds/litemlflow-operator`) — controller-runtime v0.18.4, CRD `litemlflow.dev/v1alpha1` `LiteMLflow`, reconciler manages StatefulSet+Service+optional basic-auth secret. 8 unit tests pass (envtest skip is documented when KUBEBUILDER_ASSETS is absent). Recommendation: extract to standalone `litemlflow-operator` repo eventually.
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
