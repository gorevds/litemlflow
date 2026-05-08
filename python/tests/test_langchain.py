"""Tests for the LiteMLflow LangChain callback handler.

Requires langchain-core to be installed::

    pip install 'litemlflow[langchain]'

Tests that need a running server use the ``server`` fixture from conftest.py.
Tests that only exercise the handler's internal logic use a mock client.
"""

from __future__ import annotations

import time
from typing import Any
from unittest.mock import MagicMock
from uuid import UUID, uuid4

import pytest

langchain_core = pytest.importorskip("langchain_core")

from langchain_core.callbacks import BaseCallbackHandler  # noqa: E402
from langchain_core.outputs import LLMResult  # noqa: E402

from litemlflow import Client  # noqa: E402
from litemlflow.client import LiteMLflowError  # noqa: E402
from litemlflow.langchain import LiteMLflowCallbackHandler  # noqa: E402
from litemlflow.langchain.pricing import cost  # noqa: E402


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _mock_client(run_id: str = "mock-run-id") -> MagicMock:
    """Return a mock Client that satisfies handler construction."""
    c = MagicMock(spec=Client)
    c.get_experiment_by_name.side_effect = LiteMLflowError(
        404, "RESOURCE_DOES_NOT_EXIST", "not found"
    )
    c.create_experiment.return_value = 1
    mock_run = MagicMock()
    mock_run.id = run_id
    c.create_run.return_value = mock_run
    c._request.return_value = {}
    return c


def _make_handler(run_id: str = "mock-run-id", **kwargs: Any) -> Any:
    return LiteMLflowCallbackHandler(_mock_client(run_id=run_id), **kwargs)


def _get_flushed_spans(handler: Any) -> list[dict[str, Any]]:
    """Return the list of spans from the most recent POST /api/v1/traces call."""
    calls = [
        c for c in handler._client._request.call_args_list
        if len(c.args) >= 2 and c.args[1] == "/api/v1/traces"
    ]
    assert calls, "No /api/v1/traces POST found"
    last_call = calls[-1]
    payload = last_call.kwargs.get("json") or last_call.args[2]
    return payload["spans"]


def _get_all_flushed_spans(handler: Any) -> list[dict[str, Any]]:
    """Return all spans across all POST /api/v1/traces calls."""
    all_spans: list[dict[str, Any]] = []
    for c in handler._client._request.call_args_list:
        if len(c.args) >= 2 and c.args[1] == "/api/v1/traces":
            payload = c.kwargs.get("json") or c.args[2]
            all_spans.extend(payload["spans"])
    return all_spans


# ---------------------------------------------------------------------------
# 1. on_chain_start / on_chain_end: span with parent linkage
# ---------------------------------------------------------------------------

class TestChainSpan:
    def test_chain_start_end_records_span(self) -> None:
        handler = _make_handler()
        client = handler._client

        chain_run_id = uuid4()
        parent_run_id = uuid4()

        handler.on_chain_start(
            {"name": "MyChain", "id": ["langchain", "MyChain"]},
            {"question": "what?"},
            run_id=chain_run_id,
            parent_run_id=parent_run_id,
        )

        # Spans are buffered; no HTTP call until root closes.
        # But since parent_run_id is set, this chain is a child — not a root.
        # The parent (parent_run_id) was never opened in this handler, so
        # root_depth is still 0 after the child opens... wait, we only increment
        # root_depth if parent_lc_id is None. Here parent_run_id is provided, so
        # root_depth stays 0. That means the child's close should flush immediately.
        # Let's close it and verify.
        handler.on_chain_end(
            {"answer": "42"},
            run_id=chain_run_id,
            parent_run_id=parent_run_id,
        )

        spans = _get_flushed_spans(handler)
        assert len(spans) == 1
        span = spans[0]
        assert span["name"].startswith("chain:")
        assert span["status_code"] == "OK"
        assert "outputs" in span["attributes"]
        assert "parent_run_id" in span["attributes"]

    def test_chain_span_has_inputs_in_attrs(self) -> None:
        handler = _make_handler()
        chain_run_id = uuid4()

        handler.on_chain_start(
            {"name": "QA"},
            {"question": "hello?"},
            run_id=chain_run_id,
        )
        handler.on_chain_end({"answer": "world"}, run_id=chain_run_id)

        spans = _get_flushed_spans(handler)
        attrs = spans[0]["attributes"]
        assert "inputs" in attrs
        assert "hello?" in attrs["inputs"]

    def test_chain_parent_linkage(self) -> None:
        """Child span references parent's pre-generated span_id in the batch."""
        handler = _make_handler()

        parent_lc_id = uuid4()
        child_lc_id = uuid4()

        # Open parent (root).
        handler.on_chain_start({"name": "Parent"}, {}, run_id=parent_lc_id)
        parent_pre_span_id = handler._open[str(parent_lc_id)]["span_id"]

        # Open child (non-root).
        handler.on_chain_start(
            {"name": "Child"}, {}, run_id=child_lc_id, parent_run_id=parent_lc_id
        )

        # Close child (buffered, root still open → no flush yet).
        handler.on_chain_end({}, run_id=child_lc_id)
        assert handler._client._request.call_count == 0 or all(
            c.args[1] != "/api/v1/traces" for c in handler._client._request.call_args_list
        ), "Unexpected early flush"

        # Close parent → triggers flush.
        handler.on_chain_end({"out": "x"}, run_id=parent_lc_id)

        spans = _get_flushed_spans(handler)
        assert len(spans) == 2

        span_by_id = {s["id"]: s for s in spans}
        # Find parent span.
        parent_span = span_by_id[parent_pre_span_id]
        assert parent_span["parent_id"] is None

        # Find child span.
        child_spans = [s for s in spans if s["id"] != parent_pre_span_id]
        assert len(child_spans) == 1
        assert child_spans[0]["parent_id"] == parent_pre_span_id


# ---------------------------------------------------------------------------
# 2. on_llm_end: token metrics + cost
# ---------------------------------------------------------------------------

class TestLLMTokenMetrics:
    def test_token_metrics_logged(self) -> None:
        handler = _make_handler()
        client = handler._client

        llm_run_id = uuid4()
        handler.on_llm_start(
            {"name": "gpt-4o-mini"},
            ["Hello, world!"],
            run_id=llm_run_id,
            invocation_params={"model_name": "gpt-4o-mini"},
        )

        result = LLMResult(
            generations=[[]],
            llm_output={
                "token_usage": {
                    "prompt_tokens": 100,
                    "completion_tokens": 50,
                    "total_tokens": 150,
                }
            },
        )
        handler.on_llm_end(result, run_id=llm_run_id)

        # Four metrics: tokens.prompt, tokens.completion, tokens.total, cost.usd
        metric_calls = {c.args[1]: c.args[2] for c in client.log_metric.call_args_list}
        assert metric_calls["tokens.prompt"] == 100.0
        assert metric_calls["tokens.completion"] == 50.0
        assert metric_calls["tokens.total"] == 150.0

        expected_cost = cost("gpt-4o-mini", 100, 50)
        # 100/1e6 * 0.15 + 50/1e6 * 0.60 = 0.000045
        assert abs(expected_cost - 0.000045) < 1e-10
        assert abs(metric_calls["cost.usd"] - expected_cost) < 1e-10

    def test_unknown_model_cost_is_zero(self) -> None:
        handler = _make_handler()
        llm_run_id = uuid4()
        handler.on_llm_start(
            {"name": "some-unknown-model"},
            ["Hi"],
            run_id=llm_run_id,
        )
        result = LLMResult(
            generations=[[]],
            llm_output={
                "token_usage": {"prompt_tokens": 10, "completion_tokens": 5}
            },
        )
        handler.on_llm_end(result, run_id=llm_run_id)

        metric_calls = {c.args[1]: c.args[2] for c in handler._client.log_metric.call_args_list}
        assert metric_calls["cost.usd"] == 0.0

    def test_no_token_usage_no_metrics(self) -> None:
        """LLMResult without token_usage should not log any metrics."""
        handler = _make_handler()
        llm_run_id = uuid4()
        handler.on_llm_start({"name": "gpt-4o"}, ["prompt"], run_id=llm_run_id)
        handler.on_llm_end(LLMResult(generations=[[]], llm_output={}), run_id=llm_run_id)
        handler._client.log_metric.assert_not_called()

    def test_token_metrics_in_span_attributes(self) -> None:
        """Token counts should also appear in the span attributes."""
        handler = _make_handler()
        llm_run_id = uuid4()
        handler.on_llm_start(
            {"name": "gpt-4o-mini"},
            ["p"],
            run_id=llm_run_id,
            invocation_params={"model_name": "gpt-4o-mini"},
        )
        handler.on_llm_end(
            LLMResult(
                generations=[[]],
                llm_output={"token_usage": {"prompt_tokens": 5, "completion_tokens": 3}},
            ),
            run_id=llm_run_id,
        )
        spans = _get_flushed_spans(handler)
        assert len(spans) == 1
        attrs = spans[0]["attributes"]
        assert attrs["tokens.prompt"] == 5
        assert attrs["tokens.completion"] == 3


# ---------------------------------------------------------------------------
# 3. on_chain_error: status=ERROR with error message
# ---------------------------------------------------------------------------

class TestChainError:
    def test_chain_error_records_error_status(self) -> None:
        handler = _make_handler()

        chain_run_id = uuid4()
        handler.on_chain_start(
            {"name": "BrokenChain"},
            {},
            run_id=chain_run_id,
        )

        err = RuntimeError("something exploded")
        handler.on_chain_error(err, run_id=chain_run_id)

        spans = _get_flushed_spans(handler)
        assert len(spans) == 1
        span = spans[0]
        assert span["status_code"] == "ERROR"
        assert "something exploded" in span["status_message"]
        attrs = span["attributes"]
        assert "error" in attrs
        assert "something exploded" in attrs["error"]


# ---------------------------------------------------------------------------
# 4. on_tool_start / on_tool_end: tool span with input/output
# ---------------------------------------------------------------------------

class TestToolSpan:
    def test_tool_start_end_records_span(self) -> None:
        handler = _make_handler()

        tool_run_id = uuid4()
        handler.on_tool_start(
            {"name": "web_search"},
            "latest news about AI",
            run_id=tool_run_id,
        )
        handler.on_tool_end("10 results found", run_id=tool_run_id)

        spans = _get_flushed_spans(handler)
        assert len(spans) == 1
        span = spans[0]
        assert span["name"] == "tool:web_search"
        assert span["status_code"] == "OK"
        attrs = span["attributes"]
        assert "latest news" in attrs.get("input", "")
        assert "10 results" in attrs.get("output", "")

    def test_tool_error_records_error_status(self) -> None:
        handler = _make_handler()
        tool_run_id = uuid4()
        handler.on_tool_start({"name": "broken_tool"}, "query", run_id=tool_run_id)
        handler.on_tool_error(ValueError("tool failed"), run_id=tool_run_id)

        spans = _get_flushed_spans(handler)
        assert spans[0]["status_code"] == "ERROR"


# ---------------------------------------------------------------------------
# 5. End-to-end with a tiny fake chain on live server:
#    chain_start → llm_start → llm_end → chain_end.
#    Verifies the trace tree has 2 spans with correct parent linkage.
# ---------------------------------------------------------------------------

class TestEndToEndTrace:
    def test_trace_tree_on_server(self, server: str) -> None:
        """Drive a fake chain through the handler and check span parent linkage."""
        client = Client(server)

        exp_name = f"langchain-e2e-{time.time_ns()}"
        exp_id = client.create_experiment(exp_name)

        handler = LiteMLflowCallbackHandler(client, experiment_id=exp_id)
        run_id = handler._run_id

        chain_lc_id = uuid4()
        llm_lc_id = uuid4()

        # Chain starts (root span).
        handler.on_chain_start(
            {"name": "SimpleChain"},
            {"question": "What is 2+2?"},
            run_id=chain_lc_id,
        )
        chain_pre_span_id = handler._open[str(chain_lc_id)]["span_id"]

        # LLM starts as child of chain.
        handler.on_llm_start(
            {"name": "gpt-4o-mini"},
            ["What is 2+2?"],
            run_id=llm_lc_id,
            parent_run_id=chain_lc_id,
            invocation_params={"model_name": "gpt-4o-mini"},
        )

        # LLM ends (chain still open — no flush yet).
        handler.on_llm_end(
            LLMResult(
                generations=[[]],
                llm_output={
                    "token_usage": {
                        "prompt_tokens": 10,
                        "completion_tokens": 5,
                        "total_tokens": 15,
                    }
                },
            ),
            run_id=llm_lc_id,
            parent_run_id=chain_lc_id,
        )

        # Chain ends → flushes both spans as a batch.
        handler.on_chain_end({"answer": "4"}, run_id=chain_lc_id)

        # Retrieve spans from server.
        spans = client.get_run_traces(run_id)
        assert len(spans) >= 2, f"Expected >=2 spans, got {len(spans)}: {spans}"

        chain_spans = [s for s in spans if s.get("name", "").startswith("chain:")]
        llm_spans = [s for s in spans if s.get("name", "").startswith("llm:")]
        assert len(chain_spans) >= 1, "No chain span found"
        assert len(llm_spans) >= 1, "No llm span found"

        chain_span = chain_spans[0]
        llm_span = llm_spans[0]

        # Verify the chain span was stored with the pre-generated id.
        assert chain_span["id"] == chain_pre_span_id, (
            f"Server chain span id {chain_span['id']!r} != pre-generated {chain_pre_span_id!r}"
        )
        # Verify parent linkage.
        assert llm_span.get("parent_id") == chain_span["id"], (
            f"LLM span parent_id {llm_span.get('parent_id')!r} "
            f"!= chain span id {chain_span['id']!r}"
        )

    def test_token_metrics_on_server(self, server: str) -> None:
        """Verify token metrics are persisted to the run on the live server."""
        client = Client(server)
        exp_id = client.create_experiment(f"lc-metrics-{time.time_ns()}")

        handler = LiteMLflowCallbackHandler(client, experiment_id=exp_id)
        run_id = handler._run_id

        llm_lc_id = uuid4()
        handler.on_llm_start(
            {"name": "gpt-4o-mini"},
            ["prompt"],
            run_id=llm_lc_id,
            invocation_params={"model_name": "gpt-4o-mini"},
        )
        handler.on_llm_end(
            LLMResult(
                generations=[[]],
                llm_output={
                    "token_usage": {
                        "prompt_tokens": 100,
                        "completion_tokens": 50,
                        "total_tokens": 150,
                    }
                },
            ),
            run_id=llm_lc_id,
        )

        run_data = client.get_run(run_id)["data"]
        metrics = {m["key"]: m["value"] for m in run_data.get("metrics", [])}
        assert "tokens.prompt" in metrics, f"tokens.prompt missing; got {metrics}"
        assert "tokens.completion" in metrics
        assert "tokens.total" in metrics
        assert "cost.usd" in metrics
        assert abs(metrics["cost.usd"] - 0.000045) < 1e-10
