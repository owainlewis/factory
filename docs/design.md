# Factory architecture

## The model

Factory is a durable trigger-to-prompt runner for software work:

```text
GitHub source -> matching trigger -> durable task -> sandboxed agent -> GitHub
      ^                                                            |
      +------------------------------------------------------------+
```

The ticket system is the control plane. It is where people and agents describe,
prioritize, approve, review, and observe work. Factory watches it and starts an
agent only when an explicit condition matches.

Factory v1 is deliberately scoped to one repository and one GitHub source. That
keeps repository identity, credentials, configuration, sandboxes, and recovery
simple enough to operate today. The source boundary can support Jira, Linear, or
GitLab later without changing the task and worker kernel.

## Four concepts

```text
Source    The external ticket queue. GitHub in v1.
Trigger   A status, label, or schedule condition.
Workflow  A plain Markdown prompt describing an outcome and policy.
Worker    The runtime, sandbox, timeout, and concurrency limits.
```

The config makes every relationship explicit:

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
workflow = ".factory/workflows/triage/WORKFLOW.md"

[trigger.implement]
type = "source"
state = "open"
labels = ["factory:ready-to-implement"]
workflow = ".factory/workflows/implement/WORKFLOW.md"
timeout = "4h"

[trigger.maintenance]
type = "schedule"
schedule = "0 9 * * 1"
timezone = "Europe/London"
workflow = ".factory/workflows/maintenance/WORKFLOW.md"
```

The tagged trigger type is important. It makes the data model unambiguous and
lets validation reject mixed or misspelled fields. Trigger IDs are stable queue
identities, not semantic pipeline stages.

Workflow files contain only instructions. They have no metadata or frontmatter.
Config owns when and how the prompt runs. The Markdown file owns what the agent
should achieve.

Repository-local skills are optional prompt context, not a Factory abstraction.
A workflow may instruct the agent to read a skill for reusable behaviour such
as browser verification or code review. Factory does not install, load, version,
or interpret those skills. This keeps the execution kernel independent of any
one agent's skill format.

## Responsibility boundary

Factory owns mechanisms that must be consistent:

- poll timing and source command execution;
- validation of the provider-neutral source output;
- event detection and edge rearming;
- durable task identity and atomic claims;
- concurrency and time limits;
- worktree or Docker sandbox lifecycle;
- process supervision, cancellation, logs, history, and restart recovery.

The workflow and agent own adaptive work:

- read the current issue, comments, Project, pull requests, and CI state;
- reproduce or clarify a problem;
- edit ticket content and statuses;
- inspect the repository and choose an implementation;
- use `git` and `gh` directly;
- create or update branches and pull requests;
- respond to tests, CI, reviews, and human feedback;
- post the final evidence and handoff.

This boundary avoids encoding every GitHub action inside Factory. Modern
agents already know how to use these tools and reconcile changing state. Factory
adds reliability around that work instead of replacing it with a brittle GitHub
state machine.

## Trigger semantics

### Source

A source trigger asks the configured adapter for issues matching one exact
state and every configured label. It runs once for one continuous visit to that
condition. Leaving the condition rearms the trigger. Returning later creates a
new task, which is useful for human review loops. Factory reruns the same source
query immediately before launch so stale poll results do not start work.

### Schedule

A schedule trigger uses a five-field cron expression and an IANA timezone. Its
identity includes the scheduled instant, so each instant runs at most once even
across restarts. Scheduled prompts can review code, triage pull requests, find
security problems, or create new tickets that feed the same control plane.

Polling is only detection. When no event matches and no schedule is due, Factory
does not launch an agent.

## A software development loop

The generic model can express a practical two-phase factory without hard-coding
it:

```text
New idea or bug
      |
      v
factory:ready-for-spec --triage prompt--> (label removed)
                                        |
                                 human reviews ticket
                                        |
                                        v
factory:ready-to-implement --implementation prompt--> (label removed)
      ^                                             |
      |                                             v
      +-------- human review, CI, agent feedback ---+
```

Triage turns vague work into an executable ticket with context, scope,
acceptance criteria, constraints, and verification, then stops. A human reviews
the ticket and applies the implementation trigger's label. Implementation treats
the approved ticket as the spec, makes the change, and produces review evidence.
Humans still choose work and remain accountable for quality. Their feedback
goes through the issue, review, or CI so the next agent run can continue the
loop.

The exact label names belong to the team's prompts and config, not to Factory.
A human may still track progress on a GitHub Project board for their own
visualization; Factory only ever reads issue state and labels, never a board.

## Durable execution

A ticket task identity includes the repository, trigger, ticket identity, and
source event. A scheduled task identity includes the repository, trigger, and
scheduled instant. A database uniqueness constraint and atomic queued-to-running
transition make the claim durable.

Before a ticket worker starts, Factory rereads current source state and trust.
If the event is no longer valid, it does not launch the worker. A worker is also
told to inspect live GitHub state before changing anything. This protects both
the orchestration boundary and adaptive Git operations from stale observations.

Unexpected exits are recorded as attempts, not forgotten processes. Restart
recovery reconciles active tasks and sandbox resources. A later run can continue
from the real issue, branch, pull request, and CI state rather than replay a list
of deterministic steps.

## Security boundary

Ticket content is untrusted input. Factory only accepts configured users and
keeps orchestration policy outside the ticket. Credentials must be scoped to the
repository and Project being managed. Branch protection should prevent the
worker identity from bypassing required review.

Worktrees isolate Git state from the canonical checkout but share the host,
network, credentials, and processes. Docker mode gives each run a standalone
clone, read-only root, resource limits, and narrower mounts. It still has network
access and GitHub credentials, so it is a useful local sandbox, not a complete
defence against hostile code.

## V1 boundaries

V1 includes:

- one repository and one GitHub source;
- GitHub Project status, issue label, and scheduled triggers;
- Codex workers in worktree or Docker sandboxes;
- explicit workflow paths and strict config validation;
- durable queueing, supervision, history, cancellation, and recovery.

V1 does not include multiple repositories, Jira or Linear adapters, a workflow
graph, deterministic GitHub operations, automatic deployment, or a web control
plane. Those are extensions only when real use proves the need.
