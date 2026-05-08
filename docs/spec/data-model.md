# Data model

## The unified graph

Classic ML and LLM observability share one graph. Every node is one of:

```
Experiment
  └── Run
        ├── Param  (immutable key/value, set once)
        ├── Tag    (mutable key/value)
        ├── Metric (timeseries: key, value, step, timestamp)
        ├── Artifact (file blob with relative path)
        ├── Trace  (root span with children — LLM-style)
        └── Eval   (a structured comparison referring to other Runs)

Prompt (versioned, content-addressed; referenced by Traces)
```

Why one graph instead of two: a "Run" in classic ML and a "Trace" in LLM-land are the same conceptual unit (a unit of work with inputs, outputs, and metrics). Forcing them apart creates two UIs, two backups, two query engines. Unifying lets us treat a fine-tune run, a RAG eval run, and a single agent invocation symmetrically.

## Tables

### `experiments`
| Column | Type | Constraints |
|---|---|---|
| id | INTEGER | PRIMARY KEY AUTOINCREMENT |
| name | TEXT | NOT NULL UNIQUE |
| artifact_location | TEXT | NOT NULL |
| lifecycle_stage | TEXT | NOT NULL DEFAULT 'active' (active/deleted) |
| creation_time | INTEGER | NOT NULL (unix ms) |
| last_update_time | INTEGER | NOT NULL (unix ms) |

### `runs`
| Column | Type | Constraints |
|---|---|---|
| id | TEXT | PRIMARY KEY (32-hex UUID like MLflow) |
| experiment_id | INTEGER | NOT NULL, FK experiments(id) |
| name | TEXT | nullable |
| status | TEXT | NOT NULL ('RUNNING','SCHEDULED','FINISHED','FAILED','KILLED') |
| start_time | INTEGER | NOT NULL (unix ms) |
| end_time | INTEGER | nullable (unix ms) |
| artifact_uri | TEXT | NOT NULL |
| lifecycle_stage | TEXT | NOT NULL DEFAULT 'active' |
| user_id | TEXT | nullable |
| source_type | TEXT | nullable (NOTEBOOK/JOB/LOCAL/UNKNOWN) |
| source_name | TEXT | nullable |
| run_kind | TEXT | NOT NULL DEFAULT 'classic' ('classic','trace','eval') |

`run_kind` is a LiteMLflow extension. MLflow clients always create `classic` runs. Native clients can create `trace` or `eval` runs.

### `metrics`
| Column | Type |
|---|---|
| run_id | TEXT NOT NULL |
| key | TEXT NOT NULL |
| value | REAL NOT NULL |
| timestamp | INTEGER NOT NULL (unix ms) |
| step | INTEGER NOT NULL DEFAULT 0 |
| PK | (run_id, key, timestamp, step) |

### `params`
| Column | Type |
|---|---|
| run_id | TEXT NOT NULL |
| key | TEXT NOT NULL |
| value | TEXT NOT NULL |
| PK | (run_id, key) |

Params are immutable. Re-setting a param raises an error (matching MLflow semantics).

### `tags`
| Column | Type |
|---|---|
| run_id | TEXT NOT NULL |
| key | TEXT NOT NULL |
| value | TEXT NOT NULL |
| PK | (run_id, key) |

Tags are mutable and overwrite on conflict.

### `experiment_tags`
| Column | Type |
|---|---|
| experiment_id | INTEGER NOT NULL |
| key | TEXT NOT NULL |
| value | TEXT NOT NULL |
| PK | (experiment_id, key) |

### `traces` (LLM-style spans, also reachable from a Run)
| Column | Type |
|---|---|
| id | TEXT PRIMARY KEY (16 or 32 hex chars, OTel-compatible) |
| trace_id | TEXT NOT NULL (the root trace this span belongs to) |
| parent_id | TEXT (nullable; root spans have NULL) |
| run_id | TEXT (nullable; spans may exist without a Run if ingested via OTLP) |
| name | TEXT NOT NULL |
| span_kind | TEXT (CLIENT/SERVER/PRODUCER/CONSUMER/INTERNAL) |
| start_time | INTEGER NOT NULL (unix nanos for OTel parity) |
| end_time | INTEGER (nullable; nullable until span ends) |
| attributes_json | TEXT (JSON object) |
| events_json | TEXT (JSON array of {name, ts, attrs}) |
| status_code | TEXT (UNSET/OK/ERROR) |
| status_message | TEXT |

A "Run of run_kind='trace'" exposes its child spans as the Run's content; the UI renders this as a waterfall instead of a metrics chart.

### `prompts`
| Column | Type |
|---|---|
| name | TEXT NOT NULL |
| version | INTEGER NOT NULL |
| content | TEXT NOT NULL |
| content_hash | TEXT NOT NULL (sha256, hex) |
| created_at | INTEGER NOT NULL (unix ms) |
| created_by | TEXT |
| description | TEXT |
| PK | (name, version) |
| UNIQUE | (content_hash) — same content reused across names is fine |

### `prompt_aliases`
| Column | Type |
|---|---|
| name | TEXT NOT NULL |
| alias | TEXT NOT NULL ('latest', 'production', etc.) |
| version | INTEGER NOT NULL |
| PK | (name, alias) |

### `artifacts`
Tracked in the filesystem under `$DATA/artifacts/<run_id>/<path>`. We do not duplicate file metadata in SQLite by default. `mlflow-artifacts` API serves files directly from disk.

### `evals`
| Column | Type |
|---|---|
| run_id | TEXT PRIMARY KEY (a Run with run_kind='eval') |
| target_run_ids | TEXT (JSON array of run IDs being compared) |
| dataset_ref | TEXT (free-form, e.g., HF dataset path) |
| score | REAL (optional headline scalar) |
| metrics_json | TEXT (JSON object of secondary scores) |

### `schema_migrations`
| Column | Type |
|---|---|
| version | INTEGER PRIMARY KEY |
| applied_at | INTEGER NOT NULL (unix ms) |
| name | TEXT NOT NULL |

## Indexes

```sql
CREATE INDEX idx_runs_experiment_status ON runs(experiment_id, status, start_time DESC);
CREATE INDEX idx_runs_lifecycle ON runs(lifecycle_stage);
CREATE INDEX idx_metrics_run_key ON metrics(run_id, key, step);
CREATE INDEX idx_traces_run ON traces(run_id);
CREATE INDEX idx_traces_parent ON traces(parent_id);
CREATE INDEX idx_traces_trace ON traces(trace_id);
CREATE INDEX idx_prompt_aliases_name ON prompt_aliases(name);
```

## Invariants

1. A Run cannot exist without an Experiment.
2. Params are immutable: once set, attempting to set a different value raises HTTP 400 with `RESOURCE_ALREADY_EXISTS`.
3. Metrics are append-only at the API level (we don't expose delete-metric).
4. `lifecycle_stage = 'deleted'` hides a Run from default search but does not erase data; `litemlflow gc` performs hard delete.
5. A Trace span's `parent_id` must point to another span with the same `trace_id` or be NULL.
6. Prompt content is content-addressed: identical content yields identical `content_hash`.
7. Schema migrations are forward-only by default; `down` blocks exist only for tested rollback in disaster scenarios.
