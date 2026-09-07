#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.10"
# dependencies = ["openai-codex==0.147.0"]
# ///
"""Run one Codex task using the CLI on PATH. Read-only unless --write is set."""

import argparse
from pathlib import Path
import shutil
import sys

from openai_codex import Codex, CodexConfig, Sandbox
from openai_codex.types import TurnStatus


def local_config() -> CodexConfig:
    codex_bin = shutil.which("codex")
    if not codex_bin:
        raise RuntimeError("codex was not found on PATH; install and sign in first")
    return CodexConfig(codex_bin=codex_bin)


def run_agent(prompt: str, cwd: Path, *, write: bool = False) -> str:
    cwd = cwd.expanduser().resolve()
    if not cwd.is_dir():
        raise ValueError(f"workspace is not a directory: {cwd}")
    sandbox = Sandbox.workspace_write if write else Sandbox.read_only
    with Codex(local_config()) as codex:
        thread = codex.thread_start(cwd=str(cwd), sandbox=sandbox)
        result = thread.run(prompt)
        if result.status != TurnStatus.completed:
            detail = result.error.message if result.error else result.status.value
            raise RuntimeError(f"agent did not complete: {detail}")
        return result.final_response


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("prompt")
    parser.add_argument("--cwd", type=Path, default=Path.cwd())
    parser.add_argument("--write", action="store_true", help="allow workspace edits")
    args = parser.parse_args(argv)
    try:
        print(run_agent(args.prompt, args.cwd, write=args.write))
        return 0
    except Exception as error:
        print(str(error), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
