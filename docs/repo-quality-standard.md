# Repo Quality Standard

This is the baseline Code Factory should move repos toward.

Not every repo needs every item.
Every important repo should make its current state obvious.

## Required Files

Every active repo should have:

- `README.md`
- `LICENSE`
- `.gitignore`
- `AGENTS.md`

Public repos should also have:

- clear install or run steps
- contribution guidance, even if short
- examples or screenshots when useful
- release notes or changelog for released tools

## README Standard

A strong README answers:

- What is this?
- Who is it for?
- What problem does it solve?
- What is the current status?
- How do I install or run it?
- What is a minimal example?
- What works now?
- What is not implemented yet?
- Where should issues or contributions go?

## License Standard

Every repo should have an explicit license.

For public open source repos, missing license is a real problem because users do not know what they can legally do.

For private repos, the license can still clarify ownership and future intent.

## Code Standard

Code should be:

- formatted with the repo's normal formatter
- covered by focused tests for core behavior
- split into small modules when complexity grows
- free of dead paths and stale examples
- boring in the best way

## Test Standard

Every active code repo should have:

- a documented test command
- tests for core behavior
- at least one smoke test for CLI tools or apps
- CI that runs the main checks

If tests do not exist yet, Code Factory should create an issue before inventing a large test suite.

## Docs Standard

Docs should say what is true now.

Avoid:

- future-tense promises without issues
- claims that cannot be verified
- stale install commands
- undocumented environment variables
- examples that do not run

## Release Standard

Released repos should have:

- versioning
- release notes
- clear compatibility notes
- repeatable release command or checklist

Code Factory can prepare release PRs.
Humans approve releases.

## Problem Signals

Code Factory should detect:

- missing README, license, or AGENTS.md
- broken links
- failing CI
- no test command
- stale install steps
- TODO-heavy files
- old open issues with no labels
- public claims not backed by code
- dependencies with known security issues
- examples that do not compile or run
- docs that mention files or commands that do not exist

