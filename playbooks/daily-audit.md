# Daily Audit Playbook

Run this once per day.

## Steps

1. Read `repos.yaml`.
2. For each high-priority repo, fetch the latest default branch.
3. Read README, AGENTS.md, package files, and CI config.
4. Run non-mutating checks.
5. Score problems by value, risk, and confidence.
6. Open issues for clear problems.
7. Open at most 3 low-risk PRs across all repos.
8. Write a report in `reports/`.

## Output

The daily report should include:

- repo health summary
- top problems found
- issues opened
- PRs opened
- checks run
- blocked items
- decisions needed from Owain

## Stop Conditions

Stop and report instead of changing files when:

- repo purpose is unclear
- docs conflict with code
- tests are failing for unknown reasons
- the fix would require a broad refactor
- the change needs strategy or taste judgment

