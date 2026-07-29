# Factory V2 local worker

`factory-worker` is the local Unix execution process for Factory V2. It
registers with the loopback control plane, advertises configured Git
repositories, claims work assigned to its stable worker ID, and runs Codex in a
V2-owned worktree.

The worker is separate from Factory V1. It does not import the control-plane
implementation, open SQLite, inspect V1 state, or use V1 worktree paths.

## Prerequisites

- A Unix host.
- Git available on `PATH`.
- Codex available on `PATH` and authenticated. Verify with `codex login status`.
- A running `factory-server` on a loopback address.
- One or more local non-bare Git repositories with an `origin` remote.

Build the binary from the repository root:

```sh
go build -o factory-worker ./cmd/factory-worker
```

## Configuration

The worker reads one TOML file. Pass it with `--config`. Without that flag it
uses `FACTORY_V2_WORKER_CONFIG`, then
`$FACTORY_V2_DATA_HOME/worker.toml`, then
`~/.factory-v2/worker.toml`.

```toml
server = "http://127.0.0.1:7337"
name = "owains-mac"
max_concurrent = 1
data_directory = "/Users/owainlewis/.factory-v2/workers/owains-mac"

[repositories.factory]
path = "/Users/owainlewis/Code/github/owainlewis/factory"

[repositories.website]
path = "/Users/owainlewis/Code/github/owainlewis/website"
```

`server` must be plain HTTP on a loopback IP or `localhost`. The worker refuses
public hosts. `max_concurrent` defaults to one and accepts one through four.
Repository keys are stable operator-chosen names. Repository and data paths may
be relative to the configuration file.

At startup the worker canonicalizes every repository path and reads its
normalized `origin` identity. Missing paths, duplicate paths, duplicate
remotes, non-Git directories, missing origins, and unknown TOML fields stop
startup. A repository key already registered with another remote is rejected
by the control plane.

The data directory is owner-only and held with an exclusive lock for the
worker lifetime. The first start writes an owner-only `worker-id`; later starts
reuse it. A second process cannot use the same data directory. The worker also
refuses a data directory below a Factory V1 `factory.sqlite3` state root.

## Run

Start the server first, then:

```sh
./factory-worker --config /path/to/worker.toml
```

The worker checks Git, the Codex version, and Codex login status. Failed health
checks register the worker as unhealthy and prevent claims. Checks repeat every
thirty seconds, so restoring Git or Codex recovers without restarting the
worker.

Healthy workers heartbeat their capacity and repositories, reserve a local
slot, and poll for assigned work. Claim retries reuse the same request ID and
lease secret, so a lost response cannot start a duplicate attempt.

## Execution and failure behavior

Each claim is matched back to the configured repository key and normalized
remote identity. The worker creates:

```text
<data_directory>/worktrees/<attempt-id>
```

on a local branch named:

```text
factory-v2/<task-id-prefix>-<attempt-id-prefix>
```

The Codex prompt contains a fixed Factory safety preamble, task title,
description, and repository identity. The prompt is sent over standard input
and is not placed in command arguments or normal logs.

A separate supervisor owns a process-group anchor. The control plane accepts
the attempt start before the supervisor launches Codex. The supervisor then
stops Codex and every descendant on:

- task cancellation;
- task timeout;
- worker shutdown or parent-process loss;
- lease loss or loss of the server.

The worker renews leases every ten seconds. The supervisor begins termination
early enough to kill a process that ignores the graceful signal before the
thirty-second lease expires. Cancellation and shutdown use a five-second
graceful period before killing the complete group.

Codex JSON output is sent as ordered bounded events. One event, one batch, total
event history, result, and error sizes follow the limits in the V2 architecture.
Reaching an event limit does not stop lease renewal, cancellation, or terminal
completion.

## Worktree retention

After the control plane accepts a successful result, the worker removes a
worktree only when all live-attempt checks pass:

- its canonical path is a direct child of the V2 worktree root;
- Git lists exactly that path, branch, and commit;
- the worktree is clean;
- its current commit is the original base or is reachable from a remote ref.

Cleanup uses `git worktree remove` without force and never deletes the branch.
Any mismatch or cleanup error retains the worktree. Failed, cancelled, dirty,
and unpublished worktrees are also retained for inspection.

Durable attempt manifests, restart reconciliation, retained-worktree capacity,
and preview or confirmed cleanup commands are intentionally delivered by issue
#132. Until then, the worker does not scan, repair, or delete worktrees left by
an earlier worker process.
