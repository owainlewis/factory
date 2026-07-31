# Factory architecture

> **Status:** Current implementation
>
> **Verification basis:** Working tree based on commit `295badb`

## 1. Executive summary

Factory is a local control plane for running coding agents in Git repositories.
It separates durable coordination from agent execution:

- `factory-server` stores work, assigns it, exposes the HTTP API, and serves the
  embedded browser UI.
- `factory-worker` has one stable identity and one agent runtime. It advertises
  allowed repositories, polls for work, and runs attempts in isolated Git
  worktrees.
- Codex or Claude Code performs the repository work as a child process of the
  worker.

The current task contract is a title, prompt, assigned worker, repository, and
timeout. The scheduler is part of the control plane. The deployment is limited
to a trusted user and loopback HTTP on one host.

## 2. System context

```text
Operator
   |
   | browser or JSON over loopback HTTP
   v
factory-server
   |-- embedded React UI
   |-- control-plane API and scheduler
   `-- SQLite
           ^
           | registration, polling, leases, events, completion
           |
factory-worker (one identity and one runtime)
   |-- repository allowlist
   |-- attempt manifests and owned Git worktrees
   `-- Codex CLI or Claude Code CLI
```

Workers initiate every connection. The server does not connect to workers, and
the system does not use WebSockets.

## 3. Architectural invariants

1. One worker identity has one immutable runtime, either `codex` or
   `claude-code`.
2. Tasks are assigned to a specific worker and one repository currently
   advertised by that worker.
3. Only a healthy, recently registered worker with free capacity can claim its
   queued work.
4. A lease token owns one active attempt. Active operations require a matching,
   unexpired lease. A terminal completion request with the original token may be
   replayed; the stored outcome wins.
5. Agent processes start with a worker-owned worktree below that worker's data
   directory as their working directory.
6. Cleanup fails closed when the manifest, repository, branch, path, process,
   or Git worktree identity cannot be proved.
7. Existing worktrees with unpublished, dirty, failed, cancelled, lost, or
   uncertain work are retained for inspection.
8. The control plane and worker reject non-loopback server addresses because
   remote authentication and transport security are not implemented.
9. Operator builds embed the committed `web/dist` assets and do not require
   Node.js.

## 4. Components and dependencies

### Control plane

`cmd/factory-server` starts the Go HTTP server. It:

- validates and binds a loopback address, `127.0.0.1:7337` by default;
- opens the SQLite store and applies embedded migrations;
- sweeps expired leases at startup and every five seconds;
- mounts the API and embedded UI on one origin;
- writes structured JSON logs;
- allows ten seconds for HTTP shutdown.

`internal/controlplane` owns the API, validation, state transitions, scheduling,
metrics, pagination, and persistence. Claim selection is transactional and FIFO
by execution creation time for the requesting worker.

SQLite runs with foreign keys, WAL journaling, a five-second busy timeout, and
at most eight open connections. The default database is
`~/.factory/server/factory.sqlite3`.

### Worker

`cmd/factory-worker` starts one worker manager, prints a worker identity, runs
manual cleanup, or starts the internal attempt supervisor. The manager:

- resolves and locks its data directory;
- creates or loads a durable worker ID;
- resolves repository paths and normalizes their `origin` identities;
- checks Git and runtime health;
- registers every ten seconds and polls for claims every two seconds with
  jitter;
- renews active leases every ten seconds;
- runs up to the configured capacity, from one to four attempts;
- reconciles manifests, worktrees, and process groups after restart.

The supervisor is a subprocess of `factory-worker`. It owns the runtime process
group and enforces cancellation, timeout, lease loss, and parent-process loss.
Unix process-group behavior is required, so Windows workers are unsupported.

### Agent runtimes

The worker launches the configured runtime non-interactively:

- Codex uses `codex exec` with JSON events and a file for the last message.
- Claude Code uses `claude --print` with streaming JSON and bypassed permission
  prompts.

Both runtimes receive the same generated prompt and produce the same bounded
event and completion contract.

### Browser UI

`web/src` is a React and TypeScript application with Overview, Work, Workers,
Task detail, and Delegate task views. It polls the same-origin API.

`web/dist` is generated, committed, and embedded by `web/embed.go`. The server
uses an SPA fallback for application routes, immutable caching for versioned
assets, and restrictive browser security headers.

Node.js is a contributor dependency only when UI source changes.

## 5. Critical flows

### Startup and registration

1. The server validates its data root, opens SQLite, applies migrations, and
   marks already expired attempts as `lost`.
2. The worker validates its TOML, data directory, runtime, and repositories.
3. The worker reconciles durable attempt manifests before accepting new work.
4. A healthy worker registers its identity, runtime, capacity, repositories,
   retained worktrees, and disposed attempt IDs.
5. A worker is shown as offline when its last registration is more than 30
   seconds old.

### Task creation and claiming

1. An operator submits a unique `request_key`, title, description, worker ID,
   repository ID, and optional timeout.
2. The control plane creates one task and one queued execution. Reusing the
   request key returns the original task.
3. The assigned worker polls its claim endpoint with a unique request ID and
   lease token.
4. The control plane verifies worker health, recency, capacity, runtime,
   repository advertisement, and repository retention capacity.
5. It selects the oldest eligible queued execution, creates a preparing
   attempt, stores only a digest of the lease token, and returns the claim.
6. An empty response is idempotent for five minutes. A successful response is
   idempotent while its attempt remains active and its lease remains valid.

### Attempt execution

1. The worker validates the claim against its identity and repository allowlist.
2. It reads the configured checkout's current `HEAD`.
3. It creates a branch named
   `factory/<task-prefix>-<attempt-prefix>` and an owned worktree.
4. It writes a protected attempt manifest before starting the runtime.
5. The internal supervisor starts, then the worker transitions the attempt to
   `running`.
6. Runtime output is sent as ordered, idempotent event batches.
7. The worker renews the 30-second lease while the supervisor is active.
8. Completion records a bounded result, error, and outcome, and moves the
   execution to `succeeded`, `failed`, or `cancelled`.

### Cancellation and lease expiry

- Cancelling queued work moves its execution directly to `cancelled`.
- Cancelling preparing or running work sets `cancellation_requested`. The worker
  observes the flag on its next lease renewal, stops the runtime process group,
  and reports a cancelled attempt.
- An expired preparing or running lease moves the attempt to `lost` and its
  execution to `failed`.
- Retrying is an explicit operator action available only for failed or cancelled
  executions. It returns the existing execution to `queued` and increments its
  retry count.

### Completion and cleanup

The worker automatically removes a successful worktree only after proving it is
clean and either unchanged from its base commit or that every new commit is
published. It may also delete the managed local branch when that branch is safe
and unused.

Other outcomes and uncertain publication are retained. Manual cleanup first
prints the manifest, path, branch, Git status, and reason. A separate
`--confirm` run removes the worktree but preserves the local branch.
The Worker view reports retained paths and ready-to-copy cleanup commands.

At startup, the worker stops process groups recorded as active, compares each
manifest with server state and Git state, resumes only provably safe cleanup,
and becomes unhealthy when identity cannot be established.

## 6. Interfaces and data

### Operator API

```text
GET    /healthz
GET    /api/v1/metrics/summary?window=24h|7d|30d|all
GET    /api/v1/workers
GET    /api/v1/workers/{worker_id}
GET    /api/v1/tasks?limit={1..200}&cursor={cursor}
POST   /api/v1/tasks
GET    /api/v1/tasks/{task_id}
DELETE /api/v1/tasks/{task_id}
POST   /api/v1/tasks/{task_id}/cancel
POST   /api/v1/executions/{execution_id}/retry
GET    /api/v1/attempts/{attempt_id}/events?after={sequence}&limit={1..500}
```

Task deletion is limited to terminal history whose worktree disposition has
been acknowledged. It refuses to delete history for a retained worktree.

### Worker API

```text
PUT    /api/v1/workers/{worker_id}
POST   /api/v1/workers/{worker_id}/claims
GET    /api/v1/attempts/{attempt_id}
POST   /api/v1/attempts/{attempt_id}/start
PUT    /api/v1/attempts/{attempt_id}/heartbeat
POST   /api/v1/attempts/{attempt_id}/events
POST   /api/v1/attempts/{attempt_id}/complete
```

Mutations require JSON and reject cross-origin browser requests. API requests
are bounded by operation-specific byte limits.

### Persistent model

```text
Worker 1 --- * WorkerRepository * --- 1 Repository
Task   1 --- 1 Execution       1 --- * Attempt 1 --- * AttemptEvent
```

- A task stores the operator request and repository.
- An execution stores its assigned worker, required runtime, state,
  cancellation flag, and explicit retry count.
- An attempt stores one claim, lease, process identity, result, and outcome.
- Attempt events store ordered runtime and lifecycle payloads.
- Claim requests make empty and successful claims idempotent.

Task lists use an opaque cursor ordered by creation time and ID. Event lists use
the last sequence number. Prompts remain in task detail but are omitted from the
task list.

### Limits

| Contract | Limit |
| --- | ---: |
| Worker concurrency | 1 to 4 |
| Task description | 64 KiB |
| Default task timeout | 2 hours |
| Maximum task timeout | 8 hours |
| Lease duration | 30 seconds |
| Event batch | 100 events and 256 KiB |
| Single event | 64 KiB |
| Events stored per attempt | 10 MiB |
| Completion result | 256 KiB |
| Completion error | 64 KiB |
| Retained and reserved worktrees per worker repository | 10 |
| Task page | 50 by default, 200 maximum |
| Event page | 100 by default, 500 maximum |

### Files and configuration

```text
~/.factory/
  bin/
    factory-server
    factory-worker
  server/
    factory.sqlite3
    factory.sqlite3.v2-control-plane
  worker.toml
  workers/<worker>/
    worker-id
    worker.lock
    attempts/
    disposed-attempts.json
    worktrees/
```

The marker filename and contents retain compatibility with the earlier Go
preview storage format. They do not represent a second application.

`FACTORY_DATA_HOME` changes the default root. `FACTORY_WORKER_CONFIG` selects a
worker TOML. `FACTORY_BUILD_DIR`, `FACTORY_LISTEN`, `FACTORY_SKIP_BUILD`, and
`FACTORY_WORKER_READY_SECONDS` configure local scripts. Earlier
`FACTORY_V2_*` names remain migration aliases in code and scripts, but are not
operator-facing configuration.

Relative worker data and repository paths are resolved from the directory that
contains the worker TOML. Repositories must resolve to real Git checkouts with
an `origin` remote.

## 7. Security and trust boundaries

The current trust boundary is one trusted user on one host:

- the server binds only to loopback and validates request host resolution;
- there is no login, authorization, worker credential, TLS, or tenant boundary;
- worker IDs identify local state but are not secrets;
- the agent process has the worker OS user's permissions and can access anything
  available to that user;
- the repository allowlist controls assignment and worktree creation, but it is
  not a filesystem sandbox;
- lease tokens are random, sent over local HTTP, and stored as SHA-256 digests;
- browser mutations must be same-origin and use JSON;
- worker data directories, identity files, manifests, and database marker files
  use restrictive permissions and reject unsafe symlinks where identity matters;
- cleanup proves ownership and Git identity before deleting a worktree.

Factory must not be exposed directly to a network. Remote workers require
authenticated and encrypted transport, scoped authorization, audit records,
and a reviewed tenant model.

## 8. Failure, capacity, and operations

- Loss of the worker or lease fails the execution. Recovery is an explicit
  retry, not automatic rescheduling.
- Loss of the server stops agent process groups after the lease renewal
  deadline.
- Worker shutdown stops claiming, terminates active process groups, and reports
  terminal state when the server remains available.
- Server shutdown drains HTTP requests, stops the lease sweeper, checkpoints
  the SQLite WAL, and closes the database.
- A worker data directory is locked to one running worker identity.
- Worktree reconciliation and cleanup prefer retention over destructive action.
- Repository capacity counts active work, retained work, and completed attempts
  whose local disposition has not been acknowledged. This prevents unbounded
  worktree growth.
- Task list responses are bounded by cursor pagination, but persistent task,
  prompt, result, and error history grows until an operator deletes terminal
  tasks. Factory has no age-based automatic retention job.
- Event storage is bounded per attempt. Results, errors, prompts, and request
  bodies also have byte limits.

Summary metrics are derived only from retained control-plane facts: execution
counts and outcomes, queue and running counts, success and retry rates, median
cycle time, and worker totals. Factory does not infer merged pull requests or
triaged tickets from agent text.

## 9. Verification

The implementation is covered by:

- control-plane store, HTTP, state-machine, migration, pagination, deletion,
  metrics, and lease tests in `internal/controlplane`;
- worker identity, configuration, registration, process supervision, runtime
  output, cancellation, lease loss, restart reconciliation, and cleanup tests in
  `internal/worker`;
- server and worker command tests in `cmd`;
- embedded asset tests in `web`;
- React unit, polling, and browser tests in `web/src`;
- build and local-launch checks in `scripts/test-build.sh` and
  `scripts/test-run-local.sh`.

The contributor check set is documented in [CONTRIBUTING.md](CONTRIBUTING.md).

## 10. Known limitations

- Only local loopback deployments are supported.
- Windows workers are unsupported.
- There is no authentication, authorization, tenant isolation, or remote worker
  transport.
- A task has one execution assigned to one worker. Fan-out and cross-worker
  rescheduling are not implemented.
- Scheduling is pull-based FIFO per worker. There are no priorities, cron
  triggers, provider polling, or automatic retries.
- Workflows, scheduled automations, GitHub ingest, and a unified CLI are
  proposed but not implemented.
- Metrics do not confirm external outcomes such as merged pull requests or
  closed tickets.
- Terminal history requires explicit deletion. There is no time-based retention
  policy.

Proposed behavior is documented separately in the
[workflow](docs/workflows/design.md),
[GitHub ingest](docs/github-ingest/design.md), and
[CLI](docs/cli/design.md) designs.

## 11. Source map

| Area | Source |
| --- | --- |
| Server process and defaults | `cmd/factory-server` |
| Worker process and commands | `cmd/factory-worker` |
| HTTP API and state machine | `internal/controlplane/http.go`, `state.go` |
| Persistence and metrics | `internal/controlplane/store.go`, `metrics.go` |
| Database schema | `migrations` |
| Shared contracts and limits | `internal/protocol` |
| Worker orchestration | `internal/worker/manager.go`, `registration.go`, `claiming.go`, `attempt_lifecycle.go` |
| Runtime supervision | `internal/worker/supervisor.go` |
| Git worktrees and cleanup | `internal/worker/git.go`, `reconcile.go`, `cleanup.go` |
| Durable worker state | `internal/worker/identity.go`, `manifest.go` |
| State path compatibility | `internal/statepath` |
| UI source and API client | `web/src` |
| Embedded UI serving | `web/embed.go`, `web/dist` |
| Local build and launch | `scripts/build.sh`, `scripts/run-local.sh` |
