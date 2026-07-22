# Factory

Factory is a small automation loop around one repository. It watches a trusted
GitHub source, turns matching events into durable tasks, and runs an agent prompt
in an isolated worker.

The source is the control plane. Issues and Project statuses decide what work is
ready. Factory handles polling, deduplication, durable claims, concurrency,
timeouts, sandbox setup, supervision, recovery, and run history. The workflow is
plain Markdown. The agent uses normal tools such as `gh` and `git` to understand
the issue, change code, open a pull request, respond to CI, and update the ticket.

Factory does not encode a fixed software development pipeline. A trigger simply
means: when this condition is true, run this prompt.

## Quick start

Install Rust, Git, GitHub CLI, and Codex CLI. Authenticate the host tools, then
install Factory:

```sh
gh auth login
codex login
cargo install --path . --locked
```

From the repository Factory will manage:

```sh
factory init
```

`factory init` creates a repository-scoped `.factory/config.toml` and two plain
Markdown workflows. Edit the generated source values, status names, trusted
users, and prompts. A complete worktree configuration looks like this:

```toml
version = 1
poll_every = "30s"

[worker]
runtime = "codex"
sandbox = "worktree"
timeout = "2h"
maximum_timeout = "8h"
max_concurrent = 1

[source]
type = "github"
project_owner = "owainlewis"
project_number = 16
status_field = "Status"
trusted_users = ["owainlewis"]

[trigger.triage]
type = "status"
status = "Ready For Spec"
workflow = ".factory/workflows/triage/WORKFLOW.md"

[trigger.implement]
type = "status"
status = "Ready To Implement"
workflow = ".factory/workflows/implement/WORKFLOW.md"
timeout = "4h"

[trigger.maintenance]
type = "schedule"
schedule = "0 9 * * 1"
timezone = "Europe/London"
workflow = ".factory/workflows/maintenance/WORKFLOW.md"
```

Each `[trigger.<id>]` has an explicit `type`:

- `status` runs when a trusted issue enters a configured GitHub Project status.
- `label` runs when a trusted open issue has a configured label.
- `schedule` runs once for each due cron instant.

Status and label triggers run once while the condition remains true. They become
eligible again after the issue leaves and later re-enters the condition. A
schedule trigger runs once per scheduled instant.

Workflow files contain only instructions. They have no frontmatter. The trigger,
runtime, timeout, and sandbox belong in config so there is one clear source of
truth.

Validate and start the loop:

```sh
factory validate
factory workflows
factory run --once
factory run
```

`factory run --once` polls and records eligible work without launching an agent.
`factory run` stays active until Ctrl-C. If nothing matches, Factory starts no
worker and uses no model tokens.

Inspect or operate the durable queue with:

```sh
factory tasks
factory runs
factory inspect RUN_ID
factory cancel RUN_ID
```

You can also test a configured prompt directly with `factory workflow run ID`.

## Sandboxes

`sandbox = "worktree"` is fast and uses the authenticated `gh` and Codex CLIs on
the host. It protects the canonical checkout but is not a security boundary. Use
it only for trusted work.

For stronger isolation, initialize with:

```sh
factory init --execution-mode docker
```

Docker workers use a standalone clone, an explicitly configured image, resource
limits, a read-only Codex authentication file, and the token named by
`worker.github_token_env`. The generated config defaults to a dedicated Codex
login. For a local demo, `worker.codex_auth` may explicitly point at your
existing `~/.codex/auth.json`. Host polling always uses your authenticated `gh`
CLI; the container needs the configured token for its own `gh` and Git access.
Review the generated `.factory/Dockerfile`, build the image, and use credentials
that cannot bypass protected-branch review.

Factory leaves pull requests for human review. Ticket changes, merges, and
deployments are workflow policy, not built-in Factory operations.

See [the runnable guide](docs/local-v1.md), [the architecture](docs/design.md),
and [the detailed v1 design](docs/single-repository-v1/design.md).

## Development checks

```sh
cargo fmt --all --check
cargo clippy --all-targets -- -D warnings
cargo test
```
