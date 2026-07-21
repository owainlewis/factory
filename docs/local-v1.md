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

## Initialize a trusted repository

Run the explicit initializer from the repository Factory should manage:

```sh
cd /absolute/path/to/trusted/repository
factory init
```

The command creates `.factory/config.toml` and `.factory/workflows/` in the
repository. It derives a stable state directory for this clone under the user
data directory and creates its external worktree directory there. It does not
install a workflow, inspect or mutate GitHub labels, commit files, start the
daemon, launch Codex, or merge pull requests.

Initialization is idempotent. Preview it without writes with:

```sh
factory init --check
```

To initialize a repository without changing directory, pass its path:

```sh
factory init --repository /absolute/path/to/repository
```

Create an implementation workflow from explicit prompt input. For a substantial
policy, put only the Markdown prompt body in a file and pass its path:

```sh
factory workflow create implement-ready-ticket \
  --label factory:ready \
  --timeout 4h \
  --prompt-file /absolute/path/to/implementation-policy.md
```

The command does not open `$EDITOR`. Use `--prompt` for short policies or
`--prompt-file -` to read from standard input. A label-triggered workflow
creates its missing trigger label. Create any additional labels referenced by
the policy, such as `factory:needs-review`, separately.

Review and commit the generated policy in the target repository:

```sh
git add .factory/workflows/implement-ready-ticket.md
git commit -m "chore: configure Factory"
```

Validate the machine-specific configuration without runtime work:

```sh
factory validate
```

The workflow is versioned policy: Codex owns
ticket updates, worktree and branch creation, implementation, tests, diff
review, draft pull-request creation, CI repair, and handoff. Factory owns the
durable task, one claim, concurrency, supervision, cancellation, inspection,
deduplication, and recovery.

Check the resolved workflow catalog:

```sh
factory workflows
```

## Start and prove one ticket

Write one complete issue with a bounded outcome, acceptance criteria, and
verification. Ensure there is no existing implementation or pull request.
Confirm your GitHub login appears in `[github].trusted_approvers`, then approve
the exact ticket and workflow revision:

```sh
factory approve ISSUE_NUMBER
factory daemon
```

Keep the terminal open. The daemon polls GitHub, persists one task, atomically
claims it, re-fetches the issue and approval evidence, consumes that approval
once, removes the ready label, and launches the authenticated Codex CLI. A
directly applied `factory:ready` label has no authority and launches nothing.

Use a second terminal to inspect durable state:

```sh
factory tasks
factory runs implement-ready-ticket
factory inspect RUN_ID
```

Exactly one task and run should represent the triggering issue revision. A
normal daemon restart must not create another implementation or pull request.
To exercise restart deduplication after the run is terminal, stop Factory with
Ctrl-C, start `factory daemon` again, wait through at least one poll, and confirm
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

Use a non-executing smoke test before starting the daemon:

```sh
factory run --once
```

This evaluates one schedule tick, polls GitHub once, and persists matching
tasks without claiming them or launching Codex. If nothing matches, it uses no
model tokens.

## Troubleshooting

- Authentication errors: rerun `gh auth status` and `codex login status`.
- Invalid configuration: run `factory validate` and correct the reported
  repository-local policy or concurrency constraint.
- Legacy cutover blocked: stop the old global daemon and finish or cancel its
  queued or running work for this repository. Factory leaves the old database
  untouched and does not import its history.
- Missing setup: run `factory init --check`, then `factory init` to create only
  the missing resources.
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
