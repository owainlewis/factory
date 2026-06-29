# Factory

Factory is a local runtime for autonomous software engineering.

Factory exists to raise the quality bar of every software project.
It helps produce code, docs, tests, CI, releases, and maintenance work at a level that was not practical when humans had to remember and execute every step by hand.

Every serious repo needs the same basic engineering memory:
what good looks like, how work should run, what needs doing now, what happened last time, and what a human must review.

Factory puts that memory in the repo under `.factory/`, then runs coding agents against it.

The goal is not to automate typing code.
The goal is to keep projects moving:
docs stay true, CI keeps working, releases become repeatable, issues get triaged, standards are enforced, and humans stay in control.

Factory is not a task dump.
Factory is not a policy wiki.
Factory is not a hosted service yet.
Factory does not merge PRs.

## Factory Standard

Factory gives every repo a senior engineer memory.

The project defines a default standard for professional software projects:
identity, usability, build, testing, CI, code quality, docs, release, security, operations, governance, and agent readiness.

The buckets are generic.
The answers are language-specific.
The final rules live in each target repo.

## Current V1

The first working version proves this spine:

```text
config -> clone or fetch repo -> build prompt -> run Claude Code -> save log -> save run record
```

It supports:

- `factory repos`
- `factory workflows <repo>`
- `factory run <repo> hello`
- `factory run <repo> <workflow> --mode plan`
- `factory run <repo> <workflow> --mode execute`
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

List repos:

```sh
go run ./cmd/factory repos
```

Run the no-edit smoke workflow:

```sh
go run ./cmd/factory run cortex hello
```

Plan a repo-owned workflow:

```sh
go run ./cmd/factory run cortex standards-check --mode plan
```

Execute a repo-owned workflow:

```sh
go run ./cmd/factory run cortex standards-check --mode execute
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

Each target repo should own its standards, workflows, objectives, and journal:

```text
.factory/
  AGENTS.md
  STANDARDS.md
  WORKFLOWS/
    bug-fix.md
    issue-triage.md
    docs-update.md
    dependency-update.md
    release.md
    review-pr.md
  OBJECTIVES/
    2026-06-29-release-readiness.md
  JOURNAL.md
```

Factory owns orchestration.
The target repo owns intent.

`.factory/STANDARDS.md` says what good looks like.
`.factory/WORKFLOWS/` says how repeatable work should run.
`.factory/OBJECTIVES/` says what outcome is wanted now.
`.factory/JOURNAL.md` says what happened before.

Factory compiles repo-owned objectives into agent goals at runtime.

Factory should not store target repo standards, objectives, journals, or runnable project workflows here.
Those belong in each target repo.

## Standard Factory Labels

Factory labels are standard across repos:

- `factory-ready`: an agent may work this issue now.
- `factory-triage`: the issue needs clarification, acceptance criteria, or scope shaping.
- `factory-needs-human`: the issue needs a human decision before implementation.
- `factory-blocked`: the issue cannot move until a named blocker is resolved.

## Docs

- [PRD](docs/prd.md)
- [The Factory Standard](docs/factory-standard.md)
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
