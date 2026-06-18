"""Unit tests for workspace scoping on the client (no live server needed).

Guards independent-review P2: the server isolates experiments/runs/registry per
workspace via the X-Workspace header, but the SDK had no way to set it. These
assert the header is wired from the explicit arg and the LITEMLFLOW_WORKSPACE
env var, with the explicit arg taking precedence.
"""

from __future__ import annotations

from litemlflow import Client


def test_workspace_arg_sets_header() -> None:
    c = Client("http://localhost:5000", workspace="team-a")
    assert c.workspace == "team-a"
    assert c._session.headers.get("X-Workspace") == "team-a"


def test_no_workspace_omits_header(monkeypatch) -> None:
    monkeypatch.delenv("LITEMLFLOW_WORKSPACE", raising=False)
    c = Client("http://localhost:5000")
    assert c.workspace is None
    assert "X-Workspace" not in c._session.headers


def test_workspace_from_env(monkeypatch) -> None:
    monkeypatch.setenv("LITEMLFLOW_WORKSPACE", "team-env")
    c = Client("http://localhost:5000")
    assert c.workspace == "team-env"
    assert c._session.headers.get("X-Workspace") == "team-env"


def test_explicit_workspace_overrides_env(monkeypatch) -> None:
    monkeypatch.setenv("LITEMLFLOW_WORKSPACE", "team-env")
    c = Client("http://localhost:5000", workspace="team-explicit")
    assert c._session.headers.get("X-Workspace") == "team-explicit"
