# LiteMLflow

[![CI](https://img.shields.io/github/actions/workflow/status/gorevds/litemlflow/ci.yml?branch=main&logo=github&label=CI)](https://github.com/gorevds/litemlflow/actions)
[![Coverage](https://img.shields.io/badge/coverage-TBD-lightgrey?logo=go)](https://github.com/gorevds/litemlflow/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/gorevds/litemlflow)](https://goreportcard.com/report/github.com/gorevds/litemlflow)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/gorevds/litemlflow?include_prereleases)](https://github.com/gorevds/litemlflow/releases)
[![All Contributors](https://img.shields.io/badge/all_contributors-1-orange.svg)](#acknowledgements)

> **Your experiments, in one file.**
> A single Go binary. MLflow-API-compatible. First-class LLM trace support.
> Zero databases. Zero config. `litemlflow up` and you're tracking.

<!-- TODO: record demo.gif — 90 s, 1280×720, show: curl install → litemlflow up → mlflow.log_metric → UI <300ms → trace waterfall → /metrics -->
<!-- ![Demo](docs/demo.gif) -->

---

## Why LiteMLflow

- **143× faster cold start than MLflow** — 53 ms vs 7.5 s. No Python interpreter, no ORM, no migration dance in CI.
- **One env variable to migrate** — `MLFLOW_TRACKING_URI=http://localhost:5000`. Your existing MLflow code keeps running.
- **LLM-native from day one** — traces, prompts (versioned + aliased), evals, and OpenTelemetry OTLP live beside classic ML metrics in the same UI.

---

## Quick install

```bash
# Raw binary (Linux / macOS amd64+arm64) — recommended
curl -fsSL -o /usr/local/bin/litemlflow \
  https://github.com/gorevds/litemlflow/releases/latest/download/litemlflow-linux-amd64
chmod +x /usr/local/bin/litemlflow
litemlflow up --data ~/lmf

# Docker
docker run -p 5000:5000 -v $(pwd)/data:/data ghcr.io/gorevds/litemlflow:latest up

# Kubernetes (Helm)
helm install lmf oci://ghcr.io/litemlflow/charts/litemlflow --version 0.1.0

# Build from source (Go 1.26+)
git clone https://github.com/gorevds/litemlflow && cd litemlflow && make build
```

> Snap, Homebrew, Debian, RPM, and the Terraform provider were sunset in v1.2.
> See `dist/_sunset/README.md` for the rationale and supported channels.

---

## 30-second start

```bash
litemlflow up --data ./data          # server on :5000 in ~53 ms
```

```python
import mlflow
mlflow.set_tracking_uri("http://localhost:5000")
mlflow.set_experiment("my-project")

with mlflow.start_run():
    mlflow.log_param("lr", 0.01)
    mlflow.log_metric("loss", 0.42)
```

Open http://localhost:5000 — you're done. No Postgres. No S3. No reverse proxy.

---

## How it compares

| | **LiteMLflow** | MLflow | Weights & Biases | Aim | Langfuse |
|---|---|---|---|---|---|
| Cold start | **53 ms** | 7 500 ms | SaaS | ~2 s | SaaS / heavy |
| `log_metric` p50 | **1.4 ms** | 21.6 ms | ~network | ~3 ms | ~network |
| Runtime deps | **none** | Python + optional Postgres + optional S3 | account | Python | Postgres + ClickHouse |
| Install steps | **1** | 4+ | 3 (sign up) | 2 | 5+ (docker-compose) |
| MLflow client compat | **✅ 80%+** | ✅ native | ❌ | ❌ | ❌ |
| First-class LLM traces | **✅** | bolt-on | ✅ | ❌ | ✅ |
| Single-file backup | **✅ `cp dir`** | dump DB + sync S3 | export | partial | dump 2 DBs |
| Self-hostable | **✅** | ✅ | ❌ (OSS version limited) | ✅ | ✅ |
| Auth out-of-the-box | **basic + OIDC** | none | account | basic | basic |
| License | **Apache 2.0** | Apache 2.0 | proprietary | Apache 2.0 | MIT |

*Benchmark details: [docs/bench-v04.md](docs/bench-v04.md). All numbers measured on the same loopback machine.*

---

## What works today (v1.0)

- **MLflow REST API** — experiments, runs, metrics, params, tags, artifacts (upload/download/delete), metric history, search with full filter grammar (`=`/`!=`/`<`/`>`/`LIKE`/`IN`/`BETWEEN`), `log_batch`, `log_inputs`, pagination, Model Registry
- **Native API** — traces (manual + OTLP HTTP/gRPC), prompts (versioned, content-addressed, aliased), evals
- **Embedded UI** — experiments → runs → run detail; metrics charts, trace waterfall, prompt diff, bulk compare/export/tag; run notes (markdown), starring, inline rename, artifact preview; **side-by-side run comparison** (params diff, metrics overlay, tags, run summary); **custom column picker** (per-experiment, persisted); **share button** (copies current URL); keyboard shortcuts; **global search** (⌘K palette finds runs/experiments/prompts cross-experiment); dark/light theme; embed mode
- **Auth** — anonymous, basic, OIDC (PKCE + RS256 + JWKS), session cookies, RBAC (viewer/editor/admin)
- **Storage backends** — filesystem (default, zero config) and S3-compatible (MinIO/AWS, pure-Go SigV4, multipart)
- **Observability** — Prometheus `/metrics` endpoint (12 metric families), server-side LTTB downsampling
- **CLI** — `up`, `migrate`, `rollback`, `backup`, `restore`, `import-mlflow`, `version`
- **Distribution** — Docker image, Helm chart, Kubernetes operator, raw binary (Snap / Homebrew / Debian / RPM / Terraform sunset in v1.2 — see `dist/_sunset/`)
- **Python SDK** — `litemlflow[langchain]` (auto-instrument LangChain), `litemlflow[llamaindex]` (auto-instrument LlamaIndex)
- **Tested against real MLflow Python client 3.x** — 31/31 compat checks pass live at https://lmf.gorev.space

---

## Migrate from MLflow

```python
# Before
mlflow.set_tracking_uri("http://mlflow-server:5000")

# After
mlflow.set_tracking_uri("http://litemlflow-server:5000")
# Everything else stays the same.
```

To import existing data from a running MLflow instance:

```bash
litemlflow import-mlflow \
  --src http://mlflow-server:5000 \
  --data ./data
```

---

## Project layout

```
cmd/litemlflow/      CLI entry point
internal/
  api/mlflow/        MLflow REST compat layer
  api/native/        LiteMLflow native API (traces, prompts, evals, OTLP)
  artifact/          Filesystem + S3 artifact storage
  store/             SQLite store (WAL mode, LTTB downsampling)
  server/            HTTP/2 server, middleware, auth, RBAC, metrics
  migrations/        Embedded SQL migrations + runner
python/litemlflow/   Python SDK + LangChain + LlamaIndex handlers
operator/            Kubernetes operator (separate Go module)
dist/                Helm chart (active); _sunset/ holds Snap/Brew/RPM/Deb/Terraform (unmaintained, see README inside)
docs/                Human-facing documentation
tests/integration/   End-to-end tests (real MLflow client + bench harness)
```

---

## Build & test

```bash
make build          # → bin/litemlflow (requires Go 1.22+)
make test           # Go + Python tests
make compat-test    # real MLflow Python client against local binary
make lint           # static analysis
make fuzz-short     # fuzz targets (parsers, JWT, OTLP)
```

---

## Documentation

| | |
|---|---|
| [Quickstart](docs/quickstart.md) | Install, run, first metric |
| [Quickstart notebook](examples/quickstart.ipynb) | One Jupyter tour of every feature end-to-end |
| [Cookbook](docs/cookbook.md) | sklearn, PyTorch, LangChain, LlamaIndex, OTLP, deployment |
| [Architecture](docs/architecture.md) | System design, concurrency model, perf budgets |
| [Benchmarks](docs/bench-v04.md) | 143× cold start and honest trade-offs |
| [Roadmap Y1](docs/roadmap-y1.md) | Delivered: foundation → hardening → ergonomics → distribution |
| [Roadmap Y2](docs/roadmap-y2.md) | Analytics (DuckDB), dataset versioning, federation, time-travel |
| [Vision](docs/vision.md) | Hero user, anti-personas, why this will work |
| [Governance](docs/governance.md) | Maintainership, release cadence, DCO policy |

---

## Star history

[![Star History Chart](https://api.star-history.com/svg?repos=gorevds/litemlflow&type=Date)](https://star-history.com/#gorevds/litemlflow&Date)

---

## FAQ

<details>
<summary><strong>vs MLflow — why not just run MLflow?</strong></summary>

MLflow is great but operationally expensive: Python environment, SQLAlchemy, optional Postgres, optional S3, optional Nginx. LiteMLflow replaces all of that with a single binary. If you have an existing MLflow setup with a team of 50, stick with it — LiteMLflow targets solo engineers and small teams who want real tracking without DevOps.

</details>

<details>
<summary><strong>vs Weights & Biases — what's the difference?</strong></summary>

W&B is a closed SaaS product. LiteMLflow is Apache 2.0, self-hosted, and your data stays in a directory you own. W&B wins on collaborative features and polish at large scale. LiteMLflow wins on privacy, cost, and zero operational complexity.

</details>

<details>
<summary><strong>vs Langfuse — what about LLM observability?</strong></summary>

Langfuse is excellent for LLM-only workflows. LiteMLflow unifies classic ML experiment tracking (the MLflow compat surface) with LLM traces and prompts in one binary. If you run both traditional training jobs and LLM pipelines, you no longer need two tools.

</details>

<details>
<summary><strong>vs Aim — what's different?</strong></summary>

Aim has no MLflow compatibility, no built-in auth, and is Python-based (heavier startup). LiteMLflow adds MLflow compat, OIDC, RBAC, LLM traces, a pure-Go storage layer (143× faster cold start), and distribution as a single static binary.

</details>

---

## Sponsor

LiteMLflow is free and Apache 2.0. If it saves you time, consider sponsoring development:

- [GitHub Sponsors](https://github.com/sponsors/dolonet) *(TODO: activate before launch)*
- [Polar.sh](https://polar.sh/litemlflow) *(TODO: activate before launch)*

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). DCO sign-off required (`git commit -s`); no CLA — your copyright stays yours.

## Acknowledgements

Apache 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

[![All Contributors](https://img.shields.io/badge/all_contributors-1-orange.svg)](https://github.com/gorevds/litemlflow/graphs/contributors)

LiteMLflow is independent of and not affiliated with Databricks, Inc. References to "MLflow" describe API compatibility only.
