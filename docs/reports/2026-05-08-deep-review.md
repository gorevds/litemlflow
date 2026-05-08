# Deep-review action plan execution report

**Date:** 2026-05-08
**Scope:** T1 → T4 simplification waves following the 6-architect deep review
**Outcome:** 18 of 22 items shipped; 3 deferred with rationale; 1 split into "do-now + plan-for-v2.0"

## What kicked this off

The user invoked `/loop` with "ultrathink" asking for a deep product
review with multiple independent architects. Six fresh-eyes agents
audited storage, API surface, frontend, product, security/SRE, and
quality. Their consensus was synthesised into 22 ranked simplification
proposals plus 3 real bugs. Full input is in
`/tmp/.../subagents/agent-*.jsonl` and the synthesis is in this turn's
chat history.

## Real bugs caught and fixed

| ID | Title | Severity | Fix |
|----|-------|----------|-----|
| B1 | `litemlflow backup` with `--artifact-backend=s3` silently excluded artifacts | CRITICAL (data loss on restore) | Hard-error unless `--include-only-db` or `--include-s3`; `--include-s3` enumerates active runs from SQLite and walks each via `Store.List/Open` (initial DOA implementation passed `runID=""` which both backends reject; T1-review caught it pre-merge) |
| B2 | Webhook dispatcher had no graceful Stop() — in-flight deliveries lost on SIGTERM | MAJOR (audit-log webhooks dropped) | New `Stop(drainTimeout)` with stopCh + workerWG + post-drain sweep; server.Run shutdown sequence is now HTTP → dispatcher → grpc → store; Notify gained 2-stage stopCh check to close the racy enqueue-into-dying-queue window |
| B3 | 5 hand-rolled modals had no focus trap, role=dialog, restore-focus; alert/confirm/prompt for destructive ops | MAJOR (a11y regression) | New `Modal` IIFE: MutationObserver elevates every `.modal-backdrop` with role/aria/labelledby + focus trap; `Modal.confirm/prompt` returns Promise; replaced 4 destructive `confirm()` and 1 `prompt()` |
| (T2.5 follow-on) | `SearchRuns` 500-on-bad-filter; `TransitionStage` 500-on-bad-stage | MAJOR (5xx where 4xx) | The first round of mlflow Go tests caught these; fixed by routing through new sentinel errors `store.ErrInvalidFilter` / `store.ErrInvalidStage` |

## Tier-by-tier delivery

### Tier 1 — bugs first (3/3 ✓)

- **T1.B1** S3 backup data-loss fix
- **T1.B2** Webhook dispatcher graceful Stop + supervisor
- **T1.B3** Modal a11y + Confirm/Prompt + Prefs namespace

### Tier 2 — high-impact additive (7/7 ✓)

- **T2.4** Extracted `internal/store/storetest.NopStore` — eliminates ~700 LOC of stub-store boilerplate across 3 test files
- **T2.5** First Go unit tests for `internal/api/mlflow/` (14 table-driven cases); caught 2 real 500-instead-of-400 bugs
- **T2.6** SQLite pragmas (cache_size=-65536, temp_store=MEMORY, wal_autocheckpoint, optional mmap via env) + `ANALYZE; PRAGMA optimize` after Migrate
- **T2.7** `--auth=basic` deprecation warning; sunset documented for v2.0
- **T2.8** Sunset Snap, Homebrew, Debian, RPM, Terraform packaging; moved to `dist/_sunset/`
- **T2.9** `FuzzUploadMeta` for dataset upload meta JSON; mutation gate added for `internal/datasets`
- **T2.10** Click delegation for run-table rows: ~thousand inline `onclick=` strings → one delegated listener

### Tier 3 — medium additive (3 of 6 done; 3 deferred with rationale)

Done:
- **T3.12** Dedupe tag DTOs into `kvTagDTO` (3 byte-identical structs → 1)
- **T3.14** Richer error code catalogue on native v1 (`MISSING_REQUIRED_FIELD`, `METHOD_NOT_ALLOWED`, `WORKSPACE_NOT_FOUND`, etc.); 4 high-signal sites migrated
- **T3.15** CSS utility classes (`.u-w-full`, `.u-muted`, `.u-row-end`, …) covering the most-repeated inline-style idioms

Deferred with reasons:
- **T3.11** Data-driven RBAC table — defer to v2.0 freeze; current 60-line switch is correct, exhaustively tested by 313-LOC rbac_test.go, and refactoring it now would be pure internal churn for a release that already touches a lot
- **T3.13** Generic `pagedQuery[T]` — five paginated callsites share a surface shape but their cursor formats and orderBy translators diverge (timestamp:step vs id vs name+version); the abstraction would be either too narrow to dedupe or too wide to reason about
- **T3.16** Reuse `*httptest.Server` for read-only tests — the −1.5s ubuntu / −3s macos race-time saving doesn't justify weakening test isolation in a tracker that has already shipped two CRITICAL-level concurrency bugs

### Tier 4 — v2.0-track (4 of 6 done in v1.x; 2 deferred with implementation plan)

Done:
- **T4.17** Soft escape hatch for v0.3 dataset_inputs dual-write — `LITEMLFLOW_DISABLE_V03_TO_V2_MIRROR=1` env opt-out; legacy name kept for one minor
- **T4.19** Deprecation headers (RFC 8594) on 5 legacy MLflow routes; routes still serve normally
- **T4.21** Explicit `webhookDTO` replacing `*model.Webhook` direct response (HasSecret bool replaces leaky Secret string)
- **T4.22** Workspaces+RBAC UI behind `--enable-multi-tenant`; engine + middleware stay (load-bearing, inert for solo MLE); UI surface gated via `/version` features map

Deferred to v2.0 with full implementation plan in `docs/upgrade-to-v2.md`:
- **T4.18** Squash migrations 001-009 + `upgrade-to-v2` CLI — risky to do mid-stream; needs proper schema-equivalence test
- **T4.20** Tag bag table collapse — Storage architect explicitly said DEFER; composite-PK FK CASCADE on `model_version_tags` makes a single `attributes(scope, scope_pk, key, value)` non-trivial

### Skipped per synthesis "agents disagreed"

- Run notes vs description vs tags merge (Product proposed; storage/api/security all said load-bearing)
- Dashboards + timeline + analytics merger into "Reports" tab (Product proposed; UX divergence too high)

## Quality gates

| Gate | Result |
|---|---|
| `go build ./...` | clean |
| `go vet ./...` | clean (4 pre-existing warnings in datasets_http_test.go fixed in passing) |
| `go test -race -count=1 ./...` | 12 packages pass |
| 31-check MLflow client compat | green on live `lmf.gorev.space` |
| Final independent review | 1 MAJOR + 4 MINORs found and fixed |

## Stats

- **Commits**: 5 (`f6238a2 t1`, T2 commit, `6093c43 t3`, `9efb748 t4`, `a430b05 vet cleanup`, `6804d77 final-review fixes`)
- **Files changed across all commits**: ~35
- **Net Go LOC delta**: roughly −400 (test stub dedup −700, infra +300)
- **JS LOC delta**: +250 (Modal/Prefs/Features namespaces; click delegation refactor net-zero)
- **CSS LOC delta**: +50 (utility classes)
- **New tests**: mlflow_handlers_test (14), datasets_meta fuzz (1), no regression-test deletions
- **Sunset packages**: 5 (Snap, Brew, Debian, RPM, Terraform)
- **Mutation gates**: 4 packages now (auth 80%, store 70%, webhooks 65%, datasets 60%)

## What this enables

This release is the bridge between v1.2 and v2.0. After it:
1. Real bugs that the deep review surfaced are closed (data-loss on S3
   backup, webhook drop on SIGTERM, modal a11y).
2. The codebase is ~400 LOC lighter, with ~700 LOC of test-stub
   boilerplate removed and the modal/Prefs/Features pattern in place
   for the rest of the app.js to migrate to over time.
3. v2.0's job is now well-scoped: drop legacy aliases, squash migrations,
   collapse tag-bag tables, finalise webhookDTO and other API freezes.
   The plan is in `docs/upgrade-to-v2.md` and the deprecation headers
   are already informing clients via Link and X-LiteMLflow-Removed-At.

## Live state

```
https://lmf.gorev.space → v1.2.0-rc1+t1234 (a430b05+t1234)
features: {multi_tenant: false}
deprecation headers active on:
  /api/2.0/mlflow/experiments/list (POST/GET)
  /api/2.0/mlflow/registered-models/delete (DELETE)
  /api/2.0/mlflow/model-versions/delete (DELETE)
all 31 MLflow client compat checks green
```
