"""Protect the benchmark's scoring contract without live model calls."""

import importlib.util
import json
import shutil
from pathlib import Path
from types import SimpleNamespace

import pytest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("eval_runner", ROOT / "evals/run.py")
evals = importlib.util.module_from_spec(spec)
spec.loader.exec_module(evals)


@pytest.mark.parametrize("case", sorted((ROOT / "evals/cases").iterdir()), ids=lambda p: p.name)
def test_reference_passes_and_starting_point_fails(case, tmp_path):
    assert not evals.grade(case, case / "workspace", tmp_path / "baseline")["passed"]
    shutil.copyfile(case / "solution.py", tmp_path / "app.py")
    result = evals.grade(case, tmp_path, tmp_path / "reference")
    assert result["passed"]
    assert result["tests"] >= 2


def test_candidate_cannot_weaken_original_tests(tmp_path):
    case = ROOT / "evals/cases/pagination"
    candidate = tmp_path / "candidate"
    shutil.copytree(case / "workspace", candidate)
    (candidate / "tests/test_app.py").write_text("")
    assert not evals.grade(case, candidate, tmp_path / "score")["passed"]


def test_missing_invalid_and_hanging_implementations_fail(tmp_path):
    case = ROOT / "evals/cases/pagination"
    assert not evals.grade(case, tmp_path, tmp_path / "missing")["passed"]
    (tmp_path / "app.py").write_text("not valid python!")
    assert not evals.grade(case, tmp_path, tmp_path / "syntax")["passed"]
    (tmp_path / "app.py").write_text("while True: pass")
    result = evals.grade(case, tmp_path, tmp_path / "timeout", timeout=0.1)
    assert not result["passed"]
    assert result["error"] == "Acceptance timeout"


def test_preparation_excludes_answers_and_is_identical_across_modes(tmp_path):
    case = ROOT / "evals/cases/pagination"
    args = SimpleNamespace(model="test", seconds=1, max_turns=1, attempts=3)
    for mode in evals.MODES:
        trial = tmp_path / mode
        evals.prepare_trial(case, mode, 1, args, trial)
        assert (trial / "workspace/app.py").read_bytes() == (case / "workspace/app.py").read_bytes()
        assert not (trial / "workspace/solution.py").exists()
        assert not (trial / "workspace/acceptance.py").exists()


def test_report_counts_failures_and_reports_missing_human_review(tmp_path):
    for number, passed in enumerate([True, False, False]):
        directory = tmp_path / str(number)
        directory.mkdir()
        (directory / "result.json").write_text(
            json.dumps(
                {
                    "mode": "raw",
                    "case": "pagination",
                    "accepted": passed,
                    "false_completion": not passed,
                    "wall_seconds": 10,
                    "list_price_usd": 1,
                }
            )
        )
    results = evals.report(tmp_path)
    assert len(results) == 3
    text = (tmp_path / "report.md").read_text()
    assert "| raw | 1/3 | 2 | 10.0 | 3.0000 |" in text
    assert "Human maintainability review has not been scored" in text


def test_successful_agent_exit_is_not_accepted_without_correct_code(tmp_path, monkeypatch):
    case = ROOT / "evals/cases/pagination"
    args = SimpleNamespace(model="test", seconds=1, max_turns=1, attempts=3)
    trial = tmp_path / "trial"
    spec = evals.prepare_trial(case, "raw", 1, args, trial)
    real_popen = evals.subprocess.Popen

    class CompletedWorker:
        returncode = 0

        def wait(self, timeout=None):
            evals.write_json(trial / "execution.json", {"completed": True, "error": None})
            return 0

    def launch(command, **kwargs):
        if "_worker" in command:
            return CompletedWorker()
        return real_popen(command, **kwargs)

    monkeypatch.setattr(evals.subprocess, "Popen", launch)
    evals.execute_trial(case, trial, spec)
    result = json.loads((trial / "result.json").read_text())
    assert result["completed"]
    assert not result["accepted"]
    assert result["false_completion"]
    assert not result["usage_complete"]


def test_direct_cli_keeps_default_prompt_and_records_structured_usage(tmp_path, monkeypatch):
    import asyncio
    import sys

    executable = tmp_path / "claude"
    executable.write_text(
        f"#!{sys.executable}\n"
        "import json, sys\n"
        'assert "--system-prompt" not in sys.argv\n'
        'assert "--setting-sources" in sys.argv\n'
        'assert "task text" in sys.stdin.read()\n'
        'print(json.dumps({"type":"result","subtype":"success","is_error":False,'
        '"result":"done","usage":{"output_tokens":7},"modelUsage":{"test":{}}}))\n'
    )
    executable.chmod(0o755)
    monkeypatch.setattr(evals.shutil, "which", lambda _: str(executable))
    args = SimpleNamespace(model="test", max_turns=3, task="task text")
    asyncio.run(evals.cli_agent(args, tmp_path, tmp_path))
    usage = json.loads((tmp_path / "usage.jsonl").read_text())
    assert usage["usage"]["output_tokens"] == 7
    assert usage["model_usage"] == {"test": {}}
    assert (tmp_path / "summary.md").read_text() == "done"


def test_atomic_acceptance_allows_direct_function_imports(tmp_path):
    case = ROOT / "evals/cases/atomic-save"
    source = (case / "solution.py").read_text()
    source = source.replace("import os", "import os\nfrom os import replace")
    source = source.replace("os.replace(temporary, path)", "replace(temporary, path)")
    (tmp_path / "app.py").write_text(source)
    assert evals.grade(case, tmp_path, tmp_path / "score")["passed"]


def test_matched_profiles_allow_review_and_share_reviewer_instructions(tmp_path):
    import tomllib

    case = ROOT / "evals/cases/pagination"
    args = SimpleNamespace(model="test", seconds=1, max_turns=40, attempts=3)
    for mode in ["subagent", "factory_matched"]:
        trial = tmp_path / mode
        evals.prepare_trial(case, mode, 1, args, trial)
        assert "delegate to other agents" not in (trial / "workspace/AGENTS.md").read_text()
        assert (trial / "REVIEW.md").read_text() == evals.reviewer_definition()["prompt"]
    config = tomllib.loads((tmp_path / "factory_matched/config.toml").read_text())
    assert "Agent" in config["stages"]["build"]["tools"]
    assert config["checks"]["review"]["tools"] == evals.reviewer_definition()["tools"]
    assert evals.reviewer_definition()["model"] == "inherit"


def test_subagent_audit_requires_real_tool_events_and_successful_return(tmp_path):
    log = tmp_path / "transcript.jsonl"
    events = [
        {
            "type": "assistant",
            "message": {"content": [{"type": "text", "text": "I reviewed using a subagent"}]},
        },
        {
            "type": "assistant",
            "message": {
                "content": [
                    {
                        "type": "tool_use",
                        "name": "Agent",
                        "id": "one",
                        "input": {"subagent_type": "reviewer"},
                    }
                ]
            },
        },
        {
            "type": "user",
            "message": {
                "content": [{"type": "tool_result", "tool_use_id": "one", "is_error": True}]
            },
        },
    ]
    log.write_text("\n".join(json.dumps(e) for e in events))
    assert evals.subagent_evidence(tmp_path) == {"requested": 1, "returned_without_tool_error": 0}
    events[-1]["message"]["content"][0]["is_error"] = False
    log.write_text("\n".join(json.dumps(e) for e in events))
    assert evals.subagent_evidence(tmp_path) == {"requested": 1, "returned_without_tool_error": 1}


def test_model_usage_includes_children_and_auxiliary_models_without_double_counting():
    messages = [
        {
            "usage": {"output_tokens": 5},
            "model_usage": {
                "primary": {"outputTokens": 15, "cacheReadInputTokens": 100},
                "auxiliary": {"outputTokens": 2},
            },
        },
        {"model_usage": {"primary": {"outputTokens": 3, "inputTokens": 4}}},
    ]
    assert evals.model_usage_totals(messages) == {
        "input_tokens": 4,
        "output_tokens": 20,
        "cache_read_input_tokens": 100,
        "cache_creation_input_tokens": 0,
    }


def test_verified_profiles_change_only_reviewer_instructions(tmp_path):
    case = ROOT / "evals/cases/pagination"
    args = SimpleNamespace(model="test", seconds=300, max_turns=40, attempts=3)
    for improved, baseline in evals.VARIANTS.items():
        new, old = tmp_path / improved, tmp_path / baseline
        evals.prepare_trial(case, improved, 1, args, new)
        evals.prepare_trial(case, baseline, 1, args, old)
        for name in ("BUILD.md", "config.toml", "workspace/AGENTS.md", "workspace/app.py"):
            assert (new / name).read_text() == (old / name).read_text()
        reviewer = evals.reviewer_definition(40, improved)
        assert (new / "REVIEW.md").read_text() == reviewer["prompt"]
        assert reviewer["prompt"] == (ROOT / ".factory/REVIEW.md").read_text()
        assert reviewer["prompt"] != (old / "REVIEW.md").read_text()
        assert reviewer["tools"] == evals.reviewer_definition()["tools"]
