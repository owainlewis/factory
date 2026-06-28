# Architecture

Code Factory has one main idea:

```text
one important repo = one control folder
```

Each control folder contains:

- `config.yaml`: the small repo contract
- `goal.md`: what the repo is trying to become
- `automations/*.md`: prompts that agents can run on a schedule

## Primitives

### Repo Config

The config is the machine-readable contract.

It answers:

- which repo this is
- what kind of project it is
- how important it is
- what standard profile applies
- what checks matter
- how releases work
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

- global defaults
- project checklist
- standard labels
- project type profiles
- rules

Project type profiles live in `standards/profiles/`.
Rules live in `standards/rules/`.

Examples:

- `rust-cli`
- `go-cli`
- `python-library`
- `clojure-library`
- `web-app`
- `saas-app`
- `curated-list`
- `content-repo`

## Loop

```text
read config
read goal
read automation prompt
inspect repo
compare to defaults and profile
open issues for gaps
open small PRs for safe fixes
report human decisions
```

## Why This Shape

This keeps Code Factory simple.

There is no giant workflow brain.

There are only repo goals, small repo configs, standards profiles, and scheduled prompts.

That makes it easy to add power later without hiding the logic.

## Rules

A rule is one small check.

Examples:

- repo description exists
- README exists
- LICENSE exists
- CI exists
- test command is documented

Profiles list rules by ID.

Automation can evaluate the profile rules and create issues or PRs for violations.
