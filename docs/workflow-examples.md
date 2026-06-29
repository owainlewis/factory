# Workflow Examples

These examples belong in target repositories, not in Factory.
Copy one into a managed repo under `.factory/WORKFLOWS/`.

## `.factory/WORKFLOWS/standards-check.md`

```md
# Standards Check

## Goal

Review this repository against `.factory/STANDARDS.md`.
Make the smallest safe change that improves compliance.

## Inputs

- `.factory/AGENTS.md`
- `.factory/STANDARDS.md`
- `.factory/JOURNAL.md`, when present
- current git status
- current test and CI files
- current README and docs

## Plan Mode

In plan mode:

1. Read `.factory/STANDARDS.md`.
2. Compare the repo against each standard.
3. Report which standards pass, fail, or need human review.
4. Name one smallest safe change for execute mode.
5. Do not edit files.
6. Do not create a branch.
7. Do not open a pull request.

## Execute Mode

In execute mode:

1. Read `.factory/STANDARDS.md`.
2. Pick one small fix that does not need human review.
3. Create a non-default branch named `factory/standards-check-<short-description>`.
4. Make the change.
5. Run the most relevant tests or checks.
6. Commit the change.
7. Push the branch.
8. Open a draft pull request.
9. Include what changed, what was checked, and any remaining gaps.

## Stop Rules

Stop and report `blocked` if:

- the standard requires a human product or license decision
- tests cannot run because required secrets or services are missing
- the fix would change public behavior beyond the workflow scope
- the working tree already has unrelated user changes
- the repo has no clear default branch or remote

## Safety

- Never merge a pull request.
- Never push to the default branch.
- Never do broad cleanup.
- Never change the license without human review.
- Prefer one small pull request over many unrelated fixes.
```
