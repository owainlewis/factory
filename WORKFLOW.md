# Workflow

Factory uses [GitHub Issues](https://github.com/owainlewis/factory/issues) as
the source of truth for planned work. The
[Factory project](https://github.com/users/owainlewis/projects/16) records where
each issue is in its lifecycle.

## Status

Every active issue has one status. Status describes what should happen next,
not the kind of work in the issue.

### Todo

Work we intend to do but have not prioritised or refined. A Todo issue may
still need evidence, scope, acceptance criteria, dependencies, or a decision
about whether it is worth doing.

Move an issue to Ready only after it has been reviewed, prioritised, and made
clear enough to start.

### Ready

Work that an agent or human can pick up without further clarification. A Ready
issue has a clear outcome, bounded scope, acceptance criteria, and practical
verification steps. Important dependencies and risks are recorded.

Move an issue to In Progress when someone starts work, not when they merely
intend to start.

### In Progress

Work that has started and has an active owner. The owner is implementing the
change and running the checks needed to prove it works.

Keep work in this status while it is actively being changed. If work stops and
has no active owner, return it to Ready. Blocking is the exception: if an
external dependency prevents progress, keep the issue in its current status and
add `blocked`. Remove `blocked` and apply the normal status rules when progress
can resume.

### Review

Completed work undergoing code review and final verification. For code changes,
the pull request is open, ready for review, and its author has run the relevant
checks. Changes requested during review stay here unless implementation resumes
as the main activity.

Add `needs-human-review` only when the work specifically requires human
judgement or approval. Review may otherwise be performed by an agent or a
human.

### Done

Finished work. The acceptance criteria have been met, verification has passed,
review is complete, and the change is merged or otherwise delivered. Close the
issue when it reaches Done.

An issue closed without implementation also moves to Done and uses `duplicate`,
`invalid`, or `wontfix` to record why.

## Labels

Labels add information that can apply in any status. They do not represent
lifecycle, priority, effort, ownership, agent readiness, or merge readiness.
Those belong in the project fields, assignees, and status.

Use labels only when they make a useful filter or route work to someone.

### Type

Use at most one primary type label when it adds useful context:

- `bug`: existing behaviour is broken or unsafe.
- `enhancement`: new or improved product or engineering behaviour.
- `documentation`: a documentation-only change.

An issue does not need a type label when none adds useful information.

### Attention

These labels can apply alongside any type:

- `blocked`: progress depends on an external decision, change, or resource. The
  issue explains the blocker. While this label is present, status records the
  lifecycle stage where work will resume rather than claiming active progress.
- `needs-human-review`: progress or completion requires human judgement or
  approval that an agent cannot provide.

### Resolution

Use one of these when closing an issue without delivering the requested work:

- `duplicate`: another issue represents the work.
- `invalid`: the reported problem or request does not apply.
- `wontfix`: the issue is understood but will not be pursued.

Link the duplicate or explain the closure before closing the issue.

### Contribution and automation

`good first issue` and `help wanted` are optional contributor-discovery labels.
Automated pull requests may use `dependencies` and scope labels such as `go`,
`javascript`, or `github_actions`. These labels describe the change and never
drive its lifecycle.

`factory:ready` is a temporary machine-trigger label. Factory's GitHub issue
automation can filter labels but cannot read the project Status field. Apply
`factory:ready` to a Ready issue only when it should enter an automation that
requires the label. The issue's project status remains the source of truth.
Retire this compatibility label only after affected automations can consume
project status or have been disabled or migrated.

## Labels to retire

Do not add new uses of these labels:

- `factory:ready-for-spec` and `factory:ready-to-implement`, because project
  status owns those lifecycle stages.
- `ready-to-merge`, because Review owns the review phase and
  `needs-human-review` identifies required human attention.
- `priority:p1`, `priority:p2`, and `priority:p3`, because the project Priority
  field owns priority.
- `factory:needs-review`, after its active uses have moved to
  `needs-human-review`.
- `rust` and `question`, because they do not describe current repository work.

Retiring a label means migrating active issues or pull requests before deleting
it. Historical issues and pull requests do not need to be rewritten unless the
old label causes confusion.

## Working rules

1. Keep each issue small enough for one owner and one reviewable outcome.
2. Record acceptance criteria and verification before moving work to Ready.
3. Keep only actively owned work In Progress.
4. Link the pull request to its issue and move the issue to Review when the
   change is ready for review.
5. Move the issue to Done only after the outcome is delivered and verified.
6. Update status from evidence, not intent.
