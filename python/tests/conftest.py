"""Pytest fixtures: spin up a real LiteMLflow server in a subprocess."""

from __future__ import annotations

import os
import socket
import subprocess
import tempfile
import time
from collections.abc import Iterator
from pathlib import Path

import pytest
import requests


def _free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _project_root() -> Path:
    return Path(__file__).resolve().parents[2]


def _binary_path() -> Path:
    bin_path = _project_root() / "bin" / "litemlflow"
    if not bin_path.exists():
        raise pytest.UsageError(
            f"binary not found at {bin_path}; build with: make build"
        )
    return bin_path


@pytest.fixture(scope="session")
def server() -> Iterator[str]:
    """Start a fresh server in a temp data dir; yield its base URL."""
    binary = _binary_path()
    port = _free_port()
    with tempfile.TemporaryDirectory(prefix="litemlflow-") as tmp:
        env = dict(os.environ)
        env["LITEMLFLOW_DEV"] = "1"
        proc = subprocess.Popen(
            [str(binary), "up", "--data", tmp, "--addr", f"127.0.0.1:{port}"],
            env=env,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        url = f"http://127.0.0.1:{port}"
        try:
            for _ in range(50):
                try:
                    if requests.get(url + "/healthz", timeout=0.5).ok:
                        break
                except requests.RequestException:
                    pass
                time.sleep(0.1)
            else:
                proc.terminate()
                proc.wait(timeout=5)
                raise RuntimeError("server did not become healthy")
            yield url
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
