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
3. Clone Factory and run `cargo install --path .`.
4. In a trusted target repository, run `factory init`.
5. Create a workflow with `factory workflow create`.
6. Review and commit the new file under `.factory/workflows/`.
7. Set `[github].trusted_approvers` to trusted GitHub logins.
8. Run `factory validate`, `factory workflows`, then `factory daemon`.
9. Authorize a complete issue with `factory approve ISSUE_NUMBER`.

`factory init --check` previews setup without writes. Initialization creates
`.factory/config.toml`, external machine state and worktree storage, and the
repository's workflow directory. It does not install an
opinionated workflow or create GitHub labels.

Create a scheduled pull-request triage workflow without opening an editor:

```sh
factory workflow create triage-pull-requests \
  --schedule "*/30 * * * *" \
  --timezone Europe/London \
  --timeout 1h \
  --prompt "Review open pull requests with no labels. Process at most five per run. Read each diff, checks, and existing reviews; add appropriate repository labels and leave a review only for actionable findings. Never merge or close a pull request."
```

Use `--prompt-file PATH` for longer policies, or `--prompt-file -` to read the
prompt from standard input. Label-triggered workflows create their missing
trigger label explicitly; scheduled workflows do not mutate labels during
creation.

`factory daemon` runs until Ctrl-C. `factory run --once` evaluates schedules
and polls once, persists eligible tasks, and exits without launching Codex.
If no schedule or issue matches, Factory launches no agent and uses no model
tokens.

The `factory:ready` label is only a wake signal. Adding it directly never
authorizes work. `factory approve ISSUE_NUMBER` records the exact issue title,
body, workflow revision, trusted GitHub user ID, and new label event that the
daemon must verify again immediately before Codex starts.

See [`docs/local-v1.md`](docs/local-v1.md) for the current implementation's
installation, setup, operation, recovery, and acceptance instructions. The
repo-local architecture and product boundary are documented in
[`docs/single-repository-v1/design.md`](docs/single-repository-v1/design.md).

## Development checks

Every pull request runs:

```sh
cargo fmt --all --check
cargo clippy --all-targets -- -D warnings
cargo test
```

Factory never merges software pull requests or enables automatic merge.
