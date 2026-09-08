"""Acceptance checks kept outside each agent's working directory."""

import argparse
import json
from pathlib import Path
import runpy
import sys

sys.dont_write_bytecode = True


def check(workspace: Path, suite: str) -> dict:
    parse = runpy.run_path(str(workspace / "ports.py"))["parse_ports"]
    examples = [
        ("80", [80]),
        ("443,80,443", [80, 443]),
        (" 80, 443 ", [80, 443]),
        ("8000-8002", [8000, 8001, 8002]),
        ("80-82,81-83", [80, 81, 82, 83]),
    ]
    if suite == "full":
        examples += [("1,65535", [1, 65535]), ("42-42", [42])]
        examples += [
            (value, None)
            for value in ("0", "65536", "3-1", "", "80,", "x", "-1", "1-2-3")
        ]
    failures = []
    for value, expected in examples:
        try:
            actual = parse(value)
            if expected is None or actual != expected:
                failures.append(
                    f"parse_ports({value!r}): expected {expected if expected is not None else 'ValueError'}, got {actual!r}"
                )
        except ValueError:
            if expected is not None:
                failures.append(f"parse_ports({value!r}): unexpected ValueError")
    return {"passed": not failures, "checks": len(examples), "failures": failures}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("workspace", type=Path)
    parser.add_argument("--suite", choices=("ci", "full"), default="full")
    args = parser.parse_args()
    try:
        result = check(args.workspace, args.suite)
    except Exception as error:
        result = {"passed": False, "failures": [f"{type(error).__name__}: {error}"]}
    print(json.dumps(result))
    raise SystemExit(0 if result["passed"] else 1)
