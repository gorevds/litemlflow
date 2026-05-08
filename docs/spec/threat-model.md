# Threat model (v0.1)

## Trust boundaries

```
+---------- untrusted internet -----------+
|                                         |
|   +------ trusted-network LAN -----+    |
|   |                                |    |
|   |   +---- litemlflow process --+ |    |
|   |   |                          | |    |
|   |   |   +---- plugin proc ---+ | |    |
|   |   |   +--------------------+ | |    |
|   |   +--------------------------+ |    |
|   +--------------------------------+    |
+-----------------------------------------+
```

Default deployment assumes a trusted network. Exposing to the open internet is supported but requires explicit configuration: TLS via `--tls auto` (Let's Encrypt), and at least basic auth or OIDC.

## Assets

- The SQLite database (run history, params, metrics).
- Artifact files (may include model weights, datasets).
- Auth secrets (OIDC client secret, basic credentials).
- TLS private keys.

## Threats (STRIDE)

### Spoofing
- **Forged user identity**: mitigated by OIDC (verified ID tokens with signature check) or basic auth + TLS. The `verifyIDToken` function is continuously exercised by fuzz tests (`FuzzVerifyIDToken`, `FuzzVerifyIDToken_SignatureCorruption`) that verify no malformed token can cause a panic or a false-positive verification.
- **Forged plugin identity**: plugins authenticate via UNIX socket + capability handshake. Plugins cannot listen on TCP.

### Tampering
- **Database corruption from concurrent writers**: SQLite WAL + single-writer policy prevents.
- **Path traversal in artifact upload**: every artifact path is `filepath.Clean`'d and required to be a strict prefix-match of the run's artifact root.
- **HTTP response splitting**: Go's `net/http` rejects header values containing CR/LF.

### Repudiation
- **Audit trail**: every state-mutating call is logged with user, request id, run id (in v0.1, append-only JSON-Lines log; in v0.2, queryable audit table).

### Information disclosure
- **Cross-run artifact leaks**: artifact paths are scoped per run. Auth gates artifact reads.
- **Stack traces in error responses**: production builds suppress stack traces; dev mode has them behind `LITEMLFLOW_DEV=1`.
- **Server version in headers**: hidden by default (`Server:` header is empty).

### Denial of service
- **Large request bodies**: max body size enforced (default 100 MB for non-artifact, 5 GB for artifact upload — configurable).
- **Slow-loris**: read/write/idle timeouts set to 30s/30s/120s on the HTTP server.
- **Metric flood**: `log-batch` is capped at 1000 metrics + 100 params + 100 tags per call (matching MLflow).
- **Malformed OTLP/JSON**: the `IngestOTLP` and `IngestTraces` handlers are fuzz-tested with oversized arrays, deeply nested structures, wrong field types, and extreme timestamp values to ensure no panic or hang occurs.

### Elevation of privilege
- **Arbitrary file read via artifact path**: same path-traversal mitigation as above.
- **SQL injection**: every query uses parameter binding; no string concatenation. The filter/predicate parsers (`parseRunFilter`, `parseRunPredicate`, `parseExperimentFilter`) are continuously exercised by Go native fuzz tests (`FuzzParseRunPredicate`, `FuzzParseRunFilter`, `FuzzParseExperimentFilter`) that assert the generated SQL never contains a bare single-quote character outside `?` placeholders — see `docs/contributing-fuzz.md`.
- **Plugin escape**: plugins are subprocesses with restricted capabilities and no access to the SQLite file directly.

## What is *not* protected by v0.1

- **Tenant isolation at the data layer.** All data sits in one SQLite file. v0.2 will add per-workspace encryption keys.
- **Hardware-grade secret protection.** Secrets live on disk under mode `0600`; they are not HSM-bound.
- **DDoS at the network layer.** Run behind Cloudflare / Tailscale / AWS WAF if exposed to the internet.

## Data classification

| Data | Classification | Encryption at rest |
|---|---|---|
| Run metadata (params, metrics) | Internal | filesystem-level only |
| Artifacts | Variable (may be PII) | filesystem-level only |
| Prompts | Variable (may include API instructions) | filesystem-level only |
| Auth secrets (passwords hashed; OIDC client secret encrypted) | Restricted | argon2id for passwords; AES-GCM with master key for client secret |
| TLS private keys | Restricted | AES-GCM with master key |

The master key is derived from `LITEMLFLOW_MASTER_KEY` env var. If unset on first run, a random key is generated and stored in `$DATA/.master-key` with mode `0400`.

## Dependency policy

- Pinned by go.sum / pyproject.toml.
- Renovate scans for advisories.
- Public-facing dependency updates require a passing security CI run.
- We do not vendor third-party Go modules; reproducibility is achieved via `go.sum` and Go module proxy.
