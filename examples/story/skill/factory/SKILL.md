---
name: factory
description: Takes a GitHub issue to a pull request with passing CI and addressed automated reviews, in one shot with no human in the loop. Use when asked to build an issue, ticket, or story end to end.
---

# Factory

Input: a GitHub issue number or URL. Run in any clone of the repository with
`gh` logged in. Do not ask the user anything. The issue and the pull request
hold all state, so a run needs nothing else from this machine.

## 1. Read the issue

```sh
gh issue view <issue> --comments
```

If it is too unclear to build, comment on the issue with what is missing, and stop.

## 2. Write the spec

Read the code the issue touches. Then write a spec with these sections:

- Goal
- Acceptance criteria, each one testable
- Plan: the files to change and the tests to add
- Decisions: each open question, the default you chose, and why

Post the spec as a comment on the issue. Build exactly what it says.

## 3. Build

```sh
git fetch origin
git worktree add ../factory-<issue> -b factory/<issue> origin/HEAD
```

Work in that folder. Implement the spec with a test for each criterion. Run the
project's tests (see README, AGENTS.md, or `.github/workflows`) until they pass.

## 4. Open the pull request

```sh
git push -u origin factory/<issue>
gh pr create --title "<issue title>" --body "Closes #<issue>"
```

## 5. Wait for CI

```sh
gh pr checks <pr> --watch --interval 30
```

If a check fails, read `gh run view <run-id> --log-failed`, fix the cause, push,
and wait again.

## 6. Wait for automated reviews

After CI passes, wait up to 10 minutes for review bots to comment on the latest
commit. Check every minute:

```sh
gh pr view <pr> --json reviews,comments
gh api repos/{owner}/{repo}/pulls/<pr>/comments
```

For each review comment: fix it, or decide it is wrong. Reply to the comment
with what you changed or why you did not. If you changed code, push and go back
to step 5.

## 7. Report on the issue

Comment on the issue with the PR link, the final CI result, each review comment
and what you did about it, and your decisions.

## Rules

- Stop after 3 rounds of fixes. Comment on the issue with what still fails.
- Never weaken a test or acceptance criterion to pass.
- Never merge, force-push, or approve your own pull request.
- Report only results you saw. Do not say CI passed unless `gh pr checks` said so.
