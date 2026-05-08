# Contributing to LiteMLflow

Thanks for your interest. LiteMLflow is built around a small set of unbreakable principles — the engineering bar is high precisely so the user-facing experience can stay simple.

## Core principles

1. **One binary, zero dependencies.** Nothing that breaks `curl | sh && litemlflow up`.
2. **Files are the source of truth.** SQLite + filesystem. No new datastores in core.
3. **MLflow client compat is sacred.** Existing MLflow Python code works after switching the tracking URI. Compat regressions are P0.
4. **First paint < 300ms.** UI bundle stays embedded; lists virtualize; metrics downsample server-side.
5. **No infrastructure for auth.** Built-in: localhost / basic / OIDC / mTLS. No external dependencies.
6. **Reversible migrations.** Every schema change has a tested `down`.
7. **Plugins are out-of-process.** S3, OIDC, and other extensions communicate via gRPC; they cannot crash core.
8. **Boring tech.** Go, SQLite, DuckDB, vanilla JS. No NIH, no hype-driven choices.

A PR that violates one of these is unlikely to land regardless of how clean it is.

## DCO, not CLA

Every commit must be signed off:

```
git commit -s -m "your message"
```

The `Signed-off-by` line indicates you have the right to submit the contribution under Apache 2.0. We deliberately do not use a CLA — your copyright remains yours.

## Development setup

```bash
git clone https://github.com/gorevds/litemlflow.git
cd litemlflow
make dev          # runs server with auto-reload
make test         # runs all tests
make lint         # static analysis
make compat-test  # runs MLflow Python client against LiteMLflow
```

## Code review process

- Two approvals required before merge.
- Every PR runs the full performance regression suite. Anything > 5% regression on hot paths blocks merge.
- Mutation testing is enforced on `internal/store` and `internal/api/mlflow` at >= 70%.

## Reporting bugs

Use the bug-report template. Include:
- LiteMLflow version (`litemlflow version`)
- OS/arch
- Minimal reproducer (a Python script or curl command is gold)
- The full error or unexpected behavior

## Reporting security issues

Do **not** open a public issue for vulnerabilities. See [SECURITY.md](SECURITY.md).

## Code of Conduct

See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md). Be excellent to each other.
