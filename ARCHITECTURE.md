# Factory architecture

> **Status:** Current implementation
>
> **Verification basis:** Working tree including Workflows, typed Automations,
> and the offline legacy-poller migration from issues #184 through #187

## 1. Executive summary

Factory is a local control plane for running coding agents in Git repositories.
It separates durable coordination from agent execution:

- `factory-server` stores work, assigns it, evaluates typed GitHub issue and
  pull-request Automations through `gh`, admits schedule Automations from its
  clock, exposes the HTTP API, and serves the embedded browser UI.
- `factory-worker` has one stable identity and one agent runtime. It advertises
  runtime capacity and provider access, acquires centrally managed repositories
  on demand, and runs attempts in isolated Git worktrees.
- Codex or Claude Code performs the repository work as a child process of the
  worker.

The current task contract is a title, either a legacy free-text description or
a pinned Workflow revision plus free-text context, assigned worker, repository,
and timeout. The control plane snapshots one resolved prompt in the existing
task description field before creating the task. Callers may name the
assignment directly or ask the control-plane scheduler to choose from cattle
workers. The deployment is limited to a trusted user and loopback HTTP on one
host.

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
10. Automation evaluation is read-only. An Automation and provider identity
    creates at most one Occurrence and task, including across server restarts
    and lost HTTP responses.
11. A Workflow has stable identity, enabled state, and immutable numbered
    Markdown revisions. The control plane alone composes Workflow instructions
    with free-text task context.
12. Tasks snapshot their Workflow name, revision, context, and resolved prompt.
    Workers remain generic and receive the resolved prompt through the existing
    claim task description.
13. A typed Automation is created disabled. One issue, pull request, scheduled
    UTC instant, or idempotent Run now key creates at most one durable
    Occurrence and one ordinary Task per Automation.

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
metrics, pagination, Workflow revisions, typed GitHub and schedule Automations,
provider health, prompt composition, and persistence.
Claim selection is transactional and FIFO by execution creation time for the
requesting worker.

SQLite runs with foreign keys, WAL journaling, a five-second busy timeout, and
at most eight open connections. The default database is
`~/.factory/server/factory.sqlite3`.

### Legacy poller migration

The retired standalone poller has no binary, startup path, command, or current
configuration example. `internal/controlplane/legacy_poller_*` implements its
offline migration into typed Automations. Preview, Import, and Finalize each
resolve the selected legacy config and ledger, acquire an exclusive SQLite
ledger lock, and hash the exact config bytes, selected paths, schema, and
ordered observation rows together with the ledger inode and full-file SHA-256.
A lock failure, pathname replacement, pragma change, or snapshot change aborts
the action without partial control-plane writes.

Preview records stable source paths, queue mappings, counts, proposed names,
and validation errors. Import atomically creates one Workflow and one disabled
GitHub issue Automation per supported queue. Submitted observations retain
their task identity or deleted-task tombstone. Pending observations retain the
exact stored request and require explicit Resume or Skip. Imported Automations
cannot be enabled before Finalize. The active imported migration is discoverable
after a browser or server restart, and every imported observation remains
visible beyond the ordinary paginated Automation history limit. Finalize
verifies the same locked snapshot, copies the ledger through the retained locked
file descriptor, writes and fsyncs the config and hash manifest archive, and
then records completion. It never changes or deletes the source files.

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
managed Repositories, Workflows, Task detail, and Delegate task views.
Repository detail combines the central catalog with the control plane's current
routing and acquisition readiness facts. It polls the same-origin API.

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

1. A caller submits a unique `request_key`, title, either a free-text
   `description` or a pinned Workflow revision with free-text `context`, an
   optional timeout, and either an explicit worker/repository pair or a
   repository remote plus source-access route. The two prompt forms are
   exclusive.
2. The control plane returns an existing task before rechecking mutable
   Workflow state when the request key is a replay. For a new task it validates
   the selected revision and enabled Workflow, then composes and bounds the
   resolved prompt.
3. For a route, the control plane requires an enabled managed repository,
   chooses an eligible worker by fair load, and freezes both IDs. It then
   snapshots the context, Workflow identity, revision, and resolved prompt while
   creating one task and one queued execution.
4. The assigned worker polls its claim endpoint with a unique request ID and
   lease token.
5. The control plane verifies worker health, recency, capacity, runtime,
   repository advertisement, and repository retention capacity.
6. It selects the oldest eligible queued execution, creates a preparing
   attempt, stores only a digest of the lease token, and returns the claim.
7. An empty response is idempotent for five minutes. A successful response is
   idempotent while its attempt remains active and its lease remains valid.

### Legacy poller migration

1. The operator stops every legacy poller and requests Preview.
2. The server holds an exclusive legacy-ledger lock while resolving sources,
   validating queues and pending payloads, counting observations, and storing
   the full file identity and snapshot digest.
3. Import reacquires the lock and accepts only that exact snapshot. One
   transaction creates disabled typed Automations, Workflows, and deduplicating
   Occurrences without changing tasks, workers, or source files.
4. Submitted observations link by their stored task ID. A nonblank missing ID
   becomes a stable deleted-task tombstone even if its request key was reused.
   Only a blank historical ID may fall back to the request key.
5. Each pending observation requires Resume of its exact stored request or an
   explicit Skip. A restart recovers an interrupted Resume and deterministic
   request keys prevent duplicate tasks.
6. Closing the browser or restarting the server rediscovers the one active
   imported migration. A second Preview is blocked until it is finalized.
7. Finalize reacquires the lock, verifies the same snapshot, atomically archives
   consistent config and ledger copies plus a manifest, and unlocks imported
   Automations for operator review and enablement.

### Control-plane GitHub Automation

1. An operator creates a disabled Automation bound to one Workflow and one
   managed GitHub repository, then previews its bounded matches.
2. Enabling schedules an immediate check. A legacy-imported Automation remains
   blocked until its migration is finalized.
3. The evaluator runs fixed `gh issue list` or `gh pr list` arguments without a
   shell, with a 30-second timeout, bounded output, strict JSON, and
   repository-specific URL validation. Pull-request checks also validate draft
   inclusion, labels, optional base branches, and head-commit identity.
4. One transaction validates the evaluation token and enabled dependencies,
   stores every new typed Occurrence, updates health and counters, and advances
   the next check. Repeated issues reuse the existing Occurrence identity.
5. A later transaction routes the pending Occurrence, creates or exactly
   recovers its deterministic Task, and links the Task to the Occurrence.
6. The prompt separates trusted configured conditions from bounded untrusted
   GitHub metadata and requires authenticated `gh` live-state revalidation
   before mutation.

### Control-plane schedule Automation

1. An operator creates a disabled Automation with a five-field cron expression
   and separate IANA timezone, then previews the next matching UTC instant.
2. Enabling stores the first match strictly after the enable transaction. The
   existing Automation service checks due schedules alongside provider checks
   and commits one durable Occurrence before task dispatch.
3. Cron fields are parsed by a small standard-library-only implementation. It
   iterates UTC minutes and matches local calendar fields, so a daylight-saving
   overlap yields two UTC identities and a nonexistent local minute yields none.
   This explicit behavior is why Factory does not add a cron dependency.
4. Startup admits at most the stored overdue instant, then advances directly to
   the first future match. Run now uses a separate idempotent request-key domain
   and never changes the due cursor.
5. The existing occurrence dispatcher routes the snapshotted Workflow and
   repository as an ordinary Task. Schedule work does not require provider
   access, and workers retain the same claim contract.

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
  executions. It returns the existing execution to `queued`, increments its
  retry count, and reuses the task's original resolved prompt even if its
  Workflow was revised or disabled.

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
GET    /api/v1/workflows?name={name}&enabled={bool}&limit={1..200}&cursor={cursor}
POST   /api/v1/workflows
GET    /api/v1/workflows/{workflow_id}
POST   /api/v1/workflows/{workflow_id}/revisions
PUT    /api/v1/workflows/{workflow_id}/enabled
GET    /api/v1/automations?limit={1..200}&cursor={cursor}
POST   /api/v1/automations
GET    /api/v1/automations/{automation_id}
PUT    /api/v1/automations/{automation_id}
PUT    /api/v1/automations/{automation_id}/enabled
POST   /api/v1/automations/{automation_id}/test
POST   /api/v1/automations/{automation_id}/check
POST   /api/v1/automations/{automation_id}/run
GET    /api/v1/automations/{automation_id}/occurrences?limit={1..200}&cursor={cursor}
POST   /api/v1/migrations/legacy-poller/preview
POST   /api/v1/migrations/legacy-poller/import
GET    /api/v1/migrations/legacy-poller/active
GET    /api/v1/migrations/legacy-poller/{migration_id}
POST   /api/v1/migrations/legacy-poller/{migration_id}/finalize
POST   /api/v1/occurrences/{occurrence_id}/resume
POST   /api/v1/occurrences/{occurrence_id}/skip
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
Workflow 1 --- * WorkflowRevision 1 --- * Task
Workflow 1 --- * Automation 1 --- * Occurrence 0..1 --- Task
Repository 1 --- * Automation
LegacyPollerMigration 1 --- * Automation
LegacyPollerMigration 1 --- * imported Occurrence
Worker   1 --- * WorkerRepository * --- 1 Repository
Task     1 --- 1 Execution       1 --- * Attempt 1 --- * AttemptEvent
```

- A Workflow stores stable identity, enabled state, and a pointer to its current
  immutable revision. The control plane stores at most 500 Workflows and each
  retains at most 100 revisions.
- A task stores nullable operator context, repository, optional Workflow
  snapshot, and its exact resolved prompt in the existing description field.
- A repository is the central fleet record. Its enabled flag gates new routed
  work but does not rewrite existing assignments.
- An Automation stores one concrete `github_issue`, `github_pull_request`, or
  `schedule` Trigger, health and polling or due cursor, counters, and
  disabled-first state. Its
  Occurrences snapshot the Workflow revision, repository, predicate,
  observation, prompt, and deterministic Task request key before dispatch.
- Automation and Occurrence collection APIs use opaque descending cursors, so
  every supported record remains reachable beyond the first bounded page.
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
| Workflow instructions | 48 KiB |
| Resolved prompt | 64 KiB |
| Complete agent prompt | 72 KiB |
| Workflows | 500 |
| Workflow revisions per Workflow | 100 |
| Workflow page | 50 by default, 200 maximum |
| Automations | 500 |
| Automation occurrences | 100,000 |
| Automation context | 8 KiB |
| Automation page | 50 by default, 200 maximum |
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
| Provider matches per Automation check | 100 |
| Provider command output | 4 MiB |
| Provider command stderr | 64 KiB |
| Provider command duration | 30 seconds |
| Legacy config input | 1 MiB |

### Files and configuration

```text
~/.factory/
  bin/
    factory-server
    factory-worker
  server/
    factory.sqlite3
    factory.sqlite3.v2-control-plane
  config.toml
  worker.toml
  archive/poller/<migration-id>/
    poller.toml
    poller.sqlite3
    manifest.json
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

`FACTORY_DATA_HOME` changes the default root. `FACTORY_SERVER_CONFIG` selects
the optional control-plane bootstrap TOML. `FACTORY_WORKER_CONFIG` selects one
worker TOML.
`FACTORY_BUILD_DIR`, `FACTORY_LISTEN`, `FACTORY_SKIP_BUILD`, and
`FACTORY_WORKER_READY_SECONDS` configure local commands. Earlier `FACTORY_V2_*`
names remain migration aliases in code and the local launcher, but are not
operator-facing configuration.

When `data_directory` is omitted, a worker derives the absolute
`<config-directory>/workers/<config-basename-without-.toml>` path. Explicit
relative worker data paths and optional legacy repository paths are resolved
from the directory that contains the worker TOML; explicit absolute worker data
paths are unchanged. Managed repositories, Workflows, Automations, and
evaluation state are configured in SQLite through the control-plane API. Only
the listen address and database path belong in `config.toml` because the server
needs them before SQLite opens. Managed repositories are cached below the
worker data directory.

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
- provider CLIs own their credentials; Factory does not request, store, or pass
  provider tokens;
- the control-plane evaluator invokes the local authenticated `gh` executable
  with fixed arguments and stores no GitHub token;
- workers advertise GitHub source access and managed acquisition only after a
  successful local `gh auth status` probe; registrations contain no token;
- Workflow instructions and Automation predicates are trusted operator policy;
- provider item fields are stored in an Automation Occurrence and task prompt
  as untrusted context;
- legacy migration reads only explicitly resolved regular non-symlink files,
  binds and retains the locked ledger inode for each action, rejects pathname
  replacement, and never deletes the sources;
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
- A failed Automation check retains actionable health and retry timing. New
  Occurrences remain durable pending work until the ordinary dispatcher can
  route them.
- Legacy migration lock or snapshot failure makes no partial import or archive.
  Archive failure leaves the migration imported and retryable. A restart
  recovers interrupted pending Resume and already completed archive states.
- Submitted legacy observations preserve their durable identity after import;
  pending rows retain their exact request only until explicit Resume or Skip.
- One unhealthy Automation does not stop control-plane APIs or other
  Automations. Missing, unauthenticated, timed-out, malformed, oversized, and
  over-limit `gh` results, plus corrupt stored schedule data, are stored as
  actionable per-Automation health.
- Normal server shutdown closes a shared occurrence-admission gate for both due
  schedules and HTTP Run now requests, cancels active `gh` processes, waits for
  evaluator work to finish, and uses a bounded non-cancelled context to dispatch
  committed ready Occurrences before closing SQLite.

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
- legacy migration lock, snapshot, identity, pending resolution, archive,
  restart, and duplicate-prevention tests in `internal/controlplane`;
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
- Execution scheduling is pull-based FIFO per worker. There are no priorities
  or automatic task retries.
- GitHub is the only provider implemented for typed provider Automations. Jira,
  Linear, generic provider plugins, and command adapters are not implemented.
- Legacy poller command queues are reviewed in Preview and preserved in the
  archive, but are not imported. Provider items do not automatically rearm.
- A unified `factory` CLI is proposed but not implemented.
- Metrics do not confirm external outcomes such as merged pull requests or
  closed tickets.
- Terminal history requires explicit deletion. There is no time-based retention
  policy.

The legacy migration and current typed Automation operation are documented in
the [local guide](docs/local.md). Design history is described separately in the
[workflow](docs/workflows/design.md),
[GitHub ingest](docs/github-ingest/design.md), and [CLI](docs/cli/design.md)
designs.

## 11. Source map

| Area | Source |
| --- | --- |
| Server process and defaults | `cmd/factory-server` |
| Worker process and commands | `cmd/factory-worker` |
| HTTP API and state machine | `internal/controlplane/http.go`, `state.go` |
| Persistence and metrics | `internal/controlplane/store.go`, `metrics.go` |
| Workflow identity, revisions, and listing | `internal/controlplane/workflows.go` |
| Typed Automation store and API | `internal/controlplane/automations.go`, `automations_http.go` |
| GitHub evaluator and occurrence dispatch | `internal/controlplane/automation_runtime.go` |
| Schedule parsing and admission | `internal/controlplane/schedule_cron.go`, `schedule_runtime.go` |
| Legacy poller migration and archive | `internal/controlplane/legacy_poller_*` |
| Prompt composition and complete agent input | `internal/protocol/prompt.go` |
| Database schema | `migrations` |
| Shared contracts and limits | `internal/protocol` |
| Worker orchestration | `internal/worker/manager.go`, `registration.go`, `claiming.go`, `attempt_lifecycle.go` |
| Runtime supervision | `internal/worker/supervisor.go` |
| Repository acquisition, Git worktrees, and cleanup | `internal/worker/repository_cache.go`, `git.go`, `reconcile.go`, `cleanup.go` |
| Durable worker state | `internal/worker/identity.go`, `manifest.go` |
| State path compatibility | `internal/statepath` |
| UI source and API client | `web/src` |
| Embedded UI serving | `web/embed.go`, `web/dist` |
| Build and checks | `Justfile` |
| Local process launcher | `scripts/run-local.sh` |
