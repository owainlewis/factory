import asyncio
import json
import shlex
import sys
from pathlib import Path

import pytest

from factory import cli


@pytest.fixture
def repo(tmp_path):
    cli.command(tmp_path, "git", "init", "-b", "main")
    cli.git(tmp_path, "config", "user.name", "Factory Test")
    cli.git(tmp_path, "config", "user.email", "factory@example.com")
    (tmp_path / "README.md").write_text("Example\n")
    cli.git(tmp_path, "add", ".")
    cli.git(tmp_path, "commit", "-m", "Initial commit")
    return tmp_path


@pytest.fixture
def fake_agent(monkeypatch):
    async def implementation(stage, prompt, worktree, logs, args):
        if stage == "plan":
            return "Add hello.txt containing hello."
        if stage == "review":
            return {"status": "clear", "findings": []}
        assert "Add hello.txt" in prompt
        (worktree / "hello.txt").write_text("hello\n")
        return "Added hello.txt."

    monkeypatch.setattr(cli, "agent", implementation)


def check(code):
    return f"{shlex.quote(sys.executable)} -c {shlex.quote(code)}"


def options(repo, *extra, validation=None):
    return cli.parser().parse_args(
        [
            "Add a greeting",
            "--repo",
            str(repo),
            "--check",
            validation
            or check(
                "from pathlib import Path; assert Path('hello.txt').read_text() == 'hello\\n'"
            ),
            *extra,
        ]
    )


def test_default_preserves_original_checkout(repo, fake_agent):
    base = cli.git(repo, "rev-parse", "HEAD")
    logs = asyncio.run(cli.pipeline(options(repo)))
    state = json.loads((logs / "run.json").read_text())
    assert state["stage"] == "ready"
    assert cli.git(repo, "rev-parse", "HEAD") == base
    assert not (repo / "hello.txt").exists()
    assert (Path(state["worktree"]) / "hello.txt").exists()
    assert cli.git(repo, "show", f"{state['commit']}:hello.txt") == "hello"


@pytest.mark.parametrize(
    "code, error",
    [
        ("raise SystemExit(2)", "Validation failed"),
        ("from pathlib import Path; Path('hello.txt').write_text('changed')", "Validation changed"),
    ],
)
def test_failed_validation_never_merges(repo, fake_agent, code, error):
    base = cli.git(repo, "rev-parse", "HEAD")
    with pytest.raises(cli.FactoryError, match=error):
        asyncio.run(cli.pipeline(options(repo, "--attempts", "1", validation=check(code))))
    assert cli.git(repo, "rev-parse", "HEAD") == base
    state_file = next((repo / ".git/factory").glob("*/run.json"))
    state = json.loads(state_file.read_text())
    assert state["stage"] == "needs_attention"
    assert Path(state["worktree"]).is_dir()


def test_dirty_checkout_rejected_before_agent(repo, fake_agent):
    (repo / "local.txt").write_text("keep me")
    with pytest.raises(cli.FactoryError, match="clean working tree"):
        asyncio.run(cli.pipeline(options(repo)))
    assert not (repo / ".git/factory").exists()


def test_issue_url_is_expanded_without_shell_interpolation(repo, monkeypatch):
    calls = []

    def command(cwd, *args):
        calls.append(args)
        return json.dumps({"title": "Fix input", "body": "Handle empty input", "url": args[3]})

    monkeypatch.setattr(cli, "command", command)
    url = "https://github.com/owainlewis/factory/issues/1"
    assert cli.resolve_task(url, repo) == f"Fix input\n\nHandle empty input\n\nSource: {url}"
    assert calls[0][:4] == ("gh", "issue", "view", url)
    assert cli.resolve_task("Fix $(something)", repo) == "Fix $(something)"
    assert len(calls) == 1


def test_agent_failure_preserves_artifacts_without_merge(repo, monkeypatch):
    async def fail(*args):
        raise cli.FactoryError("Agent failed")

    monkeypatch.setattr(cli, "agent", fail)
    base = cli.git(repo, "rev-parse", "HEAD")
    with pytest.raises(cli.FactoryError, match="Agent failed"):
        asyncio.run(cli.pipeline(options(repo, "--attempts", "1")))
    assert cli.git(repo, "rev-parse", "HEAD") == base
    assert next((repo / ".git/factory").glob("*/run.json")).exists()


def test_timeout_prevents_merge(repo, fake_agent):
    base = cli.git(repo, "rev-parse", "HEAD")
    with pytest.raises(TimeoutError):
        asyncio.run(
            cli.pipeline(
                options(
                    repo,
                    "--attempts",
                    "1",
                    "--timeout",
                    "1",
                    validation=check("import time; time.sleep(30)"),
                )
            )
        )
    assert cli.git(repo, "rev-parse", "HEAD") == base


@pytest.mark.parametrize(
    "subtype,is_error",
    [("error_max_turns", False), ("error_max_structured_output_retries", True), ("success", True)],
)
def test_sdk_error_results_stop_the_stage(tmp_path, monkeypatch, subtype, is_error):
    async def query(**kwargs):
        assert kwargs["options"].tools == ["Read", "Glob", "Grep"]
        yield cli.ResultMessage(
            subtype=subtype,
            is_error=is_error,
            result="Stopped",
            duration_ms=1,
            duration_api_ms=1,
            num_turns=1,
            session_id="test",
        )

    monkeypatch.setattr(cli, "query", query)
    with pytest.raises(cli.FactoryError, match="Stopped"):
        asyncio.run(cli.agent("plan", "Task", tmp_path, tmp_path, options(tmp_path)))


def test_sdk_build_tools_and_success(tmp_path, monkeypatch):
    drained = []

    async def query(**kwargs):
        assert kwargs["options"].tools == ["Read", "Glob", "Grep", "Write", "Edit"]
        assert "Bash" not in kwargs["options"].allowed_tools
        yield cli.ResultMessage(
            subtype="success",
            is_error=False,
            result="Implemented",
            duration_ms=1,
            duration_api_ms=1,
            num_turns=1,
            session_id="test",
        )
        drained.append(True)

    monkeypatch.setattr(cli, "query", query)
    assert (
        asyncio.run(cli.agent("build", "Task", tmp_path, tmp_path, options(tmp_path)))
        == "Implemented"
    )
    assert drained == [True]


def test_failed_check_repairs_then_reviews(repo, monkeypatch):
    stages = []

    async def agent(stage, prompt, worktree, logs, args):
        stages.append(stage)
        if stage == "plan":
            return "Make a greeting"
        if stage == "review":
            return {"status": "clear", "findings": []}
        if stages.count("build") == 1:
            (worktree / "hello.txt").write_text("wrong")
        else:
            assert "Validation failed" in prompt
            (worktree / "hello.txt").write_text("hello\n")
        return "Built"

    monkeypatch.setattr(cli, "agent", agent)
    logs = asyncio.run(cli.pipeline(options(repo)))
    assert stages == ["plan", "build", "build", "review"]
    assert (logs / "attempt-1/diff.patch").exists()
    assert (logs / "attempt-2/review.json").exists()
    assert json.loads((logs / "run.json").read_text())["attempt"] == 2


def test_review_findings_repair_and_recheck(repo, monkeypatch):
    stages = []

    async def agent(stage, prompt, worktree, logs, args):
        stages.append(stage)
        if stage == "plan":
            return "Make a greeting"
        if stage == "review":
            if stages.count("review") == 1:
                return {"status": "needs_fixes", "findings": ["README.md: explain the greeting"]}
            return {"status": "clear", "findings": []}
        (worktree / "hello.txt").write_text("hello\n")
        if stages.count("build") == 2:
            assert "README.md: explain" in prompt
            (worktree / "README.md").write_text("A greeting\n")
        return "Built"

    monkeypatch.setattr(cli, "agent", agent)
    logs = asyncio.run(cli.pipeline(options(repo)))
    assert stages == ["plan", "build", "review", "build", "review"]
    assert (logs / "attempt-2/validation.log").exists()


def test_attempt_limit_stops_without_publishing(repo, monkeypatch):
    builds = []

    async def agent(stage, prompt, worktree, logs, args):
        if stage == "plan":
            return "Plan"
        assert stage == "build"
        builds.append(1)
        (worktree / "hello.txt").write_text(str(len(builds)))
        return "Built"

    monkeypatch.setattr(cli, "agent", agent)
    with pytest.raises(cli.FactoryError, match=r"Attempt limit reached \(3\)"):
        asyncio.run(cli.pipeline(options(repo, "--pr", validation=check("raise SystemExit(1)"))))
    assert len(builds) == 3
    assert not (repo / "hello.txt").exists()


@pytest.mark.parametrize("mutate", [False, True])
def test_incomplete_or_mutating_review_stops(repo, fake_agent, monkeypatch, mutate):
    original = cli.agent

    async def agent(stage, prompt, worktree, logs, args):
        if stage != "review":
            return await original(stage, prompt, worktree, logs, args)
        if mutate:
            (worktree / "hello.txt").write_text("changed")
            return {"status": "clear", "findings": []}
        return {"status": "needs_attention", "findings": ["Review unavailable"]}

    monkeypatch.setattr(cli, "agent", agent)
    with pytest.raises(cli.FactoryError, match="Review"):
        asyncio.run(cli.pipeline(options(repo, "--pr")))
    assert not (repo / "hello.txt").exists()


def test_pr_publishes_exact_reviewed_commit(repo, fake_agent, monkeypatch, tmp_path):
    remote = tmp_path / "remote.git"
    cli.command(tmp_path, "git", "init", "--bare", str(remote))
    # Remote lives inside the fixture repo; ignore it before the clean-tree check.
    (repo / ".git/info/exclude").write_text("remote.git/\n")
    cli.git(repo, "remote", "add", "origin", str(remote))
    original = cli.command
    calls = []

    def command(cwd, *args):
        if args[0] == "gh":
            calls.append(args)
            assert "GitHub CI and human review" in Path(args[-1]).read_text()
            return "https://github.com/example/repo/pull/1"
        return original(cwd, *args)

    monkeypatch.setattr(cli, "command", command)
    logs = asyncio.run(cli.pipeline(options(repo, "--pr")))
    state = json.loads((logs / "run.json").read_text())
    assert state["stage"] == "pr_open"
    assert cli.git(remote, "rev-parse", state["branch"]) == state["commit"]
    assert "--base" in calls[0] and "main" in calls[0]
    assert not (repo / "hello.txt").exists()


@pytest.mark.parametrize(
    "report",
    [None, {}, {"status": "clear", "findings": ["bug"]}, {"status": "needs_fixes", "findings": []}],
)
def test_invalid_review_output_never_clears(tmp_path, monkeypatch, report):
    async def query(**kwargs):
        assert kwargs["options"].output_format == {
            "type": "json_schema",
            "schema": cli.Review.model_json_schema(),
        }
        assert "Write" in kwargs["options"].disallowed_tools
        yield cli.ResultMessage(
            subtype="success",
            is_error=False,
            result="```json\n[]\n```",
            structured_output=report,
            duration_ms=1,
            duration_api_ms=1,
            num_turns=1,
            session_id="test",
        )

    monkeypatch.setattr(cli, "query", query)
    with pytest.raises(cli.FactoryError, match="invalid structured output"):
        asyncio.run(
            cli.agent(
                "review", "/code-review high base...head", tmp_path, tmp_path, options(tmp_path)
            )
        )


def test_successful_review_is_structured_and_drained(tmp_path, monkeypatch):
    drained = []

    async def query(**kwargs):
        assert kwargs["options"].env["CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"] == "1"
        yield cli.ResultMessage(
            subtype="success",
            is_error=False,
            result="Ignored free text",
            structured_output={"completed": True, "findings": []},
            duration_ms=1,
            duration_api_ms=1,
            num_turns=1,
            session_id="test",
        )
        drained.append(True)

    monkeypatch.setattr(cli, "query", query)
    assert asyncio.run(
        cli.agent("review", "/code-review high base...head", tmp_path, tmp_path, options(tmp_path))
    ) == {"status": "clear", "findings": []}
    assert drained == [True]


def test_agent_timeout_closes_stream(tmp_path, monkeypatch):
    closed = []

    async def query(**kwargs):
        try:
            await asyncio.sleep(30)
            yield None
        finally:
            closed.append(True)

    monkeypatch.setattr(cli, "query", query)
    args = options(tmp_path, "--timeout", "1")
    with pytest.raises(TimeoutError):
        asyncio.run(cli.agent("review", "/code-review high base...head", tmp_path, tmp_path, args))
    assert closed == [True]


def test_structured_findings_are_feedback():
    review = cli.Review.model_validate(
        {
            "completed": True,
            "findings": [
                {
                    "file": "hello.py",
                    "line": 2,
                    "summary": "Missing default",
                    "failure_scenario": "Zero-argument callers raise TypeError.",
                }
            ],
        }
    )
    assert review.feedback()["status"] == "needs_fixes"
    assert "hello.py:2: Missing default" in review.feedback()["findings"][0]


def test_incomplete_review_cannot_clear():
    assert cli.Review(completed=False, findings=[]).feedback()["status"] == "needs_attention"


@pytest.mark.parametrize(
    "changes",
    [
        {"line": "2"},
        {"line": True},
        {"line": 0},
        {"summary": " "},
        {"file": ""},
        {"unexpected": "field"},
    ],
)
def test_finding_schema_rejects_invalid_fields(changes):
    data = {"file": "hello.py", "line": 2, "summary": "Bug", "failure_scenario": "Crash"}
    with pytest.raises(cli.ValidationError):
        cli.Finding.model_validate(data | changes)


@pytest.mark.parametrize(
    "report",
    [
        {"completed": "true", "findings": []},
        {"completed": True},
        {"completed": True, "findings": [], "extra": 1},
    ],
)
def test_review_schema_rejects_invalid_fields(report):
    with pytest.raises(cli.ValidationError):
        cli.Review.model_validate(report)


def test_prompt_templates_preserve_literal_task_content():
    task = "Handle {braces} in user input"
    assert task in cli.prompt("PLAN.md", task=task)
    assert task in cli.prompt("BUILD.md", task=task, plan="Plan", feedback="Fix it", check="pytest")
    assert "high base...head" in cli.prompt("VERIFY.md", base="base", commit="head")
