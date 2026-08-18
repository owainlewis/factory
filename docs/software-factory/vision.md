# Software Factory vision

> **Status:** Product direction
>
> **Superseded vocabulary:** This document uses Job Definition, Run, and Job.
> The implemented operator model is Task, Run, and Session; see
> [Tasks and Runs](../tasks/design.md) and [ARCHITECTURE.md](../../ARCHITECTURE.md).
> The thesis, scope, principles, and measures below still apply.

Factory is infrastructure for building a software factory. It turns
software-engineering intent and events into reliable agent jobs across Git
repositories and compute.

The primary user is a software team that owns many repositories and wants to
apply repeatable engineering procedures with coding agents. Factory is not a
general automation product or another issue tracker. It is the execution and
control layer between engineering work, repository fleets, agent runtimes, and
the compute that runs them.

## Product promise

A team can define a standard engineering procedure once, run it once or many
times, target one work item or hundreds of repositories, and understand what
happened at every target.

Examples include:

- triage and refine issue backlogs;
- review a ticket, implementation plan, patch, or pull request;
- inspect a codebase for bugs, security concerns, or policy drift;
- apply dependency, configuration, or CI changes across a repository fleet;
- let agents create issues, comments, branches, and pull requests with the tools
  available on their Worker;
- run scheduled maintenance and react to GitHub events.

Every use case becomes the same execution primitive. A **Job** runs one agent
against one repository and optional work item. A **Run** invokes one saved
**Job Definition** and may fan out into many independent Jobs.

## Core experience

An operator starts with a Job Definition such as `Review pull request`, `Triage
issue`, or `Find dependency risks`. The definition contains the shared prompt,
runtime needs, and execution defaults.

The operator can run it manually, call it through the API, attach a schedule,
or attach a GitHub event trigger. The invocation freezes the definition,
parameters, and concrete targets into one Run. Each target becomes one Job and
uses the same attempt, lease, log, result, retry, and cancellation machinery.

The operator sees the aggregate Run and every Job. A failure in one repository
does not erase successful siblings. Retrying one Job does not replay the whole
Run.

## Product model

```text
Job Definition
  reusable software-engineering procedure
        |
        +-- manual invocation
        +-- schedule trigger
        +-- GitHub webhook trigger
        `-- API invocation
                 |
                 v
                Run
       frozen definition and target set
                 |
       +---------+---------+
       v         v         v
      Job       Job       Job
   repo/item  repo/item  repo/item
       +---------+---------+
                 |
                 v
 Worker on a local host or VM
       |
       v
 Pi, Codex, Claude Code, or another coding agent
 using Git and GitHub CLI
```

The Job Definition is the saved procedure. Its instructions are not a separate
Runbook resource. A Trigger is an admission rule attached to a definition, not
a second kind of job. A Run records one invocation, so Factory does not need a
separate Occurrence concept.

## Product principles

1. Everything admitted becomes a Run and one or more Jobs.
2. One Job touches one repository by default.
3. A Run freezes its complete definition, parameters, and target set.
4. Saved definitions edit in place. Historical Runs retain their snapshots.
5. Triggers admit work but never execute agents.
6. Workers supply compute. Runtimes such as Pi, Codex, and Claude Code supply
   agent behavior.
7. Provider payloads and repository content are untrusted inputs. Definition
   instructions are trusted operator configuration.
8. Agents use the tools and credentials available on their Worker. Factory does
   not intermediate GitHub comments, branches, issues, or pull requests.
9. Fleet-wide work is bounded, fair, observable, and retryable per target.
10. Product concepts must earn their place through operator behavior, not
    speculative reuse.

## Differentiation

Factory focuses on software delivery execution:

- Git repository fleets and work-item targets;
- isolated worktrees, branches, commits, and pull requests;
- reliable agent scheduling, leases, attempts, and recovery;
- local and VM capacity, with more Worker targets added when needed;
- reusable engineering procedures;
- manual, scheduled, event, and API admission;
- throughput, reliability, cost, and outcome visibility.

Factory does not aim to own human project management, chat, inboxes, agent
personas, squads, a generic workflow builder, or business-process automation.
GitHub and other engineering systems remain the source of issues, pull
requests, reviews, and repository state.

## V1 experience

V1 is a local software factory. An operator can:

- configure a local agent Worker using Pi, Codex, or Claude Code;
- add the team's Git repositories;
- save a shared prompt as a Definition;
- press **Run once** for one repository or a selected repository fleet;
- see every Job, failure, cycle time, throughput, and Worker health;
- attach a schedule to repeat the same Run automatically.

Remote VM Workers are the next scaling step after this local manual and
scheduled path works end to end. GitHub webhook Triggers follow later.
Kubernetes and other execution targets remain possible future Worker types,
but they are not on the active roadmap.

The built-in SQLite control plane remains a first-class deployment for local
use and small teams. For larger production installations, the intended
direction is an optional durable orchestration backend such as Temporal. That
backend may own timers, retries, cancellation, fan-out, and recovery, but it
must not change the Definition, Trigger, Run, Job, or Worker experience. The
full boundary and migration design is tracked in
[#259](https://github.com/owainlewis/factory/issues/259) and follows the stable
local and VM Worker experience.

## Measures of progress

The product is moving toward this vision when a team can:

- connect a repository fleet without configuring each worker per repository;
- run one procedure across at least hundreds of frozen targets;
- react to a GitHub delivery without duplicate Jobs;
- add or remove local and VM capacity without changing a Job Definition;
- identify the exact instructions, input, runtime, worker, and outcome for every
  Job;
- recover or retry one failed target without replaying successful work;
- measure useful engineering outcomes rather than only process completion.

## Explicit non-goals

- General-purpose automation outside software engineering.
- A replacement for GitHub Issues, Jira, Linear, or team chat.
- Multi-step DAGs, workflow chaining, or a visual pipeline builder.
- One agent process changing several repositories atomically.
- Autonomous merge, approval, or unbounded recursive Job creation in the first
  target architecture.
- A deterministic gateway for agent GitHub actions or exactly-once external
  side effects.
