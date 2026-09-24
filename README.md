# factory

A minimal software factory using the Claude Agent SDK.

```text
task → plan → build → validate → merge
```

Claude reads the repository, makes a plan, and edits code. Python controls the
workflow, runs your validation command, and optionally merges the result.

## Install

Requires macOS or Linux, Python 3.11+, Git, and Claude authentication (an existing Claude Code
login or `ANTHROPIC_API_KEY`). GitHub issue input also needs `gh` authenticated
with `gh auth login`.

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

# Merge into the branch you started on after validation passes.
factory "Fix empty input handling" --check "uv run pytest" --merge
```

`--repo /path/to/project` targets another checkout. `--model` selects a Claude
model. `--max-turns` limits each agent stage (default 30); `--timeout` limits
validation (default 600 seconds). Run `factory --help` for all options.

The check runs in a fresh worktree: ignored files, local virtual environments,
and installed dependencies are not copied. Include setup in the check when
needed, for example `uv sync --locked && uv run pytest`.

## What happens

1. **Plan:** a read-only Claude session inspects the repository and writes a plan.
2. **Build:** a fresh Claude session receives the task and plan, edits files, and
   adds tests. Factory commits the change to `factory/<run-id>`.
3. **Validate:** your required `--check` shell command runs against that commit.
   A nonzero exit, timeout, or checkout modification stops the run.
4. **Merge:** with `--merge`, Factory fast-forwards the original branch only if
   that checkout is still clean and unchanged. Otherwise the validated commit
   stays on its branch for review. Nothing is pushed to GitHub.

Issue URLs are expanded into their title and body using `gh issue view`. Factory
always builds in the local repository you selected; an issue URL does not clone
or switch repositories. Other input is treated as a literal task prompt.

Each run prints its artifact directory under the repository's Git directory:

```text
factory/<run-id>/
  run.json         # current stage, task, branch, commit, errors
  plan.md
  summary.md
  plan.log
  build.log
  validation.log
```

The editable worktree lives beside the original repository at
`.<repo-name>-factory-<run-id>/`; its full path is printed and saved in `run.json`.

Failures return a nonzero exit code and preserve the worktree and logs. There is
no automatic retry loop. Inspect the diff and logs, fix the issue, and rerun the
check in the worktree before merging manually. Logs may contain repository and
task content; they stay local and are not included in generated commits.

After review, clean up a run with `git worktree remove <printed-worktree-path>`.
Delete its branch with `git branch -d factory/<run-id>` once merged. Artifact
logs remain available until you remove the run directory.

## Scope

This is a small local CLI, not a job server. It has no queue, PR automation, or
automatic review agent. The agent can read and edit files; shell commands are
run by the orchestrator through your check. It cannot install dependencies or
run exploratory shell commands during the build stage.

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
