# AGENTS.md

This repo defines Code Factory.

Be concise and clear.
Use simple words.
Do not use em dashes.

## Purpose

Code Factory stores reusable agent loops for important GitHub repos.

It is not a task dump.
It is not a policy wiki.
It is not a big rules engine.

## Current Scope

The first target project is `owainlewis/cortex`.

The first loop is:

```text
Task: issue to PR
```

## Rules

- Keep config small.
- Put work instructions in markdown loops.
- Work on one repo at a time.
- Work on one issue per task run.
- Do not merge PRs.
- Do not push to default branches.
- Do not do broad cleanup.
- Do not invent claims, metrics, roadmap promises, or product details.
- Stop if the issue is unclear.

## Before Editing A Loop

1. Read `config.yaml`.
2. Read the relevant file in `projects/`.
3. Read the loop prompt.
4. Keep the loop executable by a fresh Codex thread.

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
