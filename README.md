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

## Requirements

- Go 1.23 or newer to build and run the CLI.
- `git` on your `PATH`. Factory clones and fetches target repos.
- The Claude Code CLI (`claude`) on your `PATH` to run agent workflows. See the
  [Claude Code adapter](#claude-code-adapter) section.
- The GitHub CLI (`gh`), authenticated, only for `factory labels`.

Build the CLI:

```sh
go build -o factory ./cmd/factory
```

Or run it without building, as the examples below do, with `go run ./cmd/factory`.

## Your first run

1. Clone this repo and build, or use `go run ./cmd/factory`.
2. Create a `config.yaml` next to where you run Factory. Register at least one
   repo (see [Config](#config)).
3. Audit the repo to see its health. This is read-only:

   ```sh
   go run ./cmd/factory audit <repo>
   ```

4. Plan a workflow. Plan mode inspects and reports; it does not edit files:

   ```sh
   go run ./cmd/factory run <repo> standards-check --mode plan
   ```

5. Read the run record and log printed at the end of the run. They live under
   `.factory-state/runs` and `.factory-state/logs`.

A successful plan run prints a run id with status `success` and the paths to its
log and record. From there, `--mode execute` lets the agent open a draft pull
request.

## Claude Code adapter

Factory's first agent adapter shells out to the Claude Code CLI. To use it:

- Install the `claude` CLI and make sure it is on your `PATH`.
- Authenticate it and keep enough credit balance. When Claude Code reports the
  credit balance is too low, Factory records the run as `blocked` rather than
  failing.

Factory invokes `claude -p --permission-mode <mode>` in the repo checkout, where
plan mode maps to Claude's `plan` permission mode and execute mode maps to
`auto`.

## Local state

Factory keeps all of its state under the `data_dir` from `config.yaml`, which
defaults to `.factory-state`:

```text
.factory-state/
  repos/       Factory-owned checkouts of managed repos
  worktrees/   per-run worktrees for execute-mode runs
  runs/        JSON run records, one per run
  logs/        text logs, one per run
  locks/       per-repo locks
```

This directory is internal Factory state. It is safe to delete when no run is
active; Factory recreates what it needs on the next run.

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

## Audit

`factory audit <repo>` is read-only.
It checks common repo health signals and prints a Markdown report.

The audit report can suggest gaps, objectives, and workflows.
It should help decide what to run next before any agent edits files.

## Known limits

Factory is an early MVP. Today:

- Claude Code is the only agent adapter.
- `factory run` does not yet accept a named objective argument. It includes
  `.factory/OBJECTIVES/current-objective.md` or `current.md` when present.
- There is no daemon or scheduler. Runs are one-shot CLI invocations.
- `factory labels` requires the `gh` CLI and only manages the standard Factory
  labels.
- State is local to one machine under `.factory-state`. There is no shared or
  hosted backend.

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
