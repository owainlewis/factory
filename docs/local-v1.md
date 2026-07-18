# Reliable local v1

This guide takes a trusted GitHub issue from `factory:ready` to one green draft
pull request through one authenticated local Codex run. Factory never merges
the pull request.

## Supported environment

Factory v1 requires a Unix-like operating system because process supervision
uses Unix process groups. Install:

- Rust and Cargo on the current stable toolchain;
- Git;
- GitHub CLI (`gh`);
- Codex CLI authenticated with a ChatGPT subscription.

Confirm authentication before installing Factory:

```sh
gh auth status
codex --version
codex login status
```

Factory rejects API-key Codex authentication. Do not configure `OPENAI_API_KEY`
for Factory runs.

## Install from a clean checkout

```sh
git clone https://github.com/owainlewis/factory.git
cd factory
cargo install --path .
factory --version
```

Re-run `cargo install --path . --force` after updating the checkout.

## Configure Factory

Create the data and workspace directories, then copy the checked-in example:

```sh
mkdir -p ~/.factory
mkdir -p /absolute/path/to/factory-worktrees
cp examples/config.toml ~/.factory/config.toml
```

Edit only the repository and workspace paths to start. Each repository must be
a trusted local Git checkout with an authenticated GitHub remote. The workspace
must exist, be writable, and sit outside the repository and home-directory
root.

Validate the machine-specific configuration without network or runtime work:

```sh
factory validate
```

## Install the implementation workflow

In each target repository:

```sh
mkdir -p .factory/workflows
cp /path/to/factory/examples/implement-ready-ticket.md \
  .factory/workflows/implement-ready-ticket.md
```

Commit the workflow in a normal repository. It is versioned policy: Codex owns
ticket updates, worktree and branch creation, implementation, tests, diff
review, draft pull-request creation, CI repair, and handoff. Factory owns the
durable task, one claim, concurrency, supervision, cancellation, inspection,
deduplication, and recovery.

Check the resolved workflow catalog:

```sh
factory workflows
```

## Create the labels

Factory does not create or mutate label definitions. Create the two labels once
per repository:

```sh
gh label create factory:ready \
  --description "Implementation is authorised and sufficiently defined" \
  --color 0E8A16
gh label create factory:needs-review \
  --description "A human must review a question, decision, or green PR" \
  --color FBCA04
```

If a label already exists, inspect it with `gh label list` instead of replacing
it.

## Start and prove one ticket

Write one complete issue with a bounded outcome, acceptance criteria, and
verification. Ensure there is no existing implementation or pull request, then
apply the ready label:

```sh
gh issue edit ISSUE_NUMBER --add-label factory:ready
factory run
```

Keep the terminal open. The daemon polls GitHub, persists one task, atomically
claims it, and launches the authenticated Codex CLI. The workflow removes the
ready label when it takes ownership.

Use a second terminal to inspect durable state:

```sh
factory tasks
factory runs implement-ready-ticket
factory inspect RUN_ID
```

Exactly one task and run should represent the triggering issue revision. A
normal daemon restart must not create another implementation or pull request.
To exercise restart deduplication after the run is terminal, stop Factory with
Ctrl-C, start `factory run` again, wait through at least one poll, and confirm
the task/run counts and linked pull request remain unchanged.

Success means:

- one Codex run produced one ticket-numbered branch or worktree;
- one linked draft pull request contains a useful summary and verification;
- required CI and automated review are complete with no actionable feedback;
- the issue has `factory:needs-review` and a useful handoff comment;
- the pull request remains open, draft, and unmerged for a human.

## Inspect, cancel, and recover

List and inspect work without reading raw SQLite state:

```sh
factory tasks --json
factory runs --json
factory inspect RUN_ID --json
```

Request cancellation of a running Factory-owned process tree:

```sh
factory cancel RUN_ID
```

Ctrl-C stops polling and claiming, cancels active Codex process groups, records
cancelled outcomes, and leaves queued tasks durable. On restart Factory inspects
non-terminal runs. It leaves live owned work alone, otherwise stops a matching
orphan process group, closes the interrupted attempt, and permits at most two
durable recovery attempts. Recovery first resumes the stored Codex session and
falls back once to a fresh session with current issue, Git, worktree, pull
request, CI, and bounded prior evidence. Repeated failure remains inspectable;
Factory never merges as part of recovery.

## Troubleshooting

- Authentication errors: rerun `gh auth status` and `codex login status`.
- Invalid configuration: run `factory validate` and correct the reported path
  or concurrency constraint.
- Invalid workflows: run `factory workflows`; ticket workflow errors fail fast,
  while invalid scheduled workflows are reported and isolated.
- No task: confirm the issue is open, has `factory:ready`, belongs to the
  configured GitHub repository, and has changed since any earlier completed
  trigger.
- Failed run: use `factory inspect RUN_ID`; preserve the issue, branch, PR, and
  worktree so recovery or a human can reconcile current state.

## V1 acceptance evidence

The M3 exercise ran on 18 July 2026 from a clean clone of commit
`c0804e58e4159b7230116b8262ede837bb7973b2` on the pushed #10 branch. From that
checkout, `cargo install --path .` installed `factory 0.1.0` into an isolated
prefix. `factory validate` reported a valid configuration and `factory
workflows` resolved `implement-ready-ticket` as a valid `factory:ready` Codex
workflow with a four-hour timeout. Factory was started with local ChatGPT Codex
authentication and with `OPENAI_API_KEY` removed from its environment.

The real trigger was [issue #23](https://github.com/owainlewis/factory/issues/23).
The ledger was empty before the label was applied. Factory created task 1 and
run 1, with Codex session
`019f76f8-577d-7313-b421-a8680b6eeda7`. The run succeeded in 552,487 ms and
recorded [draft PR #24](https://github.com/owainlewis/factory/pull/24) at commit
`68d0a2efeb7b416923d6cc6aa51d0350d6ae4ab8`. The PR remained open, draft, and
unmerged. Its GitHub Actions `check` job passed, all requested local checks
passed, and a fresh independent Codex subagent review reported no actionable
findings. Issue #23 ended with only `factory:needs-review` and a handoff comment
linking the PR, summary, checks, review state, and limitations.

Restart deduplication was exercised after the terminal run. Before restart the
ledger contained one succeeded task and one succeeded run linked to PR #24.
After stopping the daemon, restarting it, and waiting through a five-second
poll, those counts and the linked PR were unchanged. GitHub still contained
one open PR for issue #23, so the restart created neither another Codex run nor
another implementation.

Observed limitations: the pre-merge proof necessarily checked out the pushed
#10 branch rather than released `main`; the same commit became the candidate
for the implementation PR. Isolated install, data, configuration, repository,
and workspace paths lived under a temporary macOS directory whose canonical
form was `/private/tmp`. GitHub had no submitted review on acceptance PR #24;
the required automated diff review was the fresh Codex subagent inside the one
supervised run. No model API key was used. PR #24 is intentionally left for a
human and must not be merged as part of the milestone closeout.
