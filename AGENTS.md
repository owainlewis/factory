# AGENTS.md

This repo defines how Code Factory operates.

Be concise and clear.
Use simple words.
Do not use em dashes.

## Purpose

Code Factory manages standards, goals, and recurring agent work for important GitHub repos.

Each active repo lives under `repos/<repo-name>/`.

## Rules

- Do not add a repo unless it has a clear goal.
- Do not create broad policy documents when a repo config will do.
- Do not make noisy PRs.
- Do not push directly to another repo's default branch.
- Do not auto-merge important changes.
- Do not invent claims, metrics, roadmap promises, or product details.
- Do not edit secrets, generated files, vendored files, dependency folders, or environment files.
- Keep every automation prompt specific enough that a fresh agent can run it.

## Required Repo Files

Every active repo folder must have:

- `config.yaml`
- `goal.md`
- at least one file in `automations/`

## Work Loop

Before improving a repo:

1. Read `standards/project-checklist.md`.
2. Read `standards/labels.yaml`.
3. Read the repo's `config.yaml`.
4. Read the repo's `goal.md`.
5. Read the relevant automation prompt.
6. Work only inside the allowed scope.
7. Prefer issues when the fix is unclear.
8. Prefer small PRs when the fix is clear.

## Human Review Required

Human review is required for:

- releases
- public claims
- pricing
- product strategy
- curated resource additions
- deleting features
- changing repo purpose
- changing licenses
- changing automation safety rules

