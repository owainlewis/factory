# Factory Loop

Code Factory runs as a daily maintenance loop.

The loop should start conservative and become more capable as the quality bar proves itself.

## Phase 1: Inventory

Read `repos.yaml`.

For each repo:

- fetch default branch
- read README
- read AGENTS.md
- detect language and tooling
- check open issues and PRs
- check CI status
- check recent activity

Output:

- repo summary
- detected tooling
- policy mapping
- obvious risks

## Phase 2: Audit

Run checks that do not change code:

- required file check
- broken link check
- README command check
- package metadata check
- CI status check
- stale issue check
- test command discovery

Output:

- ranked problem list
- confidence score
- suggested next action

## Phase 3: Prioritize

Rank work by:

- business value
- user impact
- repo priority
- risk
- effort
- confidence

High-confidence, low-risk docs and test fixes can become PRs.

Unclear work becomes issues.

## Phase 4: Plan

For each selected task:

- define scope
- choose branch name
- list files expected to change
- list checks to run
- identify human review needs

Do not start work without a narrow plan.

## Phase 5: Change

Make the smallest complete change.

Allowed first-wave changes:

- fix broken links
- update README commands
- add missing docs sections
- add simple smoke tests
- fix obvious CI drift
- align examples with current files

Avoid broad rewrites.

## Phase 6: Verify

Run the smallest meaningful check first.

Then run the repo's documented checks when practical.

If verification is not possible, say why in the PR.

## Phase 7: Pull Request

Every PR must include:

- what changed
- why it matters
- how it was verified
- what was not verified
- whether human review is required

## Phase 8: Report

Each run writes a report with:

- repos scanned
- issues opened
- PRs opened
- checks run
- failures
- blocked items
- recommended human decisions

## Daily Limits

Default limits:

- max 3 PRs total per day
- max 2 open PRs per repo
- unlimited audit reports
- issue creation allowed when high confidence

