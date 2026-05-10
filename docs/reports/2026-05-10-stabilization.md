# v1.3.0 + v1.4.0 stabilization

Date: 2026-05-10

This is the stabilization pass that closes deferred items from v1.3.0-rc1
(federated multi-server) and v1.4.0-rc1 (lineage DAG) reviews and tags
both `v1.3.0` and `v1.4.0` stable.

## What this pass closed

### From v1.3.0-rc1 review

| ID | Severity | Issue | Status |
|----|----------|-------|--------|
| H3 | high     | Outbound client returned `(resp, nil, err)` on body-read fail | already fixed in rc-wave; verified return shape is `(nil, nil, err)`. |
| M2 | medium   | `federatedFanOut` did not propagate parent ctx to peer requests | **fixed** — call sites now use `client.DoCtx(ctx, ...)` so cancelling the inbound search aborts in-flight peer fetches. |
| M6 | medium   | `FederateSearch` ignored `kind=prompts` | **fixed** — `runLocalSearch` adds prompt branch using `ListPrompts` + case-insensitive substring match. New test: `TestFederationSearchPrompts`. |

### From v1.4.0-rc1 review

| ID | Severity | Issue | Status |
|----|----------|-------|--------|
| M4 | high (deferred) | No test for legacy v0.3 dataset row in `runDatasetEdges` | **fixed** — `TestLineageRunDatasetEdgesLegacyV03` seeds the v0.3 `datasets` + `dataset_inputs` directly and verifies `Version=0/DatasetID=0` fall-back. |
| M7 | medium   | `DescendantDepth=0` contract diverged: store substituted 4, HTTP rejected | **documented** on `LineageOptions` godoc — kept the asymmetry intentional (HTTP gives 4xx feedback, internal Go callers get sane defaults). |
| M8 | medium   | UI truncated pill mentioned "fanout" but only depth knob existed | **fixed** — fanout knob added to lineage toolbar; tooltip rephrased. |
| L12| low      | `escAttr` only escaped `"` | **fixed** — folded into the project-wide `escapeHTML`. |
| L13| low      | Missing godoc on `DatasetEdge` fields | **fixed** — Version/DatasetID semantics documented on the struct. |

### Deferred to v1.5+ (not blockers)

- **v1.3 M3** cross-workspace federation (currently default-only) — needs a workspace-resolution design decision.
- **v1.3 M4** `(kind, id)` dedupe across peers — needs UX decision (which instance wins for an identical ID).
- **v1.4 L11** `placeholders` builder helper — purely aesthetic.

## Tests

`go test -count=1 -race ./...` — all 13 packages green. New tests:
- `TestFederationSearchPrompts` (v1.3 M6)
- `TestLineageRunDatasetEdgesLegacyV03` (v1.4 M4)

Plus the 10 lineage tests and 3 federation tests already in the rc tags.

## Files changed

- `internal/api/native/federation.go` — DoCtx propagation, prompts branch in runLocalSearch.
- `internal/api/native/webhooks.go` — godoc on the depth/fanout contract.
- `internal/store/store.go` — godoc on LineageOptions zero-handling and DatasetEdge sentinel-zero fields.
- `internal/store/sqlite_lineage_test.go` — legacy v0.3 row test.
- `internal/server/federation_http_test.go` — prompts federated search test.
- `ui/static/app.js` — fanout toolbar input + escAttr → escapeHTML.

## Tag plan

1. Tag `v1.3.0` from this commit (federated multi-server stable).
2. Tag `v1.4.0` from this commit (lineage DAG stable).

Both tags point at the same commit because the stabilization fixes touch
both feature surfaces and there's no version-skew risk in shipping them
together. Future stable tags will fork as features evolve independently.

## Deploy

Build linux/amd64, ship to https://lmf.gorev.space, smoke-check
`/version` reports `v1.4.0`, then verify lineage + federation endpoints
respond as expected.
