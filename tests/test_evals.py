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
