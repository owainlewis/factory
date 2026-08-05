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
