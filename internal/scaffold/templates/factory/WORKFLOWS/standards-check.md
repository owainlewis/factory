# Standards Check

## Goal

Review this repo against `.factory/STANDARDS.md`.
Make the smallest safe change that improves compliance.

Use this one workflow for routine work.
The objective should name the area, such as docs, CI, testing, or release.

## Plan Mode

In plan mode:

1. Read `.factory/AGENTS.md`.
2. Read `.factory/STANDARDS.md`.
3. Inspect the README, docs, source, tests, and CI.
4. Focus on the current objective when one is provided.
5. Report which standards pass, fail, or need human review.
6. Name one smallest safe execute-mode change.
7. Do not edit files.
8. Do not create branches.
9. Do not open pull requests.

## Execute Mode

In execute mode:

1. Pick one small fix that does not need human review.
2. Create a non-default branch.
3. Make the change.
4. Run the relevant checks.
5. Commit the change.
6. Push the branch.
7. Open a draft pull request.
8. Include the checks run and remaining gaps.

## Stop Rules

Stop and report `blocked` if:

- the change affects safety rules
- the change affects repo purpose
- the fix requires product strategy
- the checks cannot run locally
- the working tree has unrelated user changes
