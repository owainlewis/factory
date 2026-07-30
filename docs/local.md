# Run Factory locally

This guide starts one control plane and one worker on macOS or Linux.

## Requirements

- Go 1.25 or newer
- Git
- `curl`
- an authenticated Codex CLI or Claude Code CLI

Node.js is not required for normal startup.

## Configure a worker

Build the binaries and copy the example:

```sh
./scripts/build.sh
mkdir -p ~/.factory
cp examples/worker.toml ~/.factory/worker.toml
```

Edit `~/.factory/worker.toml`:

```toml
server = "http://127.0.0.1:7337"
name = "local-codex"
runtime = "codex"
max_concurrent = 1
data_directory = "workers/local-codex"

[repositories.factory]
path = "/absolute/path/to/factory"
```

Repository paths must be absolute, non-bare Git checkouts. Each must have an
`origin` remote. The map key is the repository name shown in the UI.

For Claude Code, use another config and identity:

```toml
server = "http://127.0.0.1:7337"
name = "local-claude"
runtime = "claude-code"
max_concurrent = 1
data_directory = "workers/local-claude"

[repositories.factory]
path = "/absolute/path/to/factory"
```

Do not share a `data_directory` between worker identities.

## Start

The launcher builds the Go binaries, starts the server, waits for health, starts
the worker, and waits for that worker to register:

```sh
./scripts/run-local.sh
```

Open [http://127.0.0.1:7337](http://127.0.0.1:7337).

Stop both processes with Ctrl+C.

To start with a different worker config:

```sh
./scripts/run-local.sh ~/.factory/claude-worker.toml
```

To run more than one worker, start the control plane once and then start each
additional worker directly:

```sh
~/.factory/bin/factory-worker \
  -config ~/.factory/claude-worker.toml
```

## Delegate a task

In the UI:

1. Open Workers and confirm the worker is online and healthy.
2. Select Delegate task.
3. Enter a title and description.
4. Select the worker and repository.
5. Submit.

The Work view shows the task state. Task detail shows attempts, lifecycle events,
the working branch, and the final result.

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
FACTORY_LISTEN=127.0.0.1:7444 ./scripts/run-local.sh

FACTORY_DATA_HOME=/srv/factory \
  ./scripts/run-local.sh /srv/factory/worker.toml
```

The server remains loopback-only.

## UI development

Only contributors changing the UI need Node.js:

```sh
cd web
npm ci
npm run dev
```

Before committing UI changes:

```sh
cd web
npm run typecheck
npm run lint
npm test
cd ..
FACTORY_SKIP_INSTALL=1 ./scripts/build-ui.sh
```

The operator build embeds the committed `web/dist` and never invokes npm.

## Troubleshooting

`127.0.0.1 refused to connect`

- confirm `./scripts/run-local.sh` is still running;
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
Use the task detail result to find the path. Preview the cleanup decision:

```sh
~/.factory/bin/factory-worker cleanup ATTEMPT_ID \
  -config ~/.factory/worker.toml
```

Add `--confirm` to remove the worktree. The local branch is preserved, but
uncommitted changes shown in the preview are lost.
