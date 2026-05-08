# ADR 0001: Go + pure-Go SQLite for the core

- Status: Accepted
- Date: 2026-05-07
- Decision-makers: Core team

## Context

LiteMLflow needs a runtime that can be distributed as a single binary and that runs anywhere a typical user's Python ML scripts run (Linux, macOS, Windows; amd64 and arm64). The core also needs a relational store that is fast, ACID, and trivial to back up.

## Decision

- The core server is written in Go (>= 1.22).
- SQLite is accessed through `modernc.org/sqlite`, the pure-Go translation of the SQLite C amalgamation.
- We do **not** use `mattn/go-sqlite3` because it requires CGO, complicating cross-compilation and static binary distribution.

## Consequences

### Positive
- Cross-compilation is trivial: `GOOS=linux GOARCH=arm64 go build` produces a working binary with no toolchain juggling.
- Static binaries: no glibc dependency, no JVM, no Python interpreter at runtime.
- Memory safety beyond what C SQLite gives us, at a small performance cost.

### Negative
- `modernc.org/sqlite` is somewhat slower than CGO SQLite (10–30% on write-heavy benchmarks). We evaluated this and judged the operational simplicity more valuable than the speed loss for our hero user.
- A few advanced SQLite features (e.g., loadable extensions, custom virtual tables) are harder. We don't need them in v1.

### Mitigations
- Performance regression CI catches >5% slowdowns.
- We benchmark against MLflow + Postgres as the baseline: as long as we are faster than that on solo workloads, we're good.

## Alternatives considered

- **Rust**: better performance ceiling, but slower iteration speed and a smaller pool of contributors familiar with the relevant crates. Deferred.
- **CGO SQLite**: faster but breaks single-binary cross-compilation.
- **Postgres**: ruled out — adding a database dependency violates principle 1.
- **DuckDB only**: DuckDB is excellent for OLAP but is single-writer and locks the file globally. We want it for analytics, not OLTP.
- **Embedded RocksDB or BoltDB**: KV stores require us to handcraft the relational layer. SQLite gives us SQL, indexes, transactions, and tooling for free.
