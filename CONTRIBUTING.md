# Contributing

Factory uses Go for the control plane and workers, and React with TypeScript for
the embedded UI.

Before starting a change, search existing issues, keep the scope focused, and
do not include credentials, private repository data, or sensitive task content.
Report vulnerabilities through [SECURITY.md](SECURITY.md), not a public issue.

## Setup

Install:

- Go 1.25 or newer
- Git
- just
- Node.js 22 and npm when changing the UI
- Codex CLI or Claude Code CLI for real worker tests

Build the Go binaries from the committed UI:

```sh
just build
```

Rebuild the UI only when its source changes:

```sh
just ui-install
just ui-build 0
```

## Checks

Run backend, tooling, and launcher checks:

```sh
just format-check
just vet
just boundary
just test
just test-tooling
just test-launcher
```

Run UI checks:

```sh
just ui-check
just test-browser
```

`web/dist` is committed because it is embedded in `factory-server`. If UI source
changes, rebuild it and commit the generated assets. An operator build must not
run Node or npm.

The release inventory treats every non-development package in
`web/package-lock.json` as shipped code. When that set changes, update the
matching license mapping and committed license text under `third_party/npm`,
then run `just test-release`. The check fails if a production package is
missing or if the mapping contains stale entries.

## Pull requests

- Keep changes focused.
- Include tests for changed behavior.
- Update `ARCHITECTURE.md` when current boundaries or contracts change.
- Use Conventional Commit messages.
- Explain what was verified, including browser checks for UI work.
- Do not commit credentials, local worker configuration, databases, or retained
  worktrees.

By participating, you agree to follow the
[Code of Conduct](CODE_OF_CONDUCT.md). Contributions use the project's
[MIT License](LICENSE).
