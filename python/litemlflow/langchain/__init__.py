"""LiteMLflow LangChain integration.

Provides ``LiteMLflowCallbackHandler``, a LangChain callback handler that
auto-instruments LangChain runs into LiteMLflow as spans, metrics, and
token costs.

Requires ``langchain-core``::

    pip install 'litemlflow[langchain]'

Importing this module does NOT require langchain-core to be installed.
The ``ImportError`` is raised only when you instantiate the handler.
"""

from litemlflow.langchain.callback import LiteMLflowCallbackHandler

__all__ = ["LiteMLflowCallbackHandler"]
