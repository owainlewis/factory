# Architecture

Factory has two sides:

- target repos own standards, workflows, objectives, and journals
- Factory owns local execution

The system should stay boring on disk and powerful in execution.

## V1 Flow

```text
factory run cortex hello
load config.yaml
resolve cortex repo
clone or fetch the repo under .factory-state/repos
build a prompt
start Claude Code in the repo
write a log
write a run record
```

## Main Packages

```text
cmd/factory
internal/config
internal/gitrepo
internal/prompt
internal/agent
internal/runner
```

`internal/config` loads the local registry.
`internal/gitrepo` clones or updates repos.
`internal/prompt` compiles repo context and workflow prompts.
`internal/agent` shells out to coding agents.
`internal/runner` connects the pieces and writes run state.

## Registry

`config.yaml` is only a local runner registry.
It should contain the minimum data needed to clone and run a repo:

```yaml
repos:
  cortex:
    url: git@github.com:owainlewis/cortex.git
    branch: main
    agent: claude
```

It should not duplicate target repo standards, checks, issue labels, journals, or workflows.

## State

Factory stores local state under `.factory-state` by default.

```text
.factory-state/
  repos/
  worktrees/
  logs/
  runs/
  locks/
```

This directory is ignored by git.
The `repos/` directory is Factory-owned internal state, not a human workspace.
Factory may fetch, checkout, and update these clones.
Runner commands take a per-repo lock before touching a managed clone.
Execute-mode runs use per-run worktrees.

## Target Repo Shape

Recommended target repo files:

```text
.factory/
  AGENTS.md
  STANDARDS.md
  WORKFLOWS/
    standards.md
    github.md
    docs.md
    ci.md
    release.md
  OBJECTIVES/
    2026-06-29-release.md
  JOURNAL.md
```

`.factory/AGENTS.md` says how agents should behave.
`.factory/STANDARDS.md` says what healthy means.
`.factory/WORKFLOWS/*.md` says how a class of engineering work should run.
`.factory/OBJECTIVES/*.md` says what outcome is wanted now.
`.factory/JOURNAL.md` carries append-only handover notes between runs.

Factory compiles objectives into agent goals.

```text
workflow = repeatable process
objective = current desired outcome
goal = runtime prompt sent to the coding agent
```

## Agent Adapter

The first adapter is Claude Code.
In plan mode, it runs:

```text
claude -p --permission-mode plan <prompt>
```

In execute mode, it runs:

```text
claude -p --permission-mode auto <prompt>
```

The adapter captures stdout and stderr into the run log.
Later adapters can support Codex, Aider, or other local coding agents.

## Next Architecture Steps

- Add verification mode.
- Add journal appends.
- Add label sync.
- Add daemon schedules.
