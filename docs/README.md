# Factory documentation

Start with the root [README](../README.md) to run Factory.

## Current implementation

- [Architecture](../ARCHITECTURE.md): system boundaries, flows, contracts,
  security, limits, and source map.
- [Local guide](local.md): build, configure, start, delegate, and troubleshoot.
- [Worker contract](worker.md): identity, runtimes, claiming, process safety,
  and worktree cleanup.
- [Remote VM Workers](remote-workers.md): TLS listener, one-time enrollment,
  authentication, and reconnect behavior.
- [Scheduled Automations](scheduled-automations.md): create, preview, enable,
  replay, and inspect Definition Runs across repositories.
- [GitHub webhook Automations](github-webhooks.md): configure signed
  pull-request events that start ordinary Definition Runs.
- [Product model upgrade](product-upgrade.md): freeze legacy writes, convert
  compatible schedules, and retain existing history without synthetic Runs.
- [Release guide](release.md): install, verify, upgrade, roll back, reproduce,
  and publish tagged releases.
- [Changelog](../CHANGELOG.md): user-visible changes and compatibility notes.
- [Security policy](../SECURITY.md): reporting and the current trust model.
- [Contributing](../CONTRIBUTING.md): setup, checks, and pull request standards.

## Project operations

- [Repository best-practices setup](resources/github/01-repository-best-practices.md):
  pasteable prompt for a safe, documented, agent-ready GitHub repository.
- [Issue-tracker setup](resources/github/02-issue-tracker.md): pasteable prompt
  for issue forms, labels, and a GitHub Project delivery board.

## Active design work

- [Routines and Work](routines/design.md): proposed single authoring model,
  manual and scheduled Work across repositories, final database names, and a
  reduced Overview.
- [Software Factory vision](software-factory/vision.md): product thesis, scope,
  principles, and measures of progress.

## Design records and superseded proposals

- [External GitHub ingest](github-ingest/design.md): replaced by control-plane
  typed Automations, then superseded by the target architecture.
- [Reusable workflows and automations](workflows/design.md): design record for
  the implemented Workflow and typed Automation slices; superseded by Routines
  and Work.
- [Coding automation experience](automation-experience/design.md): implemented
  Runbook-first UX record, superseded by Routines and Work.
- [Software Factory target architecture](software-factory/design.md): proposed
  Definitions, Triggers, Runs, and Jobs, superseded by Routines and Work.
- [Unified CLI](cli/design.md): useful process-boundary record whose resource
  names and command contract must be revised against Routines and Work.

Current behavior belongs in the root `ARCHITECTURE.md`. Proposed behavior belongs
in a focused design until it is implemented.
