# GitHub

## Goal

Make the GitHub repository professionally set up and easy to manage.

## Plan Mode

In plan mode:

1. Inspect repository metadata, labels, issues, projects, and Actions setup.
2. Compare GitHub setup against `.factory/STANDARDS.md`.
3. Report missing description, topics, labels, issue board, CI, or review automation.
4. Name one smallest safe execute-mode change.
5. Do not edit files.
6. Do not change GitHub settings.

## Execute Mode

In execute mode:

1. Make one focused GitHub setup improvement.
2. Prefer metadata, labels, or issue board setup before optional automation.
3. Do not change repository visibility, permissions, or branch protection without human review.
4. Record what changed and what remains.

## Checks

- Repository description is set.
- Repository topics are set.
- Issues are enabled.
- A GitHub Project or equivalent issue board tracks ongoing work.
- Standard Factory labels exist.
- Type, status, and priority labels exist.
- GitHub Actions run normal build and test checks.
- Automated code review is configured when a trusted tool is available.

## Stop Rules

- Stop before changing repository visibility.
- Stop before changing permissions.
- Stop before changing branch protection.
- Stop before enabling paid services.
- Stop when a human needs to choose labels, topics, or project structure.
