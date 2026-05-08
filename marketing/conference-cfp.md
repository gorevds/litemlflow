# Conference CFP drafts

Talk title: **"Single-binary MLflow: lessons from rebuilding an OSS standard"**

Same technical content, three audience framings. Adjust abstract to match each conference's stated CFP priorities before submitting.

---

## 1. PyCon US 2026

**Track:** Infrastructure, DevOps, and Testing  
**Format:** 30-minute talk  
**Submission deadline:** check pycon.org/speak

### Title

Single-binary MLflow: lessons from rebuilding an OSS standard in Go

### Abstract (300 words)

MLflow has become the de-facto experiment tracking API for the Python ML ecosystem. Its Python client is used by hundreds of thousands of engineers. Its server, however, is not a Python story — it is an operational story of Postgres, SQLAlchemy, S3, and Alembic migrations.

LiteMLflow is an MLflow-API-compatible experiment tracker written in Go. It ships as a single statically-linked binary with zero runtime dependencies. `litemlflow up` starts a server in 53 milliseconds. No Postgres. No S3. No reverse proxy. Your existing MLflow Python code works after changing one environment variable.

This talk covers three themes:

**1. The API archaeology.** What does "MLflow-compatible" mean in practice? The MLflow REST spec is informal — the real source of truth is the Python client's source code and its integration tests. I will walk through how we reverse-engineered 31 canonical client operations, where the spec is ambiguous, and how we built a compatibility test suite that runs the real MLflow Python client against our server.

**2. The performance story.** LiteMLflow is 143× faster on cold start and 15× faster on metric ingestion. The talk explains why: Go vs CPython, in-process SQLite vs SQLAlchemy + Postgres network round-trips, a 60 KB vanilla JS UI vs a 2 MB React bundle. And honest trade-offs: where Python + columnar storage wins.

**3. The distribution model.** A single binary changes how users experience your software. We ship via Homebrew, Debian, RPM, Snap, Docker, and Helm. I will cover the build pipeline, the cross-compilation approach, and the gotchas with pure-Go CGo-free SQLite.

Audience takeaway: a pattern for building compatible-but-better replacements for Python server-side tools, and a Go/SQLite architecture that delivers "infrastructure simplicity as a feature."

### Bio

Engineer building LiteMLflow (github.com/litemlflow/litemlflow). Previously worked on ML platform tooling at [company TBD]. Apache 2.0 all the way down.

---

## 2. KubeCon EU 2027

**Track:** App Development & DevX / AI + ML  
**Format:** 25-minute session  
**Submission deadline:** check kccnceu.io/cfp

### Title

From Kubernetes-hostile to Kubernetes-native: packaging ML experiment tracking as a single-binary operator

### Abstract (300 words)

Running MLflow on Kubernetes is a case study in complexity: StatefulSet for the server, Postgres for metadata, S3 for artifacts, secrets for credentials, Ingress with TLS, and RBAC for access control. Teams typically wrap this in a Helm umbrella chart and spend a day debugging pod-to-pod DNS before the first experiment runs.

LiteMLflow is a re-implementation of the MLflow API as a single Go binary. It embeds SQLite (pure-Go, no CGo), serves artifacts from a local PersistentVolumeClaim or an S3-compatible backend, and starts in 53 milliseconds. It ships with a Kubernetes operator (`litemlflow.dev/v1alpha1 LiteMLflow` CRD) built on controller-runtime that reconciles a StatefulSet, a headless Service, and optional basic-auth Secrets from a single custom resource.

This talk covers:

**1. The architecture.** Why SQLite (single-writer WAL mode) is the right storage primitive for a research tracking tool that does not need horizontal write scale. How to package a binary + embedded database as a Kubernetes StatefulSet. Why `replicas: 1` is not a limitation — it is a feature.

**2. The operator.** Building a production-grade controller-runtime operator: CRD design, the reconciliation loop, status conditions, and testing with envtest. Why we kept the operator as a separate Go module to avoid pulling controller-runtime (and its Prometheus client) into the server binary.

**3. The distribution.** Helm chart design: StatefulSet + PVC + ServiceMonitor + Ingress. How to ship a Helm chart via OCI registry. Multi-arch Docker images via BuildKit.

Audience takeaway: a worked example of "operator as the right abstraction for single-binary stateful workloads" and a pattern for keeping controller-runtime out of your server binary.

### Bio

Engineer building LiteMLflow. Open-source work in Go, Kubernetes, and ML tooling.

---

## 3. MLOps World 2026

**Track:** Open Source Tools / ML Platform Engineering  
**Format:** 20-minute talk + 10-minute Q&A  
**Submission deadline:** check mlopsworld.com/cfp

### Title

The operational tax of MLflow: how we built a zero-dependency replacement and what it took to stay compatible

### Abstract (250 words)

Every MLOps engineer has a war story about MLflow in production. Postgres migration gone wrong. S3 credentials misconfigured. The 7-second startup time blocking CI pipelines. The React bundle that makes first paint feel like a cold reboot.

LiteMLflow is our answer: a single Go binary that speaks the MLflow REST API. `litemlflow up --data ./data` starts in 53 milliseconds. Your existing MLflow Python code keeps working. No external database. No object store required. Backup is `cp -r data backup/`.

This talk is a practitioner's account of three engineering challenges:

**1. Staying compatible without a spec.** The MLflow REST API is informally documented. The real contract is the Python client's behavior. We built a test suite of 31 canonical operations that runs the real MLflow Python client (3.x) against our server on every PR. I will show how we discovered edge cases — filter grammar with `IN (...)` and `BETWEEN`, `log_inputs` dataset tracking, MLflow 3.x's `tag.mlflow.prompt.is_prompt != 'true'` filter clause — and how we resolved them.

**2. Performance without cheating.** The 143× cold-start improvement is real and structural. The 3.1× log_batch improvement is real. But there is a query shape where MLflow wins: raw sequential metric-history scans. I will be honest about where and why.

**3. MLOps-grade distribution.** Homebrew, Debian, RPM, Snap, Helm, K8s operator. How to ship all seven distribution channels from one CI pipeline.

Audience takeaway: when and why to build a single-binary infrastructure tool, and how to test API compatibility without a formal spec.

### Bio

Engineer building LiteMLflow (github.com/litemlflow/litemlflow). Apache 2.0. Talk to me at the hallway track.
