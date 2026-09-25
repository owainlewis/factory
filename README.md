# factory

A minimal software factory using the Claude Agent SDK.

```text
task → plan → build → check → review → PR
                ↑        │       │
                └────────┴───────┘
                   repair feedback
```

Claude reads the repository, makes a plan, and edits code. Python controls the
workflow, allows up to three build attempts, and optionally opens a pull request.

## Install

Requires macOS or Linux, Python 3.11+, Git, and Claude Code 2.1.223+ installed
on PATH. Factory uses that CLI through the Agent SDK so the built-in
`/code-review` command is available. Authenticate with your Claude Code login
or `ANTHROPIC_API_KEY`. GitHub issue input and PR creation also need `gh`
authenticated with `gh auth login`.

```sh
git clone https://github.com/owainlewis/factory.git
cd factory
uv sync
uv tool install .
```

Alternatively: `pip install .` in a virtual environment.

## Use

Run against a repository with at least one commit and a clean working tree.
Configure Git's `user.name` and `user.email` so Factory can commit.

```sh
cd /path/to/your/project

factory "Add a slugify function with tests" --check "uv run pytest"

factory https://github.com/owner/repo/issues/42 --check "npm ci && npm test"

# Prompt for the task interactively.
factory --check "make test"

# Push and open a PR after local checks and Claude Code review pass.
factory "Fix empty input handling" --check "uv run pytest" --pr
```

`--repo /path/to/project` targets another checkout. `--model` selects a Claude
model. `--max-turns` limits each agent stage (default 30); `--timeout` limits
each agent stage and validation command (default 600 seconds). `--attempts` sets
the total number of build attempts, including the first (default 3).
Run `factory --help` for all options.

The check runs in a fresh worktree: ignored files, local virtual environments,
and installed dependencies are not copied. Include setup in the check when
needed, for example `uv sync --locked && uv run pytest`.

## What happens

1. **Plan:** a read-only Claude session inspects the repository and writes a plan.
2. **Build:** a fresh session receives the task, fixed plan, and previous feedback.
   Factory commits each attempt to `factory/<run-id>`.
3. **Check:** your required `--check` command runs against that commit. Nonzero
   exit output goes back to the builder, bounded to its last 12,000 characters.
4. **Review:** a fresh session runs Claude's built-in `/code-review` against the
   full base-to-candidate diff. Actionable findings go back to the builder.
   Every repair must pass checks and a new review.
5. **PR:** with `--pr`, push the exact checked and reviewed commit to `origin`
   and open a PR targeting the branch you started on. Without it, leave the
   result local. GitHub CI and human approval follow; Factory does not merge.

There is one review stage; the builder has no review subagent. Claude's built-in
review may use its own internal agents and shell commands. Factory accepts only
an explicit JSON findings list from the command: an empty list clears the review,
and findings trigger repairs. Missing, malformed, failed, or incomplete reviews
never count as approval. If a Claude version changes the output format, the run
stops for human attention. This is model-generated judgment, not a guarantee
of correctness.

After three unsuccessful attempts the run needs human attention. Timeouts,
SDK/authentication errors, unexpected checkout changes, and a builder that makes
no changes stop immediately. GitHub CI failures are handled manually in this
version. `--merge` has been removed in favor of PRs and human approval.

Issue URLs are expanded into their title and body using `gh issue view`. Factory
always builds in the local repository you selected; an issue URL does not clone
or switch repositories. Other input is treated as a literal task prompt.

Each run prints its artifact directory under the repository's Git directory:

```text
factory/<run-id>/
  run.json         # current stage, task, branch, commit, errors
  plan.md
  plan.log
  attempt-1/       # one directory per build attempt
    build.log
    summary.md
    diff.patch
    validation.log
    review.log    # when checks pass
    review.json
  pr.md           # when --pr is used
```

The editable worktree lives beside the original repository at
`.<repo-name>-factory-<run-id>/`; its full path is printed and saved in `run.json`.

Failures return a nonzero exit code, record `needs_attention` in `run.json`,
and preserve every attempt and the worktree. Inspect the latest feedback before
continuing manually. There is no automatic resume. Logs may contain repository
and task content; they stay local and are not included in generated commits.

After review, clean up a run with `git worktree remove <printed-worktree-path>`.
Delete its branch with `git branch -d factory/<run-id>` once merged. Artifact
logs remain available until you remove the run directory.

## Scope

This is a small local CLI, not a job server. It has no queue or CI repair loop.
The builder can read and edit files but cannot run shell commands or install
dependencies. The orchestrator runs your check; the reviewer also has shell
access for inspection. Factory checks that review leaves the checkout unchanged.

A worktree isolates changes, not process access. Use trusted repositories and
validation commands. Passing tests is not proof that the task is correct;
review the diff before merging when the change warrants it.

## Development

```sh
uv sync --locked
uv run pytest
uv run ruff check .
uv run ruff format --check .
```

Tests use temporary real Git repositories and a fake agent, so they require no
API key or paid model calls. SDK documentation:
[Claude Agent SDK for Python](https://github.com/anthropics/claude-agent-sdk-python).
