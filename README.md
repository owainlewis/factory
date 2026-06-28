# Code Factory

Code Factory is a control plane for improving Owain's important GitHub repos.

Each repo gets:

- a config
- a goal
- one or more automations
- the same project standards

The system should stay simple.
The power comes from clear repo goals and repeatable agent work.

## Shape

```text
standards/
  defaults.yaml
  project-checklist.md
  labels.yaml
  profiles/
    rust-cli.yaml
    go-cli.yaml
    web-app.yaml
    saas-app.yaml
  rules/
    repo-description-required.yaml
    readme-required.yaml

repos/
  <repo-name>/
    config.yaml
    goal.md
    automations/
      <automation>.md

templates/
  repo/
    config.yaml
    goal.md
  automation/
    scheduled-goal.md
```

## First Principle

A repo is only in Code Factory if it matters.

If a repo matters, it should have a clear goal, consistent issues, a project board, CI, docs, and a recurring improvement loop.

Each repo config should stay small.
Put shared requirements in `standards/profiles/`.

## Factory Loop

```text
repo config -> repo goal -> standard check -> issue creation -> agent run -> PR -> review -> report
```

Read [ARCHITECTURE.md](ARCHITECTURE.md) for the first-principles model.

## Active Repos

Start small:

- `factory`
- `awesome-artificial-intelligence`
- `cortex`

Add more repos only when the loop works.

## What Code Factory Should Do

- Create and maintain standard GitHub labels.
- Ensure each active repo has a GitHub Project board.
- Ensure repo issues are linked to the right project board.
- Audit each repo against global defaults and its standards profile.
- Evaluate rules and report violations.
- Open issues for missing standards.
- Run scheduled agent goals.
- Open small PRs for safe improvements.
- Report what changed and what needs human review.

## What It Should Not Do

- Do random cleanup.
- Touch every repo at once.
- Invent product strategy.
- Auto-merge important work.
- Create public claims without proof.
- Turn old repos into active projects unless Owain chooses them.
