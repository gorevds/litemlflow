## Summary

<!-- One paragraph: what does this PR do and why? -->

## Changes

<!-- Bullet list of key changes. Keep it concise. -->

## Test plan

<!-- How did you verify this works? Paste command output or test run summary. -->

## Checklist

- [ ] DCO sign-off: every commit in this PR includes `Signed-off-by: Name <email>` (`git commit -s`)
- [ ] Tests added / updated for new behavior
- [ ] `make test` passes locally (Go + Python)
- [ ] `make compat-test` passes if MLflow API surface was changed
- [ ] `make lint` passes (no new warnings)
- [ ] Docs updated if behavior / config / CLI changed
- [ ] No new runtime dependencies added without an ADR in `docs/adr/`
- [ ] Performance: no >5% regression on hot paths (`make bench` if relevant)

## Related issues

<!-- Closes #NNN / Fixes #NNN / Part of #NNN -->
