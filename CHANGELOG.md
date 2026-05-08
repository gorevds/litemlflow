# Changelog

All notable changes to LiteMLflow are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/) starting at v1.0.

## [Unreleased]

### Planned for v0.2 (Q2 2026)

See [docs/roadmap-y1.md](docs/roadmap-y1.md) for the full plan.

- OIDC authentication + sessions
- S3-compatible artifact backend
- MLflow Model Registry
- Workspaces multi-tenancy
- `set_experiment` autocreate, `log_inputs`, edge-case compat closures

## [v0.1.1-deploy] — 2026-05-07

Live deployment release for lmf.gorev.space. The two changes were discovered only when running the real MLflow Python client against an HTTPS-fronted server (the local-only compat suite couldn't catch them because client and server share a filesystem).

### Fixed

- `artifact_uri` returned by `runs/create` and `runs/get` is now `mlflow-artifacts:/<run_id>` instead of a server-local filesystem path. The MLflow client recognizes the `mlflow-artifacts:` scheme and routes uploads/downloads through the server's HTTP API instead of attempting to write to a path that only exists on the server.
- New endpoint `GET /api/2.0/mlflow-artifacts/artifacts?path=<path>` returns the proxy-list shape `{"files": [...]}` that `MlflowArtifactsRepository.list_artifacts` calls.

## [v0.1.0] — 2026-05-07

Initial release. Single Go binary, MLflow REST API compatibility for ~80% of canonical client usage, native API for LLM traces / prompts / evals, embedded UI, basic auth, backup/restore. See [REPORT.md](REPORT.md).
