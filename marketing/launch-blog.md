# Introducing LiteMLflow: 143× faster MLflow-compatible experiment tracking in a single binary

*Published: May 2026 — litemlflow.dev/blog/introducing-litemlflow*

---

MLflow is the de-facto standard for ML experiment tracking. It has 18,000 GitHub stars and is used in organizations from two-person startups to Fortune 500 data science teams. It is also, by universal agreement, a pain to run.

This is the story of how I built a replacement in Go — MLflow-API-compatible, single binary, zero runtime dependencies — and why the performance numbers came out 143× better on cold start.

---

## The pain: what MLflow setup actually looks like

The MLflow documentation presents a cheerful one-liner:

```bash
pip install mlflow && mlflow ui
```

What the documentation does not tell you is what happens the moment you need your tracking to persist across reboots, be accessible to your team, or handle more than a few thousand runs reliably.

You need Postgres. You need to configure SQLAlchemy. You need an S3 bucket (or MinIO, or GCS) for artifacts — and you need to set up HMAC credentials for it. You need a reverse proxy with TLS termination (Nginx, Caddy, Traefik — pick your poison) because MLflow ships with no auth. You need a way to back up two different systems and keep them in sync. And you need all of this to survive restarts, storage failures, and the occasional "I accidentally deleted my data directory" moment.

I have set up MLflow from scratch at three different companies. The average time from zero to a team-accessible, production-grade MLflow instance is a full working day. The average time until the first "why is Postgres down" page is two weeks.

This is the operational tax that MLflow imposes. For a solo ML engineer — who just wants to track hyperparameters, compare runs, and see a loss curve — it is absurd.

---

## The wedge: one binary, one command, zero dependencies

LiteMLflow is my answer to this. It is a single statically-linked Go binary that, when you run `litemlflow up`, gives you:

- A full MLflow-compatible REST API (31 canonical client operations tested)
- An embedded UI (metrics charts, trace waterfall, prompt diff, bulk compare)
- A SQLite database (pure-Go, embedded, no external process)
- Filesystem artifact storage (or S3-compatible, if you prefer)
- Basic auth and OIDC out of the box
- Prometheus `/metrics` for your dashboards
- A Kubernetes operator and Helm chart when you need scale

The installation is:

```bash
brew install litemlflow/tap/litemlflow
litemlflow up --data ./data
```

Your existing MLflow Python code starts working in 53 milliseconds.

There is no Postgres to install. No S3 to configure. No reverse proxy to manage. Backup is `cp -r data backup/`. Restore is the reverse.

---

## The numbers: 143× and why it matters

The 143× cold-start headline comes from a controlled benchmark (`tests/integration/bench.py`, 1,000 runs, same loopback machine, MLflow using its SQLite backend — not Postgres — to give it the best chance):

| Metric | LiteMLflow | MLflow + SQLite | Ratio |
|---|---|---|---|
| Cold start | 53 ms | 7 513 ms | **143×** |
| `log_metric` p50 | 1.44 ms | 21.6 ms | **15×** |
| `log_batch` throughput | 24 533 rows/s | 8 008 rows/s | **3.1×** |
| Populate 1 000 runs | 3.8 s | 64.7 s | **17×** |
| UI first paint p50 | 0.6 ms | n/a (React bundle) | — |

The cold-start gap is structural: MLflow starts a CPython process, initializes SQLAlchemy, discovers and applies any pending Alembic migrations, warms up connection pools, and serves a 2 MB React bundle before it can accept a request. LiteMLflow starts a Go binary, opens a WAL-mode SQLite file, and listens. That's it.

I want to be honest about where MLflow is competitive: raw sequential metric-history scans on large series. MLflow's SQLite column scan returns 50 000 raw metric points in 2.6 ms; LiteMLflow takes 124 ms. MLflow's B-tree index layout is better tuned for sequential reads of this shape. We handle this in practice by returning LTTB-downsampled series (500 representative points instead of 50 000) which is the right trade-off for chart rendering — but the raw number is slower.

The full benchmark report with raw JSON is in [docs/bench-v04.md](https://github.com/gorevds/litemlflow/blob/main/docs/bench-v04.md).

---

## The demo

Here is the full workflow:

```bash
# Install
brew install litemlflow/tap/litemlflow

# Start (53 ms)
litemlflow up --data ./experiments

# Log from your existing code — zero changes required
export MLFLOW_TRACKING_URI=http://localhost:5000
python train.py   # your existing training script

# Or use the native SDK for LLM workflows
pip install 'litemlflow[langchain]'
```

The UI loads in 0.6 ms (first paint, p50). The trace waterfall shows spans nested under the run. The metric charts downsample large series server-side via LTTB so even 50 000 points render instantly.

If you want to see a live instance: [lmf.gorev.space](https://lmf.gorev.space) is running v1.0.0-rc1 with the demo dataset seeded.

---

## What's not in v1.0

I want to be direct about the trade-offs:

**MLflow API coverage is ~80%, not 100%.** The canonical 80% — experiments, runs, metrics, params, tags, artifacts, search, Model Registry — all work. The long tail of MLflow's surface area (dataset lineage details, certain autologging hooks, the GraphQL gateway MLflow is prototyping) is not covered. There is a compatibility matrix in the docs.

**SQLite single-writer means one concurrent writer at a time.** This is fine for a solo engineer or a team with moderate write volume. If you are logging from 200 parallel training jobs simultaneously, you will see write contention. The Kubernetes operator runs `replicas: 1` for this reason. Horizontal scale is on the Y2 roadmap.

**The external pen test is still pending.** The OIDC and auth layers have been reviewed internally and fuzz-tested, but a third-party security audit has not happened yet. The threat model is documented in `docs/spec/threat-model.md`. Do not put LiteMLflow behind a public internet endpoint with untrusted users until the pen test is done (planned for Q1 Y2).

**No hosted SaaS.** LiteMLflow v1 is self-host-only. A hosted version is a possible Y2 direction but not a commitment.

---

## What's next

Year-2 roadmap ships in four streams:

1. **v1.1 — Analytics primitives** (Q1): DuckDB attached to the SQLite file for cross-experiment OLAP queries. "Best `eval/f1` per optimizer last 30 days" in < 200 ms on 100k runs.
2. **v1.2 — Dataset versioning** (Q2): First-class datasets API with content-addressed storage and lineage. Re-uploads deduplicate to a single physical copy.
3. **v1.3 — Federated multi-server** (Q3): One UI across multiple LiteMLflow instances. For distributed teams running one instance per project.
4. **v1.4 — Time-travel + lineage** (Q4): "Show me runs as of last Tuesday" and parent/child lineage graphs between runs.

---

## Try it

```bash
brew install litemlflow/tap/litemlflow
litemlflow up --data ./data
```

GitHub: [github.com/gorevds/litemlflow](https://github.com/gorevds/litemlflow)  
Docs: [docs.litemlflow.dev](https://docs.litemlflow.dev) *(live after launch)*  
Live demo: [lmf.gorev.space](https://lmf.gorev.space)

If this saves you the afternoon I spent setting up Postgres the third time, give it a star. If you find something broken, open an issue — the compatibility matrix has a lot of surface area and I need help finding the edges.
