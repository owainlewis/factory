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
- Node.js 22 and npm when changing the UI
- Codex CLI or Claude Code CLI for real worker tests

Build the Go binaries from the committed UI:

```sh
./scripts/build.sh
```

Rebuild the UI only when its source changes:

```sh
cd web
npm ci
cd ..
FACTORY_SKIP_INSTALL=1 ./scripts/build-ui.sh
```

## Checks

Run backend and launcher checks:

```sh
gofmt -w ./cmd ./internal ./web
go test ./...
go vet ./...
./scripts/test-build.sh
./scripts/test-run-local.sh
```

Run UI checks:

```sh
cd web
npm run typecheck
npm run lint
npm test
npm run test:browser
```

`web/dist` is committed because it is embedded in `factory-server`. If UI source
changes, rebuild it and commit the generated assets. An operator build must not
run Node or npm.

## Pull requests

- Keep changes focused.
- Include tests for changed behavior.
- Update architecture documents when boundaries or contracts change.
- Use Conventional Commit messages.
- Explain what was verified, including browser checks for UI work.
- Do not commit credentials, local worker configuration, databases, or retained
  worktrees.

By participating, you agree to follow the
[Code of Conduct](CODE_OF_CONDUCT.md). Contributions use the project's
[MIT License](LICENSE).
