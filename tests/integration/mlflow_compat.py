#!/usr/bin/env python3
"""Run the official MLflow Python client against a live LiteMLflow server.

This is the canonical compatibility test: if it passes, an existing MLflow
user can swap their tracking URI to LiteMLflow with no code changes.

Usage:
    python tests/integration/mlflow_compat.py [--keep] [--addr 127.0.0.1:5232]

Exit code: 0 on full pass, 1 on first failure.
"""

from __future__ import annotations

import argparse
import os
import socket
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from typing import Any

import requests


HERE = Path(__file__).resolve().parent
PROJECT = HERE.parents[1]
BINARY = PROJECT / "bin" / "litemlflow"


def _free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _start_server(data_dir: Path, port: int) -> subprocess.Popen[bytes]:
    if not BINARY.exists():
        raise SystemExit(f"binary not found: {BINARY}; run `make build` first")
    proc = subprocess.Popen(
        [str(BINARY), "up", "--data", str(data_dir), "--addr", f"127.0.0.1:{port}"],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    url = f"http://127.0.0.1:{port}"
    for _ in range(50):
        try:
            if requests.get(url + "/healthz", timeout=0.5).ok:
                return proc
        except requests.RequestException:
            pass
        time.sleep(0.1)
    proc.terminate()
    raise RuntimeError("server failed to become healthy")


def _stop_server(proc: subprocess.Popen[bytes]) -> None:
    proc.terminate()
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()


def _run_compat(url: str) -> None:
    """Drive MLflow's Python client against LiteMLflow. Raises on failure."""
    import mlflow
    from mlflow.tracking import MlflowClient

    mlflow.set_tracking_uri(url)
    client = MlflowClient(tracking_uri=url)

    # 1. Create experiment.
    name = f"compat-{int(time.time() * 1000)}"
    eid = mlflow.create_experiment(name)
    print(f"[ok]  create_experiment -> {eid}")

    # 2. Get by name.
    exp = client.get_experiment_by_name(name)
    assert exp is not None, "experiment lookup by name returned None"
    assert exp.name == name
    print(f"[ok]  get_experiment_by_name -> {exp.experiment_id}")

    # 3. Start a run, log metrics/params/tags.
    with mlflow.start_run(experiment_id=eid, run_name="compat-run") as active:
        run_id = active.info.run_id
        mlflow.log_param("lr", 0.01)
        mlflow.log_param("optimizer", "adam")
        mlflow.set_tag("team", "vision")
        for step, loss in enumerate([0.9, 0.7, 0.5, 0.4, 0.3]):
            mlflow.log_metric("loss", loss, step=step)
            mlflow.log_metric("acc", 0.5 + step * 0.05, step=step)
    print(f"[ok]  start_run + log_param + log_metric -> run_id={run_id}")

    # 4. Get the run back.
    run = client.get_run(run_id)
    assert run.info.status == "FINISHED"
    assert run.data.params["lr"] == "0.01"
    assert "team" in run.data.tags
    metrics = {m.key: m.value for m in client.get_metric_history(run_id, "loss")}
    assert isinstance(metrics, dict)
    history = client.get_metric_history(run_id, "loss")
    assert len(history) == 5
    assert [m.step for m in history] == [0, 1, 2, 3, 4]
    print(f"[ok]  get_run + get_metric_history (history len={len(history)})")

    # 5. Log batch (under the hood many MLflow flows use this).
    from mlflow.entities import Metric, Param, RunTag

    batch_run = client.create_run(eid, run_name="batch")
    now = int(time.time() * 1000)
    client.log_batch(
        batch_run.info.run_id,
        metrics=[Metric("m", float(i), now + i, i) for i in range(50)],
        params=[Param(f"p{i}", str(i)) for i in range(20)],
        tags=[RunTag("phase", "train")],
    )
    print(f"[ok]  log_batch (50 metrics + 20 params + 1 tag)")

    hist = client.get_metric_history(batch_run.info.run_id, "m")
    assert len(hist) == 50
    print(f"[ok]  metric_history after batch len={len(hist)}")

    # 6. Search runs.
    found = client.search_runs([str(eid)], filter_string="params.lr = '0.01'")
    assert len(found) >= 1
    print(f"[ok]  search_runs filter='params.lr = 0.01' -> {len(found)} run(s)")

    # 7. Artifact upload + listing + download.
    artifact_dir = tempfile.mkdtemp(prefix="lmf-art-")
    artifact_path = Path(artifact_dir) / "model.bin"
    artifact_path.write_bytes(b"weights" * 1000)
    with mlflow.start_run(experiment_id=eid, run_name="artifact-run") as active:
        mlflow.log_artifact(str(artifact_path))
    art_run_id = active.info.run_id
    files = client.list_artifacts(art_run_id)
    assert any(f.path == "model.bin" for f in files)
    print(f"[ok]  log_artifact + list_artifacts ({len(files)} files)")

    # 8. Update + delete + restore experiment.
    new_name = name + "-renamed"
    client.rename_experiment(eid, new_name)
    e2 = client.get_experiment(eid)
    assert e2.name == new_name
    print(f"[ok]  rename_experiment")

    client.delete_experiment(eid)
    e3 = client.get_experiment(eid)
    assert e3.lifecycle_stage == "deleted"
    client.restore_experiment(eid)
    e4 = client.get_experiment(eid)
    assert e4.lifecycle_stage == "active"
    print(f"[ok]  delete + restore experiment")

    # 9. Metric history again on a deleted-then-restored run.
    client.delete_run(run_id)
    client.restore_run(run_id)
    print(f"[ok]  delete + restore run")

    print("\nAll MLflow compat checks passed.")


def _run_extended_compat(url: str) -> None:
    """Extended MLflow compat checks covering new features. Raises on failure."""
    import mlflow
    from mlflow.tracking import MlflowClient
    from mlflow.entities import Dataset, DatasetInput, InputTag, Metric, Param, RunTag

    mlflow.set_tracking_uri(url)
    client = MlflowClient(tracking_uri=url)

    # ---- 1. set_experiment auto-create flow ----
    exp_name = f"set-exp-test-{int(time.time() * 1000)}"
    # First set: experiment doesn't exist => create.
    exp1 = mlflow.set_experiment(exp_name)
    assert exp1 is not None, "set_experiment should return an Experiment"
    # Second set: experiment exists => just return it (no error).
    exp2 = mlflow.set_experiment(exp_name)
    assert exp2.experiment_id == exp1.experiment_id, "re-set should return same experiment"
    eid = exp1.experiment_id
    print(f"[ok]  set_experiment auto-create flow -> eid={eid}")

    # ---- 2. Run-name non-uniqueness ----
    r1 = client.create_run(eid, run_name="shared-name")
    r2 = client.create_run(eid, run_name="shared-name")
    assert r1.info.run_id != r2.info.run_id, "run IDs must differ"
    found = client.search_runs([str(eid)], filter_string="attributes.run_name = 'shared-name'")
    assert len(found) == 2, f"both runs with same name should be found, got {len(found)}"
    print(f"[ok]  two runs with same name in same experiment -> both found")

    # ---- 3. log_inputs (datasets) ----
    run_with_ds = client.create_run(eid, run_name="dataset-run")
    ds_run_id = run_with_ds.info.run_id
    client.log_inputs(
        run_id=ds_run_id,
        datasets=[
            DatasetInput(
                dataset=Dataset(
                    name="wikipedia-2024-q1",
                    digest="abc123",
                    source_type="http",
                    source="https://example.com/wiki",
                    schema='{"columns":["text","label"]}',
                    profile='{"num_rows":50000}',
                ),
                tags=[InputTag(key="split", value="train")],
            )
        ],
    )
    # Verify get_run returns the dataset in inputs.
    run_data = client.get_run(ds_run_id)
    inputs = run_data.inputs
    assert inputs is not None, "run.inputs should not be None after log_inputs"
    ds_inputs = inputs.dataset_inputs
    assert len(ds_inputs) == 1, f"expected 1 dataset input, got {len(ds_inputs)}"
    di = ds_inputs[0]
    assert di.dataset.name == "wikipedia-2024-q1", f"unexpected name: {di.dataset.name}"
    assert di.dataset.digest == "abc123", f"unexpected digest: {di.dataset.digest}"
    assert di.dataset.source_type == "http", f"unexpected source_type: {di.dataset.source_type}"
    assert len(di.tags) == 1 and di.tags[0].key == "split" and di.tags[0].value == "train"
    print(f"[ok]  log_inputs + get_run includes dataset (name={di.dataset.name})")

    # ---- 4. search_runs with attributes.run_id IN (...) ----
    search_exp_name = f"search-in-{int(time.time() * 1000)}"
    search_exp = mlflow.set_experiment(search_exp_name)
    s_eid = search_exp.experiment_id
    s_r1 = client.create_run(s_eid)
    s_r2 = client.create_run(s_eid)
    s_r3 = client.create_run(s_eid)  # not in the filter
    filter_str = f"attributes.run_id IN ('{s_r1.info.run_id}','{s_r2.info.run_id}')"
    found = client.search_runs([str(s_eid)], filter_string=filter_str)
    assert len(found) == 2, f"IN filter should return 2 runs, got {len(found)}"
    found_ids = {r.info.run_id for r in found}
    assert s_r1.info.run_id in found_ids and s_r2.info.run_id in found_ids
    assert s_r3.info.run_id not in found_ids
    print(f"[ok]  search_runs with attributes.run_id IN (...) -> {len(found)} runs")

    # ---- 5. get-history pagination via raw HTTP ----
    hist_run = client.create_run(eid, run_name="hist-pag")
    hist_run_id = hist_run.info.run_id
    now = int(time.time() * 1000)
    client.log_batch(
        hist_run_id,
        metrics=[Metric("pag_loss", float(i), now + i, i) for i in range(25)],
    )
    # Raw HTTP call with max_results=10.
    resp = requests.get(
        url + "/api/2.0/mlflow/metrics/get-history",
        params={"run_id": hist_run_id, "metric_key": "pag_loss", "max_results": 10},
    )
    assert resp.status_code == 200, f"get-history failed: {resp.text}"
    body = resp.json()
    assert "metrics" in body, f"missing 'metrics' key: {body}"
    assert len(body["metrics"]) == 10, f"expected 10 metrics, got {len(body['metrics'])}"
    assert "next_page_token" in body and body["next_page_token"], "expected next_page_token"
    page_token = body["next_page_token"]

    # Fetch next page.
    resp2 = requests.get(
        url + "/api/2.0/mlflow/metrics/get-history",
        params={"run_id": hist_run_id, "metric_key": "pag_loss", "max_results": 10, "page_token": page_token},
    )
    assert resp2.status_code == 200, f"page2 failed: {resp2.text}"
    body2 = resp2.json()
    assert len(body2["metrics"]) == 10, f"expected 10 metrics on page2, got {len(body2['metrics'])}"
    print(f"[ok]  get-history pagination (25 points, 10/page => token={page_token!r})")

    # ---- 6. log_batch exactly 1000 metrics (boundary) ----
    batch_run = client.create_run(eid, run_name="batch-1000")
    batch_run_id = batch_run.info.run_id
    now2 = int(time.time() * 1000)
    client.log_batch(
        batch_run_id,
        metrics=[Metric("x", float(i), now2 + i, i) for i in range(1000)],
    )
    hist = client.get_metric_history(batch_run_id, "x")
    assert len(hist) == 1000, f"expected 1000 metrics after batch, got {len(hist)}"
    print(f"[ok]  log_batch with exactly 1000 metrics succeeds (len={len(hist)})")

    # ---- 7. log_batch with 1001 metrics must fail ----
    batch_run_fail = client.create_run(eid, run_name="batch-1001")
    batch_run_fail_id = batch_run_fail.info.run_id
    resp_fail = requests.post(
        url + "/api/2.0/mlflow/runs/log-batch",
        json={
            "run_id": batch_run_fail_id,
            "metrics": [
                {"key": "y", "value": float(i), "timestamp": now2 + i, "step": i}
                for i in range(1001)
            ],
        },
    )
    assert resp_fail.status_code == 400, f"1001 metrics should fail with 400, got {resp_fail.status_code}"
    err_body = resp_fail.json()
    assert err_body.get("error_code") == "INVALID_PARAMETER_VALUE", f"wrong error_code: {err_body}"
    print(f"[ok]  log_batch with 1001 metrics fails with INVALID_PARAMETER_VALUE")

    # ---- 8. search-experiments view_type via query string ----
    vt_exp_name = f"viewtype-del-{int(time.time() * 1000)}"
    vt_eid = client.create_experiment(vt_exp_name)
    client.delete_experiment(vt_eid)
    # Query string: view_type=DELETED_ONLY should return the deleted experiment.
    resp_vt = requests.get(
        url + "/api/2.0/mlflow/experiments/search",
        params={"view_type": "DELETED_ONLY", "max_results": 50},
    )
    assert resp_vt.status_code == 200, f"search deleted: {resp_vt.text}"
    vt_body = resp_vt.json()
    deleted_ids = {e["experiment_id"] for e in vt_body.get("experiments", [])}
    assert str(vt_eid) in deleted_ids, f"deleted experiment {vt_eid} not found in DELETED_ONLY results"
    print(f"[ok]  search-experiments?view_type=DELETED_ONLY returns deleted experiment")

    print("\nAll extended MLflow compat checks passed.")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--keep", action="store_true", help="keep server running after test")
    parser.add_argument("--addr", help="reuse an existing server at host:port (skip spawn)")
    args = parser.parse_args()

    if args.addr:
        # Accept either a bare host[:port] or a full URL. If no scheme,
        # default to http for localhost and https otherwise so the live
        # smoke test can use --addr lmf.gorev.space without surprises
        # (HTTP→HTTPS redirects break the MLflow client's POSTs).
        if "://" in args.addr:
            url = args.addr
        elif args.addr.startswith("127.") or args.addr.startswith("localhost"):
            url = "http://" + args.addr
        else:
            url = "https://" + args.addr
        try:
            _run_compat(url)
            _run_extended_compat(url)
        except Exception as exc:
            print(f"\n[FAIL] {exc}", file=sys.stderr)
            return 1
        return 0

    port = _free_port()
    with tempfile.TemporaryDirectory(prefix="litemlflow-compat-") as tmp:
        data = Path(tmp)
        proc = _start_server(data, port)
        url = f"http://127.0.0.1:{port}"
        try:
            _run_compat(url)
            _run_extended_compat(url)
        except Exception as exc:
            print(f"\n[FAIL] {exc}", file=sys.stderr)
            return 1
        finally:
            if not args.keep:
                _stop_server(proc)
            else:
                print(f"\n[KEEP] server running at {url}, data dir: {data}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
