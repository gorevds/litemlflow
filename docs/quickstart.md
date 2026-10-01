# Quickstart

## Install a release

```bash
# Raw binary (linux|darwin, amd64|arm64)
curl -fsSL -o /usr/local/bin/litemlflow \
  https://github.com/gorevds/litemlflow/releases/latest/download/litemlflow-linux-amd64
chmod +x /usr/local/bin/litemlflow

docker run -p 5000:5000 -v $(pwd)/data:/data ghcr.io/gorevds/litemlflow:latest   # Docker
helm install lmf oci://ghcr.io/gorevds/charts/litemlflow --version 0.2.0         # Kubernetes
```

Homebrew, Debian, RPM and Snap packages were sunset in v1.2 (see
`dist/_sunset/README.md`).

## Install from source

Build from source (requires Go 1.26+):

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

The data directory is the source of truth. `backup` is safe while the server
runs: the DB is snapshotted with `VACUUM INTO` (committed data only, WAL folded
in) and artifacts are copied as-is. The snapshot is staged in `$TMPDIR`, so make
sure it has room for one copy of the DB.

## Health check

```bash
./bin/litemlflow healthcheck                                   # GET http://127.0.0.1:<port>/healthz
./bin/litemlflow healthcheck --url http://10.0.0.5:5000/healthz --timeout 3s
```

Exits 0 when the endpoint returns 200 with `{"ok":true}`, 1 otherwise. The
default URL uses the port from `LITEMLFLOW_ADDR` (fallback `5000`). The Docker
image uses it as its `HEALTHCHECK` (distroless has no shell or curl).

## Auth

The default listen address is `:5000` (all interfaces) with no auth, so on a
shared host pass `--addr 127.0.0.1:5000`. To expose to a small team:

```bash
# Hash a password (bcrypt; read from stdin so it stays out of argv):
HASH=$(printf '%s' 'hunter2' | ./bin/litemlflow hash-password)

./bin/litemlflow up \
  --data ./data \
  --addr 0.0.0.0:5000 \
  --auth basic \
  --basic-user alice \
  --basic-pass-hash "$HASH"
```

Then put it behind a TLS-terminating proxy (Caddy, Traefik, Nginx) or use `--auth oidc`.

### Behind a reverse proxy: `--trusted-proxies`

The login rate limiter (`POST /api/v1/auth/login`) keys on the client IP. By
default only the TCP peer address is used, so behind a proxy every user shares
the proxy's bucket. Pass the proxy addresses to trust their forwarding headers:

```bash
./bin/litemlflow up --data ./data --trusted-proxies 127.0.0.1,10.0.0.0/8
# or: LITEMLFLOW_TRUSTED_PROXIES="127.0.0.1, 10.0.0.0/8"
```

Comma/space-separated IPs or CIDRs; an invalid entry fails startup. Headers are
honoured only when the TCP peer is in the list: `X-Forwarded-For` is walked
right-to-left and the first untrusted hop is the client (a malformed hop stops
the walk and the peer address is used); without XFF, `X-Real-IP` is used. IPv6
clients are keyed by /64. Only list proxies you control and that overwrite or
append to these headers — anything listed can choose the rate-limit key.

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
