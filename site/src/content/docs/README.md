---
title: Docs Overview
---
# LiteMLflow documentation

- [Vision](vision.md) — what we're building, for whom, and why.
- [Architecture](architecture.md) — system design, performance budgets, security boundaries.
- [Quickstart](quickstart.md) — install, run, log your first metric.
- [Cookbook](cookbook.md) — common patterns: sklearn, PyTorch, LangChain, OTLP, evals, deployment.

## Specifications

- [Data model](spec/data-model.md) — the unified ML/LLM graph.
- [MLflow API compatibility](spec/api-mlflow-compat.md) — what's covered, what's not.
- [Native API](spec/api-native.md) — traces, prompts, evals, OTLP.
- [Threat model](spec/threat-model.md) — security boundaries and assumptions.

## Architecture decision records

- [ADR 0001](adr/0001-go-pure-sqlite.md) — Why Go + pure-Go SQLite.

## Examples

- [`tests/integration/mlflow_compat.py`](../tests/integration/mlflow_compat.py) — runs the official MLflow Python client against LiteMLflow.

## Contributing

See [`../CONTRIBUTING.md`](../CONTRIBUTING.md).
