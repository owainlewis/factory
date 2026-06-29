# CI

## Goal

Make Factory pull requests run the checks needed to trust code changes.

## Plan Mode

In plan mode:

1. Inspect `.github/workflows/`.
2. Inspect README and docs for documented checks.
3. Compare CI behavior against `.factory/STANDARDS.md`.
4. Report missing checks and one safe next change.
5. Do not edit files.

## Execute Mode

In execute mode:

1. Add or improve one CI workflow.
2. Keep the workflow simple.
3. Run the closest local checks.
4. Open a draft pull request.

## Required Checks

- `go test ./...`
- `go vet ./...`

## Stop Rules

- Do not add secrets.
- Do not require paid services.
- Do not add broad automation unrelated to Go checks.
