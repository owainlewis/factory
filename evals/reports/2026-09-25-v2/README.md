# Fresh trials with stronger acceptance — 25 September 2026

**Factory passed more of these difficult cases, but was slower and still produced
three false-completion signals out of nine runs.** The evidence supports a useful
review/repair mechanism, not a claim of reliably autonomous coding.

After the [first experiment and audit](../2026-09-25-v1/README.md), we froze stronger
acceptance tests before making new model calls. Task text and workflow prompts
were unchanged. Three affected cases (atomic saves, date formatting, and JSONL
Unicode handling), four modes, and three repetitions produced **36 fresh trials**.
All modes were randomized together with three concurrent slots, Sonnet 5, and the
same 300-second whole-trial budget. Evaluator integrity passed; every execution
completed and every final usage record was available.

| Mode | Passed independent tests | False completion | Median seconds | Total list-price estimate |
| --- | --- | --- | --- | --- |
| Task-only SDK prompt | 1/9 | 8/9 | 32.0 | $0.66 |
| Reusable workflow prompt | 2/9 | 7/9 | 36.4 | $0.73 |
| Factory: tests + review + repair | 6/9 | 3/9 | 118.5 | $2.44 |
| Direct Claude Code CLI | 3/9 | 6/9 | 28.2 | $0.89 |

A false completion means execution finished successfully but independent tests
failed. It is not semantic analysis of the final prose. For Factory specifically,
the failing outputs had also passed its configured tests and structured review.
SDK dollar values are list-price estimates, **not subscription bills**.

| Case | Task-only SDK | Workflow prompt | Factory | Direct CLI |
| --- | --- | --- | --- | --- |
| Atomic saves, including file modes | 0/3 | 0/3 | 2/3 | 0/3 |
| Strict due-date formatting | 1/3 | 1/3 | 3/3 | 3/3 |
| JSONL Unicode record handling | 0/3 | 1/3 | 1/3 | 0/3 |

Factory completed six runs only after repairs. It was repeatably successful on
just one of the three cases; direct CLI was also repeatably successful on one.
JSONL handling remained unreliable across every mode. That matters more than the
headline 6/9 score: an improved average is not the same as consistent correctness.

Including failed runs, estimated cost per passing result was about $0.66 for
task-only SDK prompting, $0.37 for the reusable prompt, $0.41 for Factory, and
$0.30 for direct CLI. Factory's additional work sometimes pays off relative to a
weak baseline, but did not make it the cheapest approach per correct result here.
Human intervention time and human maintainability judgments were not measured.

## Interpretation and limits

The original easy acceptance suite showed no quality difference and substantial
Factory overhead. Better tests revealed defects that the original score hid.
Fresh runs reproduced some benefit from independent review, while also showing
that reviewers miss requirements and results vary between repetitions.

This is a **targeted follow-up**, selected using earlier review findings, not a
representative sample of software development. There are only three distinct
tasks; repetitions are not independent task samples. No broad statistical
superiority claim follows from these counts. The tasks are single-file synthetic
Python exercises, not application builds or changes to large existing systems.
The review prompt is a direct structured review, not Claude's code-review skill.
Human-guided interactive Claude sessions remain a separate, unmeasured baseline.

The practical next step is to make independently verified behavior checks the
main acceptance gate, then measure when separate review earns its cost. Preserve
these failures as regression cases. Test future workflow changes on additional
real repository bugs and app follow-up tasks, rather than tuning until this small
set is memorized. No production workflow was automatically changed or promoted.

## Additional evaluator hardening

A separately labelled source audit found that all 12 original optimistic-update
outputs raised TypeError for list/dict status values, despite the task requiring
ValueError. The current suite adds that regression check. It was not part of this
three-case experiment and is not included in either score table above.

We also replaced an implementation-coupled `os.replace` mock with a real
filesystem replacement failure. The old test could wrongly reject a valid
`from os import replace` implementation. A harness regression test now covers
that import style. Stored atomic-save outputs were rechecked to verify this
change did not alter the earlier strengthened-test pass/fail results.

The current runner additionally records Python/platform/lockfile provenance and
rejects accidentally evaluating a different checkout's installed Factory module.
These are post-experiment harness improvements; the manifest below identifies
the exact older revision used for this experiment.

## Evidence

- [All 36 per-trial records](runs.json), [summary](summary.json), [schedule](schedule.json).
- [Frozen manifest](manifest.json) at revision `d8b8c6f` and [integrity result](integrity.json).
- Exact acceptance test sources used for this experiment are preserved alongside this report.
- [Additional status audit](../2026-09-25-v1/status-audit-results.json) and [model source-review notes](../2026-09-25-v1/qualitative-review.json), explicitly not human ratings.
- [Atomic grading recheck](grading-recheck.json).

Together with the first cohort, this is **180 scored live benchmark trials**, plus
nine exploratory pilot trials. Complete transcripts and workspaces remain in the
ignored local results directories; published metrics contain no authentication data.
