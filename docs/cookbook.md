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

## 3. Auto-instrument a LangChain RAG run

Install the optional extra:

```bash
pip install 'litemlflow[langchain]'
```

```python
from litemlflow import Client
from litemlflow.langchain import LiteMLflowCallbackHandler
from langchain_openai import ChatOpenAI
from langchain_core.prompts import ChatPromptTemplate
from langchain_core.output_parsers import StrOutputParser

# Connect and create the handler — it auto-creates a run in the "langchain"
# experiment (or specify experiment_id= / run_id= to attach to an existing one).
client = Client("http://localhost:5000")
handler = LiteMLflowCallbackHandler(client, auto_metrics=True)

# Build a simple RAG-style chain.
prompt = ChatPromptTemplate.from_template(
    "Answer based on the context.\n\nContext: {context}\n\nQuestion: {question}"
)
llm = ChatOpenAI(model="gpt-4o-mini")
chain = prompt | llm | StrOutputParser()

# Run the chain — all spans are recorded automatically.
answer = chain.invoke(
    {"context": "LiteMLflow is a lightweight experiment tracker.", "question": "What is LiteMLflow?"},
    config={"callbacks": [handler]},
)
print(answer)
```

Sample trace tree (visible in the LiteMLflow UI under the auto-created run):

```
langchain-trace-1746700000
└── chain:RunnableSequence          [OK, 1.23 s]
    ├── chain:ChatPromptTemplate     [OK, 0.001 s]
    ├── chat:gpt-4o-mini             [OK, 1.22 s]
    │     tokens.prompt=42  tokens.completion=31  cost.usd=0.0000247
    └── chain:StrOutputParser        [OK, 0.000 s]

Metrics logged to run:
  tokens.prompt      42
  tokens.completion  31
  tokens.total       73
  cost.usd           0.0000247
```

Every LangChain event type (chain, LLM, chat model, tool, retriever) is
captured as a span with timing, status, and relevant attributes. Token usage
and cost are logged as run metrics on `on_llm_end` / `on_chat_model_end` when
`auto_metrics=True` (default).

Spans are batched and flushed in a single HTTP call when the root span closes,
so you pay one round-trip per chain invocation.

### Attaching to an existing run

```python
# Create a run manually and pass its id.
exp_id = client.create_experiment("rag-evals")
run = client.create_run(exp_id, name="trial-1")
run.log_param("retriever_k", "5")

handler = LiteMLflowCallbackHandler(client, run_id=run.id)
chain.invoke({"context": "...", "question": "..."}, config={"callbacks": [handler]})

run.finish()
```

## 4. Auto-instrument a LlamaIndex query engine

Install the optional extra:

```bash
pip install 'litemlflow[llamaindex]'
```

```python
from litemlflow import Client
from litemlflow.llamaindex import LiteMLflowEventHandler
import llama_index.core.instrumentation as instrument

# Connect and create the handler — it auto-creates a run in the "llamaindex"
# experiment (or specify experiment_id= / run_id= to attach to an existing one).
client = Client("http://localhost:5000")
handler = LiteMLflowEventHandler(client, auto_metrics=True)

# Register the handler on the root dispatcher so it receives all events
# emitted by any LlamaIndex component in this process.
dispatcher = instrument.get_dispatcher()
dispatcher.add_event_handler(handler)

# Run any query — the handler records spans automatically.
from llama_index.core import VectorStoreIndex, SimpleDirectoryReader

documents = SimpleDirectoryReader("data/").load_data()
index = VectorStoreIndex.from_documents(documents)
query_engine = index.as_query_engine()
response = query_engine.query("What is LiteMLflow?")
print(response)
```

Sample trace tree (visible in the LiteMLflow UI under the auto-created run):

```
llamaindex-trace-1746700000
└── query:a3f1b2c4                        [OK, 0.91 s]
    ├── retrieval                          [OK, 0.03 s]
    │     nodes.count=3
    ├── synthesis                          [OK, 0.87 s]
    └── llm:gpt-4o-mini                   [OK, 0.86 s]
          tokens.prompt=312  tokens.completion=45  cost.usd=0.0000735

Metrics logged to run:
  tokens.prompt      312
  tokens.completion  45
  tokens.total       357
  cost.usd           0.0000735
```

Every LlamaIndex event type (query, retrieval, synthesis, LLM completion, chat,
embedding) is captured as a span with timing, status, and relevant attributes.
Token usage and cost are logged as run metrics when present.

Spans are batched and flushed in a single HTTP call when the root query span
closes, so you pay one round-trip per query invocation.

### Attaching to an existing run

```python
exp_id = client.create_experiment("rag-evals")
run = client.create_run(exp_id, name="trial-1")
run.log_param("retriever_k", "5")

handler = LiteMLflowEventHandler(client, run_id=run.id)
dispatcher.add_event_handler(handler)
# ... run your queries ...
run.finish()
```

## 6. Evaluate two models against each other

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

## 7. Send OpenTelemetry traces

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

## 7b. Send traces via OTLP/gRPC

LiteMLflow also accepts standard OTLP/gRPC at the address configured with
`--otlp-grpc-addr`. This is the transport used by default in the Go and Java
OpenTelemetry SDKs, and by the OpenTelemetry Collector.

### Start LiteMLflow with the gRPC listener

```bash
litemlflow up --data ./data --otlp-grpc-addr 127.0.0.1:4317
```

The OTLP/gRPC default port is `4317`. The HTTP/JSON listener remains active
on `--addr` (default `:5000`) in parallel.

### Python example (OpenTelemetry SDK)

```bash
pip install opentelemetry-sdk opentelemetry-exporter-otlp-proto-grpc
```

```python
from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor

# Point the gRPC exporter at LiteMLflow's gRPC listener.
provider = TracerProvider(resource=Resource.create({
    "service.name": "my-rag-service",
    "litemlflow.run_id": "f0a1b2c3...",  # optional: link to a run
}))
provider.add_span_processor(BatchSpanProcessor(
    OTLPSpanExporter(
        endpoint="http://localhost:4317",  # plaintext gRPC
        insecure=True,
    )
))
trace.set_tracer_provider(provider)

tracer = trace.get_tracer(__name__)
with tracer.start_as_current_span("inference") as span:
    span.set_attribute("tokens.input", 120)
    # ... your code ...
```

### OpenTelemetry Collector pipeline

Forward spans from an OTel Collector to LiteMLflow:

```yaml
# otel-collector-config.yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317

exporters:
  otlp/litemlflow:
    endpoint: localhost:4317   # LiteMLflow gRPC listener
    tls:
      insecure: true

service:
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [otlp/litemlflow]
```

### Production TLS

The gRPC listener is plaintext. For production, terminate TLS at a sidecar:

```bash
# Envoy example (excerpt):
#   listeners[0].filter_chains[0].filters[0].typed_config.route_config
#   → cluster pointing to 127.0.0.1:4317 with grpc_web or h2 upstream.

# Nginx (nginx >= 1.13.10 with --with-http_v2_module):
server {
    listen 4318 ssl http2;
    ssl_certificate /etc/ssl/litemlflow.crt;
    ssl_certificate_key /etc/ssl/litemlflow.key;
    location / {
        grpc_pass grpc://127.0.0.1:4317;
    }
}
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

## 12. Migrate from MLflow to LiteMLflow

Move all your existing MLflow experiments, runs, metrics, params, tags, and
artifacts into LiteMLflow in a single command.  No Python script required; no
LiteMLflow server needs to be running during the import.

### Prerequisites

- The source MLflow tracking server must be reachable (HTTP/HTTPS).
- The target data directory must exist and be writable.
- `litemlflow` binary must be v0.1 or newer (built with `make build`).

### One-shot import

```bash
# Create (or reuse) a fresh data directory.
mkdir -p /var/lib/litemlflow/data

# Run the import — LiteMLflow MUST NOT be running on this data dir.
litemlflow import-mlflow \
  --from  http://my-mlflow-server:5000 \
  --data  /var/lib/litemlflow/data

# The importer prints a live progress summary, e.g.:
#   [import] connecting to http://my-mlflow-server:5000 ... ok (mlflow 3.12.0)
#   [import] enumerated 12 experiments, 247 runs
#   [import] importing exp 1/12: "iris-classification" ... 23 runs ... done in 1.4s
#   ...
#   [import] complete: 12 experiments, 247 runs, 1248 metrics, 312 params, 89 tags, 14 artifacts in 18.3s
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `--from URL` | (required) | URL of the source MLflow server |
| `--data DIR` | (required) | LiteMLflow data directory to write into |
| `--workspace WS` | `default` | Workspace to import into |
| `--include-deleted` | off | Also import `lifecycle_stage=deleted` experiments and runs |
| `--dry-run` | off | Enumerate without writing; useful for a pre-flight check |

### Resuming an interrupted import

The importer writes a checkpoint to `<data>/.import-state.json` after each
successfully imported run.  If the process is killed mid-import, simply
re-run the same command with the same `--data` directory.  Runs that have
already been imported are skipped automatically.

```bash
# First attempt — interrupted at run 180/247.
litemlflow import-mlflow --from http://my-mlflow-server:5000 --data ./data
# ^C

# Resume — automatically picks up from run 181.
litemlflow import-mlflow --from http://my-mlflow-server:5000 --data ./data
# [import] resuming: 180 runs already imported
```

### Dry-run preview

```bash
litemlflow import-mlflow \
  --from http://my-mlflow-server:5000 \
  --data ./data \
  --dry-run
```

Nothing is written.  The summary line shows what *would* be imported.

### Including deleted experiments

By default only `lifecycle_stage=active` experiments and runs are imported.
Pass `--include-deleted` to also copy soft-deleted entities:

```bash
litemlflow import-mlflow \
  --from http://my-mlflow-server:5000 \
  --data ./data \
  --include-deleted
```

### Multi-workspace import

Import into a specific workspace (creates the workspace if it doesn't exist yet):

```bash
litemlflow import-mlflow \
  --from http://my-mlflow-server:5000 \
  --data ./data \
  --workspace team-nlp
```

### Start LiteMLflow after import

Once the import is complete, start the server normally:

```bash
litemlflow up --data /var/lib/litemlflow/data
```

Point your MLflow Python client (or browser) at `http://localhost:5000`.
All experiments, runs, metrics, and artifacts should be available immediately.

### Name collision handling

If an experiment with the same name already exists in the target workspace
(e.g., from a previous partial import), the importer creates a new experiment
named `<original>-imported-<timestamp>` rather than merging runs into an
unrelated experiment.  You can clean up duplicates via the UI or API after
verifying the import.

### Run ID preservation

MLflow run IDs are UUID4 hex strings (32 chars).  LiteMLflow stores run IDs
in the same format and accepts caller-supplied IDs at `CreateRun`.  The
importer passes the original MLflow run ID through, so:

- `get_run(run_id)` returns the same run in LiteMLflow as in MLflow.
- Existing links from notebooks/reports that reference run IDs continue to work.
- On a re-import the run is skipped (idempotent) because the checkpoint
  records its ID.

### Artifact handling

Artifacts are downloaded from the MLflow source (via
`GET /api/2.0/mlflow-artifacts/artifacts/{run_id}/{path}`) and uploaded to
the LiteMLflow filesystem artifact store at `<data>/artifacts/<run_id>/<path>`.
Nested directories are resolved recursively.

If an artifact download fails, a warning is printed and the import continues;
artifacts can be re-imported on the next invocation (checkpoint tracks only
completed runs, so a run whose artifact failed will be retried in full).

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

## 9. Switch artifact storage to S3-compatible

LiteMLflow defaults to writing artifacts to `$DATA/artifacts/` on local disk.
To store artifacts in any S3-compatible object store (AWS S3, MinIO, Ceph, Garage, …)
set `--artifact-backend s3` and supply the five required connection parameters.

### Quick start with MinIO (Docker)

```bash
# Start a local MinIO instance.
docker run -d --name minio \
  -p 9000:9000 -p 9001:9001 \
  -e MINIO_ROOT_USER=minioadmin \
  -e MINIO_ROOT_PASSWORD=minioadmin \
  quay.io/minio/minio server /data --console-address :9001

# Create the bucket (one-time).
docker exec minio mc alias set local http://localhost:9000 minioadmin minioadmin
docker exec minio mc mb local/litemlflow

# Start LiteMLflow with S3 backend.
litemlflow up \
  --data ./data \
  --artifact-backend s3 \
  --s3-endpoint http://localhost:9000 \
  --s3-bucket litemlflow \
  --s3-region us-east-1 \
  --s3-access-key minioadmin \
  --s3-secret-key minioadmin
```

### AWS S3

```bash
litemlflow up \
  --data ./data \
  --artifact-backend s3 \
  --s3-endpoint https://s3.amazonaws.com \
  --s3-bucket my-ml-artifacts \
  --s3-region eu-west-1 \
  --s3-access-key "$AWS_ACCESS_KEY_ID" \
  --s3-secret-key "$AWS_SECRET_ACCESS_KEY"
```

Credentials are better supplied via environment variables so they do not appear
in process listings:

```bash
export LITEMLFLOW_ARTIFACT_BACKEND=s3
export LITEMLFLOW_S3_ENDPOINT=https://s3.amazonaws.com
export LITEMLFLOW_S3_BUCKET=my-ml-artifacts
export LITEMLFLOW_S3_REGION=eu-west-1
export LITEMLFLOW_S3_ACCESS_KEY="$AWS_ACCESS_KEY_ID"
export LITEMLFLOW_S3_SECRET_KEY="$AWS_SECRET_ACCESS_KEY"

litemlflow up --data ./data
```

### Key layout

Artifacts are stored under:

```
<prefix>artifacts/<run-id>/<relative-path>
```

`--s3-prefix` (default `""`) lets you share a bucket between multiple
LiteMLflow deployments, e.g. `--s3-prefix staging/`.

### Addressing style

- **Path-style** is used for any endpoint that is not `amazonaws.com`:
  `http://minio:9000/<bucket>/<key>` — this is what MinIO requires by default.
- **Virtual-hosted style** is used for `amazonaws.com`:
  `https://<bucket>.s3.<region>.amazonaws.com/<key>`.

### Signing

Every request is signed with AWS Signature Version 4 using only Go standard
library primitives (`crypto/hmac`, `crypto/sha256`, `encoding/hex`).
No AWS SDK or MinIO client library is required.

### Limitations (v0.2 roadmap)

- Uploads are single-part PUT requests; multipart upload for very large files
  (> 5 GiB) is planned for v0.3.
- No presigned URL generation yet (all data is proxied through LiteMLflow).
- IAM instance-profile / IRSA automatic credential discovery is not implemented;
  explicit `access-key` / `secret-key` are required.

## 13. Manage workspace members from the UI

LiteMLflow's embedded UI provides a dedicated page for managing workspace
membership without needing `curl`. Navigate to it from the **Workspaces** page
or directly via the URL hash.

### Step 1 — open the Workspaces page

Click the **Workspaces** link in the header (or navigate to `/#/workspaces`).
You will see a table of all workspaces with a **Manage members** link on each
row.

### Step 2 — open the Members page

Click **Manage members** for a workspace, or navigate directly to:

```
http://localhost:5000/#/workspaces/<workspace-id>/members
```

For example, `http://localhost:5000/#/workspaces/team-nlp/members`.

The page fetches two API calls simultaneously:

```
GET /api/v1/workspaces/team-nlp          → workspace name + description
GET /api/v1/workspaces/team-nlp/members  → current member list
```

### Step 3 — view existing members

The members table shows three columns:

| Column | Description |
|---|---|
| **User ID** | The identifier used for authentication (e.g. the basic-auth username or OIDC `sub` claim) |
| **Role** | A dropdown: `viewer`, `editor`, or `admin` |
| **Actions** | **Remove** button |

### Step 4 — change a member's role

Click the **Role** dropdown on any row and select the new role. The UI immediately
calls:

```
PUT /api/v1/workspaces/team-nlp/members/alice
Content-Type: application/json

{"role": "editor"}
```

No "Save" button needed — the change is applied on dropdown change.

### Step 5 — add a new member

Fill in the **User ID** field and choose a **Role** in the "Add member" form at
the bottom, then click **+ Add member** (or press Enter). The UI calls:

```
PUT /api/v1/workspaces/team-nlp/members/bob
Content-Type: application/json

{"role": "viewer"}
```

On success the page reloads the member list; the new member appears in the
table.

### Step 6 — remove a member

Click the **Remove** button on any row. After confirmation the UI calls:

```
DELETE /api/v1/workspaces/team-nlp/members/bob
```

The row disappears immediately from the table.

### Access control

The Members page is admin-only. If you are not an admin of the selected
workspace (or if `auth=none` is disabled), the page shows:

> You must be an **admin** of workspace `team-nlp` to manage its members.

This surfaces the underlying **403** response from the API.

### curl equivalents

```bash
# List members
curl http://localhost:5000/api/v1/workspaces/team-nlp/members | python3 -m json.tool

# Add/update a member
curl -X PUT http://localhost:5000/api/v1/workspaces/team-nlp/members/alice \
  -H 'Content-Type: application/json' -d '{"role": "admin"}'

# Remove a member
curl -X DELETE http://localhost:5000/api/v1/workspaces/team-nlp/members/bob
```

## 10. Multiple workspaces for a small team

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

### Step 6 — role-based access control

Starting from v0.3, member roles are enforced. The three roles are:

| Role | Read | Write experiments/runs | Manage workspace / members |
|---|:---:|:---:|:---:|
| `viewer` | ✅ | ❌ | ❌ |
| `editor` | ✅ | ✅ | ❌ |
| `admin`  | ✅ | ✅ | ✅ |

```bash
# Promote bob from viewer to editor.
curl -s -X PUT http://localhost:5000/api/v1/workspaces/team-nlp/members/bob \
  -H 'Content-Type: application/json' -d '{"role": "editor"}'

# Remove bob's access.
curl -s -X DELETE http://localhost:5000/api/v1/workspaces/team-nlp/members/bob

# List all members of a workspace.
curl -s http://localhost:5000/api/v1/workspaces/team-nlp/members | python3 -m json.tool
```

**Open mode (fresh-install backward compat):** The `default` workspace with zero members configured operates in *open mode* — all authenticated users can read and write, no role required. This ensures that existing MLflow clients and solo users need no configuration changes. The moment you add the first member to `default`, role enforcement activates for that workspace.

**auth=none single-user mode:** When `--auth=none` is set, RBAC is entirely inactive. All requests pass through regardless of workspace membership.

### Notes

- The `default` workspace cannot be deleted and requires no `X-Workspace` header; existing MLflow clients continue to work unchanged.
- Workspace IDs are slugs (`[a-z0-9-]{1,64}`), immutable after creation.
- A workspace with experiments cannot be deleted until all experiments are removed or moved (there is no move API yet — delete the experiments first).
- RBAC uses the workspace resolved from `X-Workspace` / `lmf_workspace` / default for the role check. When managing members of workspace `team-nlp`, set `X-Workspace: team-nlp` so the middleware knows your role in that workspace.

## 11. Plot a million-point metric series in <300 ms

When a training run logs hundreds of thousands of metrics (e.g., per-token loss in an LLM pre-training job), fetching the full series is slow and the browser renders it poorly. Use the `?downsample=N` query parameter to get a visual summary instead.

### REST

```bash
# Fetch 500 LTTB-representative points from a large series.
curl "http://localhost:5000/api/2.0/mlflow/metrics/get-history?run_id=<RUN_ID>&metric_key=loss&downsample=500"
```

Response:
```json
{
  "metrics": [
    {"key": "loss", "value": 3.14, "timestamp": 1715000000000, "step": 0},
    {"key": "loss", "value": 1.07, "timestamp": 1715003600000, "step": 10000},
    ...
  ],
  "downsampled_from": 1000000
}
```

`"downsampled_from"` tells you the total raw count; `"metrics"` contains the representative subset. The first and last points are always included.

### Python

```python
import requests

BASE = "http://localhost:5000"
run_id = "your-run-id-here"

resp = requests.get(
    f"{BASE}/api/2.0/mlflow/metrics/get-history",
    params={"run_id": run_id, "metric_key": "loss", "downsample": 500},
)
data = resp.json()
print(f"Showing {len(data['metrics'])} of {data['downsampled_from']} points")

# Plot with matplotlib.
import matplotlib.pyplot as plt
pts = data["metrics"]
xs = [p["step"] for p in pts]
ys = [p["value"] for p in pts]
plt.plot(xs, ys)
plt.title(f"loss (LTTB, {len(pts)} of {data['downsampled_from']} pts)")
plt.show()
```

### UI

The embedded UI automatically uses `?downsample=1000` when rendering metric charts. When the server returns fewer points than the raw total, a note appears next to the chart title:

```
loss  (showing 1000 of 1000000 points, LTTB)
```

No client-side configuration is needed.

### Algorithm

LiteMLflow uses **Largest-Triangle-Three-Buckets (LTTB)** by Steinarsson (2013). The series is divided into `target` equal-width buckets; within each bucket the point that forms the largest triangle area with the previously selected point and the centroid of the next bucket is kept. This greedy selection preserves visually prominent peaks and troughs far better than uniform stride sampling.

### When to use which path

| Use case | Query parameters |
|---|---|
| Visualising a large series in the browser or a notebook | `?downsample=N` |
| Programmatic access that needs every point | `?max_results=N&page_token=...` |
| Small series (≤ N points) | Either; LTTB returns all points unchanged |

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

## 11. Scrape LiteMLflow with Prometheus

LiteMLflow exposes `GET /metrics` in OpenMetrics text format. The endpoint is
public (no credentials required) even when `auth=basic` is configured.

### prometheus.yml

```yaml
global:
  scrape_interval: 15s

scrape_configs:
  - job_name: litemlflow
    static_configs:
      - targets: ["localhost:5000"]
    # No authentication needed — /metrics is always public.
    # If you want to restrict access at the network layer, put LiteMLflow
    # behind a reverse proxy and allow-list the Prometheus scraper IP.
```

Point Prometheus at your LiteMLflow instance and it will scrape every 15 s.

### Quick smoke test (no Prometheus required)

```bash
# Check the endpoint is reachable and returns OpenMetrics text.
curl -s http://localhost:5000/metrics | grep "^litemlflow_"

# Verify at least the HTTP request counter is present.
curl -s http://localhost:5000/metrics | grep litemlflow_http_requests_total

# Watch the request counter grow in real time.
watch -n 5 'curl -s http://localhost:5000/metrics | grep litemlflow_http_requests_total'
```

### Key metrics to alert on

| Alert | Expression | Meaning |
|---|---|---|
| High error rate | `rate(litemlflow_http_requests_total{status=~"5.."}[5m]) > 0.01` | More than 1% of requests are 5xx |
| Slow p95 latency | `histogram_quantile(0.95, rate(litemlflow_http_request_duration_seconds_bucket[5m])) > 0.5` | 95th-percentile latency above 500 ms |
| DB growth | `litemlflow_db_size_bytes > 5e9` | Database is larger than 5 GiB |
| Process restarted | `increase(litemlflow_build_info[5m]) > 0` | Build-info gauge resets on restart |
