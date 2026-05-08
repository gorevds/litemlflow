# LiteMLflow

[![Test Coverage](https://img.shields.io/badge/coverage-TBD-lightgrey?logo=go)](https://github.com/litemlflow/litemlflow/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/litemlflow/litemlflow)](https://goreportcard.com/report/github.com/litemlflow/litemlflow)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Latest Release](https://img.shields.io/github/v/release/litemlflow/litemlflow?include_prereleases)](https://github.com/litemlflow/litemlflow/releases)

> Your experiments, in one file. A single Go binary, MLflow-API-compatible, with first-class LLM trace support.

LiteMLflow is a lightweight, self-hosted experiment tracker for solo ML engineers and small teams who want real tracking infrastructure without standing up Postgres, S3, and a reverse proxy.

```bash
# 1. Build (pre-built binaries land with v0.1.0 release)
make build

# 2. Run it
./bin/litemlflow up --data ./data

# 3. Log from Python (your existing MLflow code works unchanged)
import mlflow
mlflow.set_tracking_uri("http://localhost:5000")
mlflow.log_metric("loss", 0.42)
```

That's it. No database to install. No object store to configure. No reverse proxy.

## Why LiteMLflow

| | LiteMLflow | MLflow | Aim | Langfuse |
|---|---|---|---|---|
| Install steps | 1 | 4+ | 2 | 5+ (docker-compose) |
| Runtime deps | none | Python, optional Postgres, optional S3 | Python | Postgres, ClickHouse |
| MLflow client compat | ✅ pragmatic 80% | ✅ native | ❌ | ❌ |
| First-class LLM traces | ✅ | bolt-on | ❌ | ✅ |
| Single-file backup | ✅ `cp` the dir | dump DB + sync S3 | partial | dump 2 DBs |
| Auth out-of-the-box | basic + (OIDC v0.2) | none | basic | basic |
| License | Apache 2.0, OSS | Apache 2.0 | Apache 2.0 | MIT |

## What works today (v0.1)

- **MLflow REST API**: experiments, runs, metrics, params, tags, artifacts (list/upload/download/delete), metric history, search with filters
- **LiteMLflow native API**: traces (manual + OTLP/JSON), prompts (versioned, content-addressed, aliases), evals
- **Embedded UI**: experiments → runs → run detail (metrics charts + trace waterfall)
- **CLI**: `up`, `migrate`, `rollback`, `backup`, `restore`, `version`
- **Auth**: anonymous, basic; OIDC scaffolded for v0.2
- **Tested against the real MLflow Python client** (3.x) — see [`tests/integration/mlflow_compat.py`](tests/integration/mlflow_compat.py)

## Documentation

- [docs/vision.md](docs/vision.md) — hero user, anti-features, success metrics
- [docs/architecture.md](docs/architecture.md) — system design, performance budgets
- [docs/quickstart.md](docs/quickstart.md) — install, run, first metric
- [docs/cookbook.md](docs/cookbook.md) — sklearn, PyTorch, LangChain, OTLP, deployment recipes
- [docs/spec/](docs/spec/) — data model, API specs, threat model

## Project structure

```
.
├── cmd/litemlflow/      # CLI entry point
├── internal/
│   ├── api/mlflow/      # MLflow REST API compat layer
│   ├── api/native/      # LiteMLflow native API (traces, prompts, evals, OTLP)
│   ├── artifact/        # filesystem artifact storage (plus interface for plugins)
│   ├── config/          # runtime configuration
│   ├── migrations/      # embedded SQL migrations + runner
│   ├── model/           # domain types
│   ├── server/          # HTTP server, middleware
│   └── store/           # SQLite store (the OLTP layer)
├── pkg/version/         # build-time version metadata
├── ui/static/           # embedded SPA (vanilla HTML/CSS/JS)
├── python/litemlflow/   # native Python SDK
├── docs/                # human-facing documentation
└── tests/integration/   # end-to-end tests (incl. real MLflow client)
```

## Build

Requires Go 1.22+ and Python 3.9+ (only for tests/SDK).

```bash
make build           # produces bin/litemlflow
make test            # Go + Python tests
make compat-test     # runs the real MLflow client against the binary
make lint            # static analysis
```

## Status

Pre-1.0. The MLflow compat surface is stable for the canonical 80% of usage. APIs marked "v0.2" in the spec are scaffolded but not wired (workspaces, OIDC, plugin host).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). DCO sign-off required (`git commit -s`); no CLA.

## License

Apache 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

LiteMLflow is independent of and not affiliated with Databricks, Inc. References to MLflow describe API compatibility only.
