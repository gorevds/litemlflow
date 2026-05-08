---
title: Mutation Testing
---
# Mutation Testing Guide

LiteMLflow uses [gremlins](https://github.com/go-gremlins/gremlins) for
mutation testing on the core store and auth packages.

Mutation testing systematically modifies ("mutates") the production code and
runs the test suite against each mutant. If the tests fail, the mutant is
"killed" -- good. If the tests pass, the mutant "survives" -- that is a gap in
test coverage. The mutation score is the percentage of killed mutants.

## Installing gremlins

```bash
go install github.com/go-gremlins/gremlins/cmd/gremlins@latest
```

Confirm the install:

```bash
gremlins --version
```

## Running mutation tests

### Via make

```bash
make mutation
```

This runs gremlins on both `internal/store` and `internal/auth` with a 70%
efficacy threshold. A score below 70% causes a non-zero exit code.

### Manually

```bash
# Store layer (parsers, SQLite wrapper)
gremlins unleash --threshold-efficacy 70 ./internal/store/...

# Auth layer (OIDC, JWT, session management)
gremlins unleash --threshold-efficacy 70 ./internal/auth/...
```

### Focusing on specific files

```bash
gremlins unleash ./internal/store/sqlite.go
```

### Adjusting the threshold

```bash
gremlins unleash --threshold-efficacy 80 ./internal/store/...
```

## Understanding the output

gremlins prints a table of mutants per file, showing each mutation and whether
it was killed (KILLED), survived (SURVIVED/LIVED), or timed out (TIMEOUT).

Example output (illustrative):

```
internal/store/sqlite.go
  LINE  | MUTATION         | STATUS
  ------+------------------+--------
  328   | change > to >=   | KILLED
  334   | remove return    | KILLED
  699   | invert condition | LIVED   <-- test gap
```

A surviving mutant (`LIVED`) means the test suite did not detect the change.
Consider adding a targeted test case that would catch that mutation.

## Baseline mutation score

> gremlins was not installed in the worktree environment during the v1.0
> stabilisation sprint. The baseline score will be established on the first
> CI run that includes the `mutation` target.
>
> To establish the baseline locally:
>
> ```bash
> go install github.com/go-gremlins/gremlins/cmd/gremlins@latest
> make mutation 2>&1 | tee docs/mutation-baseline.txt
> ```
>
> Commit `docs/mutation-baseline.txt` alongside the first passing CI run.

## Mutation operators

gremlins applies the following mutations by default:

| Operator | Description |
|---|---|
| Conditionals boundary | `<` to `<=`, `>` to `>=`, etc. |
| Negate conditionals | `==` to `!=`, `true` to `false`, etc. |
| Increment / decrement | `i++` to `i--` |
| Arithmetic | `+` to `-`, `*` to `/`, etc. |
| Remove return | replaces a return statement with zero values |
| Invert negatives | negates numeric literals |

Refer to the [gremlins documentation](https://github.com/go-gremlins/gremlins/blob/main/docs/mutations.md)
for the full list and examples.

## Integrating mutation scores into CI

The `make mutation` target exits non-zero if the efficacy threshold is not met,
making it suitable as a CI gate. To add it to the CI pipeline:

```yaml
# .github/workflows/ci.yml (example)
- name: Mutation testing
  run: |
    go install github.com/go-gremlins/gremlins/cmd/gremlins@latest
    make mutation
```

Because mutation testing is CPU-intensive (each mutant requires a full test run),
it is recommended to run it on a schedule (e.g., nightly) rather than on every
pull request.

## Tips for improving mutation score

1. **Test boundary conditions explicitly.** If a predicate uses `<`, add a
   test case where the value is exactly at the boundary.
2. **Verify both branches of every `if`.** If the `if` body is tested but the
   `else` is not, inverting the condition will survive.
3. **Assert return values.** A "remove return" mutant survives if the caller
   ignores the return value.
4. **Use table-driven tests.** They naturally cover many input variations and
   kill more mutants per line of test code.
