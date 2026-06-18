"""Unit tests for client retry on transient failures (independent-review P2).

The client raised immediately on a connection error or a 5xx, so a brief server
restart or load spike during a training run lost the call. These assert it
retries transport errors and retryable status codes, gives up after max_retries,
and does not retry non-retryable 4xx. time.sleep is patched out so tests are fast.
"""

from __future__ import annotations

import pytest
import requests

from litemlflow import Client, LiteMLflowError


class _Resp:
    def __init__(self, status: int, payload=None) -> None:
        self.status_code = status
        self.ok = 200 <= status < 300
        self.headers = {"Content-Type": "application/json"} if payload is not None else {}
        self._payload = payload
        self.text = "" if payload is not None else "err"
        self.reason = "reason"

    def json(self):
        if self._payload is None:
            raise ValueError("no json")
        return self._payload


@pytest.fixture(autouse=True)
def _no_sleep(monkeypatch):
    monkeypatch.setattr("litemlflow.client.time.sleep", lambda _s: None)


def test_retries_transport_error_then_succeeds(monkeypatch):
    c = Client("http://x", max_retries=3)
    calls = {"n": 0}

    def fake(method, url, **kw):
        calls["n"] += 1
        if calls["n"] < 3:
            raise requests.ConnectionError("boom")
        return _Resp(200, {"ok": True})

    monkeypatch.setattr(c._session, "request", fake)
    assert c._request("GET", "/x") == {"ok": True}
    assert calls["n"] == 3


def test_retries_exhausted_raises(monkeypatch):
    c = Client("http://x", max_retries=2)
    calls = {"n": 0}

    def fake(method, url, **kw):
        calls["n"] += 1
        raise requests.ConnectionError("boom")

    monkeypatch.setattr(c._session, "request", fake)
    with pytest.raises(LiteMLflowError):
        c._request("GET", "/x")
    assert calls["n"] == 3  # initial + 2 retries


def test_retries_on_503_then_succeeds(monkeypatch):
    c = Client("http://x", max_retries=2)
    calls = {"n": 0}

    def fake(method, url, **kw):
        calls["n"] += 1
        return _Resp(503) if calls["n"] == 1 else _Resp(200, {"ok": 1})

    monkeypatch.setattr(c._session, "request", fake)
    assert c._request("GET", "/x") == {"ok": 1}
    assert calls["n"] == 2


def test_does_not_retry_4xx(monkeypatch):
    c = Client("http://x", max_retries=3)
    calls = {"n": 0}

    def fake(method, url, **kw):
        calls["n"] += 1
        return _Resp(400, {"error_code": "BAD_REQUEST", "message": "nope"})

    monkeypatch.setattr(c._session, "request", fake)
    with pytest.raises(LiteMLflowError) as ei:
        c._request("POST", "/x")
    assert ei.value.status == 400
    assert calls["n"] == 1  # no retry on a deterministic client error
