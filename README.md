# Factory

**Developer preview**

Factory is currently in *developer preview* and is iterating rapidly.
**THERE WILL BE COMPATIBILITY-BREAKING CHANGES.**

Factory runs repeatable software-engineering Work across Git repositories and
compute. Operators save prompts as Routines, run them now, or schedule them
across one or more repositories. Its Go control plane coordinates Work and a
fleet of Workers that provide Pi, Codex, or Claude Code runtimes.

The browser UI provides:

- an operational overview of active, completed, and attention-needed Work;
- Routines with prompts, repositories, runtime settings, and optional schedules;
- table, list, and board views of Work, with per-repository Target details;
- registered worker and repository status;

Factory is local-first. Its browser and operator API accept loopback connections
only. An optional, separate TLS-authenticated endpoint accepts Workers on remote
VMs.

Factory starts with coding agents on machines you control and is intended to
scale to elastic cloud execution without rewriting a Routine or splitting its
Work history. Existing Routines remain persistent by default. Once the proposed
cloud backend is implemented, a manual run will be able to select a compatible
elastic profile. Persistent local and VM Workers remain the rich path for
subscription authentication, warm repository caches, and inspectable
worktrees. A proposed
Cloud Run backend adds disposable, API-backed agent containers for bursty and
parallel Work. Read the
[Cloud Run agent backend design](docs/cloud-run-agents/design.md) for the
planned boundary, security model, and rollout.

Read the [product vision](docs/software-factory/vision.md), the
[current implementation architecture](ARCHITECTURE.md), and the active
[Cloud Run backend design](docs/cloud-run-agents/design.md). Planned work
follows the [project workflow](WORKFLOW.md). Superseded proposals remain
available through the [documentation index](docs/README.md).

## Quick start

Requirements:

- Go 1.25.12 or newer on the 1.25 release line, or Go 1.26.5 or newer
- Git
- `curl`
- `just`
- Codex CLI or Claude Code CLI, authenticated on the worker host

Node.js is only needed when changing the UI. Normal builds use the committed,
embedded UI assets.

Managed GitHub repository checkout and Routines that allow the `gh` tool depend
on the GitHub CLI. Factory does not include a separate GitHub API client.
Install and authenticate [`gh`](https://cli.github.com/) on the control-plane
host and each eligible Worker host:

```sh
gh --version
gh auth status
```

```sh
just build
mkdir -p ~/.factory
cp examples/worker.toml ~/.factory/worker.toml
```

Edit `~/.factory/worker.toml` to select any installed `pi`, `codex`, and
`claude-code` agents. Workers need no repository list. They probe local `gh`
access and acquire centrally managed GitHub repositories on demand. Then start
the server and Worker:

```sh
just run
```

Open [http://127.0.0.1:7337](http://127.0.0.1:7337).

Database migration 27 converts supported pre-launch Definitions, schedules,
and execution history into Routines and Work after the database is backed up.
Unsupported legacy provider admission is blocked and reported instead of being
silently discarded.

One Worker has one stable identity, a configurable set of coding-agent
capabilities, and a pool of independent sessions. The pool defaults to ten
slots. A single local Worker can advertise Pi, Codex, and Claude Code:

```sh
FACTORY_WORKER_CONFIG=~/.factory/worker.toml \
  ~/.factory/bin/factory-worker
```

See the [local guide](docs/local.md) for a complete setup and the
[worker guide](docs/worker.md) for runtime and worktree behavior. Use the
[remote VM guide](docs/remote-workers.md) to enroll a Worker outside the server
host. Tagged binary
installation, upgrades, compatibility, rollback, and release verification are
covered by the [release guide](docs/release.md).

## Architecture

```text
Browser
   |
   | HTTP + JSON
   v
Go control plane
  SQLite, Routine scheduler, Work admission, embedded UI
   ^
   | registration, claim, heartbeat, events, completion
   |
Go Workers
  one identity + ready runtime capabilities + N agent slots + repository cache
   |
   +-- Pi CLI
   +-- Codex CLI
   `-- Claude Code CLI
```

The control plane owns durable coordination. Workers own execution and Git
worktrees. Workers poll the API, so the system does not require WebSockets or
inbound connections to worker hosts.

The future execution model keeps three choices independent:

```text
Execution backend     Agent runtime              Provider and model
-----------------     -------------              ------------------
Persistent Worker  +  Pi, Codex, Claude Code  +  subscription or API
Cloud Run Job      +  Pi, Codex, Claude Code  +  API-backed model
```

Cloud Run is a proposed execution backend, not a DeepSeek-specific product.
For example, the completed experiment ran the Pi runtime in Cloud Run with
DeepSeek V4 Flash through OpenRouter. Cloud execution is managed and elastic,
but not infrastructure-free, infinitely scalable, or a hostile-code sandbox.

All default state is below `~/.factory`:

```text
~/.factory/
  bin/
  server/factory.sqlite3
  config.toml       optional control-plane bootstrap only
  worker.toml
  workers/
```

Read the [architecture](ARCHITECTURE.md) for the contracts and
security boundaries.

## Current scope

Implemented:

- Go control-plane API and embedded React UI;
- durable Routines, Work, Targets, executions, attempts, leases, events, and
  cancellation;
- Pi, Codex, and Claude Code Worker capabilities;
- manual and scheduled Routine admission across one or more repositories;
- versioned Routine prompts, execution settings, repository scope, and optional
  schedules;
- a central managed-repository catalog, bounded worker caches, and isolated Git
  worktrees;
- bounded list APIs and retained-data metrics;
- automatic cleanup of clean unchanged or published work and preservation of
  unpublished branches.

Designed but not implemented: a unified `factory` CLI and an elastic Cloud Run
agent backend.

See the [documentation index](docs/README.md) for current guides and proposed
designs.

## Development

Backend:

```sh
just test
just vet
```

UI:

```sh
just ui-install
just ui-check
```

After changing the UI, rebuild the committed assets:

```sh
just ui-build 0
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full check set.
