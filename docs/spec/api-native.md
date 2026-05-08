# Native LiteMLflow API

The native API exposes capabilities that don't exist in MLflow: LLM traces, prompt versioning, evals, OpenTelemetry ingest. It is versioned independently of MLflow compat.

Base path: `/api/v1/`

All endpoints accept and return JSON. Auth (when configured) is enforced before any handler.

## Conventions

- Timestamps are unix milliseconds for run/metric/prompt-level timestamps.
- Trace span timestamps are unix nanoseconds (OpenTelemetry parity).
- Errors use the same shape as MLflow compat (`{error_code, message}`).
- All list endpoints support `?limit=N&page_token=...` and return `{items: [...], next_page_token: "..."}`.

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

```
POST /v1/traces                           # OTLP/JSON, OpenTelemetry-compatible
```

Accepts the OTLP `ExportTraceServiceRequest` shape. Spans are mapped to LiteMLflow's `traces` table by:

- `trace_id`, `span_id` → IDs (hex)
- `attributes` → `attributes_json`
- `events` → `events_json`
- `status` → status_code, status_message
- `litemlflow.run_id` resource attribute → `run_id` (optional)

A future v0.2 will add OTLP/gRPC.

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

**Security notes:** The PKCE `code_challenge` uses S256 (SHA-256). The anti-CSRF `state` is 24 bytes of `crypto/rand`. The `lmf_oidc_state` cookie is short-lived (10 minutes) and HttpOnly. Session IDs are 32 bytes of `crypto/rand` hex-encoded.
