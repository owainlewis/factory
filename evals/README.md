# Evaluating Factory

This suite compares **automated task-only prompting**, a **reusable workflow
prompt**, **Factory's enforced tests + separate review + bounded repair**, and direct
**Claude Code (`claude -p`) with its default system prompt**.
It measures correctness and repeatability on small standard-library Python tasks.
It is not a timed comparison with a person operating Claude Code interactively.

## Run

From the repository root after `uv sync --locked` and Claude authentication:

```sh
# No model calls: prove each broken baseline fails and each reference passes.
uv run python evals/run.py validate

# Nine-run smoke test before a larger experiment.
uv run python evals/run.py run --cases pagination,atomic-save,optimistic-update \
  --modes raw,prompted,factory --repetitions 1 --output evals/results/pilot

# 12 tasks × 4 modes × 3 repetitions = 144 runs.
uv run python evals/run.py run --repetitions 3 --jobs 3 \
  --output evals/results/comparison

# Rebuild a report without model calls.
uv run python evals/run.py report --output evals/results/comparison
```

Output directories must be new, preventing accidental overwrites. Transcripts,
workspaces, diffs, reference-validation logs, telemetry, and per-run JSON are
retained under the output directory. Results are ignored by Git; publish a
sanitized report and aggregate results after inspecting them. Live evals are not
part of CI. Ordinary `uv run pytest` verifies the harness without credentials.

## Protocol

All modes receive identical task text, starting code, visible tests, tools, model,
workspace restrictions, and whole-trial wall-clock allowance. Raw and prompted
SDK modes have one agent session and may run tools, tests, and repairs within it.
Factory uses the actual `factory.runner.run`, including fresh review and repair
sessions. The `claude_cli` mode invokes the installed CLI directly without overriding its
default system prompt. Settings and tool permissions match the other modes, but
the different built-in system prompt is an intentional real-world baseline.
No production runner is replaced. Its SDK query stream is observed to
capture structured `ResultMessage` usage; natural-language logs aren't parsed.

`--seconds` (default 300) applies to the **entire trial**, not each Factory stage.
`--max-turns` is a per-call safety ceiling, not an equal total token budget.
Factory can spend more tokens across calls; this is measured, not hidden.
`--attempts` bounds Factory repairs. Run order is randomized with a recorded seed.
Use a pinned model name when available; results record resolved model names.

`--jobs 1` is the default for clean elapsed-time comparisons. Concurrent runs can
experience shared rate limits and contention. Compare elapsed times only within
the same concurrency/budget setting, and label them accordingly. Cached tokens
are recorded separately; cache warming and provider changes remain confounders.

Cases are immutable file snapshots, hashed in the manifest along with prompts
and harness sources. Factory revision, source hashes, SDK/CLI versions, and all
settings are recorded. Each trial gets a separate local Git repository and
session. The agent sees only the task workspace. No reference implementation or
held-out test is copied there. This is **instructional isolation, not a security
sandbox**: the local agent has Bash access. Do not use this harness for adversarial
agents or untrusted submissions. Use disposable containers/VMs for that threat model.

After the agent session ends, an independent process tests a snapshot of app.py
against the original visible tests and held-out acceptance tests. Agent edits to
visible tests cannot weaken final grading. All cases explicitly require a
single-file, standard-library implementation. Missing files, import failures,
zero tests, skipped tests, grader errors, and timeouts fail closed.

## Measurements

- **Accepted:** agent execution completed successfully AND independent tests pass.
- **Acceptance:** functional correctness separately, even if execution did not finish.
- **False completion:** successful execution with failed independent tests. For raw
  modes, execution success does not necessarily mean the prose claimed success;
  this is an operational signal, not semantic analysis of the final message.
- **Repeatability:** accepted repetitions per case; inspect all-pass cases as well
  as the overall rate. Repetitions of the same task are not independent tasks.
- **Time:** wall-clock duration including agent startup, tests, review, and repair;
  excludes fixture preparation and independent final grading.
- **Usage:** input, output, cache-read, and cache-creation tokens across completed
  SDK calls. Interrupted calls can lack final telemetry; `usage_complete=false`
  flags a lower bound. `list_price_usd` is the SDK estimate, not a subscription bill.
- **Intervention:** these runs have no mid-task help. Human time spent authoring
  tasks, implementing this harness, or reviewing results is not measured.
- **Maintainability:** `human_review=null` until reviewed separately. Passing
  automated tests is not a maintainability score.

Count failed runs in resource totals. Compare modes on the same cases; do not
cherry-pick successful outputs. Small samples cannot establish broad superiority.

## Improving the workflow

Eight cases are development cases; four are labeled heldout. Do not tune prompts
against held-out results and then reuse them as untouched evidence. The cases
are intentionally small and synthetic: a ceiling result is evidence that the
suite needs more difficult realistic tasks, not that every approach is equally
good on production repositories.

Freeze the suite and baseline, propose one prompt/check/runner change, and rerun
matched cases. Inspect regressions and time/usage before promoting the change.
Do not let the optimizing agent edit the evaluator or reference answers.
Add real bug reproductions and app follow-up changes as failures teach us what
this set misses. Keep setup/timeout/retry mechanics in runner unit tests rather
than mixing those successes into coding acceptance rates.

For an actual human-guided baseline, have a person run the same task in a fresh
copy without seeing the answers, record active minutes and interventions, then
submit app.py to the same grader. Automated baselines cannot establish saved
human time on their own.

The review prompt in this experiment is `prompts/review.md`: a direct independent
review with structured output, not the optional Claude Code code-review skill.
This keeps tool access equal and avoids testing skill availability. Treat results
as evidence for this recorded workflow configuration, not every Factory setup.

Grade a human-produced implementation without making any model calls:

```sh
uv run python evals/run.py grade --cases pagination \
  --candidate /path/to/human-workspace --output evals/results/human-pagination
```

Record active human minutes, total elapsed time, prompts/interventions, and model
usage alongside that result. Do not label an automated prompting run as manual.


## Suite revisions and evidence

The [first experiment](reports/2026-09-25-v1/README.md) preserves original metrics,
manifests, and a separately labelled follow-up audit. It is not rescored in place.
The audit exposed missing acceptance criteria: JSONL Unicode line separators,
strict calendar-date formatting, and atomic-write file modes. The current suite
adds those tests and fixes the reference implementations. Prompts and task text
were not tuned to the audit outcomes. Comparisons across these suite revisions
must distinguish new acceptance coverage from a change in agent performance.

The four `heldout` labels describe the original development split. These cases
have now been evaluated and inspected; they are not an untouched future holdout.
Add fresh cases before making claims about generalization.
