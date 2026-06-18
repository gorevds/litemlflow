"""Unit tests for token pricing lookup (independent-review P2).

Exact-match-only pricing returned $0 for the dated/versioned and
provider-prefixed model IDs that real SDKs emit (e.g. "gpt-4o-2024-08-06",
"openai/gpt-4o"), silently under-reporting cost.usd. These assert the safe
fallback: strip a leading provider, then match the longest known base name
that is a prefix; genuinely unknown models still cost 0.
"""

from __future__ import annotations

from litemlflow._pricing import cost


def test_exact_match_unchanged() -> None:
    expected = (100 / 1_000_000) * 0.15 + (50 / 1_000_000) * 0.60
    assert abs(cost("gpt-4o-mini", 100, 50) - expected) < 1e-12


def test_dated_suffix_matches_base() -> None:
    # 1M input tokens of gpt-4o-2024-08-06 should price as gpt-4o ($2.50/1M).
    assert cost("gpt-4o-2024-08-06", 1_000_000, 0) == 2.50


def test_longest_prefix_wins() -> None:
    # Must resolve to gpt-4o-mini ($0.15), not the shorter gpt-4o ($2.50).
    assert cost("gpt-4o-mini-2024-07-18", 1_000_000, 0) == 0.15


def test_provider_prefix_stripped() -> None:
    assert cost("anthropic/claude-3-5-sonnet-20241022", 1_000_000, 0) == 3.00
    assert cost("openai/gpt-4o-2024-08-06", 1_000_000, 0) == 2.50


def test_unknown_model_is_zero() -> None:
    assert cost("totally-made-up-model", 1_000_000, 1_000_000) == 0.0
