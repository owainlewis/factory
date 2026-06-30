# Target Repo File Model

Factory keeps the runner outside your repo. Each target repo owns its own
Factory contract under `.factory/`. These files are plain markdown, editable,
and committed to the target repo. Factory reads them; it does not own them.

## File layout

```text
.factory/
  AGENTS.md
  STANDARDS.md
  WORKFLOWS/
    standards-check.md
  OBJECTIVES/
    current-objective.md
  JOURNAL.md
```

## What each file is for

- `.factory/AGENTS.md` gives repo-specific agent instructions: how to build,
  test, and run the project, and the checks that must pass before a pull
  request.
- `.factory/STANDARDS.md` says what good looks like. Factory judges repo health
  against it.
- `.factory/WORKFLOWS/standards-check.md` is the default repeatable playbook.
  Prefer one workflow and many objectives. Add another workflow only when the
  process is truly different.
- `.factory/OBJECTIVES/` holds the current desired outcomes. In the current V1,
  Factory includes `current-objective.md` or `current.md` when present.
- `.factory/JOURNAL.md` records handoff notes between runs.

Factory owns orchestration. The target repo owns intent.

## Getting the files

Two ways to create them:

- Run `factory init` in the target repo to scaffold the files with defaults.
- Copy the templates from this repo's docs:
  - [STANDARDS.md examples](standards-examples.md)
  - [Workflow examples](workflow-examples.md)
  - [Objective examples](objective-examples.md)

Generated files are defaults. Edit them to match your project.

## Standard Factory labels

A managed repo should carry the four standard Factory labels. `factory labels
<repo>` creates any that are missing.

- `factory-ready`: an agent may work this issue now.
- `factory-triage`: the issue needs clarification, acceptance criteria, or scope shaping.
- `factory-needs-human`: the issue needs a human decision before implementation.
- `factory-blocked`: the issue cannot move until a named blocker is resolved.

## When Factory opens a pull request, opens an issue, or stops

Each run produces one outcome:

- **Pull request** when one small, safe change improves compliance and can be
  verified locally. Factory opens a draft PR and never merges it.
- **Issue** when a real gap is too large or needs scoping. Factory labels it
  `factory-ready` when it is workable as-is, or `factory-triage` when it still
  needs shaping.
- **Stop (`blocked`)** when the work needs a human decision. Factory uses
  `factory-needs-human` for product, license, or strategy calls, and
  `factory-blocked` when a named dependency must be resolved first.

## Safety rules these files must respect

- Never merge a pull request.
- Never push to the default branch.
- Never run broad cleanup.
- Never make public claims without evidence.
- Stop when the workflow or objective is unclear.
