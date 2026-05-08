"""Token pricing table for known LLM models.

USD per 1M tokens (input, output) — last updated 2026-05.
Unknown models return (0.0, 0.0); cost.usd metric will be 0.

.. deprecated::
    Import from ``litemlflow._pricing`` directly. This module re-exports
    ``PRICING`` and ``cost`` for backward compatibility.
"""

from __future__ import annotations

# Re-export from the canonical location so existing imports continue to work.
from litemlflow._pricing import PRICING, cost

__all__ = ["PRICING", "cost"]
