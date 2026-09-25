# Working on Factory

Keep this a small runner: one task stage, distinct checks, one bounded retry loop.

- CLI: `src/factory/cli.py`; execution: `runner.py`; schemas: `config.py`.
- Project prompts and stage/check definitions: `.factory/`.
- Install: `uv sync --locked`.
- Validate: `uv run pytest`, `uv run ruff check .`, `uv run ruff format --check .`.
- Tests use real temporary directories and command processes with fake agents.
  Do not require credentials or model calls in automated tests.
- Setup runs once; every attempt and check shares the configured workspace.
- Missing or invalid structured check output must stop the run.
- Keep Git operations, publishing, and task-specific behavior out of the runner.
- Preserve logs and workspace. Do not add automatic cleanup or merging.
