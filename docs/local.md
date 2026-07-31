# Run Factory locally

This guide starts one control plane and one worker on macOS or Linux.

## Requirements

- Go 1.25 or newer
- Git
- `curl`
- `just`
- an authenticated Codex CLI or Claude Code CLI
- an authenticated `gh` CLI for centrally managed GitHub repositories

Node.js is not required for normal startup.

## Configure a worker

Build the binaries and copy the example:

```sh
just build
mkdir -p ~/.factory
cp examples/worker.toml ~/.factory/worker.toml
```

Edit `~/.factory/worker.toml`:

```toml
server = "http://127.0.0.1:7337"
name = "local-codex"
runtime = "codex"
max_concurrent = 1
```

With this `~/.factory/worker.toml` filename, Factory defaults durable worker
state to `~/.factory/workers/worker`. The config filename, rather than `name`,
selects the state directory.

No worker repository list is required. Factory detects local GitHub access with
`gh auth status` and clones centrally managed repositories on demand. Optional
legacy repository paths remain available for manual UI delegation. Relative
paths resolve from the worker TOML directory, and each path must be a real,
non-bare Git checkout with an `origin` remote. Factory starts legacy work from
the origin default branch without changing the checkout. To use another base,
configure it under that repository:

```toml
base_branch = "release/2026.07"
```

For Claude Code, use another config and identity:

```toml
server = "http://127.0.0.1:7337"
name = "local-claude"
runtime = "claude-code"
max_concurrent = 1
```

Saved as `~/.factory/claude-worker.toml`, this worker uses
`~/.factory/workers/claude-worker`. Different config filenames keep multiple
worker identities separate on one host. Set `data_directory` only when an
explicit relative or absolute override is needed; never share one data
directory between worker identities.

## Start

The launcher builds the Go binaries, starts the server, waits for health, starts
the worker, and waits for that worker to register:

```sh
just run
```

Open [http://127.0.0.1:7337](http://127.0.0.1:7337).

Stop both processes with Ctrl+C.

Add a GitHub repository to the central fleet once:

```sh
curl --fail --silent --show-error \
  -H 'Content-Type: application/json' \
  -d '{"remote_identity":"github.com/OWNER/REPOSITORY"}' \
  http://127.0.0.1:7337/api/v1/repositories
```

List the current fleet with `GET /api/v1/repositories`. Set
`{"enabled":false}` with `PUT /api/v1/repositories/REPOSITORY_ID/enabled` to
stop new routed work without interrupting an execution whose worker assignment
is already frozen. Posting a repository first discovered from a legacy worker
promotes it into the enabled central fleet. Reposting a centrally managed
repository does not override an explicit disable.

To start with a different worker config:

```sh
just run ~/.factory/claude-worker.toml
```

To run more than one worker, start the control plane once and then start each
additional worker directly:

```sh
~/.factory/bin/factory-worker \
  -config ~/.factory/claude-worker.toml
```

## Delegate a task

The current manual delegation screen lists optional legacy checkouts advertised
by workers. Add a `[repositories.<key>]` entry when you need that path. Cattle
workers with no static checkout are currently exercised through centrally
routed task creation such as the GitHub poller.

In the UI:

1. Open Workers and confirm the worker is online and healthy.
2. Select Delegate task.
3. Enter a title and description.
4. Select the worker and repository.
5. Submit.

The Work view shows the task state. Task detail shows attempts, lifecycle events,
results, and errors.

The same operation is available through the API:

```sh
curl --fail --silent --show-error \
  -H 'Content-Type: application/json' \
  -d '{
    "request_key": "manual-example-1",
    "title": "Review the README",
    "description": "Review the README for errors, fix them, test the change, and commit it.",
    "worker_id": "WORKER_ID",
    "repository_id": "REPOSITORY_ID"
  }' \
  http://127.0.0.1:7337/api/v1/tasks
```

Worker and repository IDs are available from:

```sh
curl --fail --silent --show-error \
  http://127.0.0.1:7337/api/v1/workers
```

## Data and overrides

Factory stores state below `~/.factory` by default. Common overrides:

```text
FACTORY_DATA_HOME
FACTORY_WORKER_CONFIG
FACTORY_BUILD_DIR
FACTORY_LISTEN
FACTORY_SKIP_BUILD
FACTORY_WORKER_READY_SECONDS
```

Examples:

```sh
FACTORY_LISTEN=127.0.0.1:7444 just run

FACTORY_DATA_HOME=/srv/factory \
  just run /srv/factory/worker.toml
```

The server remains loopback-only.

## UI development

Only contributors changing the UI need Node.js:

```sh
just ui-install
cd web && npm run dev
```

Before committing UI changes:

```sh
just ui-check
just ui-build 0
```

The operator build embeds the committed `web/dist` and never invokes npm.

## Troubleshooting

`127.0.0.1 refused to connect`

- confirm `just run` is still running;
- inspect the terminal for server or worker startup errors;
- check `curl http://127.0.0.1:7337/healthz`;
- check that another process is not using port 7337.

Worker never becomes healthy

- confirm the selected runtime command is on `PATH`;
- authenticate Codex or Claude Code as the same OS user;
- confirm every repository path and its `origin`;
- ensure each worker has a unique data directory;
- inspect the worker JSON logs.

Work is retained

Factory keeps worktrees when they are dirty or may contain unpublished work.
Open the assigned Worker to see retained paths and cleanup commands. Use the
attempt ID from the task detail or retained worktree card to preview cleanup:

```sh
~/.factory/bin/factory-worker cleanup ATTEMPT_ID \
  --config ~/.factory/worker.toml
```

Add `--confirm` to remove the worktree. The local branch is preserved, but
uncommitted changes shown in the preview are lost.
