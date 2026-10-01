"""CLI for a task stage and its acceptance checks."""

import argparse
import asyncio
import shutil
import sys
from importlib.metadata import version
from pathlib import Path

from factory.runner import FactoryError, run

DISTRIBUTION_NAME = "factory-cli"


def positive_int(value: str) -> int:
    number = int(value)
    if number < 1:
        raise argparse.ArgumentTypeError("must be greater than zero")
    return number


def parser() -> argparse.ArgumentParser:
    cli = argparse.ArgumentParser(
        description="Run a task, check the result, and retry with feedback"
    )
    cli.add_argument(
        "--version",
        action="version",
        version=f"%(prog)s {version(DISTRIBUTION_NAME)}",
    )
    cli.add_argument("task", nargs="?", help="Task prompt; asks interactively if omitted")
    cli.add_argument("--stage", default="build", help="Named task stage (default: build)")
    cli.add_argument("--check", help="Comma-separated check names; overrides the stage's checks")
    cli.add_argument("--cwd", type=Path, default=Path.cwd(), help="Starting directory")
    cli.add_argument("--config", type=Path, default=Path(".factory/config.toml"))
    cli.add_argument("--attempts", type=positive_int, default=1, help="Total attempts (default: 1)")
    cli.add_argument("--model", help="Claude model (default: SDK default)")
    cli.add_argument("--max-turns", type=positive_int, default=30)
    cli.add_argument(
        "--timeout", type=positive_int, default=600, help="Seconds per agent or command"
    )
    return cli


def main() -> int:
    args = parser().parse_args()
    try:
        args.task = (args.task if args.task is not None else input("Task: ")).strip()
        if not args.task:
            raise FactoryError("Task cannot be empty.")
        if not shutil.which("claude"):
            raise FactoryError("Install and authenticate Claude Code on PATH.")
        asyncio.run(run(args))
    except KeyboardInterrupt:
        print("\nInterrupted. Workspace and logs are preserved.", file=sys.stderr)
        return 130
    except Exception as exc:  # noqa: BLE001 -- report configuration, SDK, and process errors at the CLI boundary
        print(f"factory: {str(exc) or type(exc).__name__}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
