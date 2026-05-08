# LiteMLflow security audit baseline (v1.0)

This document is the v1.0 self-pen-test deliverable. It captures the result of running three Go-ecosystem security scanners against the codebase, the fixes applied, and the residual findings that have been reviewed and accepted as false positives.

A scheduled CI workflow re-runs all three on every PR plus weekly cron, and gates the build on **no new HIGH-severity gosec findings outside the accept-list below**. govulncheck and semgrep are advisory.

This document is **not a substitute for a paid third-party penetration test** when you deploy LiteMLflow into a hostile multi-tenant environment, but it is what we ship for v1.0 and what the project will keep clean.

## Tools

| Tool | Version | What it catches |
|---|---|---|
| `govulncheck` | latest from `golang.org/x/vuln` | Known CVEs in stdlib + transitive deps that are *actually called* by your code paths |
| `gosec` | latest from `securego/gosec` | Static patterns: SQL concat, weak hashes, file-path traversal, file modes, integer conversions, cookie hygiene, gzip bombs, …  |
| `semgrep` | `1.162.0` with `p/security-audit` + `p/golang` rulesets | Pattern matches across the wider community ruleset |
| `gremlins` | latest from `go-gremlins/gremlins` | Mutation testing — measures how good our tests are at catching bugs |

Reproduce locally:

```bash
go install github.com/securego/gosec/v2/cmd/gosec@latest
go install golang.org/x/vuln/cmd/govulncheck@latest
go install github.com/go-gremlins/gremlins/cmd/gremlins@latest
pipx install semgrep

govulncheck ./...
gosec -fmt json -quiet ./... > /tmp/gosec.json
python3 .github/scripts/check_gosec.py /tmp/gosec.json docs/security-audit.md
semgrep --config=p/security-audit --config=p/golang .
gremlins unleash --threshold-efficacy 80 ./internal/auth/
gremlins unleash --threshold-efficacy 70 ./internal/store/
```

---

## Findings and fixes (v1.0 audit)

### govulncheck — 4 stdlib + dep CVEs, **all fixed**

The audit identified four issues that called into vulnerable code paths:

| ID | Description | Fix |
|---|---|---|
| GO-2026-4982 | XSS in `html/template` (Go 1.26.2) | Bumped Go toolchain to 1.26.3 (set `toolchain` directive in `go.mod`; CI also pinned to 1.26.3) |
| GO-2026-4980 | XSS in `html/template` (Go 1.26.2) | Same as above |
| GO-2026-4971 | `net.Dialer` panic on Windows NUL byte (Go 1.26.2) | Same as above; Linux deploys not affected, fixed defensively |
| GO-2026-4918 | Infinite loop in HTTP/2 transport (`golang.org/x/net@v0.51.0`) | Bumped `golang.org/x/net` to v0.53.0 (`go get -u`) in both modules (root + operator) |

Re-run: **0 vulnerabilities** with Go 1.26.3 + x/net v0.53.0.

### gosec — fixed

| Rule | Where | Fix |
|---|---|---|
| **G110** (decompression bomb) | `cmd/litemlflow/main.go` (restore tarball path) | Wrapped the gzip reader in `io.LimitReader` capped at 200 GiB by default; override via `LITEMLFLOW_RESTORE_MAX_GIB`. |
| **G118** (goroutine uses `context.Background()`) | `internal/server/middleware.go` (session-touch goroutine) | Switched to a 5-second timeout-bounded context. The detached lifetime is intentional (touch must outlive the request); the timeout prevents goroutine leaks against a wedged DB. Annotated with `//nolint:contextcheck` and inline justification. |
| **G301** (dir perms 0o755) | 8 sites across `cmd`, `internal/server`, `internal/artifact` | Tightened to `0o750` (no world-readable). |
| **G302** (file perms 0o644) | `internal/artifact/store.go` | Tightened artifact writes to `0o640`. |
| **G306** (file perms 0o644 on writes) | `internal/migrator/mlflow.go` (checkpoint file) | Tightened to `0o600` — checkpoint is internal state. |
| Open redirect (semgrep `open-redirect`) | `internal/api/native/handlers.go` `OIDCCallback` `return_to` validation | New `safeReturnTo` helper rejects everything that isn't a single-leading-slash absolute path: blocks `//evil.com`, `\x00`/`\r`/`\n` injection, length > 2 KiB. New unit test `TestSafeReturnTo` covers each rejection case. |

### gosec — accepted false positives

Locked-in via `.github/scripts/check_gosec.py`. CI fails on any new HIGH outside this set; if you add a new accepted finding, append the line here with a one-line justification.

#### Accepted gosec false-positives

```
# rule  file:line                                            why

# G115 — integer conversions where range is bounded by construction
G115 cmd/litemlflow/main.go:373                              tar restore mode mask: hdr.Mode &= 0o640 then cast — values are bounded
G115 internal/grpcotlp/ingest.go:79                          OTLP timestamps from gRPC peer; bounded by spec to fit int64
G115 internal/grpcotlp/ingest.go:90                          ditto
G115 internal/model/types.go:223                             hex-byte conversion; values <=255 by construction
G115 internal/model/types.go:229                             ditto
G115 internal/model/types.go:244                             ditto
G115 internal/model/types.go:269                             ditto
G115 internal/server/middleware.go:277                       request_id hex-byte conversion; values <=255 by construction
G115 internal/server/middleware.go:278                       ditto

# G118 — detached goroutine context (intentional, see code comment)
G118 internal/server/middleware.go:179                       session-touch must outlive request; 5s timeout caps lifetime

# G122 — backup walk uses absolute path with a known root; not user-controllable
G122 cmd/litemlflow/main.go:284                              backup tar walker; root is operator-supplied

# G124 — *Insecure cookie helpers are explicitly named for dev/test only
G124 internal/auth/session.go:99                             SetSessionCookieInsecure: explicit "Insecure" name; dev/test only
G124 internal/auth/session.go:184                            SetOIDCStateCookieInsecure: ditto

# G202 — dynamic SQL where the variable parts are internal allowlists; args bound via ?
G202 internal/store/sqlite.go:315                            WHERE clause from internal where[] slice
G202 internal/store/sqlite.go:467                            UPDATE SET from internal sets[] slice
G202 internal/store/sqlite.go:535                            WHERE clause from internal where[] slice
G202 internal/store/sqlite.go:1326-1329                      internal helper; where clause is constant from caller
G202 internal/store/registry.go:142                          dynamic WHERE on registered-models; allowlist-validated
G202 internal/store/registry.go:219                          ditto
G202 internal/store/registry.go:576                          ditto
G202 internal/store/workspaces.go:98                         dynamic UPDATE SET; columns from internal allowlist

# G304 — path-from-variable where Clean+abs+prefix-check is performed
G304 internal/artifact/store.go:76                           filepath.Clean'd and verified inside ArtifactsDir prefix
G304 internal/artifact/store.go:125                          ditto
G304 cmd/litemlflow/main.go:255                              backup tar reader; path is clean+abs source
G304 cmd/litemlflow/main.go:283                              backup walk; path comes from filepath.Walk
G304 cmd/litemlflow/main.go:361                              tar restore; hdr.Name path-traversal-checked above

# Other accepted
G302 internal/artifact/store.go:76                           0o640 (group readable); systemd-service deploys rely on this
G710 internal/server/middleware.go                           informational; not a vuln
G104 various                                                 deferred Close() / Rollback() return values
```

**How the accept-list works:** the CI check parses the fenced block above for `<rule> <file>:<line>` pairs. Each entry is matched verbatim against the gosec output. A new finding on a different line, or a new rule on an existing line, fails the build. To accept a new finding, **add a line and the justification**.

### semgrep — fixed

| Rule | Where | Fix |
|---|---|---|
| `open-redirect` | `internal/server/middleware.go:215` flagged the `Redirect` to `/api/v1/auth/oidc/start?return_to=...` | The redirect itself is to a fixed internal path; the issue is whether the *callback* sanitises `return_to`. Fixed in `internal/api/native/handlers.go` via `safeReturnTo` (see G118 row above). |
| `cookie-missing-secure` | Same as gosec G124 (dup) | Accepted: dev-only `*Insecure` variants, named accordingly. |
| `decompression-bomb` | Same as gosec G110 (dup) | Fixed via `io.LimitReader` cap. |

---

## Mutation testing baseline

Run nightly via `.github/workflows/mutation.yml`. Thresholds match the values measured at v1.0 tag, slightly relaxed to absorb test-flake. Tighten as more tests are added.

| Package | Killed | Lived | Not covered | Timed out | Efficacy | CI threshold |
|---|---|---|---|---|---|---|
| `internal/auth` | 59 | 7 | 8 | 4 | **89.39%** | 80% |
| `internal/store` | 365 | 124 | 120 | 3 | **74.64%** | 70% |
| `internal/webhooks` | 16 | 14 | 26 | 11 | **53.33%** | 50% |

These numbers say: in `internal/auth` we kill 89% of the synthetic bugs gremlins introduces; in `internal/store` we kill 75%; in `internal/webhooks` we kill 53%. The auth+store baselines are strong; webhooks deserves more tests in a future patch.

---

## Threat model walkthrough

The threat model in `docs/security.md` (canonical source) was reviewed against the W8 + v1.0 codebase. This audit confirms the following pre-existing mitigations and adds the bolded ones from this pass.

| Threat | Mitigation | Verified by |
|---|---|---|
| Path traversal via artifact key | `filepath.Clean`+abs+ArtifactsDir prefix check in `internal/artifact/store.go` | `TestArtifactStore_Traversal` |
| Path traversal via tar restore | Refuse entries with `..` or absolute prefix; **0o640 mode mask** to drop world-writable / setuid | `TestRestore_RejectsTraversal` |
| **gzip bomb on restore** | **`io.LimitReader` 200 GiB cap; env-overrideable** | manual smoke |
| SSRF via webhook URL | Block loopback / RFC1918 / link-local; DNS resolves with 3 s timeout; env-overridable; `lmf://echo` carved out as in-process target | `TestValidateWebhookURL`, `TestEchoBypass` |
| Webhook replay / forgery | HMAC-SHA256 signed bodies (header `X-LiteMLflow-Signature`); secret stored per webhook | `TestVerifySignature` |
| Header smuggling — `X-LiteMLflow-User` | Stripped on every request before auth middleware sets it | `TestAuth_HeaderSmuggling` |
| Run lineage cycle DoS | Visited set + 256-depth cap in `GetRunLineage` | `TestLineage_Cycle` |
| **Open redirect via OIDC `return_to`** | **`safeReturnTo` rejects non-absolute / `//host` / CRLF / NUL / >2 KiB** | **`TestSafeReturnTo` (new)** |
| **Goroutine leak via session-touch** | **5 s timeout on detached context** | manual review |
| OIDC nonce reuse | `EncryptedState` cookie binds nonce; `ID-Token`'s nonce claim verified against expected | `TestOIDCCallback_BadNonce` |
| Body-size DoS | `bodyLimitMiddleware` enforces `cfg.MaxRequestSize` (default 32 MiB) on all non-artifact endpoints | `TestBodyLimit` |
| gRPC OTLP DoS | `MaxRecvMsgSize=64 MiB`, `MaxConcurrentStreams=1024` | `internal/grpcotlp/ingest_test.go` |
| Webhook delivery DoS to ourselves | `lmf://echo` in-process — no socket | `TestEchoBypass` |
| SQL injection | All user-input bound via `?` placeholders; dynamic ORDER BY/LIMIT validated against allowlist; G202 findings audited and listed above | gosec accept-list |
| File modes too permissive | dirs 0o750, artifacts 0o640, internal files 0o600 | gosec re-run |
| Mass-assignment in API DTOs | DTOs have explicit field allowlists; struct tags `omitempty` for optional fields | code review |
| RBAC bypass via missing workspace | `workspaceMiddleware` falls back to "default" but RBAC still applies; admins required for cross-workspace mutation | `internal/server/rbac_test.go` |

### Residual risks (accepted)

1. **`auth=none` mode**: by design, single-user instances run without auth. Not a v1.0 issue but a deployment choice; surfaced in `docs/quickstart.md`.
2. **Operator manifest pulls images from `ghcr.io/gorevds/litemlflow:<version>`** — operators deploying off-internet need to pre-mirror.
3. **No paid pen-test**: We ship the OSS scanner output. Production-critical use cases should arrange a paid third-party engagement before deploying LiteMLflow to a hostile multi-tenant environment.

---

## CI integration

| Workflow | Trigger | What it does |
|---|---|---|
| `.github/workflows/security.yml` | Every push + PR + Mon 08:00 UTC cron | govulncheck (root + operator), gosec (with accept-list gate), semgrep |
| `.github/workflows/mutation.yml` | Nightly 04:00 UTC + manual | gremlins on auth (≥80%), store (≥70%), webhooks (≥50%) |
| `.github/workflows/kind.yml` | PR/push touching operator + manual | Boots kind, builds & loads server+operator images, applies CRD+RBAC+manager, creates LiteMLflow CR, smokes `/healthz` |

The accept-list gate (`.github/scripts/check_gosec.py`) parses this document and refuses to merge a PR that introduces a new HIGH gosec finding unless that PR also adds a justification line above.

---

## Sign-off

This audit covers the v1.0 source tree as of tag `v1.0.0`. Next audit is at the next minor release (v1.1) or when a maintainer requests it via `make security`.

— Maintainer: dmitrii@gorev.space
— Audit date: 2026-05-08
