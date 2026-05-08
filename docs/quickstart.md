# Quickstart

## Install via package manager

```bash
brew install litemlflow/tap/litemlflow            # macOS / Linux brew
sudo apt install litemlflow                       # Debian/Ubuntu (after adding apt repo)
sudo dnf install litemlflow                       # Fedora/RHEL (after adding rpm repo)
sudo snap install litemlflow                      # Snap
helm install lmf oci://ghcr.io/litemlflow/charts/litemlflow --version 0.1.0   # Kubernetes
```

## Install from source

Build from source (requires Go 1.22+):

```bash
git clone https://github.com/gorevds/litemlflow
cd litemlflow
make build
```

## Run the server

```bash
./bin/litemlflow up --data ./data
```

Open http://localhost:5000 — the UI is served from the same binary.

## Log from Python (existing MLflow code)

```python
import mlflow

mlflow.set_tracking_uri("http://localhost:5000")
mlflow.set_experiment("my-first")

with mlflow.start_run():
    mlflow.log_param("lr", 0.01)
    for step, loss in enumerate([0.9, 0.7, 0.5, 0.3]):
        mlflow.log_metric("loss", loss, step=step)
```

That's it — no SDK install required, your existing MLflow code works. Refresh the UI to see your run.

## Log via the native SDK (LLM traces, prompts, evals)

```bash
pip install litemlflow
```

```python
from litemlflow import Client

c = Client("http://localhost:5000")
exp_id = c.create_experiment("rag-experiments")

with c.start_run(exp_id, name="trial-1") as run:
    run.log_param("model", "gpt-4o-mini")
    trace_id = c.start_trace()
    parent = c.log_span(trace_id, "rag.pipeline", run_id=run.id, attrs={"k": 5})
    c.log_span(trace_id, "rag.retrieve", run_id=run.id, parent_id=parent, attrs={"docs": 5})
    c.log_span(trace_id, "rag.generate", run_id=run.id, parent_id=parent, attrs={"tokens": 230})
    run.log_metric("answer_quality", 0.83)

# Versioned prompts
v = c.create_prompt("rag.system", "You are a helpful assistant.")
c.set_prompt_alias("rag.system", "production", v)
```

Open the UI at the run page — you'll see the metric chart *and* the trace waterfall in one place.

## Backup and restore

```bash
./bin/litemlflow backup --data ./data --out backup.tar.gz
./bin/litemlflow restore --data ./fresh-data --in backup.tar.gz
```

The data directory is the source of truth; copy it anywhere.

## Auth

Localhost-only is the default. To expose to a small team:

```bash
# Hash a password:
HASH=$(printf 'hunter2' | sha256sum | awk '{print $1}')

./bin/litemlflow up \
  --data ./data \
  --addr 0.0.0.0:5000 \
  --auth basic \
  --basic-user alice \
  --basic-pass-hash "$HASH"
```

Then put it behind a TLS-terminating proxy (Caddy, Traefik, Nginx) or use `--auth oidc` (lands in v0.2).

## What works today (v0.1)

- MLflow REST API: experiments, runs, metrics, params, tags, artifacts (list, upload, download, delete), metric history, search with filters
- LiteMLflow native API: traces (manual + OTLP/JSON), prompts (versioned, content-addressed, aliases), evals
- Embedded UI: experiments → runs → run detail (metrics charts + trace waterfall)
- Basic auth, anonymous mode
- Backup, restore, migrate, rollback

## What's coming in v0.2

- OIDC auth
- Built-in TLS via Let's Encrypt (autocert)
- Workspaces (multi-tenant) UI
- Plugin host (S3/GCS artifact backends)
- gRPC OTLP ingest
- Server-side metric downsampling for very large series
