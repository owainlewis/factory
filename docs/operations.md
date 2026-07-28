# Operate Factory v1

Factory is always watching, not always spending tokens. `factory run` polls one
configured GitHub source and starts a worker only when a configured status,
label, or schedule trigger matches.

## Normal operation

Start from anywhere inside the configured repository:

```sh
factory validate
factory run
```

Startup validates the configured execution backend before claiming work.
Lifecycle events report polls, claims, worker delegation, safe Codex progress,
and terminal outcomes. Progress distinguishes work such as reasoning, commands,
file changes, web searches, and subtask coordination. Factory deliberately does
not print prompts, reasoning content, command arguments, or raw tool output.
Use the supported inspection commands instead of reading SQLite directly:

```sh
factory tasks [--json]
factory runs [WORKFLOW] [--json]
factory inspect RUN_ID [--json]
```

Each ticket trigger is durable. Repeated polls and normal restarts do not create
another task while the issue remains in the same status or label entry. Leaving
and re-entering creates one new task-scoped sandbox. The workflow tells the
agent to find and continue an existing branch or pull request when appropriate.

Factory stores durable state and managed worktrees below
`~/.factory/<repository-hash>/`. Set `FACTORY_DATA_HOME` to override the
`~/.factory` root. When upgrading an installation that already has state below
the previous platform data directory, Factory refuses to select the new default
while the previous ledger remains and reports the `FACTORY_DATA_HOME` value that
continues using that state. Factory also refuses to start while the older global
`~/.factory/factory.sqlite3` ledger remains, preventing overlap with work owned
by an old Factory process regardless of the `FACTORY_DATA_HOME` setting. These
overlap guards run when `factory run` starts; inspection and cleanup commands
remain available.

## Fleet operation

Run more than one trusted primary checkout from an explicit fleet file:

```sh
factory run --fleet ~/.config/factory/fleet.toml --once
factory run --fleet ~/.config/factory/fleet.toml
```

Fleet configuration is read once at startup. Adding, removing, editing,
enabling, or disabling a repository has no effect on the running process.
Restart Factory to apply the changed file. There is no hot reload and no fleet
`version` field. While that supervisor process is live, fleet inspection and
mutation commands use its durable fleet and repository configuration startup
snapshot, even if either file has since been edited. After the process stops,
commands load the current files.

Each `repository.name` is a pinned GitHub `owner/name` identity. Factory
lowercases it for durable identity, verifies the canonical primary checkout and
its `origin`, and never substitutes another remote or checkout for existing
state. Relative repository paths resolve from the fleet file's parent
directory. A leading `~` and `$NAME` environment variables use the same
expansion rules as repository configuration. An unset variable is an error.

The initial supported envelope is 20 enabled repositories, three
source-triggered workflows per repository, and polling intervals of at least
60 seconds. `max_concurrent` limits the fleet. An optional repository
`max_concurrent` combines with its repository worker limit by taking the lower
value. Factory admits eligible repositories round-robin, runs at most two host
source queries concurrently, deterministically staggers polls, and shares a
GitHub rate-limit pause across repositories using the same credential.

Inspect a running or stopped fleet with:

```sh
factory status --fleet ~/.config/factory/fleet.toml
factory tasks --fleet ~/.config/factory/fleet.toml
factory tasks --fleet ~/.config/factory/fleet.toml --repository acme/payments
factory runs --fleet ~/.config/factory/fleet.toml --repository acme/payments
factory polls --fleet ~/.config/factory/fleet.toml
factory workspaces --fleet ~/.config/factory/fleet.toml
factory recovery --fleet ~/.config/factory/fleet.toml
```

All fleet rows and JSON objects include pinned repository identity. `status`
reports `loading`, `healthy`, `invalid_config`, `unavailable`, `backing_off`,
`rate_limited`, or `disabled`, plus the latest validation or runtime error,
consecutive failure count, and next retry time when one exists. Ordinary
transient failures retry after 5 seconds and double to a 15-minute cap.
Rate-limit responses use the server retry time, or 60 seconds when no usable
time is available. A locally valid repository remains `loading` until a
supervisor durably records successful runtime validation; inspection never
invents a healthy state for an unstarted fleet. `polls` reports the latest
durable results. Continuous
supervision records each staggered workflow query. A one-shot evaluation
replaces those rows with one repository aggregate named `all`; later continuous
workflow results replace that aggregate without mixing stale rows.
`workspaces` reports active retained, recovery, and cleanup-pending ownership;
add `--all` to include cleaned records.

Read-only task and run lists aggregate every configured repository unless
`--repository owner/name` filters them. A single `inspect RUN_ID` may omit the
selector only when that numeric ID exists in one configured repository. If the
same ID exists in more than one ledger, Factory rejects the request and names
the required selector.

Cancellation and cleanup are mutations, so fleet mode always requires the
pinned repository:

```sh
factory cancel --fleet ~/.config/factory/fleet.toml \
  --repository acme/payments RUN_ID
factory cleanup --fleet ~/.config/factory/fleet.toml \
  --repository acme/payments RUN_ID
factory cleanup --fleet ~/.config/factory/fleet.toml \
  --repository acme/payments RUN_ID --confirm
```

Factory resolves the selector before opening a ledger, verifies the run, task,
and workspace ownership, and refuses unknown or inconsistent ownership without
mutation. This is required because repository ledgers can both contain run
`42`.

Disabling a repository takes effect after restart. Ctrl-C and process shutdown
first stop new polling and claims, cancel active workers, wait for them to
finish durable finalization, and leave queued work durable. On the next startup
a disabled repository is opened only for non-destructive reconciliation:
interrupted work becomes bounded recoverable or queued work but stays dormant.
Disabled repositories do not poll, create schedules, claim tasks, launch normal
or recovery workers, or perform destructive cleanup. Their ledgers, history,
branches, and owned workspaces remain. Re-enable the entry and restart to resume
normal recovery, reconciliation, polling, and dispatch.

Removing an entry from the fleet file does not open, move, reset, clean, or
delete its data directory or workspaces. Restore the same pinned identity and
canonical checkout path to operate that state again. Automatic cloning,
checkout relocation, and state migration are not supported.

## Worker boundary

Worktree mode runs the host Codex CLI in a Factory-owned Git worktree. It is
fast and uses the host user credentials and process boundary. It is not a
security sandbox and should be used only for trusted local work.

Docker Sandbox mode gives every task a disposable microVM and private in-VM Git
clone. Its Factory-owned host source clone is read-only to the VM. The canonical
checkout, Factory database, host Docker daemon, host credentials, and unrelated
repositories are outside the VM boundary.

The worker has full privileges inside the microVM, including its own Docker
daemon, while Docker Sandboxes applies the hypervisor and network boundaries.
Codex and GitHub credentials are injected by a host proxy and their raw values
do not enter the VM. Factory records the sandbox name, template, `sbx` version,
and limits before creation. Before removal, Factory snapshots tracked and
untracked changes in the VM and fetches that commit into trusted host Git
metadata. If the handoff fails, Factory stops and retains the sandbox.

Docker Sandboxes blocks network access unless policy allows it, but allowed
services and Git remotes still create external effects. Treat all
issue, comment, attachment, and review text as untrusted input. Use a dedicated
GitHub identity and protected branches so the worker cannot merge or bypass
review.

## Prove an idle poll

When no configured trigger matches and no scheduled workflow is due,
capture the task list before and after one poll:

```sh
factory tasks --json
factory run --once
factory tasks --json
```

The two task listings should show zero new tasks. In Docker Sandbox mode, also
run `sbx ls --quiet` and confirm that no `factory-` sandbox was created. This
proves the empty poll persisted and launched nothing.

## Cancellation and recovery

Request cancellation with:

```sh
factory cancel RUN_ID
```

Ctrl-C stops new polling and claims, cancels active workers, records terminal
outcomes, and leaves queued work durable. Worktree mode supervises the host
process group. Docker Sandbox mode also reconciles durable sandbox ownership by
its Factory instance name before stopping or removing a VM. It captures
recovered evidence, then permits bounded recovery. Repeated failure remains
inspectable and never turns into an automatic merge.

## Workspace retention and cleanup

Successful clean ticket workspaces are removed when they made no code commits,
or after their current branch is pushed. Failed, cancelled, dirty, unpublished,
or incomplete ticket workspaces are retained for recovery, up to ten.

Preview a retained clone before removal:

```sh
factory cleanup RUN_ID
factory cleanup RUN_ID --confirm
```

Confirmed cleanup removes only the recorded managed worktree or standalone
clone. The remote branch and pull request remain. Proposal workspaces are
disposable and are removed at a terminal outcome.

## Reset durable state

Preview a fresh start before removing task history:

```sh
factory reset
factory reset --confirm
```

Reset includes the current repository ledger and the older global ledger when
it exists. Confirmation is refused while either ledger has queued or running
work, a live daemon lease, an unremoved managed container, or retained workspace
ownership. Stop Factory and use `factory cleanup RUN_ID --confirm` for retained
work before retrying.

Reset removes only SQLite state. Repository configuration, workflows, branches,
and worktree directories are preserved.

## Troubleshooting

- `factory init --check` reports missing repository assets without writing.
- `factory validate` reports invalid triggers, trusted users, host Codex
  availability in worktree mode, `sbx` and secret prerequisites in Docker
  Sandbox mode, and data-path permissions.
- `factory run --once` proves polling without launching a model or worker.
- `factory inspect RUN_ID` shows bounded task, workspace, optional sandbox,
  branch, pull-request, and error evidence.
- `factory status --fleet FLEET` reports each configured repository even when a
  peer is invalid or unavailable. Use its error, failure count, and retry time
  before changing configuration.
- An `invalid_config` repository is not retried until restart. `unavailable`,
  `backing_off`, and `rate_limited` repositories remain retryable and never
  become permanently disabled because of repeated transient failures.

Scheduled workflows use the same configured worker as ticket workflows.
