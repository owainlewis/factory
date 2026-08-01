# Factory architecture

> **Status:** Current implementation
>
> **Verification basis:** Working tree based on commit `2d732ec`

## 1. Executive summary

Factory is a local control plane for running coding agents in Git repositories.
It separates durable coordination from agent execution:

- `factory-server` stores work, assigns it, exposes the HTTP API, and serves the
  embedded browser UI.
- `factory-worker` has one stable identity and one agent runtime. It advertises
  runtime capacity and provider access, acquires centrally managed repositories
  on demand, and runs attempts in isolated Git worktrees.
- `factory-poller` lists configured issue queues through provider CLIs and
  submits matching tickets as ordinary control-plane tasks.
- Codex or Claude Code performs the repository work as a child process of the
  worker.

The current task contract is a title, prompt, assigned worker, repository, and
timeout. Callers may name the assignment directly or ask the control-plane
scheduler to choose from cattle workers. The deployment is limited to a trusted
user and loopback HTTP on one host.

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
   |-- bounded on-demand repository cache
   |-- optional legacy static checkouts
   |-- attempt manifests and owned Git worktrees
   `-- Codex CLI or Claude Code CLI

factory-poller
   |-- GitHub through gh
   |-- command adapters for other provider CLIs
   `-- local dispatch ledger
           |
           `-- POST normal tasks to factory-server
```

Workers initiate every connection. The server does not connect to workers, and
the system does not use WebSockets.

## 3. Architectural invariants

1. One worker identity has one immutable runtime, either `codex` or
   `claude-code`.
2. Every task freezes one worker and one control-plane repository. Routed work
   may select a cattle worker before that repository exists in its local cache.
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
10. Polling is read-only. A queue and issue identity creates at most one task,
    including across poller restarts and lost HTTP responses.

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

### Issue poller

`cmd/factory-poller` tests GitHub matching without mutation with `-test-github`,
runs one submitting pass with `-once`, or polls continuously. Each configured
queue names a source, project, native status, required labels, prompt, and
timeout. Non-GitHub command queues also retain an explicit worker and
repository key for compatibility.

GitHub support is built in and invokes the authenticated `gh issue list`
command. A configured GitHub queue fails at startup with installation and
authentication guidance when `gh` is unavailable. Other source names invoke
one configured executable without a shell. Factory appends `--project`,
`--status`, and repeated `--label` arguments. The command returns the normalized
issue shape documented in [docs/poller.md](docs/poller.md). This keeps provider
credentials and API clients outside Factory.

For GitHub, the poller submits the repository remote and a GitHub source-access
requirement. In the task-creation transaction, the control plane chooses the
healthy online cattle worker with GitHub access and the lowest `(active +
queued) / capacity` load, breaking an exact tie by worker ID. A legacy worker
that already advertises the checkout is also eligible. It excludes repositories
without retained-worktree headroom and excludes a cattle worker without cache
headroom unless that repository is already cached. It then freezes the selected
worker and repository on the execution. The poller writes the exact task request
to its own SQLite ledger, submits through
`POST /api/v1/tasks`, and records the returned task ID. Its default state is
`~/.factory/poller/poller.sqlite3`.

### Worker

`cmd/factory-worker` starts one worker manager, prints a worker identity, runs
manual cleanup, or starts the internal attempt supervisor. The manager:

- resolves and locks its data directory;
- creates or loads a durable worker ID;
- resolves any optional legacy repository paths and normalizes their `origin`
  identities;
- checks Git and runtime health and automatically probes local GitHub access;
- clones or fetches assigned managed repositories into a bounded cache before
  agent startup;
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
2. The worker validates its TOML, data directory, runtime, and any optional
   legacy repositories.
3. The worker reconciles durable attempt manifests before accepting new work.
4. A healthy worker registers its identity, runtime, capacity, provider access,
   managed-repository acquisition capability, optional legacy repositories,
   bounded cached repository IDs, retained worktrees, and disposed attempt IDs.
5. A worker is shown as offline when its last registration is more than 30
   seconds old.

### Task creation and claiming

1. A caller submits a unique `request_key`, title, description, optional timeout,
   and either an explicit worker/repository pair or a repository remote plus
   source-access route.
2. For a route, the control plane requires an enabled managed repository,
   chooses an eligible worker by fair load, and freezes both IDs. It then
   creates one task and one queued execution. Reusing the request key returns
   the original task.
3. The assigned worker polls its claim endpoint with a unique request ID and
   lease token.
4. The control plane verifies worker health, recency, capacity, runtime,
   repository advertisement, and repository retention capacity.
5. It selects the oldest eligible queued execution, creates a preparing
   attempt, stores only a digest of the lease token, and returns the claim.
6. An empty response is idempotent for five minutes. A successful response is
   idempotent while its attempt remains active and its lease remains valid.

### Issue polling and dispatch

1. The poller recovers every pending stored request before reading a source.
2. For each queue, it asks the provider CLI for issues matching the configured
   project, status, and labels.
3. GitHub results and normalized command results are validated and limited to
   100 issues.
4. GitHub polling keeps only issue identity, URL, title, state, and labels. The
   poller composes the trusted queue prompt followed by that clearly marked
   untrusted context and a live-state revalidation instruction.
5. It stores the exact task request before posting it to the control plane.
6. The existing task request key makes a lost response safe to replay.
7. Later polls skip the same queue, source, project, and issue key.

Source polling never changes the issue. The agent prompt may tell the worker to
use its installed provider CLI to update the issue and open a pull request.

### Attempt execution

1. The worker validates the claim identity, assignment, runtime, repository ID,
   and remote identity.
2. It uses a compatible legacy checkout or serially clones/acquires the managed
   repository cache.
3. It revalidates the registered origin identity, discovers the origin default
   branch or uses a legacy repository's configured `base_branch`, fetches it,
   freezes its exact commit, and checks the origin identity again.
4. It creates a branch named
   `factory/<task-prefix>-<attempt-prefix>` and an owned worktree.
5. It writes a protected attempt manifest before starting the runtime.
6. The internal supervisor starts, then the worker transitions the attempt to
   `running`.
7. Runtime output is sent as ordered, idempotent event batches.
8. The worker renews the 30-second lease while the supervisor is active.
9. Completion records a bounded result, error, and outcome, and moves the
   execution to `succeeded`, `failed`, or `cancelled`.

The legacy checkout or managed cache is repository metadata and a shared Git
object store; agent work never runs inside it. Worktrees isolate Git state, not
process, network, credential, or host filesystem access. A future sandbox may
contain the prepared worktree without changing task or execution identity.

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
GET    /api/v1/repositories
POST   /api/v1/repositories
GET    /api/v1/repositories/{repository_id}
PUT    /api/v1/repositories/{repository_id}/enabled
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
- A repository is the central fleet record. Its enabled flag gates new routed
  work but does not rewrite existing assignments.
- A worker-repository row may be a legacy static advertisement or the dynamic
  association frozen when a cattle worker is selected.
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
| Managed repositories | 1,000 |
| Cached repositories per worker | 100 |
| Task page | 50 by default, 200 maximum |
| Event page | 100 by default, 500 maximum |
| Issues per queue pass | 100 |
| Source command output | 4 MiB |
| Source command stderr | 64 KiB |
| Source command duration | 30 seconds |
| Poller observations | 10,000 |

### Files and configuration

```text
~/.factory/
  bin/
    factory-server
    factory-worker
    factory-poller
  server/
    factory.sqlite3
    factory.sqlite3.v2-control-plane
  worker.toml
  poller.toml
  poller/
    poller.sqlite3
  workers/<worker>/
    worker-id
    worker.lock
    repositories/<repository-id>/
    attempts/
    disposed-attempts.json
    worktrees/
```

The marker filename and contents retain compatibility with the earlier Go
preview storage format. They do not represent a second application.

`FACTORY_DATA_HOME` changes the default root. `FACTORY_WORKER_CONFIG` and
`FACTORY_POLLER_CONFIG` select worker and poller TOML files.
`FACTORY_BUILD_DIR`, `FACTORY_LISTEN`, `FACTORY_SKIP_BUILD`, and
`FACTORY_WORKER_READY_SECONDS` configure local commands. Earlier `FACTORY_V2_*`
names remain migration aliases in code and the local launcher, but are not
operator-facing configuration.

When `data_directory` is omitted, a worker derives the absolute
`<config-directory>/workers/<config-basename-without-.toml>` path. Explicit
relative worker data paths and optional legacy repository paths are resolved
from the directory that contains the worker TOML; explicit absolute worker data
paths are unchanged. Managed repositories are configured by the control-plane
API and cached below the worker data directory.

## 7. Security and trust boundaries

The current trust boundary is one trusted user on one host:

- the server binds only to loopback and validates request host resolution;
- there is no login, authorization, worker credential, TLS, or tenant boundary;
- worker IDs identify local state but are not secrets;
- the agent process has the worker OS user's permissions and can access anything
  available to that user;
- the enabled central repository catalog controls routed assignment. Workers
  accept only canonical GitHub identities from that catalog and never clone an
  arbitrary URL supplied by a ticket. This is not a filesystem sandbox;
- provider CLIs own their credentials; the poller does not request, store, or
  pass provider tokens;
- workers advertise GitHub source access and managed acquisition only after a
  successful local `gh auth status` probe; registrations contain no token;
- configured source commands and queue prompts are trusted operator policy;
- issue fields are stored in the poller ledger and task prompt as untrusted
  context;
- lease tokens are random, sent over local HTTP, and stored as SHA-256 digests;
- browser mutations must be same-origin and use JSON;
- worker data directories, identity files, and manifests use restrictive
  permissions and reject unsafe symlinks where identity matters;
- an existing database must be a regular non-symlink file; its adjacent marker
  validates the storage format, and a newly created marker uses mode `0600`;
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
- One failed issue queue does not stop later queues in the same pass. Failed
  source results create no observations. Failed task submissions remain pending
  and replay before the next source poll.
- A GitHub route with no eligible worker removes its unsubmitted pending row so
  the next pass refetches the live issue before another routing attempt.
- Poller observations are capped at 10,000. Submitted rows discard their stored
  request body, but remain as deduplication records until the operator archives
  and resets the ledger. An issue does not rearm after leaving and re-entering
  its queue condition.

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
- issue-source validation, durable dispatch, HTTP replay, and restart
  deduplication tests in `internal/poller`;
- server and worker command tests in `cmd`;
- embedded asset tests in `web`;
- React unit, polling, and browser tests in `web/src`;
- Just command-surface and local-launch checks in `scripts/test-build.sh` and
  `scripts/test-run-local.sh`.

The contributor check set is documented in [CONTRIBUTING.md](CONTRIBUTING.md).

## 10. Known limitations

- Only local loopback deployments are supported.
- Windows workers are unsupported.
- There is no authentication, authorization, tenant isolation, or remote worker
  transport.
- A task has one execution assigned to one worker. Fan-out and cross-worker
  rescheduling are not implemented.
- Execution scheduling is pull-based FIFO per worker. There are no priorities,
  cron triggers, or automatic retries.
- GitHub is the only built-in issue source. Jira, Linear, and other providers
  need a command adapter that implements the normalized issue JSON contract.
- Poller configuration is file-based and has no UI. Issue observations do not
  rearm or expire automatically.
- Reusable workflows, scheduled automations, and a unified `factory` CLI are
  proposed but not implemented.
- Metrics do not confirm external outcomes such as merged pull requests or
  closed tickets.
- Terminal history requires explicit deletion. There is no time-based retention
  policy.

The current poller is documented in [docs/poller.md](docs/poller.md). More
advanced behavior is described separately in the
[workflow](docs/workflows/design.md),
[GitHub ingest](docs/github-ingest/design.md), and [CLI](docs/cli/design.md)
designs.

## 11. Source map

| Area | Source |
| --- | --- |
| Server process and defaults | `cmd/factory-server` |
| Worker process and commands | `cmd/factory-worker` |
| Poller process and commands | `cmd/factory-poller` |
| HTTP API and state machine | `internal/controlplane/http.go`, `state.go` |
| Persistence and metrics | `internal/controlplane/store.go`, `metrics.go` |
| Database schema | `migrations` |
| Shared contracts and limits | `internal/protocol` |
| Worker orchestration | `internal/worker/manager.go`, `registration.go`, `claiming.go`, `attempt_lifecycle.go` |
| Runtime supervision | `internal/worker/supervisor.go` |
| Repository acquisition, Git worktrees, and cleanup | `internal/worker/repository_cache.go`, `git.go`, `reconcile.go`, `cleanup.go` |
| Durable worker state | `internal/worker/identity.go`, `manifest.go` |
| Issue sources and dispatch ledger | `internal/poller` |
| State path compatibility | `internal/statepath` |
| UI source and API client | `web/src` |
| Embedded UI serving | `web/embed.go`, `web/dist` |
| Build and checks | `Justfile` |
| Local process launcher | `scripts/run-local.sh` |
