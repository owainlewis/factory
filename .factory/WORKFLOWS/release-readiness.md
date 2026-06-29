# Release Readiness

## Goal

Make Factory releasable when a human decides it is ready.

## Plan Mode

In plan mode:

1. Inspect README, docs, versioning, changelog, and release notes.
2. Inspect CI and verification commands.
3. Report release gaps.
4. Name one smallest safe execute-mode change.
5. Do not publish a release.

## Execute Mode

In execute mode:

1. Improve one release-readiness gap.
2. Prefer docs, CI, or changelog scaffolding before release automation.
3. Run relevant checks.
4. Open a draft pull request.

## Stop Rules

- Do not create a release.
- Do not publish binaries.
- Do not change license.
- Stop for human review on versioning decisions.
