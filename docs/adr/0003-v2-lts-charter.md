# ADR 0003 — v2.0 LTS charter

Date: 2026-05-11
Status: Accepted (v2.0.0-rc1)
Supersedes: nothing.
Related: [docs/roadmap-y2.md](../roadmap-y2.md) v2.0 row, [docs/upgrade-to-v2.md](../upgrade-to-v2.md).

## Context

We've shipped through v1.5 in Y2:
- v1.0 stable (Year-1 close): foundation, hardening, scale, distribution.
- v1.1 analytics primitives, v1.2 dataset versioning, v1.3 federation, v1.4 lineage DAG, v1.5 time-travel.

The Y2 roadmap calls for a v2.0 release at end of Q4 that promotes the
existing surface to LTS — not a radical rewrite. The questions to settle:

1. What is the **stability contract** for v2.0? What can change in v2.x?
2. Which **legacy surfaces** get removed at v2.0 GA vs deferred?
3. How long do **v1 endpoints** keep working after v2.0 ships?
4. What's the **wire-level namespace** going forward (`/api/v1/...` vs `/api/v2/...`)?

## Decision

### 1. Stability contract

Within v2.x (until v3.0 ships), the following are **frozen**:

- HTTP **wire shape** of `/api/v1/...` and `/api/v2/...` endpoints — request
  schemas, response field names, sentinel error codes (`INVALID_PARAMETER_VALUE`,
  `RESOURCE_DOES_NOT_EXIST`, etc.). Additive changes (new optional fields, new
  optional query params, new endpoints) are allowed.
- **MLflow client compat** at the 31/31 test-suite level. Breaking changes here
  require either bumping to v3.0 or a feature flag with a deprecation cycle.
- **Migration ordering** — new migrations strictly append; v2.0 schema is the
  baseline for any new install.
- **Single-binary, no-CGO** distribution promise.

Within v2.x we explicitly **may** change:

- Internal Go package layout (`internal/...`). Per Go's import path rules,
  packages under `internal/...` are NOT a public API — third-party code
  cannot import them. The LTS contract is HTTP-wire-only; there is no
  Go-embedder contract because the import path doesn't grant one.
- The `Store` interface — adding methods is routine.
- UI layout and JS bundle structure.
- Tracing, logging, metrics shapes.
- Default values of env-var configs (with a CHANGELOG note).

### 2. Legacy surfaces at v2.0 GA

| Surface | Status at v2.0 GA | Sunset |
|---|---|---|
| `POST /api/2.0/mlflow/experiments/list` | Deprecation header live | 2027-05-11 |
| `GET /api/2.0/mlflow/experiments/list` | Deprecation header live | 2027-05-11 |
| `LITEMLFLOW_DISABLE_DATASETS_V03_MIRROR` (deprecated alias) | Honored | 2027-05-11 |
| `dataset_inputs` (v0.3 dual-write) | Still written (T4.17 deferred) | v2.1 |
| `tag_bag` table consolidation (T4.20) | Deferred (separate migration) | v2.x or v3.0 |
| Migration squash (T4.18) | Deferred | v2.x |

The **Sunset** date `2027-05-11` is exactly 12 months after v2.0 GA target
(2026-05-11), matching the roadmap's "v1 endpoints supported for 12 months
after v2.0" commitment.

### 3. Wire-level namespace

Both `/api/v1/...` and `/api/v2/...` resolve to the **same** native handlers.
v2.0 ships v2 routes as **aliases** so that:

- Existing v1 clients (including LiteMLflow SDKs and external integrators)
  keep working unchanged.
- New clients can point at `/api/v2/...` to make their dependency on the
  LTS contract explicit.
- Internal handlers don't fork — both routes invoke the same Go function.

If a future v3.0 needs an incompatible change, it gets its own `/api/v3/...`
namespace and v1+v2 enter their own sunset window.

This is **not** a path migration — there is no plan to break v1. v1 paths
are the LTS surface; v2 paths are a sibling alias for clients that want
the explicit version stamp.

### 4. MLflow-compat surface

`/api/2.0/mlflow/...` is a **third-party compat namespace** (mirrors the
upstream MLflow REST API). It is not a LiteMLflow versioned namespace.
Endpoints we've added beyond the MLflow surface continue to live at
`/api/v1/...`; MLflow-compat-only endpoints (`/api/2.0/mlflow/...`) follow
the upstream MLflow contract.

## Consequences

### What ships in v2.0.0-rc1

- ADR 0003 (this doc) committed.
- `/api/v2/...` route aliases mounted for every endpoint currently at `/api/v1/...`.
- The MLflow-compat `deprecated()` wrapper now emits an RFC 7231 IMF-fixdate
  `Sunset` header (`Tue, 11 May 2027 00:00:00 GMT` — 2027-05-11 is a Tuesday).
- CHANGELOG.md gains a `[v2.0.0-rc1]` section that links here.

### Still deferred to v2.1 / v3.0

- T4.17: stop writing v0.3 `dataset_inputs` rows by default. The forward-fill
  mirror from v0.3 → datasets_v2 was the user-visible piece of this work
  (already done). The remaining piece is removing the v0.3 write entirely;
  it touches the `log_input` hot path and would benefit from a real
  cross-version client test matrix before flipping the default.
- T4.18: migration squash. Existing installs apply 001..013 cleanly today
  (verified by `internal/migrations/embed_test.go` on each release); the
  baseline-001 squash is a maintenance optimisation, not a correctness fix.
- T4.20: tag-bag table consolidation. Half a dozen tag tables collapse into
  one `attributes` table. Significant migration; ships in its own minor.
- External pen test on the federation protocol. Federation has independent-review
  defenses (RBAC, OOM cap, cache correctness, peer-name non-enumeration); a
  paid external test is the right next step before we promote LiteMLflow as
  "production federation" for unaudited multi-tenant deployments.

### Why this is "LTS"

Not because the codebase stops moving. Because the wire contract and
distribution promise stop moving. Users pinning to `litemlflow:2` get a
12-month minimum support window for the v1 endpoints, a sunsetted-but-
working compat shim for MLflow's legacy paths, and the same single-binary
no-CGO shape they've had since v1.0.
