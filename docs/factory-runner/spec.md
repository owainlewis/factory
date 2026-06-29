# Factory Runner Spec

## What

Factory is a local runtime for autonomous engineering.
It manages local checkouts, compiles repository context, dispatches coding agents, records outcomes, and repeats.

Factory is not the engineer.
Factory runs engineers.

## Repository Contract

Every managed repository owns its engineering process:

```text
AGENTS.md
STANDARDS.md
WORKFLOWS/
JOURNAL.md
```

`AGENTS.md` describes coding-agent instructions.
`STANDARDS.md` defines what good looks like.
`WORKFLOWS/*.md` defines engineering playbooks such as bug fixes, triage, docs updates, dependency updates, releases, and PR review.
`JOURNAL.md` is append-only handover between runs.

Factory must not duplicate target repo standards, workflows, journals, checks, issue labels, or product purpose in this repo.

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
factory repos
factory workflows <repo>
factory run <repo> [workflow] [--mode plan|execute]
factory runs
```

Planned commands:

```sh
factory plan <repo>
factory execute <repo>
factory triage <repo>
factory daemon
```

`plan` is the default mode.
It asks the agent to inspect and report without editing files.

`execute` is write-capable.
It allows the agent to make a workflow-scoped change, create a non-default branch, commit it, push it, and open a draft pull request when the workflow asks for code changes.

## Prompt Compilation

For a non-built-in workflow, Factory builds a prompt from:

- repository checkout
- `AGENTS.md`, when present
- `STANDARDS.md`, when present
- `JOURNAL.md`, when present
- selected `WORKFLOWS/<workflow>.md`
- runtime mode

The agent receives complete engineering context and should not need to guess the process.

## Built-In Hello Workflow

`hello` is a no-edit smoke workflow.
It proves clone, prompt dispatch, logging, and run recording.
It must not edit files, create branches, run tests, open issues, or open PRs.

## Run State

Factory stores local state under `.factory-state` by default.

```text
.factory-state/
  repos/
  logs/
  runs/
  locks/
```

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

- Add repo locks.
- Add worktrees per write run.
- Add verification mode.
- Add journal appends.
- Add GitHub issue and PR context loading.
- Add daemon schedules.
- Add more agent adapters.
