# Hacker News Launch

## Submission title

**LiteMLflow: MLflow-compatible experiment tracker in a single Go binary (143× faster cold start)**

*79 characters — within the 80-char limit. No buzzwords.*

---

## Submission URL

https://github.com/gorevds/litemlflow

---

## Author's first comment (post immediately after submission)

Hi HN, I built LiteMLflow because I was tired of the MLflow operational tax.

**The problem:** MLflow is the de-facto standard for ML experiment tracking, but running it properly requires Postgres, S3 (or MinIO), a reverse proxy with TLS, and a backup strategy that covers two different systems. I have set this up from scratch at three companies. It takes a full day the first time and something breaks every month.

**What LiteMLflow is:** a single statically-linked Go binary. `litemlflow up --data ./data` gives you the full MLflow REST API, an embedded UI, SQLite for metadata, filesystem (or S3) for artifacts, basic/OIDC auth, and Prometheus metrics. No external process. No runtime dependencies.

**The performance numbers** come from a controlled benchmark on loopback (same machine, MLflow using SQLite — not Postgres — to give it the best chance):
- Cold start: 53 ms vs 7.5 s → 143×
- `log_metric` p50: 1.4 ms vs 21.6 ms → 15×
- `log_batch` throughput: 24 533 rows/s vs 8 008 rows/s → 3.1×

The cold-start gap is structural: MLflow starts Python, SQLAlchemy, Alembic migrations, and serves a React bundle. LiteMLflow opens a WAL-mode SQLite file and listens.

**Honest trade-offs:**

1. MLflow API coverage is ~80%. The canonical 80% (experiments, runs, metrics, params, tags, artifacts, search, Model Registry) all pass against the real MLflow Python client (31/31 checks). The long tail is not covered. If your code uses obscure MLflow internals, check the compat matrix first.

2. SQLite single-writer. If you are logging from 200 parallel training jobs, you will see write contention. This is a deliberate choice for the target user (solo engineer, small team). Horizontal scale is on the roadmap for Y2.

3. Where MLflow wins: raw sequential metric-history scans. 50 000 raw metric points: MLflow 2.6 ms, LiteMLflow 124 ms. We handle this with server-side LTTB downsampling (returning 500 representative points), which is the right UI trade-off — but the raw number is slower.

4. External pen test pending. Auth layers are fuzz-tested and reviewed internally; third-party audit is Q1 Y2. Do not expose to untrusted internet users yet.

**Why Go?** Single binary distribution (proven by Ollama, DuckDB, Litestream, Caddy). Pure-Go SQLite driver (modernc.org/sqlite) avoids CGo. Go's net/http is HTTP/2-capable and allocates far less than a Python WSGI stack per request.

**Migration from MLflow:** change one environment variable.
```
MLFLOW_TRACKING_URI=http://localhost:5000  # was: http://mlflow-server:5000
```
Your existing MLflow Python code works unchanged.

There is also `litemlflow import-mlflow --src http://mlflow-server:5000` to copy existing data over.

**Live demo:** https://lmf.gorev.space (v1.0.0-rc1, seeded with the demo dataset)

Would love to hear from anyone running MLflow at scale — especially if you have hit a wall with the operational complexity or want to share what the long tail of the API surface looks like in your usage.
