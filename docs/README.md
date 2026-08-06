# Factory documentation

Start with the root [README](../README.md) to run Factory.

## Current implementation

- [Architecture](../ARCHITECTURE.md): system boundaries, flows, contracts,
  security, limits, and source map.
- [Local guide](local.md): build, configure, start, delegate, and troubleshoot.
- [Worker contract](worker.md): identity, runtimes, claiming, process safety,
  and worktree cleanup.
- [Scheduled Automations](scheduled-automations.md): create, preview, enable,
  replay, and inspect Definition Runs across repositories.
- [Release guide](release.md): install, verify, upgrade, roll back, reproduce,
  and publish tagged releases.
- [Changelog](../CHANGELOG.md): user-visible changes and compatibility notes.
- [Security policy](../SECURITY.md): reporting and the current trust model.
- [Contributing](../CONTRIBUTING.md): setup, checks, and pull request standards.

## Active design work

- [Coding automation experience](automation-experience/design.md): current
  Runbook-first UX and proposed multi-repository Run model.
- [Software Factory vision](software-factory/vision.md): product thesis, scope,
  principles, and measures of progress.
- [Software Factory target architecture](software-factory/design.md): proposed
  Definitions, Triggers, Runs, Jobs, Attempts, Runners, GitHub admission, and
  migration.

## Design records and superseded proposals

- [External GitHub ingest](github-ingest/design.md): replaced by control-plane
  typed Automations, then superseded by the target architecture.
- [Reusable workflows and automations](workflows/design.md): design record for
  the implemented Workflow and typed Automation slices; superseded for future
  product work by the target architecture.
- [Unified CLI](cli/design.md): useful process-boundary record whose resource
  names and command contract must be revised against the target architecture.

Current behavior belongs in the root `ARCHITECTURE.md`. Proposed behavior belongs
in a focused design until it is implemented.
