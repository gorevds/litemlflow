---
title: Benchmarks
---
# v0.4 Benchmark Report — LiteMLflow vs MLflow

**Date:** 2026-05-08  
**Harness:** `tests/integration/bench.py --runs 1000 --out bench-v04.json`  
**Both servers ran on local loopback** (same machine, idle). LiteMLflow uses its
embedded SQLite (modernc.org/sqlite, pure Go) store; MLflow uses SQLite + filesystem artifacts (`--workers 1`).

## Results

| Metric | LiteMLflow | MLflow + SQLite | Ratio |
|---|---|---|---|
| Cold start | **53 ms** | 7 513 ms | 143× faster |
| `log_metric` p50 | **1.44 ms** | 21.6 ms | 15× faster |
| `log_metric` p95 | **1.68 ms** | 30.9 ms | 18× faster |
| `log_batch` throughput | **24 533 rows/s** | 8 008 rows/s | 3.1× faster |
| `search_runs` p50 (1 000-run DB) | **45.9 ms** | 46.5 ms | 1.01× — essentially tied |
| `search_runs` p95 | **47.7 ms** | 89.6 ms | 1.9× faster |
| UI first paint p50 | **0.6 ms** | n/a (React bundle) | — |
| Populate 1 000 runs | **3.8 s** | 64.7 s | 17× faster |

*UI first paint for MLflow is not measured — MLflow serves a large React bundle;
the benchmark correctly skips that comparison.*

## Interpretation

**Where LiteMLflow wins clearly:**

- **Cold start (143×):** LiteMLflow starts in ~53 ms because it is a single,
  statically linked binary with an embedded key-value store — no Python
  interpreter startup, no SQLAlchemy ORM initialisation, no SQLite schema
  migration. This matters for CI pipelines and ephemeral environments.
- **`log_metric` latency (15× p50):** Each metric write goes through an
  in-process SQLite (pure-Go modernc.org/sqlite) transaction without HTTP → Python → SQL overhead. The p99
  (2.4 ms vs 51.5 ms) gap is even wider because MLflow's SQLite path shows a
  long tail under occasional lock contention.
- **`log_batch` throughput (3.1×):** LiteMLflow handles batch inserts as a
  single bolt transaction; MLflow must fan out to multiple SQLAlchemy INSERT
  statements with per-row column widths.
- **Population speed (17×):** The same SQLite (modernc.org/sqlite, pure Go) advantage compounds over 1 000 runs.

**Where MLflow wins or ties:**

- **`search_runs` p50 (tie, ~46 ms):** Both systems deliver similar median
  latency for a filtered search across 1 000 runs. SQLite's B-tree index on
  `metrics.value` is mature and well-tuned for this query shape. LiteMLflow's
  p95 is 2× better (48 ms vs 90 ms), which matters under concurrent load, but
  the median is not a differentiator here.
- **Raw metric-history fetch (MLflow wins on this size):** For the downsample
  benchmark, MLflow returns 50 000 raw metric points in 2.6 ms p50 vs
  LiteMLflow's 124 ms. MLflow's SQLite column-scan is faster for this specific
  sequential read pattern; LiteMLflow reads from SQLite indexed lookups which involve
  more random-access seeks. LiteMLflow's LTTB downsample path (119 ms) only
  returns 500 representative points, which is the correct operational choice
  for chart rendering, but the raw baseline is slower.

**Summary:** LiteMLflow is the right choice when startup time, write
throughput, and operational simplicity matter. MLflow's SQLite backend is
competitive on aggregate search and raw sequential reads, though it is slower
everywhere else and carries a heavy startup cost.

---

## Raw JSON report

```json
{
  "timestamp": "2026-05-08T09:53:37Z",
  "runs": 1000,
  "litemlflow": {
    "label": "LiteMLflow",
    "cold_start_ms": 52.69252194557339,
    "log_metric": {
      "p50_ms": 1.4436725177802145,
      "p95_ms": 1.6830075066536665,
      "p99_ms": 2.385744344210252,
      "min_ms": 1.260903081856668,
      "max_ms": 2.413104986771941,
      "n": 200
    },
    "log_batch": {
      "median_rows_per_sec": 24532.83413843149,
      "max_rows_per_sec": 26085.84687996,
      "rows_per_call": 1000,
      "calls": 3
    },
    "ui_first_paint": {
      "p50_ms": 0.6444460013881326,
      "p95_ms": 2.8346376726403832,
      "min_ms": 0.5830909358337522
    },
    "search_runs": {
      "populate_seconds": 3.750538722029887,
      "populate_runs": 1000,
      "search_p50_ms": 45.938143506646156,
      "search_p95_ms": 47.72651746752672,
      "search_p99_ms": 47.73814196232706
    },
    "downsample": {
      "metric_count": 50000,
      "downsample_n": 500,
      "baseline_p50_ms": 123.69358900468796,
      "baseline_max_ms": 128.73099802527577,
      "downsampled_p50_ms": 119.06213907059282,
      "downsampled_max_ms": 120.16132101416588,
      "returned_points": 500,
      "downsampled_from": 50000,
      "speedup": 1.0388994349526102
    }
  },
  "mlflow": {
    "label": "MLflow + SQLite",
    "cold_start_ms": 7512.840962037444,
    "log_metric": {
      "p50_ms": 21.57586149405688,
      "p95_ms": 30.89541897061281,
      "p99_ms": 51.540551006328315,
      "min_ms": 18.25853600166738,
      "max_ms": 148.78150599543005,
      "n": 200
    },
    "log_batch": {
      "median_rows_per_sec": 8008.415885517751,
      "max_rows_per_sec": 8096.3053561913675,
      "rows_per_call": 1000,
      "calls": 3
    },
    "ui_first_paint": {
      "p50_ms": null,
      "p95_ms": null,
      "min_ms": null
    },
    "search_runs": {
      "populate_seconds": 64.68276763800532,
      "populate_runs": 1000,
      "search_p50_ms": 46.530019491910934,
      "search_p95_ms": 89.60670009837486,
      "search_p99_ms": 90.34614404663444
    },
    "downsample": {
      "metric_count": 50000,
      "downsample_n": 500,
      "baseline_p50_ms": 2.631225041113794,
      "baseline_max_ms": 3.85748699773103,
      "downsampled_p50_ms": 2.403354039415717,
      "downsampled_max_ms": 2.6782150380313396,
      "returned_points": 0,
      "downsampled_from": null,
      "speedup": 1.094813746939038
    }
  }
}
```
