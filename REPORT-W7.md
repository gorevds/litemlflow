# Wave 7 — Tier 1-3 productivity sprint final report

**Date:** 2026-05-08
**Tag:** `v1.0.0-rc3`
**Live:** https://lmf.gorev.space
**Repo:** https://github.com/gorevds/litemlflow/releases/tag/v1.0.0-rc3

## Scope

User identified 16 user-facing features across three tiers as the biggest gaps after v1.0.0-rc2. This wave shipped **14 of 16** in one sprint via three parallel specialist agents working in worktree isolation. Two features (custom dashboards and timeline view) deferred to v1.1 — each is a substantial standalone product addition.

## Delivery breakdown

### Tier 1 — biggest solo-MLE pain (4/4 ✅)

| Feature | Owner | Files |
|---|---|---|
| **Run notes (markdown)** | W7.A | `internal/migrations/006_run_notes.sql`, `SetRunNote`/`GetRunNote` in store, `PUT/GET /api/v1/runs/{id}/note`, ~60-line tiny markdown renderer in `app.js` (whitelist: bold/italic/code/lists/headings/links — no CDN) |
| **Run starring** | W7.A | `lmf.starred` tag convention, ⭐/☆ button on run page, sort-starred-first toggle in experiment runs list |
| **Compare side-by-side** | W7.B | Real `renderCompare` route handling up to 6 runs in parallel; params table with diff-highlight + "show only differing" toggle; metrics overlay SVG charts (one line per run); tags + run-summary sections |
| **Param diff between 2 runs** | W7.B | `mode=diff` URL param shows `optimizer: adam → adamw` style; default for exactly 2 runs |

### Tier 2 — operational (5/6, custom dashboards deferred)

| Feature | Owner | Files |
|---|---|---|
| **Auto-archive stale runs** | W7.C | `internal/server/janitor.go` background ticker, `ArchiveStaleRuns` atomic UPDATE, configurable via `--run-stale-after` (default 24h) |
| **Webhooks + HMAC** | W7.C | `internal/webhooks/dispatcher.go` (1024 channel + 8 in-flight, exp backoff 1s/5s/25s), `internal/migrations/008_webhooks.sql`, REST CRUD + Test endpoint, UI page at `#/webhooks` |
| **Run lineage** | W7.C | `internal/migrations/007_run_lineage.sql` (ALTER runs ADD COLUMN parent_run_id), MLflow `mlflow.parentRunId` tag mirror, `GET /api/v1/runs/{id}/lineage`, lineage tree viz on run page |
| **Experiment templates** | W7.C | `POST /api/v1/experiments/{id}/clone` copies tags + project assignment; UI Clone button |
| **Global search** | W7.B | `GET /api/v1/search?q=...&kind=all` cross-experiment runs+experiments+prompts, capped at 10 results, workspace-scoped via X-Workspace header. cmd+K palette extended to query it |
| ~~Custom dashboards per project~~ | — | **deferred to v1.1** (drag-drop widget UI is a substantial separate workstream) |

### Tier 3 — polish (5/6, timeline view deferred)

| Feature | Owner | Files |
|---|---|---|
| **Custom columns picker** | W7.B | `ColumnPicker` module persists per-experiment column choice in `localStorage.litemlflow.columns.<expID>`; param.* and metric.* columns available in addition to defaults |
| **Run rename (UI)** | W7.A | ✏ button next to run name; click-to-edit input; Enter commits via `POST /runs/update`; Escape cancels |
| **Bulk tag editor** | W7.A | "Tags" button in BulkSelect bar; modal with Add/Update/Remove/Replace-project modes; iterates selected runs |
| **Permalink button** | W7.B | 🔗 Share button on experiment + run pages; `navigator.clipboard.writeText(location.href)` + toast |
| **Artifact preview** | W7.A | Inline `<details>`-gated lazy preview: PNG/JPG/SVG as `<img>`, JSON pretty-printed, CSV first 10 rows as `<table>`, txt/md/log/yaml in `<pre>`. All HTML-escaped |
| ~~Run timeline view~~ | — | **deferred to v1.1** (alternative-to-list visualization, separate workstream) |

## Stats

- **Go LOC added:** ~3,200 (W7.A ~600, W7.B ~800, W7.C ~1,800)
- **JS LOC added:** ~1,800 (multiple sections of `app.js`)
- **CSS LOC added:** ~280
- **Test files added:** 5 (`sqlite_notes_test.go`, `notes_http_test.go`, `search_http_test.go`, `webhooks_http_test.go`, `dispatcher_test.go`)
- **Migrations added:** 3 (`006_run_notes.sql`, `007_run_lineage.sql`, `008_webhooks.sql`) — strict numbering coordinated up-front in agent prompts
- **All 11 Go packages** test green with `-race`
- **35 Python tests** (+ 2 skipped LlamaIndex live ones)
- **31/31 MLflow client compat** still green on live HTTPS

## Independent review

A fresh-eyes reviewer pass found:

| Severity | Issue | Fix |
|---|---|---|
| **CRITICAL** | Webhook SSRF — operator-controlled URL had no validation, could probe AWS metadata / loopback / RFC1918 | Added `validateWebhookURL` blocking loopback/private/link-local; override via `LITEMLFLOW_WEBHOOK_ALLOW_PRIVATE=1` env var |
| **MAJOR** | Lineage walk had no cycle detection — malicious tag-set could create A→B→A and infinite-loop the request | Added visited set + 256-depth cap in `GetRunLineage` |
| MINOR | Webhook signature length-check is technically not constant-time | Acknowledged: HMAC-SHA256 hex length is public, downgraded to non-issue |
| MINOR | Artifact preview Range header server-side ignored | Documented; client truncates to 4 KB |
| MINOR | Experiment clone copies all tags incl. `lmf.starred` | Edge case noted in cookbook |

Both legitimate findings (CRITICAL + MAJOR) fixed before deploy; tests still pass.

## Integration challenges

The three agents all heavily edited `ui/static/app.js` (3-way conflict in 8+ blocks). All conflicts resolved manually preserving every feature:
- Run-table row generation merged: W7.B's ColumnPicker structure + W7.A's starring sort.
- Run-detail header merged: star/rename buttons (W7.A) alongside share button (W7.B/W7.C).
- Both wave Mount() routes co-exist.
- A leftover `006_reserved.sql` placeholder migration from W7.C's coordination shim was discovered post-merge and removed (would have collided with W7.A's real `006_run_notes.sql`).

## What this enables for users

1. **Solo MLE** can now: leave a markdown rationale on every run; star the 3 runs they actually want to keep; rename a run to "winner-v2" right after seeing the metric land; bulk-add `lmf.project=RAG` to 12 old runs that didn't have it.
2. **Team of 4** can now: receive Slack notifications when a colleague's run finishes via webhooks; auto-archive abandoned RUNNING runs from a colleague's crashed laptop; click cmd+K and find "the run with `loss` below 0.3 from last week" without remembering the experiment ID.
3. **Production use** can now: define run lineage for reproducibility audits; clone an experiment template to start a new sweep with consistent tags.

## Live state

```
https://lmf.gorev.space → v1.0.0-rc3 (bf20818, 2026-05-08T11:59:18Z)
schema version 8
demo data preserved (3 demo experiments, 12 runs, 8 prompts, 1 eval)
all 31 MLflow client compat checks green against live HTTPS
```

## Deferred to v1.1

1. **Custom dashboards per project** (Tier 2) — drag-drop metric widgets per project page; needs new `dashboards` table, widget-config JSON, dashboard-render JS module.
2. **Run timeline view** (Tier 3) — horizontal Gantt-like view of run durations; needs alternative-to-list view-mode toggle.

## Files of interest

- [`CHANGELOG.md`](CHANGELOG.md) — v1.0.0-rc3 entry
- [`docs/cookbook.md`](docs/cookbook.md) — recipes 12 ("Annotate runs"), 13 ("Compare runs and find runs across experiments"), 14 ("Migrate from MLflow"), webhook recipes
- [`docs/spec/api-native.md`](docs/spec/api-native.md) — note + search + webhooks + lineage endpoints
- [`internal/webhooks/`](internal/webhooks) — dispatcher, sync delivery, HMAC verification
- [`internal/server/janitor.go`](internal/server/janitor.go) — stale-run archiver
- [`internal/store/sqlite_lineage.go`](internal/store/sqlite_lineage.go) — lineage walk with cycle detection
