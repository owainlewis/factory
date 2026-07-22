# Factory

Factory is a local-first daemon that turns trusted GitHub Project items into
supervised Codex runs. Factory owns polling, durable claims, concurrency,
workspace isolation, inspection, cancellation, cleanup, and recovery. Codex
owns the adaptive workflow and uses `gh` and `git` directly. It leaves pull
requests for human review and merge.

Factory v1 manages one trusted repository on a Unix-like host. Worktree mode is
the fast default for trusted local development. Docker mode uses standalone
clones, a dedicated GitHub token, and a dedicated Codex login for reproducible,
resource-bounded execution. Factory does not use model API keys.

## Quick start

1. Install Rust, `git`, `gh`, and Codex CLI.
2. Authenticate the host with `gh auth login`.
3. Clone Factory and run `cargo install --path . --locked`.
4. In a trusted target repository, run `factory init`.
5. Configure the GitHub Project, trusted users, and status names in
   `.factory/config.toml`.
6. Review the generated triage and implementation workflows.
7. Run `factory validate`, `factory workflows`, and `factory daemon`.

For Docker execution, initialize with `factory init --execution-mode docker`,
adapt the generated `.factory/Dockerfile`, and build the worker image:

```sh
docker build --file .factory/Dockerfile --tag factory-codex:dev .
```

Create the dedicated Codex login used by the worker:

```sh
mkdir -p "$HOME/.local/share/factory/codex"
CODEX_HOME="$HOME/.local/share/factory/codex" codex login
```

Set `worker.codex_auth` to `~/.local/share/factory/codex/auth.json` in the
config.

Export `FACTORY_GITHUB_TOKEN` for a dedicated GitHub identity before starting
Factory in Docker mode.

Run the install command from the Factory repository, then verify the command:

```sh
cargo install --path . --locked
factory --help
```

Cargo normally installs the binary at `~/.cargo/bin/factory`. If zsh cannot
find it, add Cargo's binary directory to your path:

```sh
echo 'export PATH="$HOME/.cargo/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc
```

Reinstall after local development changes:

```sh
cargo install --path . --locked --force
```

`factory init --check` previews setup without writes. Initialization creates
`.factory/config.toml`, external machine state and workspace storage, and the
two workflows. Docker mode also creates `.factory/Dockerfile`. Existing files
are never overwritten. It does not alter the GitHub Project or start an agent.

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
runs triage. An item in the ready-to-implement state runs implementation and
advances to review. Only items from configured trusted users can be claimed.
All six status names are configurable.

`execution_mode = "worktree"` runs every daemon task with the host Codex CLI in
a Factory-owned Git worktree. It is fast, but it is not a security boundary and
should be used only for trusted local work. `execution_mode = "docker"` runs
every daemon task in a disposable container backed by a standalone clone. The
container has a read-only root, no added Linux capabilities, bounded CPU,
memory and processes, and no Docker socket or canonical repository mount.

See [`docs/single-repository-v1/design.md`](docs/single-repository-v1/design.md)
for the setup, state machine, worker boundary, recovery model, and acceptance
checks. See [`docs/local-v1.md`](docs/local-v1.md) for the runnable setup and
[`docs/operations.md`](docs/operations.md) for day-two operation.

## Development checks

Every pull request runs:

```sh
cargo fmt --all --check
cargo clippy --all-targets -- -D warnings
cargo test
```

Factory never merges software pull requests or enables automatic merge.
