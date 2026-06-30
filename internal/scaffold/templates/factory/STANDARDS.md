# STANDARDS.md

These standards define what good looks like for this repo.

Edit them to match your project. Factory reads this file to judge repo health.

## Purpose

State what this project is and the quality bar it holds itself to.

## Code

- Keep boundaries clear and changes small.
- Match existing conventions.

## Testing

- Code changes must include focused tests.
- The test suite must pass before a pull request is opened.

## Documentation

- The README must explain what the project is and how to run it.
- Public claims must be backed by code, docs, tests, issues, or pull requests.

## GitHub Standards

- The repository description and topics must be set.
- Issues must be enabled.
- Standard Factory labels must exist:
  - `factory-ready`
  - `factory-triage`
  - `factory-needs-human`
  - `factory-blocked`

## Safety

- Factory must not merge pull requests automatically.
- Factory must not push directly to the default branch.
- Factory must stop when a workflow needs human input.
- Factory must record enough evidence to explain what happened.

## Human Review Required

Human review is required for:

- merging
- releases
- public claims
- pricing
- product strategy
- deleting features
- changing repo purpose
- changing licenses
- changing safety rules
