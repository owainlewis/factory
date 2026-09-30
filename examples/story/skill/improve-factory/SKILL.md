---
name: improve-factory
description: Reviews recent factory runs on GitHub and opens a pull request that improves the factory skill. Use on a schedule, or when asked to improve the factory.
---

# Improve factory

Input: a number of days to review (default 14). Run in a clone of the
repository that keeps the factory skill at `.claude/skills/factory/SKILL.md`.
Do not ask the user anything.

## 1. Find the runs

    gh pr list --state all --search "head:factory/ created:>=<date>" --limit 100 \
      --json number,title,state,url

## 2. Collect what people corrected

For each pull request, read:

    gh pr view <pr> --json state,commits,reviews,comments,closingIssuesReferences
    gh api repos/{owner}/{repo}/pulls/<pr>/comments
    gh issue view <issue> --comments

Record:

- Merged, closed without merging, or still open.
- The number of fix rounds from the factory's final comment on the issue.
- Review comments from people. Skip bots and the factory's own replies.
- Commits a person pushed to the branch, and what they changed.
- Stop reports the factory posted on the issue.

## 3. Find patterns

List each problem with the pull requests where it happened. Keep only problems
that happened in at least 2 runs. One-off problems are not evidence.

## 4. Propose the change

For each pattern, make the smallest edit to `.claude/skills/factory/SKILL.md`
that would have prevented it. Open one pull request:

    git fetch origin
    git worktree add ../improve-factory -b improve-factory/<date> origin/HEAD

In the description, include a table of every run (pull request, result, fix
rounds, human comments), and link each edit to the runs that justify it.

If there is no pattern, change nothing and report the table only.

## Rules

- Change only the factory skill.
- Never merge. A person reviews every change to the factory.
- Never remove a rule that stops tests from being weakened or pull requests
  from being merged.
