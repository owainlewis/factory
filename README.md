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

In the current V1, `factory run` selects a workflow.
It automatically includes `.factory/OBJECTIVES/current-objective.md` or `.factory/OBJECTIVES/current.md` when present.
Named objective selection is planned, not implemented yet.

A typical target repo starts with one default workflow:

```text
.factory/
  AGENTS.md
  STANDARDS.md
  WORKFLOWS/
    standards-check.md
  OBJECTIVES/
    current-objective.md
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
go run ./cmd/factory run cortex standards-check --mode plan
```

Execute a repo-owned workflow:

```sh
go run ./cmd/factory run cortex standards-check --mode execute
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

Current limitation: Factory does not yet accept a named objective argument.
Use `.factory/OBJECTIVES/current-objective.md` for the current directed goal.

## Locks and run lifecycle

Factory must not run two write-capable jobs against one repo at the same time.
Before a run touches a repo it takes a per-repo lock under
`.factory-state/locks/<repo>.lock`.

- While a run holds the lock, a second `factory run` for the same repo **skips
  cleanly**: it writes a run record with status `skipped` and exits without
  error. It does not block or wait.
- The lock records the owning process id. If a run crashes and leaves a lock
  behind, the next run detects that the owner process is gone and **reclaims
  the stale lock** automatically.
- A lock whose owner cannot be determined is left in place to avoid stealing a
  live lock. Remove `.factory-state/locks/<repo>.lock` by hand if you are sure
  no run is active.

Every run writes a JSON record under `.factory-state/runs` with a final status:

- `success` - the run completed.
- `skipped` - the repo was locked by another run.
- `blocked` - the run stopped and needs human input.
- `failed` - the run hit an error.
- `cancelled` - the run was cancelled or timed out.

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
