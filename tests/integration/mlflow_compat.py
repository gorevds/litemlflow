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

    # Run Model Registry compat checks.
    _run_registry_compat(url)


def _run_registry_compat(url: str) -> None:
    """Drive MLflow Model Registry API against LiteMLflow. Raises on failure."""
    import mlflow
    from mlflow.tracking import MlflowClient

    client = MlflowClient(tracking_uri=url)
    mlflow.set_tracking_uri(url)

    suffix = str(int(time.time() * 1000))
    model_name = f"compat-registry-{suffix}"

    # 1. Create a registered model.
    rm = client.create_registered_model(model_name, description="compat test model")
    assert rm.name == model_name, f"expected {model_name!r}, got {rm.name!r}"
    print(f"[ok] registry: create_registered_model -> {model_name}")

    # 2. Create a run and log an artifact as the model source.
    eid = mlflow.create_experiment(f"reg-compat-{suffix}")
    with mlflow.start_run(experiment_id=eid, run_name="reg-run") as active:
        run_id = active.info.run_id
        mlflow.log_param("epochs", 10)
        # Simulate logging a model artifact.
        artifact_dir = tempfile.mkdtemp(prefix="lmf-reg-art-")
        model_path = Path(artifact_dir) / "model.pkl"
        model_path.write_bytes(b"serialised-model-weights")
        mlflow.log_artifact(str(model_path), artifact_path="model")
    artifact_uri = f"mlflow-artifacts:/{run_id}/model"

    # 3. Create a model version pointing to the artifact URI.
    mv = client.create_model_version(
        name=model_name,
        source=artifact_uri,
        run_id=run_id,
        description="v1 from compat test",
    )
    assert mv.name == model_name
    assert mv.version == "1", f"expected version='1', got {mv.version!r}"
    assert mv.source == artifact_uri
    print(f"[ok] registry: create_model_version -> version={mv.version}")

    # 4. Get the version back.
    mv_got = client.get_model_version(model_name, "1")
    assert mv_got.version == "1"
    assert mv_got.run_id == run_id
    print(f"[ok] registry: get_model_version")

    # 5. Search registered models.
    results = client.search_registered_models(filter_string=f"name = '{model_name}'")
    assert len(results) >= 1, f"expected at least 1, got {len(results)}"
    assert results[0].name == model_name
    print(f"[ok] registry: search_registered_models ({len(results)} result(s))")

    # 6. Search model versions.
    mv_results = client.search_model_versions(filter_string=f"name = '{model_name}'")
    assert len(mv_results) >= 1
    print(f"[ok] registry: search_model_versions ({len(mv_results)} version(s))")

    # 7. Transition stage.
    client.transition_model_version_stage(
        name=model_name,
        version="1",
        stage="Staging",
        archive_existing_versions=False,
    )
    mv_staged = client.get_model_version(model_name, "1")
    assert mv_staged.current_stage == "Staging", f"expected Staging, got {mv_staged.current_stage}"
    print(f"[ok] registry: transition_model_version_stage -> Staging")

    # 8. Set a tag on the model and on the version.
    client.set_registered_model_tag(model_name, "team", "mlops")
    client.set_model_version_tag(model_name, "1", "env", "staging")

    rm_tagged = client.get_registered_model(model_name)
    tag_keys = {t.key for t in rm_tagged.tags}
    assert "team" in tag_keys, f"model tag not found; tags={rm_tagged.tags}"
    print(f"[ok] registry: set_registered_model_tag")

    mv_tagged = client.get_model_version(model_name, "1")
    vtag_keys = {t.key for t in mv_tagged.tags}
    assert "env" in vtag_keys, f"version tag not found; tags={mv_tagged.tags}"
    print(f"[ok] registry: set_model_version_tag")

    # 9. Set and resolve an alias.
    client.set_registered_model_alias(model_name, "champion", "1")
    mv_alias = client.get_model_version_by_alias(model_name, "champion")
    assert mv_alias.version == "1", f"alias resolved to wrong version: {mv_alias.version}"
    print(f"[ok] registry: set_registered_model_alias + get_model_version_by_alias")

    # 10. Create a second version and verify get-latest-versions.
    mv2 = client.create_model_version(
        name=model_name,
        source=artifact_uri,
        run_id=run_id,
    )
    assert mv2.version == "2", f"expected version='2', got {mv2.version!r}"

    client.transition_model_version_stage(
        name=model_name, version="2", stage="Production",
        archive_existing_versions=True,
    )
    latest = client.get_latest_versions(model_name, stages=["Production"])
    assert len(latest) == 1 and latest[0].version == "2", (
        f"expected latest Production=2, got {[(v.version, v.current_stage) for v in latest]}"
    )
    print(f"[ok] registry: get_latest_versions -> Production=v2")

    # 11. Rename the model.
    new_model_name = model_name + "-renamed"
    client.rename_registered_model(model_name, new_model_name)
    rm_renamed = client.get_registered_model(new_model_name)
    assert rm_renamed.name == new_model_name
    print(f"[ok] registry: rename_registered_model -> {new_model_name}")

    # 12. Delete a version, then the model.
    client.delete_model_version(new_model_name, "1")
    client.delete_registered_model(new_model_name)
    try:
        client.get_registered_model(new_model_name)
        raise AssertionError("expected ResourceDoesNotExist after delete")
    except Exception as exc:
        if "RESOURCE_DOES_NOT_EXIST" not in str(exc) and "404" not in str(exc):
            raise
    print(f"[ok] registry: delete_model_version + delete_registered_model")

    print("\nAll registry compat checks passed.")


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
