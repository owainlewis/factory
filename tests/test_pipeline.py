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


def test_merge_uses_validated_commit(repo, fake_agent):
    logs = asyncio.run(cli.pipeline(options(repo, "--merge")))
    state = json.loads((logs / "run.json").read_text())
    assert state["stage"] == "merged"
    assert cli.git(repo, "rev-parse", "HEAD") == state["commit"]
    assert (repo / "hello.txt").read_text() == "hello\n"
    assert cli.git(repo, "status", "--porcelain") == ""


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
        asyncio.run(cli.pipeline(options(repo, "--merge", validation=check(code))))
    assert cli.git(repo, "rev-parse", "HEAD") == base
    state_file = next((repo / ".git/factory").glob("*/run.json"))
    state = json.loads(state_file.read_text())
    assert state["stage"] == "failed"
    assert Path(state["worktree"]).is_dir()


def test_dirty_checkout_rejected_before_agent(repo, fake_agent):
    (repo / "local.txt").write_text("keep me")
    with pytest.raises(cli.FactoryError, match="clean working tree"):
        asyncio.run(cli.pipeline(options(repo)))
    assert not (repo / ".git/factory").exists()


def test_changed_base_prevents_merge(repo, fake_agent):
    code = f"from pathlib import Path; Path({str(repo / 'local.txt')!r}).write_text('local work')"
    with pytest.raises(cli.FactoryError, match="Original checkout changed"):
        asyncio.run(cli.pipeline(options(repo, "--merge", validation=check(code))))
    assert not (repo / "hello.txt").exists()
    assert (repo / "local.txt").read_text() == "local work"


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
        asyncio.run(cli.pipeline(options(repo, "--merge")))
    assert cli.git(repo, "rev-parse", "HEAD") == base
    assert next((repo / ".git/factory").glob("*/run.json")).exists()


def test_timeout_prevents_merge(repo, fake_agent):
    base = cli.git(repo, "rev-parse", "HEAD")
    with pytest.raises(TimeoutError):
        asyncio.run(
            cli.pipeline(
                options(
                    repo,
                    "--merge",
                    "--timeout",
                    "1",
                    validation=check("import time; time.sleep(30)"),
                )
            )
        )
    assert cli.git(repo, "rev-parse", "HEAD") == base


@pytest.mark.parametrize("subtype,is_error", [("error_max_turns", False), ("success", True)])
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
