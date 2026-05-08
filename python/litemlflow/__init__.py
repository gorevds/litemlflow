"""LiteMLflow native Python SDK.

Public surface:
- Client: low-level HTTP client.
- Run: context manager wrapping a created run.
- Span: context manager for trace spans.
- LiteMLflowError: base exception.
"""

from litemlflow.client import Client, LiteMLflowError, Run, Span

__version__ = "0.1.0"
__all__ = ["Client", "LiteMLflowError", "Run", "Span", "__version__"]
