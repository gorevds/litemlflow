"""End-to-end tests for the litemlflow Python SDK against a live server."""

from __future__ import annotations

import time

import pytest

from litemlflow import Client, LiteMLflowError


def test_health_and_version(server: str) -> None:
    c = Client(server)
    assert c.health() is True
    v = c.version()
    assert "version" in v


def test_experiment_lifecycle(server: str) -> None:
    c = Client(server)
    name = f"test-exp-{time.time_ns()}"
    eid = c.create_experiment(name)
    assert eid > 0

    fetched = c.get_experiment(eid)
    assert fetched["name"] == name

    by_name = c.get_experiment_by_name(name)
    assert int(by_name["experiment_id"]) == eid

    # Duplicate name must error.
    with pytest.raises(LiteMLflowError) as ei:
        c.create_experiment(name)
    assert ei.value.code == "RESOURCE_ALREADY_EXISTS"

    # Search includes the new exp.
    found = [e for e in c.search_experiments() if e["name"] == name]
    assert len(found) == 1


def test_run_logging(server: str) -> None:
    c = Client(server)
    eid = c.create_experiment(f"runs-{time.time_ns()}")
    with c.start_run(eid, name="r1", tags={"team": "vision"}) as run:
        run.log_param("lr", "0.01")
        for step, loss in enumerate([0.9, 0.7, 0.4]):
            run.log_metric("loss", loss, step=step)
    fetched = c.get_run(run.id)
    info = fetched["info"]
    assert info["status"] == "FINISHED"
    data = fetched["data"]
    assert any(m["key"] == "loss" for m in data["metrics"])
    assert any(p["key"] == "lr" and p["value"] == "0.01" for p in data["params"])
    assert any(t["key"] == "team" and t["value"] == "vision" for t in data["tags"])

    # Metric history.
    hist = c.get_metric_history(run.id, "loss")
    assert len(hist) == 3
    assert [m["step"] for m in hist] == [0, 1, 2]


def test_run_failed_on_exception(server: str) -> None:
    c = Client(server)
    eid = c.create_experiment(f"fail-{time.time_ns()}")
    with pytest.raises(RuntimeError):
        with c.start_run(eid) as run:
            run.log_param("a", "1")
            raise RuntimeError("boom")
    fetched = c.get_run(run.id)
    assert fetched["info"]["status"] == "FAILED"


def test_search_runs_with_filter(server: str) -> None:
    c = Client(server)
    eid = c.create_experiment(f"search-{time.time_ns()}")
    for i, lr in enumerate(["0.01", "0.05", "0.1"]):
        with c.start_run(eid, name=f"r-{i}") as run:
            run.log_param("lr", lr)
            run.log_metric("acc", 0.5 + i * 0.1, step=0)

    all_runs = c.search_runs([eid])
    assert len(all_runs) == 3

    only_lr_05 = c.search_runs([eid], filter="params.lr = '0.05'")
    assert len(only_lr_05) == 1

    high_acc = c.search_runs([eid], filter="metrics.acc > 0.65")
    assert len(high_acc) == 1


def test_log_batch(server: str) -> None:
    c = Client(server)
    eid = c.create_experiment(f"batch-{time.time_ns()}")
    run = c.create_run(eid, name="batch-run")
    ts = int(time.time() * 1000)
    metrics = [{"key": "loss", "value": 0.5 - i * 0.05, "timestamp": ts + i, "step": i} for i in range(20)]
    params = [{"key": f"p{i}", "value": str(i)} for i in range(10)]
    c.log_batch(run.id, metrics=metrics, params=params)
    hist = c.get_metric_history(run.id, "loss")
    assert len(hist) == 20


def test_param_immutability(server: str) -> None:
    c = Client(server)
    eid = c.create_experiment(f"params-{time.time_ns()}")
    run = c.create_run(eid)
    c.log_param(run.id, "lr", "0.01")
    # Same value: idempotent.
    c.log_param(run.id, "lr", "0.01")
    # Different value: rejected.
    with pytest.raises(LiteMLflowError) as ei:
        c.log_param(run.id, "lr", "0.5")
    assert ei.value.code == "RESOURCE_ALREADY_EXISTS"


def test_traces(server: str) -> None:
    c = Client(server)
    eid = c.create_experiment(f"trace-{time.time_ns()}")
    run = c.create_run(eid)
    trace_id = c.start_trace()
    parent_id = c.log_span(trace_id, "rag.pipeline", run_id=run.id, attrs={"k": 5})
    c.log_span(trace_id, "rag.retrieve", run_id=run.id, parent_id=parent_id, attrs={"top_k": 5})
    c.log_span(trace_id, "rag.generate", run_id=run.id, parent_id=parent_id, attrs={"model": "gpt-4o"})
    spans = c.get_run_traces(run.id)
    assert len(spans) == 3


def test_prompts(server: str) -> None:
    c = Client(server)
    name = f"sys-{time.time_ns()}"
    v1 = c.create_prompt(name, "v1 content")
    assert v1 == 1
    v2 = c.create_prompt(name, "v2 content")
    assert v2 == 2
    v1_again = c.create_prompt(name, "v1 content")
    assert v1_again == 1  # content-addressed reuse

    versions = c.list_prompt_versions(name)
    assert {p["version"] for p in versions} == {1, 2}

    c.set_prompt_alias(name, "production", v2)
    p = c.get_prompt_by_alias(name, "production")
    assert p["version"] == 2


def test_evals(server: str) -> None:
    c = Client(server)
    eid = c.create_experiment(f"evals-{time.time_ns()}")
    a = c.create_run(eid, name="model-a")
    b = c.create_run(eid, name="model-b")
    eval_run = c.create_run(eid, name="eval")
    e = c.create_eval(eval_run.id, [a.id, b.id], dataset_ref="hf://x", score=0.71, metrics={"f1": 0.71})
    assert e["score"] == 0.71
    fetched = c.get_eval(eval_run.id)
    assert sorted(fetched["target_run_ids"]) == sorted([a.id, b.id])


def test_artifact_round_trip(server: str) -> None:
    """Artifacts via direct REST since the SDK doesn't wrap them yet."""
    import io

    import requests

    c = Client(server)
    eid = c.create_experiment(f"art-{time.time_ns()}")
    run = c.create_run(eid)
    payload = b"the quick brown fox" * 100
    upload = requests.put(f"{server}/api/2.0/mlflow-artifacts/artifacts/{run.id}/output.bin", data=payload)
    upload.raise_for_status()

    listing = requests.get(f"{server}/api/2.0/mlflow/artifacts/list?run_id={run.id}").json()
    assert any(f["path"] == "output.bin" for f in listing["files"])

    download = requests.get(f"{server}/api/2.0/mlflow-artifacts/artifacts/{run.id}/output.bin")
    assert download.content == payload
