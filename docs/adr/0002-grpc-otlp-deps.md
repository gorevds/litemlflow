# ADR 0002: Add google.golang.org/grpc and go.opentelemetry.io/proto/otlp for OTLP/gRPC

- Status: Accepted
- Date: 2026-05-08
- Decision-makers: Core team

## Context

LiteMLflow's dependency policy (see `docs/architecture.md`) requires an ADR for
any dependency outside the explicitly allowed set (stdlib, modernc.org/sqlite,
go-chi/chi, golang.org/x/...). Two new dependencies are needed to implement the
OTLP/gRPC receiver:

- `google.golang.org/grpc` — the official Go gRPC stack.
- `go.opentelemetry.io/proto/otlp` — protobuf definitions for the
  OpenTelemetry Protocol (OTLP), including the `TraceService` RPC used by
  virtually every OpenTelemetry SDK and collector.

## Decision

Add both dependencies. They are the authoritative, wire-protocol-level libraries
for OTLP/gRPC and cannot be replaced without effectively reimplementing them.

## Rationale

### Why we cannot avoid these deps

OTLP/gRPC is a binary protocol defined by protobuf IDL files maintained by the
OpenTelemetry project. Accepting an OTLP/gRPC connection requires:

1. A gRPC server (HTTP/2 framing, header compression, stream multiplexing,
   flow control, TLS handshake, keepalives) — this is precisely what
   `google.golang.org/grpc` provides.
2. The generated Go code for `opentelemetry.proto.collector.trace.v1.TraceService`
   — this is what `go.opentelemetry.io/proto/otlp` provides.

Reimplementing either would mean:

- **gRPC from scratch**: implementing the HTTP/2 frame parser, HPACK header
  compression, stream state machine, flow-control windows, and the gRPC framing
  layer (`Content-Type: application/grpc`, 5-byte frame header, trailer
  decoding). This is a multi-thousand-line implementation maintained by Google
  and used in production by millions of services. It is not a candidate for
  in-house reimplementation.

- **Cloning the OTLP proto definitions**: the `.proto` files and the generated
  Go code are versioned together with the OTel specification. Vendoring and
  hand-maintaining a copy would immediately fall behind the upstream and break
  compatibility with new OTel SDK releases. The generated code is also not
  human-maintainable: it is 100% derived output.

In both cases the alternative is "clone a large, externally maintained,
specification-driven artifact" — which is strictly worse than taking a
dependency on the canonical upstream package.

### Binary size impact

The addition of gRPC + proto increases the binary from ~12 MB to ~18-22 MB.
This is within the explicitly accepted range stated in the functional spec:

> Binary size: it's OK if the binary grows from ~12 MB to ~18-22 MB due to
> grpc dep — that's expected.

### Security surface

The gRPC receiver is opt-in: it only starts when `--otlp-grpc-addr` is set.
In the default configuration the binary is unchanged — no new network port is
opened, no new attack surface is introduced.

No TLS is configured on the gRPC listener directly. The intended deployment
model is:

- **Development**: plaintext gRPC on loopback (`127.0.0.1:4317`).
- **Production**: place a TLS-terminating sidecar (e.g., Envoy, Nginx with
  grpc_pass) in front. This is the same pattern used for the HTTP listener and
  avoids duplicating certificate management inside the binary.

### Transitive deps

The two added packages pull in:

| Package | Role |
|---|---|
| `google.golang.org/protobuf` | protobuf runtime (no CGO) |
| `google.golang.org/genproto/googleapis/rpc` | gRPC status codes |
| `golang.org/x/net` | HTTP/2 implementation used by gRPC |
| `golang.org/x/text` | Unicode normalization (transitive from x/net) |

All are pure Go, have no CGO requirements, and do not affect cross-compilation.

## Consequences

### Positive

- LiteMLflow can now receive traces from any standard OpenTelemetry SDK or
  collector over OTLP/gRPC, the most widely supported OTel transport.
- The gRPC receiver is a thin adapter (< 300 LoC including tests) that
  reuses the existing span mapping and `store.Store` insertion path. There is
  no duplicate business logic.
- Feature is fully opt-in; zero runtime cost when not configured.

### Negative

- The binary grows by ~6-10 MB.
- `google.golang.org/grpc` is a large dependency with frequent minor releases.
  It must be kept up to date to track security fixes.

### Mitigations

- `go mod tidy` + `dependabot` (or equivalent) will surface updates.
- The gRPC listener is not exposed in the default configuration; reducing the
  risk that a gRPC vulnerability in the server affects users who do not use
  the feature.

## Alternatives considered

- **HTTP/2 + hand-written gRPC parser**: rejected — see "Why we cannot avoid
  these deps" above.
- **OTLP/HTTP only (no gRPC)**: rejected — the spec requires both transports
  for full OTel SDK compatibility. Many SDKs default to gRPC (the Go and Java
  SDKs, for example).
- **Plugin / out-of-process gRPC receiver**: rejected for v1 — it would
  require a plugin protocol and adds operational complexity for what is a
  thin, well-bounded feature.
