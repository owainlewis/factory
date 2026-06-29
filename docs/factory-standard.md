# The Factory Standard

The Factory Standard is a checklist for professional software projects.

It describes what a senior engineer would expect to find in any serious repository.
Factory uses this standard to create repo-owned `STANDARDS.md` files and workflow files.

The standard is generic.
The implementation is language-specific.

```text
Every repo gets the same questions.
Each language gives different answers.
Each repo owns the final rules.
```

## Why this exists

Most projects do not fail because the code is impossible.
They fail because the basic engineering work is never finished.

Examples:

- the README is stale
- CI does not run
- tests exist but are not documented
- releases are manual
- the GitHub description is empty
- issue labels are inconsistent
- dependencies are not checked
- no one knows how to ship the thing

These are not glamorous tasks.
They are the work that makes software usable.

Factory turns that work into a repeatable standard and gives agents workflows for improving one bucket at a time.

## The buckets

Every project should be evaluated across these buckets.

### Identity

The repository should clearly say what it is.

Standards:

- GitHub description is set.
- README explains the purpose in plain language.
- README names the audience or use case.
- Project status is clear.
- Public claims match the current code.

Agent workflows:

- description-readiness
- readme-purpose-check
- repo-metadata-check

### Usability

A new person should be able to use the project from a clean checkout.

Standards:

- README has install, build, test, and run sections.
- At least one example command works.
- Required tools and versions are named.
- Errors from missing setup are documented when common.

Agent workflows:

- usability-check
- readme-usage-check
- example-smoke-test

### Build

The project should have a documented build path.

Standards:

- Build command is documented.
- Build command works locally.
- Build files match the language and project type.
- Build output is predictable.

Language examples:

- Go: `go build ./...`
- OCaml: `dune build`
- Node: `npm run build`
- Python: package build or documented no-build path
- Rust: `cargo build`

Agent workflows:

- build-readiness
- fix-build

### Testing

The project should prove its core behavior.

Standards:

- Test command is documented.
- Tests run locally.
- Tests run in CI.
- Bug fixes include regression tests.
- Important examples are tested or clearly marked as examples only.

Language examples:

- Go: `go test ./...`
- OCaml: `dune runtest` or `make test`
- Node: `npm test`
- Python: `pytest`
- Rust: `cargo test`

Agent workflows:

- testing-readiness
- add-missing-test
- improve-test-coverage

### CI and automation

Pull requests should run automated checks.

Standards:

- GitHub Actions or equivalent CI runs on pull requests.
- CI runs build and tests.
- CI fails when tests fail.
- CI does not need secrets for normal pull request checks.
- CI logs are useful enough to debug failures.

Common checks:

- build
- tests
- format
- lint
- typecheck
- dependency audit
- generated file check

Agent workflows:

- ci-readiness
- add-ci
- fix-failing-ci

### Code quality

Code should be readable, idiomatic, and maintainable.

Standards:

- Code follows the language style of the project.
- Public behavior is stable or changes are called out.
- New abstractions solve real duplication or complexity.
- Dead code is removed when safe.
- Complex code has focused comments.

AI code review belongs here.

AI review standards:

- AI review can be used as a first pass.
- AI review findings are suggestions, not authority.
- Human review is required before merge.
- Security-sensitive changes require human review.
- Review output should include severity, evidence, and file references.

Agent workflows:

- ai-review
- review-pr
- refactor-small
- remove-dead-code

### Documentation

Docs should match the current system.

Standards:

- README is accurate.
- Architecture notes exist when the system has non-trivial structure.
- API docs exist when the project exposes an API.
- Examples are current.
- Generated docs are updated by the documented command.

Agent workflows:

- docs-readiness
- docs-code-alignment
- update-examples

### Release

The project should explain how it ships.

Standards:

- Release process is documented.
- Releases use tags.
- Release notes explain user-visible changes.
- `CHANGELOG.md` exists when the project has users.
- Runnable tools document install or download options.
- Libraries document package publishing when relevant.

Project type examples:

- CLI: release artifacts or a clear source install path
- Library: package metadata and API docs
- Service: deploy, rollback, and environment notes
- App: build, deploy, and version notes

Agent workflows:

- release-readiness
- add-changelog
- add-release-workflow

### Security

The project should avoid common preventable risks.

Standards:

- Secrets are not committed.
- Dependency update path exists when dependencies are used.
- CI permissions are minimal.
- Security-sensitive issues are labeled for human review.
- Vulnerability reporting path exists for public projects when relevant.

Agent workflows:

- security-readiness
- dependency-update
- secret-scan-check

### Operations

Runnable systems should explain how they run after release.

Standards:

- Required environment variables are documented.
- Health checks exist for services.
- Logs are useful.
- Deployment and rollback are documented when relevant.
- Local development and production setup are clearly separated.

Agent workflows:

- operations-readiness
- env-docs-check
- deploy-docs-check

### Governance

The project should make ownership and contribution rules clear.

Standards:

- License is present and metadata agrees across files.
- Contribution path is documented when the project accepts outside work.
- Issue labels are consistent.
- Human review rules are explicit.
- Maintainer-only decisions are listed.

Factory labels:

- `factory-ready`
- `factory-triage`
- `factory-needs-human`
- `factory-blocked`

Agent workflows:

- governance-readiness
- label-sync
- issue-triage

### Agent readiness

The repo should be safe for coding agents to work on.

Standards:

- `AGENTS.md` exists or the repo documents agent instructions.
- `STANDARDS.md` defines repo health.
- `WORKFLOWS/` contains agent playbooks.
- `OBJECTIVES/` contains current work orders when agents are doing directed work.
- `JOURNAL.md` records handover notes when agents run regularly.
- Stop rules are explicit.
- Agents open draft pull requests.
- Agents do not merge pull requests.
- Agents do not push to default branches.

Agent workflows:

- standards-check
- workflow-readiness
- journal-update

## Template model

Factory defaults should be composable.

```text
base standard
+ language pack
+ project type pack
+ selected capability packs
= repo-owned STANDARDS.md, WORKFLOWS/, and OBJECTIVES/
```

Example:

```text
Project: OCaml
Type: CLI
Capabilities:
- coding standards
- build
- testing
- CI
- docs
- release
- agent readiness
```

This produces:

```text
STANDARDS.md
WORKFLOWS/
  standards-check.md
  ci-readiness.md
  docs-readiness.md
  release-readiness.md
  issue-triage.md
OBJECTIVES/
  2026-06-29-ci-readiness.md
JOURNAL.md
```

## Language-specific answers

The buckets stay the same across projects.
The answers change by language and project type.

OCaml examples:

- Build with `dune build`.
- Test with `dune runtest` or `make test`.
- Keep `.opam` metadata accurate.
- Use `ocaml/setup-ocaml` in GitHub Actions.
- Document source install before binary release unless binaries are provided.

Go examples:

- Build with `go build ./...`.
- Test with `go test ./...`.
- Run `go vet ./...`.
- Keep module metadata in `go.mod`.
- Release CLIs with tagged binaries when relevant.

## Evidence

Agents should not only say a repo is healthy.
They should show evidence.

Good evidence:

- commands run
- test output summary
- files changed
- links to CI runs
- links to pull requests
- unresolved blockers
- human decisions needed

Bad evidence:

- vague confidence
- unsupported claims
- broad cleanup
- hidden assumptions

## How Factory should use this

Factory should use this standard in three ways.

1. Bootstrap

Create repo-owned standards and workflows from templates.

2. Plan

Ask an agent to inspect one bucket and report gaps.

3. Execute

Ask an agent to fix one small gap, verify it, and open a draft pull request.

The source of truth remains in the target repo.
Factory provides the memory, templates, and execution loop.
