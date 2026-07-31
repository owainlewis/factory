# Factory documentation

Start with the root [README](../README.md) to run Factory.

## Current implementation

- [Architecture](../ARCHITECTURE.md): system boundaries, flows, contracts,
  security, limits, and source map.
- [Local guide](local.md): build, configure, start, delegate, and troubleshoot.
- [Worker contract](worker.md): identity, runtimes, claiming, process safety,
  and worktree cleanup.
- [Issue poller](poller.md): GitHub queues, provider command adapters,
  deduplication, and operation.
- [Security policy](../SECURITY.md): reporting and the current trust model.
- [Contributing](../CONTRIBUTING.md): setup, checks, and pull request standards.

## Active design work

The Workflow slice is implemented; the same document specifies its pending
typed Automation follow-on:

- [Reusable workflows and automations](workflows/design.md)
- [Unified CLI](cli/design.md)

## Superseded designs

- [External GitHub ingest](github-ingest/design.md): replaced by control-plane
  typed Automations.

Current behavior belongs in the root `ARCHITECTURE.md`. Proposed behavior belongs
in a focused design until it is implemented.
