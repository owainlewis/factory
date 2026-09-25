# factory

Run one task stage, check the result, and retry with feedback.

```sh
factory "Add pagination" --stage=build --check=test,lint,format,review --attempts=3
factory "Triage open GitHub issues" --stage=triage
```

A stage is a Markdown prompt. A check is a shell command or an agent prompt with
structured output. Factory has one fixed loop:

```text
setup once → worker → checks in order → done
               ↑         │
               └─ retry ─┘
```

## Install

Requires macOS or Linux, Python 3.11+, and Claude Code installed and authenticated
on PATH. Prompts using Claude's code-review skill need Claude Code 2.1.223+.

```sh
git clone https://github.com/owainlewis/factory.git
cd factory
uv tool install .
```

Or run `pip install .` in a virtual environment. An existing Claude Code login
works; setting `ANTHROPIC_API_KEY` uses API authentication instead.

## Configure a project

Create `.factory/config.toml` and the prompt files alongside it:

```toml
[stages.build]
prompt = "BUILD.md"
checks = ["test", "lint", "format", "review"]

[stages.triage]
prompt = "TRIAGE.md"
pre = ["gh auth status"]
tools = ["Read", "Glob", "Grep", "Bash"]

[checks.test]
command = "uv run pytest"

[checks.lint]
command = "uv run ruff check ."

[checks.format]
command = "uv run ruff format --check ."

[checks.review]
prompt = "REVIEW.md"
```

This repository's `.factory/` directory contains working examples. Write prompts
as ordinary Markdown: Factory appends JSON context containing the task, workspace,
and previous failure feedback. Checks also receive the worker's latest summary.
No prompt-template syntax is needed; braces and code examples remain literal.

Prompts are resolved relative to the configuration file and read before setup.
Run from the project directory or select it with `--cwd /path/to/project`.
`--config` selects another config file, relative to that starting directory.

`--check=test,lint,format,review` overrides the stage's configured checks in that order;
`--check=` explicitly disables them. Without an override the stage's checks are
used. No configured checks means success when the task agent completes.

## Checks and retries

Checks stop at the first unsuccessful result. A retry runs the worker again with
that feedback, then reruns **all** checks in order in the same workspace. Setup
never repeats. `--attempts` includes the first attempt and defaults to **1**;
opt into retries only for tasks that can safely revise their previous work.

Command checks use exit status:

| Exit | Meaning |
| --- | --- |
| 0 | Pass |
| 1 | Fixable failure: give the last 12 KB of output to the worker |
| Anything else | Stop for human attention |

Wrap commands that use different exit conventions. For example, pytest exit 2
(collection failure) stops rather than automatically treating it as a test failure.

Agent checks return this SDK schema, validated with strict Pydantic models:

```json
{"status": "retry", "feedback": "src/api.py:42: empty input causes an exception."}
```

`status` is `pass`, `retry`, or `stop`. Unsuccessful checks must provide feedback.
There is no free-form text parsing fallback. Missing or invalid structured output,
SDK/authentication errors, and timeouts stop the run rather than triggering repairs.
Schemas validate result structure, not the correctness of the agent's judgment.

Stage tools default to `Read`, `Glob`, `Grep`, `Write`, `Edit`, and `Bash`. Agent
checks default to `Read`, `Glob`, `Grep`, `Bash`, `Skill`, and `Agent`. Override
`tools` on any stage or prompt check. These tools are pre-approved; use trusted
configuration and prompts. Shell access is not a read-only sandbox. Checks should
inspect work without editing it or publishing anything.

## Setup and worktrees

Setup commands run once in the starting directory. The configured `cwd` is used
by the worker and every check. It must exist after setup completes.

```toml
[stages.build]
prompt = "BUILD.md"
pre = ["git worktree add -b factory/$FACTORY_RUN_ID \"$FACTORY_WORKSPACE\" HEAD"]
cwd = ".worktrees/{run_id}"
checks = ["test", "lint", "format", "review"]
```

`cwd` is relative to the starting directory (absolute paths also work).
`{run_id}` is replaced with a unique ID in `cwd` only. Commands receive:

- `FACTORY_RUN_ID`: the run ID.
- `FACTORY_ROOT`: the original absolute directory.
- `FACTORY_WORKSPACE`: the resolved absolute workspace.

Commands are passed unchanged to the shell. Quote environment variables normally.
For longer setup, use `pre = ["./scripts/create-worktree.sh"]`. Setup can also
install dependencies; dependencies and ignored files are not copied into worktrees.
Any nonzero setup exit stops immediately. A shell `cd` inside setup or an agent
cannot change the runner's working directory. There is no dynamic workspace handoff.

Factory contains no Git-specific orchestration. It doesn't require a repository,
create commits, push, open PRs, or merge. Those are separate tasks or scripts.
Keep publishing outside the retry loop to avoid duplicate side effects.

## Logs and limits

Every run prints its directory under `.factory/runs/<run-id>/`:

```text
run.json
setup-1.log
attempt-1/
  stage.log
  summary.md
  check-1.log
  check-1.json
```

Add `.factory/runs/` and, if used, `.worktrees/` to `.gitignore`. Workspace and logs
are preserved on success, failure, and interruption. There is no automatic cleanup
or resume. Logs may contain task and repository content.

`--timeout` limits each command or agent invocation (600 seconds by default).
`--max-turns` limits each agent invocation (30 by default). `--model` selects the
Claude model. Omit the task argument to enter it interactively.

## Migration

This replaces the fixed plan/build/review/PR pipeline. `--stage` selects one
worker; `--check` now takes configured check names rather than a shell command.
`--cwd` replaces `--repo`. Automatic GitHub issue fetching and `--pr` are removed:
a prompt can use `gh` when needed. Built-in prompts moved to the project's
`.factory/` directory; copy them to another project to reuse them.

## Development

```sh
uv sync --locked
uv run pytest
uv run ruff check .
uv run ruff format --check .
```

Tests use real command processes, temporary directories, and fake agents. No API
key or paid model call is required.
