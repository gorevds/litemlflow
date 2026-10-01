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


# --- idempotency-aware retries -------------------------------------------------


def _counting(responses):
    """Return (fake_request, calls) yielding each item: raise if exception."""
    calls: list[tuple[str, str]] = []

    def fake(method, url, **kw):
        calls.append((method, url))
        item = responses[min(len(calls) - 1, len(responses) - 1)]
        if isinstance(item, BaseException):
            raise item
        return item

    return fake, calls


def test_non_idempotent_post_not_retried_after_read_timeout(monkeypatch):
    # The request reached the server; replaying create_run could duplicate it.
    c = Client("http://x", max_retries=3)
    fake, calls = _counting([requests.ReadTimeout("slow"), _Resp(200, {"run": {}})])
    monkeypatch.setattr(c._session, "request", fake)
    with pytest.raises(LiteMLflowError) as ei:
        c.create_run(1)
    assert ei.value.code == "TRANSPORT_ERROR"
    assert len(calls) == 1


def test_non_idempotent_post_retried_when_never_sent(monkeypatch):
    c = Client("http://x", max_retries=3)
    fake, calls = _counting(
        [requests.ConnectTimeout("connect"), _Resp(200, {"experiment_id": "7"})]
    )
    monkeypatch.setattr(c._session, "request", fake)
    assert c.create_experiment("e") == 7
    assert len(calls) == 2


def test_non_idempotent_post_retried_on_connection_refused(monkeypatch):
    from urllib3.exceptions import MaxRetryError, NewConnectionError

    refused = requests.ConnectionError(
        MaxRetryError(None, "/x", NewConnectionError(None, "refused"))
    )
    c = Client("http://x", max_retries=3)
    fake, calls = _counting([refused, _Resp(200, {"experiment_id": "3"})])
    monkeypatch.setattr(c._session, "request", fake)
    assert c.create_experiment("e") == 3
    assert len(calls) == 2


def test_non_idempotent_post_not_retried_on_502_but_on_503(monkeypatch):
    c = Client("http://x", max_retries=3)
    fake, calls = _counting([_Resp(502), _Resp(200, {"experiment_id": "1"})])
    monkeypatch.setattr(c._session, "request", fake)
    with pytest.raises(LiteMLflowError) as ei:
        c.create_experiment("e")
    assert ei.value.status == 502
    assert len(calls) == 1

    fake, calls = _counting([_Resp(503), _Resp(200, {"experiment_id": "1"})])
    monkeypatch.setattr(c._session, "request", fake)
    assert c.create_experiment("e") == 1
    assert len(calls) == 2


def test_idempotent_post_retried_after_read_timeout(monkeypatch):
    c = Client("http://x", max_retries=3)
    fake, calls = _counting([requests.ReadTimeout("slow"), _Resp(200, {})])
    monkeypatch.setattr(c._session, "request", fake)
    c.log_metric("r", "loss", 1.0, timestamp_ms=1)
    assert len(calls) == 2


def test_ssl_error_not_retried(monkeypatch):
    c = Client("http://x", max_retries=3)
    fake, calls = _counting([requests.exceptions.SSLError("bad cert")])
    monkeypatch.setattr(c._session, "request", fake)
    with pytest.raises(LiteMLflowError):
        c._request("GET", "/x")
    assert len(calls) == 1


def test_negative_retry_after_does_not_crash():
    c = Client("http://x")
    r = _Resp(503)
    r.headers = {"Retry-After": "-5"}
    assert c._retry_delay(r, 0) == c._backoff(0)
    r.headers = {"Retry-After": "2"}
    assert c._retry_delay(r, 0) == 2.0


def test_path_segments_are_percent_encoded(monkeypatch):
    c = Client("http://x")
    fake, calls = _counting([_Resp(200, {"name": "a?b"})])
    monkeypatch.setattr(c._session, "request", fake)
    c.get_prompt_by_alias("a?b #1", "prod%")
    assert calls[-1][1] == "http://x/api/v1/prompts/a%3Fb%20%231/aliases/prod%25"


def test_start_run_marks_killed_on_keyboard_interrupt(monkeypatch):
    c = Client("http://x")
    statuses: list[str] = []
    monkeypatch.setattr(
        c, "create_run", lambda exp, name=None, tags=None: __import__("litemlflow").Run(c, "r", exp)
    )

    def fail_update(run_id, *, status=None, end_time_ms=None, name=None):
        statuses.append(status)
        raise LiteMLflowError(0, "TRANSPORT_ERROR", "server down")

    monkeypatch.setattr(c, "update_run", fail_update)
    with pytest.raises(KeyboardInterrupt):
        with c.start_run(1):
            raise KeyboardInterrupt
    # The run is closed, and the reporting failure does not mask the interrupt.
    assert statuses == ["KILLED"]
