# Architecture

Code Factory has one main idea:

```text
one important repo = one control folder
```

Each control folder contains:

- `config.yaml`: what the repo is, what standards apply, and what work is allowed
- `goal.md`: what the repo is trying to become
- `automations/*.md`: prompts that agents can run on a schedule

## Primitives

### Repo Config

The config is the machine-readable contract.

It answers:

- where the repo lives
- whether it is active
- what role it plays
- whether issues are required
- whether a project board is required
- whether CI is required
- which automations run
- what requires human review

### Repo Goal

The goal is the human-readable north star.

It tells an agent what better means for that repo.

### Automation Prompt

An automation is just a markdown prompt with a schedule and runner.

The runner can be Codex, Claude Code, or another agent later.

The prompt should be complete enough that a fresh agent can run it without extra context.

### Standards

Standards are shared across repos.

The first standards are:

- project checklist
- standard labels

## Loop

```text
read config
read goal
read automation prompt
inspect repo
compare to standards
open issues for gaps
open small PRs for safe fixes
report human decisions
```

## Why This Shape

This keeps Code Factory simple.

There is no giant workflow brain.

There are only repo goals, repo configs, standards, and scheduled prompts.

That makes it easy to add power later without hiding the logic.

