# Factory

[![CI](https://github.com/owainlewis/factory/actions/workflows/ci.yml/badge.svg)](https://github.com/owainlewis/factory/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-2f6feb.svg)](LICENSE)

Factory keeps coding agents working on a repository without making a human
orchestrate every step from a terminal.

It watches a trusted ticket queue. When a configured condition matches, Factory
creates a durable task, prepares an isolated workspace, and gives one Markdown
workflow to an agent. The agent uses normal tools such as `gh` and `git` to do
the work. When nothing matches, Factory does nothing and spends no model tokens.

![AI-native development cycle: Intake, Spec, Task, Build, Validate, Review, Merge, Learn, then feedback returns to Intake](docs/assets/readme/factory-loop.svg)

## Why Factory exists

Coding agents can implement increasingly substantial changes, but most teams
still operate them as one-off terminal sessions. Every developer uses different
prompts, skills, checks, and handoff conventions. Humans remain responsible for
noticing ready work, starting an agent, waiting for CI, forwarding review
feedback, and remembering to try again.

Factory makes this process repeatable. It plays a similar role to CI/CD: work
enters a consistent system, receives the same checks and feedback loops, and
keeps moving until it reaches a human decision.

The goal is not to replace developers. Humans decide what matters, supply
product context, review the result, and remain accountable for what ships.
Factory removes the manual coordination between those decisions.

## The ticket is the control plane

The issue tracker is where humans and agents coordinate. A ticket records the
problem, scope, acceptance criteria, decisions, status, and evidence. Moving a
ticket into a configured state is an explicit request for an agent pass.

This makes ticket quality load-bearing. A vague ticket is not ready for either a
human or an agent. A triage workflow can inspect the codebase, reproduce the
problem, clarify scope, add testable acceptance criteria, and ask for the
smallest missing human decision. Once the ticket is clear, it becomes the spec
for implementation.

![A ticket moves from specification through implementation and review](docs/assets/readme/ticket-workflow.svg)

The status names in this example are not built into Factory. They are ordinary
issue labels and repository-owned prompts. You may also track them on a
GitHub Project board for your own visualization; Factory does not read that
board.

## A deliberately small model

Factory has four concepts:

| Concept | Responsibility |
| --- | --- |
| Source | The ticket queue and control plane. GitHub in v1. |
| Trigger | A status, label, or schedule condition. |
| Workflow | A plain Markdown prompt describing the outcome and policy. |
| Worker | The agent runtime, sandbox, timeout, and concurrency limit. |

The boundary is intentional:

- Factory owns polling, trust checks, deduplication, durable claims,
  concurrency, timeouts, sandbox lifecycle, supervision, cancellation, history,
  and recovery.
- The workflow and agent own adaptive engineering work: reading the issue,
  inspecting code, clarifying requirements, implementing changes, using `gh`
  and `git`, opening a pull request, responding to CI and review, and updating
  the ticket.

Factory does not encode a fixed SDLC, a workflow graph, or deterministic GitHub
effects. A trigger means only: **when this condition is true, run this prompt**.

## Human review is the shipping boundary

Factory revalidates live source state immediately before execution, but does
not filter tickets by author. The trust boundary for a source trigger is
whoever can satisfy its configured condition, such as applying a label or
changing a Project status, not who opened the ticket. Do not use a source whose
configured condition can be changed by untrusted people. Ticket bodies,
comments, linked pull requests, and attachments remain untrusted input
regardless. Use narrow credentials and protected branches that the worker
cannot bypass.

Factory-created software pull requests remain for human review. Factory and its
default workflows never merge them or enable automatic merge. The human who
merges remains accountable for what ships.

For the complete trust and isolation model, read the
[operations guide](docs/operations.md) and [security policy](SECURITY.md).

## Get started

Install Rust, Git, the GitHub CLI, and the Codex CLI, then authenticate the host
tools and install Factory:

```sh
gh auth login
codex login
cargo install --path . --locked
```

From the repository Factory will manage:

```sh
factory init
```

Edit the generated configuration and workflows for your repository, then
validate them and start Factory:

```sh
factory validate
factory run --once
factory run
```

The [runnable guide](docs/local-v1.md) covers the complete configuration, source
contract, first demonstration, and sandbox setup. The
[operations guide](docs/operations.md) covers inspection, cancellation,
recovery, and cleanup.

## V1 scope

V1 intentionally supports:

- one repository and one GitHub source;
- status, label, and schedule triggers;
- Codex workers in managed worktrees or Docker clones;
- explicit Markdown workflows;
- durable queueing, supervision, history, cancellation, and recovery.

Jira, Linear, multiple repositories, other agent runtimes, hosted workers, and
webhook wake-ups can fit behind the same source, trigger, workflow, and worker
boundaries later. This repository includes a [Jira source adapter](docs/jira.md)
as an example of that extension point, but Jira is not part of the supported V1
scope.

## Learn more

- [Vision and technical design](docs/design.md)
- [Labels and ticket status](docs/labels.md)
- [Setup, configuration, and first run](docs/local-v1.md)
- [Operations and recovery](docs/operations.md)
- [Jira source adapter](docs/jira.md)
- [Docker Sandbox development environment](docs/docker-sandbox-template.md)
- [Contributing](CONTRIBUTING.md)
- [Security](SECURITY.md)

## License

Factory is available under the [MIT License](LICENSE).
