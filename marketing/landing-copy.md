# Landing page copy — litemlflow.dev

*Audience: ML engineer landing from HN / Google / Twitter. Time to decision: 15 seconds.*

---

## Hero section

### Headline

**Your experiments, in one file.**

### Subhead

MLflow-compatible experiment tracking. Single Go binary. 143× faster cold start.  
No Postgres. No S3. No reverse proxy. Just `litemlflow up`.

### Primary CTA

**Install in 30 seconds** → /quickstart/

### Secondary CTA

**View on GitHub** → github.com/gorevds/litemlflow

### Social proof (below CTAs)

31/31 MLflow compat checks pass · Apache 2.0 · Live demo at lmf.gorev.space

---

## Install strip (always visible, above the fold)

```bash
brew install litemlflow/tap/litemlflow && litemlflow up
# → server ready in 53 ms at http://localhost:5000
```

---

## Three feature blocks

### Block 1 — Drop-in replacement

**One env variable. No code changes.**

Change `MLFLOW_TRACKING_URI` to point at LiteMLflow. Your existing sklearn, PyTorch, Hugging Face, and LangChain logging code keeps working. 31 canonical MLflow client operations tested against the real Python client.

*"I migrated our team's tracking in 10 minutes. It just worked."*

### Block 2 — LLM-native observability

**Traces + prompts + evals, right next to your metrics.**

The embedded UI shows metric charts and LLM trace waterfalls in the same run view. Versioned, content-addressed prompts with aliases (`production`, `staging`). LangChain and LlamaIndex auto-instrumentation via `pip install 'litemlflow[langchain]'`.

*No second tool. No second dashboard. One file.*

### Block 3 — Operational simplicity

**Backup is `cp`. Restore is the reverse.**

SQLite + filesystem. No external database. No object store required. Runs in CI with zero setup in 53 ms. `litemlflow backup` produces a tarball. Kubernetes? Helm chart and a StatefulSet operator are included.

*"Our CI spins up a fresh tracking server for every job. Previously that added 8 seconds. Now it's 53 milliseconds."*

---

## Comparison table

| | **LiteMLflow** | MLflow | W&B | Aim |
|---|---|---|---|---|
| Cold start | **53 ms** | 7 500 ms | SaaS | ~2 s |
| Install steps | **1** | 4+ | 3 | 2 |
| Runtime deps | **none** | Python + Postgres | account | Python |
| MLflow compat | **✅ 80%+** | native | ❌ | ❌ |
| LLM traces | **✅** | bolt-on | ✅ | ❌ |
| License | **Apache 2.0** | Apache 2.0 | proprietary | Apache 2.0 |

---

## Final CTA section

### Headline

Ready to drop the database?

### Body

Start in 30 seconds. Migrate in 10 minutes. Back up in one command.

### Buttons

- **Get started** → /quickstart/  
- **Read the docs** → /architecture/  
- **Star on GitHub** → github.com/gorevds/litemlflow  

---

## Footer

Apache 2.0 · Not affiliated with Databricks, Inc.  
Built with Go, SQLite, and no operational drama.
