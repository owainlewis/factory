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

Factory requires a current stable Rust toolchain. Some integration tests also
exercise local `git` and GitHub CLI behavior.

```sh
git clone https://github.com/owainlewis/factory.git
cd factory
cargo build --locked
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
cargo fmt --all --check
cargo clippy --locked --all-targets -- -D warnings
cargo test --locked --all-targets
```

## Pull requests

Explain the problem and the chosen solution, call out security or compatibility
risks, and include the commands or manual steps used to verify the change. CI
must pass before merge. Maintainers may ask for a smaller scope or additional
evidence when a change affects trust boundaries, credentials, workspaces, or
durable task state.

By participating in this project, you agree to follow the
[Code of Conduct](CODE_OF_CONDUCT.md). Contributions are licensed under the
project's [MIT License](LICENSE).
