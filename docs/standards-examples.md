# .factory/STANDARDS.md Examples

Use these examples as starting points for target repos.
Each target repo should own and edit its own `.factory/STANDARDS.md`.

## General Repo

```md
# .factory/STANDARDS.md

These standards define what healthy means for this repo.
Agents must use this file when reviewing the repo, opening issues, or preparing PRs.

## Required

### License

- The repo must have a `LICENSE` file.
- The license must match package metadata and README claims.
- Agents must not change the license without human approval.

### Documentation

- `README.md` must explain what the project does.
- `README.md` must include install, usage, test, and development commands.
- Docs must match current code behavior.
- Public claims must be backed by code, tests, releases, or linked issues.

### Tests

- Test coverage must stay above 85%.
- New behavior must include tests.
- Bug fixes must include regression tests.
- Tests must run locally with one documented command.

### CI

- CI must run on pull requests.
- CI must run tests, lint, formatting, and build checks.
- CI must fail if tests fail.
- CI must not require secrets for normal pull request checks.

### Code Quality

- Code must be formatted with the repo formatter.
- Lint warnings must be fixed or explicitly justified.
- Public APIs must be documented.
- Dead code must be removed.
- Errors must include enough context to debug.

### Security

- Secrets must not be committed.
- Dependencies with known high or critical vulnerabilities must be fixed or tracked.
- Auth, permissions, crypto, payments, and data deletion changes require human review.

### GitHub Issues

Every open issue must have one type label:

- `bug`
- `documentation`
- `enhancement`
- `quality`

Factory labels are standard:

- `factory-ready`: an agent may work this issue now.
- `factory-triage`: the issue needs clarification.
- `factory-needs-human`: the issue needs a human decision.
- `factory-blocked`: the issue has a named blocker.

An issue may use `factory-ready` only when:

- expected behavior is clear
- acceptance criteria are clear
- the work fits in one focused PR
- no human decision is needed
- no blocker is known

## Recommended

- `CONTRIBUTING.md` explains setup and PR expectations.
- `SECURITY.md` explains vulnerability reporting.
- Each important feature has docs or examples.
- Performance-sensitive paths have benchmarks or smoke tests.
- Dependencies are reviewed monthly.

## Human Review Required

Agents must stop and ask before:

- merging PRs
- changing licenses
- cutting releases
- changing product direction
- deleting features
- adding large dependencies
- changing pricing
- making public claims
- weakening safety rules

## Agent Actions

When an agent finds a failed standard, classify it as:

- `fix`: open the smallest safe PR
- `issue`: open or update a focused issue
- `blocked`: report the missing decision or permission

Agents should prefer PRs for mechanical fixes.
Agents should prefer issues for judgment calls.
Agents must not merge.
Agents must not push to the default branch.
```

## Go CLI

```md
# .factory/STANDARDS.md

These standards define repo health for this Go CLI.

## Required

- `go test ./...` must pass.
- `go vet ./...` must pass.
- Public commands must be documented in `README.md`.
- Command output used by scripts must stay stable or be called out in release notes.
- Errors must be actionable and include the command or file that failed.
- Long-running commands must handle cancellation.
- Local runtime state must be ignored by git.

## Coverage

- Core packages must maintain at least 85% test coverage.
- CLI argument parsing must have tests for valid and invalid input.
- File and git operations must have tests where practical.

## CI

- CI must run `go test ./...`.
- CI must run `go vet ./...`.
- CI must build the CLI.

## Human Review Required

- Changing config file shape.
- Changing command names or output format.
- Adding network calls.
- Adding persistent background behavior.
- Changing release or install flow.
```

## Rust CLI

```md
# .factory/STANDARDS.md

These standards define repo health for this Rust CLI.

## Required

- `cargo fmt --check` must pass.
- `cargo clippy -- -D warnings` must pass.
- `cargo test` must pass.
- `cargo build --release` must pass before releases.
- README must document install, run, test, and release commands.
- User-facing behavior must match README and docs.

## Tests

- New editor or command behavior must include focused tests.
- Bug fixes must include regression tests.
- Terminal-facing behavior must include manual smoke evidence when automated tests are not enough.

## CI

- CI must run on pull requests.
- CI must use stable Rust unless the repo documents a different toolchain.
- CI must not require secrets for normal checks.

## Human Review Required

- Changing keybindings.
- Changing editor behavior not clearly requested by an issue.
- Adding large dependencies.
- Changing supported platforms.
- Changing release workflows.
```

## Documentation Repo

```md
# .factory/STANDARDS.md

These standards define repo health for this documentation repo.

## Required

- README must explain the repo purpose.
- Docs must be accurate, current, and source-backed.
- Links must work or be intentionally archived.
- Public claims must cite proof or be removed.
- Generated docs must name their source.

## Review Rules

- Agents may fix typos, stale links, formatting, and small factual drift.
- Agents must open issues for unclear claims, product direction, pricing, or public positioning.
- Agents must not invent roadmap promises, metrics, or customer claims.

## CI

- Link checks should run when practical.
- Markdown formatting should be documented.

## Human Review Required

- Public claims.
- Product strategy.
- Pricing.
- Legal or license text.
- Removing large sections of content.
```
