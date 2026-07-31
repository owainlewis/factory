# Worker contract

A Factory worker is one stable identity, one runtime, a capacity limit, and a
repository allowlist. It can run on a developer machine, VM, or Unix container.
Windows is not supported.

## Configuration

```toml
server = "http://127.0.0.1:7337"
name = "local-codex"
runtime = "codex"
max_concurrent = 1
data_directory = "workers/local-codex"

[repositories.factory]
path = "/absolute/path/to/factory"
```

`runtime` is `codex` or `claude-code`. A worker never switches runtime per task.
Run two workers when you want to send the same task to both agents.

Relative data directories and repository paths resolve from the directory
containing the worker TOML file. Repository paths must resolve to real, non-bare
Git repositories with an `origin`.

## Identity and registration

The first start creates a protected `worker-id` file in the worker data
directory. The worker reuses that ID on every restart. The local API does not
authenticate workers, so the ID is identity, not a credential.

Registration advertises:

- display name and runtime;
- maximum concurrent attempts;
- worker version;
- repository keys, normalized remote identities, and retained counts.

The server returns repository IDs used for task assignment. Heartbeats refresh
health and current capacity. A worker is offline when its heartbeat expires.

Print the configured identity without starting the worker:

```sh
~/.factory/bin/factory-worker identity \
  -config ~/.factory/worker.toml
```

## Claiming

Workers poll the loopback API for compatible work. A claim succeeds only when:

- the task targets that worker;
- the repository is in its current advertisement;
- worker capacity is available;
- the repository has fewer than ten retained worktrees, active attempts, and
  terminal attempts awaiting a local disposition on that worker.

An attempt has a lease. The worker renews it while the agent process is alive.
If the worker disappears, the control plane marks the attempt lost after the
lease expires.

## Attempt lifecycle

The worker prepares a task as follows:

1. validate the repository and remote identity;
2. read the base commit from the configured checkout;
3. create an owned worktree below its data directory;
4. create a `factory/<task-prefix>-<attempt-prefix>` branch;
5. write a protected, durable attempt manifest;
6. launch the selected runtime in the worktree;
7. stream bounded lifecycle and output events;
8. report the result and outcome;
9. inspect final Git state locally to decide whether cleanup is safe.

Codex is launched non-interactively with structured result output. Claude Code
is launched non-interactively with JSON output. The worker normalizes both into
the same control-plane result contract.

Runtime output and API event payloads are bounded. Oversized output is truncated
or summarized so one agent cannot grow a request without limit.

## Cancellation and shutdown

Cancellation is cooperative but enforced:

1. the control plane sets the execution's cancellation request flag;
2. the worker observes the state while renewing the lease;
3. it terminates the runtime process group;
4. it reports the attempt as cancelled.

On SIGINT or SIGTERM the worker stops claiming, terminates active runtimes,
reports their outcomes when possible, and exits.

## Worktree cleanup

Successful clean work can be removed only after the worker proves it is safe.
The worker validates:

- the protected manifest and its recorded worker identity;
- exact worker, task, attempt, repository, path, and branch identity;
- that the path is a direct child of its worktree root;
- the matching Git worktree registration;
- a clean working tree;
- publication of every new commit before automatic cleanup.

Dirty work, uncommitted changes, unpushed commits, and uncertain publication are
retained. The Worker view reports the path, reason, and cleanup command. Its
cleanup preview reports the branch and Git status.

Preview manual cleanup without changing the worktree:

```sh
factory-worker cleanup ATTEMPT_ID --config ~/.factory/worker.toml
```

If the preview is correct, confirm removal:

```sh
factory-worker cleanup ATTEMPT_ID \
  --config ~/.factory/worker.toml \
  --confirm
```

Confirmed operator cleanup preserves the local branch but removes the worktree
with force, so uncommitted changes shown in the preview are lost. Cleanup never
deletes the configured checkout or another worker's path.

## Deployment model

The current control plane accepts loopback workers only. On one VM, run the
server and one or more workers as supervised Unix services.

Remote VM and Kubernetes fleets are a planned extension. They require transport
security and worker authentication before the loopback restriction can be
removed.
