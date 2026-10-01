"""LiteMLflow event handler for LlamaIndex.

Instruments LlamaIndex query pipelines into LiteMLflow as spans + metrics +
token costs.

Usage::

    from litemlflow import Client
    from litemlflow.llamaindex import LiteMLflowEventHandler
    import llama_index.core.instrumentation as instrument

    client = Client("http://localhost:5000")
    handler = LiteMLflowEventHandler(client)

    dispatcher = instrument.get_dispatcher()
    dispatcher.add_event_handler(handler)

    # Now run any LlamaIndex query — spans are recorded automatically.
    response = query_engine.query("What is LiteMLflow?")

The handler lazily imports ``llama_index.core.instrumentation.event_handlers``
at first instantiation rather than at module import time, so that the SDK can be
imported without llama-index-core installed (it will only fail when you try to
create an instance).

## Span flushing strategy

Spans are accumulated in memory as they close and flushed to the server in a
single ``POST /api/v1/traces`` batch when the last root-level span closes.
The root span is the ``QueryStartEvent``/``QueryEndEvent`` pair.  Batching
avoids the foreign-key ordering problem (child posted before its parent) and
minimises HTTP round-trips.

For safety, any span that closes when no root is currently open is flushed
immediately.
"""

from __future__ import annotations

import secrets
import threading
import time
import traceback
from typing import Any

from litemlflow.client import Client, LiteMLflowError
from litemlflow._pricing import cost

_LLAMAINDEX_IMPORT_ERROR = (
    "llama-index-core is required for LiteMLflowEventHandler. "
    "Install it with: pip install 'litemlflow[llamaindex]'"
)

_DEFAULT_EXPERIMENT_NAME = "llamaindex"


def _truncate(text: str, max_len: int = 1000) -> str:
    if len(text) <= max_len:
        return text
    return text[:max_len] + f"... [truncated {len(text) - max_len} chars]"


# Events llama-index-core emits that intentionally map to no span (their work
# is already covered by a recorded span); don't warn about them.
_IGNORED_EVENTS = frozenset(
    {
        "GetResponseStartEvent",
        "GetResponseEndEvent",
        "LLMPredictStartEvent",
        "LLMPredictEndEvent",
        "LLMStructuredPredictStartEvent",
        "LLMStructuredPredictEndEvent",
        "StreamChatStartEvent",
        "StreamChatEndEvent",
        "StreamChatDeltaReceivedEvent",
        "StreamChatErrorEvent",
    }
)


def _event_key(event: Any) -> str:
    """Key that matches a ``*StartEvent`` with its ``*EndEvent``.

    In llama-index-core every event gets a fresh ``id_`` (uuid4), so start and
    end events never share it. What they share is ``span_id`` — the id of the
    dispatcher span (the ``@dispatcher.span``-decorated call) that emitted
    both. The event family (class name without Start/End/Event) is folded in
    so distinct pairs emitted inside one dispatcher span cannot collide.
    Falls back to ``id_`` for events without a span id.
    """
    span_id = getattr(event, "span_id", None)
    if span_id:
        family = type(event).__name__
        for suffix in ("StartEvent", "EndEvent"):
            if family.endswith(suffix):
                family = family[: -len(suffix)]
                break
        return f"{family}:{span_id}"
    return str(getattr(event, "id_", id(event)))


def _get_or_create_experiment(client: Client, name: str) -> int:
    """Return experiment id, creating it if it doesn't exist yet.

    Only a 404 means "missing" (a transport/auth error must surface as-is),
    and a concurrent creator winning the race is resolved by re-reading.
    """
    try:
        exp = client.get_experiment_by_name(name)
        return int(exp["experiment_id"])
    except LiteMLflowError as exc:
        if exc.status != 404:
            raise
    try:
        return client.create_experiment(name)
    except LiteMLflowError as exc:
        if exc.code != "RESOURCE_ALREADY_EXISTS":
            raise
        return int(client.get_experiment_by_name(name)["experiment_id"])


def LiteMLflowEventHandler(  # noqa: N802 — intentionally uppercase to look like a class
    client: Client,
    run_id: str | None = None,
    experiment_id: int | None = None,
) -> "_LiteMLflowEventHandlerImpl":
    """Create a LlamaIndex event handler that auto-instruments pipelines into LiteMLflow.

    This is a factory function that behaves like a class constructor. It lazily
    imports ``llama_index.core.instrumentation.event_handlers`` so that the
    module can be imported without llama-index-core installed.

    Args:
        client: A connected :class:`litemlflow.Client` instance.
        run_id: If provided, attach spans to this existing run.
        experiment_id: If provided (and ``run_id`` is not), create a new run
            in this experiment.

    Returns:
        A ``BaseEventHandler`` subclass instance.

    Raises:
        ImportError: If ``llama-index-core`` is not installed.
    """
    try:
        from llama_index.core.instrumentation.event_handlers import BaseEventHandler
    except ImportError as exc:
        raise ImportError(_LLAMAINDEX_IMPORT_ERROR) from exc

    impl_cls = _get_impl_class(BaseEventHandler)
    instance = object.__new__(impl_cls)
    _LiteMLflowEventHandlerImpl.__init__(instance, client, run_id=run_id, experiment_id=experiment_id)
    return instance


# Cache for the dynamically built concrete class.
_IMPL_CLASS_CACHE: dict[int, type] = {}


def _get_impl_class(base_cls: type) -> type:
    """Return (and cache) a concrete BaseEventHandler subclass."""
    key = id(base_cls)
    if key in _IMPL_CLASS_CACHE:
        return _IMPL_CLASS_CACHE[key]

    # _LiteMLflowEventHandlerImpl must come FIRST so our methods override base.
    impl_cls = type(
        "_LiteMLflowEventHandlerConcrete",
        (_LiteMLflowEventHandlerImpl, base_cls),
        {},
    )
    _IMPL_CLASS_CACHE[key] = impl_cls
    return impl_cls


class _LiteMLflowEventHandlerImpl:
    """Real implementation; mixed into BaseEventHandler at construction time.

    LlamaIndex's event system uses a single ``handle(event)`` method that
    dispatches on the event type.  We open a span at ``*StartEvent`` and close
    it at the matching ``*EndEvent``, keyed by ``event.id_`` which is the same
    string across the start/end pair (LlamaIndex sets this on the dispatcher).

    Span IDs are pre-generated at open time so that child spans can reference
    their parent's ID while the parent is still open.

    All spans are accumulated in a buffer and flushed as a single batch when
    the last open root span (QueryStartEvent) closes.
    """

    def __init__(
        self,
        client: Client,
        run_id: str | None = None,
        experiment_id: int | None = None,
    ) -> None:
        try:
            super().__init__()  # type: ignore[call-arg]
        except TypeError:
            pass

        self._client = client

        # Resolve / create the run.
        if run_id is not None:
            self._run_id = run_id
        elif experiment_id is not None:
            ts = int(time.time())
            run = client.create_run(experiment_id, name=f"llamaindex-trace-{ts}")
            self._run_id = run.id
        else:
            exp_id = _get_or_create_experiment(client, _DEFAULT_EXPERIMENT_NAME)
            ts = int(time.time())
            run = client.create_run(exp_id, name=f"llamaindex-trace-{ts}")
            self._run_id = run.id

        # One shared trace id per handler instance.
        self._trace_id: str = secrets.token_hex(16)

        # Maps event_id string → pending span dict (open spans):
        #   span_id, name, attrs, parent_event_id, start_ns
        self._open: dict[str, dict[str, Any]] = {}

        # Stack tracking the nesting of spans so children know their parent.
        # We push event_id on open and pop on close.
        self._stack: list[str] = []

        # Completed spans waiting to be flushed (in close order).
        self._buffer: list[dict[str, Any]] = []

        # Counts how many root-level spans (QueryStart) are currently open.
        # When it drops to 0, we flush all buffered spans.
        self._root_depth: int = 0

        # The dispatcher may call handle() from several threads (parallel
        # retrieval, async workers); guard the shared span bookkeeping.
        self._lock = threading.RLock()

    # --------------------------------------------------------------- internal

    def _open_span(
        self,
        event_id: str,
        name: str,
        *,
        attrs: dict[str, Any] | None = None,
        is_root: bool = False,
        dispatch_span_id: str | None = None,
    ) -> str:
        """Register an open span and return its pre-generated span_id."""
        span_id = secrets.token_hex(8)
        with self._lock:
            # Parent is the top of the stack (if any).
            parent_event_id: str | None = self._stack[-1] if self._stack else None
            # A span opened with nothing enclosing it is a root even if it is
            # not a query (e.g. a bare llm.complete() call); otherwise its
            # children would be flushed before it, breaking the parent FK.
            is_root = is_root or parent_event_id is None
            self._open[event_id] = {
                "span_id": span_id,
                "name": name,
                "attrs": attrs or {},
                "parent_event_id": parent_event_id,
                "start_ns": time.time_ns(),
                "is_root": is_root,
                "dispatch_span_id": dispatch_span_id,
            }
            self._stack.append(event_id)
            if is_root:
                self._root_depth += 1
        return span_id

    def _close_span(
        self,
        event_id: str,
        *,
        extra_attrs: dict[str, Any] | None = None,
        status_code: str = "OK",
        status_message: str = "",
    ) -> str | None:
        """Move a span from open → buffer; flush buffer if no roots remain open."""
        to_post: list[dict[str, Any]] = []
        with self._lock:
            span_id = self._close_span_locked(
                event_id,
                extra_attrs=extra_attrs,
                status_code=status_code,
                status_message=status_message,
            )
            if span_id is not None and self._root_depth == 0 and self._buffer:
                to_post = self._buffer
                self._buffer = []
        if to_post:
            self._post(to_post)
        return span_id

    def _close_span_locked(
        self,
        event_id: str,
        *,
        extra_attrs: dict[str, Any] | None,
        status_code: str,
        status_message: str,
    ) -> str | None:
        pending = self._open.pop(event_id, None)
        if pending is None:
            return None

        # Pop from stack.
        try:
            self._stack.remove(event_id)
        except ValueError:
            pass

        end_ns = time.time_ns()
        is_root = pending.get("is_root", False)

        # Resolve server-side parent_id from our pre-generated sibling map.
        parent_span_id: str | None = None
        parent_event_id = pending.get("parent_event_id")
        if parent_event_id is not None:
            parent_entry = self._open.get(parent_event_id)
            if parent_entry is not None:
                parent_span_id = parent_entry["span_id"]
            else:
                # Search buffer (closed parents).
                for buffered in self._buffer:
                    if buffered.get("_event_id") == parent_event_id:
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
            "_event_id": event_id,  # internal; stripped before posting
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

        if is_root:
            self._root_depth = max(0, self._root_depth - 1)

        return pending["span_id"]

    def _flush(self) -> None:
        """POST all buffered spans in topological order (parents before children)."""
        with self._lock:
            buffered = self._buffer
            self._buffer = []
        if buffered:
            self._post(buffered)

    def _post(self, buffered: list[dict[str, Any]]) -> None:
        # Spans are buffered in close order (innermost first), so reverse gives
        # us parents before children (topological order).
        ordered = list(reversed(buffered))

        # Strip the internal _event_id field before posting.
        clean_spans = [{k: v for k, v in s.items() if k != "_event_id"} for s in ordered]

        try:
            self._client._request(  # type: ignore[attr-defined]
                "POST",
                "/api/v1/traces",
                json={"trace_id": self._trace_id, "spans": clean_spans},
                idempotent=True,  # spans are upserted by id; safe to retry
            )
        except Exception:
            pass  # Tracing is best-effort; never crash the pipeline.

    # ----------------------------------------------------------- event dispatch

    # _unknown_event_warned is a per-instance set so we log at most one
    # warning per unrecognized event class name. Keeps tracing observable when
    # llama-index-core renames events in a new release without filling logs.
    def handle(self, event: Any, **kwargs: Any) -> None:  # type: ignore[override]
        """Dispatch a LlamaIndex event to the appropriate handler method."""
        cls_name = type(event).__name__
        handler_name = f"_on_{cls_name}"
        handler = getattr(self, handler_name, None)
        if handler is None:
            if cls_name in _IGNORED_EVENTS or cls_name.endswith("InProgressEvent"):
                return
            warned = getattr(self, "_unknown_event_warned", None)
            if warned is None:
                warned = set()
                self._unknown_event_warned = warned
            if cls_name not in warned:
                warned.add(cls_name)
                import logging
                logging.getLogger(__name__).warning(
                    "litemlflow.llamaindex: no handler for event class %r — "
                    "spans for this event type will be silently dropped. "
                    "This usually indicates a llama-index-core version mismatch.",
                    cls_name,
                )
            return
        try:
            handler(event)
        except Exception:
            pass  # Best-effort; never crash the pipeline.

    # ------------------------------------------------------------------ query

    def _on_QueryStartEvent(self, event: Any) -> None:
        event_id = _event_key(event)
        query = getattr(event, "query", None)
        attrs: dict[str, Any] = {"event.type": "query"}
        if query is not None:
            attrs["query"] = _truncate(str(query))
        self._open_span(
            event_id,
            f"query:{str(getattr(event, 'id_', event_id))[:8]}",
            attrs=attrs,
            is_root=True,
            dispatch_span_id=getattr(event, "span_id", None),
        )

    def _on_QueryEndEvent(self, event: Any) -> None:
        event_id = _event_key(event)
        response = getattr(event, "response", None)
        extra: dict[str, Any] = {}
        if response is not None:
            extra["response"] = _truncate(str(response))
        self._close_span(event_id, extra_attrs=extra, status_code="OK")

    # --------------------------------------------------------------- retrieval

    def _on_RetrievalStartEvent(self, event: Any) -> None:
        event_id = _event_key(event)
        attrs: dict[str, Any] = {"event.type": "retrieval"}
        self._open_span(
            event_id, "retrieval", attrs=attrs, dispatch_span_id=getattr(event, "span_id", None)
        )

    def _on_RetrievalEndEvent(self, event: Any) -> None:
        event_id = _event_key(event)
        nodes = getattr(event, "nodes", None)
        extra: dict[str, Any] = {}
        if nodes is not None:
            extra["nodes.count"] = len(nodes) if hasattr(nodes, "__len__") else str(nodes)
        self._close_span(event_id, extra_attrs=extra, status_code="OK")

    # --------------------------------------------------------------- synthesis

    def _on_SynthesisStartEvent(self, event: Any) -> None:
        event_id = _event_key(event)
        attrs: dict[str, Any] = {"event.type": "synthesis"}
        self._open_span(
            event_id, "synthesis", attrs=attrs, dispatch_span_id=getattr(event, "span_id", None)
        )

    def _on_SynthesisEndEvent(self, event: Any) -> None:
        event_id = _event_key(event)
        response = getattr(event, "response", None)
        extra: dict[str, Any] = {}
        if response is not None:
            extra["response"] = _truncate(str(response))
        self._close_span(event_id, extra_attrs=extra, status_code="OK")

    # llama-index-core names these SynthesizeStartEvent/SynthesizeEndEvent.
    _on_SynthesizeStartEvent = _on_SynthesisStartEvent
    _on_SynthesizeEndEvent = _on_SynthesisEndEvent

    # --------------------------------------------------------- LLM completion

    def _on_LLMCompletionStartEvent(self, event: Any) -> None:
        event_id = _event_key(event)
        model = _extract_model(event)
        attrs: dict[str, Any] = {"event.type": "llm_completion", "model": model}
        prompt = getattr(event, "prompt", None)
        if prompt is not None:
            attrs["prompt"] = _truncate(str(prompt))
        self._open_span(
            event_id, f"llm:{model}", attrs=attrs, dispatch_span_id=getattr(event, "span_id", None)
        )

    def _on_LLMCompletionEndEvent(self, event: Any) -> None:
        event_id = _event_key(event)
        extra: dict[str, Any] = {}

        # Extract token usage — LlamaIndex stores this in event.response or
        # event.token_usage depending on the version.
        token_usage = _extract_token_usage(event)
        if token_usage:
            prompt_tokens = int(token_usage.get("prompt_tokens") or token_usage.get("input_tokens") or 0)
            completion_tokens = int(
                token_usage.get("completion_tokens") or token_usage.get("output_tokens") or 0
            )
            total_tokens = int(
                token_usage.get("total_tokens") or prompt_tokens + completion_tokens
            )

            # Look up model from the open span's attrs.
            with self._lock:
                pending = self._open.get(event_id, {})
            model: str = pending.get("attrs", {}).get("model", "unknown")
            cost_usd = cost(model, prompt_tokens, completion_tokens)

            try:
                self._client.log_metric(self._run_id, "tokens.prompt", float(prompt_tokens))
                self._client.log_metric(self._run_id, "tokens.completion", float(completion_tokens))
                self._client.log_metric(self._run_id, "tokens.total", float(total_tokens))
                self._client.log_metric(self._run_id, "cost.usd", cost_usd)
            except Exception:
                pass  # Best-effort.

            extra.update(
                {
                    "tokens.prompt": prompt_tokens,
                    "tokens.completion": completion_tokens,
                    "tokens.total": total_tokens,
                    "cost.usd": cost_usd,
                }
            )

        response = getattr(event, "response", None)
        if response is not None:
            extra["response"] = _truncate(str(response))

        self._close_span(event_id, extra_attrs=extra, status_code="OK")

    # ------------------------------------------------------------------- chat

    def _on_LLMChatStartEvent(self, event: Any) -> None:
        event_id = _event_key(event)
        model = _extract_model(event)
        attrs: dict[str, Any] = {"event.type": "llm_chat", "model": model}
        messages = getattr(event, "messages", None)
        if messages is not None:
            attrs["messages"] = _truncate(str(messages))
        self._open_span(
            event_id, f"chat:{model}", attrs=attrs, dispatch_span_id=getattr(event, "span_id", None)
        )

    def _on_LLMChatEndEvent(self, event: Any) -> None:
        event_id = _event_key(event)
        extra: dict[str, Any] = {}

        token_usage = _extract_token_usage(event)
        if token_usage:
            prompt_tokens = int(token_usage.get("prompt_tokens") or token_usage.get("input_tokens") or 0)
            completion_tokens = int(
                token_usage.get("completion_tokens") or token_usage.get("output_tokens") or 0
            )
            total_tokens = int(
                token_usage.get("total_tokens") or prompt_tokens + completion_tokens
            )

            with self._lock:
                pending = self._open.get(event_id, {})
            model: str = pending.get("attrs", {}).get("model", "unknown")
            cost_usd = cost(model, prompt_tokens, completion_tokens)

            try:
                self._client.log_metric(self._run_id, "tokens.prompt", float(prompt_tokens))
                self._client.log_metric(self._run_id, "tokens.completion", float(completion_tokens))
                self._client.log_metric(self._run_id, "tokens.total", float(total_tokens))
                self._client.log_metric(self._run_id, "cost.usd", cost_usd)
            except Exception:
                pass  # Best-effort.

            extra.update(
                {
                    "tokens.prompt": prompt_tokens,
                    "tokens.completion": completion_tokens,
                    "tokens.total": total_tokens,
                    "cost.usd": cost_usd,
                }
            )

        response = getattr(event, "response", None)
        if response is not None:
            extra["response"] = _truncate(str(response))

        self._close_span(event_id, extra_attrs=extra, status_code="OK")

    # ---------------------------------------------------------------- embedding

    def _on_EmbeddingStartEvent(self, event: Any) -> None:
        event_id = _event_key(event)
        model = _extract_model(event)
        attrs: dict[str, Any] = {"event.type": "embedding", "model": model}
        self._open_span(
            event_id, f"embed:{model}", attrs=attrs, dispatch_span_id=getattr(event, "span_id", None)
        )

    def _on_EmbeddingEndEvent(self, event: Any) -> None:
        event_id = _event_key(event)
        chunks = getattr(event, "chunks", None)
        extra: dict[str, Any] = {}
        if chunks is not None:
            extra["chunks.count"] = len(chunks) if hasattr(chunks, "__len__") else str(chunks)
        self._close_span(event_id, extra_attrs=extra, status_code="OK")

    # ------------------------------------------------------------------ errors

    def _on_ExceptionEvent(self, event: Any) -> None:
        """Handle a generic exception event — close any open span as ERROR."""
        exception = getattr(event, "exception", None)
        # ExceptionEvent has its own id_; it shares only the dispatcher
        # span_id with the start event of the span that failed.
        dispatch_span_id = getattr(event, "span_id", None)
        with self._lock:
            keys = [
                k for k, v in self._open.items()
                if dispatch_span_id and v.get("dispatch_span_id") == dispatch_span_id
            ]
            legacy_key = str(getattr(event, "id_", id(event)))
            if not keys and legacy_key in self._open:
                keys = [legacy_key]
        if isinstance(exception, BaseException):
            tb = "".join(
                traceback.format_exception(type(exception), exception, exception.__traceback__)
            )
        else:
            tb = traceback.format_exc()
        for key in keys:
            self._close_span(
                key,
                extra_attrs={
                    "error": str(exception) if exception else "unknown",
                    "traceback": _truncate(tb, 2000),
                },
                status_code="ERROR",
                status_message=str(exception) if exception else "",
            )


# ------------------------------------------------------------------ helpers

def _extract_model(event: Any) -> str:
    """Try various attribute paths that LlamaIndex uses for model names."""
    for attr in ("model", "model_name", "model_dict"):
        val = getattr(event, attr, None)
        if val is not None:
            if isinstance(val, dict):
                return str(val.get("model_name", val.get("model", "unknown")))
            return str(val)
    # Try going through serialized llm
    llm = getattr(event, "llm", None)
    if llm is not None:
        for attr in ("model", "model_name"):
            val = getattr(llm, attr, None)
            if val is not None:
                return str(val)
    return "unknown"


def _usage_to_dict(usage: Any) -> dict[str, Any] | None:
    """Normalise a usage dict/object (OpenAI- or Anthropic-style) to a dict."""
    if usage is None:
        return None
    if isinstance(usage, dict):
        return usage or None
    pt = getattr(usage, "prompt_tokens", None)
    if pt is None:
        pt = getattr(usage, "input_tokens", None)
    ct = getattr(usage, "completion_tokens", None)
    if ct is None:
        ct = getattr(usage, "output_tokens", None)
    if pt is None and ct is None:
        return None
    pt, ct = int(pt or 0), int(ct or 0)
    total = getattr(usage, "total_tokens", None)
    return {
        "prompt_tokens": pt,
        "completion_tokens": ct,
        "total_tokens": int(total) if total else pt + ct,
    }


def _extract_token_usage(event: Any) -> dict[str, Any] | None:
    """Extract token usage dict from a LlamaIndex end event."""
    # Direct attribute (some versions).
    usage = _usage_to_dict(getattr(event, "token_usage", None))
    if usage:
        return usage

    # Nested inside response object.
    response = getattr(event, "response", None)
    if response is not None:
        raw = getattr(response, "raw", None)
        # raw is a dict for some providers and the provider SDK's response
        # object (e.g. openai ChatCompletion with a .usage attribute) for others.
        raw_usage = raw.get("usage") if isinstance(raw, dict) else getattr(raw, "usage", None)
        usage = _usage_to_dict(raw_usage)
        if usage:
            return usage
        # ChatResponse / CompletionResponse may have additional_kwargs, either
        # nested under "usage" or flat (llama-index-llms-openai sets
        # prompt_tokens/completion_tokens/total_tokens directly).
        additional = getattr(response, "additional_kwargs", {})
        if isinstance(additional, dict):
            if "usage" in additional:
                return _usage_to_dict(additional["usage"])
            if "prompt_tokens" in additional or "completion_tokens" in additional:
                return {
                    k: additional[k]
                    for k in ("prompt_tokens", "completion_tokens", "total_tokens")
                    if k in additional
                }

    return None
