# AGENTS.md

This repo defines Code Factory.

Be concise and clear.
Use simple words.
Do not use em dashes.

## Purpose

Code Factory is a local runtime for autonomous engineering.
Target repos own their own standards, workflows, objectives, and journals.
Factory owns local execution, logs, state, and agent adapters.

It is not a task dump.
It is not a policy wiki.
It is not a big rules engine.

## Rules

- Keep config small.
- Put repo work instructions in the target repo under `.factory/WORKFLOWS/`.
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
4. Keep target repo standards, workflows, objectives, and journals out of this repo unless they are examples or templates.

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
