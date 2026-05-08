"""Tests for the LiteMLflow LlamaIndex event handler.

Tests that only exercise the handler's internal logic use a mock client and
synthetic events (no real LlamaIndex Index required).

Tests that need a running server use the ``server`` fixture from conftest.py.

If llama-index-core is NOT installed, the lazy-import tests run but the
server integration tests are skipped.
"""

from __future__ import annotations

import time
from typing import Any
from unittest.mock import MagicMock

import pytest

from litemlflow import Client
from litemlflow.client import LiteMLflowError
from litemlflow.llamaindex import LiteMLflowEventHandler


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


def _make_event(cls_name: str, event_id: str, **kwargs: Any) -> Any:
    """Build a minimal fake LlamaIndex event as a dynamically-typed object.

    The handler dispatches on ``type(event).__name__``, so we need a real
    class with the right ``__name__``, not just a SimpleNamespace.
    """
    # Dynamically create a class whose __name__ matches the LlamaIndex event
    # class name.  Any unknown attributes passed as kwargs are stored as
    # instance attributes; ``id_`` is set explicitly.
    cls = type(cls_name, (), {})
    obj = cls.__new__(cls)
    obj.id_ = event_id  # type: ignore[attr-defined]
    for k, v in kwargs.items():
        setattr(obj, k, v)
    return obj


def _get_flushed_spans(client: MagicMock) -> list[dict[str, Any]]:
    """Return spans from the most recent POST /api/v1/traces call."""
    calls = [
        c for c in client._request.call_args_list
        if len(c.args) >= 2 and c.args[1] == "/api/v1/traces"
    ]
    assert calls, "No /api/v1/traces POST found"
    last_call = calls[-1]
    payload = last_call.kwargs.get("json") or last_call.args[2]
    return payload["spans"]


def _get_all_flushed_spans(client: MagicMock) -> list[dict[str, Any]]:
    """Return all spans across all POST /api/v1/traces calls."""
    all_spans: list[dict[str, Any]] = []
    for c in client._request.call_args_list:
        if len(c.args) >= 2 and c.args[1] == "/api/v1/traces":
            payload = c.kwargs.get("json") or c.args[2]
            all_spans.extend(payload["spans"])
    return all_spans


# Detect whether llama-index-core is installed.
try:
    import llama_index.core  # noqa: F401
    _HAS_LLAMAINDEX = True
except ImportError:
    _HAS_LLAMAINDEX = False

_skip_if_no_llamaindex = pytest.mark.skipif(
    not _HAS_LLAMAINDEX,
    reason="llama-index-core is not installed; skipping event handler tests",
)


# ---------------------------------------------------------------------------
# 1. Lazy-import: LiteMLflowEventHandler importable without llama-index-core
# ---------------------------------------------------------------------------

class TestLazyImport:
    def test_module_imports_without_llamaindex(self) -> None:
        """Importing the module must succeed regardless of llama-index-core."""
        # The import at the top of this module already exercised this path.
        from litemlflow.llamaindex import LiteMLflowEventHandler as _  # noqa: F401

    def test_instantiation_raises_import_error_without_llamaindex(self) -> None:
        """If llama-index-core is absent, instantiation raises ImportError with hint."""
        if _HAS_LLAMAINDEX:
            pytest.skip("llama-index-core is installed; cannot test missing-dep path")

        c = _mock_client()
        with pytest.raises(ImportError, match="litemlflow\\[llamaindex\\]"):
            LiteMLflowEventHandler(c)


# ---------------------------------------------------------------------------
# 2. Handler construction (with mock — no llama-index-core needed)
# ---------------------------------------------------------------------------

class TestHandlerInternal:
    """These tests bypass the lazy-import by directly using the impl class."""

    def _make_impl(self, run_id: str = "mock-run-id") -> Any:
        """Construct _LiteMLflowEventHandlerImpl directly, skipping the
        lazy-import factory."""
        from litemlflow.llamaindex.handler import _LiteMLflowEventHandlerImpl
        c = _mock_client(run_id=run_id)
        inst = object.__new__(_LiteMLflowEventHandlerImpl)
        _LiteMLflowEventHandlerImpl.__init__(inst, c, run_id=run_id)
        return inst

    def _emit(self, handler: Any, cls_name: str, event_id: str, **kwargs: Any) -> SimpleNamespace:
        ev = _make_event(cls_name, event_id, **kwargs)
        handler.handle(ev)
        return ev

    # -- query start/end produces a root span and flushes on close ----------

    def test_query_span_flushed_on_end(self) -> None:
        h = self._make_impl()
        eid = "qry-001"
        self._emit(h, "QueryStartEvent", eid, query="What is LiteMLflow?")
        # No flush yet.
        assert h._client._request.call_count == 0 or all(
            c.args[1] != "/api/v1/traces" for c in h._client._request.call_args_list
        )
        self._emit(h, "QueryEndEvent", eid, response="A lightweight tracker.")
        spans = _get_flushed_spans(h._client)
        assert len(spans) == 1
        assert spans[0]["name"].startswith("query:")
        assert spans[0]["status_code"] == "OK"

    # -- retrieval child span has parent linkage ----------------------------

    def test_retrieval_child_span_parent_linkage(self) -> None:
        h = self._make_impl()
        qid = "qry-002"
        rid = "ret-002"
        self._emit(h, "QueryStartEvent", qid, query="Who wrote Hamlet?")
        query_pre_id = h._open[qid]["span_id"]

        self._emit(h, "RetrievalStartEvent", rid)
        self._emit(h, "RetrievalEndEvent", rid, nodes=[object(), object()])

        # Still buffered — root open.
        assert not any(
            c.args[1] == "/api/v1/traces" for c in h._client._request.call_args_list
        )

        self._emit(h, "QueryEndEvent", qid, response="Shakespeare.")
        spans = _get_flushed_spans(h._client)
        assert len(spans) == 2

        span_by_id = {s["id"]: s for s in spans}
        query_span = span_by_id[query_pre_id]
        assert query_span["parent_id"] is None

        ret_spans = [s for s in spans if s["id"] != query_pre_id]
        assert len(ret_spans) == 1
        assert ret_spans[0]["parent_id"] == query_pre_id
        assert ret_spans[0]["name"] == "retrieval"
        assert ret_spans[0]["attributes"]["nodes.count"] == 2

    # -- LLMCompletion span with token+cost metrics ------------------------

    def test_llm_completion_token_metrics(self) -> None:
        h = self._make_impl()
        from litemlflow._pricing import cost as pricing_cost

        eid = "llm-003"
        self._emit(h, "LLMCompletionStartEvent", eid, model="gpt-4o-mini")

        # Fake token_usage as a dict.
        token_usage = {"prompt_tokens": 200, "completion_tokens": 80, "total_tokens": 280}
        self._emit(h, "LLMCompletionEndEvent", eid, token_usage=token_usage, response="42")

        spans = _get_all_flushed_spans(h._client)
        assert len(spans) == 1
        span = spans[0]
        assert span["name"] == "llm:gpt-4o-mini"
        attrs = span["attributes"]
        assert attrs["tokens.prompt"] == 200
        assert attrs["tokens.completion"] == 80
        assert attrs["tokens.total"] == 280
        expected_cost = pricing_cost("gpt-4o-mini", 200, 80)
        assert abs(attrs["cost.usd"] - expected_cost) < 1e-10

        # Verify log_metric was called 4 times.
        metric_calls = {c.args[1]: c.args[2] for c in h._client.log_metric.call_args_list}
        assert metric_calls["tokens.prompt"] == 200.0
        assert metric_calls["tokens.completion"] == 80.0
        assert metric_calls["tokens.total"] == 280.0
        assert abs(metric_calls["cost.usd"] - expected_cost) < 1e-10

    # -- unknown model → cost 0 --------------------------------------------

    def test_unknown_model_cost_is_zero(self) -> None:
        h = self._make_impl()
        eid = "llm-004"
        self._emit(h, "LLMCompletionStartEvent", eid, model="some-unknown-model-xyz")
        token_usage = {"prompt_tokens": 50, "completion_tokens": 20}
        self._emit(h, "LLMCompletionEndEvent", eid, token_usage=token_usage)
        spans = _get_all_flushed_spans(h._client)
        assert spans[0]["attributes"]["cost.usd"] == 0.0

    # -- no token usage → no metrics ---------------------------------------

    def test_no_token_usage_no_metrics(self) -> None:
        h = self._make_impl()
        eid = "llm-005"
        self._emit(h, "LLMCompletionStartEvent", eid, model="gpt-4o")
        self._emit(h, "LLMCompletionEndEvent", eid)
        h._client.log_metric.assert_not_called()

    # -- chat span ---------------------------------------------------------

    def test_chat_span_name(self) -> None:
        h = self._make_impl()
        eid = "chat-006"
        self._emit(h, "LLMChatStartEvent", eid, model="claude-3-5-sonnet-20241022")
        token_usage = {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
        self._emit(h, "LLMChatEndEvent", eid, token_usage=token_usage)
        spans = _get_all_flushed_spans(h._client)
        assert spans[0]["name"] == "chat:claude-3-5-sonnet-20241022"

    # -- embed span --------------------------------------------------------

    def test_embed_span(self) -> None:
        h = self._make_impl()
        eid = "emb-007"
        self._emit(h, "EmbeddingStartEvent", eid, model="text-embedding-ada-002")
        self._emit(h, "EmbeddingEndEvent", eid, chunks=["a", "b", "c"])
        spans = _get_all_flushed_spans(h._client)
        assert spans[0]["name"] == "embed:text-embedding-ada-002"
        assert spans[0]["attributes"]["chunks.count"] == 3

    # -- synthesis span ----------------------------------------------------

    def test_synthesis_span(self) -> None:
        h = self._make_impl()
        eid = "syn-008"
        self._emit(h, "SynthesisStartEvent", eid)
        self._emit(h, "SynthesisEndEvent", eid, response="synthesized answer")
        spans = _get_all_flushed_spans(h._client)
        assert spans[0]["name"] == "synthesis"
        assert "synthesized answer" in spans[0]["attributes"].get("response", "")

    # -- multiple root spans flush separately ------------------------------

    def test_multiple_root_spans_flush_independently(self) -> None:
        h = self._make_impl()
        eid1 = "qry-009a"
        eid2 = "qry-009b"

        self._emit(h, "QueryStartEvent", eid1, query="first")
        self._emit(h, "QueryEndEvent", eid1, response="first answer")
        spans_after_first = _get_all_flushed_spans(h._client)
        assert len(spans_after_first) == 1

        self._emit(h, "QueryStartEvent", eid2, query="second")
        self._emit(h, "QueryEndEvent", eid2, response="second answer")
        spans_all = _get_all_flushed_spans(h._client)
        assert len(spans_all) == 2

    # -- unknown event type is silently ignored ----------------------------

    def test_unknown_event_ignored(self) -> None:
        h = self._make_impl()
        ev = _make_event("SomeUnknownEvent", "unk-010")
        h.handle(ev)  # Must not raise.
        assert h._client._request.call_count == 0


# ---------------------------------------------------------------------------
# 3. End-to-end on live server (requires llama-index-core)
# ---------------------------------------------------------------------------

@_skip_if_no_llamaindex
class TestEndToEndTrace:
    def test_trace_tree_on_server(self, server: str) -> None:
        """Drive a fake query through the handler and check span parent linkage."""
        client = Client(server)
        exp_name = f"llamaindex-e2e-{time.time_ns()}"
        exp_id = client.create_experiment(exp_name)

        handler = LiteMLflowEventHandler(client, experiment_id=exp_id)
        run_id = handler._run_id

        qid = "qry-e2e-001"
        rid = "ret-e2e-001"
        llm_id = "llm-e2e-001"

        # Emit events directly.
        def emit(cls_name: str, eid: str, **kwargs: Any) -> None:
            ev = _make_event(cls_name, eid, **kwargs)
            handler.handle(ev)

        emit("QueryStartEvent", qid, query="What is 2+2?")
        query_pre_span_id = handler._open[qid]["span_id"]

        emit("RetrievalStartEvent", rid)
        emit("RetrievalEndEvent", rid, nodes=[object()])

        emit("LLMCompletionStartEvent", llm_id, model="gpt-4o-mini")
        emit(
            "LLMCompletionEndEvent",
            llm_id,
            model="gpt-4o-mini",
            token_usage={"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
            response="4",
        )

        emit("QueryEndEvent", qid, response="4")

        # Retrieve spans from server.
        spans = client.get_run_traces(run_id)
        assert len(spans) >= 3, f"Expected >=3 spans, got {len(spans)}: {spans}"

        query_spans = [s for s in spans if s.get("name", "").startswith("query:")]
        retrieval_spans = [s for s in spans if s.get("name") == "retrieval"]
        llm_spans = [s for s in spans if s.get("name", "").startswith("llm:")]

        assert len(query_spans) >= 1, "No query span found"
        assert len(retrieval_spans) >= 1, "No retrieval span found"
        assert len(llm_spans) >= 1, "No llm span found"

        query_span = query_spans[0]
        retrieval_span = retrieval_spans[0]

        assert query_span["id"] == query_pre_span_id, (
            f"Server query span id {query_span['id']!r} != pre-generated {query_pre_span_id!r}"
        )
        assert retrieval_span.get("parent_id") == query_span["id"], (
            f"Retrieval span parent_id {retrieval_span.get('parent_id')!r} "
            f"!= query span id {query_span['id']!r}"
        )

    def test_token_metrics_on_server(self, server: str) -> None:
        """Verify token metrics are persisted to the run on the live server."""
        client = Client(server)
        exp_id = client.create_experiment(f"li-metrics-{time.time_ns()}")

        handler = LiteMLflowEventHandler(client, experiment_id=exp_id)
        run_id = handler._run_id

        from litemlflow._pricing import cost as pricing_cost

        def emit(cls_name: str, eid: str, **kwargs: Any) -> None:
            ev = _make_event(cls_name, eid, **kwargs)
            handler.handle(ev)

        eid = "llm-srv-001"
        emit("LLMCompletionStartEvent", eid, model="gpt-4o-mini")
        emit(
            "LLMCompletionEndEvent",
            eid,
            token_usage={"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150},
        )

        run_data = client.get_run(run_id)["data"]
        metrics = {m["key"]: m["value"] for m in run_data.get("metrics", [])}
        assert "tokens.prompt" in metrics, f"tokens.prompt missing; got {metrics}"
        assert "tokens.completion" in metrics
        assert "tokens.total" in metrics
        assert "cost.usd" in metrics
        expected_cost = pricing_cost("gpt-4o-mini", 100, 50)
        assert abs(metrics["cost.usd"] - expected_cost) < 1e-10
