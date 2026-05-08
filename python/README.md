# litemlflow (Python SDK)

The native Python client for [LiteMLflow](https://github.com/litemlflow/litemlflow).

## Install

```bash
pip install litemlflow
```

## Two ways to log

### A. Drop-in MLflow client (no code changes)

LiteMLflow speaks the MLflow REST API. Existing MLflow code works after switching the tracking URI:

```python
import mlflow
mlflow.set_tracking_uri("http://localhost:5000")
mlflow.log_metric("loss", 0.42)
```

You don't need this SDK for that path — just keep using `mlflow`.

### B. Native LiteMLflow client (LLM traces, prompts, evals)

The native client adds capabilities MLflow doesn't model:

```python
from litemlflow import Client

c = Client("http://localhost:5000")

# Classic ML
exp_id = c.create_experiment("training")
with c.start_run(exp_id, name="trial-1") as run:
    run.log_param("lr", 0.01)
    for step, loss in enumerate([0.9, 0.7, 0.5, 0.3]):
        run.log_metric("loss", loss, step=step)

# LLM trace ingest
trace_id = c.start_trace()
c.log_span(trace_id, name="rag.retrieve", attrs={"k": 5}, run_id=run.id)

# Prompt versioning
v = c.create_prompt("rag.system", "You are a helpful assistant.")
c.set_prompt_alias("rag.system", "production", v)
```

## License

Apache 2.0.
