# Run Factory V2 locally

This guide starts the complete local V2 control plane on Unix: one Go server
with its embedded UI and one Go worker that can run Codex across two local Git
repositories. A worker can instead run Claude Code. V1 remains separate and
unchanged.

## Prerequisites

Install:

- Go 1.24 or newer;
- Git;
- curl;
- the Codex or Claude Code CLI.

Node.js 22 and npm are needed only when changing the web UI. Normal builds use
the reviewed `web/dist` assets committed to the repository.

Authenticate the runtime before starting the worker:

```sh
codex login
codex login status
# Or:
claude auth login
claude auth status --json
```

Each configured repository must be a local non-bare Git repository with an
`origin` remote. Factory creates managed worktrees from the repository's
current `HEAD`.

## Fresh checkout

Clone Factory, create a local ignored configuration, and edit the two
repository paths:

```sh
git clone https://github.com/owainlewis/factory.git
cd factory
mkdir -p .factory-v2
cp examples/v2-worker.toml .factory-v2/worker.toml
```

The sample config is:

```toml
server = "http://127.0.0.1:7337"
name = "local"
runtime = "codex"
max_concurrent = 1
data_directory = "data/workers/local"

[repositories.factory]
path = "/absolute/path/to/factory"

[repositories.second-repository]
path = "/absolute/path/to/another-repository"
```

Relative paths are resolved from the configuration file. Repository keys are
stable display names. Use a different worker data directory for every worker
process.

To run both agents locally, copy the worker file and set the second worker to
`runtime = "claude-code"`. Give it a different `name` and `data_directory`.
Both workers may advertise the same repositories and appear as separate
schedulable identities in the UI.

## Build and start

One command builds both Go binaries from the committed embedded UI assets:

```sh
./scripts/build-v2.sh
```

When changing the web UI, rebuild the committed assets explicitly before
building the Go binaries:

```sh
./scripts/build-v2-ui.sh
./scripts/build-v2.sh
```

After editing `.factory-v2/worker.toml`, one command builds and starts the
server and worker:

```sh
./scripts/run-v2-local.sh .factory-v2/worker.toml
```

Open `http://127.0.0.1:7337/`. The script keeps both processes attached to the
terminal and stops them together on Ctrl-C. Set `FACTORY_V2_SKIP_BUILD=1` to
reuse a completed build. `FACTORY_V2_LISTEN` may select another loopback port,
and `FACTORY_V2_DATA_HOME` may select another V2 root. When changing the port,
set the worker config's `server` URL to the same port.

The server prints its UI URL and database path. The worker prints its stable
worker ID, control-plane URL, data directory, and repository count. Startup
fails with a direct error when the config is missing, a repository is invalid,
the port is already in use, the server cannot become healthy, or either process
exits early.

The production process model and normal local build do not need Node. Node is
used only by contributors to rebuild the UI that `factory-server` embeds.

## Delegate and inspect work

Open **Workers** and confirm that `local` is online, healthy, and advertises
both repositories. Select **Delegate task**, then enter:

- a title;
- the full agent prompt in Description;
- the worker;
- one repository advertised by that worker;
- a timeout.

The task opens immediately. Its detail page polls ordinary HTTP endpoints for
state and ordered progress. It shows the assigned worker and repository,
attempt history, terminal result or error, and cancellation state. Refreshing
or reopening the browser reads the same durable SQLite state.

Task list reads default to 50 items and accept at most 200. Attempt event reads
default to 100 items and accept at most 500. Use the returned `next_cursor` and
`next_after` values to request older tasks and later events.

The equivalent read-only API checks are:

```sh
curl --fail http://127.0.0.1:7337/healthz
curl --fail http://127.0.0.1:7337/api/v1/workers
curl --fail 'http://127.0.0.1:7337/api/v1/tasks?limit=50'
```

Use the UI to cancel queued or active work. Failed and cancelled tasks expose
an explicit **Retry task** action. Retry creates another attempt for the same
task; Factory never retries automatically.

Task prompts, terminal results, attempts, and events remain durable until an
operator deletes them. A terminal task exposes **Delete history** on its detail
page. The action requires confirmation and removes that task's execution,
attempts, claim records, and events in one database transaction. Queued or
active tasks cannot be deleted. After an attempt reaches a terminal state,
deletion waits for the worker to report whether its worktree was cleaned or
retained. A task whose attempt is still reported as a retained worktree must
be cleaned first. The equivalent API call is:

```sh
curl --fail -X DELETE -H 'Content-Type: application/json' \
  --data '{}' http://127.0.0.1:7337/api/v1/tasks/TASK_ID
```

## Results, restarts, and cleanup

The selected worker runtime runs in:

```text
<worker data_directory>/worktrees/<attempt-id>
```

Its branch is:

```text
factory-v2/<task-id-prefix>-<attempt-id-prefix>
```

Successful clean worktrees and their managed local branches are removed only
when the branch commit is the original base or is reachable from a remote ref.
Failed, cancelled, dirty, unpublished, or otherwise unproven worktrees and
branches are retained and shown on Worker detail with a cleanup command.

Stop the worker before cleanup. Preview first:

```sh
.factory-v2/bin/factory-worker cleanup ATTEMPT_ID \
  --config .factory-v2/worker.toml
```

After inspecting or preserving local changes, confirm:

```sh
.factory-v2/bin/factory-worker cleanup ATTEMPT_ID --confirm \
  --config .factory-v2/worker.toml
```

Operator-confirmed cleanup preserves the branch. It removes only the
manifest-owned V2 worktree after rechecking its repository, path, branch,
commit, and Git registration.

The server and worker may be restarted with the same start command. The server
reopens durable task history. The worker reuses its ID, stops any verified
leftover process group, reconciles every attempt manifest, and reports retained
or missing worktrees before claiming new work. It never resumes an agent after a
worker restart.

## V1 isolation

The local script stores server state under `.factory-v2/data` by default, and
the sample stores worker state below that same V2-only root. It never reads
`.factory/` or V1's `factory.sqlite3`. Both server and worker refuse a selected
V2 root that contains or sits below a V1 database marker.

Do not point `FACTORY_V2_DATA_HOME` or `data_directory` at a V1 state
directory. The automated Go suite verifies that V1 database bytes and V1 Git
worktree registrations remain unchanged during a real V2 attempt.

The launcher regression check also proves that an existing healthy worker
cannot mask an unhealthy worker started by the command, and that the launcher
does not create a missing V2 descendant below V1 state before server validation:

```sh
./scripts/test-run-v2-local.sh
```

## Test the real local workflow

The browser suite builds and starts the real Go server and worker, creates two
real temporary Git repositories, and runs tasks through a deterministic fake
Codex executable. It proves UI delegation, HTTP polling, ordered events,
terminal results, branch and worktree evidence, active cancellation, offline
state, retry, retained cleanup information, durable browser refresh, and the
absence of WebSocket or server-sent event connections:

```sh
cd web
npm ci
npx playwright install chromium
npm run test:browser
```

Screenshots are written to `web/test-results/screenshots/` for desktop and
narrow layouts.

## Browser screenshots

The committed screenshots below come from the real-process browser proof.

| Desktop | Narrow |
| --- | --- |
| [Work](assets/v2/work-desktop.png) | [Work](assets/v2/work-narrow.png) |
| [Workers](assets/v2/workers-desktop.png) | [Workers](assets/v2/workers-narrow.png) |
| [Delegate task](assets/v2/delegate-desktop.png) | [Delegate task](assets/v2/delegate-narrow.png) |
| [Completed task detail](assets/v2/task-detail-desktop.png) | [Active task detail](assets/v2/task-detail-narrow.png) |

When the installed Codex CLI is authenticated, run a bounded manual smoke task
through the same local UI. Use a disposable repository, a short prompt such as
“Inspect this repository and return its current branch without changing
files,” and set a separate one-minute timer. Confirm the task reaches a
terminal state and inspect its result. If it remains active when the timer
expires, cancel it in the UI. CI never uses local credentials.

## Limits and later ingest

The MVP is local-only, loopback-only, Unix-only, supports Codex and Claude Code,
and is manually
triggered. It has no login, OIDC, public binding, remote workers, Kubernetes,
WebSockets, server-sent events, scheduler, automatic merge, or source ingest.

A later separate ingest worker may monitor up to ten configured GitHub
repositories. It will keep independent cursor and error state per repository,
poll at most two repositories concurrently, normalize each eligible item into
repository, title, description, and worker, and call the same task API as this
UI. That extension is documented by the
[V2 architecture](v2-architecture/design.md) but is not implemented here.
