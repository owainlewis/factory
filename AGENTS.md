# Working on Factory

Keep this a minimal Python CLI. The workflow belongs in ordinary Python; use
the Claude Agent SDK for planning and file editing.

- Implementation: `src/factory/cli.py`.
- Stage prompts: `src/factory/prompts/{PLAN,BUILD,VERIFY}.md`.
- Install dependencies: `uv sync --locked`.
- Check changes: `uv run pytest`, `uv run ruff check .`, and
  `uv run ruff format --check .`.
- Tests use real temporary Git repositories and fake agent responses. Do not
  require credentials or model calls in automated tests.
- Preserve the check and review gates: only the exact validated and reviewed
  commit may be published. Missing or failed reviews must stop the run.
- Preserve every attempt on failure. Push generated changes only with `--pr`.
- Leave CI failures and merging to the human; do not add automatic merging.
