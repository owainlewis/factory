# Run Factory locally

This guide starts one control plane and one worker on macOS or Linux.

## Requirements

- Go 1.25.12 or newer on the 1.25 release line, or Go 1.26.5 or newer
- Git
- `curl`
- `just`
- an authenticated Codex CLI or Claude Code CLI
- an authenticated `gh` CLI for centrally managed GitHub repositories

Node.js is not required for normal startup.

## Configure the control plane

Most control-plane configuration is stored in SQLite and managed through the
browser. The optional `~/.factory/config.toml` contains only bootstrap settings
needed before SQLite opens:

```toml
listen = "127.0.0.1:7337"
database = "server/factory.sqlite3"
```

Relative database paths resolve from the config file directory. Unknown fields,
symlinks, and files larger than 1 MiB are rejected. Command-line flags override
the file. Copy [the example](../examples/config.toml) only when changing these
defaults.

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

The launcher builds the Go binaries, starts one control-plane process, waits for
health, starts the worker, and waits for that worker to register. The server
runs every provider and schedule Automation evaluation loop:

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
workers with no static checkout receive centrally routed work from typed
Automations and API task creation.

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
FACTORY_SERVER_CONFIG
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

## Migrate a legacy factory-poller

Migration is offline and disabled-first. Never run it while any legacy poller
process can write the ledger.

1. Stop every `factory-poller` process and keep it stopped.
2. Back up the legacy `poller.toml` and ledger.
3. Start the current Factory control plane and open **Automations → Migrate
   legacy poller**.
4. Select the legacy paths when they are not the defaults, confirm the poller is
   stopped, and choose **Preview locked snapshot**.
5. Review every resolved source path, managed-repository identity and ID,
   proposed Workflow and Automation title, observation count, and error.
   Unsupported command queues
   remain recoverable from the later archive but are not imported. If the
   ledger contains observations for a queue removed or renamed in
   `poller.toml`, Factory shows that ledger-only queue and blocks Import. Restore
   the matching queue entry, stop the poller, and run Preview again so no
   observation identity is silently omitted.
6. Choose **Import disabled Automations**. Factory verifies the exact Preview
   snapshot while holding an exclusive legacy-ledger lock. Existing submitted
   task identities and deleted-task tombstones are retained. Imported
   Automations cannot be enabled yet.
7. For every legacy pending observation, choose **Resume** to replay its exact
   durable task request or **Skip** to record that it must not dispatch.
8. Choose **Finalize and archive**. Factory locks and verifies the same snapshot,
   then archives consistent copies of the config and ledger with a hash
   manifest. It does not modify or delete either source file.
9. Review and test each typed Automation, then enable it. The control plane now
   owns evaluation and deduplication.

Preview, Import, and Finalize fail closed if the source paths, config bytes,
ledger bytes, inode, schema or rows, snapshot digest, or lock availability
changes. Correct the cause and run a new Preview. An archive write failure
leaves the migration imported and safe to retry. Closing the browser or
restarting Factory rediscovers the active imported migration and preserves
pending Resume or Skip decisions and an already completed archive.

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

GitHub Automation reports `gh_missing` or `gh_unauthenticated`

- install `gh` on the control-plane host;
- run `gh auth login` as the same OS user that starts `factory-server`;
- verify `gh auth status --hostname github.com`;
- restart the server or test the Automation again.

Legacy poller migration cannot acquire its lock

- confirm every old poller process is stopped;
- confirm no SQLite inspection tool has a transaction open on the legacy
  ledger;
- do not copy or edit the source files during Preview, Import, or Finalize;
- retry the action after releasing the writer.

Legacy poller snapshot changed

- leave imported Automations disabled;
- inspect what changed in the config or ledger;
- return to the migration dialog and run a new Preview against the stable
  source. Factory does not partially import or archive a changed snapshot.

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
