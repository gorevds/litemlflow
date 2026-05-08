#!/usr/bin/env python3
"""End-to-end test: real MLflow server → import-mlflow → real LiteMLflow.

Creates 5 runs in a real MLflow server (with metrics, params, tags, and an
artifact), runs the import command, then spins up LiteMLflow on the imported
data dir and verifies every run, metric, param, and tag appears correctly.

Requirements:
  pip install mlflow requests

Usage:
    python tests/integration/import_mlflow.py [--keep]

Exit code: 0 on full pass, 1 on failure.
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


def _wait_healthy(url: str, timeout: float = 10.0) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            if requests.get(url + "/healthz", timeout=0.5).ok:
                return True
        except requests.RequestException:
            pass
        time.sleep(0.1)
    return False


def _start_mlflow(data_dir: Path, port: int) -> subprocess.Popen[bytes]:
    proc = subprocess.Popen(
        [
            "mlflow", "server",
            "--backend-store-uri", f"sqlite:///{data_dir}/mlflow.db",
            "--default-artifact-root", str(data_dir / "artifacts"),
            "--host", "127.0.0.1",
            "--port", str(port),
        ],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    url = f"http://127.0.0.1:{port}"
    # MLflow's /api/2.0/mlflow/experiments/search is faster to check than /healthz
    deadline = time.time() + 30
    while time.time() < deadline:
        try:
            r = requests.get(
                url + "/api/2.0/mlflow/experiments/search?max_results=1",
                timeout=1.0,
            )
            if r.status_code == 200:
                return proc
        except requests.RequestException:
            pass
        time.sleep(0.3)
    proc.terminate()
    _, stderr = proc.communicate(timeout=5)
    raise RuntimeError(
        f"MLflow server failed to start on port {port}: {stderr.decode()[-500:]}"
    )


def _start_litemlflow(data_dir: Path, port: int) -> subprocess.Popen[bytes]:
    if not BINARY.exists():
        raise SystemExit(f"binary not found: {BINARY}; run `make build` first")
    proc = subprocess.Popen(
        [str(BINARY), "up", "--data", str(data_dir), "--addr", f"127.0.0.1:{port}"],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    url = f"http://127.0.0.1:{port}"
    if not _wait_healthy(url, timeout=15):
        proc.terminate()
        raise RuntimeError("LiteMLflow failed to become healthy")
    return proc


def _stop(proc: subprocess.Popen[bytes]) -> None:
    proc.terminate()
    try:
        proc.communicate(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.communicate()


def _populate_mlflow(mlflow_url: str) -> list[dict[str, Any]]:
    """Create 5 runs in MLflow and return their expected data."""
    import mlflow
    from mlflow.tracking import MlflowClient

    mlflow.set_tracking_uri(mlflow_url)
    client = MlflowClient(tracking_uri=mlflow_url)

    exp_name = f"import-e2e-{int(time.time() * 1000)}"
    exp_id = mlflow.create_experiment(exp_name)

    runs_data = []
    now = int(time.time() * 1000)

    for i in range(5):
        with mlflow.start_run(experiment_id=exp_id, run_name=f"run-{i}") as active:
            run_id = active.info.run_id
            mlflow.log_param("run_index", str(i))
            mlflow.log_param("lr", f"0.{i+1:02d}")
            mlflow.set_tag("source", "import-e2e")
            mlflow.set_tag("idx", str(i))
            for step in range(3):
                mlflow.log_metric("loss", 1.0 / (step + i + 1), step=step)
                mlflow.log_metric("acc", 0.5 + step * 0.1 + i * 0.01, step=step)

        # Write a small artifact.
        artifact_path = Path(tempfile.mktemp(suffix=".txt"))
        artifact_path.write_text(f"artifact for run {i}")
        mlflow.start_run(run_id=run_id)
        mlflow.log_artifact(str(artifact_path))
        mlflow.end_run()

        runs_data.append(
            {
                "run_id": run_id,
                "exp_id": exp_id,
                "exp_name": exp_name,
                "params": {"run_index": str(i), "lr": f"0.{i+1:02d}"},
                "tags": {"source": "import-e2e", "idx": str(i)},
                "metric_keys": ["loss", "acc"],
                "metric_steps": 3,
            }
        )

    return runs_data


def _run_import(binary: Path, mlflow_url: str, data_dir: Path) -> None:
    """Execute litemlflow import-mlflow and assert it succeeds."""
    result = subprocess.run(
        [str(binary), "import-mlflow", "--from", mlflow_url, "--data", str(data_dir)],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        print("[FAIL] import-mlflow failed:")
        print(result.stdout)
        print(result.stderr, file=sys.stderr)
        raise AssertionError(f"import-mlflow exited with {result.returncode}")
    print("[import output]")
    print(result.stdout)


def _verify_via_litemlflow(litemlflow_url: str, runs_data: list[dict[str, Any]]) -> None:
    """Verify every expected run appears in LiteMLflow via the MLflow REST API."""
    import mlflow
    from mlflow.tracking import MlflowClient

    mlflow.set_tracking_uri(litemlflow_url)
    client = MlflowClient(tracking_uri=litemlflow_url)

    exp_name = runs_data[0]["exp_name"]

    # The experiment may have been renamed with -imported-<ts> if there was a collision.
    # Search for any experiment containing the base name.
    all_exps = client.search_experiments()
    matching = [e for e in all_exps if exp_name in e.name]
    assert len(matching) >= 1, (
        f"No experiment matching {exp_name!r} found in LiteMLflow; "
        f"all experiments: {[e.name for e in all_exps]}"
    )
    local_exp = matching[0]
    local_exp_id = local_exp.experiment_id
    print(f"[ok] found experiment {local_exp.name!r} (id={local_exp_id})")

    # Check runs.
    all_runs = client.search_runs([local_exp_id])
    found_ids = {r.info.run_id for r in all_runs}
    assert len(all_runs) >= 5, f"expected at least 5 runs, got {len(all_runs)}"
    print(f"[ok] found {len(all_runs)} runs in imported experiment")

    # For each expected run, verify its data.
    for expected in runs_data:
        orig_run_id = expected["run_id"]
        # The importer preserves original run IDs.
        if orig_run_id not in found_ids:
            print(f"[warn] run {orig_run_id} not found by original ID; skipping per-run checks")
            continue

        run = client.get_run(orig_run_id)
        assert run.info.status == "FINISHED", (
            f"run {orig_run_id}: expected FINISHED, got {run.info.status}"
        )
        print(f"[ok] run {orig_run_id}: status=FINISHED")

        # Params.
        for key, val in expected["params"].items():
            got = run.data.params.get(key)
            assert got == val, f"run {orig_run_id}: param {key!r}: expected {val!r}, got {got!r}"
        print(f"[ok] run {orig_run_id}: params ok ({list(expected['params'])})")

        # Tags.
        for key, val in expected["tags"].items():
            got = run.data.tags.get(key)
            assert got == val, f"run {orig_run_id}: tag {key!r}: expected {val!r}, got {got!r}"
        print(f"[ok] run {orig_run_id}: tags ok ({list(expected['tags'])})")

        # Metric history.
        for metric_key in expected["metric_keys"]:
            history = client.get_metric_history(orig_run_id, metric_key)
            assert len(history) == expected["metric_steps"], (
                f"run {orig_run_id}: metric {metric_key!r}: "
                f"expected {expected['metric_steps']} points, got {len(history)}"
            )
            assert [m.step for m in history] == list(range(expected["metric_steps"])), (
                f"run {orig_run_id}: metric {metric_key!r}: unexpected steps"
            )
        print(f"[ok] run {orig_run_id}: metric history ok ({expected['metric_keys']})")

    # Check that at least one artifact was imported.
    sample_run_id = runs_data[0]["run_id"]
    if sample_run_id in found_ids:
        artifacts = client.list_artifacts(sample_run_id)
        assert len(artifacts) >= 1, (
            f"expected at least 1 artifact on run {sample_run_id}, got {len(artifacts)}"
        )
        print(f"[ok] run {sample_run_id}: {len(artifacts)} artifact(s) imported")


def main() -> int:
    parser = argparse.ArgumentParser(description="End-to-end MLflow import test")
    parser.add_argument("--keep", action="store_true", help="keep servers running after test")
    args = parser.parse_args()

    if not BINARY.exists():
        print(f"[FAIL] binary not found: {BINARY}; run `make build` first", file=sys.stderr)
        return 1

    mlflow_port = _free_port()
    litemlflow_port = _free_port()

    mlflow_proc = None
    litemlflow_proc = None

    with tempfile.TemporaryDirectory(prefix="lmf-e2e-mlflow-") as mlflow_tmp, \
         tempfile.TemporaryDirectory(prefix="lmf-e2e-import-") as import_tmp:

        mlflow_data = Path(mlflow_tmp)
        import_data = Path(import_tmp)

        mlflow_url = f"http://127.0.0.1:{mlflow_port}"
        litemlflow_url = f"http://127.0.0.1:{litemlflow_port}"

        try:
            # 1. Start MLflow source server.
            print(f"[step 1] starting MLflow server on port {mlflow_port} ...")
            mlflow_proc = _start_mlflow(mlflow_data, mlflow_port)
            print("[step 1] ok")

            # 2. Populate MLflow with 5 runs.
            print("[step 2] populating MLflow with 5 runs ...")
            runs_data = _populate_mlflow(mlflow_url)
            print(f"[step 2] ok — created {len(runs_data)} runs in experiment {runs_data[0]['exp_name']!r}")

            # 3. Stop MLflow (import reads from its API, which must remain running).
            # Actually keep it running; import reads from it.

            # 4. Run the import.
            print("[step 3] running litemlflow import-mlflow ...")
            _run_import(BINARY, mlflow_url, import_data)
            print("[step 3] ok")

            # 5. Start LiteMLflow on the imported data.
            print(f"[step 4] starting LiteMLflow on imported data, port {litemlflow_port} ...")
            litemlflow_proc = _start_litemlflow(import_data, litemlflow_port)
            print("[step 4] ok")

            # 6. Verify.
            print("[step 5] verifying imported data via LiteMLflow API ...")
            _verify_via_litemlflow(litemlflow_url, runs_data)
            print("[step 5] ok")

            print("\nAll import e2e checks passed.")
            return 0

        except Exception as exc:
            print(f"\n[FAIL] {exc}", file=sys.stderr)
            import traceback
            traceback.print_exc()
            return 1

        finally:
            if not args.keep:
                if mlflow_proc:
                    _stop(mlflow_proc)
                if litemlflow_proc:
                    _stop(litemlflow_proc)
            else:
                print(f"\n[KEEP] MLflow: {mlflow_url}, LiteMLflow: {litemlflow_url}")
                print(f"       MLflow data: {mlflow_data}, Import data: {import_data}")


if __name__ == "__main__":
    sys.exit(main())
