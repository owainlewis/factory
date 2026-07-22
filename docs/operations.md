# Operate Factory v1

Factory is always watching, not always spending tokens. The daemon polls one
configured GitHub Project and starts a worker only for a trusted item in
`ready_for_spec` or `ready_to_implement`, or when a scheduled workflow is due.

## Normal operation

Start from anywhere inside the configured repository:

```sh
factory validate
factory run
```

Startup validates the configured execution backend before claiming work.
Lifecycle events report polls, claims, worker delegation, and terminal outcomes. Use
the supported inspection commands instead of reading SQLite directly:

```sh
factory tasks [--json]
factory runs [WORKFLOW] [--json]
factory inspect RUN_ID [--json]
```

Each Project transition is level-triggered and durable. Repeated polls and
normal restarts do not create another task for the same state generation. A
review-to-implementation transition creates one new generation and reuses the
title-independent `factory/<issue-number>` branch and linked pull request.

## Worker boundary

Worktree mode runs the host Codex CLI in a Factory-owned Git worktree. It is
fast and uses the host user credentials and process boundary. It is not a
security sandbox and should be used only for trusted local work.

Docker mode gives every daemon task a disposable container and standalone HTTPS
clone. Triage and scheduled workflows mount their clone read-only.
Implementation and label-triggered delivery workflows mount it read-write. The
canonical checkout, Factory database, Docker socket, host credentials, and
unrelated repositories are not mounted.

The worker runs as the clone owner with a read-only root filesystem, dropped
Linux capabilities, bounded CPU, memory and processes, and a temporary `/tmp`.
Only the dedicated Codex auth file and task clone are writable. Factory records
the exact image ID and limits before starting the container and captures bounded
stdout and stderr. Containers are removed after their terminal evidence is
durable.

Docker is not a VM. It shares the host kernel, permits outbound network access,
and receives long-lived credentials for Codex and GitHub. Run only trusted
authors' issues. Treat all issue, comment, attachment, and review text as
untrusted input. Use a dedicated GitHub identity and protected branches so the
worker cannot merge or bypass review.

## Prove an idle poll

When no configured Project item is ready and no scheduled workflow is due,
capture the task list before and after one poll:

```sh
factory tasks --json
factory run --once
factory tasks --json
```

The two task listings should show zero new tasks. In Docker mode, also run
`docker ps --all --filter label=dev.factory.managed=true` and confirm that no
Factory container was created. This proves the empty poll persisted and
launched nothing.

Before first repository-local startup, Factory reads the old
`~/.factory/factory.sqlite3` database without modifying it. If that database
contains queued or running work for this repository, startup stops with
instructions to stop the old daemon and finish or cancel the work. Terminal
legacy history is not imported.

## Cancellation and recovery

Request cancellation with:

```sh
factory cancel RUN_ID
```

Ctrl-C stops new polling and claims, cancels active workers, records terminal
outcomes, and leaves queued work durable. Worktree mode supervises the host
process group. Docker mode also reconciles durable container ownership before
stopping or removing only containers labelled for that exact Factory instance.
It captures recovered evidence, then permits bounded recovery. Repeated failure
remains inspectable and never turns into an automatic merge.

## Workspace retention and cleanup

Successful clean implementation workspaces are removed only after the branch
is pushed and a pull-request handoff is recorded. Failed, cancelled, dirty,
unpublished, or incomplete implementation workspaces are retained for
recovery, up to ten.

Preview a retained clone before removal:

```sh
factory cleanup RUN_ID
factory cleanup RUN_ID --confirm
```

Confirmed cleanup removes only the recorded managed worktree or standalone
clone. The remote branch and pull request remain. Proposal workspaces are
disposable and are removed at a terminal outcome.

## Troubleshooting

- `factory init --check` reports missing repository assets without writing.
- `factory validate` reports invalid state mappings, trusted users, host Codex
  availability in worktree mode, Docker prerequisites in Docker mode, and
  data-path permissions.
- `factory run --once` proves polling without launching a model or worker.
- `factory inspect RUN_ID` shows bounded task, workspace, optional container,
  branch, pull-request, and error evidence.
- If old global Factory work is active, stop the old daemon and finish or cancel
  it. Repository-local Factory never mutates or imports the legacy database.

Scheduled workflows remain outside the two-state Project pipeline but use the
same configured worktree or Docker execution mode as ticket workflows.
