# Software Factory vision

> **Status:** Product direction
>
> The [target design](design.md) proposes this direction. The root
> [architecture](../../ARCHITECTURE.md) continues to describe only code that
> exists today.

Factory is the control plane for software work performed by coding agents. A
developer gives Factory one work item or a repository fleet. Factory queues the
work, assigns it to available machines, supplies a consistent procedure, and
shows what is queued for capacity, running, ready, needs input, failed, or
completed under a legacy exit-based contract.

Factory does not try to become a better coding agent. Pi, Codex, Claude Code,
and future runtimes already reason about repositories, use tools, create
subagents, test changes, and open pull requests. Factory makes those agents
repeatable and operable across more work than a developer can coordinate in
terminal windows.

## Product promise

A developer can submit several software tasks, close the terminal, and return
to ready pull requests or precise questions:

```sh
factory build LINEAR-123 LINEAR-124 --repo github.com/acme/api
factory status
```

The same system can apply one reusable engineering procedure across a managed
repository fleet:

```sh
factory run bug-fix --repos all
```

Each target proceeds independently. One failure does not replay successful
siblings. Worker capacity bounds parallelism. Every agent reports progress and
its outcome through a small Factory capability instead of leaving Factory to
interpret arbitrary text.

## Core experience

Factory has four operator concepts:

- A **Procedure** is trusted, reusable engineering instruction such as the
  built-in standard Build procedure or a saved `bug-fix` procedure.
- A **Run** is one immutable invocation of a Procedure against frozen targets.
- **Work** is one independently scheduled target inside a Run. It owns one
  repository and may also own one work-item reference.
- A **Worker** is a local or remote machine that runs coding agents in isolated
  Git worktrees.

An Attempt remains internal history for one agent process. A queue is not a
separate product object. It is the set of Work waiting for Worker capacity.

```text
Work item references or repository selector
                    |
                    v
                  Run
        frozen Procedure and targets
                    |
           +--------+--------+
           v        v        v
          Work     Work     Work
        repo/item  repo     repo/item
           |        |        |
           +--------+--------+
                    v
          local and VM Workers
                    |
                    v
       Pi, Codex, Claude Code, or another agent
```

## Agent-directed work

The agent owns the meaning of the work. It decides how to refine the task,
explore the repository, plan, use subagents, implement, test, review, open a
pull request, and fix CI. Factory does not encode those activities as a
pipeline graph.

Factory injects a short procedure wrapper and an Attempt-scoped update tool:

```sh
factory update --status=running --message="Running integration tests"
factory update --status=ready --pr=<url> --message="Change ready for review"
factory update --status=needs-input --message="Which behavior is correct?"
factory update --status=failed --message="The required service is unavailable"
factory update --status=no-change --message="No concrete bug was found"
```

The agent supplies semantic judgment. Factory supplies durable state,
authorization, validation, idempotency, capacity, process supervision, and
visibility. Deterministic checks may return useful feedback to the agent, but
they do not replace its judgment about whether the software task is complete.

## Where Factory is better

An interactive coding-agent terminal remains better for exploration, difficult
product choices, and one high-touch task. Factory is better when a developer
has several buildable tasks, wants work to continue without open terminals, or
needs the same procedure applied across many repositories.

Factory succeeds when it reduces developer attention per ready pull request.
It fails when a developer still has to open, prompt, and monitor every agent
session individually.

## Product principles

1. Factory owns coordination; coding agents own engineering judgment.
2. Every invocation becomes one Run and one or more independent Work targets.
3. One Work target changes one repository by default.
4. Every Run freezes its Procedure and complete target set.
5. Agents update semantic Work state through bounded, scoped capabilities.
6. Workers retain exclusive ownership of leases, processes, and worktree
   cleanup.
7. Work-item bodies and repository content are untrusted agent context.
   Procedures are trusted operator instructions.
8. Agents use the tools and credentials available on their Worker.
9. Fleet work is bounded, observable, and retryable per target.
10. Factory adds a durable outer loop, not another model conversation loop.

## First useful version

The first version proves two paths through the same engine:

1. `factory build` admits one or more work-item references and runs the
   built-in standard Build procedure.
2. `factory run` applies a saved Procedure to one or more managed repositories.

It includes local and VM Workers, bounded concurrency, isolated worktrees,
versioned Procedure snapshots, agent progress and terminal updates, central
status, cancellation, and warned retries.

It pauses Work when an agent needs input and requeues it after an answer.
Agent-update Work finishes when it is ready for human review, has no change,
fails, or is cancelled. Unconverted legacy Work may finish as `succeeded` from
its existing exit-based contract without implying a pull request. Human merge
remains in GitHub. Central CI monitoring, automatic merge, general pipeline
graphs, and a new Factory coding agent are not required to test the product
thesis.

## Measures of progress

Factory is moving toward this vision when a developer can:

- submit at least five independent work items in one command;
- stop managing separate coding-agent conversations;
- identify every active agent, repository, branch, and latest progress update;
- receive a precise question when an agent cannot continue;
- run one Procedure across at least 100 frozen repository targets;
- retry one failed target without replaying successful siblings;
- add or remove Worker capacity without changing a Procedure;
- identify the exact Procedure, Factory-supplied context, source reference,
  runtime, Worker, updates, and outcome used for historical Work;
- measure ready pull requests and developer interventions rather than only
  process exits.

## Explicit non-goals

- A replacement for an interactive coding-agent terminal.
- A new coding agent, model loop, planner, or subagent framework.
- A replacement for GitHub, Linear, Jira, or team chat.
- A generic workflow builder, DAG, or visual pipeline editor.
- Deterministically deciding whether an agent's software work is correct.
- Exactly-once GitHub or other external side effects.
- Automatic merge in the first version.
- Public multi-user control-plane access in the first version.
