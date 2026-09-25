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

# 16 tasks × 4 modes × 3 repetitions = 192 runs.
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

The original suite has eight development cases and four labeled heldout; four
additional cases are labeled fresh-v4. Do not tune prompts
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

The [fresh targeted follow-up](reports/2026-09-25-v2/README.md) adds another 36
live trials with stronger tests frozen before execution. The current suite also
covers invalid status types and uses an implementation-independent filesystem
failure test; the reports identify which checks applied to each experiment.

`usage` contains the SDK/CLI final result's usage fields. Complete raw result
records also retain per-model usage, which may include auxiliary model activity.
The list-price estimate comes from the provider's result, not a calculation from
our token columns. `usage_complete` means final records were received for a
successful execution; it is not an independent billing reconciliation.

## Matched subagent-review comparison

To isolate enforced orchestration from the benefit of having a reviewer:

```sh
# Original 12 cases × 2 modes × 3 repetitions = 72 trials.
uv run python evals/run.py run --modes subagent,factory_matched \
  --cases atomic-save,batch-update,config-overlay,csv-export,cursor-pagination,due-report,jsonl-import,label-filter,optimistic-update,pagination,schema-migration,timestamp-order \
  --repetitions 3 --model claude-sonnet-5 --seconds 300 --jobs 3 \
  --output evals/results/subagent-comparison-v3
```

Both profiles use Claude Code's default system prompt, the same primary model,
workspace, task, builder tools (including Agent), reviewer instructions and
read/check tools. The direct `subagent` profile is explicitly prompted to build,
test, invoke the designated foreground reviewer, repair findings, and re-review.
The `factory_matched` profile uses Factory to enforce tests and independent
structured review, with bounded repairs. Its SDK calls opt into the Claude Code
system-prompt preset; the historical `factory` profile remains unchanged.

The common reviewer is `prompts/review_matched.md`. Neither profile uses the
optional Claude Code code-review skill. The subagent inherits its parent's model.
Factory uses separate SDK sessions and validates structured review output; the
direct parent coordinates its reviewer within a continuing conversation. Those
context and enforcement differences are part of the treatment, not eliminated
confounders. Both have a 300-second whole-trial deadline and a 40-turn per-call
ceiling. Three implementation/review cycles are enforced by Factory and requested
in the direct prompt. These are not equal total token budgets.

Record actual Agent tool calls and successful returns in `subagent_review`, plus
native final `subagent_stats`. A prose claim to have reviewed is not evidence.
Report missing or incomplete reviews separately without excluding such trials
from the correctness denominator. `usage_all_models` sums per-model totals from
final result records, including child and auxiliary models. The older `usage`
field can omit subagent tokens and must not be used to compare total consumption
for this pair. Timeouts can lose final telemetry; usage and cost are lower bounds
when this occurs.

Freeze prompts and cases before the 72-run experiment. The two-run pilot is an
adapter/permission check and is excluded from reported comparison results. Run
all cases and repetitions; do not tune against intermediate outcomes. Report
acceptance, false completions, all-pass cases, time, total usage/cost, and actual
review completion. These synthetic tasks and concurrent timings cannot establish
production reliability or saved human time.

The [matched 72-trial comparison](reports/2026-09-25-v3/README.md) found 30/36
end-to-end successes for Factory versus 29/36 for direct Claude with subagent
review. Direct produced test-passing code in 31/36 trials, including two unfinished
review loops. Factory's median was 48.6 seconds versus 75.5 seconds. The report
includes actual review execution, incomplete timeout telemetry, per-task
repeatability and a separately labelled post-run descriptor-leak audit. It does
not establish a code-quality advantage for scripted orchestration.

## Reviewer improvement experiment (v4)

The candidate `.factory/REVIEW-EXPERIMENTAL.md` asks the reviewer to choose up to three
high-risk assumptions from the task, run focused independent probes, provide
concrete evidence, and focus re-review on repairs and affected behavior. It also
removes the project template's dependence on an optional code-review skill.
The historical benchmark reviewer already used direct review without that skill;
its instructions remain unchanged as `review_matched.md`. The production default
was restored after the experiment; the candidate is opt-in only.

The experiment changes only reviewer instructions: `factory_matched` is the
current control; `factory_verified` uses the candidate reviewer; and
`subagent_verified` gives direct Claude exactly the same candidate reviewer.
The latter profiles reuse their existing builder instructions, tools, model,
system-prompt settings and budgets. The evaluated candidate prompt is an exact,
tested copy of the experimental project prompt. No runner stages or result-schema fields were
added. A prompt requests evidence; it does not mechanically prove probes ran.

```sh
uv run python evals/run.py run \
  --cases atomic-save,due-report,jsonl-import,optimistic-update,inventory-reservation,dependency-order,slice-ranges,cache-expiry \
  --modes factory_matched,factory_verified,subagent_verified \
  --repetitions 2 --model claude-sonnet-5 --seconds 300 --jobs 3 \
  --output evals/results/reviewer-comparison-v4
```

This is 48 fresh trials: four selected regressions plus four newly authored tasks,
three profiles and two repetitions. Keep the regression and fresh-task results
separate. The fresh cases were authored for this experiment, not independently
sourced or an external holdout. Freeze prompts and tests before execution; do not
tune or rerun failed trials. Preserve the prior acceptance tests unchanged, and
keep descriptor fault-injection diagnostics separate from frozen scores.

Primary outcome: completed within the deadline AND independently correct.
Also report correctness alone, false completions, timeouts, repeatability,
median/tail latency, usage completeness, and cost including failures. Compare
old versus revised Factory to estimate the prompt change's effect, then compare
revised Factory versus revised direct prompting to assess orchestration. These
are small synthetic samples; no automatic promotion is justified by a small
score difference. Preserve prompt regressions and report mixed outcomes honestly.

The [48-trial reviewer experiment](reports/2026-09-25-v4/README.md) produced 12/16
accepted runs for every profile. The candidate did not improve Factory's score
or its fresh-task results; its recorded cost was slightly higher. The existing
project default was therefore restored, and the exact tested candidate retained
as `.factory/REVIEW-EXPERIMENTAL.md`. To explicitly try it, set the existing
`checks.review.prompt` configuration value to `"REVIEW-EXPERIMENTAL.md"`. The
`verified` benchmark profile names identify the experimental strategy; they do
not mean the candidate has been approved or proved superior.
