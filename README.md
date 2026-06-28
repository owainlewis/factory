# Code Factory

Code Factory is a private control plane for making Owain's GitHub repos better every day.

It exists to run a steady loop across repos:
audit, detect problems, open issues, fix safe work, improve docs, prepare releases, and report what changed.

The goal is not busy work.
The goal is to make every important repo clearer, healthier, easier to use, and closer to its purpose.

## Status

Draft operating system.

This repo defines the standards, repo registry, policies, and playbooks first.
Automation code comes after the loop is clear.

## Core Loop

```text
inventory -> audit -> score -> plan -> branch -> fix -> test -> PR -> report
```

The first version should run in audit mode.
It should produce reports and high-quality issues before it starts opening many PRs.

## Principles

- Improve repo quality, not commit count.
- Make small, reviewable changes.
- Keep every repo aligned with its real purpose.
- Prefer facts from the repo over guesses.
- Never invent roadmap claims, metrics, or product details.
- Never push directly to protected branches.
- Treat docs, tests, release notes, and examples as part of the product.
- Human review stays required for strategy, public claims, releases, and curated recommendations.

## Repo Shape

- `repos.yaml`: source of truth for monitored repos and allowed work.
- `docs/`: system design, quality standards, and operating loop.
- `policies/`: rules agents must follow for different repo types.
- `playbooks/`: repeatable tasks agents can run.
- `reports/`: generated summaries from factory runs.
- `runs/`: run logs, artifacts, and audit output.

## Start Here

1. Read [docs/operating-principles.md](docs/operating-principles.md).
2. Read [docs/repo-quality-standard.md](docs/repo-quality-standard.md).
3. Read [docs/factory-loop.md](docs/factory-loop.md).
4. Update [repos.yaml](repos.yaml) when adding or changing monitored repos.

## Initial Scope

The first repos to bring under control are:

- `owainlewis/awesome-artificial-intelligence`
- `owainlewis/youtube-tutorials`
- `owainlewis/blueprint`
- `owainlewis/neo`
- `owainlewis/cortex`
- `owainlewis/push`
- `owainlewis/website`
- `owainlewis/business-os`

## Non-Goals

- Do not turn every repo into a product.
- Do not rewrite code for style alone.
- Do not auto-merge meaningful changes.
- Do not replace human taste in curated repos.
- Do not create public claims that cannot be proven from the repo.

