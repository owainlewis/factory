# Task Loop: Issue To PR

Use this loop for one project.

## Goal

Complete one ready GitHub issue and open a pull request.

Stop before merge.

## Inputs

- Project config supplied by Factory
- Target repo from the project config
- Local path from the project config
- Required issue label from the project config

## Process

1. Read the project config.
2. Open the target repo.
3. Find one open issue with the configured ready label.
4. If there is no ready issue, stop and report that no work is ready.
5. If more than one issue is ready, choose the smallest clear issue.
6. Read the issue, comments, and relevant code before editing.
7. If the issue is unclear, comment with the blocking question and stop.
8. Create a branch named `codex/issue-<number>-<short-slug>`.
9. Implement the smallest complete change.
10. Add or update tests when the behavior changes.
11. Run the project checks from the project config.
12. Review the diff for unrelated changes, missing tests, and broken behavior.
13. Open a pull request linked to the issue.
14. Leave the PR ready for human review.

## Rules

- Work on one issue only.
- Do not merge.
- Do not push to the default branch.
- Do not do unrelated cleanup.
- Do not rewrite broad areas of code.
- Do not change editor behavior or keybindings unless the issue clearly asks for it.
- Do not add large dependencies without human approval.
- If tests cannot run, explain why in the PR.

## Done

The loop is done when one of these is true:

- A PR is open for one completed issue.
- No issue exists with the configured ready label.
- The chosen issue is blocked and has a clear blocking comment.
