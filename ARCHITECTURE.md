# Architecture

Code Factory has two sides:

- target repos own standards and goals
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
`internal/prompt` builds built-in and repo-owned goal prompts.
`internal/agent` shells out to coding agents.
`internal/runner` connects the pieces and writes run state.

## State

Factory stores local state under `.factory-state` by default.

```text
.factory-state/
  repos/
  logs/
  runs/
```

This directory is ignored by git.

## Target Repo Shape

Recommended target repo files:

```text
AGENTS.md
STANDARDS.md
.factory/
  goals/
    standards-review.md
    triage.md
    execute.md
```

`AGENTS.md` says how agents should behave.
`STANDARDS.md` says what healthy means.
`.factory/goals/*.md` says what Factory may run.

## Agent Adapter

The first adapter is Claude Code.
It runs:

```text
claude -p --permission-mode plan <prompt>
```

The adapter captures stdout and stderr into the run log.
Later adapters can support Codex, Aider, or other local coding agents.

## Next Architecture Steps

- Add repo locks.
- Add worktrees per write run.
- Add `factory goals <repo>`.
- Add `standards-review`.
- Add label sync.
- Add daemon schedules.
