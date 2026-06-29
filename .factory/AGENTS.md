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

## Verification

Run these checks before opening a pull request when code changes:

```sh
go test ./...
go vet ./...
```
