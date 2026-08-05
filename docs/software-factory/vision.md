# Software Factory vision

> **Status:** Product direction

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
- create issues, comments, branches, and pull requests under an explicit action
  policy;
- run scheduled maintenance and react to GitHub events.

Every use case becomes the same execution primitive. A **Job** runs one agent
against one repository and optional work item. A **Run** invokes one saved
**Job Definition** and may fan out into many independent Jobs.

## Core experience

An operator starts with a Job Definition such as `Review pull request`, `Triage
issue`, or `Find dependency risks`. The definition contains the trusted
instructions, runtime needs, execution defaults, and allowed external actions.

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
        +-- GitHub webhook or query trigger
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
       |
       v
    Attempt
       |
       v
 Runner on a local host, VM, or Kubernetes cluster
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
6. Runners supply compute. Runtimes such as Codex and Claude Code supply agent
   behavior.
7. Provider payloads and repository content are untrusted inputs. Job
   instructions and action policy are trusted operator configuration.
8. External writes require explicit capabilities. Read and report is the
   default.
9. Fleet-wide work is bounded, fair, observable, and retryable per target.
10. Product concepts must earn their place through operator behavior, not
    speculative reuse.

## Differentiation

Factory focuses on software delivery execution:

- Git repository fleets and work-item targets;
- isolated worktrees, branches, commits, and pull requests;
- reliable agent scheduling, leases, attempts, and recovery;
- local, VM, and Kubernetes capacity;
- reusable engineering procedures;
- manual, scheduled, event, and API admission;
- throughput, reliability, cost, and outcome visibility.

Factory does not aim to own human project management, chat, inboxes, agent
personas, squads, a generic workflow builder, or business-process automation.
GitHub and other engineering systems remain the source of issues, pull
requests, reviews, and repository state.

## Measures of progress

The product is moving toward this vision when a team can:

- connect a repository fleet without configuring each runner per repository;
- run one procedure across at least hundreds of frozen targets;
- react to a GitHub delivery without duplicate Jobs;
- add or remove local, VM, and Kubernetes capacity without changing a Job
  Definition;
- identify the exact instructions, input, permissions, runtime, runner, and
  outcome for every Job;
- recover or retry one failed target without replaying successful work;
- measure useful engineering outcomes rather than only process completion.

## Explicit non-goals

- General-purpose automation outside software engineering.
- A replacement for GitHub Issues, Jira, Linear, or team chat.
- Multi-step DAGs, workflow chaining, or a visual pipeline builder.
- One agent process changing several repositories atomically.
- Autonomous merge, approval, or unbounded recursive Job creation in the first
  target architecture.
