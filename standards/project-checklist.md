# Project Checklist

This is the standard for every active repo in Code Factory.

Use it to create issues and measure repo health.

Project type details live in `standards/profiles/`.
Use this checklist as the shared baseline.

## Identity

- README exists.
- README says what the repo is.
- README says who it is for.
- README says the current status.
- Repo description is accurate.
- Repo topics are useful.

## Legal

- License exists.
- License matches the repo's intended use.

## Agent Guidance

- AGENTS.md exists.
- AGENTS.md explains repo-specific rules.
- AGENTS.md says how to test and verify work.

## Work Tracking

- GitHub Issues are enabled.
- Standard labels exist.
- A GitHub Project board exists.
- Open issues are linked to the project board.
- Issues have type and priority labels.
- Work that needs a human is labelled `status:needs-human`.

## Quality

- Test command is documented.
- Test command works.
- CI exists.
- CI runs the main checks.
- Build or run command is documented.
- Examples are current.
- Broken links are fixed or tracked.

## Maintenance

- Stale issues are triaged.
- Stale PRs are triaged.
- Known limitations are documented.
- Next useful work is tracked as issues.
- Releases are documented when the repo ships versions.

## Automation

- Repo has a Code Factory config.
- Repo has a clear goal.
- Repo has at least one scheduled or manual automation prompt.
- Automation is scoped to safe work.
- Human review boundaries are explicit.

## Release Pipeline

- Release type is explicit.
- Package registry is explicit when applicable.
- Versioning rule is clear when the repo ships versions.
- Release notes exist when the repo ships versions.
- Publishing is automated or documented.
