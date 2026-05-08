# Changelog

All notable changes to LiteMLflow are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/) starting at v1.0.

## [Unreleased] — v0.2 RC1

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
