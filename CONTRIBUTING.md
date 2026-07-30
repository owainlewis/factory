# Contributing to Factory

Thanks for helping improve Factory. Bug reports, design feedback, documentation
fixes, and focused code changes are all welcome.

## Before you start

- Search the existing issues before opening a new one.
- Use a feature request to discuss substantial behavior or architecture changes
  before investing in an implementation.
- Keep pull requests focused on one problem. Unrelated cleanup is easier to
  review separately.
- Do not include secrets, credentials, private repository data, or sensitive
  ticket content in issues, logs, fixtures, or pull requests.

Security vulnerabilities should be reported privately as described in
[SECURITY.md](SECURITY.md), not through a public issue.

## Development setup

Factory V1 requires a current stable Rust toolchain. Factory V2 requires Go
1.24 or newer. The V2 web UI requires Node.js 22 for development and is
compiled into `factory-server` for production. Some integration tests also
exercise local `git` and GitHub CLI behavior.

```sh
git clone https://github.com/owainlewis/factory.git
cd factory
cargo build --locked
go build ./...
./scripts/build-v2-ui.sh
./scripts/build-v2.sh
```

To exercise Factory against GitHub, install and authenticate the GitHub CLI.
Most unit and integration tests do not require live GitHub access.

## Making a change

1. Create a branch from `main`.
2. Add or update tests for behavior changes.
3. Update documentation when commands, configuration, or operational behavior
   changes.
4. Run the project checks:

```sh
test -z "$(gofmt -l cmd internal migrations web/*.go)"
go vet ./...
go test ./...
cd web
npm run lint
npm run typecheck
npm test
npm run build
npm run test:browser
cd ..
cargo fmt --all --check
cargo clippy --locked --all-targets -- -D warnings
cargo test --locked --all-targets
```

The browser suite builds and starts a real `factory-server`, creates its
real `factory-worker`, creates two temporary Git repositories, executes work
through a deterministic fake Codex command, and saves desktop and narrow
screenshots under `web/test-results/screenshots/`. Install its Chromium runtime
once with `cd web && npx playwright install chromium`.

Run `./scripts/test-run-v2-local.sh` to check that the combined launcher refuses
to report an unhealthy worker as ready. Run `./scripts/test-build-v2.sh` to
prove the normal operator build does not invoke Node or npm.

## Pull requests

Explain the problem and the chosen solution, call out security or compatibility
risks, and include the commands or manual steps used to verify the change. CI
must pass before merge. Maintainers may ask for a smaller scope or additional
evidence when a change affects trust boundaries, credentials, workspaces, or
durable task state.

By participating in this project, you agree to follow the
[Code of Conduct](CODE_OF_CONDUCT.md). Contributions are licensed under the
project's [MIT License](LICENSE).
