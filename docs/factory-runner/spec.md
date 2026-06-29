# Factory Runner Spec

## What

Factory is a local-first runner for keeping many GitHub repos healthy with coding agents.
Each target repo owns its own standards and goals.
Factory discovers those repos, runs selected goals on a schedule or on demand, starts the configured coding agent, records results, and never merges.

## Context

Factory currently stores a local runner registry and a Claude Code adapter.
The first operating loop is:

```text
GitHub issue -> agent work -> tests -> self-review -> PR -> human decision
```

The next model should reduce central config and move intent into each target repo.
The target repo should say what healthy means and which goals exist.
Factory should do the control work: scheduling, locking, running agents, logging, and guarding unsafe actions.

## Requirements

- A target repo may define `STANDARDS.md`.
- A target repo may define goals under `.factory/goals/*.md`.
- A goal exists only when the markdown file exists in the target repo.
- Factory must support one-shot runs.
- Factory must support a long-running daemon.
- Factory must support many projects.
- Factory must lock a repo before running a goal in that repo.
- Factory must not run two write-capable goals against one repo at the same time.
- Factory must keep standard Factory labels consistent across repos.
- Factory must never merge PRs.
- Factory must never push to default branches.
- Factory must record each run with status, timestamps, repo, goal, agent, log path, and result.
- Factory must stop when a goal needs human input.

## Standard Factory Labels

Factory labels are global across all repos.
Factory behavior may depend on these labels.
Project-specific labels may still exist, but Factory should not require them.

Required labels:

- `factory-ready`: an agent may work this issue now.
- `factory-triage`: the issue needs clarification, acceptance criteria, or scope shaping.
- `factory-needs-human`: the issue needs a human decision before implementation.
- `factory-blocked`: the issue cannot move until a named blocker is resolved.

Factory must not add more required state labels without a spec update.
GitHub Projects, PR state, and run records should carry workflow state such as in progress, reviewing, and done.

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

`AGENTS.md` defines agent behavior and repo-specific rules.
`STANDARDS.md` defines what healthy means for the repo.
`.factory/goals/*.md` defines runnable Factory goals.

Factory should ship templates for these files, but the target repo owns its copies.

## Built-In Default Goal

The default Factory goal is `standards-review`.

The goal:

```text
Read STANDARDS.md.
Check the repo against it.
For each failed standard, open the smallest safe PR or create an issue.
Stop before merge.
```

Classification rule:

- `fix`: open the smallest safe PR.
- `issue`: open or update a focused issue.
- `blocked`: report the missing human decision or permission.

Factory should prefer PRs for mechanical fixes.
Factory should prefer issues for judgment calls.

Examples:

- Missing `LICENSE`, and `STANDARDS.md` says MIT: open a PR.
- Missing `LICENSE`, and no license is named: open an issue or block for human input.
- Missing CI, and test commands are clear: open a PR.
- Docs conflict with code in a small clear way: open a PR.
- Docs conflict with an unclear product decision: open an issue.
- Missing Factory labels: create the labels when permissions allow.
- Missing GitHub Project membership: add it when permissions and project rules are clear, otherwise open an issue.

## Runner Commands

Minimum CLI:

```sh
factory repos
factory goals <repo>
factory run <repo> <goal>
factory daemon
factory runs
```

Examples:

```sh
factory run cortex standards-review
factory run cortex triage
factory run cortex execute
factory daemon
```

`factory run <repo> <goal>` runs one goal once.
`factory daemon` watches configured repos and schedules eligible goals.

## Project Registry

Factory needs a small local registry for repos.
The registry answers where repos live and which agent adapter to use.
It should not duplicate goals or standards.

Example:

```yaml
repos:
  cortex:
    url: git@github.com:owainlewis/cortex.git
    branch: main
    agent: claude
```

Assumption: a small YAML file is acceptable for local runner state.
The target repo should still own standards and goals in markdown.
The registry must not contain target repo checks, issue labels, product purpose, or runnable project goals.

## Scheduling

Schedules belong to the runner, not the target repo, for the first version.
This keeps target repos simple and lets one operator choose how aggressive Factory should be.

Example:

```yaml
schedules:
  - repo: cortex
    goal: standards-review
    every: 24h
  - repo: cortex
    goal: triage
    every: 12h
  - repo: cortex
    goal: execute
    every: 1h
    requires_label: factory-ready
```

Daemon rules:

- Run at most one goal per repo at a time.
- Prefer lower-risk goals before higher-risk goals.
- Do not start `execute` if there are no `factory-ready` issues.
- Do not start a new run if the previous run for the same repo and goal is still active.
- Back off after repeated failures.
- Record blocked runs instead of retrying forever.

Risk order:

1. `standards-review`
2. `triage`
3. `execute`

## Agent Adapters

Factory should be agent-neutral.
Adapters start a coding agent in a repo with a goal prompt and capture output.

Initial adapter:

- `claude`

Future adapters:

- `codex`
- `aider`
- `cursor`

Adapter contract:

- Accept repo path.
- Accept goal markdown.
- Accept run metadata.
- Start the agent in the target repo.
- Capture logs.
- Return status: success, blocked, failed, or cancelled.
- Return result details such as PR URL, issue URL, or blocker text when available.

## Run State

Factory should store run state locally first.

Suggested layout:

```text
.factory-state/
  runs/
    <run-id>.json
  logs/
    <run-id>.log
  locks/
    <repo>.lock
```

Run record fields:

- run id
- repo name
- repo path
- goal name
- goal file
- agent
- status
- started at
- finished at
- log path
- branch, if created
- PR URL, if opened
- issue URL, if opened
- blocker, if blocked

Statuses:

- `queued`
- `running`
- `success`
- `blocked`
- `failed`
- `cancelled`

## Design

Factory has four core modules:

- repo registry
- goal loader
- scheduler
- agent adapter

Data flow:

```text
registry -> repo path -> goal loader -> scheduler -> repo lock -> adapter -> run record
```

One-shot run flow:

```text
factory run cortex standards-review
load registry
resolve cortex path
read cortex/.factory/goals/standards-review.md, or use built-in default if allowed
take cortex repo lock
start codex adapter
record logs and result
release lock
```

Daemon flow:

```text
factory daemon
load registry and schedules
loop
  find due schedules
  skip locked repos
  skip execute when no factory-ready issue exists
  start eligible run
  record result
  sleep
```

## Decisions

Choice: goals live in target repos.
Alternative: goals live centrally in Factory.
Why: repo-owned goals make the behavior visible, reviewable, and OSS-friendly.
Reversible: yes, Factory can later support inherited central templates.

Choice: Factory labels are global.
Alternative: each repo defines its own ready and blocked labels.
Why: the runner needs a common queue language across repos.
Reversible: partly, aliases can be added later.

Choice: schedules live in the local Factory registry first.
Alternative: schedules live in target repos.
Why: schedules are operator policy, not repo health policy.
Reversible: yes, repo-owned schedules can be added later.

Choice: first state store is local files.
Alternative: SQLite.
Why: file state is easy to inspect and enough for one local daemon.
Reversible: yes, run records can migrate to SQLite.

Choice: one repo lock prevents concurrent write goals.
Alternative: allow many agents per repo.
Why: simple locking prevents branch, worktree, and PR confusion in the MVP.
Reversible: yes, read-only goals can later run concurrently.

## Invariants

- Factory never merges.
- Factory never pushes to default branches.
- Factory never runs a missing repo-local goal unless an explicit built-in default is allowed.
- Factory never runs two write-capable goals in the same repo at once.
- Factory labels keep the same meaning across repos.
- Target repos own standards and goals.
- Factory records enough evidence to explain what happened.

## Error Behavior

- Missing repo path: mark run failed and report the missing path.
- Missing goal file: mark run failed unless a built-in default is explicitly allowed.
- Missing `STANDARDS.md` for `standards-review`: mark blocked and ask for standards.
- Repo already locked: skip and retry later.
- GitHub auth missing: mark blocked and report required auth.
- Agent command missing: mark blocked and report missing adapter dependency.
- Agent exits non-zero: mark failed with log path.
- Same blocker repeats: stop retrying until human input changes the state.

## Testing Strategy

- Unit test registry loading.
- Unit test goal discovery.
- Unit test standard label definitions.
- Unit test schedule due logic.
- Unit test lock acquire and release.
- Unit test run record writes.
- Integration test `factory goals <repo>` against a fixture repo.
- Integration test `factory run <repo> standards-review --dry-run` against a fixture repo.
- Adapter tests may use a fake adapter before invoking real coding agents.

## Out of Scope

- Hosted service.
- Remote VM deployment.
- Auto-merge.
- Multi-agent concurrency inside one repo.
- Full GitHub Project automation.
- Web dashboard.
- Agent-specific prompt tuning beyond the adapter contract.
