# Direct Claude Code with subagent review vs Factory

**Factory finished successfully slightly more often and was faster in this run;
there is no demonstrated code-quality advantage over prompted subagent review.**
The stronger direct baseline changes the interpretation of our earlier experiment.
Independent review and repair are capabilities Claude Code can coordinate itself.

We ran **72 fresh trials: 12 tasks × two profiles × three repetitions**, excluding
the two adapter pilot runs. Both profiles used Sonnet 5, Claude Code's default
system prompt, the same task fixtures, builder tools, reviewer instructions and
review tools. Each trial had a 300-second deadline. Order was randomized (seed 42)
with three concurrent slots. Prompts and acceptance tests were frozen before
execution; the integrity check passed.

## Results

| Measure | Direct Claude + subagent review | Factory + separate review |
| --- | ---: | ---: |
| Completed within budget AND passed independent tests | **29/36 (80.6%)** | **30/36 (83.3%)** |
| Code passed independent tests, regardless of completion | 31/36 | 30/36 |
| Successful execution but failed independent tests | 5/36 | 6/36 |
| Timed out | 2/36 | 0/36 |
| Tasks accepted on all three repetitions | 9/12 | 8/12 |
| Median wall time | 75.5 s | 48.6 s |
| 90th percentile wall time, nearest-rank | 165.8 s | 101.3 s |
| Recorded list-price estimate, all runs | **≥ $6.96** | $7.19 |
| Final usage records available | 34/36 | 36/36 |

Both timed-out direct runs had test-passing code but were still completing their
third review. They count as end-to-end failures, not successes. Their missing
final usage records make direct cost and token totals **lower bounds**. We cannot
claim that direct prompting was cheaper. Recorded cost per accepted result is
at least $0.240 for direct and $0.240 for Factory, including failed runs. These
are provider list-price estimates, not subscription charges.

Factory's median was about 36% lower in this configuration. Concurrent timings,
cache state, provider variation and context differences limit generalization.
The one-run acceptance difference is not evidence of broad superiority: there
are only 12 distinct, small synthetic tasks, with correlated repetitions.

### Per-task repeatability

Accepted repetitions require both successful execution and independent tests.

| Case | Direct + subagent | Factory |
| --- | ---: | ---: |
| Atomic save | 1/3 | 2/3 |
| Batch update | 3/3 | 3/3 |
| Config overlay | 3/3 | 3/3 |
| CSV export | 3/3 | 3/3 |
| Cursor pagination | 3/3 | 3/3 |
| Due report | 3/3 | 2/3 |
| JSONL import | 1/3 | 2/3 |
| Label filter | 3/3 | 3/3 |
| Optimistic update | 0/3 | 0/3 |
| Pagination | 3/3 | 3/3 |
| Schema migration | 3/3 | 3/3 |
| Timestamp ordering | 3/3 | 3/3 |

All three direct atomic-save implementations passed the frozen tests; two ran
out of time during review. Both profiles failed every optimistic-update trial
because invalid list/dict status values raised TypeError instead of the required
ValueError. Other failures involved file permissions, strict date formatting and
Unicode JSONL line boundaries. These are reviewed outputs, not skipped reviews.

## Did the direct baseline actually use reviewers?

Yes. All **36/36** direct trials invoked the designated reviewer and received at
least one successful tool return. There were 42 reviewer calls and 40 successful
returns; two calls were interrupted by the deadline. Native final statistics
confirmed 36 completed child reviews across the 34 finished parent sessions;
the timed-out sessions each recorded two more completed returns before interruption.
No background or nested delegation was reported by the available final records.
Reviewer message events identified Sonnet 5, including in the timed-out trials.
No permission denials appeared in final result records.

Factory recorded 40 structured reviews: 36 passes and four retries. Thirty-three
trials needed one build attempt, two needed two, and one needed three. Its
builders did not invoke extra subagents. Both profiles demonstrated a successful
review → repair → re-review sequence on the same JSONL Unicode bug.

This is a prompted direct baseline, not a person manually operating Claude Code.
Factory enforces checks, review output validation and retry limits in code;
the direct parent follows instructions within one continuing conversation.
Factory's builder/reviewer use fresh SDK sessions. Those context differences are
part of the comparison. Turn limits are per call, not equal total token budgets.
Factory also reported auxiliary Haiku usage, which is included in the totals;
build/review work used the same primary Sonnet model.

### Usage including subagents

Top-level CLI `usage` omits child work. This comparison uses `usage_all_models`,
summed from per-model final records, including children and auxiliary models.

| Token category | Direct + subagent, lower bound | Factory |
| --- | ---: | ---: |
| Uncached input | ≥ 918 | 107,725 |
| Cache creation | ≥ 758,478 | 857,537 |
| Cache reads | ≥ 8,001,141 | 8,308,042 |
| Output | ≥ 271,469 | 199,135 |

Token categories have different prices. Missing timed-out-session records are
not zero consumption. Final telemetry is not an independent billing audit.

## Separate post-run quality audit

The timed-out direct reviewers found file-descriptor leaks introduced while
repairing file permissions. A separately labelled fault-injection diagnostic
confirmed that this concern was real and also exposed a gap in our frozen tests:

- Both accepted Factory atomic-save outputs and the one accepted direct output
  leaked a descriptor when setting file permissions raised PermissionError.
- One timed-out direct output fixed that failure path but still leaked on a
  destination-stat failure. The other had no leak in either injected scenario.

These observations **do not change the frozen scores**. They show why neither
“passed tests” nor “review approved” is a complete quality metric, and why the
longer direct reviews should not simply be described as wasted work. The audit
was motivated by observed findings, not preregistered or comprehensive. It is
not a human maintainability rating. Raw candidate sources, diagnostic results
and the diagnostic script are preserved below.

## What this means for Factory

The earlier comparison gave Factory review and repair while the direct baseline
had no subagent review. It did not isolate the value of scripted orchestration.
This stronger comparison supports a narrower conclusion: **Factory provides
explicit execution control and records, but orchestration alone has not shown
better code quality.** Here it completed more reliably within the deadline and
ran faster, while direct prompting produced slightly more test-passing code and
more tasks that passed all repetitions.

Keep the runner small. Invest in independent acceptance checks, retain failures
as regression cases, and test workflow changes against a strong prompted-review
baseline. Before claiming production gains, add fresh real-repository tasks and
measure human review effort. This experiment did not measure human time saved,
maintainability, large-repository scaling or application-building performance.
The original heldout labels no longer represent untouched tasks.

## Evidence and reproduction

- [72 trial records](runs.json), [summary](summary.json), [schedule](schedule.json).
- [Frozen manifest](manifest.json), [integrity](integrity.json), and
  [frozen harness](https://github.com/owainlewis/factory/blob/b8c075d/evals/run.py).
- [Review execution audit](review-audit.json), including real tool IDs and
  structured Factory findings; [tool-input audit](tool-audit.json). The keyword
  audit found no hidden-asset filename references in 1,064 tool calls; this is
  not a sandbox or proof that leakage is impossible.
- [Post-run descriptor audit](fd-audit.json), [candidate sources](atomic-candidates.json),
  and [diagnostic script](fd-audit.py.txt). With raw trial workspaces retained:
  `uv run python evals/reports/2026-09-25-v3/fd-audit.py.txt evals/results/subagent-comparison-v3`.
- [Protocol and command](../../README.md#matched-subagent-review-comparison).

All 72 scheduled trials reconcile with their result records. Full transcripts,
diffs and workspaces remain in the ignored local results directory. This brings
the suite to 252 scored live trials plus 11 exploratory pilot trials; older
cohorts have different protocols and acceptance coverage and are kept separate.
