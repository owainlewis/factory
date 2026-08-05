# Contributing

Factory uses Go for the control plane and workers, and React with TypeScript for
the embedded UI.

Before starting a change, search existing issues, keep the scope focused, and
do not include credentials, private repository data, or sensitive task content.
Report vulnerabilities through [SECURITY.md](SECURITY.md), not a public issue.

## Setup

Install:

- Go 1.25.12 or newer on the 1.25 release line, or Go 1.26.5 or newer
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
just vuln
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

When adding or changing an HTTP route, update the route definition and its
request, response, pagination, and error metadata together. Regenerate the
[HTTP API contract](docs/api.md), then verify it:

```sh
just api-contract
just api-contract-check
```

The router and generated inventory share one definition table. Go tests also
snapshot every typed JSON request and response reference. The separate
`docs/api-compat.json` baseline makes CI reject changes to existing routes or
field inventories. After a compatible route or schema addition, ratchet the
baseline. This refuses removals and mutations:

```sh
just api-contract-ratchet
```

If a reviewed change is intentionally breaking, use the explicit command and
call it out in the pull request:

```sh
just api-contract-accept-breaking
```

## Dependency updates

Dependabot checks Go and npm dependencies each week and GitHub Actions each
month. Minor and patch updates are grouped to reduce pull-request noise; major
updates remain separate so their compatibility impact is visible. Review the
upstream release notes and keep `go.mod`, `go.sum`, `web/package.json`, and
`web/package-lock.json` in sync with the change.

Workflow actions must use a full commit SHA followed by a version comment, for
example `owner/action@0123456789abcdef0123456789abcdef01234567 # v1.2.3`.
Dependabot updates both the SHA and comment. Do not replace a pinned SHA with a
mutable tag.

Before merging an npm update, run:

```sh
cd web
npm audit --omit=dev
```

The weekly dependency-audit workflow runs the same production audit. Treat a
failure as a security maintenance task; confirm exploitability and update or
mitigate the dependency rather than suppressing the audit without evidence.

## Pull requests

- Keep changes focused.
- Include tests for changed behavior.
- Update `ARCHITECTURE.md` when current boundaries or contracts change.
- Use Conventional Commit messages.
- Explain what was verified, including browser checks for UI work.
- Do not commit credentials, local worker configuration, databases, or retained
  worktrees.

Changes to `main` must go through a pull request and pass the required `check`
status. Maintainers should follow the [maintainer guide](docs/maintainers.md)
when changing repository policy or recovering from a broken required check.

By participating, you agree to follow the
[Code of Conduct](CODE_OF_CONDUCT.md). Contributions use the project's
[MIT License](LICENSE).
