# Examples

End-to-end walkthroughs of the tracker.

| File | What it covers |
|------|----------------|
| [`quickstart.ipynb`](quickstart.ipynb) | One Jupyter notebook touring every feature: experiments, projects, runs, notes, traces, prompts, lineage, model registry, compare/search, webhooks, dashboards. |

## Running the quickstart

```bash
pip install litemlflow jupyter
litemlflow serve --addr :5050 --db ~/lmf.db &
jupyter notebook examples/quickstart.ipynb
```

Or point it at the public demo by editing the `TRACKING_URL` variable at the top:

```python
TRACKING_URL = "https://lmf.gorev.space"
```

The notebook is idempotent — every section uses unique names so you can re-run the whole thing without conflicts.
