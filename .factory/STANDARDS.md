# STANDARDS.md

These standards define repo health for Factory.

## Purpose

Factory must clearly explain its mission:
raise the quality bar of software projects by running coding agents against repo-owned standards, workflows, objectives, and journals.

## Repository Contract

- Factory repo contract files live under `.factory/`.
- Target repos should put their Factory contract under `.factory/`.
- Factory must prefer `.factory/` files and may fall back to old root-level files for compatibility.
- Factory examples and templates may live in this repo.
- Runnable target repo workflows should not live in this repo except as examples.

## Go Code

- Keep package boundaries clear.
- `internal/config` owns registry loading.
- `internal/gitrepo` owns clone and fetch behavior.
- `internal/prompt` owns prompt compilation.
- `internal/workflows` owns workflow discovery.
- `internal/agent` owns coding agent adapters.
- `internal/runner` wires packages together and records run state.

## Testing

- Code changes must include focused tests.
- `go test ./...` must pass.
- `go vet ./...` must pass.
- Tests should cover safety defaults, fallback behavior, and prompt compilation when those paths change.

## Documentation

- README must describe Factory, not Code Factory.
- README must explain the current V1 honestly.
- Docs must distinguish standards, workflows, objectives, journals, and runtime goals.
- Public claims must be backed by code, docs, tests, issues, or pull requests.

## CI

- Pull requests should run Go tests.
- Pull requests should run `go vet ./...`.
- CI should not require secrets for normal pull request checks.

## Release

- Release process should be documented before any public release.
- Releases should use tags.
- Release notes should explain user-visible changes.
- Do not publish releases without human review.

## Safety

- Factory must not merge pull requests automatically.
- Factory must not push directly to default branches.
- Factory must stop when a workflow needs human input.
- Factory must record enough evidence to explain what happened.

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
