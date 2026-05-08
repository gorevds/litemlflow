# Cookbook

Recipes for common LiteMLflow patterns. Each recipe is self-contained.

## 1. Track a sklearn workflow

```python
import mlflow
from sklearn.datasets import load_iris
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import accuracy_score
from sklearn.model_selection import train_test_split

mlflow.set_tracking_uri("http://localhost:5000")
mlflow.set_experiment("iris-classification")

X, y = load_iris(return_X_y=True)
Xtr, Xte, ytr, yte = train_test_split(X, y, test_size=0.2, random_state=42)

with mlflow.start_run():
    for C in [0.1, 1.0, 10.0]:
        model = LogisticRegression(C=C, max_iter=200).fit(Xtr, ytr)
        acc = accuracy_score(yte, model.predict(Xte))
        mlflow.log_metric("acc", acc, step=int(C * 10))
        mlflow.log_param(f"C_{int(C*10)}", str(C))
```

## 2. Log a PyTorch training loop

```python
import mlflow
import torch
import torch.nn as nn

mlflow.set_tracking_uri("http://localhost:5000")

with mlflow.start_run(run_name="pytorch-mnist"):
    mlflow.log_param("optimizer", "adam")
    mlflow.log_param("lr", 1e-3)
    model = nn.Linear(784, 10)
    opt = torch.optim.Adam(model.parameters(), lr=1e-3)
    for epoch in range(10):
        # ... your training logic ...
        loss = train_one_epoch(model, opt)  # noqa: F821 (placeholder)
        mlflow.log_metric("train_loss", loss, step=epoch)
        if epoch % 5 == 0:
            torch.save(model.state_dict(), f"/tmp/ckpt-{epoch}.pt")
            mlflow.log_artifact(f"/tmp/ckpt-{epoch}.pt")
```

## 3. Trace a LangChain RAG run

```python
from litemlflow import Client
from langchain_openai import ChatOpenAI
from langchain.prompts import ChatPromptTemplate

c = Client("http://localhost:5000")
exp_id = c.create_experiment("rag-traces")

prompt_text = "Answer based on context: {context}\n\nQuestion: {question}"
prompt_version = c.create_prompt("rag.qa", prompt_text)

with c.start_run(exp_id, name="rag-trial") as run:
    run.log_param("model", "gpt-4o-mini")
    run.log_param("prompt_version", str(prompt_version))

    trace_id = c.start_trace()
    pipeline = c.log_span(trace_id, "rag.pipeline", run_id=run.id)

    retrieve = c.log_span(trace_id, "rag.retrieve", run_id=run.id, parent_id=pipeline,
                           attrs={"k": 5, "index": "wiki"})
    generate = c.log_span(trace_id, "rag.generate", run_id=run.id, parent_id=pipeline,
                           attrs={"model": "gpt-4o-mini", "prompt_tokens": 120,
                                  "completion_tokens": 80})
    run.log_metric("latency_ms", 850.0)
    run.log_metric("tokens.total", 200.0)
```

## 4. Evaluate two models against each other

```python
from litemlflow import Client

c = Client("http://localhost:5000")
exp_id = c.create_experiment("model-evals")

# Two model runs
a = c.create_run(exp_id, name="gpt-4o-mini")
b = c.create_run(exp_id, name="claude-haiku")

# ... log model-specific metrics ...
c.log_metric(a.id, "f1", 0.81)
c.log_metric(b.id, "f1", 0.79)

# Eval run that compares them
eval_run = c.create_run(exp_id, name="eval-2026-05-07")
c.create_eval(
    eval_run.id,
    target_run_ids=[a.id, b.id],
    dataset_ref="hf://allenai/squad",
    score=0.81,  # headline (winner)
    metrics={"a.f1": 0.81, "b.f1": 0.79, "delta": 0.02},
)
```

## 5. Send OpenTelemetry traces

LiteMLflow accepts standard OTLP/JSON at `/v1/traces`. Configure your OTel exporter:

```python
from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor

provider = TracerProvider(resource=Resource.create({
    "service.name": "my-rag-service",
    "litemlflow.run_id": "f0a1b2c3...",  # optional: link to a run
}))
provider.add_span_processor(BatchSpanProcessor(
    OTLPSpanExporter(endpoint="http://localhost:5000/v1/traces")
))
trace.set_tracer_provider(provider)

tracer = trace.get_tracer(__name__)
with tracer.start_as_current_span("inference") as span:
    span.set_attribute("tokens.input", 120)
    # ... your code ...
```

## 6. Filter runs by metric and param

```python
import mlflow
from mlflow.tracking import MlflowClient

mlflow.set_tracking_uri("http://localhost:5000")
client = MlflowClient()

# Get the best-performing run with lr <= 0.01
runs = client.search_runs(
    [str(experiment_id)],
    filter_string="params.lr = '0.01' AND metrics.acc > 0.85",
    order_by=["attributes.start_time DESC"],
    max_results=10,
)
for r in runs:
    print(r.info.run_id, r.data.metrics.get("acc"))
```

## 7. Backup and migrate to a new server

On the source:

```bash
litemlflow backup --data /var/lib/litemlflow --out backup.tar.gz
scp backup.tar.gz new-server:/tmp/
```

On the destination:

```bash
mkdir -p /var/lib/litemlflow/data
litemlflow restore --data /var/lib/litemlflow/data --in /tmp/backup.tar.gz
litemlflow up --data /var/lib/litemlflow/data
```

## 9. Multiple workspaces for a small team

LiteMLflow supports multi-tenancy through workspaces. Each workspace is an isolated namespace for experiments and runs. A single `default` workspace exists out of the box so solo users and existing MLflow clients need no changes.

### Step 1 — create workspaces

```bash
# API token / basic-auth header omitted for brevity; add -u user:pass if needed.

curl -s -X POST http://localhost:5000/api/v1/workspaces \
  -H 'Content-Type: application/json' \
  -d '{"id": "team-nlp", "name": "NLP Team", "description": "Sentence embeddings and RAG work"}'

curl -s -X POST http://localhost:5000/api/v1/workspaces \
  -H 'Content-Type: application/json' \
  -d '{"id": "team-cv", "name": "CV Team", "description": "Vision models"}'
```

### Step 2 — assign members

```bash
# Give alice admin rights on team-nlp, bob read-only access.
curl -s -X PUT http://localhost:5000/api/v1/workspaces/team-nlp/members/alice \
  -H 'Content-Type: application/json' -d '{"role": "admin"}'

curl -s -X PUT http://localhost:5000/api/v1/workspaces/team-nlp/members/bob \
  -H 'Content-Type: application/json' -d '{"role": "viewer"}'
```

### Step 3 — log experiments in a workspace

**Python (MLflow client)**

```python
import mlflow

mlflow.set_tracking_uri("http://localhost:5000")

# All API calls below target "team-nlp".
# The MLflow client passes arbitrary headers since v2.x via the tracking client.
from mlflow.tracking import MlflowClient
client = MlflowClient(
    tracking_uri="http://localhost:5000",
)
# Set the workspace header on the underlying session.
client._tracking_client.store.get_host_creds().token  # dummy access to init
import requests
session = requests.Session()
session.headers.update({"X-Workspace": "team-nlp"})
# Or simpler: use the native Python SDK (python/litemlflow/).

mlflow.set_experiment("rag-v2")  # created in team-nlp if X-Workspace is forwarded
with mlflow.start_run():
    mlflow.log_param("model", "bge-small-en")
    mlflow.log_metric("recall@5", 0.87)
```

**curl**

```bash
curl -s -X POST http://localhost:5000/api/2.0/mlflow/experiments/create \
  -H 'Content-Type: application/json' \
  -H 'X-Workspace: team-nlp' \
  -d '{"name": "rag-v2"}'
```

### Step 4 — verify isolation

```bash
# List experiments in team-nlp — sees rag-v2.
curl -s -X POST http://localhost:5000/api/2.0/mlflow/experiments/search \
  -H 'X-Workspace: team-nlp' \
  -d '{}' | python3 -m json.tool

# List experiments in default — does NOT see rag-v2.
curl -s -X POST http://localhost:5000/api/2.0/mlflow/experiments/search \
  -d '{}' | python3 -m json.tool
```

### Step 5 — find your current workspace

```bash
curl -s http://localhost:5000/api/v1/workspaces/current \
  -H 'X-Workspace: team-nlp'
# → {"workspace": {"id": "team-nlp", ...}, "user": "alice", "role": "admin"}
```

### Notes

- The `default` workspace cannot be deleted and requires no `X-Workspace` header; existing MLflow clients continue to work unchanged.
- Workspace IDs are slugs (`[a-z0-9-]{1,64}`), immutable after creation.
- A workspace with experiments cannot be deleted until all experiments are removed or moved (there is no move API yet — delete the experiments first).
- Member roles (`viewer`, `editor`, `admin`) are stored but not yet enforced by the API layer; enforcement is planned for v0.2 once OIDC lands.

## 8. Run as a systemd service

`/etc/systemd/system/litemlflow.service`:

```ini
[Unit]
Description=LiteMLflow experiment tracker
After=network-online.target

[Service]
Type=simple
User=litemlflow
Group=litemlflow
ExecStart=/usr/local/bin/litemlflow up --data /var/lib/litemlflow --addr 127.0.0.1:5000
Restart=on-failure
RestartSec=2s
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/litemlflow
PrivateTmp=true
NoNewPrivileges=true
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

```bash
sudo useradd --system --home /var/lib/litemlflow --shell /usr/sbin/nologin litemlflow
sudo install -d -o litemlflow -g litemlflow -m 0750 /var/lib/litemlflow
sudo systemctl daemon-reload
sudo systemctl enable --now litemlflow
```

Put a Caddy/Nginx in front for TLS (Caddy auto-provisions Let's Encrypt; v0.2 will bring this in-process).
