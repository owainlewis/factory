# AGENTS.md

This repo coordinates automated work across Owain's GitHub repos.

Be concise and clear.
Use simple words.
Do not use em dashes.

## Purpose

Code Factory exists to make important repos healthier every day.

It should find real problems, create useful issues, open small PRs, improve docs, strengthen tests, and keep repo state aligned with reality.

It is not a dumping ground for vague plans.

## Hard Rules

- Never push directly to another repo's `main` or `master` branch.
- Never auto-merge a PR unless a repo policy explicitly allows it.
- Never invent product details, metrics, URLs, roadmap promises, or results.
- Never rewrite a repo just to make activity.
- Never create more than one concern per PR.
- Never edit secrets, credentials, private keys, environment files, generated files, vendored code, or dependency directories.
- Never browse archives or git history unless the current task requires it.
- Prefer issues and reports when the correct fix is unclear.
- Prefer small PRs when the fix is clear and low risk.

## Operating Principle

Improve quality through a calm loop:

```text
observe -> diagnose -> prioritize -> change -> verify -> report
```

Doing less is better than doing noisy work.

## Repo Work Rules

Before changing any monitored repo:

1. Read its README.
2. Read its AGENTS.md if present.
3. Read its package or project files.
4. Check git status.
5. Identify the repo policy in `repos.yaml`.
6. Make one scoped change.
7. Run the smallest meaningful verification.
8. Report what changed and what remains.

## Human Review Required

Human review is required for:

- releases
- pricing
- product strategy
- public claims
- curated resource additions
- deleting files or removing features
- changing repo purpose
- changing licenses
- broad refactors

