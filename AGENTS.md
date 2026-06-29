# AGENTS.md

This repo defines Factory.

Be concise and clear.
Use simple words.
Do not use em dashes.

## Purpose

Factory is a local runtime for autonomous engineering.
Factory exists to raise the quality bar of every software project.

Target repos own their own `.factory/` contract:

- `.factory/STANDARDS.md`: what good looks like
- `.factory/WORKFLOWS/`: repeatable agent playbooks
- `.factory/OBJECTIVES/`: what we want done now
- `.factory/JOURNAL.md`: what happened before
- `.factory/AGENTS.md`: repo-specific agent instructions

Factory owns local execution, logs, state, and agent adapters.

It is not a task dump.
It is not a policy wiki.
It is not a big rules engine.

## Product Model

- `factory audit <repo>` is read-only and prints a Markdown health report.
- Audit is the planning eval surface.
- Audit finds gaps and candidate objectives.
- Planning should choose one high-leverage objective.
- Workflows describe how to do a class of work.
- Objectives describe the desired outcome for the current run.
- Runtime goals are prompts compiled from repo context.
- `factory init` should bootstrap `.factory/` defaults in a new repo. See issue #16.

## Repo Contract

Factory should prefer `.factory/` files:

```text
.factory/
  AGENTS.md
  STANDARDS.md
  WORKFLOWS/
  OBJECTIVES/
  JOURNAL.md
```

Root-level `STANDARDS.md`, `WORKFLOWS/`, `OBJECTIVES/`, and `JOURNAL.md` are legacy fallback paths only.

## Rules

- Keep config small.
- Keep target repo standards, workflows, objectives, and journals under `.factory/`.
- Work on one repo at a time.
- Work through one workflow per task run.
- Do not merge PRs.
- Do not push to default branches.
- Do not do broad cleanup.
- Do not invent claims, metrics, roadmap promises, or product details.
- Stop if the issue is unclear.

## Before Editing Runner Behavior

1. Read `config.yaml`.
2. Read `docs/prd.md`.
3. Read `docs/factory-runner/spec.md`.
4. Read `.factory/STANDARDS.md`, when present.
5. Keep target repo standards, workflows, objectives, and journals out of this repo unless they are examples, templates, or Factory dogfooding files.

## Current Core Commands

```sh
factory audit <repo>
factory repos
factory workflows <repo>
factory run <repo> [workflow] [--mode plan|execute]
factory runs
```

`audit` must stay read-only.
`run --mode plan` must not edit files.
`run --mode execute` may create branches and draft PRs, but must not merge.

## Implementation Notes

- `internal/config` owns local registry loading.
- `internal/gitrepo` owns clone and fetch behavior.
- `internal/workflows` owns workflow discovery.
- `internal/prompt` owns prompt compilation.
- `internal/audit` owns deterministic repo health checks and Markdown output.
- `internal/agent` owns coding agent adapters.
- `internal/runner` wires packages together and writes run state.

Before changing code, prefer adding focused tests around the package that owns the behavior.

## Human Review Required

Human review is required for:

- merging
- releases
- public claims
- pricing
- product strategy
- deleting features
- changing repo purpose
- changing licenses
- changing safety rules
