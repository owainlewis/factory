# Standards Check

## Goal

Review Factory against `.factory/STANDARDS.md`.
Make the smallest safe change that improves compliance.

## Plan Mode

In plan mode:

1. Read `.factory/AGENTS.md`.
2. Read `.factory/STANDARDS.md`.
3. Inspect README, docs, Go packages, tests, and CI.
4. Report which standards pass, fail, or need human review.
5. Name one smallest safe execute-mode change.
6. Do not edit files.
7. Do not create branches.
8. Do not open pull requests.

## Execute Mode

In execute mode:

1. Pick one small fix that does not need human review.
2. Create a non-default branch.
3. Make the change.
4. Run relevant checks.
5. Commit the change.
6. Push the branch.
7. Open a draft pull request.
8. Include checks run and remaining gaps.

## Stop Rules

Stop and report `blocked` if:

- the change affects safety rules
- the change affects repo purpose
- the fix requires product strategy
- tests cannot run locally
- the working tree has unrelated user changes

## Verification

Run when code changes:

```sh
go test ./...
go vet ./...
```
