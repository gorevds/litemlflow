---
title: Fuzz Testing
---
# Fuzz Testing Guide

LiteMLflow uses Go 1.18+ native fuzz testing (`go test -fuzz`) to continuously
probe parser and input-processing code paths that accept untrusted data.

## What is fuzzed

| File | Functions fuzzed | What they parse |
|---|---|---|
| `internal/store/sqlite_fuzz_test.go` | `FuzzParseRunPredicate`, `FuzzParseRunFilter`, `FuzzParseExperimentFilter`, `FuzzSplitOnAnd` | MLflow filter expression language (the text fed to `SearchRuns` / `SearchExperiments`) |
| `internal/auth/oidc_fuzz_test.go` | `FuzzVerifyIDToken`, `FuzzVerifyIDToken_SignatureCorruption` | JWT / OIDC ID-token validation |
| `internal/api/native/otlp_fuzz_test.go` | `FuzzIngestOTLP`, `FuzzIngestTraces` | OTLP/JSON and native trace HTTP bodies |

## Running fuzz tests

### Seed-corpus only (fast, used in CI)

Every fuzz function doubles as a regular table-driven test when run without the
`-fuzz` flag. The seed inputs are the hand-crafted examples baked into each
`f.Add(...)` call:

```bash
go test -count=1 ./internal/store/
go test -count=1 ./internal/auth/
go test -count=1 ./internal/api/native/
```

The `make test` target already covers this.

### Short fuzz run (CI smoke check)

```bash
make fuzz-short
```

This runs each fuzzer for 20 seconds — enough to catch obvious crashes without
blocking CI for minutes. Equivalent to:

```bash
go test -fuzz='^FuzzParseRunPredicate$'     -fuzztime=20s ./internal/store/
go test -fuzz='^FuzzParseRunFilter$'        -fuzztime=20s ./internal/store/
go test -fuzz='^FuzzParseExperimentFilter$' -fuzztime=20s ./internal/store/
go test -fuzz='^FuzzSplitOnAnd$'            -fuzztime=20s ./internal/store/
go test -fuzz='^FuzzVerifyIDToken$'         -fuzztime=20s ./internal/auth/
go test -fuzz='^FuzzVerifyIDToken_SignatureCorruption$' -fuzztime=20s ./internal/auth/
go test -fuzz='^FuzzIngestOTLP$'            -fuzztime=20s ./internal/api/native/
go test -fuzz='^FuzzIngestTraces$'          -fuzztime=20s ./internal/api/native/
```

### Extended local run (recommended before merging parser changes)

Pick a specific fuzzer and run for several minutes:

```bash
go test -fuzz='^FuzzParseRunPredicate$' -fuzztime=10m ./internal/store/
```

When the fuzzer finds an interesting new input, it saves it to a seed file
under `testdata/fuzz/<FuzzFunctionName>/`. Commit those files so future runs
replay the same inputs and CI can catch regressions.

### Running a specific seed file

```bash
go test -run='FuzzParseRunPredicate/corpus_filename' ./internal/store/
```

## Understanding the oracles

Each fuzz function encodes these invariants:

1. **No panic** -- the function must handle any input without crashing. A
   `recover()` in the fuzz body catches panics and turns them into failures.

2. **No bare quotes in output SQL** -- parser functions return parameterised
   SQL clauses. User values are bound as `?` parameters, never concatenated.
   If the generated clause contains a `'` character, user input leaked into
   the SQL structure, which is a potential injection vector (`noUnescapedQuote`
   helper enforces this).

3. **Correct error handling for malformed input** -- invalid inputs must return
   a non-nil error, not silently return empty or garbage output.

## Extending the fuzz suite

To add a new fuzz target:

1. Create `<package>/<name>_fuzz_test.go`.
2. Write a `func FuzzXxx(f *testing.F)` function.
3. Add seed inputs via `f.Add(...)` covering valid, edge-case, and adversarial
   inputs.
4. Add the fuzzer to the `fuzz-short` Makefile target.
5. Document the function in the table above.

Refer to the [Go fuzzing documentation](https://go.dev/doc/fuzz) for details.

## Persistent corpus and CI integration

The Go fuzzing engine writes newly discovered interesting inputs to
`testdata/fuzz/<FunctionName>/`. These files are checked in and replayed on
every `go test` run, ensuring regressions found during fuzzing become
permanent regression tests.

If a fuzzer run produces a failure file (e.g. after a crash), it will be placed
in `testdata/fuzz/<FunctionName>/` automatically. Fix the bug, verify the seed
still fails before the fix, then verify it passes after. Commit both the fix
and the seed file.
