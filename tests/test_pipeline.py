import asyncio
import json
import shlex
import sys
from pathlib import Path

import pytest
from pydantic import ValidationError

from factory import cli, runner
from factory.config import Check, CheckResult, Config


def command(code):
    return f"{shlex.quote(sys.executable)} -c {shlex.quote(code)}"


def configure(root, *, pre=(), cwd=".", check=None, agent_check=False, stage="build"):
    folder = root / ".factory"
    folder.mkdir(exist_ok=True)
    (folder / "TASK.md").write_text("Do the task; preserve literal {braces}.")
    (folder / "CHECK.md").write_text("Check the work using the output schema.")
    text = f"""[stages.{stage}]
prompt = "TASK.md"
pre = {json.dumps(list(pre))}
cwd = {json.dumps(cwd)}
checks = {json.dumps(["test"] if check else [])}
"""
    if check:
        text += f"\n[checks.test]\ncommand = {json.dumps(check)}\n"
    if agent_check:
        text += '\n[checks.review]\nprompt = "CHECK.md"\n'
    (folder / "config.toml").write_text(text)


def options(root, *extra):
    return cli.parser().parse_args(["Add a greeting", "--cwd", str(root), *extra])


def state(logs):
    return json.loads((logs / "run.json").read_text())


@pytest.fixture
def fake_agent(monkeypatch):
    calls = []

    async def agent(instructions, context, tools, cwd, log, args, env, *, check=False):
        calls.append((check, cwd, dict(context)))
        if check:
            return CheckResult(status="pass", feedback="")
        (cwd / "hello.txt").write_text("hello\n")
        return "Added greeting"

    monkeypatch.setattr(runner, "agent", agent)
    return calls


def test_stage_without_checks_or_git(tmp_path, fake_agent):
    configure(tmp_path, stage="triage")
    logs = asyncio.run(runner.run(options(tmp_path, "--stage", "triage")))
    assert state(logs)["status"] == "passed"
    assert len(fake_agent) == 1
    assert not (tmp_path / ".git").exists()


def test_setup_once_and_retry_in_shared_workspace(tmp_path, monkeypatch):
    setup = command(
        "import os; from pathlib import Path; "
        "Path('setup-count').write_text('once'); "
        "Path(os.environ['FACTORY_WORKSPACE']).mkdir(parents=True)"
    )
    check = command("from pathlib import Path; assert Path('hello.txt').read_text() == 'hello'")
    configure(tmp_path, pre=[setup], cwd=".worktrees/{run_id}", check=check, agent_check=True)
    calls = []

    async def agent(instructions, context, tools, cwd, log, args, env, *, check=False):
        calls.append((check, cwd))
        assert cwd == Path(env["FACTORY_WORKSPACE"])
        if check:
            return CheckResult(status="pass", feedback="")
        builds = sum(not c[0] for c in calls)
        if builds == 2:
            assert "exited 1" in context["feedback"]
        (cwd / "hello.txt").write_text("wrong" if builds == 1 else "hello")
        return "Built"

    monkeypatch.setattr(runner, "agent", agent)
    logs = asyncio.run(runner.run(options(tmp_path, "--check", "test,review", "--attempts", "3")))
    assert [c[0] for c in calls] == [False, False, True]
    assert len({c[1] for c in calls}) == 1
    assert len(list(logs.glob("setup-*.log"))) == 1
    assert (tmp_path / "setup-count").read_text() == "once"
    assert state(logs)["attempt"] == 2
    assert not (tmp_path / "hello.txt").exists()


def test_review_feedback_reruns_worker_and_all_checks(tmp_path, monkeypatch):
    configure(tmp_path, check=command("print('checked')"), agent_check=True)
    events = []

    async def agent(instructions, context, tools, cwd, log, args, env, *, check=False):
        events.append(check)
        if check:
            return (
                CheckResult(status="retry", feedback="Handle empty input")
                if events.count(True) == 1
                else CheckResult(status="pass", feedback="")
            )
        if events.count(False) == 2:
            assert "Handle empty input" in context["feedback"]
        return "Built"

    monkeypatch.setattr(runner, "agent", agent)
    logs = asyncio.run(runner.run(options(tmp_path, "--check", "test,review", "--attempts", "2")))
    assert events == [False, True, False, True]
    assert (logs / "attempt-2/check-1.log").read_text().strip() == "checked"


@pytest.mark.parametrize("code,attempts", [(1, 3), (2, 1), (127, 1)])
def test_command_failure_policy(tmp_path, fake_agent, code, attempts):
    configure(tmp_path, check=command(f"raise SystemExit({code})"))
    with pytest.raises(runner.FactoryError):
        asyncio.run(runner.run(options(tmp_path, "--attempts", "3")))
    assert len(fake_agent) == attempts
    logs = next((tmp_path / ".factory/runs").iterdir())
    assert state(logs)["status"] == "needs_attention"


def test_setup_failure_never_runs_agent(tmp_path, fake_agent):
    configure(tmp_path, pre=[command("raise SystemExit(1)")])
    with pytest.raises(runner.FactoryError, match="Setup failed"):
        asyncio.run(runner.run(options(tmp_path, "--attempts", "3")))
    assert fake_agent == []


def test_missing_workspace_stops(tmp_path, fake_agent):
    configure(tmp_path, cwd="missing/{run_id}")
    with pytest.raises(runner.FactoryError, match="Workspace does not exist"):
        asyncio.run(runner.run(options(tmp_path)))
    assert fake_agent == []


def test_unknown_check_fails_before_setup(tmp_path, fake_agent):
    configure(tmp_path, pre=[command("from pathlib import Path; Path('marker').touch()")])
    with pytest.raises(runner.FactoryError, match="Unknown check"):
        asyncio.run(runner.run(options(tmp_path, "--check", "missing")))
    assert not (tmp_path / "marker").exists()
    assert not (tmp_path / ".factory/runs").exists()


def test_explicit_empty_check_override(tmp_path, fake_agent):
    configure(tmp_path, check=command("raise SystemExit(1)"))
    logs = asyncio.run(runner.run(options(tmp_path, "--check=")))
    assert state(logs)["status"] == "passed"
    assert not list(logs.glob("attempt-1/check-*"))


def test_agent_stop_is_not_retried(tmp_path, monkeypatch):
    configure(tmp_path, agent_check=True)
    calls = []

    async def agent(*args, check=False):
        calls.append(check)
        return CheckResult(status="stop", feedback="Authentication required") if check else "Built"

    monkeypatch.setattr(runner, "agent", agent)
    with pytest.raises(runner.FactoryError, match="Authentication required"):
        asyncio.run(runner.run(options(tmp_path, "--check", "review", "--attempts", "3")))
    assert calls == [False, True]


def test_timeout_stops_without_retry(tmp_path, fake_agent):
    configure(tmp_path, check=command("import time; time.sleep(30)"))
    with pytest.raises(TimeoutError):
        asyncio.run(runner.run(options(tmp_path, "--timeout", "1", "--attempts", "3")))
    assert len(fake_agent) == 1


@pytest.mark.parametrize("data", [{}, {"command": "true", "prompt": "a.md"}, {"command": ""}])
def test_invalid_check_config(data):
    with pytest.raises(ValidationError):
        Check.model_validate(data)


def test_unknown_config_fields_rejected():
    with pytest.raises(ValidationError):
        Config.model_validate({"stages": {"build": {"prompt": "a.md", "cheks": ["test"]}}})


@pytest.mark.parametrize(
    "data",
    [
        None,
        {},
        {"status": "pass"},
        {"status": "retry", "feedback": " "},
        {"status": "unknown", "feedback": "why"},
        {"status": "pass", "feedback": "", "extra": 1},
    ],
)
def test_check_schema_rejects_invalid_results(data):
    with pytest.raises(ValidationError):
        CheckResult.model_validate(data)


def result(**overrides):
    return runner.ResultMessage(
        **(
            {
                "subtype": "success",
                "is_error": False,
                "duration_ms": 1,
                "duration_api_ms": 1,
                "num_turns": 1,
                "session_id": "test",
                "result": "Built",
            }
            | overrides
        )
    )


def test_agent_check_requires_structured_output(tmp_path, monkeypatch):
    async def query(**kwargs):
        assert kwargs["options"].output_format["schema"] == CheckResult.model_json_schema()
        yield result(result='{"status":"pass","feedback":""}', structured_output=None)

    monkeypatch.setattr(runner, "query", query)
    with pytest.raises(runner.FactoryError, match="invalid structured"):
        asyncio.run(
            runner.agent(
                "Check", {}, ["Read"], tmp_path, tmp_path / "log", options(tmp_path), {}, check=True
            )
        )


def test_agent_drains_stream_and_preserves_literal_prompts(tmp_path, monkeypatch):
    drained = []

    async def query(**kwargs):
        assert "literal {braces}" in kwargs["prompt"]
        assert kwargs["options"].cwd == str(tmp_path)
        assert kwargs["options"].tools == ["Read"]
        yield result(structured_output={"status": "pass", "feedback": ""})
        drained.append(True)

    monkeypatch.setattr(runner, "query", query)
    report = asyncio.run(
        runner.agent(
            "literal {braces}",
            {"task": "x"},
            ["Read"],
            tmp_path,
            tmp_path / "log",
            options(tmp_path),
            {},
            check=True,
        )
    )
    assert report.status == "pass"
    assert drained == [True]


@pytest.mark.parametrize(
    "overrides",
    [
        {"is_error": True},
        {"subtype": "error_max_structured_output_retries"},
        {"permission_denials": [{"tool_name": "Bash"}]},
    ],
)
def test_agent_errors_never_clear(tmp_path, monkeypatch, overrides):
    async def query(**kwargs):
        yield result(structured_output={"status": "pass", "feedback": ""}, **overrides)

    monkeypatch.setattr(runner, "query", query)
    with pytest.raises(runner.FactoryError):
        asyncio.run(
            runner.agent(
                "Check", {}, [], tmp_path, tmp_path / "log", options(tmp_path), {}, check=True
            )
        )


def test_real_worktree_setup_is_just_a_pre_command(tmp_path, fake_agent):
    import subprocess

    root = tmp_path / "source repo"
    root.mkdir()
    for args in [
        ["init", "-b", "main"],
        ["config", "user.name", "Factory Test"],
        ["config", "user.email", "factory@example.com"],
        ["commit", "--allow-empty", "-m", "Initial"],
    ]:
        subprocess.run(["git", *args], cwd=root, check=True, capture_output=True)
    configure(
        root,
        pre=['git worktree add -b factory/$FACTORY_RUN_ID "$FACTORY_WORKSPACE" HEAD'],
        cwd="work trees/{run_id}",
        check="git rev-parse --show-toplevel",
    )
    logs = asyncio.run(runner.run(options(root)))
    workspace = Path(state(logs)["workspace"])
    assert (workspace / ".git").is_file()
    assert (workspace / "hello.txt").read_text() == "hello\n"
    assert (logs / "attempt-1/check-1.log").read_text().strip() == str(workspace)
    assert not (root / "hello.txt").exists()


def test_dogfood_build_stage_runs_test_lint_format_review_in_order():
    repo_root = Path(__file__).resolve().parent.parent
    config = Config.load(repo_root / ".factory" / "config.toml")

    assert config.stages["build"].checks == ["test", "lint", "format", "review"]
    assert config.checks["test"].command == "uv run pytest"
    assert config.checks["lint"].command == "uv run ruff check ."
    assert config.checks["format"].command == "uv run ruff format --check ."
    # The format check must only report drift, never rewrite files in place.
    assert "--check" in config.checks["format"].command
    assert config.checks["review"].prompt == "REVIEW.md"
