# Working on Factory

Keep this a minimal Python CLI. The workflow belongs in ordinary Python; use
the Claude Agent SDK for planning and file editing.

- Implementation: `src/factory/cli.py`.
- Install dependencies: `uv sync --locked`.
- Check changes: `uv run pytest`, `uv run ruff check .`, and
  `uv run ruff format --check .`.
- Tests use real temporary Git repositories and fake agent responses. Do not
  require credentials or model calls in automated tests.
- Preserve the validation gate: failures must never merge, and only the exact
  validated commit can be merged into an unchanged original checkout.
- Preserve run artifacts on failure. Never push generated task changes
  automatically.
