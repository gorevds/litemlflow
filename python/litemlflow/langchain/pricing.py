"""Token pricing table for known LLM models.

USD per 1M tokens (input, output) — last updated 2026-05.
Unknown models return (0.0, 0.0); cost.usd metric will be 0.
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


def cost(model: str, prompt_tokens: int, completion_tokens: int) -> float:
    """Compute USD cost for a given model and token counts.

    Args:
        model: Model name (e.g., "gpt-4o-mini").
        prompt_tokens: Number of input/prompt tokens consumed.
        completion_tokens: Number of output/completion tokens generated.

    Returns:
        Cost in USD. Returns 0.0 if the model is not in the pricing table.
    """
    pin, pout = PRICING.get(model, (0.0, 0.0))
    return (prompt_tokens / 1_000_000) * pin + (completion_tokens / 1_000_000) * pout
