# Factory

Factory is a local-first daemon that turns trusted GitHub Project items into
supervised Codex runs. Factory owns polling, durable claims, concurrency,
Docker isolation, inspection, cancellation, cleanup, and recovery. Codex owns
the adaptive workflow inside a standalone clone and uses `gh` and `git`
directly. It leaves pull requests for human review and merge.

Factory v1 manages one trusted repository on a Unix-like host. It uses Docker,
the local `gh` CLI for polling, a dedicated GitHub token for workers, and a
dedicated Codex login. It does not use model API keys.

## Quick start

1. Install Rust, Docker, `git`, `gh`, and Codex CLI.
2. Authenticate the host with `gh auth login`.
3. Clone Factory and run `cargo install --path .`.
4. In a trusted target repository, run `factory init`.
5. Configure the GitHub Project, trusted users, and status names in
   `.factory/config.toml`.
6. Add the triage and implementation workflows under `.factory/workflows/`
   and a repository-specific `.factory/Dockerfile` with the real toolchain.
7. Build the repository worker image:

   ```sh
   docker build --file .factory/Dockerfile --tag factory-codex:dev .
   ```

8. Create the dedicated Codex login used by the worker:

   ```sh
   mkdir -p "$HOME/.local/share/factory/codex"
   CODEX_HOME="$HOME/.local/share/factory/codex" codex login
   ```

   Set `worker.codex_auth` to
   `~/.local/share/factory/codex/auth.json` in the config.

9. Export `FACTORY_GITHUB_TOKEN` for a dedicated GitHub identity, then run
   `factory validate`, `factory workflows`, and `factory daemon`.

`factory init --check` previews setup without writes. Initialization creates
`.factory/config.toml`, external machine state and workspace storage, and the
repository's workflow directory. It does not alter the GitHub Project or
install opinionated workflows.

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

The v1 loop has two reactions. An item in the configured ready-for-spec state
runs triage in a read-only clone. An item in the ready-to-implement state runs
implementation in a writable clone and advances to review. Only items from
configured trusted users can be claimed. All six status names are configurable.

Each Project-triggered run gets one disposable Docker container and one
standalone clone. Docker is the isolation boundary, so sandboxed runs do not
use Git worktrees. The container has a read-only root, no added Linux
capabilities, bounded CPU, memory and processes, and no Docker socket or
canonical repository mount. Scheduled workflows remain a separate host-run
feature in v1.

See [`docs/single-repository-v1/design.md`](docs/single-repository-v1/design.md)
for the setup, state machine, worker boundary, recovery model, and acceptance
checks.

## Development checks

Every pull request runs:

```sh
cargo fmt --all --check
cargo clippy --all-targets -- -D warnings
cargo test
```

Factory never merges software pull requests or enables automatic merge.
