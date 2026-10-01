# First live evaluation — 25 September 2026

**Factory did not improve the original automated acceptance score, and it took
more time and tokens. But its reviews exposed real gaps in that acceptance
suite. A separate follow-up audit found some quality benefits, with mixed
consistency. This does not establish broad superiority over ordinary prompting.**

## Frozen experiment

Twelve small, synthetic, single-file Python tasks, repeated three times per mode.
The same task text, visible tests, model (`claude-sonnet-5`), tools, and 300-second
whole-trial budget were used. Concurrency was three. The 108 SDK trials were
randomized together; the 36 direct CLI trials ran afterward in randomized order.
Both cohorts passed their evaluator-integrity checks. No mid-task human help was
provided. No human-guided development-time comparison was performed.

| Mode | Original acceptance | Median seconds | Total SDK list-price estimate |
| --- | --- | --- | --- |
| Task-only SDK prompt | 36/36 | 29.3 | $2.47 |
| Reusable workflow prompt | 36/36 | 30.8 | $2.87 |
| Factory: tests + independent review + repair | 36/36 | 61.8 | $6.16 |
| Direct `claude -p`, default system prompt | 36/36 | 26.6 | $3.52 |

All modes passed all three repetitions of every case under the original tests.
There were no original false-completion signals or execution failures. All
completed-call usage records were available. Factory's median latency was about
2.1× the task-only SDK baseline, and its list-price estimate was about 2.5× higher.
These estimates are **not subscription charges**. Cached tokens, cohort timing,
and shared provider load can affect comparisons. Direct CLI uses its normal
system prompt; the SDK modes use the production runner's SDK configuration.

Factory required repairs in 6/36 trials and received eight retry findings across
those trials. Its 90th-percentile latency was 148.9 seconds versus 43.9 seconds
for task-only SDK prompting. This is a meaningful tail-latency cost, even though
all trials completed within their allowance.

## Why the initial score was insufficient

The evaluator checked that every starting implementation failed and every
reference implementation passed. That was necessary but insufficient: three
reference implementations shared untested defects with agent outputs.

Review transcripts pointed to missing checks for:

- Valid U+2028/U+2029 characters inside JSON strings, which `splitlines()` treats
  as line boundaries even though the record is on one physical JSONL line.
- Strict `YYYY-MM-DD` parsing: `date.fromisoformat()` also accepts basic and week
  date forms, which did not satisfy the stated format.
- Atomic writes preserving existing file modes and default new-file permissions.
  Replacing a destination with a `mkstemp` file otherwise changes its mode to 0600.

We tested these properties against **all stored outputs for the affected cases**,
not just Factory's outputs. The original references failed these probes too.
Original scores above remain unchanged; this is an exploratory follow-up audit.

| Case | Task-only SDK | Workflow prompt | Factory | Direct CLI |
| --- | --- | --- | --- | --- |
| Atomic writes, including file modes | 0/3 | 0/3 | 3/3 | 0/3 |
| Strict due-date formats | 3/3 | 2/3 | 1/3 | 1/3 |
| JSONL Unicode handling | 0/3 | 0/3 | 1/3 | 0/3 |
| **Affected-case total** | **3/9** | **2/9** | **5/9** | **1/9** |

Factory's review delivered a concrete benefit on file modes, but it did not
consistently catch the date or JSONL issues. Task-only prompting actually did
better on date formatting in this sample. Other review requests concerned
symlink behavior and non-list pagination inputs; these need clearer scope and
were not automatically counted as defects or incorporated into this audit.

These cases were selected after inspecting Factory review findings. The audit
therefore has selection bias and is **not a confirmatory estimate of Factory's
overall quality advantage**. Three repetitions of three tasks are also too few
to support broad reliability claims. A targeted fresh cohort with these probes
frozen in advance is the next check; more realistic repository and app tasks
are still needed to assess generalization.

## Practical assessment

The workflow buys enforceable checks, preserved evidence, bounded stopping, and
a chance to catch defects beyond existing tests. It does not make the checks
complete or the reviewer consistently right. For simple, clear tasks, the
original tests showed added overhead without an acceptance gain. The audit
shows why that should not be mistaken for proof of equal code quality.

Keep deterministic checks and bounded repairs. Treat separate review as a
measured trade-off rather than an automatic quality guarantee. Improve the
acceptance suite from concrete failures, preserve old results, then evaluate
changes on fresh runs. Do not optimize prompts against a weak score or silently
rewrite the score after seeing outcomes. Human maintainability review and active
human minutes remain unmeasured; neither is inferred from these tests.

## Evidence and reproduction

- [All 144 per-trial metrics](runs.json), including token categories and resolved models.
- [Original SDK manifest](comparison-v1-manifest.json) and [integrity result](comparison-v1-integrity.json).
- [Direct CLI manifest](cli-v1-manifest.json), [integrity result](cli-v1-integrity.json), and [exact adapter used](original-cli-adapter.py.txt).
- [Follow-up audit results](posthoc-results.json) and [review feedback](review-feedback.json).
- Audit test sources and original reference sources are preserved alongside this report.

The SDK cohort ran the harness at revision `1e0efe8`; the later root README-only
commit did not alter evaluated code. Manifests contain exact source/prompt/case
hashes. Live workspaces and complete transcripts remain in ignored local
`evals/results/comparison-v1` and `evals/results/cli-v1` directories.

A nine-trial exploratory pilot preceded these cohorts. All modes failed a
nested-copy check under an ambiguous "independent copy" requirement; the wording
was clarified before the frozen experiment. Pilot scores are not pooled here.
The current suite adds the missing audit checks and fixes the references, so a
new run of the current tree evaluates a stronger suite than this original table.

The [fresh targeted follow-up](../2026-09-25-v2/README.md) is now complete. A separate
[status-value audit](status-audit-results.json) also found a shared exception-type
bug in all 12 original optimistic-update outputs. Neither later finding rewrites
the frozen original score table. [Qualitative source-review notes](qualitative-review.json)
are model observations with workflow labels withheld during inspection, not human
ratings or a statistically validated maintainability comparison.
