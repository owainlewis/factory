"""Check benchmark scoring and accounting without model or GitHub calls."""

import contextlib
import io
import json
import subprocess
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from evals import agent_benchmark as benchmark


def tokens(total):
    return {
        "input_tokens": total - 10,
        "cached_input_tokens": 20,
        "output_tokens": 10,
        "reasoning_output_tokens": 5,
        "total_tokens": total,
    }


def rollout(path, *snapshots):
    path.write_text(
        "\n".join(
            json.dumps(
                {
                    "type": "event_msg",
                    "payload": {
                        "type": "token_count",
                        "info": {"total_token_usage": snapshot},
                    },
                }
            )
            for snapshot in snapshots
        )
        + "\n"
    )


class BenchmarkTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def workspace(self, case):
        workspace = self.root / case
        benchmark.prepare(workspace, case)
        return workspace

    def test_clean_fixture_meets_all_acceptance_checks(self):
        workspace = self.workspace("clean")
        self.assertTrue(benchmark.grade(workspace)["passed"])
        self.assertEqual(benchmark.feedback(workspace, "clean")["ci_status"], "passed")

    def test_seeded_range_bug_fails_ci_and_grade(self):
        workspace = self.workspace("repair")
        self.assertFalse(benchmark.grade(workspace)["passed"])
        feedback = benchmark.feedback(workspace, "repair")
        self.assertEqual(feedback["ci_status"], "failed")
        self.assertIn("8000-8002", feedback["failed_job_logs"][0]["log"])
        (workspace / "ports.py").write_text(
            (benchmark.FIXTURE / "ports.py").read_text()
        )
        fixed = benchmark.feedback(workspace, "repair")
        self.assertNotEqual(fixed["head_sha"], feedback["head_sha"])
        self.assertEqual(fixed["ci_status"], "passed")
        self.assertTrue(benchmark.grade(workspace)["passed"])

    def test_review_case_catches_bug_ci_misses(self):
        workspace = self.workspace("review-only")
        feedback = benchmark.feedback(workspace, "review-only")
        self.assertEqual(feedback["ci_status"], "passed")
        self.assertFalse(feedback["review_comments"][0]["is_resolved"])
        self.assertFalse(benchmark.grade(workspace)["passed"])
        (workspace / "ports.py").write_text(
            (benchmark.FIXTURE / "ports.py").read_text()
        )
        self.assertTrue(
            benchmark.feedback(workspace, "review-only")["review_comments"][0][
                "is_resolved"
            ]
        )

    def test_checker_crash_is_a_failure(self):
        workspace = self.workspace("clean")
        (workspace / "ports.py").write_text("raise RuntimeError('broken')")
        self.assertFalse(benchmark.grade(workspace)["passed"])

    def test_usage_uses_last_cumulative_counter_not_sum(self):
        path = self.root / "session.jsonl"
        rollout(path, tokens(100), tokens(250))
        result = benchmark.rollout_usage(path)
        self.assertEqual(result["tokens"]["total_tokens"], 250)
        self.assertEqual(result["tokens"]["output_tokens"], 10)

    def test_missing_usage_is_not_zero(self):
        path = self.root / "session.jsonl"
        path.write_text("{}\n")
        with self.assertRaises(ValueError):
            benchmark.rollout_usage(path)

    def test_schedule_has_matched_cases_and_repeats(self):
        scheduled = list(benchmark.schedule(benchmark.CASES, 3, 7))
        self.assertEqual(len(scheduled), 18)
        self.assertEqual(len(set(scheduled)), 18)
        for index in range(0, len(scheduled), 2):
            left, right = scheduled[index : index + 2]
            self.assertEqual(left[:2], right[:2])
            self.assertEqual({left[2], right[2]}, set(benchmark.ARMS))
        self.assertEqual(scheduled, list(benchmark.schedule(benchmark.CASES, 3, 7)))

    def test_meter_counts_each_native_descendant_once(self):
        root_path, child_path = self.root / "root.jsonl", self.root / "child.jsonl"
        rollout(root_path, tokens(100), tokens(200))
        rollout(child_path, tokens(70))

        def call(tool, receiver):
            return {
                "type": "collabAgentToolCall",
                "tool": tool,
                "receiverThreadIds": [receiver],
            }

        def thread(path, items):
            return {
                "thread": {
                    "path": str(path),
                    "turns": [{"status": "completed", "items": items}],
                }
            }

        records = {
            "root": thread(
                root_path,
                [
                    call("spawnAgent", "child"),
                    call("wait", "child"),
                    {
                        "type": "subAgentActivity",
                        "kind": "started",
                        "agentThreadId": "child",
                    },
                    {
                        "type": "subAgentActivity",
                        "kind": "completed",
                        "agentThreadId": "child",
                    },
                ],
            ),
            "child": thread(child_path, [call("sendInput", "root")]),
        }
        client = SimpleNamespace(
            _client=SimpleNamespace(
                _request_raw=lambda method, params: records[params["threadId"]]
            )
        )
        meter = benchmark.Meter(SimpleNamespace(Codex=None), self.root)
        meter.calls = 1
        meter.collect(client, ["root"])
        meter.collect(client, ["root"])
        result = meter.result()
        self.assertTrue(result["complete"])
        self.assertEqual(result["tokens"]["total_tokens"], 270)
        self.assertEqual(result["review_agents"], 1)
        self.assertEqual(set(result["threads"]), {"root", "child"})

    def test_missing_descendant_usage_invalidates_total(self):
        meter = benchmark.Meter(SimpleNamespace(Codex=None), self.root)
        meter.calls = 1
        client = SimpleNamespace(
            _client=SimpleNamespace(
                _request_raw=lambda *args, **kwargs: (_ for _ in ()).throw(
                    RuntimeError("unavailable")
                )
            )
        )
        meter.collect(client, ["missing"])
        self.assertFalse(meter.result()["complete"])
        self.assertIsNone(meter.result()["tokens"])

    def test_no_agent_calls_have_known_zero_usage(self):
        meter = benchmark.Meter(SimpleNamespace(Codex=None), self.root)
        self.assertTrue(meter.result()["complete"])
        self.assertEqual(meter.result()["tokens"]["total_tokens"], 0)

    def test_model_or_effort_mismatch_cannot_produce_a_ratio(self):
        def result(model, effort):
            return {
                "usage": {
                    "agent_calls": 1,
                    "threads": {"root": {"models": [[model, effort]]}},
                }
            }

        pair = {
            "scripted": result("same-model", "medium"),
            "prompted": result("same-model", "medium"),
        }
        self.assertTrue(benchmark.comparable_models(pair))
        pair["prompted"] = result("same-model", "high")
        self.assertFalse(benchmark.comparable_models(pair))
        pair["prompted"] = result("different-model", "medium")
        self.assertFalse(benchmark.comparable_models(pair))
        pair["prompted"] = result("same-model", None)
        self.assertFalse(benchmark.comparable_models(pair))
        pair["prompted"] = result("same-model", "medium")
        pair["scripted"] = {"usage": {"agent_calls": 0, "threads": {}}}
        self.assertTrue(benchmark.comparable_models(pair))

    def test_failed_trial_remains_visible_and_cannot_produce_savings(self):
        trials = []
        for arm in benchmark.ARMS:
            directory = self.root / arm
            directory.mkdir()
            benchmark.write_json(
                directory / "result.json",
                {
                    "success": arm == "prompted",
                    "usage": {"complete": True, "tokens": tokens(100)},
                },
            )
            trials.append({"case": "repair", "repeat": 1, "arm": arm, "directory": arm})
        benchmark.write_json(
            self.root / "manifest.json",
            {"cases": ["repair"], "repeats": 1, "trials": trials},
        )
        report = benchmark.render_report(self.root)
        self.assertIn("FAIL", report)
        self.assertIn("0/1 pairs", report)
        self.assertIn("no valid token ratio", report)

    def test_timeout_is_recorded_and_process_group_is_stopped(self):
        args = SimpleNamespace(
            output=self.root / "run", cases=["clean"], repeats=1, seed=1, timeout=1
        )
        process = MagicMock(pid=12345, returncode=-9)
        process.wait.side_effect = [subprocess.TimeoutExpired("trial", 1), None] * 2
        with (
            patch.object(benchmark.subprocess, "Popen", return_value=process),
            patch.object(
                benchmark.subprocess, "check_output", return_value="codex test"
            ),
            patch.object(benchmark.importlib.metadata, "version", return_value="test"),
            patch.object(benchmark.os, "killpg") as kill,
            contextlib.redirect_stdout(io.StringIO()),
        ):
            self.assertEqual(benchmark.run_suite(args), 1)
        self.assertEqual(kill.call_count, 2)
        for path in args.output.glob("*/result.json"):
            result = json.loads(path.read_text())
            self.assertFalse(result["success"])
            self.assertFalse(result["usage"]["complete"])
            self.assertIsNone(result["usage"]["tokens"])
            self.assertIn("timed out", result["report"]["summary"])

    def test_worker_crash_is_a_failed_attempt_not_an_unstarted_trial(self):
        args = SimpleNamespace(
            output=self.root / "crash", cases=["clean"], repeats=1, seed=1, timeout=1
        )
        with (
            patch.object(
                benchmark.subprocess, "Popen", return_value=MagicMock(returncode=2)
            ),
            patch.object(
                benchmark.subprocess, "check_output", return_value="codex test"
            ),
            patch.object(benchmark.importlib.metadata, "version", return_value="test"),
            contextlib.redirect_stdout(io.StringIO()),
        ):
            self.assertEqual(benchmark.run_suite(args), 1)
        report = (args.output / "report.md").read_text()
        self.assertIn("FAIL", report)
        self.assertNotIn("not run", report)
        result = json.loads(next(args.output.glob("*/result.json")).read_text())
        self.assertIn("Worker exited 2", result["report"]["summary"])

    def test_real_loop_skips_clean_case_without_starting_an_agent(self):
        # Reuse the existing test module's SDK-free import of production agent.py.
        from evals.test_agent import agent

        workspace = self.workspace("clean")
        with patch.object(agent, "run_codex") as run, patch.object(agent, "gh") as gh:
            report = agent.iterate(
                "REQUIREMENTS.md",
                "local/replay",
                {"status": "completed", "pr_number": 1},
                benchmark.feedback(workspace, "clean"),
            )
        self.assertEqual(report["status"], "completed")
        run.assert_not_called()
        gh.assert_not_called()


if __name__ == "__main__":
    unittest.main()
