# Run the single-repository Factory v1

Factory watches one GitHub Project and reacts only when trusted work enters one
of two configured states. It can run in fast local worktrees or isolated Docker
clones. Implementation pushes one stable issue branch and leaves one pull
request for human review and merge.

## Requirements

Install Rust, Git, GitHub CLI, and Codex CLI on a Unix-like host. Install and
start Docker as well when using Docker mode. Authenticate the host GitHub CLI:

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
.factory/workflows/triage-ticket.md
.factory/workflows/implement-ready-ticket.md
```

It also creates the repository-specific Factory data directory outside the
checkout. Re-running the command preserves every existing file. Preview missing
resources without writing with `factory init --check`.

This selects `execution_mode = "worktree"`. Worktrees are quick for trusted
local development but are not a security boundary. To select Docker and create
the worker Dockerfile instead, run:

```sh
factory init --execution-mode docker
```

Edit `.factory/config.toml` with the GitHub Project owner and number, the exact
Status field values, and trusted issue authors. Status names are local policy,
so Jira-style or team-specific names are valid when mapped to all six semantic
states.

Review the two workflow prompts. They are the adaptive part of the factory.
In Docker mode, review `.factory/Dockerfile` and add the repository toolchain
needed by tests and builds, then build the configured image:

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
factory run
```

Validation checks the repository, all configured Project states, trusted users,
and writable Factory data path. Worktree mode validates the host Codex CLI.
Docker mode validates the Docker daemon, exact image, authenticated Codex
session inside that image, and live worker GitHub token. `run --once` polls and
records matching work but does not claim tasks or launch workers. With no
matching Project item, Factory invokes no model.

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

## Project 16 self-hosting evidence

Factory v1 was exercised against the public
[Factory Project](https://github.com/users/owainlewis/projects/16) on 22 July
2026 from a clean standalone clone of commit `f15a919`. `factory init` created
the missing repository config without changing the checked-in workflows or
Dockerfile. The image built as `factory-codex:dev` with digest
`sha256:af5b2c31afc1e06d809a96d8c675bd7470532a4f1e30b0de118cab046fd3a52e`.
`factory validate` resolved all six Project states, the trusted user, live
worker token, Docker daemon and image, and a dedicated Codex login verified
inside the hardened worker container.

Before adding ready work, `factory run --once` saw seven repository issues and
created zero tasks. `factory tasks --json` returned `[]`, and Docker listed no
container for the Factory instance. This is the no-work, no-model path.

The implementation proof used [issue #54](https://github.com/owainlewis/factory/issues/54).
Factory moved it from Ready To Implement to Implementing, created task 1 and
run 1, cloned commit `eeb3858`, checked out `factory/54`, and launched container
`1bb173253992` with a read-only root, 4 CPUs, 8 GB memory, and 512 PIDs. The
agent used `gh` and `git`, changed only `docs/operations.md`, recorded review
and verification limitations, pushed commit `5733d47`, and opened
[PR #55](https://github.com/owainlewis/factory/pull/55). GitHub CI passed in
1m37s. The agent marked the PR ready, moved the issue to Reviewing, posted the
[handoff comment](https://github.com/owainlewis/factory/issues/54#issuecomment-5040193067),
and left merge and auto-merge untouched. Run 1 succeeded in 463,624 ms, stored
the PR link and exact image evidence, then removed its container and clean
clone.

After stopping and restarting the daemon, the ledger still contained one
succeeded task and one succeeded run for issue #54. GitHub still contained one
open `factory/54` branch and one PR, and Docker contained no Factory container.
The restart created no duplicate work.

The triage proof used [issue #56](https://github.com/owainlewis/factory/issues/56).
Factory moved it from Ready For Spec to Creating Spec, created task 2 and run 2,
and launched the same image against a separate read-only `triage-2` clone. The
agent inspected the startup path and rewrote the vague issue into a bounded
goal, scope, acceptance criteria, verification plan, and explicit exclusions,
then moved it to Ready To Implement. Run 2 succeeded in 112,535 ms and produced
no branch or pull request.

Two limitations were recorded. The proof used the operator's broad GitHub token,
so it demonstrated the warning and human workflow but not least-privilege bot
enforcement. Also, the daemon observed issue #56 become ready immediately after
triage and queued its implementation generation before shutdown. Shutdown
cancelled authorization before any implementation container launched. This is
expected pipeline behavior, and it shows why a demo operator should stop or
park a triaged ticket before the next poll when only the triage phase is being
demonstrated.
