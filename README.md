# Code Factory

Code Factory is a local-first runner for coding agents.
It keeps important GitHub repos moving by cloning them locally, compiling repo-owned engineering context, dispatching agents, saving logs, and leaving humans in control.

Factory is not a task dump.
Factory is not a hosted service yet.
Factory does not merge PRs.

## Current V1

The first working version proves this spine:

```text
config -> clone or fetch repo -> build prompt -> run Claude Code -> save log -> save run record
```

It supports:

- `factory repos`
- `factory workflows <repo>`
- `factory run <repo> hello`
- `factory runs`
- Claude Code as the first agent adapter
- local repo checkouts under `.factory-state/repos`
- JSON run records under `.factory-state/runs`
- text logs under `.factory-state/logs`

## Config

`config.yaml` lists repos that Factory can manage.
It is a local runner registry, not the source of repo standards, workflows, or journals.

```yaml
factory:
  name: Code Factory
  purpose: Run disciplined agent loops across important GitHub repos.
  data_dir: .factory-state

repos:
  cortex:
    url: git@github.com:owainlewis/cortex.git
    branch: main
    agent: claude
```

## Commands

List repos:

```sh
go run ./cmd/factory repos
```

Run the no-edit smoke workflow:

```sh
go run ./cmd/factory run cortex hello
```

List workflows for a repo:

```sh
go run ./cmd/factory workflows cortex
```

List run records:

```sh
go run ./cmd/factory runs
```

## Target Repo Model

Each target repo should own its standards, workflows, and journal:

```text
AGENTS.md
STANDARDS.md
WORKFLOWS/
  bug-fix.md
  issue-triage.md
  docs-update.md
  dependency-update.md
  release.md
  review-pr.md
JOURNAL.md
```

Factory owns orchestration.
The target repo owns intent.

Factory should not store target repo standards, journals, or runnable project workflows here.
Those belong in each target repo.

## Standard Factory Labels

Factory labels are standard across repos:

- `factory-ready`: an agent may work this issue now.
- `factory-triage`: the issue needs clarification, acceptance criteria, or scope shaping.
- `factory-needs-human`: the issue needs a human decision before implementation.
- `factory-blocked`: the issue cannot move until a named blocker is resolved.

## Docs

- [PRD](docs/prd.md)
- [Runner spec](docs/factory-runner/spec.md)
- [STANDARDS.md examples](docs/standards-examples.md)

## Safety Rules

- Do not merge PRs.
- Do not push directly to a default branch.
- Do not run broad cleanup.
- Do not make public claims without evidence.
- Stop if the workflow or issue is unclear.
