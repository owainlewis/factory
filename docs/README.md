# Factory documentation

Start with the root [README](../README.md) to run Factory.

## Current implementation

- [Architecture](../ARCHITECTURE.md): system boundaries, flows, contracts,
  security, limits, and source map.
- [Local guide](local.md): build, configure, start, delegate, and troubleshoot.
- [Worker contract](worker.md): identity, runtimes, claiming, process safety,
  and worktree cleanup.
- [Security policy](../SECURITY.md): reporting and the current trust model.
- [Contributing](../CONTRIBUTING.md): setup, checks, and pull request standards.

## Proposed designs

These documents describe work that is not implemented:

- [Reusable workflows and automations](workflows/design.md)
- [GitHub issue ingest](github-ingest/design.md)
- [Unified CLI](cli/design.md)

Current behavior belongs in the root `ARCHITECTURE.md`. Proposed behavior belongs
in a focused design until it is implemented.
