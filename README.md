# Factory

Factory is a local CLI for running coding agents against repo-owned engineering process.

It keeps the runner outside the target repo, but lets each target repo define its own standards, workflows, objectives, and journal under `.factory/`.

Factory is not a hosted service.
Factory is not a task tracker.
Factory is not the engineer.
It prepares context, runs an agent, and records what happened.

## Current Status

Factory is an early local runner.

The current loop is:

```text
config -> clone or fetch repo -> build prompt -> run agent -> save log -> save run record
```

It supports:

- `factory audit <repo>`
- `factory repos`
- `factory workflows <repo>`
- `factory run <repo> [workflow] [--mode plan|execute]`
- `factory runs`
- Claude Code as the first agent adapter
- Factory-owned repo checkouts under `.factory-state/repos`
- execute-mode worktrees under `.factory-state/worktrees`
- JSON run records under `.factory-state/runs`
- text logs under `.factory-state/logs`
- per-repo locks under `.factory-state/locks`

`.factory-state/repos` is internal Factory state.
Do not use those checkouts as human working copies.
Factory may fetch, checkout, and update them while running commands.
Execute-mode runs use per-run worktrees so agent edits do not dirty the repo cache.

## Repo Contract

Each target repo should own its Factory files:

```text
.factory/
  AGENTS.md
  STANDARDS.md
  WORKFLOWS/
    standards-check.md
  OBJECTIVES/
  JOURNAL.md
```

These files have separate jobs:

- `.factory/AGENTS.md` gives repo-specific agent instructions.
- `.factory/STANDARDS.md` says what good looks like.
- `.factory/WORKFLOWS/standards-check.md` is the default repeatable playbook.
- `.factory/OBJECTIVES/` contains current desired outcomes.
- `.factory/JOURNAL.md` records handoff notes between runs.

Factory owns orchestration.
The target repo owns intent.
Prefer one workflow and many objectives.
Add another workflow only when the process is truly different.

A typical target repo starts with short workflow area names:

```text
.factory/
  AGENTS.md
  STANDARDS.md
  WORKFLOWS/
    standards.md
    github.md
    docs.md
    ci.md
    release.md
    security.md
    dependencies.md
    tests.md
  OBJECTIVES/
    2026-06-29-release.md
  JOURNAL.md
```

## Config

`config.yaml` is a local registry of repos Factory can manage.
It should contain only the data needed to find and run a repo.

```yaml
factory:
  name: Factory
  purpose: Run disciplined agent loops across important GitHub repos.
  data_dir: .factory-state

repos:
  cortex:
    url: git@github.com:owainlewis/cortex.git
    branch: main
    agent: claude
```

## Commands

Audit a repo and print a Markdown health report:

```sh
go run ./cmd/factory audit factory
```

List managed repos:

```sh
go run ./cmd/factory repos
```

List workflows for a repo:

```sh
go run ./cmd/factory workflows cortex
```

Run the built-in no-edit smoke workflow:

```sh
go run ./cmd/factory run cortex hello
```

Plan a repo-owned workflow without editing files:

```sh
go run ./cmd/factory run cortex standards --mode plan
```

Execute a repo-owned workflow:

```sh
go run ./cmd/factory run cortex standards --mode execute
```

List run records:

```sh
go run ./cmd/factory runs
```

## Modes

`plan` mode asks the agent to inspect and report.
It must not edit files.

`execute` mode may create a branch, edit files, commit, push, and open a draft PR when the selected workflow asks for that.
It must not merge PRs or push to a default branch.

## Audit

`factory audit <repo>` is read-only.
It checks common repo health signals and prints a Markdown report.

The audit report can suggest gaps, objectives, and workflows.
It should help decide what to run next before any agent edits files.

## Docs

- [PRD](docs/prd.md)
- [Factory Standard](docs/factory-standard.md)
- [What makes a great software project](docs/what-makes-a-great-software-project.md)
- [Runner spec](docs/factory-runner/spec.md)
- [STANDARDS.md examples](docs/standards-examples.md)
- [Workflow examples](docs/workflow-examples.md)
- [Objective examples](docs/objective-examples.md)

## Safety Rules

- Do not merge PRs.
- Do not push directly to a default branch.
- Do not run broad cleanup.
- Do not make public claims without evidence.
- Stop if the workflow or issue is unclear.
