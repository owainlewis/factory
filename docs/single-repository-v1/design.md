# Factory: single-repository v1

Status: implemented design for the first runnable Factory.

## Goal

Run a reliable, token-efficient automation loop inside one repository:

> When a ticket matches this condition, or this schedule becomes due,
> run this agent prompt in a sandbox.

GitHub is the source and control plane. Factory is the durable execution kernel.
The agent owns the engineering workflow and uses `gh` and `git` directly.

## Acceptance criteria

- Factory is configured by one repository-owned `.factory/config.toml`.
- The config has one `[worker]`, one command-backed `[source]`, and one or more explicit
  `[trigger.<id>]` tables.
- Every trigger has exactly one tagged type: `source` or `schedule`.
- Every trigger names one plain Markdown workflow under `.factory/workflows`.
- Workflow files have no frontmatter and cannot override execution config.
- A source condition creates one task while continuously matched and is
  rearmed only after the ticket leaves that condition.
- A schedule creates at most one task for each due instant.
- The source adapter returns only explicitly authorized work.
- Factory starts no model when no trigger matches.
- Workers run in a managed Git worktree or disposable Docker Sandbox.
- Factory survives restart without duplicating a durable task.
- Agents can use authenticated GitHub tools to update tickets and open or update
  pull requests. GitHub behavior is prompt policy, not hard-coded orchestration.

## Configuration

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
command = [
  ".factory/sources/github",
  "--project-owner", "owainlewis",
  "--project-number", "16",
  "--status-field", "Status",
  "--trusted-user", "owainlewis",
]

[trigger.triage]
type = "source"
state = "Ready For Spec"
labels = ["factory:ready"]
workflow = ".factory/workflows/triage/WORKFLOW.md"

[trigger.implement]
type = "source"
state = "Ready To Implement"
labels = ["factory:ready"]
workflow = ".factory/workflows/implement/WORKFLOW.md"
timeout = "4h"

[trigger.security-review]
type = "schedule"
schedule = "0 8 * * 1-5"
timezone = "Europe/London"
workflow = ".factory/workflows/security-review/WORKFLOW.md"
timeout = "1h"
```

The top-level model is intentionally small:

```text
[source] + [trigger.<id>] + workflow file + [worker]
```

`type` is required on a trigger because the variant determines its other valid
fields. Source requires `state` and accepts `labels`. Schedule requires both
`schedule` and `timezone`. All variants require `workflow` and may override
`timeout`. Unknown and mixed fields are errors.

Trigger IDs use lowercase kebab-case and are durable workflow identities. A
workflow path must be repository-relative, end in `.md`, and remain below
`.factory/workflows`. The referenced file must be a regular, non-symlinked,
non-empty Markdown file with no frontmatter.

The source command is the provider boundary. The generated adapter uses `gh`,
but another repository can provide an adapter backed by Jira or Linear.

The worker runtime is Codex in v1. `runtime` remains explicit so a later runtime
adapter can add Claude or another agent without changing trigger semantics.

## Poll and claim loop

```text
poll source and clock
        |
        v
resolve matching events
        |
        v
reconcile edge state and insert unique queued tasks
        |
        v
atomically claim within concurrency limit
        |
        v
re-run source query and revalidate condition
        |
        v
prepare sandbox -> run prompt -> record outcome -> clean up
```

Polling is deterministic and cheap. Agent execution is conditional and
expensive. Keeping those two parts separate lets Factory run continuously
without spending tokens when there is no work.

For source triggers, Factory passes the configured state and labels to the
repository-owned adapter. The adapter returns normalized matching issues.

Factory records whether each ticket currently matches each trigger. The first
matching observation creates a task. Repeated polls do not. A non-matching
observation rearms that pair. A later match creates a new source event and task.
This supports review loops without polling duplicates.

For schedules, the task key includes the cron instant in the configured IANA
timezone. Restarting after an instant cannot enqueue it twice.

Immediately before launch, Factory runs the same source query again. It rejects
a ticket that is no longer returned or whose durable identity does not match.
This closes the gap between polling and claiming.

## Prompt contract

Factory builds a higher-level task prompt containing:

- repository identity;
- workflow ID and workflow instructions;
- ticket identity and triggering condition for ticket work;
- execution and safety context;
- a requirement to inspect current external state before acting.

The workflow describes the outcome. It should tell the agent what evidence to
produce, what constraints apply, and where human review is required. It should
not duplicate trigger metadata.

A practical triage prompt asks the agent to:

1. fetch the issue, comments, Project context, and relevant code;
2. reproduce or investigate the report;
3. rewrite the ticket with context, scope, acceptance criteria, constraints, and
   verification steps;
4. ask a precise human question when a decision is missing;
5. move clearly executable work to the team's implementation-ready status.

A practical implementation prompt asks the agent to:

1. treat the current ticket as the specification;
2. inspect existing branches and pull requests before changing anything;
3. implement the acceptance criteria in a focused change;
4. run relevant tests and an independent code review;
5. push one stable branch and open or update one pull request;
6. wait for CI and respond to actionable feedback;
7. update the issue and Project with evidence for human review.

The agent chooses how to perform these actions with `gh`, `git`, repository
tools, and available skills. Factory does not model comments, branch creation,
pull requests, CI repair, or Project transitions as built-in operations.

## Worker sandboxes

### Worktree

Worktree mode prepares a Factory-owned Git worktree and starts the host Codex
CLI there. It is the simplest demo and development path. It isolates branch and
working-tree state from the canonical checkout, but it shares host credentials,
network, filesystem access, and process privileges. Only trusted work should use
it.

### Docker Sandboxes

Docker Sandbox mode prepares a standalone host clone, then asks `sbx --clone`
to create a private in-VM clone with:

- a separate Linux kernel and private Docker daemon;
- bounded CPU and memory;
- deny-by-default networking;
- no canonical repository or Factory database mount;
- proxy-managed Codex and GitHub credentials whose values stay on the host.

Before removal, Factory snapshots tracked and untracked changes inside the VM
and fetches that commit into trusted host Git metadata. The sandbox is stopped
and retained if this handoff fails.

Docker Sandbox config is explicit:

```toml
[worker]
runtime = "codex"
sandbox = "docker_sandbox"
timeout = "2h"
maximum_timeout = "8h"
max_concurrent = 1
template = "docker/sandbox-templates:codex"
memory = "8g"
cpus = 4
github_token_env = "FACTORY_GITHUB_TOKEN"
```

Factory records the sandbox identity before agent execution and removes managed
resources after terminal outcomes. Retained worktrees are visible through run
inspection and can be removed with `factory cleanup RUN_ID --confirm`.

## Durable state and recovery

SQLite outside the repository stores tasks, source identities, claim state, run
attempts, cancellation requests, bounded output, result summaries, and sandbox
metadata. A unique task key provides deduplication. A transaction provides the
queued-to-running claim.

If Factory stops during a run, startup reconciliation determines what happened
to the recorded process or container and updates the attempt. Future attempts
receive current task context and are expected to reconcile the actual issue,
branch, pull request, and CI state. Recovery does not replay a deterministic list
of GitHub mutations.

## Security and human control

Only configured issue authors can trigger ticket work. This protects against a
random GitHub user opening an issue that receives repository-capable credentials.
Ticket text must still be treated as untrusted. Worker credentials should have
the smallest useful repository and Project permissions and must not bypass branch
protection.

Humans remain responsible for deciding what work matters and for reviewing the
quality of results. The useful feedback loop is external and durable:

```text
ticket -> agent -> pull request -> CI and human review -> ticket or review update
   ^                                                        |
   +--------------------------------------------------------+
```

Moving a ticket back into a triggering state is an explicit request for another
agent pass. Humans guide the system through tickets and reviews instead of taking
over the worker terminal.

## Operations

```sh
factory init
factory validate
factory workflows
factory run --once
factory run
factory tasks --json
factory runs --json
factory inspect RUN_ID
factory cancel RUN_ID
factory cleanup RUN_ID --confirm
```

`factory init` creates opinionated triage and implementation examples. They are
defaults, not built-in pipeline stages. Operators add a workflow by creating its
Markdown file and adding an explicit trigger table to config.

## Deferred scope

- multiple repositories or sources in one daemon;
- Jira, Linear, GitLab, and pull-request event adapters;
- runtime adapters other than Codex;
- webhooks as an optional wake-up optimization;
- hosted worker pools and stronger VM isolation;
- automatic merge and deployment policy;
- workflow graphs or provider-specific action languages.

The v1 should earn each extension through operating evidence. The stable core is
the source, trigger, prompt, and supervised worker loop.
