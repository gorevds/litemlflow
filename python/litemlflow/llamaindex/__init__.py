"""LiteMLflow LlamaIndex integration.

Provides ``LiteMLflowEventHandler``, a LlamaIndex event handler that
auto-instruments LlamaIndex query pipelines into LiteMLflow as spans,
metrics, and token costs.

Requires ``llama-index-core``::

    pip install 'litemlflow[llamaindex]'

Importing this module does NOT require llama-index-core to be installed.
The ``ImportError`` is raised only when you instantiate the handler.
"""

from litemlflow.llamaindex.handler import LiteMLflowEventHandler

__all__ = ["LiteMLflowEventHandler"]
