# Daily Control Plane Audit

## Schedule

Daily.

## Runner

Codex.

## Goal

Keep Code Factory internally consistent and simple.

## Steps

1. Check every `repos/*/config.yaml` parses.
2. Check every active repo has `goal.md`.
3. Check every active repo has at least one automation prompt.
4. Check every automation file named in config exists.
5. Check shared labels parse.
6. Check README matches the current architecture.
7. Open issues for missing pieces.

## Allowed Fixes

- Fix broken local links.
- Fix config references.
- Add missing template fields.
- Update README when the architecture has already changed.

## Stop Rules

Stop and ask Owain before:

- adding new active repos
- changing standards
- changing safety defaults
- deleting repo configs

