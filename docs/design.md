# Factory vision and technical design

This is the source of truth for what Factory is, why it exists, and how its
implemented architecture works. The [setup guide](local-v1.md) explains how to
run it, while the [operations guide](operations.md) explains how to inspect and
recover it.

## Vision

Coding agents can implement substantial changes, but most teams still operate
them as one-off terminal sessions. A person notices work, chooses a prompt,
starts an agent, waits for it, forwards feedback, and remembers to try again.
The quality of the process depends on who happens to be driving.

Factory makes that process repeatable. It gives software work the same kind of
durable, observable execution that CI/CD gives builds and deployments:

```text
ticket or schedule -> trusted trigger -> durable task -> isolated agent -> review
```

The ticket system is the control plane. It holds the problem, decisions,
acceptance criteria, discussion, status, and evidence. Moving a ticket into a
configured condition is an explicit request for an agent pass. The agent reads
the live ticket, works in the repository, and returns its result to the same
human-owned review loop.

Factory is not an autonomous product manager or a replacement for engineering
judgment. Humans decide what matters, resolve ambiguous product choices, review
the result, and remain accountable for what ships. Factory removes the manual
coordination between those decisions.

The long-term goal is a small, reliable kernel that can supervise different
ticket sources, agent runtimes, and isolation systems without encoding one
team's development process.

## Design principles

### Tickets are the durable interface

Important context must live in the issue, pull request, or review, not only in
an agent session. A later human or worker should be able to continue from
external state after a timeout, crash, or restart.

### Configuration owns mechanism; prompts own policy

Factory owns polling, matching, durable claims, concurrency, timeouts,
isolation, cancellation, history, and recovery. Repository-owned Markdown
workflows tell the agent what outcome to produce and what policy to follow.

This boundary lets teams change their process without changing Factory. Triage,
implementation, security review, and maintenance are prompt conventions, not
built-in pipeline stages.

### Idle must be cheap

Polling and schedule evaluation are deterministic local work. Factory starts no
model when no trigger matches and no schedule is due.

### Human review is the shipping boundary

Factory's default workflows may update tickets and open pull requests, but they
do not merge or enable automatic merge. Credentials and branch protection must
preserve that boundary.

### Recovery uses real external state

Factory records attempts and workspace ownership, then lets a later run inspect
the current issue, branch, pull request, CI, and review state. It does not try to
replay a fixed list of GitHub mutations.

### Start narrow

The current product runs one repository, one source, and Codex workers. New
providers and runtimes should fit behind existing boundaries only when operating
evidence justifies them.

## System model

Factory has four concepts:

| Concept | Responsibility |
| --- | --- |
| Source | Returns tickets that match a requested state and labels. |
| Trigger | Connects a source condition or schedule to a workflow. |
| Workflow | Plain Markdown describing the agent's outcome and policy. |
| Worker | Runs the workflow with a timeout, concurrency limit, and sandbox. |

The complete loop is:

```text
              poll
                |
                v
source -> matching event -> durable task -> atomic claim -> revalidate
   ^                                                   |
   |                                                   v
   +---- ticket, pull request, CI, and review <- worker in sandbox
```

The generated GitHub adapter queries native issue state and labels. A
repository-owned source may query another trusted control plane instead. This
repository's `github-project` source queries Project status so Status is its
machine-facing gate.

## Repository contract

Each managed repository owns:

```text
.factory/
├── config.toml
├── sources/
│   └── github
└── workflows/
    ├── bug-finder.md
    ├── implement.md
    └── triage.md
```

The configuration makes the relationships explicit:

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
command = [".factory/sources/github"]

[trigger.triage]
type = "source"
state = "open"
labels = ["factory:ready-for-spec"]
workflow = ".factory/workflows/triage.md"

[trigger.implement]
type = "source"
state = "open"
labels = ["factory:ready-to-implement"]
workflow = ".factory/workflows/implement.md"
timeout = "4h"

[trigger.bug-finder]
type = "schedule"
schedule = "0 9 * * 1"
timezone = "Europe/London"
workflow = ".factory/workflows/bug-finder.md"
```

Trigger types are tagged so validation can reject mixed, unknown, or misspelled
fields. Trigger IDs are stable queue identities, not semantic stages.

Workflow files contain instructions only. They have no frontmatter and cannot
override worker configuration. A workflow may direct the agent to use
repository instructions or skills, but Factory does not install, interpret, or
version those formats.

## Source boundary

For every source trigger, Factory invokes the configured command with:

```text
--state <state> --label <label> ...
```

The command returns a provider-neutral JSON object:

```json
{
  "issues": [
    {
      "key": "#56",
      "title": "Fix the daemon",
      "description": "What is broken and why",
      "state": "open",
      "labels": ["factory:ready-to-implement"],
      "url": "https://github.com/example/repo/issues/56"
    }
  ]
}
```

Factory bounds execution time and output size, validates the schema, rejects
duplicate keys, and verifies that every result satisfies the requested
condition. The included GitHub adapter implements this contract with the
authenticated `gh` CLI. The experimental [Jira adapter](jira.md) demonstrates
the same boundary for another provider.

The source command is part of the trust boundary. Source adapters do not need
to filter by issue author, but only trusted people may satisfy their configured
condition. For the generated adapter that means applying a triggering label;
for this repository's Project adapter it means changing Project status.

## Trigger semantics

### Source triggers

A source trigger runs once for each unchanged revision during a continuous
match. Factory records the ticket and trigger pair when it first appears.
Repeated polls of the same revision do not create duplicate tasks. Leaving the
condition rearms the pair, so returning later creates a new task for a review or
correction pass.

An adapter may supply a revision to identify a new event without requiring the
ticket to leave the condition first. A changed revision can create a new task
when no task for that ticket is already queued or running. When the adapter
omits it, Factory derives a stable revision from the ticket key and requested
condition.

Immediately before starting a source task, Factory runs the same query again.
If the ticket no longer matches, the worker does not start. This closes the
race between polling and execution.

### Schedule triggers

A schedule trigger uses a five-field cron expression and an IANA timezone. Its
durable identity includes the scheduled instant, so an instant creates at most
one task across restarts.

Scheduled workflows can inspect the repository, find bugs, review dependencies,
or create tickets that enter the same human-controlled loop.

## Task and worker lifecycle

SQLite stores tasks, trigger observations, run attempts, cancellation requests,
bounded output, workspace ownership, and sandbox metadata under Factory's data
directory outside the repository.

A database uniqueness constraint deduplicates task identities. An atomic
queued-to-running transition prevents two daemon workers from claiming the same
task. The daemon enforces the configured concurrency limit and each workflow's
resolved timeout, capped by `maximum_timeout`.

Execution follows this sequence:

1. Poll sources and evaluate schedules.
2. Reconcile observations and insert new durable tasks.
3. Atomically claim eligible queued work within the concurrency limit.
4. Revalidate live source tasks.
5. Prepare an isolated workspace.
6. Build a prompt from workflow instructions and task context.
7. Run Codex while recording bounded activity and cancellation state.
8. Record the outcome and reconcile or retain the workspace.

Unexpected exits remain visible as run attempts. On startup, Factory reconciles
active tasks and owned resources. Interrupted work receives up to two bounded
recovery attempts; later work continues from durable repository and ticket
state.

## Workspace and isolation model

### Worktree mode

Worktree mode creates a Factory-owned Git worktree outside the primary checkout
and runs the host Codex CLI inside it. It isolates branches and working-tree
state, but shares the host filesystem, processes, network, and credentials.
Only trusted work should use this mode.

Each new task starts from the fetched remote default branch in a detached
workspace. The workflow and agent own branch selection and may continue an
existing trusted branch or pull request after inspecting live state. Terminal
scheduled workspaces are disposable. Ticket workspaces with unpublished, dirty,
failed, or cancelled work are retained for inspection and explicit cleanup.

### Docker Sandbox mode

Docker Sandbox mode creates a standalone host clone and a private clone inside
a microVM. The worker has a separate kernel and Docker daemon, bounded CPU and
memory, deny-by-default networking, and proxy-managed OpenAI and GitHub
credentials. The canonical checkout and Factory database are not mounted.

Before removing the sandbox, Factory snapshots tracked and untracked changes
and fetches that commit into trusted host Git metadata. If handoff fails, it
retains the sandbox and host clone for recovery.

Isolation limits the blast radius but does not make ticket content trusted.
Allowed network calls and repository writes are still external effects, so
credentials must be narrow and protected branches must remain effective.

## Responsibility boundaries

Factory owns:

- repository-local configuration validation;
- source command execution and normalized result validation;
- polling, edge detection, schedules, and durable task identity;
- atomic claims, concurrency, timeouts, and cancellation;
- worktree or Docker Sandbox lifecycle;
- process supervision, bounded logs, inspection, and restart recovery.

The workflow and agent own:

- reading live issues, comments, pull requests, CI, and review state;
- investigating or reproducing the problem;
- clarifying requirements and updating the ticket;
- choosing and implementing a technical solution;
- using `git`, `gh`, and repository tools;
- creating or updating branches and pull requests;
- responding to feedback and reporting evidence.

Factory deliberately does not encode comments, branches, pull requests, CI
repair, or ticket transitions as deterministic built-in operations. Those steps
change often, require judgment, and are best reconciled by an agent against live
state.

## Example development loop

The default files express a two-phase process without hard-coding it:

```text
new idea or bug
      |
      v
ready-for-spec -> triage workflow -> human reviews refined ticket
                                            |
                                            v
ready-to-implement -> implementation workflow -> pull request
        ^                                              |
        +------------ CI and human feedback -----------+
```

Triage removes its trigger label, investigates the repository, and turns vague
work into an executable ticket. A human reviews that specification and applies
the implementation label. Implementation removes its trigger label, makes the
change, verifies it, and opens or updates a pull request. Human review and CI
may send the ticket through another pass.

The label names and workflow details belong to the repository. Factory only
sees a condition connected to a prompt.

## Security model

Ticket bodies, comments, linked pull requests, and attachments are untrusted
input. They cannot override repository-owned workflows or operator policy.

Operators must:

- restrict label and triage access for triggering repositories;
- use dedicated, revocable credentials with the smallest useful scope;
- keep secrets out of config, workflows, tickets, and logs;
- enforce branch protection that the worker identity cannot bypass;
- use Docker Sandbox mode when host-level isolation is not acceptable;
- retain human review as the merge boundary.

See [SECURITY.md](../SECURITY.md) and the
[worker boundary](operations.md#worker-boundary) for deployment guidance.

## Current scope and extension points

The implemented V1 supports:

- one repository and one command-backed source;
- source conditions based on state and labels;
- five-field cron schedules with IANA timezones;
- Codex workers in managed worktrees or Docker Sandboxes;
- strict repository-local configuration and Markdown workflows;
- durable queueing, deduplication, supervision, inspection, cancellation,
  cleanup, reset, and restart recovery.

The architecture leaves room for:

- Jira, Linear, GitLab, and pull-request source adapters;
- multiple repositories or sources in one daemon;
- agent runtimes other than Codex;
- webhooks as a wake-up optimization;
- hosted worker pools and stronger isolation;
- deployment workflows where a separate policy defines authorization.

These are extension points, not promises. Factory does not currently provide a
workflow graph, a web control plane, automatic merge, or a provider-specific
action language.
