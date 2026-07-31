# Factory

Factory is infrastructure for running coding agents across Git repositories.
Its Go control plane stores work, shows the worker fleet, and delegates tasks.
Each Go worker owns one agent runtime, currently Codex or Claude Code.

The browser UI provides:

- an overview of execution throughput, reliability, cycle time, queue depth, and
  worker health;
- a work queue and task details;
- registered worker and repository status;
- task delegation using a title, prompt, repository, and worker.

Factory is local-first today. The server only accepts loopback connections and
does not include user authentication.

## Quick start

Requirements:

- Go 1.25 or newer
- Git
- `curl`
- Codex CLI or Claude Code CLI, authenticated on the worker host

Node.js is only needed when changing the UI. Normal builds use the committed,
embedded UI assets.

```sh
./scripts/build.sh
mkdir -p ~/.factory
cp examples/worker.toml ~/.factory/worker.toml
```

Edit `~/.factory/worker.toml` to select `codex` or `claude-code` and add the
repositories this worker may use. Then start the server and worker:

```sh
./scripts/run-local.sh
```

Open [http://127.0.0.1:7337](http://127.0.0.1:7337).

One worker has one stable identity and one runtime. Run another worker with a
different config and data directory when you want both Codex and Claude Code:

```sh
FACTORY_WORKER_CONFIG=~/.factory/claude-worker.toml \
  ~/.factory/bin/factory-worker
```

See the [local guide](docs/local.md) for a complete setup and the
[worker guide](docs/worker.md) for runtime and worktree behavior.

## Architecture

```text
Browser
   |
   | HTTP + JSON
   v
Go control plane
  SQLite, scheduler, embedded UI
   |
   | registration, claim, heartbeat, events, completion
   v
Go workers
  one identity + one runtime + allowed repositories
   |
   +-- Codex CLI
   `-- Claude Code CLI
```

The control plane owns durable coordination. Workers own execution and Git
worktrees. Workers poll the API, so the system does not require WebSockets or
inbound connections to worker hosts.

All default state is below `~/.factory`:

```text
~/.factory/
  bin/
  server/factory.sqlite3
  worker.toml
  workers/
```

Read the [architecture](ARCHITECTURE.md) for the contracts and
security boundaries.

## Current scope

Implemented:

- Go control-plane API and embedded React UI;
- durable tasks, executions, attempts, leases, events, and cancellation;
- Codex and Claude Code workers;
- repository allowlists and isolated Git worktrees;
- bounded list APIs and retained-data metrics;
- automatic cleanup of clean unchanged or published work and preservation of
  unpublished branches.

Designed but not implemented:

- reusable workflows;
- scheduled automations;
- GitHub issue ingest;
- a unified `factory` CLI.

See the [documentation index](docs/README.md) for current guides and proposed
designs.

## Development

Backend:

```sh
go test ./...
go vet ./...
```

UI:

```sh
cd web
npm ci
npm run typecheck
npm run lint
npm test
```

After changing the UI, rebuild the committed assets:

```sh
FACTORY_SKIP_INSTALL=1 ./scripts/build-ui.sh
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full check set.
