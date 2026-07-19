# Factory

Factory is a local-first daemon that turns trusted GitHub issues and scheduled
prompts into supervised Codex runs. Factory owns durable scheduling, task
claims, concurrency, process supervision, inspection, cancellation, and
recovery. Codex owns the adaptive development procedure and leaves software
pull requests for human merge.

Factory v1 supports Unix-like systems and uses the authenticated local `gh`
and Codex CLIs. It does not use model API keys.

## Quick start

1. Install Rust, `git`, `gh`, and Codex CLI.
2. Authenticate with `gh auth login` and `codex login`.
3. Clone Factory and install it globally with `cargo install --path . --locked`.
4. In a trusted target repository, run `factory init`.
5. Review and commit `.factory/workflows/implement-ready-ticket.md`.
6. Run `factory validate`, `factory workflows`, then `factory run`.

For local development, run the install command from the Factory repository:

```sh
cargo install --path . --locked
factory --help
```

Cargo normally installs the binary at `~/.cargo/bin/factory`. If your shell
cannot find it, add Cargo's binary directory to your zsh path:

```sh
echo 'export PATH="$HOME/.cargo/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc
```

Reinstall the development build after local code changes:

```sh
cargo install --path . --locked --force
```

`factory init --check` previews setup without writes. Use `--no-labels` for
offline local setup and `--update-workflow` only when you intend to replace a
customized implementation workflow with the bundled version.

See [`docs/local-v1.md`](docs/local-v1.md) for complete installation, setup,
operation, recovery, and acceptance instructions. The architecture and product
boundary are documented in [`docs/design.md`](docs/design.md).

## Development checks

Every pull request runs:

```sh
cargo fmt --all --check
cargo clippy --all-targets -- -D warnings
cargo test
```

Factory never merges software pull requests or enables automatic merge.
