# Factory architecture

Status: implemented local control plane and workers

## Purpose

Factory coordinates coding agents across Git repositories. It separates durable
coordination from execution:

- the control plane accepts work, schedules attempts, stores state, and serves
  the UI;
- workers advertise repositories and capacity, claim attempts, and run one agent
  runtime;
- Codex or Claude Code performs the repository work inside a dedicated Git
  worktree.

The smallest useful task contract is:

```text
title + description + repository + worker
```

The description is the prompt. A future workflow can supply a reusable prompt,
while context such as a ticket or merge request provides the subject.

## Components

### Control plane

`factory-server` is a Go HTTP server. It owns:

- the JSON API;
- an embedded React UI;
- SQLite persistence and migrations;
- task, execution, attempt, lease, and event state machines;
- worker registration and health;
- fair claim selection within worker and repository capacity;
- cancellation, retry, pagination, retention deletion, and summary metrics.

The server binds to `127.0.0.1:7337` by default. It rejects non-loopback listen
addresses because authentication and tenant isolation are not implemented.

### Worker

`factory-worker` is a Go process with a stable identity. One worker configuration
selects exactly one runtime:

- `codex`
- `claude-code`

A worker may advertise several local repositories and a concurrency limit. Run
separate worker identities for different runtimes, machines, trust boundaries,
or capacity pools.

Workers initiate all control-plane traffic. They register, heartbeat, long-poll
for claims, renew leases, append events, and complete attempts. The control
plane never needs an inbound connection to a worker.

### UI

The React UI is compiled into `web/dist` and embedded in the server binary.
Operators do not need Node.js. Contributors rebuild the assets only when UI
source changes.

The UI has four primary views:

- Overview: retained execution and fleet metrics;
- Work: paginated task list;
- Workers: registered identities, runtimes, repositories, capacity, and health;
- Task detail: execution status, attempt events, branch, result, retry,
  cancellation, and deletion.

Task delegation uses a drawer so the operator can keep fleet or task context
visible.

## Data model

```text
Task
  one operator request
  |
  +-- Execution
        durable assignment and current state for the task
        |
        +-- Attempt
              one claimed run or retry with a worker lease and runtime process
              |
              +-- Events
                    bounded output and lifecycle records

Worker
  stable identity + runtime + capacity
  |
  +-- Repositories
        allowed local checkouts advertised to the control plane
```

Tasks and execution history are durable. List endpoints use cursor pagination.
Operators can delete terminal tasks. Deletion removes control-plane history but
does not delete a branch that may contain unpublished work.

## Scheduling and leases

The scheduler is part of the control plane for the local system. Keeping it next
to the database makes claiming transactional and avoids another deployment unit.

1. A task creates a queued execution.
2. A compatible worker asks for work.
3. The control plane checks worker capacity and repository availability.
4. It creates an attempt with a time-limited lease.
5. The worker renews the lease while the runtime is alive.
6. Completion moves the execution to `succeeded`, `failed`, or `cancelled`.
7. An expired lease becomes lost and may produce another attempt.

Claims and completion are idempotent. Stale leases cannot complete newer
attempts.

## Worker execution

For each attempt, the worker:

1. validates the assigned repository against its allowlist;
2. reads the repository's current `HEAD`;
3. creates an owned worktree and `factory/<task>-<attempt>` branch;
4. starts the configured agent runtime with the task prompt and execution
   contract;
5. captures bounded structured output and lifecycle events;
6. reports branch, commits, summary, and outcome;
7. removes a clean published worktree when safe;
8. retains unpublished or dirty work and reports its path.

Cleanup is fail-closed. The worker checks its protected manifest, expected path,
branch, repository identity, and Git worktree registration before removing
anything.

## API

Current operator endpoints include:

```text
GET    /healthz
GET    /api/v1/tasks
POST   /api/v1/tasks
GET    /api/v1/tasks/{id}
POST   /api/v1/tasks/{id}/cancel
POST   /api/v1/executions/{id}/retry
DELETE /api/v1/tasks/{id}
GET    /api/v1/workers
GET    /api/v1/metrics/summary?window=24h|7d|30d|all
```

Worker endpoints cover registration, heartbeat, claim, lease renewal, event
append, and completion. All requests and responses use JSON. The system uses
polling rather than WebSockets.

## Metrics

The overview reports only facts held by the control plane:

- executions created and completed;
- succeeded, failed, and cancelled outcomes;
- success and retry rates;
- median execution cycle time;
- queued and running work;
- online and registered workers.

Windows are `24h`, `7d`, `30d`, or all retained history. Success rate excludes
cancellations. Factory does not infer merged pull requests or triaged tickets
from an agent's text output. Those metrics require provider-confirmed source
events in a future ingest integration.

## Storage

Factory uses one default home:

```text
~/.factory/
  bin/
  server/factory.sqlite3
  server/factory.sqlite3.v2-control-plane
  worker.toml
  workers/<configured-worker-directory>/
    worker-id
    worker.lock
    attempts/
    disposed-attempts.json
    worktrees/
```

The adjacent marker filename and contents are retained as a storage-format
compatibility detail. They do not identify a separate application.

`FACTORY_DATA_HOME` changes the root. `FACTORY_WORKER_CONFIG` selects a worker
config. Earlier preview environment names remain accepted as migration aliases
but are not documented operator interfaces.

## Security boundary

The current deployment is one trusted user on one machine:

- loopback HTTP only;
- no OIDC or login;
- no remote worker authentication;
- no Windows worker support;
- agent processes run with the worker user's permissions.

Before accepting remote workers, Factory needs TLS, authentication, scoped
worker credentials, authorization, audit logs, and explicit multi-tenant
isolation.

## Planned extensions

The current contracts leave room for:

- reusable workflow prompts and version snapshots;
- cron schedules and provider triggers;
- GitHub issue and pull request ingest;
- one ingest process monitoring several repositories;
- remote VM or Kubernetes workers;
- a unified `factory` CLI.

These extend the same task and attempt model. They do not require a second
control plane or runtime plugin framework.
