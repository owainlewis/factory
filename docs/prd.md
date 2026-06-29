# Code Factory PRD

## Vision

Code Factory is a local-first system for keeping many GitHub repositories healthy with coding agents.
It should feel like a careful developer is always available to inspect repos, open useful issues, make small fixes, run checks, and prepare PRs for human review.

Factory does not replace judgment.
It turns clear repo standards and goals into steady, reviewable work.

## Mission

Factory helps one person maintain many projects without turning every project into manual upkeep.
It should make old docs, missing licenses, weak CI, stale issues, and small bugs visible and fixable.
It should do that through issues and PRs, not hidden background mutation.

## Model

Each target repo owns its intent:

```text
AGENTS.md
STANDARDS.md
.factory/
  goals/
    standards-review.md
    triage.md
    execute.md
```

Factory owns execution:

```text
config.yaml
cmd/factory
internal runner code
run logs
run records
agent adapters
```

The runner clones or updates repos locally, reads repo-owned goals, starts a coding agent in the checkout, captures logs, and records the result.

## MVP V1

V1 proves the runner spine:

```text
config -> clone or fetch repo -> build prompt -> shell out to Claude Code -> save log -> save run record
```

V1 supports:

- one config file
- many registered repos
- local checkout under `.factory-state/repos`
- `factory repos`
- `factory run <repo> hello`
- `factory runs`
- Claude Code as the first adapter
- no-edit hello smoke prompt
- JSON run records
- text logs

V1 does not yet support:

- daemon scheduling
- repo locks
- worktrees
- issue selection
- PR creation
- standard label sync
- GitHub Project automation
- multiple agent adapters

## Safety

Factory must not merge PRs.
Factory must not push to default branches.
Factory must prefer small, reviewable work.
Factory must stop when standards or goals require human judgment.
Factory must log every run.

## Next Milestones

1. Run a no-edit Claude Code smoke test in a cloned repo.
2. Run a repo-owned `.factory/goals/standards-review.md` goal.
3. Add repo locks and run state.
4. Add standard Factory label sync.
5. Add `triage` to open useful issues.
6. Add `execute` to work `factory-ready` issues.
7. Add daemon schedules.
