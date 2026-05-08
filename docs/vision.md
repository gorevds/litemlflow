# LiteMLflow vision

## Hero user

A solo ML engineer (or a 2–10 person team) who already knows MLflow, runs experiments daily, and is tired of the operational tax: setting up Postgres, configuring S3, hand-rolling nginx, dumping databases for backup. Add to that the same engineer is now also building LLM workflows — agents, RAG, evals — and wants observability that lives next to their classic ML metrics, not in a second tool.

## Anti-personas (we are explicitly not for them)

- Mid-market enterprise teams (10–100 ML engineers) — they need RBAC, audit, multi-server, fine-grained quotas. We will not optimize for them in v1.
- Vendors of model-marketplaces or model-serving platforms — we are not a model registry first, we are a tracker first.
- Pure data-engineering use cases — we are not Airflow, Dagster, or Prefect.

## What LiteMLflow is

A single Go binary that, when run, exposes:

1. **An MLflow-API-compatible HTTP server** (~80% of the canonical surface). Existing MLflow Python code keeps working after switching `tracking_uri`.
2. **A native LiteMLflow API** for everything MLflow doesn't model well: LLM traces, prompt versions, evals, OpenTelemetry ingest.
3. **An embedded modern UI** (no external SPA hosting required).
4. **Built-in TLS via Let's Encrypt and authentication** (none / basic / OIDC / mTLS).
5. **A storage layer** that is just files: SQLite for metadata, filesystem (or S3-compatible) for artifacts.

## What LiteMLflow is not

- Not a workflow orchestrator. We track experiments. Use Airflow/Prefect/Dagster for pipelines.
- Not a model-serving platform. We track artifacts but do not serve models. Pair with vLLM, Triton, BentoML, KServe.
- Not a feature store. Use Feast, Tecton.
- Not a data-versioning tool. Use DVC, lakeFS.
- Not a hosted SaaS. v1 is self-host-only. We may revisit in year 2.

## Hero features in v0.1

1. **`litemlflow up`** — single command, single binary, in 30 seconds you have a tracker.
2. **MLflow client compatibility** — `mlflow.set_tracking_uri()` and existing scripts log into LiteMLflow without code changes.
3. **Unified data model** — runs, traces, prompts, and metrics live in one graph. The UI shows them together.
4. **Built-in TLS + OIDC** — exposing safely to a small team needs no reverse proxy.
5. **`litemlflow backup`** — one command produces a tarball with the SQLite file and all artifacts. `litemlflow restore` puts it back.

## Non-goals for v0.1

- Distributed deployment (one server is fine for our hero user)
- RBAC (basic per-workspace ACL is enough)
- LDAP, SAML (use OIDC bridge)
- Web-based admin (CLI is enough)
- Custom Python alerts engine (use webhooks)
- Built-in vector DB (we point to your own — Qdrant/PGVector/etc.)

## Success metrics for v1.0

- A new user reads the README and is logging metrics within 5 minutes (measured: Day-1 to first-metric on telemetry, opt-in).
- An existing MLflow user migrates by changing one environment variable.
- 90-day retention of beta users >= 60%.
- 5,000 GitHub stars within 90 days of public launch.
- 30+ external contributors with merged PRs in the first 180 days.
- Zero P0 security findings during external pen test before v1.0.

## Why this will work

- The single-binary distribution model is proven (Ollama, DuckDB, Litestream, Caddy, Tailscale all show pent-up demand for "just one file" infrastructure).
- MLflow has 18k stars but is universally cursed for its operational complexity. Migration cost is the moat — we destroy that moat with the compat layer.
- LLM observability tools (Langfuse, Phoenix, Helicone) all bolt onto stacks; nobody unifies. Engineers using both classic ML and LLM are the fastest-growing segment of MLOps.
- Apache 2.0 + DCO removes the largest adoption blockers for corporate users (W&B is not OSS; Aim has lukewarm enterprise adoption due to no auth).

## Why this might not work

- **MLflow API is large and growing.** 80% compat is a moving target. Mitigation: clear "what's covered" matrix in docs; native API as escape hatch.
- **Aim, ClearML, and W&B are entrenched.** We need a clear "10x" wedge. Single-binary install + LLM-native is that wedge, but only if we communicate it well.
- **Solo MLEs are price-insensitive but switch-cost-sensitive.** Migration must be one env variable. If our first onboarding has any friction, we lose them.
- **We are dependent on the MLflow Python client's stability.** A breaking client release could partially break us. Mitigation: pin tested client versions in CI; subscribe to MLflow release notes.
