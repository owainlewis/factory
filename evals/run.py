"""Small controlled benchmark; production runner behavior stays unchanged."""

import argparse
import asyncio
import hashlib
import json
import os
import random
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import asdict
from datetime import UTC, datetime
from importlib.metadata import version
from pathlib import Path
from types import SimpleNamespace

from claude_agent_sdk import ResultMessage

from factory import runner

ROOT = Path(__file__).resolve().parent
MODES = ("raw", "prompted", "factory")
TOOLS = ["Read", "Glob", "Grep", "Write", "Edit", "Bash"]
CONSTRAINTS = """
Use only this workspace; do not inspect parent directories or evaluation assets.
Do not access the network, delegate to other agents, change Git history, or launch
background processes. Keep the implementation in app.py. You may add tests.
Visible checks: python -m unittest discover -s tests -v
"""


def write_json(path, data):
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(data, indent=2) + "\n")
    temporary.replace(path)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def grade(case, workspace, output, timeout=30):
    """Copy only the public implementation; original tests cannot be weakened."""
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="factory-grade-") as directory:
        snapshot = Path(directory)
        implementation = workspace / "app.py"
        if implementation.is_symlink() or not implementation.is_file():
            result = {"passed": False, "error": "Missing or symlinked app.py"}
        else:
            shutil.copyfile(implementation, snapshot / "app.py")
            command = [
                sys.executable,
                "-I",
                str(ROOT / "grade.py"),
                str(snapshot),
                str(case / "workspace/tests/test_app.py"),
                str(case / "acceptance.py"),
            ]
            try:
                process = subprocess.run(
                    command, capture_output=True, text=True, timeout=timeout, check=False
                )
                (output / "tests.log").write_text(process.stdout + process.stderr)
                try:
                    result = json.loads(process.stdout.strip().splitlines()[-1])
                    result["passed"] = result["passed"] and process.returncode == 0
                except (ValueError, IndexError, KeyError, TypeError):
                    result = {"passed": False, "error": "Grader returned no valid result"}
            except subprocess.TimeoutExpired:
                result = {"passed": False, "error": "Acceptance timeout"}
        write_json(output / "grade.json", result)
        return result


def validate_cases(cases, output):
    failures = []
    for case in cases:
        baseline = grade(case, case / "workspace", output / case.name / "baseline")
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory)
            shutil.copyfile(case / "solution.py", workspace / "app.py")
            reference = grade(case, workspace, output / case.name / "reference")
        valid = not baseline["passed"] and baseline.get("tests", 0) > 0 and reference["passed"]
        print(f"{case.name}: {'valid' if valid else 'INVALID'}", flush=True)
        if not valid:
            failures.append(case.name)
    if failures:
        raise SystemExit("Invalid cases: " + ", ".join(failures))


async def trial_worker(spec_path):
    spec = json.loads(spec_path.read_text())
    directory = spec_path.parent
    workspace = directory / "workspace"
    telemetry = directory / "usage.jsonl"
    original_query = runner.query

    async def measured_query(**kwargs):
        async for message in original_query(**kwargs):
            if isinstance(message, ResultMessage):
                with telemetry.open("a") as stream:
                    stream.write(json.dumps(asdict(message)) + "\n")
            yield message

    runner.query = measured_query
    args = SimpleNamespace(
        cwd=workspace,
        config=directory / "config.toml",
        stage="build",
        task=spec["task"],
        check=None,
        attempts=spec["attempts"],
        model=spec["model"],
        max_turns=spec["max_turns"],
        timeout=spec["seconds"],
    )
    started = time.monotonic()
    outcome = {"completed": False, "error": None}
    try:
        async with asyncio.timeout(spec["seconds"]):
            if spec["mode"] == "factory":
                await runner.run(args)
            else:
                summary = await runner.agent(
                    (directory / "BUILD.md").read_text(),
                    {"task": args.task},
                    TOOLS,
                    workspace,
                    directory / "agent.log",
                    args,
                    {},
                )
                (directory / "summary.md").write_text(summary)
            outcome["completed"] = True
    except Exception as exc:  # noqa: BLE001 — record SDK and transport failures as trial outcomes
        outcome["error"] = f"{type(exc).__name__}: {exc}"
    finally:
        outcome["elapsed_seconds"] = time.monotonic() - started
        write_json(directory / "execution.json", outcome)


def prepare_trial(case, mode, repetition, args, directory):
    directory.mkdir(parents=True)
    workspace = directory / "workspace"
    shutil.copytree(case / "workspace", workspace)
    (workspace / "AGENTS.md").write_text(CONSTRAINTS)
    (workspace / ".gitignore").write_text(".factory/\n__pycache__/\n")
    for command in [
        ["git", "init", "-q"],
        ["git", "add", "."],
        [
            "git",
            "-c",
            "user.name=Eval",
            "-c",
            "user.email=eval@localhost",
            "commit",
            "-qm",
            "Benchmark starting point",
        ],
    ]:
        subprocess.run(command, cwd=workspace, check=True)
    spec = {
        "case": case.name,
        "split": json.loads((case / "case.json").read_text())["split"],
        "mode": mode,
        "repetition": repetition,
        "task": (case / "task.md").read_text(),
        "model": args.model,
        "seconds": args.seconds,
        "max_turns": args.max_turns,
        "attempts": args.attempts,
    }
    write_json(directory / "spec.json", spec)
    (directory / "BUILD.md").write_text((ROOT / f"prompts/{mode}.md").read_text() + CONSTRAINTS)
    shutil.copyfile(ROOT / "prompts/review.md", directory / "REVIEW.md")
    (directory / "config.toml").write_text("""[stages.build]
prompt = "BUILD.md"
checks = ["tests", "review"]
[checks.tests]
command = "python -m unittest discover -s tests -v"
[checks.review]
prompt = "REVIEW.md"
tools = ["Read", "Glob", "Grep", "Bash"]
""")
    return spec


def execute_trial(case, directory, spec):
    started = time.monotonic()
    with (directory / "console.log").open("w") as stream:
        process = subprocess.Popen(
            [sys.executable, str(ROOT / "run.py"), "_worker", str(directory / "spec.json")],
            stdout=stream,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        try:
            process.wait(timeout=spec["seconds"] + 15)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait()
    execution_path = directory / "execution.json"
    execution = (
        json.loads(execution_path.read_text())
        if execution_path.exists()
        else {"completed": False, "error": f"Worker terminated: {process.returncode}"}
    )
    execution["wall_seconds"] = time.monotonic() - started
    workspace = directory / "workspace"
    # Capture all source edits, including files created by the agent.
    subprocess.run(["git", "add", "-N", "."], cwd=workspace, check=True)
    diff = subprocess.run(
        ["git", "diff", "HEAD", "--", "."],
        cwd=workspace,
        capture_output=True,
        text=True,
        check=True,
    )
    (directory / "changes.diff").write_text(diff.stdout)
    score = grade(case, workspace, directory / "acceptance")
    messages = []
    if (directory / "usage.jsonl").exists():
        messages = [
            json.loads(line) for line in (directory / "usage.jsonl").read_text().splitlines()
        ]
    usages = [m.get("usage") or {} for m in messages]
    usage = {
        key: sum(u.get(key, 0) for u in usages)
        for key in (
            "input_tokens",
            "output_tokens",
            "cache_read_input_tokens",
            "cache_creation_input_tokens",
        )
    }
    run_states = list((workspace / ".factory/runs").glob("*/run.json"))
    state = json.loads(run_states[0].read_text()) if run_states else {}
    result = {
        **spec,
        **execution,
        "acceptance": score,
        "accepted": bool(execution["completed"] and score["passed"]),
        "false_completion": bool(execution["completed"] and not score["passed"]),
        "usage": usage,
        "usage_complete": bool(messages) and execution["completed"],
        "list_price_usd": sum(m.get("total_cost_usd") or 0 for m in messages),
        "resolved_models": sorted({model for m in messages for model in m.get("model_usage", {})}),
        "agent_calls": len(messages),
        "worker_attempts": state.get("attempt", 1),
        "human_interventions": 0,
        "human_review": None,
    }
    write_json(directory / "result.json", result)
    print(
        f"{directory.name}: accepted={result['accepted']} time={execution['wall_seconds']:.1f}s",
        flush=True,
    )


def report(output):
    import statistics

    results = [json.loads(path.read_text()) for path in sorted(output.glob("*/result.json"))]
    schedule_path = output / "schedule.json"
    planned = len(json.loads(schedule_path.read_text())) if schedule_path.exists() else len(results)
    lines = [
        "# Controlled workflow comparison",
        "",
        "Automated prompting baselines, not timed human-guided Claude sessions.",
        f"Scored {len(results)}/{planned} planned trials. Missing trials are not successes.",
        "",
        "| Mode | Accepted | False completion | Median seconds | List-price USD* |",
        "| --- | --- | --- | --- | --- |",
    ]
    for mode in MODES:
        rows = [r for r in results if r["mode"] == mode]
        if rows:
            lines.append(
                f"| {mode} | {sum(r['accepted'] for r in rows)}/{len(rows)} | "
                f"{sum(r['false_completion'] for r in rows)} | "
                f"{statistics.median(r['wall_seconds'] for r in rows):.1f} | "
                f"{sum(r['list_price_usd'] for r in rows):.4f} |"
            )
    lines += [
        "",
        (
            "*SDK list-price estimates, not subscription charges. Interrupted calls may have "
            "missing usage; consult usage_complete. All runs count, including failures."
        ),
        "",
        "## Per-case repeatability",
        "",
        "| Case | Mode | Accepted repetitions |",
        "| --- | --- | --- |",
    ]
    for case in sorted({r["case"] for r in results}):
        for mode in MODES:
            rows = [r for r in results if r["case"] == case and r["mode"] == mode]
            if rows:
                lines.append(
                    f"| {case} | {mode} | {sum(r['accepted'] for r in rows)}/{len(rows)} |"
                )
    lines += [
        "",
        (
            "Human maintainability review has not been scored by this script. "
            "No automatic promotion or prompt tuning is performed."
        ),
    ]
    (output / "report.md").write_text("\n".join(lines) + "\n")
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["validate", "run", "report", "_worker"])
    parser.add_argument("worker_spec", nargs="?", type=Path)
    parser.add_argument("--cases", help="Comma-separated case names; default all")
    parser.add_argument("--modes", default=",".join(MODES))
    parser.add_argument("--repetitions", type=int, default=3)
    parser.add_argument("--model", default="sonnet")
    parser.add_argument("--seconds", type=int, default=300, help="Whole-trial wall-clock budget")
    parser.add_argument("--max-turns", type=int, default=40, help="Per-call safety ceiling")
    parser.add_argument("--attempts", type=int, default=3)
    parser.add_argument("--seed", type=int, default=42)
    parser.add_argument(
        "--jobs", type=int, default=1, help="Concurrent trials; use 1 for latency comparisons"
    )
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if args.action == "_worker":
        asyncio.run(trial_worker(args.worker_spec.resolve()))
        return
    if any(
        n < 1 for n in (args.repetitions, args.seconds, args.max_turns, args.attempts, args.jobs)
    ):
        parser.error("Budgets and repetitions must be positive")
    modes = args.modes.split(",")
    if len(set(modes)) != len(modes) or set(modes) - set(MODES):
        parser.error("Choose distinct modes from " + ",".join(MODES))
    cases = sorted(path for path in (ROOT / "cases").iterdir() if path.is_dir())
    if args.cases:
        names = args.cases.split(",")
        selected = {path.name: path for path in cases}
        if set(names) - set(selected):
            parser.error("Unknown cases")
        cases = [selected[name] for name in names]
    output = (
        args.output or ROOT / "results" / datetime.now(UTC).strftime("%Y%m%dT%H%M%SZ")
    ).resolve()
    if args.action == "report":
        if not args.output:
            parser.error("report requires --output")
        report(output)
        return
    output.mkdir(parents=True, exist_ok=False)
    validate_cases(cases, output / "validation")
    if args.action == "validate":
        return
    paths = [
        path
        for path in ROOT.rglob("*")
        if path.is_file() and "results" not in path.parts and "__pycache__" not in path.parts
    ]
    manifest = {
        "created_at": datetime.now(UTC).isoformat(),
        "arguments": vars(args),
        "suite_sha256": {str(p.relative_to(ROOT)): digest(p) for p in paths},
        "factory_revision": subprocess.check_output(
            ["git", "rev-parse", "HEAD"], text=True
        ).strip(),
        "factory_source_sha256": {
            str(p.relative_to(ROOT.parent)): digest(p)
            for p in (ROOT.parent / "src/factory").glob("*.py")
        },
        "sdk_version": version("claude-agent-sdk"),
        "claude_version": subprocess.check_output(["claude", "--version"], text=True).strip(),
    }
    manifest["arguments"] = {k: str(v) if isinstance(v, Path) else v for k, v in vars(args).items()}
    write_json(output / "manifest.json", manifest)
    jobs = [
        (case, mode, rep)
        for case in cases
        for mode in modes
        for rep in range(1, args.repetitions + 1)
    ]
    random.Random(args.seed).shuffle(jobs)
    write_json(output / "schedule.json", [(c.name, m, r) for c, m, r in jobs])

    def execute(job):
        case, mode, rep = job
        directory = output / f"{case.name}--{mode}--{rep}"
        spec = prepare_trial(case, mode, rep, args, directory)
        execute_trial(case, directory, spec)

    with ThreadPoolExecutor(max_workers=args.jobs) as pool:
        futures = [pool.submit(execute, job) for job in jobs]
        for future in as_completed(futures):
            future.result()
            report(output)
    changed = [
        str(p.relative_to(ROOT))
        for p in paths
        if not p.exists() or digest(p) != manifest["suite_sha256"][str(p.relative_to(ROOT))]
    ]
    write_json(output / "integrity.json", {"unchanged": not changed, "changed": changed})
    if changed:
        raise SystemExit("Evaluation assets changed during the experiment; results are invalid")


if __name__ == "__main__":
    main()
