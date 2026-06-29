# AGENTS.md

This repository defines Factory.

Factory is a local runtime for autonomous software engineering.
It clones repos, compiles repo-owned context, runs coding agents, records outcomes, and keeps humans in control.

## Agent Rules

- Keep changes small and easy to review.
- Prefer simple Go code and clear package boundaries.
- Follow existing package ownership.
- Do not push to `main`.
- Do not merge pull requests.
- Do not change safety rules without human review.
- Do not invent product claims, metrics, pricing, or roadmap promises.
- Stop when the requested issue or objective is unclear.

## Important Context

- `config.yaml` is local runner registry state.
- Target repo standards, workflows, objectives, and journals belong under `.factory/` in the target repo.
- Factory may include examples and templates, but it should not become a policy wiki.

## Before Editing Runner Behavior

1. Read `config.yaml`.
2. Read `docs/prd.md`.
3. Read `docs/factory-runner/spec.md`.
4. Read `.factory/STANDARDS.md`.
5. Identify the owning package before editing.
6. Add focused tests for changed behavior.

Keep target repo standards, workflows, objectives, and journals out of this repo unless they are examples, templates, or Factory dogfooding files.

## Package Ownership

- `internal/config` owns local registry loading.
- `internal/gitrepo` owns clone and fetch behavior.
- `internal/workflows` owns workflow discovery.
- `internal/prompt` owns prompt compilation.
- `internal/audit` owns deterministic repo health checks and Markdown output.
- `internal/agent` owns coding agent adapters.
- `internal/runner` wires packages together and records run state.

## Verification

Run these checks before opening a pull request when code changes:

```sh
go test ./...
go vet ./...
```
