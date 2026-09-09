# Experiment 2: retain maker context between repairs

Protocol written before the measured runs on 9 September 2026. The user authorised
24 local trials. The question is whether preserving the maker conversation improves
time or token use without reducing correctness. Production `agent.py` remains unchanged
until the result justifies a change.

## Design

Two cases, three arms, four repetitions: 24 trials. All run serially with randomised
arm order inside each matched case/repetition, using seed 29. Keep the installed Codex
model, reasoning effort, SDK, prompts, checker, and source fixed throughout.

- `repair`: the original failing-test repair. This is a control where retaining context
  should have little effect because one repair call is normally enough.
- `follow-up`: the same initial repair, then a client requirement to accept whitespace
  around range endpoints after the initial checks pass. The controller updates the
  requirements and supplies the same review to each arm. It never edits candidate code.
- `prompted`: one autonomous conversation owns the loop.
- `scripted`: production Python control flow starts a new maker conversation each pass.
- `scripted-reuse`: the same Python control flow, prompt, and review budget keep one
  maker conversation across passes. Reviewers are still fresh native subagents.

The expanded checker is readable. An agent might anticipate the follow-up and complete
both changes in one pass. Keep that result, and report whether the retained-context
case actually exercised two or more observed repair cycles. Do not force wasted work
or exclude a correct early implementation to make the hypothesis look better.

```sh
uv run evals/agent_benchmark.py run \
  --output .machinist/benchmarks/context-reuse-24 \
  --cases repair follow-up \
  --arms scripted prompted scripted-reuse \
  --repeats 4 --seed 29 --timeout 600
```

## Measurements

| Measure | Definition | Interpretation |
| --- | --- | --- |
| Correct completion | Independent acceptance checks pass, report is completed for the same synthetic PR, final feedback covers the final code, required follow-up was delivered, requirements are intact, and reviewer participation was recorded | Primary quality measure |
| False completion | Agent reports completed while independent acceptance checks fail | Reliability failure |
| Observed repair attempts | Candidate code changes between consecutive controller feedback observations | Repair/publish cycles, not every edit or local test attempt |
| First attempt passes | First observed changed candidate passes the requirements in effect before new feedback arrives | A later requirement does not retroactively make a correct first repair fail |
| Regressions | Previously passing acceptance cases fail at a later feedback observation | Rework signal; newly added cases are not regressions |
| Reviewers | Distinct native reviewer conversations | Participation count, not proof of one independent review at the correct point of every cycle |
| Wall time | Parent-measured trial process duration, including initial feedback, SDK setup/shutdown, final grading, and feedback transport | End-to-end local trial latency |
| Agent session time | Time inside maker calls, including tools, reviewers, and waiting | Not pure model inference time; can overlap controller work |
| Tokens | Maker plus native descendants, final cumulative counters counted once per conversation | Report total, uncached input, cached input, and output separately |
| Measurement validity | All identified usage records are readable and model/effort match | Correct code with missing usage is INCOMPLETE, not a valid measured success |

There is no simulated CI delay in this experiment. Remote waiting and human intervention
time are not measured. Each trial allows 600 seconds. Failure, timeout, missing usage,
and source changes remain visible. Attempt counts and review counts are diagnostics;
reducing them is useful only while correctness holds.

## Decision rule

Treat the first two repetitions as exploratory and the last two as confirmation. The
candidate is fixed before all four repetitions; no tuning or selective reruns are
allowed between them. These small samples reveal practical signals, not statistical
proof or a general claim about software factories.

Consider adopting retained maker context only if:

1. All candidate trials complete correctly with complete accounting, with no false
   completion, regression, or loss of reviewer participation.
2. On follow-up work that actually requires multiple repair cycles in both script arms,
   median paired wall time improves by at least 15%, at least three of four matched
   observations favour reuse, and both confirmation observations favour reuse.
3. Median wall time on the single-pass control is no more than 10% worse, and uncached
   input and output tokens each increase by no more than 10% in their per-case medians.
4. The final implementation passes focused tests and independent review. If too few
   multi-pass pairs occur, report insufficient evidence for the context-reuse hypothesis.

If the candidate fails this rule, preserve production behavior and identify the next
single change from the failure logs or timing data. Stop after the authorised 24 trials
and review the evidence before spending more runs. A neutral result is useful.

## Local execution boundary

The controller runs candidate code through `codex sandbox --permission-profile :read-only`
with no unsandboxed fallback. The agent's feedback CLI only requests a controller result
through local files; it never imports candidate code. Controller writes use atomic file
replacement and reject symlink parents. Unit fixture tests can substitute a plain checker
command for trusted test code; live trials always use the sandbox.

The normal acceptance command may also run inside the coding agent's own workspace
sandbox. This experiment assumes cooperative agents, not hostile grading attacks.
The read-only sandbox and safe metadata writes prevent candidate execution from gaining
the controller's unrestricted filesystem writes.
