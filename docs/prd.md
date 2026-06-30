# Factory PRD

## Vision

Every software project should have a permanent autonomous engineering team.

Humans define direction.

Factory ensures that work continuously happens.

The objective is not to automate coding.

The objective is to automate software engineering.

## Problem

Coding agents are extremely capable.

What they lack is process.

Today a developer repeatedly tells an agent:

- what repository to work on
- what standards to follow
- what workflow to use
- what issue to fix
- what to verify
- when to stop

The agent starts from scratch every session.

The project only moves forward when a human starts another conversation.

The bottleneck is no longer writing code.

The bottleneck is continuously deciding what should happen next.

## Solution

Factory is a local runtime for autonomous engineering.

It manages many repositories.

For each repository it:

- checks out the latest code
- gathers project context
- selects the appropriate engineering workflow
- generates a complete prompt
- dispatches a coding agent
- records the outcome
- repeats

Factory is not the engineer.

Factory runs engineers.

## Factory Standard

Factory includes a default standard for professional software projects.

The standard captures the questions a senior engineer asks of any repo:

- what is this project
- can a new person use it
- can it build
- can it test
- does CI run
- is the code reviewable
- are docs accurate
- can it release
- is it secure enough for its purpose
- is ownership clear
- can agents work safely

These buckets are generic.
The answers are language-specific.

Example:

```text
Testing bucket

Go:
go test ./...

OCaml:
dune runtest

Node:
npm test
```

Factory should use this standard to bootstrap repo-owned `.factory/STANDARDS.md` and one default `.factory/WORKFLOWS/standards-check.md` file.
After bootstrap, the target repo owns the final standard.

## Philosophy

Factory separates deterministic work from reasoning.

Factory performs deterministic orchestration.

Agents perform reasoning.

```text
Factory
    |
Planning agent
    |
Execution agent
    |
Verification agent
```

Factory never decides what code to write.

Factory asks agents to decide inside well-defined engineering workflows.

## Repository Model

Every managed repository defines how engineering should happen.

```text
.factory/
  AGENTS.md
  STANDARDS.md
  WORKFLOWS/
    standards-check.md
  OBJECTIVES/
  JOURNAL.md
```

### .factory/AGENTS.md

General instructions for coding agents.

Examples:

- repository overview
- coding conventions
- project structure
- important constraints

### .factory/STANDARDS.md

Defines what good looks like.

Examples:

- coding standards
- testing requirements
- documentation standards
- verification commands
- merge policy

Standards change rarely.

### .factory/WORKFLOWS

Engineering playbooks.

Start with one workflow:

```text
standards-check.md
```

That workflow describes the common loop: compare the repo to its standards, plan one small improvement, and execute one safe change when allowed.

Factory does not invent process.

It follows repository-owned SOPs.
Add another workflow only when the process is truly different, not just because the objective is different.

### .factory/OBJECTIVES

Repo-owned work orders.

Examples:

```text
2026-06-29-release-readiness.md
2026-06-29-docs-audit.md
2026-06-29-ci-hardening.md
```

An objective describes the desired outcome for a specific run or short sequence of runs.

Objectives can be broad:

- create this project from scratch
- make the project release-ready
- find and fix documentation gaps
- improve CI and testing

Objectives should name:

- goal
- context
- scope
- done conditions
- selected workflow
- runtime mode
- stop rules

Workflows are reusable process.
Objectives are current intent.

### .factory/JOURNAL.md

Append-only engineering handover.

Each run records:

- what happened
- what decisions were made
- what the next run should know

The journal is continuity, not memory.

## Managed Repositories

Factory manages many repositories.

```text
factory repos

awesome-ai
scheme-rs
factory
passage
slate
```

Factory keeps local checkouts up to date.

Each repository is processed independently.

## Core Loop

```text
Load managed repositories

|

For each repository

|

Fetch latest code

|

Run planning workflow

|

Execute returned work

|

Verify

|

Update GitHub

|

Append journal

|

Next repository
```

## Planning

Planning is an agent workflow.

Factory starts the planning workflow.

The planning agent decides what work should happen.

Planning reads:

- repository
- standards
- workflows
- objectives
- journal
- GitHub issues
- pull requests
- current objective

The planning agent returns work items.

Examples:

- triage issue #15
- execute issue #42
- update documentation
- perform standards review
- nothing to do

Factory simply executes the plan.

## Execution

Each work item selects a workflow.

Example:

Issue:

```text
Parser crashes on nested lists.
```

Planning decides:

```text
Workflow:
standards-check
```

Factory loads:

```text
.factory/WORKFLOWS/standards-check.md
```

Factory builds the prompt:

```text
Read .factory/STANDARDS.md.

Read .factory/AGENTS.md.

Read the journal.

Read issue #42.

Read .factory/OBJECTIVES/current-objective.md.

Follow .factory/WORKFLOWS/standards-check.md.
```

The coding agent performs the work.

## Objectives And Goals

Factory treats objectives as repo-owned input.

Under the hood, Factory compiles an objective into an agent goal.

```text
workflow = repeatable process
objective = current desired outcome
goal = runtime instruction sent to the coding agent
```

Example objective:

```md
# Objective: Release readiness

## Goal

Make this project releasable by a new user.

## Scope

- README install, build, test, and run sections
- GitHub Actions CI
- CHANGELOG.md
- docs/releasing.md

## Done

- one focused draft PR is opened
- relevant checks have run
- remaining gaps are listed

## Workflow

Use `.factory/WORKFLOWS/standards-check.md`.

## Stop Rules

- Do not publish a release.
- Do not change the license.
- Do not push to the default branch.
```

Planned command:

```bash
factory objective <repo> <objective> --mode plan

factory objective <repo> <objective> --mode execute
```

## Example Workflow

Bug Fix

```text
Read issue.

Determine whether reproduction is possible.

Write a failing test.

Verify failure.

Implement the smallest fix.

Run verification.

Open PR or merge according to policy.

Update issue.

Append journal.
```

Issue Triage

```text
Read issue.

Determine if duplicate.

Determine if clarification is required.

Apply labels.

Close invalid issues.

Mark ready issues.

Append journal.
```

Documentation

```text
Inspect documentation.

Update docs.

Run markdown checks.

Merge if policy allows.

Append journal.
```

The workflows belong to the repository.

Factory simply executes them.

## Prompt Compilation

Factory's primary responsibility is compiling context.

Every run builds a prompt from:

- repository checkout
- .factory/AGENTS.md
- .factory/STANDARDS.md
- selected .factory/WORKFLOWS workflow
- selected .factory/OBJECTIVES objective
- .factory/JOURNAL.md
- GitHub issue
- runtime mode

The coding agent receives complete engineering context.

The agent does not need to guess the process.

## Runtime Modes

Examples:

```bash
factory plan scheme

factory execute scheme

factory triage scheme

factory daemon
```

Factory supports both manual and scheduled execution.

Scheduling is an implementation detail.

## Agent Independence

Factory is agent-neutral.

It should work with any coding agent.

Examples:

- Claude Code
- Codex
- Pi
- Aider
- future coding agents

Factory owns:

- repository management
- workflow selection
- prompt compilation
- orchestration

The coding agent owns reasoning.

## Long-Term Vision

Factory is not another coding agent.

Factory is the runtime that gives every repository an autonomous engineering team.

Projects define:

- standards
- engineering workflows
- objectives

Factory continuously supplies engineering effort.

Instead of repeatedly prompting an agent, software projects continuously improve themselves through repeatable engineering workflows executed by autonomous coding agents.
