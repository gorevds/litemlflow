# Security Policy

## Reporting a vulnerability

Please report security vulnerabilities **privately**. Do not open a public GitHub issue.

Email: security@litemlflow.invalid (placeholder until launch)
GPG: (TBD, will be published before v1.0)

We will acknowledge your report within 72 hours and aim to provide a fix or mitigation timeline within 7 days.

## Disclosure timeline

1. We acknowledge the report within 72 hours.
2. We work on a fix in a private branch.
3. We coordinate a release date with the reporter.
4. We publish the fix and a security advisory simultaneously.
5. After 90 days from disclosure, we publicly credit the reporter (unless they prefer to stay anonymous).

## Supported versions

During pre-1.0, only the latest minor receives security fixes. After 1.0, the previous minor will receive critical security backports for 6 months.

## Threat model

LiteMLflow's threat model is documented in [docs/spec/threat-model.md](docs/spec/threat-model.md).

Summary:
- LiteMLflow is intended for trusted-network deployment by default.
- TLS, OIDC, and basic auth are first-class but optional features.
- Artifact storage may contain user-supplied content; LiteMLflow does not execute or interpret artifacts beyond serving them with `Content-Disposition: attachment`.
- Plugins run out-of-process and cannot escalate to core privileges.
