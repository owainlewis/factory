# Factory Runner Spec

## What

Factory is a local runtime for autonomous engineering.
It manages local checkouts, compiles repository context, dispatches coding agents, records outcomes, and repeats.

Factory is not the engineer.
Factory runs engineers.

## Repository Contract

Every managed repository owns its engineering process:

```text
.factory/
  AGENTS.md
  STANDARDS.md
  WORKFLOWS/
  OBJECTIVES/
  JOURNAL.md
```

`.factory/AGENTS.md` describes coding-agent instructions.
`.factory/STANDARDS.md` defines what good looks like.
`.factory/WORKFLOWS/*.md` defines engineering playbooks such as bug fixes, triage, docs updates, dependency updates, releases, and PR review.
`.factory/OBJECTIVES/*.md` defines current desired outcomes for one run or a short sequence of runs.
`.factory/JOURNAL.md` is append-only handover between runs.

Factory must not duplicate target repo standards, workflows, objectives, journals, checks, issue labels, or product purpose in this repo.

## Factory Responsibilities

Factory owns deterministic orchestration:

- load managed repositories
- clone or fetch the latest code
- discover repository-owned workflows
- compile context from repository files
- dispatch the configured coding agent
- capture logs and run records
- update external systems when workflows ask for it
- append journal entries in later versions

Agents own reasoning:

- planning work
- choosing implementation details
- applying code changes
- verifying behavior
- deciding when work is blocked

## Local Registry

`config.yaml` is a local runner registry.
It should contain only the data needed to find and run a repo.

```yaml
repos:
  cortex:
    url: git@github.com:owainlewis/cortex.git
    branch: main
    agent: claude
```

The registry is operator state, not project intent.

## Commands

Current commands:

```sh
factory audit <repo>
factory repos
factory workflows <repo>
factory run <repo> [workflow] [--mode plan|execute]
factory runs
```

Planned commands:

```sh
factory objective <repo> <objective> --mode plan|execute
factory plan <repo>
factory execute <repo>
factory triage <repo>
factory daemon
```

`plan` is the default mode.
It asks the agent to inspect and report without editing files.

`execute` is write-capable.
It allows the agent to make a workflow-scoped change, create a non-default branch, commit it, push it, and open a draft pull request when the workflow asks for code changes.

## Audit

`factory audit <repo>` is read-only.
It inspects a repo and prints a Markdown health report.

The first audit version is deterministic.
It checks repo shape and common project signals:

- README
- license
- build metadata
- test signal
- GitHub Actions workflows
- changelog
- release docs
- `.factory/` contract files

The audit output includes:

- summary counts
- findings grouped by bucket
- evidence for each finding
- suggested objective
- suggested workflow
- candidate objectives ranked by priority

Audit is the planning eval surface.
It lets Factory test whether planning chooses the right next objective before any agent edits files.

## Prompt Compilation

For a non-built-in workflow, Factory builds a prompt from:

- repository checkout
- `.factory/AGENTS.md`, when present
- `.factory/STANDARDS.md`, when present
- `.factory/JOURNAL.md`, when present
- selected `.factory/OBJECTIVES/<objective>.md`, when an objective is provided
- selected `.factory/WORKFLOWS/<workflow>.md`
- runtime mode

The agent receives complete engineering context and should not need to guess the process.

## Objectives

Objectives are repo-owned work orders.

They answer:

- what outcome is wanted now
- why this matters
- what is in scope
- what is done
- which workflow to use
- when to stop for human review

Factory compiles objectives into agent goals.

```text
workflow = repeatable process
objective = current desired outcome
goal = runtime prompt sent to the coding agent
```

Objective example:

```md
# Objective: CI

## Goal

Make pull requests run build and tests in CI.

## Workflow

Use `.factory/WORKFLOWS/ci.md`.

## Done

- one draft PR is opened
- CI config is added or improved
- local checks are run where possible

## Stop Rules

- Do not add secrets.
- Do not merge the pull request.
- Stop if the build command is unclear.
```

## Built-In Hello Workflow

`hello` is a no-edit smoke workflow.
It proves clone, prompt dispatch, logging, and run recording.
It must not edit files, create branches, run tests, open issues, or open PRs.

## Run State

Factory stores local state under `.factory-state` by default.

```text
.factory-state/
  repos/
  worktrees/
  logs/
  runs/
  locks/
```

`repos/` contains Factory-owned clones.
These are internal runner state, not human working copies.
Factory may fetch, checkout, and update these clones while commands run.
Commands that touch one managed clone must hold that repo lock.
Execute-mode runs use per-run worktrees under `worktrees/`.

Run record fields:

- run id
- repo name
- repo path
- workflow name
- workflow source
- runtime mode
- agent
- status
- started at
- finished at
- log path
- blocker, if blocked
- error, if failed

Statuses:

- `queued`
- `running`
- `success`
- `blocked`
- `failed`
- `cancelled`

## Safety

- Factory must not merge PRs unless a repository workflow and policy explicitly allow it.
- Factory must not push directly to default branches.
- Factory must not run two write-capable workflows in one repo at the same time.
- Factory must stop when a workflow needs human input.
- Factory must record enough evidence to explain what happened.

## Next Steps

- Add verification mode.
- Add journal appends.
- Add GitHub issue and PR context loading.
- Add daemon schedules.
- Add more agent adapters.
