# Does code orchestration save tokens?

Measure it before making the claim. A workflow can save agent calls while spending more
on repeated context during repairs. This suite compares two ways to handle the **repair
stage** of `agent.py`. It is a starting experiment, not a benchmark of full delivery. The [first pilot](agent-benchmark-pilot.md) records
six successful trials and the limits of what they show.

[Experiment 2](agent-benchmark-experiment-2.md) completed 24 matched trials with
timing, correctness, repair-cycle metrics, and a retained-context candidate. The
[results and trial data](agent-benchmark-experiment-2-results.md) show a small local time
advantage for the current script, but the candidate failed its adoption rule.

## Run locally

Use your installed, authenticated Codex CLI and `uv`. No API key setup is needed when the
CLI already uses your ChatGPT login. Runs consume normal Codex usage. Start with one pair:

```sh
uv run evals/agent_benchmark.py run \
  --output .machinist/benchmarks/repair-smoke \
  --cases repair --repeats 1
```

Then run the original three cases three times, producing 18 trials:

```sh
uv run evals/agent_benchmark.py run \
  --output .machinist/benchmarks/baseline-v1 \
  --cases clean repair review-only --repeats 3 --seed 1
```

The output directory must be new. The suite creates ordinary local fixture folders,
not Git worktrees, issues, branches, or pull requests. Results stay in the specified
folder; `.machinist/` is already ignored. Codex keeps its usual local session records.
Trials run serially and stop after 600 seconds each by default. `--timeout` changes that
limit. A timeout is a failed trial with unknown usage, never a zero-token success.
The command exits nonzero if a trial fails or its usage is incomplete.

Each trial saves its starting feedback and prompts, final workspace, agent log, acceptance
results, elapsed time, thread IDs, and usage counters. `manifest.json` records run order,
versions, source hashes, and limits. The model and reasoning effort follow your local
Codex configuration and are recorded from each participating thread's rollout. Keep that
configuration unchanged during a comparison. Regenerate a report without starting agents:

```sh
python3 evals/agent_benchmark.py report .machinist/benchmarks/baseline-v1
python3 -m unittest evals.test_agent_benchmark
```

## What is compared?

| Arm | Control flow | Agent context |
| --- | --- | --- |
| `scripted` | Production `agent.iterate()` and `REPAIR_PROMPT`, with local feedback replacing GitHub | Fresh conversation for each repair pass |
| `prompted` | One autonomous prompt tells the agent to triage, fix, review, check feedback, and stop | One conversation retains context |
| `scripted-reuse` (opt in) | Production Python loop with the same prompts | One maker conversation across repair calls |

All arms use production `run_codex()`, the same CLI and SDK, identical starting code,
requirements, feedback, acceptance checks, sandbox settings, and review budget: at most
three repair passes, each with at most three fresh review rounds. The baseline is a real
autonomous prompt, not a deliberately vague instruction. It gets the same instruction to
skip work when no action is needed.

A common developer instruction adapts both arms to local operation: edits stand in for
pushing, the summary stands in for replying to review comments, and a local command
replaces CI polling. Both use a workspace-write sandbox with approvals denied. This is
an intentional difference from production's full-access setting. The harness changes no
production behavior. Local fixtures and ordinary session history are the only state.

The cases use a small port-list parser so runs are inexpensive and defects are testable:

| Case | Starting state | What it tests |
| --- | --- | --- |
| `clean` | Correct code, green CI, no review findings | Cost of deciding there is nothing to do |
| `repair` | Range expansion drops the final port; CI fails | Repair, verification, and independent review |
| `review-only` | Port zero is accepted; CI misses it but a review identifies it | Acting on valid review feedback despite green CI |
| `follow-up` | Initial range bug, then a follow-up client requirement | Repeated repair work and retained maker context |

The final checker lives outside the agent workspace, covers more than CI, and is hashed
by the runner. The controller runs it inside a Codex read-only sandbox. The feedback CLI
requests the controller result through local files, without importing agent-written code. Completion requires passing acceptance checks, a completed report for PR
1, and native reviewer participation when code changed. Final feedback must cover the final
code, and follow-up cases must receive their follow-up requirement. The child count establishes participation,
not the quality or independence of its reasoning. Read the saved logs and native Codex
sessions before using a result in published material.

## How to read results

Read success and failure first. A cheap failure is not a win. Every attempted trial stays
in the report. Per-case token ratios use only pairs where both arms succeeded with complete
usage and matching recorded model and reasoning settings. Source changes during a run
invalidate the experiment. Keep the excluded pairs and failure rate visible alongside that conditional ratio.
A ratio below 1 favours the first named arm. The report includes per-case medians and
ranges for token counts and elapsed time, matched wins, repair cycles, regressions, and
false completion. Correct code with missing usage is labelled INCOMPLETE.

The meter reads the final cumulative token counter once for each root and recursively
identified native subagent. It does not add cumulative snapshots or treat missing records
as zero. Cached input is already included in input, and reasoning is already included in
output. Raw token totals are not a dollar estimate. Compare cached input and output too;
two runs with the same total can have different billed costs.

Usage collection depends on SDK 0.147's thread records and the CLI's local rollout format.
Missing or unreadable records invalidate token accounting. Only native agents reached
through spawn calls or native subagent activity records are counted. Nested model clients are prohibited in the common
instructions; this is a cooperative local benchmark, not a hostile-agent containment test.

The suite groups selected arms by case and repetition, then randomizes group and arm order.
This reduces order effects but does not control server-side caches or service load.
One repeat is a wiring check. Three repeats reveal obvious variance, not statistical proof.
Do not combine the no-work case with repair cases to claim general token savings.

## Improve the experiment

1. Establish a baseline with unchanged code, prompts, model, and configuration. Inspect
   every trial. Record invalid runs and the reason before rerunning anything.
2. Change one thing, such as shortening the repair prompt or retaining maker context.
   Run the same cases and seeds again. Compare quality before token use.
3. Add realistic cases from your own failures: stale reviews, contradictory comments,
   multiple repair passes, larger repository context, and tests that pass for a wrong fix.
   Keep new cases separate until their graders have been checked against known broken code.
4. For a full factory claim, add matched issue-to-PR runs in a scratch GitHub repository.
   Measure initial implementation, PR creation, CI/reviewer delays, failures, and human
   intervention. Those stages, and the cost of developing and maintaining the workflow, are excluded here.

The honest narrative to test is: **code can remove routine decisions from agent calls;
agents still handle the judgment.** Whether that saves tokens on meaningful work is an
experimental result, not a premise of this suite.
