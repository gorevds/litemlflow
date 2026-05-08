# Demo script — 90-second video

**Format:** screen recording, 1280×720, 30 fps. No voiceover (subtitles only). Background: dark terminal + browser split-screen.  
**Goal:** show the full zero-to-tracking loop without narration gaps.

---

## Setup before recording

- Clean shell: no previous litemlflow processes, no existing `./data/` dir.
- Browser: dark mode, Chrome or Firefox, tab at `http://localhost:5000`.
- Font: JetBrains Mono 14pt in terminal.
- Terminal: two panes — left for server, right for client commands.

---

## Frame-by-frame

### Frames 0–5 s — Title card

Text overlay on dark background:  
```
LiteMLflow
143× faster than MLflow. Zero databases.
```

---

### Frames 5–20 s — Install

**Left pane (terminal):**

Subtitle: *"Install via Homebrew — or use the Docker one-liner"*

```bash
$ brew install litemlflow/tap/litemlflow
```

Show brief Homebrew output (pre-installed is fine — cut to prompt).

```bash
$ litemlflow version
litemlflow v1.0.0 (go1.22, linux/amd64)
```

---

### Frames 20–30 s — Start the server

**Left pane:**

Subtitle: *"`litemlflow up` — server ready in 53 ms"*

```bash
$ litemlflow up --data ./data
```

Show the startup log line:
```
2026-05-08T10:00:00Z  INFO  server ready  addr=:5000  startup_ms=53
```

**Highlight:** the `startup_ms=53` value. Pause 1 second.

---

### Frames 30–50 s — Log from Python (existing MLflow code)

**Right pane:**

Subtitle: *"Your existing MLflow code — no changes"*

```python
import mlflow

mlflow.set_tracking_uri("http://localhost:5000")
mlflow.set_experiment("demo-experiment")

with mlflow.start_run(run_name="first-run"):
    mlflow.log_param("model", "logistic-regression")
    mlflow.log_param("lr", 0.01)
    for step in range(20):
        loss = 1.0 / (step + 1)
        mlflow.log_metric("loss", loss, step=step)
    mlflow.log_metric("accuracy", 0.94)

print("done")
```

Run it:
```bash
$ python demo.py
done
```

---

### Frames 50–65 s — UI loads in <300ms

**Switch to browser. Refresh the runs list.**

Subtitle: *"UI first paint: 0.6 ms"*

Show: experiments list → click "demo-experiment" → runs list → click "first-run".

Run detail page appears. Highlight:
- The metric chart (loss curve, 20 points)
- The accuracy value
- The params panel (model, lr)

---

### Frames 65–75 s — Trace waterfall

**Still on run detail page. Scroll to the Traces section (pre-seeded via the demo seed script for visual interest).**

Subtitle: *"Traces and metrics in the same run view"*

Show a pre-seeded trace with 3 spans: `rag.pipeline` → `rag.retrieve` → `rag.generate`. Expand a span to show attributes (tokens, latency).

---

### Frames 75–85 s — Prometheus /metrics

**Right pane (terminal):**

Subtitle: *"Prometheus /metrics — no credentials required"*

```bash
$ curl -s http://localhost:5000/metrics | grep litemlflow_runs
litemlflow_runs_created_total 1
litemlflow_metrics_logged_total 21
```

---

### Frames 85–90 s — Outro

**Left pane stays on terminal. Text overlay:**

```
github.com/litemlflow/litemlflow
brew install litemlflow/tap/litemlflow
Apache 2.0
```

Fade out.

---

## Post-production notes

- Cut any >2 s pauses.
- Add captions via `.srt` (same subtitle text as above).
- Export as `.gif` (1280×720, optimized with `gifsicle -O3`) for README embed.
- Export as `.mp4` (H.264, CRF 22) for YouTube / Twitter.
- Target file size: GIF ≤ 8 MB, MP4 ≤ 25 MB.
- Upload GIF to repo as `docs/demo.gif` and reference in README (see the TODO comment there).
