# Run the single-repository Factory v1

Factory watches one GitHub Project and reacts only when trusted work enters one
of two configured states. Triage runs from a read-only clone. Implementation
runs from a writable clone, pushes one stable issue branch, and leaves one pull
request for human review and merge.

## Requirements

Install Rust, Git, GitHub CLI, Docker, and Codex CLI on a Unix-like host. Docker
must be running. Authenticate the host GitHub CLI:

```sh
gh auth login
gh auth status
```

Install Factory from a clean checkout:

```sh
git clone https://github.com/owainlewis/factory.git
cd factory
cargo install --path .
```

## Initialize one repository

Run Factory inside the trusted repository it will manage:

```sh
factory init
```

This creates, only when missing:

```text
.factory/config.toml
.factory/Dockerfile
.factory/workflows/triage-ticket.md
.factory/workflows/implement-ready-ticket.md
```

It also creates the repository-specific Factory data directory outside the
checkout. Re-running the command preserves every existing file. Preview missing
resources without writing with `factory init --check`.

Edit `.factory/config.toml` with the GitHub Project owner and number, the exact
Status field values, and trusted issue authors. Status names are local policy,
so Jira-style or team-specific names are valid when mapped to all six semantic
states.

Review the two workflow prompts. They are the adaptive part of the factory.
Review `.factory/Dockerfile` and add the repository toolchain needed by tests
and builds, then build the configured image:

```sh
docker build --file .factory/Dockerfile --tag factory-codex:dev .
```

Create a dedicated writable Codex login for the worker:

```sh
mkdir -p "$HOME/.local/share/factory/codex"
CODEX_HOME="$HOME/.local/share/factory/codex" codex login
```

Set `worker.codex_auth` to that `auth.json` path. Export a dedicated GitHub
token through the environment named by `worker.github_token_env`:

```sh
export FACTORY_GITHUB_TOKEN='...'
```

Use a bot or narrowly scoped identity that can read and write the repository
and Project but cannot bypass protected-branch review. Factory does not make a
personal owner token safe.

## Validate and start

```sh
factory validate
factory workflows
factory run --once
factory daemon
```

Validation checks the repository, all configured Project states, trusted users,
Docker daemon, exact image, writable Codex auth, live worker GitHub token, and
writable Factory data path. `run --once` polls and records matching work but
does not claim tasks or launch containers. With no matching Project item,
Factory starts no container and invokes no model.

The continuous daemon reacts to two states:

1. `ready_for_spec` moves to `creating_spec`. The triage agent investigates the
   issue, improves its acceptance criteria, and either moves it to
   `ready_to_implement` or posts a precise blocker.
2. `ready_to_implement` moves to `implementing`. The implementation agent uses
   normal `gh` and `git`, tests the change, obtains independent review, pushes
   `factory/<issue-number>`, opens or updates one pull request, waits for CI,
   and moves the item to `ready_to_review`.

Humans review the specification and the pull request. Feedback is given through
the issue, review, Project state, and CI. Moving reviewed work back to
`ready_to_implement` creates one continuation run on the same branch and pull
request. Factory never merges or enables auto-merge.

## Observe a run

```sh
factory tasks
factory runs
factory inspect RUN_ID
```

JSON output is available with `--json`. A successful implementation handoff
records the issue, task, run, container, image, limits, clone, branch, pull
request, bounded logs, and result. Restart the daemon and wait through another
poll to confirm the task, branch, and pull-request counts remain unchanged.

See [operations.md](operations.md) for cancellation, recovery, cleanup, trust,
and Docker limitations.
