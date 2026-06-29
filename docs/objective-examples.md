# Objective Examples

Objectives are repo-owned work orders.

They live in the target repo under `OBJECTIVES/`.
Factory compiles them into agent goals at runtime.

```text
workflow = repeatable process
objective = current desired outcome
goal = runtime prompt sent to the coding agent
```

## `OBJECTIVES/2026-06-29-release-readiness.md`

```md
# Objective: Release readiness

## Goal

Make this project releasable by a new user.

## Context

This project is a command-line tool.
A user should be able to install it, build it, test it, run it, and understand release status.

## Scope

- README install, build, test, and run sections
- GitHub Actions CI
- `CHANGELOG.md`
- `docs/releasing.md`
- release workflow proposal

## Done

- one focused draft pull request is opened
- relevant local checks have run where possible
- remaining release gaps are listed

## Workflow

Use `WORKFLOWS/release-readiness.md`.

## Mode

Start in plan mode.
Execute one small safe change after planning.

## Stop Rules

- Do not publish a release.
- Do not change the license.
- Do not push to the default branch.
- Stop for human review on versioning decisions.
```

## `OBJECTIVES/2026-06-29-docs-audit.md`

```md
# Objective: Documentation audit

## Goal

Find and fix one documentation gap that blocks a new contributor.

## Scope

- README
- docs directory
- examples
- build and test commands

## Done

- one focused draft pull request is opened
- the changed docs are checked for accuracy
- any larger documentation gaps are listed

## Workflow

Use `WORKFLOWS/docs-readiness.md`.

## Stop Rules

- Do not make broad rewrites.
- Do not invent unsupported product claims.
- Stop if code behavior is unclear.
```

## `OBJECTIVES/2026-06-29-ci-readiness.md`

```md
# Objective: CI readiness

## Goal

Make pull requests run the project build and tests.

## Scope

- GitHub Actions workflow files
- documented build and test commands
- language setup

## Done

- one focused draft pull request is opened
- CI runs build and tests
- local checks have run where possible

## Workflow

Use `WORKFLOWS/ci-readiness.md`.

## Stop Rules

- Do not add secrets.
- Do not require paid services.
- Stop if the correct build or test command is unclear.
```
