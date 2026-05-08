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

## Workspace (multi-tenant scaffolding)

For v0.1, all data lives in a single workspace. The scaffolding is in place for v0.2:

| Endpoint | Method | Description |
|---|---|---|
| `/api/v1/workspaces` | GET | list (returns `[{id: "default"}]` in v0.1) |
| `/api/v1/workspaces` | POST | create (returns 501 in v0.1) |

## Auth introspection

| Endpoint | Method | Description |
|---|---|---|
| `/api/v1/auth/whoami` | GET | returns `{user, mode, scopes}` |
| `/api/v1/auth/login` | POST | basic auth login (returns session cookie) |
| `/api/v1/auth/logout` | POST | terminates session |
| `/api/v1/auth/oidc/start` | GET | OIDC redirect initiator |
| `/api/v1/auth/oidc/callback` | GET | OIDC callback |
