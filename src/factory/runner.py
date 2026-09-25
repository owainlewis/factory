"""Setup once, then repeat work and acceptance checks within a fixed workspace."""

import asyncio
import json
import os
import shutil
import signal
from pathlib import Path
from uuid import uuid4

from claude_agent_sdk import ClaudeAgentOptions, ResultMessage, query
from pydantic import ValidationError

from factory.config import CheckResult, Config


class FactoryError(Exception):
    """A run needs human attention."""


async def shell(command: str, cwd: Path, log: Path, timeout: int, env: dict) -> int:
    with log.open("w") as output:
        process = await asyncio.create_subprocess_shell(
            command,
            cwd=cwd,
            env={**os.environ, **env},
            stdout=output,
            stderr=asyncio.subprocess.STDOUT,
            start_new_session=True,
        )
        try:
            return await asyncio.wait_for(process.wait(), timeout)
        except (TimeoutError, asyncio.CancelledError):
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            await process.wait()
            raise


async def agent(
    instructions: str,
    context: dict,
    tools: list[str],
    cwd: Path,
    log: Path,
    args,
    env: dict,
    *,
    check: bool = False,
):
    options = ClaudeAgentOptions(
        cwd=str(cwd),
        tools=tools,
        allowed_tools=tools,
        permission_mode="dontAsk",
        cli_path=shutil.which("claude"),
        model=args.model,
        max_turns=args.max_turns,
        env={**env, "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS": "1"},
        strict_mcp_config=True,
        setting_sources=[],
        output_format={"type": "json_schema", "schema": CheckResult.model_json_schema()}
        if check
        else None,
    )
    result = None
    async with asyncio.timeout(args.timeout):
        with log.open("w") as output:
            async for message in query(
                prompt=instructions + "\n\nRun context (JSON data):\n" + json.dumps(context),
                options=options,
            ):
                output.write(f"{message}\n")
                output.flush()
                if isinstance(message, ResultMessage):
                    result = message
    if result is None or result.is_error or result.subtype != "success":
        raise FactoryError(f"Agent failed; see {log}")
    if result.permission_denials:
        raise FactoryError(f"Agent tool permissions were denied; see {log}")
    if check:
        try:
            return CheckResult.model_validate(result.structured_output)
        except ValidationError as exc:
            raise FactoryError(f"Missing or invalid structured check result; see {log}") from exc
    if not result.result:
        raise FactoryError(f"Agent returned no result; see {log}")
    return result.result


def tail(path: Path) -> str:
    with path.open("rb") as stream:
        stream.seek(max(0, path.stat().st_size - 12000))
        return stream.read().decode(errors="replace")


async def run(args) -> Path:
    root = args.cwd.resolve()
    config_path = (root / args.config).resolve()
    config = Config.load(config_path)
    if args.stage not in config.stages:
        raise FactoryError(f"Unknown stage: {args.stage}")
    stage = config.stages[args.stage]
    names = stage.checks
    if args.check is not None:
        names = [name.strip() for name in args.check.split(",")] if args.check else []
    # Resolve every check and read prompts before setup can change the workspace.
    instructions = (config_path.parent / stage.prompt).read_text()
    checks = []
    for name in names:
        if name not in config.checks:
            raise FactoryError(f"Unknown check: {name}")
        check = config.checks[name]
        text = (config_path.parent / check.prompt).read_text() if check.prompt else None
        checks.append((name, check, text))

    run_id = uuid4().hex[:12]
    workspace = (root / stage.cwd.replace("{run_id}", run_id)).resolve()
    logs = root / ".factory" / "runs" / run_id
    logs.mkdir(parents=True)
    env = {"FACTORY_RUN_ID": run_id, "FACTORY_ROOT": str(root), "FACTORY_WORKSPACE": str(workspace)}
    state = {
        "task": args.task,
        "stage": args.stage,
        "checks": names,
        "workspace": str(workspace),
        "run_id": run_id,
    }

    def status(value: str, **details):
        state.update(status=value, **details)
        (logs / "run.json").write_text(json.dumps(state, indent=2) + "\n")
        print(f"[{value}] {details.get('message', '')}", flush=True)

    print(f"Run: {logs}\nWorkspace: {workspace}", flush=True)
    try:
        status("setup")
        for index, command in enumerate(stage.pre, start=1):
            log = logs / f"setup-{index}.log"
            if await shell(command, root, log, args.timeout, env):
                raise FactoryError(f"Setup failed; see {log}")
        if not workspace.is_dir():
            raise FactoryError(f"Workspace does not exist: {workspace}")

        feedback = ""
        for attempt in range(1, args.attempts + 1):
            attempt_logs = logs / f"attempt-{attempt}"
            attempt_logs.mkdir()
            context = {"task": args.task, "feedback": feedback, "workspace": str(workspace)}
            status("working", attempt=attempt)
            summary = await agent(
                instructions, context, stage.tools, workspace, attempt_logs / "stage.log", args, env
            )
            (attempt_logs / "summary.md").write_text(summary + "\n")
            context["summary"] = summary
            result = CheckResult(status="pass", feedback="")
            for index, (name, check, text) in enumerate(checks, start=1):
                status("checking", message=name)
                log = attempt_logs / f"check-{index}.log"
                if check.command:
                    code = await shell(check.command, workspace, log, args.timeout, env)
                    result = CheckResult(
                        status={0: "pass", 1: "retry"}.get(code, "stop"),
                        feedback=f"Check {name} exited {code}.\n{tail(log)}" if code else "",
                    )
                else:
                    result = await agent(
                        text, context, check.tools, workspace, log, args, env, check=True
                    )
                (attempt_logs / f"check-{index}.json").write_text(
                    result.model_dump_json(indent=2) + "\n"
                )
                if result.status != "pass":
                    feedback = f"Check {name}: {result.feedback}"
                    break
            if result.status == "pass":
                status("passed", message="Task and checks completed.")
                return logs
            if result.status == "stop":
                raise FactoryError(feedback)
        raise FactoryError(f"Attempt limit reached ({args.attempts}).\n{feedback}")
    except BaseException as exc:
        status("needs_attention", message=str(exc) or type(exc).__name__)
        raise
