"""LiteMLflow callback handler for LangChain.

Instruments LangChain runs into LiteMLflow as spans + metrics + token costs.

Usage::

    from litemlflow import Client
    from litemlflow.langchain import LiteMLflowCallbackHandler

    client = Client("http://localhost:5000")
    handler = LiteMLflowCallbackHandler(client)

    chain.invoke({"question": "..."}, config={"callbacks": [handler]})

The handler lazily imports ``langchain_core`` at first instantiation rather than
at module import time, so that the SDK can be imported without langchain
installed (it will only fail when you try to create an instance).

## Span flushing strategy

Spans are accumulated in memory as they close and flushed to the server in
a single ``POST /api/v1/traces`` batch when the last root-level span closes.
Batching avoids the foreign-key ordering problem (child posted before its
parent) and minimises HTTP round-trips.

For safety, any span whose enclosing root has no matching ``on_chain_end``
call (e.g., a bare LLM call) is flushed immediately on close.
"""

from __future__ import annotations

import secrets
import time
import traceback
from typing import Any
from uuid import UUID

from litemlflow.client import Client, LiteMLflowError
from litemlflow.langchain.pricing import cost

_LANGCHAIN_IMPORT_ERROR = (
    "langchain-core is required for LiteMLflowCallbackHandler. "
    "Install it with: pip install 'litemlflow[langchain]'"
)

_DEFAULT_EXPERIMENT_NAME = "langchain"


def _truncate(text: str, max_len: int = 1000) -> str:
    if len(text) <= max_len:
        return text
    return text[:max_len] + f"... [truncated {len(text) - max_len} chars]"


def _get_or_create_experiment(client: Client, name: str) -> int:
    """Return experiment id, creating it if it doesn't exist yet."""
    try:
        exp = client.get_experiment_by_name(name)
        return int(exp["experiment_id"])
    except LiteMLflowError:
        return client.create_experiment(name)


def LiteMLflowCallbackHandler(  # noqa: N802 — intentionally uppercase to look like a class
    client: Client,
    run_id: str | None = None,
    experiment_id: int | None = None,
    auto_metrics: bool = True,
) -> "_LiteMLflowCallbackHandlerImpl":
    """Create a LangChain callback handler that auto-instruments runs into LiteMLflow.

    This is a factory function that behaves like a class constructor. It lazily
    imports ``langchain_core`` so that the module can be imported without
    langchain installed.

    Args:
        client: A connected :class:`litemlflow.Client` instance.
        run_id: If provided, attach spans to this existing run.
        experiment_id: If provided (and ``run_id`` is not), create a new run
            in this experiment.
        auto_metrics: Log token usage and cost metrics on ``on_llm_end``
            (default: ``True``).

    Returns:
        A ``BaseCallbackHandler`` subclass instance.

    Raises:
        ImportError: If ``langchain-core`` is not installed.
    """
    try:
        from langchain_core.callbacks import BaseCallbackHandler
    except ImportError as exc:
        raise ImportError(_LANGCHAIN_IMPORT_ERROR) from exc

    impl_cls = _get_impl_class(BaseCallbackHandler)
    instance = object.__new__(impl_cls)
    _LiteMLflowCallbackHandlerImpl.__init__(
        instance, client, run_id=run_id, experiment_id=experiment_id, auto_metrics=auto_metrics
    )
    return instance


# Cache for the dynamically built concrete class.
_IMPL_CLASS_CACHE: dict[int, type] = {}


def _get_impl_class(base_cls: type) -> type:
    """Return (and cache) a concrete BaseCallbackHandler subclass."""
    key = id(base_cls)
    if key in _IMPL_CLASS_CACHE:
        return _IMPL_CLASS_CACHE[key]

    # _LiteMLflowCallbackHandlerImpl must come FIRST so our callbacks override
    # the default no-op stubs in BaseCallbackHandler / its mixins.
    impl_cls = type(
        "_LiteMLflowHandlerConcrete",
        (_LiteMLflowCallbackHandlerImpl, base_cls),
        {},
    )
    _IMPL_CLASS_CACHE[key] = impl_cls
    return impl_cls


class _LiteMLflowCallbackHandlerImpl:
    """Real implementation; mixed into BaseCallbackHandler at construction time.

    Span IDs are pre-generated at ``_open_span`` time so that child spans can
    reference their parent's ID while the parent is still open.

    All spans are accumulated in a buffer and flushed as a single batch when
    the last open root span closes. This avoids FK violations from posting a
    child before its parent.
    """

    def __init__(
        self,
        client: Client,
        run_id: str | None = None,
        experiment_id: int | None = None,
        auto_metrics: bool = True,
    ) -> None:
        try:
            super().__init__()  # type: ignore[call-arg]
        except TypeError:
            pass

        self._client = client
        self._auto_metrics = auto_metrics

        # Resolve / create the run.
        if run_id is not None:
            self._run_id = run_id
        elif experiment_id is not None:
            ts = int(time.time())
            run = client.create_run(experiment_id, name=f"langchain-trace-{ts}")
            self._run_id = run.id
        else:
            exp_id = _get_or_create_experiment(client, _DEFAULT_EXPERIMENT_NAME)
            ts = int(time.time())
            run = client.create_run(exp_id, name=f"langchain-trace-{ts}")
            self._run_id = run.id

        # One shared trace id per handler instance.
        self._trace_id: str = secrets.token_hex(16)

        # Maps LC run_id string → pending span dict (open spans):
        #   span_id, name, attrs, parent_lc_id, start_ns
        self._open: dict[str, dict[str, Any]] = {}

        # Completed spans waiting to be flushed (in close order).
        self._buffer: list[dict[str, Any]] = []

        # Counts how many spans at root level (parent_lc_id is None) are
        # currently open.  When it drops to 0, we flush all buffered spans.
        self._root_depth: int = 0

    # ---------------------------------------------------------------- internal

    def _open_span(
        self,
        lc_run_id: UUID,
        lc_parent_run_id: UUID | None,
        name: str,
        *,
        attrs: dict[str, Any] | None = None,
    ) -> str:
        """Register an open span and return its pre-generated span_id."""
        key = str(lc_run_id)
        span_id = secrets.token_hex(8)
        self._open[key] = {
            "span_id": span_id,
            "name": name,
            "attrs": attrs or {},
            "parent_lc_id": str(lc_parent_run_id) if lc_parent_run_id else None,
            "start_ns": time.time_ns(),
        }
        if lc_parent_run_id is None:
            self._root_depth += 1
        return span_id

    def _close_span(
        self,
        lc_run_id: UUID,
        *,
        extra_attrs: dict[str, Any] | None = None,
        status_code: str = "OK",
        status_message: str = "",
    ) -> str | None:
        """Move a span from open → buffer; flush buffer if no roots remain open."""
        key = str(lc_run_id)
        pending = self._open.pop(key, None)
        if pending is None:
            return None

        end_ns = time.time_ns()
        was_root = pending.get("parent_lc_id") is None

        # Resolve server-side parent_id from our pre-generated sibling map.
        parent_span_id: str | None = None
        parent_lc_id = pending.get("parent_lc_id")
        if parent_lc_id is not None:
            # Parent may still be open or already closed (in buffer).
            parent_entry = self._open.get(parent_lc_id)
            if parent_entry is not None:
                parent_span_id = parent_entry["span_id"]
            else:
                # Search buffer (closed parents).
                for buffered in self._buffer:
                    if buffered.get("_lc_id") == parent_lc_id:
                        parent_span_id = buffered["id"]
                        break

        attrs: dict[str, Any] = dict(pending.get("attrs", {}))
        if extra_attrs:
            attrs.update(extra_attrs)

        serialised: dict[str, Any] = {}
        for k, v in attrs.items():
            if isinstance(v, (str, int, float, bool)):
                serialised[k] = v
            else:
                serialised[k] = str(v)

        span_record: dict[str, Any] = {
            "_lc_id": key,  # internal; stripped before posting
            "id": pending["span_id"],
            "trace_id": self._trace_id,
            "parent_id": parent_span_id,
            "run_id": self._run_id,
            "name": pending["name"],
            "span_kind": "INTERNAL",
            "start_time_ns": pending["start_ns"],
            "end_time_ns": end_ns,
            "attributes": serialised,
            "status_code": status_code,
            "status_message": status_message,
        }
        self._buffer.append(span_record)

        if was_root:
            self._root_depth = max(0, self._root_depth - 1)

        # Flush when no root spans remain open.
        if self._root_depth == 0 and self._buffer:
            self._flush()

        return pending["span_id"]

    def _flush(self) -> None:
        """POST all buffered spans in topological order (parents before children)."""
        if not self._buffer:
            return

        # Sort spans so parents come before children.
        # A root span (parent_id=None) always comes first; otherwise preserve
        # the close order which is innermost-first (reverse topological), so
        # we reverse.
        ordered = list(reversed(self._buffer))
        self._buffer = []

        # Strip the internal _lc_id field before posting.
        clean_spans = [{k: v for k, v in s.items() if k != "_lc_id"} for s in ordered]

        try:
            self._client._request(  # type: ignore[attr-defined]
                "POST",
                "/api/v1/traces",
                json={"trace_id": self._trace_id, "spans": clean_spans},
            )
        except Exception:
            pass  # Tracing is best-effort; never crash the chain.

    # ---------------------------------------------------------------- chain

    def on_chain_start(
        self,
        serialized: dict[str, Any],
        inputs: dict[str, Any],
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        tags: list[str] | None = None,
        metadata: dict[str, Any] | None = None,
        **kwargs: Any,
    ) -> None:
        chain_type = serialized.get("name") or (
            serialized.get("id", ["unknown"])[-1]
        )
        attrs: dict[str, Any] = {
            "name": str(chain_type),
            "run_id": str(run_id),
            "inputs": _truncate(str(inputs)),
        }
        if parent_run_id is not None:
            attrs["parent_run_id"] = str(parent_run_id)
        if tags:
            attrs["tags"] = str(tags)
        if metadata:
            attrs["metadata"] = str(metadata)

        self._open_span(run_id, parent_run_id, f"chain:{chain_type}", attrs=attrs)

    def on_chain_end(
        self,
        outputs: dict[str, Any],
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        **kwargs: Any,
    ) -> None:
        self._close_span(
            run_id,
            extra_attrs={"outputs": _truncate(str(outputs))},
            status_code="OK",
        )

    def on_chain_error(
        self,
        error: BaseException,
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        **kwargs: Any,
    ) -> None:
        tb = traceback.format_exc()
        self._close_span(
            run_id,
            extra_attrs={
                "error": str(error),
                "traceback": _truncate(tb, 2000),
            },
            status_code="ERROR",
            status_message=str(error),
        )

    # ------------------------------------------------------------------ llm

    def on_llm_start(
        self,
        serialized: dict[str, Any],
        prompts: list[str],
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        tags: list[str] | None = None,
        metadata: dict[str, Any] | None = None,
        invocation_params: dict[str, Any] | None = None,
        **kwargs: Any,
    ) -> None:
        model_name = (
            (invocation_params or {}).get("model_name")
            or (invocation_params or {}).get("model")
            or serialized.get("name")
            or "unknown"
        )
        attrs: dict[str, Any] = {
            "model": str(model_name),
            "run_id": str(run_id),
            "prompts": _truncate(str(prompts)),
        }
        if invocation_params:
            attrs["invocation_params"] = _truncate(str(invocation_params))

        self._open_span(run_id, parent_run_id, f"llm:{model_name}", attrs=attrs)

    def on_llm_end(
        self,
        response: Any,
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        **kwargs: Any,
    ) -> None:
        extra_attrs: dict[str, Any] = {}

        if self._auto_metrics:
            llm_output = getattr(response, "llm_output", None) or {}
            token_usage = (
                llm_output.get("token_usage") if isinstance(llm_output, dict) else {}
            ) or {}
            if token_usage:
                prompt_tokens = int(token_usage.get("prompt_tokens", 0))
                completion_tokens = int(token_usage.get("completion_tokens", 0))
                total_tokens = int(
                    token_usage.get("total_tokens", prompt_tokens + completion_tokens)
                )

                pending = self._open.get(str(run_id), {})
                model_name: str = pending.get("attrs", {}).get("model", "unknown")
                cost_usd = cost(model_name, prompt_tokens, completion_tokens)

                try:
                    self._client.log_metric(self._run_id, "tokens.prompt", float(prompt_tokens))
                    self._client.log_metric(
                        self._run_id, "tokens.completion", float(completion_tokens)
                    )
                    self._client.log_metric(self._run_id, "tokens.total", float(total_tokens))
                    self._client.log_metric(self._run_id, "cost.usd", cost_usd)
                except Exception:
                    pass  # Metrics are best-effort; never crash the chain.

                extra_attrs.update(
                    {
                        "tokens.prompt": prompt_tokens,
                        "tokens.completion": completion_tokens,
                        "tokens.total": total_tokens,
                        "cost.usd": cost_usd,
                    }
                )

        self._close_span(run_id, extra_attrs=extra_attrs, status_code="OK")

    def on_llm_error(
        self,
        error: BaseException,
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        **kwargs: Any,
    ) -> None:
        tb = traceback.format_exc()
        self._close_span(
            run_id,
            extra_attrs={"error": str(error), "traceback": _truncate(tb, 2000)},
            status_code="ERROR",
            status_message=str(error),
        )

    # --------------------------------------------------------- chat model

    def on_chat_model_start(
        self,
        serialized: dict[str, Any],
        messages: list[list[Any]],
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        tags: list[str] | None = None,
        metadata: dict[str, Any] | None = None,
        invocation_params: dict[str, Any] | None = None,
        **kwargs: Any,
    ) -> None:
        model_name = (
            (invocation_params or {}).get("model_name")
            or (invocation_params or {}).get("model")
            or serialized.get("name")
            or "unknown"
        )
        flat_msgs = [m for sublist in messages for m in sublist]
        attrs: dict[str, Any] = {
            "model": str(model_name),
            "run_id": str(run_id),
            "prompts": _truncate(str([str(m) for m in flat_msgs])),
        }
        if invocation_params:
            attrs["invocation_params"] = _truncate(str(invocation_params))

        self._open_span(run_id, parent_run_id, f"chat:{model_name}", attrs=attrs)

    # ---------------------------------------------------------------- tool

    def on_tool_start(
        self,
        serialized: dict[str, Any],
        input_str: str,
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        tags: list[str] | None = None,
        metadata: dict[str, Any] | None = None,
        **kwargs: Any,
    ) -> None:
        tool_name = serialized.get("name") or "unknown_tool"
        attrs: dict[str, Any] = {
            "tool": str(tool_name),
            "run_id": str(run_id),
            "input": _truncate(str(input_str)),
        }
        self._open_span(run_id, parent_run_id, f"tool:{tool_name}", attrs=attrs)

    def on_tool_end(
        self,
        output: Any,
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        **kwargs: Any,
    ) -> None:
        self._close_span(
            run_id,
            extra_attrs={"output": _truncate(str(output))},
            status_code="OK",
        )

    def on_tool_error(
        self,
        error: BaseException,
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        **kwargs: Any,
    ) -> None:
        tb = traceback.format_exc()
        self._close_span(
            run_id,
            extra_attrs={"error": str(error), "traceback": _truncate(tb, 2000)},
            status_code="ERROR",
            status_message=str(error),
        )

    # ---------------------------------------------------------- retriever

    def on_retriever_start(
        self,
        serialized: dict[str, Any],
        query: str,
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        tags: list[str] | None = None,
        metadata: dict[str, Any] | None = None,
        **kwargs: Any,
    ) -> None:
        retriever_name = serialized.get("name") or "retriever"
        attrs: dict[str, Any] = {
            "retriever": str(retriever_name),
            "run_id": str(run_id),
            "query": _truncate(query),
        }
        self._open_span(run_id, parent_run_id, f"retriever:{retriever_name}", attrs=attrs)

    def on_retriever_end(
        self,
        documents: list[Any],
        *,
        run_id: UUID,
        parent_run_id: UUID | None = None,
        **kwargs: Any,
    ) -> None:
        doc_count = len(documents)
        first_three_ids: list[str] = []
        for i, doc in enumerate(documents[:3]):
            doc_id = getattr(doc, "id", None)
            if doc_id is None:
                meta = getattr(doc, "metadata", {})
                doc_id = meta.get("id", f"doc-{i}") if isinstance(meta, dict) else f"doc-{i}"
            first_three_ids.append(str(doc_id))

        self._close_span(
            run_id,
            extra_attrs={
                "documents.count": doc_count,
                "documents.first_ids": str(first_three_ids),
            },
            status_code="OK",
        )
