# Security

## Reporting

Do not open a public issue for an unpatched vulnerability. Email
[owain@owainlewis.com](mailto:owain@owainlewis.com) with the affected revision,
impact, reproduction steps, and any suggested mitigation. Do not include real
credentials or private repository data.

You should receive an acknowledgement within seven days. The maintainer will
coordinate remediation and disclosure after the report is understood.

## Supported versions

Factory is under active development. Only the latest commit on `main` receives
security fixes.

## Current trust model

Factory is a local control plane. It binds to loopback, has no authentication,
and must not be exposed directly to a network.

A worker can:

- run Codex or Claude Code with the worker host user's permissions;
- read and change its configured repositories;
- create Git branches and worktrees;
- call tools available to the selected agent runtime.

Treat worker hosts and repository allowlists as trusted infrastructure. Do not
register a repository that the runtime should not be able to modify.
The allowlist controls Factory assignment and worktree creation. It does not
sandbox the agent from other files or tools available to the worker OS user.

The control plane validates loopback addresses, worker leases, repository
assignments, event sizes, and state transitions. Workers validate owned
worktrees before cleanup and preserve branches that may contain unpublished
work.

## Local data

Factory state defaults to `~/.factory`. Protect this directory because it may
contain:

- task prompts and execution events;
- polled ticket bodies and pending task requests;
- worker identity and disposal records;
- repository paths and branch names;
- retained worktrees with unpublished changes.

Worker configuration should use mode `0600`. Data directories should not be
shared between worker identities.

Provider CLIs own their credentials. Factory does not request or persist
provider tokens. Treat configured poller commands and queue prompts as trusted
operator policy. Ticket titles and bodies are untrusted input passed to the
agent as labelled context.

See the [architecture](ARCHITECTURE.md) and
[worker guide](docs/worker.md) for the complete boundary.
