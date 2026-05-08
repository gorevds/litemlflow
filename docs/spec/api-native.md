# Native LiteMLflow API

The native API exposes capabilities that don't exist in MLflow: LLM traces, prompt versioning, evals, OpenTelemetry ingest. It is versioned independently of MLflow compat.

Base path: `/api/v1/`

All endpoints accept and return JSON. Auth (when configured) is enforced before any handler.

## Conventions

- Timestamps are unix milliseconds for run/metric/prompt-level timestamps.
- Trace span timestamps are unix nanoseconds (OpenTelemetry parity).
- Errors use the same shape as MLflow compat (`{error_code, message}`).
- All list endpoints support `?limit=N&page_token=...` and return `{items: [...], next_page_token: "..."}`.

## Special tag conventions

LiteMLflow reserves the `lmf.` tag-key namespace for internal metadata. These
tags are first-class in the UI but are plain MLflow tags under the hood — any
MLflow client can set or delete them via the standard `set-tag` / `delete-tag`
endpoints.

| Tag key | Values | Purpose |
|---|---|---|
| `lmf.project` | any string | Experiment-level grouping tag. Groups experiments into "projects" in the UI. See `GET /api/v1/projects`. |
| `lmf.starred` | `"true"` | Run-level starring. The UI shows ⭐ on starred runs and can sort them first. Toggle via `set-tag` / `delete-tag`. |
| `lmf.note` | reserved | Not used as a tag. Run notes are stored in the `run_notes` table (dedicated API below) to avoid polluting the tag namespace with multiline markdown blobs. |

## Run notes

Long-form markdown notes for a run. Stored outside the tag namespace to keep
the tag surface clean for k=v pairs. Multiple lines, code blocks, and basic
formatting are supported (rendered in the UI without external dependencies).

```
GET /api/v1/runs/{run_id}/note
```
Returns `{content, updated_at, updated_by}` or **404** if no note has been set.

```
PUT /api/v1/runs/{run_id}/note
{"content": "# My note\n\nWith **markdown**."}
```
Upserts the note. Returns the stored note (same shape as GET).

```
PUT /api/v1/runs/{run_id}/note
{"content": ""}
```
**Deletes** the note (empty content is the delete signal). Returns `{"deleted": true}`.

**Auth**: `PUT` requires `editor` role (same as all write operations). `GET` requires `viewer`.

## Health and meta

| Endpoint | Method | Description |
|---|---|---|
| `/healthz` | GET | liveness, returns `{ok: true}` |
| `/readyz` | GET | readiness (DB reachable), returns `{ok: true}` or 503 |
| `/version` | GET | `{version, commit, date}` |

## Traces

```
POST /api/v1/traces
{
  "trace_id": "...",
  "spans": [
    {
      "id": "...",
      "parent_id": null,
      "run_id": "...",
      "name": "rag.retrieve",
      "span_kind": "INTERNAL",
      "start_time_ns": 1234567890000000000,
      "end_time_ns": 1234567892000000000,
      "attributes": {"model": "gpt-4o-mini", "tokens.input": 120},
      "events": [],
      "status_code": "OK"
    }
  ]
}
```

A trace can be ingested with or without a `run_id`. If absent, LiteMLflow auto-creates a Run with `run_kind='trace'` in a default experiment named `__traces__`.

```
GET /api/v1/runs/{run_id}/traces
```
Returns the spans for the given run as a tree.

## Prompts

```
POST /api/v1/prompts
{
  "name": "rag.system",
  "content": "You are a helpful assistant...",
  "description": "system prompt for production RAG",
  "tags": {"owner": "alice"}
}
```

Returns the resolved version (auto-incremented) and `content_hash`. Identical content under the same name reuses the existing version.

```
GET /api/v1/prompts/{name}                # latest
GET /api/v1/prompts/{name}/versions       # list versions
GET /api/v1/prompts/{name}/versions/{n}   # specific version
GET /api/v1/prompts/{name}@{alias}        # alias resolution (e.g., @production)
POST /api/v1/prompts/{name}/aliases       # set alias to version
```

## Evals

```
POST /api/v1/evals
{
  "name": "rag-correctness-2026-05",
  "target_run_ids": ["a1b2...", "c3d4..."],
  "dataset_ref": "hf://allenai/squad",
  "score": 0.834,
  "metrics": {"em": 0.71, "f1": 0.834}
}
```

Creates a Run with `run_kind='eval'` and an entry in `evals` linking the targets.

```
GET /api/v1/evals/{run_id}                # full eval payload
GET /api/v1/runs/{run_id}/evals           # evals comparing this run to others
```

## OTLP ingest

LiteMLflow supports two OTLP transports:

### HTTP/JSON (default, always enabled)

```
POST /v1/traces
```

Accepts the OTLP `ExportTraceServiceRequest` shape serialised as JSON.
Spans are mapped to LiteMLflow's `traces` table by:

- `trace_id`, `span_id` → IDs (hex)
- `attributes` → `attributes_json`
- `events` → `events_json`
- `status` → status_code, status_message
- `litemlflow.run_id` resource attribute → `run_id` (optional)

### gRPC (opt-in, v1.0)

When `--otlp-grpc-addr` is set, LiteMLflow also starts a gRPC listener
implementing `opentelemetry.proto.collector.trace.v1.TraceService.Export`.

The field mapping is identical to the HTTP/JSON path. The response always
reports `partial_success.rejected_spans = 0` — malformed IDs are recorded
best-effort rather than rejected.

**Configuration:**

```bash
# Start with gRPC OTLP on the standard OTLP port:
litemlflow up --data ./data --otlp-grpc-addr 127.0.0.1:4317

# Or via environment variable:
export LITEMLFLOW_OTLP_GRPC_ADDR=127.0.0.1:4317
litemlflow up --data ./data
```

**No TLS on the gRPC listener:** The listener accepts plaintext gRPC. For
production, place a TLS-terminating sidecar (Envoy, Nginx `grpc_pass`, etc.)
in front. This mirrors how the HTTP listener is deployed.

**Dependency note:** The gRPC receiver adds `google.golang.org/grpc` and
`go.opentelemetry.io/proto/otlp` to the binary. See
`docs/adr/0002-grpc-otlp-deps.md` for the rationale.

## Global search

```
GET /api/v1/search?q=<query>&kind=all|runs|experiments|prompts[&names=p1,p2]
```

Cross-experiment, workspace-scoped search. Returns at most **10 items** total (4 experiments + 4 runs + 2 prompts). The workspace is resolved from the `X-Workspace` header exactly like every other workspace-scoped endpoint.

### Query parameters

| Param | Default | Description |
|---|---|---|
| `q` | `""` | Search term. Empty returns recents. |
| `kind` | `all` | Filter kind: `all`, `runs`, `experiments`, or `prompts`. |
| `names` | — | Comma-separated known prompt names (used for `kind=prompts` and `kind=all`). Client-side prompt registry lives in `localStorage.litemlflow.knownPrompts`. |

### Response

```json
{
  "query": "alpha",
  "items": [
    {
      "kind": "experiment",
      "id": "42",
      "name": "alpha-project",
      "url": "#/experiments/42"
    },
    {
      "kind": "run",
      "id": "a1b2c3d4...",
      "name": "training-run-alpha",
      "subtitle": "exp 42",
      "status": "FINISHED",
      "url": "#/experiments/42/runs/a1b2c3d4...",
      "experiment_id": "42"
    }
  ]
}
```

### Search behaviour by kind

- **`experiments`** — `name LIKE '%q%'` on active experiments in the current workspace. Max 4.
- **`runs`** — Runs whose `name LIKE '%q%'` OR `id LIKE 'q%'` in the current workspace. Cross-experiment (joins `experiments` for workspace filtering). Max 4.
- **`prompts`** — Probes names supplied in `?names=` for those whose name contains `q`. Max 2.

### Workspace scoping

Search is always workspace-scoped via the same `X-Workspace` header that every other endpoint uses. The UI's workspace selector (top-right dropdown) changes the `X-Workspace` value sent with all requests. If the header is absent, `"default"` is used.

## Workspaces (multi-tenancy)

Workspaces are the tenant boundary in LiteMLflow. Every experiment belongs to exactly one workspace. Clients select a workspace by sending the `X-Workspace` HTTP header (API/SDK) or the `lmf_workspace` cookie (UI). When neither is present, the request operates on the `default` workspace, which is seeded automatically on first startup and cannot be deleted.

### Workspace resolution (middleware)

Resolution order per request:

1. `X-Workspace` HTTP header — e.g. `X-Workspace: team-foo`
2. `lmf_workspace` cookie — set by the UI after workspace selection
3. Fallback: `"default"`

If the requested workspace id does not exist in the database, the middleware returns **400 INVALID_PARAMETER_VALUE** before any handler runs.

### Endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/workspaces` | List all workspaces |
| `POST` | `/api/v1/workspaces` | Create a workspace |
| `GET` | `/api/v1/workspaces/current` | Return the workspace active for this request + caller's role |
| `GET` | `/api/v1/workspaces/{id}` | Get a single workspace |
| `PATCH` | `/api/v1/workspaces/{id}` | Update name and/or description |
| `DELETE` | `/api/v1/workspaces/{id}` | Delete (refused for `default` or workspaces with experiments) |
| `GET` | `/api/v1/workspaces/{id}/members` | List workspace members |
| `PUT` | `/api/v1/workspaces/{id}/members/{user_id}` | Set or update a member's role (`viewer`\|`editor`\|`admin`) |
| `DELETE` | `/api/v1/workspaces/{id}/members/{user_id}` | Revoke membership |

### Request / response shapes

**POST /api/v1/workspaces**
```json
{ "id": "team-foo", "name": "Team Foo", "description": "optional" }
```
Returns the created workspace object with server-set timestamps. HTTP 201.

**PATCH /api/v1/workspaces/{id}**
```json
{ "name": "New Name", "description": "New description" }
```
Both fields are optional; omit to leave unchanged. Returns the updated workspace.

**PUT /api/v1/workspaces/{id}/members/{user_id}**
```json
{ "role": "editor" }
```
Valid roles: `viewer`, `editor`, `admin`. Upserts — calling again changes the role.

**GET /api/v1/workspaces/current**
```json
{
  "workspace": { "id": "team-foo", "name": "Team Foo", ... },
  "user": "alice",
  "role": "admin"
}
```
`role` is empty for anonymous users or users not explicitly listed as members.

### Workspace IDs

Workspace IDs are slugs: lowercase letters, digits, and hyphens; max 64 characters. They are immutable after creation. The `default` workspace is seeded by migration 005 and may not be deleted.

### Roles (RBAC)

Every workspace member has exactly one role. Roles are hierarchical: `admin > editor > viewer`.

| Action | viewer | editor | admin |
|---|:---:|:---:|:---:|
| Read experiments / runs / metrics in workspace | ✅ | ✅ | ✅ |
| Create / update experiments, runs, metrics | ❌ | ✅ | ✅ |
| Manage workspace settings (rename, delete) | ❌ | ❌ | ✅ |
| Add / remove members | ❌ | ❌ | ✅ |

Roles are enforced by `rbacMiddleware`, which runs after `workspaceMiddleware`. The workspace used for the RBAC check is the one resolved from `X-Workspace` / `lmf_workspace` / fallback, not the workspace ID embedded in the URL path.

#### Open mode (no-gate rule)

RBAC is **inactive** (pass-through) in two cases:

1. **`auth=none`** — single-user / anonymous mode. No roles are checked.
2. **Default workspace with zero configured members** — fresh-install open mode. This preserves backward compatibility for solo users and MLflow clients that have never configured workspace membership. As soon as the first member is added to `"default"`, the role gate activates.

The open-mode rule for non-default workspaces does **not** apply: any workspace that is not `"default"` requires explicit membership regardless of whether it has zero members.

#### Path classification

The middleware classifies routes into three permission tiers:

| Tier | Examples |
|---|---|
| `admin` | `POST /api/v1/workspaces`, `PATCH/DELETE /api/v1/workspaces/{id}`, `PUT/DELETE /api/v1/workspaces/{id}/members/...` |
| `editor` | `POST/PUT/DELETE /api/2.0/mlflow/...`, `POST /api/v1/traces`, `POST /api/v1/prompts`, `POST /api/v1/evals` |
| `viewer` | `GET /api/2.0/mlflow/...`, `GET /api/v1/...` (non-auth, non-health) |
| *(none)* | `/healthz`, `/readyz`, `/version`, `/metrics`, `/ui/...`, `/api/v1/auth/...` |

### MLflow compat layer

The MLflow API endpoints (`/api/2.0/mlflow/experiments/...`) are workspace-aware: `CreateExperiment`, `SearchExperiments`, and `GetExperimentByName` all operate within the workspace resolved from the request. Existing MLflow Python clients that send no `X-Workspace` header continue to use the `default` workspace unchanged.

## Auth introspection

All auth endpoints below are public (no authentication required to reach them,
though their behaviour varies by the server's `--auth` mode).

### Session cookie

After a successful login or OIDC callback the server sets a cookie named
`lmf_session` (HttpOnly, SameSite=Lax). All subsequent requests that present
this cookie are authenticated as the owning user regardless of the server's
`--auth` mode. Sessions expire after `--session-ttl` (default 7 days). The
`GarbageCollectSessions` background job removes expired rows.

### Endpoints

| Endpoint | Method | Description |
|---|---|---|
| `GET /api/v1/auth/whoami` | GET | Returns `{user, auth_method}`. `auth_method` is `"basic"`, `"oidc"`, `"none"`, or `"anonymous"`. |
| `POST /api/v1/auth/login` | POST | Basic-auth login. Body: `{"user":"…","pass":"…"}`. Returns `{ok, session_expires_at}` and sets `lmf_session` cookie. Returns 400 when `auth=oidc`. |
| `POST /api/v1/auth/logout` | POST | Deletes the server-side session row and clears the cookie. Always returns 200 (idempotent). |
| `GET /api/v1/auth/oidc/start` | GET | Generates PKCE verifier + anti-CSRF state, stashes them in `lmf_oidc_state` cookie, and 302-redirects to the IdP. Optional `?return_to=<path>` for post-login redirect. |
| `GET /api/v1/auth/oidc/callback` | GET | Validates CSRF state, exchanges code for ID token (RS256, verified against JWKS), mints a session, and redirects to `return_to` or `/ui/`. |

### OIDC configuration

Set `--auth=oidc` and provide:
- `--oidc-issuer` / `LITEMLFLOW_OIDC_ISSUER` — the IdP issuer URL (e.g. `https://accounts.google.com`). The discovery document is loaded from `<issuer>/.well-known/openid-configuration`.
- `--oidc-client-id` / `LITEMLFLOW_OIDC_CLIENT_ID` — required.
- `--oidc-client-secret` / `LITEMLFLOW_OIDC_CLIENT_SECRET` — optional for public clients (PKCE-only flows).
- `--oidc-redirect-url` / `LITEMLFLOW_OIDC_REDIRECT_URL` — must match the IdP callback allowlist.
- `--oidc-scopes` / `LITEMLFLOW_OIDC_SCOPES` — space-separated; defaults to `openid email profile`.
- `--session-ttl` / `LITEMLFLOW_SESSION_TTL` — e.g. `168h` (7 days, the default).

**JWT verification:** v1 supports RS256 only. The JWKS is fetched once at startup and cached; rotate keys by restarting the server. ES256 / EdDSA support is planned for v0.3.

**Security notes:** The PKCE `code_challenge` uses S256 (SHA-256). The anti-CSRF `state` is 24 bytes of `crypto/rand`. A 32-byte `nonce` is generated per login attempt, included in the auth URL, stashed in the state cookie, and validated against the `nonce` claim in the returned ID token — preventing token replay across sessions. The `lmf_oidc_state` cookie is short-lived (10 minutes) and HttpOnly. Session IDs are 32 bytes of `crypto/rand` hex-encoded.
