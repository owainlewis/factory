# Testing a reviewer-prompt improvement

**The candidate did not improve Factory's acceptance rate. We retained it as an
experimental prompt and restored the existing project default.** All three
profiles passed 12/16 trials, with different failures and costs.

The proposed improvement asked reviewers to identify up to three high-risk
assumptions, run independent probes, give concrete evidence for findings, and
focus subsequent reviews on repairs and affected behavior. It was intended to
address confident approvals of incorrect code without adding runner stages or
more model-output parsing.

## Controlled experiment

We froze the candidate and ran **48 fresh trials**: four known regressions plus
four newly authored tasks, three profiles and two repetitions. The new tasks
cover inventory reservations, dependency ordering, range parsing and cache
expiry. They were authored for this experiment, not independently sourced or an
external holdout. Regression and fresh-task results are reported separately.

- **Existing Factory profile:** unchanged `review_matched.md` benchmark prompt.
- **Candidate Factory:** identical builder, checks, tools and budgets, replacing
  only reviewer instructions with `review_verified.md`.
- **Candidate direct:** Claude Code coordinating a reviewer subagent with those
  same candidate instructions.

Every profile used Sonnet 5 and Claude Code's default system prompt, a 300-second
whole-trial deadline, up to three build/review cycles, and a 40-turn per-call
ceiling. Trials were randomized together with seed 42 and three concurrent slots.
The runner enforced Factory's cycles; direct Claude was instructed to follow them.
Factory used separate SDK sessions; direct Claude used a continuing parent with
subagents. There was no tuning or rerunning of failed trials during the experiment.
All starting implementations failed and all references passed the independent
checks before execution. The frozen-assets integrity check passed.

The control is the prior **benchmark** reviewer, not the optional Claude Code
code-review skill used by the project's existing default template. The candidate
was an exact tested copy of the provisional project prompt at revision `dbaab0e`.
After seeing the results, we restored the default and preserved that candidate as
[REVIEW-EXPERIMENTAL.md](../../../.factory/REVIEW-EXPERIMENTAL.md). No production
runner logic or check-result schema changed.

## Results

| Measure | Existing Factory profile | Candidate Factory | Candidate direct |
| --- | ---: | ---: | ---: |
| Completed within budget AND passed independent tests | **12/16** | **12/16** | **12/16** |
| Code passed independent tests | 12/16 | 12/16 | 12/16 |
| Known regressions accepted | 4/8 | 4/8 | 5/8 |
| Fresh tasks accepted | 8/8 | 8/8 | 7/8 |
| Successful execution but failed independent tests | 4 | 4 | 3 |
| Timeouts | 0 | 0 | 1 |
| Tasks accepted on both repetitions | 5/8 | 4/8 | 5/8 |
| Median wall time | 90.5 s | 88.5 s | 130.2 s |
| 90th percentile, nearest-rank | 144.8 s | 142.0 s | 230.3 s |
| Total wall time summed across trials | 1,513.8 s | 1,649.5 s | 2,324.1 s |
| Recorded list-price estimate, all trials | $4.37 | $4.53 | **≥ $4.60** |
| Recorded cost per accepted result | $0.364 | $0.378 | **≥ $0.384** |
| Final usage available | 16/16 | 16/16 | 15/16 |

The candidate's median was about two seconds lower, but aggregate elapsed time
was about 9% higher and its recorded cost about 4% higher. With eight small tasks
and two repetitions, these differences do not establish a general performance
ranking. The candidate did not improve correctness, false completions or the
fresh-task score. It also did not improve the number of consistently passed tasks.
That evidence does not justify changing the default.

The direct timeout occurred during atomic-save review, and the unfinished code
also failed the existing-file-mode test. Its final token/cost record is missing,
so direct totals are lower bounds, not zero consumption for that run. Unlike the
prior experiment's two timeouts, this was not a test-passing unfinished workflow.
Dollar figures are provider list-price estimates, not subscription charges.

### Per-case outcomes

| Case | Existing Factory | Candidate Factory | Candidate direct |
| --- | ---: | ---: | ---: |
| Atomic save — regression | 2/2 | 1/2 | 1/2 |
| Strict due dates — regression | 1/2 | 1/2 | 2/2 |
| JSONL import — regression | 1/2 | 1/2 | 2/2 |
| Optimistic update — regression | 0/2 | 1/2 | 0/2 |
| Inventory reservation — fresh | 2/2 | 2/2 | 2/2 |
| Dependency ordering — fresh | 2/2 | 2/2 | 2/2 |
| Slice ranges — fresh | 2/2 | 2/2 | 1/2 |
| Cache expiry — fresh | 2/2 | 2/2 | 2/2 |

The candidate Factory gain on optimistic update came from an already-correct
builder implementation, with no repair requested by review. We cannot attribute
that isolated success to the new review instructions. It also lost an atomic-save
success relative to the control. Both Factory profiles hit a ceiling on the four
fresh tasks; harder, realistic repository tasks are still needed.

### Token usage including child and auxiliary models

| Category | Existing Factory | Candidate Factory | Candidate direct, lower bound |
| --- | ---: | ---: | ---: |
| Uncached input | 57,714 | 58,886 | ≥ 478 |
| Cache creation | 477,729 | 485,778 | ≥ 483,388 |
| Cache reads | 4,614,500 | 4,821,127 | ≥ 4,458,191 |
| Output | 147,581 | 156,648 | ≥ 208,693 |

These are `usage_all_models` totals, not the top-level CLI usage that can omit
children. Factory's reported auxiliary Haiku usage is included. Primary
build/review work used Sonnet 5. Cache state, concurrent execution and provider
variation remain confounders; final telemetry is not a billing reconciliation.

## Did the changed instructions take effect?

Yes. The traces show additional reviewer probes, including inline Python checks.
The existing Factory profile produced 20 structured reviews: 16 pass and four
retry. The candidate produced 19: 16 pass and three retry. In each case, retries
led to another build/review attempt, with no stop results or failed executions.

All 16 direct trials invoked the designated reviewer and received at least one
successful return. There were 21 calls and 20 successful returns; one review was
interrupted by the deadline. No permission denials appeared in final records.
Factory builders did not invoke additional subagents. Recorded direct reviewer
messages used Sonnet 5.

Two failures are particularly informative:

- A candidate Factory reviewer demonstrated acceptance of a non-`YYYY-MM-DD`
  date, described that behavior in its feedback, but dismissed it and returned
  pass. Executing a probe did not guarantee correct interpretation of its result.
- A direct range-parser run passed 30 builder tests and independent review but
  accepted a trailing newline, violating the explicit allowed-whitespace rule.
  The frozen acceptance tests caught it.

The practical lesson is to keep independently enforced behavior checks as the
acceptance gate. More review instructions or more detailed feedback alone are
not evidence of better code.

## Supplemental descriptor diagnostic

We reused the previous experiment's fault-injection diagnostic on all six
atomic-save outputs, separately from frozen acceptance scoring. Setting file
permissions raised PermissionError and leaked a descriptor in one accepted
output from **each** profile: existing Factory repetition 2, candidate Factory
repetition 1, and candidate direct repetition 1. None of the profiles eliminated
this known gap in the acceptance suite. Unexercised fault paths are marked as
such; absence of a leak in this diagnostic is not a general correctness grade.

The diagnostic does not change the headline scores. Its source, candidate sources
and results are preserved for inspection and future evaluator hardening.

## Decision and evidence

Keep the current default. Preserve the candidate for explicit experiments by
setting `checks.review.prompt = "REVIEW-EXPERIMENTAL.md"` in the existing project
configuration. The `verified` profile names describe the experimental method;
they are not an endorsement. The new cases, comparison profiles and diagnostics
remain available for the next iteration.

This experiment establishes neither a universal advantage for orchestration nor
an impossibility of improving prompts. It rejects this proposed improvement as
a justified default change on the evidence collected. No human maintainability
ratings, saved human time, large-repository scaling or full application builds
were measured.

- [48 trial records](runs.json), [summary](summary.json), [schedule](schedule.json).
- [Frozen manifest](manifest.json), [integrity](integrity.json), and
  [frozen harness](https://github.com/owainlewis/factory/blob/dbaab0e/evals/run.py).
- [Review execution and findings](review-audit.json), [tool-input audit](tool-audit.json).
  No hidden-asset filename references were found in 801 recorded tool calls;
  this keyword audit is not a security sandbox or proof of no leakage.
- [Descriptor diagnostic](fd-audit.json), [candidate sources](atomic-candidates.json),
  [diagnostic script](fd-audit.py.txt). Reproduce with retained raw workspaces:
  `uv run python evals/reports/2026-09-25-v4/fd-audit.py.txt evals/results/reviewer-comparison-v4`.
- [Protocol and command](../../README.md#reviewer-improvement-experiment-v4).

All 48 scheduled trials reconcile with their records. Full logs, diffs and
workspaces remain in the ignored results directory. The suite now preserves
300 scored live trials plus 11 pilot trials across separately labelled cohorts.
