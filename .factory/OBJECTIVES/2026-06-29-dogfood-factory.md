# Objective: Dogfood Factory

## Goal

Make Factory comply with its own repo contract.

## Context

Factory should use the same `.factory/` model it asks target repos to use.
This gives the project a real standards file, real workflows, and a current objective for agent runs.

## Scope

- `.factory/AGENTS.md`
- `.factory/STANDARDS.md`
- `.factory/WORKFLOWS/standards-check.md`
- `.factory/OBJECTIVES/`
- `.factory/JOURNAL.md`
- README and docs alignment
- Go test and vet checks

## Done

- Factory has a repo-owned `.factory/` contract.
- Factory is listed in local `config.yaml`.
- `factory workflows factory` discovers repo-owned workflows.
- `go test ./...` passes.
- `go vet ./...` passes.
- any agent runtime blocker is recorded.

## Workflow

Use `.factory/WORKFLOWS/standards-check.md`.

## Stop Rules

- Do not merge pull requests.
- Do not push to `main`.
- Do not change safety rules without human review.
- Stop if product strategy decisions are required.
