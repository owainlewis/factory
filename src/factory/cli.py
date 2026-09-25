"""Code controls the workflow; Claude plans and edits files."""

import argparse
import asyncio
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import uuid
from pathlib import Path

from claude_agent_sdk import ClaudeAgentOptions, ResultMessage, query
from pydantic import BaseModel, ConfigDict, Field, ValidationError


class FactoryError(Exception):
    """An actionable pipeline failure."""


def command(cwd: Path, *args: str) -> str:
    result = subprocess.run(args, cwd=cwd, capture_output=True, text=True, check=False)
    if result.returncode:
        raise FactoryError(result.stderr.strip() or result.stdout.strip() or f"Failed: {args}")
    return result.stdout.strip()


def git(cwd: Path, *args: str) -> str:
    return command(cwd, "git", *args)


def resolve_task(value: str, cwd: Path) -> str:
    """A GitHub issue URL is fetched with the user's existing gh authentication."""
    if not re.fullmatch(r"https://github\.com/[^/]+/[^/]+/issues/[0-9]+/?", value):
        return value
    issue = json.loads(command(cwd, "gh", "issue", "view", value, "--json", "title,body,url"))
    return f"{issue['title']}\n\n{issue['body'] or ''}\n\nSource: {issue['url']}"


def prompt(name: str, **context: str) -> str:
    return (Path(__file__).parent / "prompts" / name).read_text().format(**context)


class Finding(BaseModel):
    model_config = ConfigDict(strict=True, extra="forbid", str_strip_whitespace=True)

    file: str = Field(min_length=1)
    line: int = Field(ge=1)
    summary: str = Field(min_length=1)
    failure_scenario: str = Field(min_length=1)


class Review(BaseModel):
    model_config = ConfigDict(strict=True, extra="forbid")

    completed: bool
    findings: list[Finding]

    def feedback(self) -> dict:
        if not self.completed:
            status = "needs_attention"
        elif self.findings:
            status = "needs_fixes"
        else:
            status = "clear"
        return {
            "status": status,
            "findings": [
                f"{f.file}:{f.line}: {f.summary}\n{f.failure_scenario}" for f in self.findings
            ],
        }


async def agent(stage: str, prompt: str, worktree: Path, logs: Path, args):
    tools = ["Read", "Glob", "Grep"]
    if stage == "build":
        tools += ["Write", "Edit"]
    if stage == "review":
        tools += ["Bash", "Agent", "Skill"]
    options = ClaudeAgentOptions(
        cwd=str(worktree),
        tools=tools,
        allowed_tools=tools,
        disallowed_tools=["Write", "Edit", "NotebookEdit"] if stage == "review" else [],
        cli_path=shutil.which("claude"),
        env={"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS": "1"},
        output_format={"type": "json_schema", "schema": Review.model_json_schema()}
        if stage == "review"
        else None,
        strict_mcp_config=True,
        setting_sources=[],
        permission_mode="dontAsk",
        model=args.model,
        max_turns=args.max_turns,
    )
    result = None
    async with asyncio.timeout(args.timeout):
        with (logs / f"{stage}.log").open("w") as log:
            async for message in query(prompt=prompt, options=options):
                log.write(f"{message}\n")
                log.flush()
                if isinstance(message, ResultMessage):
                    result = message
    # Drain the SDK stream so its subprocess and async context close in this task.
    if result is None:
        raise FactoryError(f"{stage}: agent ended without a result")
    if result.is_error or result.subtype != "success":
        raise FactoryError(f"{stage}: {result.result or result.subtype}")
    if result.permission_denials:
        raise FactoryError(f"{stage}: tool permissions were denied; see {logs / f'{stage}.log'}")
    if stage == "review":
        try:
            return Review.model_validate(result.structured_output).feedback()
        except ValidationError as exc:
            raise FactoryError("Review returned missing or invalid structured output.") from exc
    if not result.result:
        raise FactoryError(f"{stage}: agent returned an empty result")
    return result.result


async def validate(check: str, worktree: Path, log_path: Path, timeout: int) -> bool:
    with log_path.open("w") as log:
        process = await asyncio.create_subprocess_shell(
            check,
            cwd=worktree,
            stdout=log,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        try:
            await asyncio.wait_for(process.wait(), timeout=timeout)
        except (TimeoutError, asyncio.CancelledError):
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            await process.wait()
            raise
    return process.returncode == 0


def unchanged(worktree: Path, commit: str, stage: str):
    if git(worktree, "status", "--porcelain") or git(worktree, "rev-parse", "HEAD") != commit:
        raise FactoryError(f"{stage} changed the checkout; human input is needed.")


async def pipeline(args) -> Path:
    repo = Path(git(args.repo, "rev-parse", "--show-toplevel"))
    if git(repo, "status", "--porcelain"):
        raise FactoryError("Start with a clean working tree (commit or stash your changes).")
    base_branch = git(repo, "symbolic-ref", "--short", "HEAD")
    base = git(repo, "rev-parse", "HEAD")
    task = resolve_task(args.task, repo)
    run_id = uuid.uuid4().hex[:12]
    branch = f"factory/{run_id}"
    common = Path(git(repo, "rev-parse", "--path-format=absolute", "--git-common-dir"))
    logs = common / "factory" / run_id
    logs.mkdir(parents=True)
    # Claude protects paths inside .git, so keep editable files outside Git metadata.
    worktree = repo.parent / f".{repo.name}-factory-{run_id}"
    state = {"task": task, "branch": branch, "base": base, "worktree": str(worktree)}

    def status(stage: str, **details):
        state.update(stage=stage, **details)
        (logs / "run.json").write_text(json.dumps(state, indent=2) + "\n")
        print(f"[{stage}] {details.get('message', '')}", flush=True)

    print(f"Branch: {branch}\nRun: {logs}\nWorktree: {worktree}", flush=True)
    try:
        git(repo, "worktree", "add", "-b", branch, str(worktree), base)
        status("plan")
        plan = await agent(
            "plan",
            prompt("PLAN.md", task=task),
            worktree,
            logs,
            args,
        )
        (logs / "plan.md").write_text(plan + "\n")

        feedback = "None; this is the first attempt."
        candidate = base
        for attempt in range(1, args.attempts + 1):
            attempt_logs = logs / f"attempt-{attempt}"
            attempt_logs.mkdir()
            status("build", attempt=attempt)
            summary = await agent(
                "build",
                prompt("BUILD.md", task=task, plan=plan, feedback=feedback, check=args.check),
                worktree,
                attempt_logs,
                args,
            )
            (attempt_logs / "summary.md").write_text(summary + "\n")
            if git(worktree, "rev-parse", "HEAD") != candidate:
                raise FactoryError("Build unexpectedly changed Git history.")
            git(worktree, "add", "--all")
            if not git(worktree, "diff", "--cached", "--name-only"):
                raise FactoryError("Build produced no changes; human input is needed.")
            (attempt_logs / "diff.patch").write_text(git(worktree, "diff", "--cached") + "\n")
            git(worktree, "diff", "--cached", "--check")
            git(
                worktree,
                "commit",
                "-m",
                f"factory: {task.splitlines()[0][:100]} (attempt {attempt})",
            )
            candidate = git(worktree, "rev-parse", "HEAD")

            status("validate", commit=candidate)
            passed = await validate(
                args.check, worktree, attempt_logs / "validation.log", args.timeout
            )
            unchanged(worktree, candidate, "Validation")
            if not passed:
                output = (attempt_logs / "validation.log").read_text(errors="replace")
                feedback = f"Validation failed. Output (last 12000 characters):\n{output[-12000:]}"
                continue

            status("review")
            report = await agent(
                "review",
                prompt("VERIFY.md", base=base, commit=candidate),
                worktree,
                attempt_logs,
                args,
            )
            (attempt_logs / "review.json").write_text(json.dumps(report, indent=2) + "\n")
            unchanged(worktree, candidate, "Review")
            if report["status"] == "needs_attention":
                raise FactoryError("Review could not complete; human input is needed.")
            if report["status"] == "needs_fixes":
                feedback = "Fix these review findings:\n" + "\n".join(report["findings"])
                continue
            break
        else:
            raise FactoryError(
                f"Attempt limit reached ({args.attempts}). Latest feedback:\n{feedback}"
            )

        if args.pr:
            status("publish")
            body = logs / "pr.md"
            body.write_text(
                f"## Task\n\n{task}\n\n## Changes\n\n{summary}\n\n"
                f"## Validation\n\n- Local check passed: `{args.check}`\n"
                f"- Claude Code review clear for `{candidate}`\n"
                "- GitHub CI and human review are still required before merging.\n"
            )
            git(worktree, "push", "origin", f"{candidate}:refs/heads/{branch}")
            url = command(
                worktree,
                "gh",
                "pr",
                "create",
                "--base",
                base_branch,
                "--head",
                branch,
                "--title",
                task.splitlines()[0][:100],
                "--body-file",
                str(body),
            )
            status("pr_open", url=url, message=url)
        else:
            status("ready", message=f"Validated and reviewed. Inspect: git diff {base} {candidate}")
        return logs
    except BaseException as exc:
        status("needs_attention", message=str(exc) or type(exc).__name__)
        raise


def positive_int(value: str) -> int:
    number = int(value)
    if number < 1:
        raise argparse.ArgumentTypeError("must be greater than zero")
    return number


def parser() -> argparse.ArgumentParser:
    cli = argparse.ArgumentParser(description="Plan → Build → Check → Review → PR with Claude")
    cli.add_argument("task", nargs="?", help="Task prompt or GitHub issue URL; prompts if omitted")
    cli.add_argument(
        "--repo", type=Path, default=Path.cwd(), help="Repository (default: current directory)"
    )
    cli.add_argument(
        "--check", required=True, help="Shell command that must pass, e.g. 'uv run pytest'"
    )
    cli.add_argument(
        "--pr", action="store_true", help="Push and open a PR after checks and review pass"
    )
    cli.add_argument(
        "--attempts", type=positive_int, default=3, help="Total build attempts (default: 3)"
    )
    cli.add_argument("--model", default=None, help="Claude model (default: SDK default)")
    cli.add_argument(
        "--max-turns",
        type=positive_int,
        default=30,
        help="Agent turn limit per stage (default: 30)",
    )
    cli.add_argument(
        "--timeout",
        type=positive_int,
        default=600,
        help="Timeout per agent stage or check, in seconds (default: 600)",
    )
    return cli


def main() -> int:
    args = parser().parse_args()
    try:
        args.task = (
            args.task if args.task is not None else input("Task or GitHub issue URL: ")
        ).strip()
        if not args.task:
            raise FactoryError("Task cannot be empty.")
        if not shutil.which("claude"):
            raise FactoryError("Install Claude Code 2.1.223+ on PATH for built-in code review.")
        version = command(args.repo, "claude", "--version")
        match = re.search(r"(\d+)\.(\d+)\.(\d+)", version)
        if not match or tuple(map(int, match.groups())) < (2, 1, 223):
            raise FactoryError("Update Claude Code to 2.1.223+ for built-in code review.")
        asyncio.run(pipeline(args))
    except KeyboardInterrupt:
        print("\nInterrupted. Run artifacts and worktree are preserved.", file=sys.stderr)
        return 130
    except TimeoutError:
        print("factory: Stage timed out; see the run logs.", file=sys.stderr)
        return 1
    except (FactoryError, OSError, EOFError) as exc:
        print(f"factory: {exc}", file=sys.stderr)
        return 1
    except Exception as exc:  # noqa: BLE001 -- report SDK errors at the CLI boundary
        print(f"factory: {type(exc).__name__}: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
