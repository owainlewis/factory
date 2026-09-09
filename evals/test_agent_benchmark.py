"""Check benchmark scoring and accounting without model or GitHub calls."""

import contextlib
import io
import json
import subprocess
import sys
from pathlib import Path
import tempfile
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from evals import agent_benchmark as benchmark

SANDBOX_COMMAND = benchmark.checker_command


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
        self.root = Path(self.temp.name).resolve()
        trusted = patch.object(
            benchmark,
            "checker_command",
            side_effect=lambda workspace, suite: [
                sys.executable,
                "-B",
                str(benchmark.FIXTURE / "check_ports.py"),
                str(workspace),
                "--suite",
                suite,
            ],
        )
        trusted.start()
        self.addCleanup(trusted.stop)

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

    def test_controller_does_not_follow_metadata_symlinks(self):
        workspace = self.workspace("follow-up")
        outside = self.root / "outside.txt"
        outside.write_text("preserve")
        controller = benchmark.FeedbackController(workspace, "follow-up")
        controller.get()
        (workspace / "ports.py").write_text(
            (benchmark.FIXTURE / "ports.py").read_text()
        )
        (workspace / "REQUIREMENTS.md").unlink()
        (workspace / "REQUIREMENTS.md").symlink_to(outside)
        (workspace / ".feedback-response.json").symlink_to(outside)
        with controller:
            result = benchmark.request_feedback(workspace)
        self.assertEqual(outside.read_text(), "preserve")
        self.assertFalse((workspace / "REQUIREMENTS.md").is_symlink())
        self.assertFalse((workspace / ".feedback-response.json").is_symlink())
        self.assertIn("follow-up", result["review_comments"][0]["body"])

    def test_safe_writer_rejects_a_symlink_parent(self):
        outside = self.root / "outside"
        outside.mkdir()
        link = self.root / "link"
        link.symlink_to(outside, target_is_directory=True)
        with self.assertRaises(OSError):
            benchmark.write_json(link / "result.json", {"unsafe": True})
        self.assertFalse((outside / "result.json").exists())

    def test_controller_delivers_follow_up_and_counts_two_attempts(self):
        workspace = self.workspace("follow-up")
        controller = benchmark.FeedbackController(workspace, "follow-up")
        initial = controller.get()
        self.assertEqual(initial["ci_status"], "failed")
        code = (benchmark.FIXTURE / "ports.py").read_text()
        (workspace / "ports.py").write_text(code)
        follow_up = controller.get()
        self.assertEqual(follow_up["ci_status"], "passed")
        self.assertFalse(follow_up["review_comments"][0]["is_resolved"])
        self.assertIn("--suite expanded", (workspace / "REQUIREMENTS.md").read_text())
        self.assertTrue(controller.history[-1]["attempt_passed"])
        fixed = code.replace(
            'part.strip().split("-")',
            '[bound.strip() for bound in part.strip().split("-")]',
        )
        (workspace / "ports.py").write_text(fixed)
        final = controller.get()
        self.assertTrue(final["review_comments"][0]["is_resolved"])
        metrics = controller.metrics(
            benchmark.grade(workspace, "expanded"), {"status": "completed"}
        )
        self.assertEqual(metrics["repair_attempts"], 2)
        self.assertEqual(metrics["feedback_polls"], 2)
        self.assertTrue(metrics["first_attempt_passed"])
        self.assertEqual(metrics["regressions"], 0)
        self.assertFalse(metrics["false_completion"])

    def test_feedback_transport_only_requests_controller_checks(self):
        workspace = self.workspace("repair")
        with benchmark.FeedbackController(workspace, "repair") as controller:
            result = benchmark.request_feedback(workspace)
        self.assertEqual(result["ci_status"], "failed")
        self.assertEqual(len(controller.history), 1)
        self.assertTrue((workspace / ".feedback-response.json").is_file())

    def test_metrics_detect_false_completion_and_regressions(self):
        workspace = self.workspace("clean")
        controller = benchmark.FeedbackController(workspace, "clean")
        controller.history = [
            {
                "head_sha": "before",
                "passed_checks": ["a", "b"],
                "infrastructure_error": False,
            },
            {
                "head_sha": "after",
                "passed_checks": ["a"],
                "infrastructure_error": False,
                "attempt_passed": False,
            },
        ]
        metrics = controller.metrics({"passed": False}, {"status": "completed"})
        self.assertEqual(metrics["regressions"], 1)
        self.assertTrue(metrics["false_completion"])
        self.assertFalse(metrics["first_attempt_passed"])

    def test_parent_checker_always_uses_read_only_sandbox(self):
        command = SANDBOX_COMMAND(self.root, "full")
        self.assertEqual(
            command[:4], ["codex", "sandbox", "--permission-profile", ":read-only"]
        )
        self.assertIn("-B", command)
        self.assertEqual(command[-2:], ["--suite", "full"])

    def test_retained_context_starts_one_client_and_thread_for_two_calls(self):
        sdk = ModuleType("openai_codex")
        sdk.ApprovalMode = SimpleNamespace(deny_all="deny")
        sdk.Sandbox = SimpleNamespace(workspace_write="workspace")
        factory = MagicMock()
        client = factory.return_value.__enter__.return_value
        client.thread_start.return_value.id = "maker"
        meter = benchmark.Meter(SimpleNamespace(Codex=factory), self.root, reuse=True)
        with (
            patch.dict(sys.modules, {"openai_codex": sdk}),
            patch.object(meter, "collect"),
            meter.sessions,
        ):
            with meter.client(None) as first:
                one = first.thread_start()
            with meter.client(None) as second:
                two = second.thread_start()
        self.assertIs(one, two)
        factory.assert_called_once_with(None)
        client.thread_start.assert_called_once()
        self.assertEqual(meter.calls, 2)

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
        scheduled = list(benchmark.schedule(("clean", "repair", "review-only"), 3, 7))
        self.assertEqual(len(scheduled), 18)
        self.assertEqual(len(set(scheduled)), 18)
        for index in range(0, len(scheduled), 2):
            left, right = scheduled[index : index + 2]
            self.assertEqual(left[:2], right[:2])
            self.assertEqual({left[2], right[2]}, set(benchmark.DEFAULT_ARMS))
        self.assertEqual(
            scheduled,
            list(benchmark.schedule(("clean", "repair", "review-only"), 3, 7)),
        )

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

    def test_retained_usage_updates_root_and_adds_new_reviewer_once(self):
        paths = {key: self.root / f"{key}.jsonl" for key in ("root", "first", "second")}
        for key, total in (("root", 100), ("first", 50), ("second", 70)):
            rollout(paths[key], tokens(total))
        children = ["first"]

        def read(method, params):
            key = params["threadId"]
            items = (
                [
                    {
                        "type": "subAgentActivity",
                        "kind": "started",
                        "agentThreadId": child,
                    }
                    for child in children
                ]
                if key == "root"
                else []
            )
            return {
                "thread": {
                    "path": str(paths[key]),
                    "turns": [{"status": "completed", "items": items}],
                }
            }

        client = SimpleNamespace(_client=SimpleNamespace(_request_raw=read))
        meter = benchmark.Meter(SimpleNamespace(Codex=None), self.root, reuse=True)
        meter.calls = 1
        meter.collect(client, ["root"])
        self.assertEqual(meter.result()["tokens"]["total_tokens"], 150)
        rollout(paths["root"], tokens(100), tokens(250))
        children.append("second")
        meter.calls = 2
        meter.collect(client, ["root"])
        self.assertEqual(meter.result()["tokens"]["total_tokens"], 370)
        self.assertEqual(meter.result()["review_agents"], 2)
        self.assertEqual(meter.result()["agent_calls"], 2)

    def test_ratio_format_preserves_small_differences(self):
        self.assertEqual(
            benchmark.median_range([0.96, 1.03], 3), "0.995 [0.960, 1.030]"
        )

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

    def test_correct_code_with_missing_usage_is_incomplete_not_valid(self):
        directory = self.root / "incomplete"
        directory.mkdir()
        benchmark.write_json(
            directory / "result.json",
            {"success": True, "usage": {"complete": False, "tokens": None}},
        )
        benchmark.write_json(
            self.root / "manifest.json",
            {
                "cases": ["repair"],
                "repeats": 1,
                "trials": [
                    {
                        "case": "repair",
                        "repeat": 1,
                        "arm": "scripted",
                        "directory": "incomplete",
                    }
                ],
            },
        )
        report = benchmark.render_report(self.root)
        self.assertIn("INCOMPLETE", report)
        self.assertNotIn("| VALID |", report)

    def test_deleted_candidate_preserves_usage_and_false_completion(self):
        from evals.test_agent import agent

        directory = self.root / "deleted"
        workspace = directory / "workspace"
        benchmark.prepare(workspace, "clean")

        def delete_candidate(prompt):
            (workspace / "ports.py").unlink()
            return {"status": "completed", "pr_number": 1, "summary": "done"}

        usage = {"complete": True, "tokens": tokens(123), "review_agents": 0}
        with (
            patch.object(benchmark, "load_agent", return_value=agent),
            patch.object(agent, "run_codex", delete_candidate),
            patch.object(benchmark.Meter, "result", return_value=usage),
        ):
            benchmark.run_trial(directory, "clean", "prompted")
        result = json.loads((directory / "result.json").read_text())
        self.assertFalse(result["success"])
        self.assertTrue(result["metrics"]["false_completion"])
        self.assertFalse(result["metrics"]["final_feedback_checked"])
        self.assertEqual(result["usage"]["tokens"]["total_tokens"], 123)

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
