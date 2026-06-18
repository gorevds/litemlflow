"""LiteMLflow native HTTP client.

Design:
- A thin, well-typed wrapper around the LiteMLflow REST API.
- Reuses a requests.Session for connection pooling.
- All API calls raise LiteMLflowError on non-2xx responses, with the
  server's error_code and message included.
- Context managers (Run, Span) make the common case ergonomic without
  hiding the underlying primitives.
"""

from __future__ import annotations

import os
import secrets
import time
from contextlib import contextmanager
from dataclasses import dataclass, field
from typing import Any, Iterator

import requests


class LiteMLflowError(Exception):
    """Raised when the server returns a non-2xx response."""

    def __init__(self, status: int, code: str, message: str) -> None:
        super().__init__(f"{status} {code}: {message}")
        self.status = status
        self.code = code
        self.message = message


@dataclass
class Run:
    """A handle to an open run on the server."""

    client: "Client"
    id: str
    experiment_id: int
    name: str | None = None

    def log_metric(self, key: str, value: float, *, step: int = 0, timestamp_ms: int | None = None) -> None:
        self.client.log_metric(self.id, key, value, step=step, timestamp_ms=timestamp_ms)

    def log_param(self, key: str, value: str) -> None:
        self.client.log_param(self.id, key, value)

    def set_tag(self, key: str, value: str) -> None:
        self.client.set_tag(self.id, key, value)

    def finish(self, status: str = "FINISHED") -> None:
        self.client.update_run(self.id, status=status, end_time_ms=int(time.time() * 1000))


@dataclass
class Span:
    """A handle to an open trace span. Use within a `with run.span(...)` block."""

    client: "Client"
    id: str
    trace_id: str
    name: str
    parent_id: str | None = None
    run_id: str | None = None
    start_time_ns: int = 0
    attributes: dict[str, Any] = field(default_factory=dict)


class Client:
    """LiteMLflow HTTP client.

    Args:
        url: base URL of the server, e.g., "http://localhost:5000".
        auth: optional (username, password) tuple for basic auth.
        timeout: request timeout in seconds (default 30).
        workspace: optional workspace to scope all requests to. Sent as the
            ``X-Workspace`` header on every request; the server validates it and
            isolates experiments/runs/registry per workspace. Defaults to the
            ``LITEMLFLOW_WORKSPACE`` env var, else the server's "default".
        max_retries: number of retries for transient failures (connection
            errors and 429/502/503/504) with exponential backoff (default 3).
    """

    def __init__(
        self,
        url: str | None = None,
        *,
        auth: tuple[str, str] | None = None,
        timeout: float = 30.0,
        workspace: str | None = None,
        max_retries: int = 3,
    ) -> None:
        self.url = (url or os.environ.get("LITEMLFLOW_URL", "http://localhost:5000")).rstrip("/")
        self.timeout = timeout
        self.max_retries = max_retries
        self.workspace = workspace or os.environ.get("LITEMLFLOW_WORKSPACE") or None
        self._session = requests.Session()
        if auth is not None:
            self._session.auth = auth
        if self.workspace:
            self._session.headers["X-Workspace"] = self.workspace

    # ------------------------------------------------------------------ http

    # HTTP statuses worth retrying: transient overload / gateway errors. 5xx
    # like 500/501 are treated as deterministic and not retried.
    _RETRYABLE_STATUS = frozenset({429, 502, 503, 504})

    def _request(self, method: str, path: str, json: Any | None = None, params: dict | None = None) -> Any:
        full = self.url + path
        attempt = 0
        while True:
            try:
                resp = self._session.request(method, full, json=json, params=params, timeout=self.timeout)
            except requests.RequestException as exc:
                # Transport error (connection refused, reset, timeout): retry
                # with backoff, then surface as a TRANSPORT_ERROR.
                if attempt < self.max_retries:
                    time.sleep(self._backoff(attempt))
                    attempt += 1
                    continue
                raise LiteMLflowError(0, "TRANSPORT_ERROR", str(exc)) from exc
            if resp.status_code in self._RETRYABLE_STATUS and attempt < self.max_retries:
                time.sleep(self._retry_delay(resp, attempt))
                attempt += 1
                continue
            return self._handle_response(resp)

    @staticmethod
    def _backoff(attempt: int) -> float:
        """Exponential backoff in seconds, capped at 10s."""
        return min(10.0, 0.25 * (2**attempt))

    def _retry_delay(self, resp: requests.Response, attempt: int) -> float:
        """Honor a numeric Retry-After header, else exponential backoff."""
        ra = resp.headers.get("Retry-After")
        if ra:
            try:
                return min(60.0, float(ra))
            except ValueError:
                pass
        return self._backoff(attempt)

    def _handle_response(self, resp: requests.Response) -> Any:
        ctype = resp.headers.get("Content-Type", "")
        body: Any = None
        if "application/json" in ctype:
            try:
                body = resp.json()
            except ValueError:
                body = None
        if not resp.ok:
            code = "HTTP_ERROR"
            msg = resp.text or resp.reason or ""
            if isinstance(body, dict):
                code = body.get("error_code", code)
                msg = body.get("message", msg)
            raise LiteMLflowError(resp.status_code, code, msg)
        return body if body is not None else resp.text

    # ----------------------------------------------------------- experiments

    def create_experiment(self, name: str, *, artifact_location: str | None = None, tags: dict[str, str] | None = None) -> int:
        payload: dict[str, Any] = {"name": name}
        if artifact_location is not None:
            payload["artifact_location"] = artifact_location
        if tags:
            payload["tags"] = [{"key": k, "value": v} for k, v in tags.items()]
        body = self._request("POST", "/api/2.0/mlflow/experiments/create", json=payload)
        return int(body["experiment_id"])

    def get_experiment(self, experiment_id: int) -> dict[str, Any]:
        body = self._request("GET", "/api/2.0/mlflow/experiments/get", params={"experiment_id": experiment_id})
        return body["experiment"]

    def get_experiment_by_name(self, name: str) -> dict[str, Any]:
        body = self._request("GET", "/api/2.0/mlflow/experiments/get-by-name", params={"experiment_name": name})
        return body["experiment"]

    def search_experiments(self, *, max_results: int = 1000, filter: str | None = None) -> list[dict[str, Any]]:
        payload: dict[str, Any] = {"max_results": max_results}
        if filter:
            payload["filter"] = filter
        body = self._request("POST", "/api/2.0/mlflow/experiments/search", json=payload)
        return body.get("experiments", []) or []

    def delete_experiment(self, experiment_id: int) -> None:
        self._request("POST", "/api/2.0/mlflow/experiments/delete", json={"experiment_id": str(experiment_id)})

    def restore_experiment(self, experiment_id: int) -> None:
        self._request("POST", "/api/2.0/mlflow/experiments/restore", json={"experiment_id": str(experiment_id)})

    # ------------------------------------------------------------------- runs

    def create_run(self, experiment_id: int, *, name: str | None = None, tags: dict[str, str] | None = None) -> Run:
        payload: dict[str, Any] = {"experiment_id": str(experiment_id), "start_time": int(time.time() * 1000)}
        if name:
            payload["run_name"] = name
        if tags:
            payload["tags"] = [{"key": k, "value": v} for k, v in tags.items()]
        body = self._request("POST", "/api/2.0/mlflow/runs/create", json=payload)
        info = body["run"]["info"]
        return Run(self, id=info["run_id"], experiment_id=int(info["experiment_id"]), name=info.get("run_name"))

    @contextmanager
    def start_run(self, experiment_id: int, *, name: str | None = None, tags: dict[str, str] | None = None) -> Iterator[Run]:
        run = self.create_run(experiment_id, name=name, tags=tags)
        try:
            yield run
        except Exception:
            run.finish(status="FAILED")
            raise
        else:
            run.finish(status="FINISHED")

    def get_run(self, run_id: str) -> dict[str, Any]:
        body = self._request("GET", "/api/2.0/mlflow/runs/get", params={"run_id": run_id})
        return body["run"]

    def update_run(self, run_id: str, *, status: str | None = None, end_time_ms: int | None = None, name: str | None = None) -> None:
        payload: dict[str, Any] = {"run_id": run_id}
        if status:
            payload["status"] = status
        if end_time_ms is not None:
            payload["end_time"] = end_time_ms
        if name:
            payload["run_name"] = name
        self._request("POST", "/api/2.0/mlflow/runs/update", json=payload)

    def search_runs(
        self,
        experiment_ids: list[int],
        *,
        filter: str | None = None,
        order_by: list[str] | None = None,
        max_results: int = 1000,
    ) -> list[dict[str, Any]]:
        payload: dict[str, Any] = {
            "experiment_ids": [str(e) for e in experiment_ids],
            "max_results": max_results,
        }
        if filter:
            payload["filter"] = filter
        if order_by:
            payload["order_by"] = order_by
        body = self._request("POST", "/api/2.0/mlflow/runs/search", json=payload)
        return body.get("runs", []) or []

    # -------------------------------------------------------- metrics & params

    def log_metric(self, run_id: str, key: str, value: float, *, step: int = 0, timestamp_ms: int | None = None) -> None:
        if timestamp_ms is None:
            timestamp_ms = int(time.time() * 1000)
        self._request(
            "POST",
            "/api/2.0/mlflow/runs/log-metric",
            json={"run_id": run_id, "key": key, "value": value, "timestamp": timestamp_ms, "step": step},
        )

    def log_param(self, run_id: str, key: str, value: str) -> None:
        self._request(
            "POST",
            "/api/2.0/mlflow/runs/log-parameter",
            json={"run_id": run_id, "key": key, "value": value},
        )

    def log_batch(
        self,
        run_id: str,
        *,
        metrics: list[dict[str, Any]] | None = None,
        params: list[dict[str, Any]] | None = None,
        tags: list[dict[str, Any]] | None = None,
    ) -> None:
        self._request(
            "POST",
            "/api/2.0/mlflow/runs/log-batch",
            json={"run_id": run_id, "metrics": metrics or [], "params": params or [], "tags": tags or []},
        )

    def set_tag(self, run_id: str, key: str, value: str) -> None:
        self._request("POST", "/api/2.0/mlflow/runs/set-tag", json={"run_id": run_id, "key": key, "value": value})

    def get_metric_history(self, run_id: str, metric_key: str) -> list[dict[str, Any]]:
        body = self._request(
            "GET",
            "/api/2.0/mlflow/metrics/get-history",
            params={"run_id": run_id, "metric_key": metric_key},
        )
        return body.get("metrics", []) or []

    # ---------------------------------------------------------------- traces

    def start_trace(self) -> str:
        """Generate a new trace ID locally; spans should reference this id."""
        return secrets.token_hex(16)

    def log_span(
        self,
        trace_id: str,
        name: str,
        *,
        run_id: str | None = None,
        parent_id: str | None = None,
        start_time_ns: int | None = None,
        end_time_ns: int | None = None,
        kind: str = "INTERNAL",
        attrs: dict[str, Any] | None = None,
        status_code: str = "OK",
        status_message: str = "",
    ) -> str:
        if start_time_ns is None:
            start_time_ns = time.time_ns()
        if end_time_ns is None:
            end_time_ns = start_time_ns
        span_id = secrets.token_hex(8)
        self._request(
            "POST",
            "/api/v1/traces",
            json={
                "trace_id": trace_id,
                "spans": [
                    {
                        "id": span_id,
                        "trace_id": trace_id,
                        "parent_id": parent_id,
                        "run_id": run_id,
                        "name": name,
                        "span_kind": kind,
                        "start_time_ns": start_time_ns,
                        "end_time_ns": end_time_ns,
                        "attributes": attrs or {},
                        "status_code": status_code,
                        "status_message": status_message,
                    }
                ],
            },
        )
        return span_id

    def get_run_traces(self, run_id: str) -> list[dict[str, Any]]:
        body = self._request("GET", f"/api/v1/runs/{run_id}/traces")
        return body.get("spans", []) or []

    # --------------------------------------------------------------- prompts

    def create_prompt(self, name: str, content: str, *, description: str | None = None) -> int:
        payload: dict[str, Any] = {"name": name, "content": content}
        if description:
            payload["description"] = description
        body = self._request("POST", "/api/v1/prompts", json=payload)
        return int(body["version"])

    def get_prompt(self, name: str) -> dict[str, Any]:
        return self._request("GET", f"/api/v1/prompts/{name}")

    def get_prompt_version(self, name: str, version: int) -> dict[str, Any]:
        return self._request("GET", f"/api/v1/prompts/{name}/versions/{version}")

    def list_prompt_versions(self, name: str) -> list[dict[str, Any]]:
        body = self._request("GET", f"/api/v1/prompts/{name}/versions")
        return body.get("versions", []) or []

    def set_prompt_alias(self, name: str, alias: str, version: int) -> None:
        self._request("POST", f"/api/v1/prompts/{name}/aliases", json={"alias": alias, "version": version})

    def get_prompt_by_alias(self, name: str, alias: str) -> dict[str, Any]:
        return self._request("GET", f"/api/v1/prompts/{name}/aliases/{alias}")

    # ------------------------------------------------------------------ evals

    def create_eval(
        self,
        run_id: str,
        target_run_ids: list[str],
        *,
        dataset_ref: str | None = None,
        score: float | None = None,
        metrics: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        payload: dict[str, Any] = {"run_id": run_id, "target_run_ids": target_run_ids}
        if dataset_ref is not None:
            payload["dataset_ref"] = dataset_ref
        if score is not None:
            payload["score"] = score
        if metrics is not None:
            payload["metrics"] = metrics
        return self._request("POST", "/api/v1/evals", json=payload)

    def get_eval(self, run_id: str) -> dict[str, Any]:
        return self._request("GET", f"/api/v1/evals/{run_id}")

    # --------------------------------------------------------------- meta

    def health(self) -> bool:
        body = self._request("GET", "/healthz")
        return bool(body.get("ok"))

    def version(self) -> dict[str, str]:
        return self._request("GET", "/version")

    def close(self) -> None:
        self._session.close()
