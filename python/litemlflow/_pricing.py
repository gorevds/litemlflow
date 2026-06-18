"""Token pricing table for known LLM models.

USD per 1M tokens (input, output) — last updated 2026-05.
Unknown models return (0.0, 0.0); cost.usd metric will be 0.

This is the canonical pricing module; litemlflow.langchain.pricing re-exports
``cost`` from here for backward compatibility.
"""

from __future__ import annotations

PRICING: dict[str, tuple[float, float]] = {
    "gpt-4o": (2.50, 10.00),
    "gpt-4o-mini": (0.15, 0.60),
    "gpt-4-turbo": (10.00, 30.00),
    "gpt-3.5-turbo": (0.50, 1.50),
    "claude-3-5-sonnet-20241022": (3.00, 15.00),
    "claude-3-5-haiku-20241022": (1.00, 5.00),
    "claude-3-opus-20240229": (15.00, 75.00),
    "gemini-1.5-pro": (1.25, 5.00),
    "gemini-1.5-flash": (0.075, 0.30),
}


def _rates(model: str) -> tuple[float, float]:
    """Resolve (input, output) USD/1M rates for a model name.

    Real SDKs emit dated/versioned IDs ("gpt-4o-2024-08-06") and
    provider-prefixed IDs ("openai/gpt-4o"), which an exact-match table misses.
    Resolution order: exact match -> strip a leading "provider/" and match
    exact -> longest known base name that is a prefix of the (de-prefixed)
    model. Genuinely unknown models fall through to (0.0, 0.0).
    """
    if model in PRICING:
        return PRICING[model]
    name = model.rsplit("/", 1)[-1]  # strip "provider/" if present
    if name in PRICING:
        return PRICING[name]
    best = ""
    for key in PRICING:
        if name.startswith(key) and len(key) > len(best):
            best = key
    return PRICING[best] if best else (0.0, 0.0)


def cost(model: str, prompt_tokens: int, completion_tokens: int) -> float:
    """Compute USD cost for a given model and token counts.

    Args:
        model: Model name (e.g., "gpt-4o-mini"). Dated/versioned and
            provider-prefixed variants resolve to their base price; see _rates.
        prompt_tokens: Number of input/prompt tokens consumed.
        completion_tokens: Number of output/completion tokens generated.

    Returns:
        Cost in USD. Returns 0.0 if the model is not in the pricing table.
    """
    pin, pout = _rates(model)
    return (prompt_tokens / 1_000_000) * pin + (completion_tokens / 1_000_000) * pout
