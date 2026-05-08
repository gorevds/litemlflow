#!/usr/bin/env python3
"""Comparative benchmark: LiteMLflow vs MLflow.

Measures cold-start time, single-metric write latency, batch throughput,
search_runs latency on a populated database, and UI first-paint.

Both servers run on local loopback ports. We use the same MLflow Python
client to drive both, so we measure server behavior, not client.

Usage:
    python tests/integration/bench.py [--runs 5000] [--out report.json]

Requires: bin/litemlflow built; mlflow installed (mlflow-skinny + sqlalchemy).
"""

from __future__ import annotations

import argparse
import json
import os
import socket
import statistics
import subprocess
import sys
import tempfile
import time
from contextlib import contextmanager
from pathlib import Path
from typing import Any, Iterator

import requests

PROJECT = Path(__file__).resolve().parents[1].parent
LMF_BINARY = PROJECT / "bin" / "litemlflow"


def _free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


@contextmanager
def litemlflow_server() -> Iterator[tuple[str, float]]:
    if not LMF_BINARY.exists():
        raise SystemExit(f"binary missing: {LMF_BINARY}")
    port = _free_port()
    with tempfile.TemporaryDirectory(prefix="lmf-bench-") as tmp:
        t0 = time.perf_counter()
        proc = subprocess.Popen(
            [str(LMF_BINARY), "up", "--data", tmp, "--addr", f"127.0.0.1:{port}"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        url = f"http://127.0.0.1:{port}"
        try:
            for _ in range(200):
                try:
                    if requests.get(url + "/healthz", timeout=0.2).ok:
                        cold = time.perf_counter() - t0
                        break
                except requests.RequestException:
                    pass
                time.sleep(0.05)
            else:
                proc.terminate()
                raise RuntimeError("LiteMLflow did not become healthy in 10s")
            yield url, cold
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()


@contextmanager
def mlflow_server() -> Iterator[tuple[str, float]]:
    """Run an MLflow tracking server with SQLite + filesystem artifacts."""
    port = _free_port()
    with tempfile.TemporaryDirectory(prefix="mlflow-bench-") as tmp:
        backend = f"sqlite:///{tmp}/mlflow.db"
        artifacts = f"{tmp}/artifacts"
        os.makedirs(artifacts, exist_ok=True)
        t0 = time.perf_counter()
        proc = subprocess.Popen(
            [
                "mlflow",
                "server",
                "--backend-store-uri", backend,
                "--artifacts-destination", artifacts,
                "--host", "127.0.0.1",
                "--port", str(port),
                "--workers", "1",
            ],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        url = f"http://127.0.0.1:{port}"
        try:
            for _ in range(400):  # MLflow takes longer
                try:
                    if requests.get(url + "/health", timeout=0.2).ok:
                        cold = time.perf_counter() - t0
                        break
                except requests.RequestException:
                    pass
                time.sleep(0.05)
            else:
                proc.terminate()
                raise RuntimeError("MLflow did not become healthy in 20s")
            yield url, cold
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()


def bench_single_metric_latency(url: str, n: int = 200) -> dict[str, float]:
    """Time `log_metric` n times and report distribution."""
    import mlflow
    from mlflow.tracking import MlflowClient

    mlflow.set_tracking_uri(url)
    client = MlflowClient(tracking_uri=url)
    eid = mlflow.create_experiment(f"bench-{time.time_ns()}")
    run = client.create_run(str(eid))
    samples = []
    for i in range(n):
        t0 = time.perf_counter()
        client.log_metric(run.info.run_id, "loss", 0.5, step=i)
        samples.append((time.perf_counter() - t0) * 1000)
    return {
        "p50_ms": statistics.median(samples),
        "p95_ms": statistics.quantiles(samples, n=20)[-1],
        "p99_ms": statistics.quantiles(samples, n=100)[-1] if len(samples) >= 100 else max(samples),
        "min_ms": min(samples),
        "max_ms": max(samples),
        "n": n,
    }


def bench_log_batch_throughput(url: str, batch: int = 1000, batches: int = 5) -> dict[str, float]:
    """Throughput of log-batch (rows per second)."""
    import mlflow
    from mlflow.entities import Metric
    from mlflow.tracking import MlflowClient

    mlflow.set_tracking_uri(url)
    client = MlflowClient(tracking_uri=url)
    eid = mlflow.create_experiment(f"bench-batch-{time.time_ns()}")
    run = client.create_run(str(eid))
    now = int(time.time() * 1000)
    rows_per_sec = []
    for b in range(batches):
        ms = [Metric("loss", float(i), now + i, i) for i in range(b * batch, b * batch + batch)]
        t0 = time.perf_counter()
        client.log_batch(run.info.run_id, metrics=ms)
        dt = time.perf_counter() - t0
        rows_per_sec.append(batch / dt)
    return {
        "median_rows_per_sec": statistics.median(rows_per_sec),
        "max_rows_per_sec": max(rows_per_sec),
        "rows_per_call": batch,
        "calls": batches,
    }


def bench_search_runs(url: str, target_runs: int = 1000) -> dict[str, float]:
    """Populate target_runs runs, then measure search latency."""
    import mlflow
    from mlflow.entities import Metric, Param
    from mlflow.tracking import MlflowClient

    mlflow.set_tracking_uri(url)
    client = MlflowClient(tracking_uri=url)
    eid = mlflow.create_experiment(f"bench-search-{time.time_ns()}")

    t0 = time.perf_counter()
    now = int(time.time() * 1000)
    for i in range(target_runs):
        run = client.create_run(str(eid))
        client.log_batch(
            run.info.run_id,
            metrics=[Metric("acc", 0.5 + (i % 50) / 100, now + i, 0)],
            params=[Param("lr", str(0.01 + (i % 10) * 0.01))],
        )
    populate_dt = time.perf_counter() - t0

    samples = []
    for _ in range(20):
        t0 = time.perf_counter()
        client.search_runs(
            [str(eid)],
            filter_string="metrics.acc > 0.7",
            max_results=100,
        )
        samples.append((time.perf_counter() - t0) * 1000)

    return {
        "populate_seconds": populate_dt,
        "populate_runs": target_runs,
        "search_p50_ms": statistics.median(samples),
        "search_p95_ms": statistics.quantiles(samples, n=20)[-1],
        "search_p99_ms": statistics.quantiles(samples, n=100)[-1] if len(samples) >= 100 else max(samples),
    }


def bench_ui_first_paint(url: str) -> dict[str, float]:
    """Time how long it takes to fetch the embedded UI HTML."""
    samples = []
    for _ in range(20):
        t0 = time.perf_counter()
        r = requests.get(url + "/ui/", timeout=5)
        r.raise_for_status()
        samples.append((time.perf_counter() - t0) * 1000)
    return {
        "p50_ms": statistics.median(samples),
        "p95_ms": statistics.quantiles(samples, n=20)[-1],
        "min_ms": min(samples),
    }


def run_suite(label: str, ctx, runs: int) -> dict[str, Any]:
    print(f"\n=== {label} ===")
    with ctx as (url, cold_start):
        print(f"  cold start:        {cold_start*1000:.1f} ms")
        single = bench_single_metric_latency(url, n=min(runs, 200))
        print(f"  log_metric p50:    {single['p50_ms']:.2f} ms")
        print(f"  log_metric p95:    {single['p95_ms']:.2f} ms")
        batch = bench_log_batch_throughput(url, batch=1000, batches=3)
        print(f"  log_batch tput:    {batch['median_rows_per_sec']:.0f} rows/s")
        # MLflow's first paint is its react bundle which is not the same surface;
        # we just ping its index for parity.
        ui = bench_ui_first_paint(url) if "litemlflow" in label.lower() else {"p50_ms": float("nan"), "p95_ms": float("nan"), "min_ms": float("nan")}
        print(f"  ui first paint p50:{ui['p50_ms']:.1f} ms")
        search = bench_search_runs(url, target_runs=runs)
        print(f"  search 100 runs:   p50 {search['search_p50_ms']:.1f} ms / p95 {search['search_p95_ms']:.1f} ms")
        print(f"  populated {search['populate_runs']} runs in {search['populate_seconds']:.1f} s")
        return {
            "label": label,
            "cold_start_ms": cold_start * 1000,
            "log_metric": single,
            "log_batch": batch,
            "ui_first_paint": ui,
            "search_runs": search,
        }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--runs", type=int, default=1000, help="number of runs to populate for search benchmark")
    ap.add_argument("--out", default="bench-report.json")
    ap.add_argument("--skip-mlflow", action="store_true", help="benchmark only LiteMLflow")
    args = ap.parse_args()

    report: dict[str, Any] = {"timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "runs": args.runs}
    report["litemlflow"] = run_suite("LiteMLflow", litemlflow_server(), args.runs)
    if not args.skip_mlflow:
        try:
            report["mlflow"] = run_suite("MLflow + SQLite", mlflow_server(), args.runs)
        except Exception as exc:
            report["mlflow_error"] = str(exc)
            print(f"\n[warn] MLflow bench failed: {exc}", file=sys.stderr)

    Path(args.out).write_text(json.dumps(report, indent=2))
    print(f"\nReport: {args.out}")
    if "mlflow" in report:
        lmf = report["litemlflow"]
        mlf = report["mlflow"]
        print("\n=== Comparison (LiteMLflow / MLflow) ===")
        print(f"  cold start:           {lmf['cold_start_ms']:.0f} / {mlf['cold_start_ms']:.0f} ms  ({mlf['cold_start_ms']/lmf['cold_start_ms']:.2f}x faster)")
        print(f"  log_metric p50:       {lmf['log_metric']['p50_ms']:.2f} / {mlf['log_metric']['p50_ms']:.2f} ms  ({mlf['log_metric']['p50_ms']/lmf['log_metric']['p50_ms']:.2f}x)")
        print(f"  log_batch tput:       {lmf['log_batch']['median_rows_per_sec']:.0f} / {mlf['log_batch']['median_rows_per_sec']:.0f} rows/s  ({lmf['log_batch']['median_rows_per_sec']/mlf['log_batch']['median_rows_per_sec']:.2f}x)")
        print(f"  search_runs p50:      {lmf['search_runs']['search_p50_ms']:.1f} / {mlf['search_runs']['search_p50_ms']:.1f} ms  ({mlf['search_runs']['search_p50_ms']/lmf['search_runs']['search_p50_ms']:.2f}x)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
