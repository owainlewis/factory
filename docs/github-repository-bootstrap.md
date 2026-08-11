# GitHub repository bootstrap

Use this playbook to turn an empty or existing GitHub repository into a safe,
legible project that humans and coding agents can work in. Give an agent this
file and the repository URL, then ask it to follow the prompt below.

The defaults reflect the working patterns in `owainlewis/factory`,
`owainlewis/blueprint`, `owainlewis/slate.do`, and `owainlewis/push`. They also
close gaps that were inconsistent across those repositories: one merge policy,
automatic branch cleanup, dependable default-branch protection, security
updates, and reusable account-level defaults.

This is a baseline, not a demand for every possible GitHub feature. The agent
must only create files and automation that have a real owner and purpose.

Start a run with:

> Follow the reusable agent prompt in `docs/github-repository-bootstrap.md` for
> `[OWNER/REPOSITORY]`. Infer repository facts before asking questions, leave
> legal or production-access decisions as explicit gaps, open a pull request,
> and do not merge it unless I ask.

## Reusable agent prompt

### Goal

Provision `[OWNER/REPOSITORY]` as a well-maintained GitHub project. Make the
repository easy to understand, safe to change, ready for coding agents, and
cheap to keep current.

Work against the checked-out repository when one is available. Otherwise clone
the named repository. If the GitHub repository does not exist, create it only
when the request explicitly authorizes creation and states its owner and
visibility.

### Inputs

- Repository: `[OWNER/REPOSITORY or infer from origin]`
- Visibility: `[public/private or infer from GitHub]`
- Project purpose: `[one sentence or infer from committed code and metadata]`
- License: `[SPDX identifier, proprietary, or undecided]`
- Delivery target: `[none, package registry, GitHub Release, service, app store,
  or other]`
- Maintainers: `[users or teams, when code ownership or required reviews matter]`

Infer facts from the repository and GitHub before asking for them. A missing
license choice, visibility, owner, or external deployment credential is a real
decision. Do not guess it. Record it as a gap and complete everything that does
not depend on it.

### Operating rules

1. Inspect before changing. Read repository instructions, root files, package
   manifests, task runners, current workflows, GitHub settings, labels, rules,
   projects, releases, and open pull requests.
2. Preserve useful existing material. Never replace a non-empty file with a
   generic template. Update it in place or show the conflict as a gap.
3. Use one focused branch and pull request for an existing repository. Do not
   push directly to its default branch and do not merge unless explicitly
   asked. An empty repository may need one initial commit on `main` before CI
   and rules can exist; explain this before doing it.
4. Treat the repository as the source of truth. Do not duplicate maintained
   instructions across the README, architecture, contribution guide, agent
   files, wiki, and project description.
5. Create the smallest complete setup. Do not add badges, workflows, folders,
   generated examples, release automation, or community files that cannot be
   made accurate today.
6. Use the GitHub CLI or API for settings that cannot be committed. Resolve
   exact repository IDs, branch names, check names, project field IDs, and
   capabilities before mutating them. Never copy IDs from another repository.
7. Do not expose tokens, secrets, private URLs, vulnerability details, customer
   data, or local machine paths in files, logs, issues, or pull requests.
8. Run relevant checks, inspect the final diff, and report exact evidence. A
   created file, enabled setting, successful check, or API response is proof.

### Phase 1: audit and decide

Build a concise gap table with these columns: area, current state, proposed
change, reason, and blocked decision. Cover:

- repository identity, visibility, description, homepage, and topics;
- default branch, merge methods, branch cleanup, rulesets, and access;
- root documentation and agent instructions;
- issue forms, pull request template, labels, and project board;
- CI, dependency updates, code scanning, secret scanning, and releases;
- environments, webhooks, apps, secrets, and deploy keys, without reading or
  printing secret values.

Detect the language, package manager, lockfiles, generated files, supported
platforms, and real commands from the repository. Do not invent commands from
the language ecosystem's conventions.

Classify the repository before choosing optional files:

- `private application`: optimize for a small trusted team and deployment
  safety;
- `public product`: add contributor and security paths;
- `reusable library or CLI`: add public API, compatibility, release, and
  support guidance;
- `docs or content`: keep CI and architecture proportional;
- `template`: remove project-specific claims and make every placeholder
  explicit.

Continue on reasonable, reversible choices. Stop only before a choice that
changes legal rights, visibility, billing, production access, or third-party
credentials.

### Phase 2: establish repository files

Create or improve only the applicable files below.

#### Required baseline

- `README.md`: explain what the project is, who it is for, current status,
  quickest working path, prerequisites, exact setup and test commands,
  configuration at a safe level, and links to deeper docs. Put the value and
  quick start before internals.
- `.gitignore`: derive it from the actual stack and local tooling. Keep example
  configuration files trackable and ignore real local configuration.
- `.editorconfig`: add only when the repository has no formatter-owned complete
  equivalent. Match existing formatting.
- `AGENTS.md`: the canonical operating policy for coding agents. Include the
  project purpose, verified source map, architectural boundaries, exact build,
  format, lint, test, and generated-file commands, security constraints,
  change workflow, documentation update triggers, and definition of done.
- `CLAUDE.md`: keep shared policy in `AGENTS.md`. Default to:

  ```md
  # Claude Code

  @AGENTS.md

  This repository keeps shared agent instructions in `AGENTS.md`. Add only
  Claude Code-specific setup here.
  ```

  Preserve any real Claude-specific instructions already present.
- `ARCHITECTURE.md`: add it for software with meaningful components,
  persistence, protocols, trust boundaries, or deployment topology. Describe
  the verified system that exists today, not a proposed design. Include status,
  verification basis, system context, components and dependency direction,
  critical flows, data and state, security boundaries, operational model,
  hard limits, and a source map. Put future designs under
  `docs/<feature>/design.md`.
- `docs/README.md`: add an index when the project has more than one supporting
  document. Separate current guides from proposed or historical design work.
  Do not create an otherwise empty `docs/` directory.

#### Add when applicable

- `LICENSE`: required for a public open-source repository. Use the explicit
  license input. Do not assume that public source is open source and do not
  choose legal terms for the owner.
- `CONTRIBUTING.md`: add when anyone beyond the owner may contribute. Link to
  exact local setup and checks. Explain focused changes, tests, docs, commits,
  and the pull request path.
- `SECURITY.md`: add for public projects and any project handling credentials,
  personal data, network access, or privileged execution. State supported
  versions, a private reporting route, expected response window, and what must
  not be posted publicly. Prefer GitHub private vulnerability reporting for a
  public repository when available.
- `CODE_OF_CONDUCT.md`: add for a public project accepting community
  participation.
- `SUPPORT.md`: add only when support requests need a different route from bug
  reports.
- `CHANGELOG.md`: add only when the project will deliberately maintain one.
  Otherwise use generated GitHub release notes as the single source.
- `.github/CODEOWNERS`: add only when real users or teams own distinct areas.
  Protect `.github/`, deployment files, security policy, and `CODEOWNERS`
  itself. Every named owner must have the required repository access.
- `.gitattributes`: add when line endings, linguist classification, generated
  files, or release archives need explicit behavior.

Do not put volatile implementation detail in `AGENTS.md`. Link to
`ARCHITECTURE.md` for system facts and to `CONTRIBUTING.md` for long setup
instructions. Agent rules should be short, concrete, and verifiable.

### Phase 3: standardize collaboration

Create `.github/PULL_REQUEST_TEMPLATE.md` with:

- `Problem`: the user or operational problem, with an issue link when one
  exists;
- `Changes`: the smallest important changes;
- `Verification`: exact commands and manual evidence, including affected
  failure paths;
- `Risks`: security, compatibility, migration, data, and operational risk, or
  `None`;
- truthful checkboxes for focus, tests, docs, independent review, and removal
  of sensitive data.

Create YAML issue forms under `.github/ISSUE_TEMPLATE/`:

- `bug_report.yml`: problem, expected behavior, minimal reproduction, version,
  environment, sanitized evidence, duplicate search, and a reminder to use the
  private security route;
- `feature_request.yml`: problem, observable outcome, constraints, possible
  approach, alternatives, and sanitized context;
- `task.yml`, when Issues drive delivery: outcome, context, acceptance
  criteria, proof/checks, dependencies, and out of scope;
- `config.yml`: disable blank issues when the forms cover all supported intake;
  link the private security route and support route where applicable.

Use a compact label taxonomy. Prefer GitHub Project fields for workflow state,
priority, and effort instead of encoding all three as labels. Start with:

| Label | Color | Purpose |
| --- | --- | --- |
| `bug` | `d73a4a` | Existing behavior is broken or unsafe |
| `enhancement` | `a2eeef` | New or improved behavior |
| `documentation` | `0075ca` | Documentation-only work |
| `dependencies` | `0366d6` | Dependency updates |
| `needs-agent` | `0E8A16` | The next action belongs to an agent |
| `needs-human` | `FBCA04` | Human review or a decision is required |
| `blocked` | `B60205` | Work cannot progress because of a dependency |

Add `good first issue` and `help wanted` only for a public project that
welcomes outside contributions. Add area labels only when maintainers will use
them for routing. Do not keep redundant pairs such as `bug` and `type:bug`.
Before deleting or renaming existing labels, check open issues, pull requests,
project workflows, and external automation. Migrate references first.

### Phase 4: configure the GitHub Project

If the repository uses GitHub Issues for planned work, create or link one
GitHub Project owned by the same user or organization. Make it the single
delivery board and link it to the repository.

Use these status options in order:

1. `Todo`
2. `Ready`
3. `In Progress`
4. `Blocked`
5. `Review`
6. `Done`

Add `Priority` with `P0`, `P1`, `P2`, and `P3`. Add `Effort` with `XS`, `S`,
`M`, `L`, and `XL` only when the team will use estimates. Keep GitHub's native
assignee, label, milestone, repository, linked pull request, reviewer, parent
issue, and sub-issue progress fields.

Create:

- a `Pipeline` board grouped by Status;
- a `Backlog` table for `Todo` work, sorted by Priority;
- a `Ready` table for work an agent or developer can start;
- a `Review` view for pull requests and issues awaiting human action.

Enable built-in workflows to add matching repository issues and pull requests,
set closed or merged work to `Done`, and archive old completed items. If board
automation uses labels such as `needs-agent`, verify the exact transition rules
and avoid two automations fighting over Status.

A task is `Ready` only when a fresh agent can complete it without product or
technical questions. Its issue must state the outcome, constraints, acceptance
criteria, checks, dependencies, and what is out of scope.

Skip the project for a repository with no continuing backlog. Do not make draft
project items the canonical source for engineering work when issues can carry
discussion, links, and history.

### Phase 5: add CI and dependency maintenance

Create `.github/workflows/ci.yml` from commands verified in the repository.
The workflow must:

- run on every pull request and on pushes to the default branch;
- declare least-privilege permissions, normally `contents: read`;
- set explicit timeouts;
- use lockfile-respecting installs such as `npm ci` or equivalent;
- run the project's formatter check, linter or static analysis, tests, and
  build in the order that gives useful failures quickly;
- test generated files when committed artifacts must stay current;
- test every supported operating system or architecture only when that support
  is a real contract;
- use concurrency cancellation for superseded pull request runs;
- expose one stable final job named `check` when a matrix or several jobs sit
  behind branch protection. Make it depend on every required job, run with
  `if: always()`, and fail unless every `needs.*.result` is `success`. A skipped
  required check can otherwise be reported as successful;
- upload useful failure evidence with a short retention period when browser or
  integration tests produce it;
- pin third-party actions to full commit SHAs and retain a version comment;
- never give untrusted pull request code access to write tokens or secrets.

Do not guess action SHAs. Resolve the current upstream release tag to a commit
from the action's official repository, then pin that commit. Do not use
`pull_request_target` to run untrusted checked-out code.

Create `.github/dependabot.yml` for every detected package ecosystem and for
`github-actions`. Prefer weekly grouped minor and patch updates with bounded
open pull requests. Keep major updates separate for deliberate review. Use
either Dependabot or Renovate, not both.

For public repositories, add dependency review on pull requests when supported.
For private repositories, check plan eligibility before adding a workflow that
cannot run.

### Phase 6: secure repository settings

Configure repository settings deliberately:

- use `main` as the default branch for a new project;
- enable Issues when they are the work tracker;
- enable Projects only when a project is linked;
- disable the wiki when versioned `docs/` are the source of truth;
- enable Discussions only when the project has a community that needs them;
- enable squash merge and use pull request titles as squash commit messages;
- disable merge commits and rebase merge unless the repository has an explicit
  history policy that needs them;
- enable automatic deletion of merged head branches;
- enable auto-merge and branch updates when the account plan supports them;
- give GitHub Actions read-only default permissions and prevent Actions from
  approving pull requests unless a reviewed workflow requires it.

Enable every available security feature that fits the repository:

- dependency graph;
- Dependabot alerts and Dependabot security updates;
- secret scanning and push protection;
- private vulnerability reporting for a public repository;
- CodeQL default setup for supported languages, unless an accurate advanced
  workflow already exists.

Do not enable a feature and call the work complete if its first run fails or it
does not support the repository's language or plan.

### Phase 7: protect the default branch

Create a repository ruleset targeting the default branch. Prefer a ruleset over
a legacy branch protection rule so its scope and combined rules remain visible.
Apply it only after the CI workflow exists and its `check` job has completed in
the repository. GitHub cannot reliably require a check that has never run.

Default rules:

- active enforcement with no routine bypass actor;
- prevent branch deletion and non-fast-forward pushes;
- require a pull request before merging;
- require the branch to be up to date before merging;
- require the `check` status from GitHub Actions;
- require all review conversations to be resolved;
- allow only the repository's chosen merge method.

For a solo repository, require the pull request but use zero mandatory human
approvals so the owner is not permanently blocked. For a repository with two or
more active maintainers, require at least one approval and require approval of
the most recent reviewable push or dismiss stale approvals. Require code-owner
review only when a valid `CODEOWNERS` file exists.

Do not require signed commits, linear history, merge queue, successful
deployments, or code-scanning results by reflex. Add them when the project has
the signing workflow, merge volume, deployment environment, and stable checks
needed to keep those rules from becoming ceremonial or blocking.

Test the rule with a non-default branch or inspect its API representation.
Confirm that direct pushes, force pushes, deletion, unresolved conversations,
and failing CI are handled as intended.

### Phase 8: releases and deployment

Only create release or deployment automation when the delivery target is
known.

For a versioned library or CLI:

- document the versioning policy and supported versions;
- trigger releases from protected version tags or an explicit manual workflow;
- build from a clean checkout and locked dependencies;
- test the same revision before publishing;
- publish checksums for binary artifacts and provenance or attestations when
  supported;
- grant `contents: write`, package, or identity-token permission only to the
  release job that needs it;
- use GitHub environments for approval-gated or credentialed publication;
- create GitHub release notes and verify installation from the produced
  artifact.

For a deployed application, document environments, migrations, rollback,
health verification, and ownership. Never create placeholder deploy workflows
or empty environments.

### Phase 9: verify and hand off

Run local format, lint, test, build, and documentation checks. Validate all YAML
and inspect the exact staged diff. Then use GitHub to verify:

- repository metadata and merge settings;
- labels and issue form rendering;
- linked Project fields, views, and workflows;
- Actions default permissions;
- the latest CI run and the exact required check name;
- ruleset target, enforcement, pull request rule, required checks, deletion,
  and force-push behavior;
- enabled security features and their first successful or pending scans;
- the pull request diff and checks.

Return:

1. `Created or changed`: committed files and GitHub settings.
2. `Verified`: exact local commands, workflow URLs, and API-backed settings.
3. `Decisions`: choices made and why.
4. `Gaps`: only unresolved owner decisions, unavailable plan features, missing
   credentials, or failed checks, each with the next action.
5. `Links`: repository, Project, pull request, Actions, and security pages.

Do not say the repository is protected, secure, or ready until the relevant
rules and checks are active and verified.

## Maintainer notes

The first use of this guide should be on one repository and reviewed for fit.
After the pattern is stable, reduce repeated setup work:

1. Create an `OWNER/.github` public repository for account-wide community
   health defaults such as `CONTRIBUTING.md`, `SECURITY.md`, issue forms, and the
   pull request template. Repository-local files override these defaults.
2. Create a deliberately generic GitHub template repository for committed
   files and starter CI. Do not put project-specific architecture, commands, or
   package versions in it.
3. Keep `AGENTS.md`, `README.md`, and CI project-local because useful content in
   those files must describe verified repository facts.
4. Re-run the audit sections of this prompt after major stack, team, release,
   or deployment changes.

## Why these defaults

- GitHub recommends a README and community health files, protected important
  branches, required reviews and status checks, and private vulnerability
  reporting for public projects.
- Rulesets make the complete policy easier to inspect than isolated legacy
  branch rules. Required checks must be established before they can protect a
  branch reliably.
- A read-only default `GITHUB_TOKEN`, job-specific escalation, and immutable
  action SHAs reduce workflow supply-chain risk.
- Project fields are a better single source for status, priority, and effort
  than overlapping labels. Built-in workflows keep the board current.
- The sampled repositories supplied complementary patterns rather than one
  uniform template: Factory and Push have community files, issue forms, and
  dependency automation; Factory, Slate, and Push have current-state
  architecture; Blueprint keeps canonical agent policy in `AGENTS.md` and
  imports it from `CLAUDE.md`; Factory, Slate, and Push use explicit agent and
  human handoff labels; and the active Factory, Push, and Neo Projects share a
  six-state delivery flow.

## References

- [GitHub repository best practices](https://docs.github.com/en/repositories/creating-and-managing-repositories/best-practices-for-repositories)
- [Available repository ruleset rules](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets)
- [Troubleshooting required status checks](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/collaborating-on-repositories-with-code-quality-features/troubleshooting-required-status-checks)
- [Secure use of GitHub Actions](https://docs.github.com/en/actions/reference/security/secure-use)
- [GitHub repository security quickstart](https://docs.github.com/en/code-security/getting-started/quickstart-for-securing-your-repository)
- [Issue and pull request templates](https://docs.github.com/en/communities/using-templates-to-encourage-useful-issues-and-pull-requests/about-issue-and-pull-request-templates)
- [GitHub Projects best practices](https://docs.github.com/en/issues/planning-and-tracking-with-projects/learning-about-projects/best-practices-for-projects)
- [Default community health files](https://docs.github.com/en/communities/setting-up-your-project-for-healthy-contributions/creating-a-default-community-health-file)
- [About code owners](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners)
