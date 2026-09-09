# Experiment 2 results: 24 local repair trials

Run on 9 September 2026. All 24 trials completed correctly with complete usage records.
The current script showed a small time advantage over the prompted baseline. Retaining
maker context did not meet the predefined adoption rule. Production `agent.py` is unchanged.

The [protocol](agent-benchmark-experiment-2.md) was written before the run. Two cases,
three arms, four matched repetitions, serial execution, randomised order with seed 29.
No prompts or source changed during the batch, and no trial was rerun or excluded.
The trial processes took 37.7 minutes in total; this excludes developing and auditing
the benchmark. No further model trials were run after the authorised 24.

## What was tested

- **Current script:** Python owns the production repair loop; each repair starts a fresh maker.
- **Prompted baseline:** one autonomous agent conversation owns the equivalent loop.
- **Script with context reuse:** the same Python loop and repair prompt retain the maker
  conversation between calls. Native reviewers remain fresh.

The single repair fixes an inclusive range endpoint bug in a small port parser. The
follow-up case first repairs that bug, then receives a new requirement to accept whitespace
around range endpoints. This tests continued work after new feedback, not recovery from
an incorrect first fix. All 12 follow-up trials actually used two repair cycles.

All makers and reviewers used `gpt-6-astra`, medium reasoning. Codex CLI 0.153.4,
`openai-codex` 0.147.0, Python 3.11.11. The benchmark used the locally installed CLI.
Each trial had a 600-second limit. There was no simulated CI delay.

## Results

Each cell is the median of four trials. Time is parent-measured wall time. Total tokens
include cached input and native reviewers. Neither total tokens nor uncached input alone
is a dollar cost estimate.

| Case | Arm | Wall seconds | Total tokens | Uncached input | Output |
| --- | --- | ---: | ---: | ---: | ---: |
| Single repair | Current script | 54.8 | 242,380.5 | 35,981.5 | 914.5 |
| Single repair | Prompted | 60.0 | 224,767.5 | 43,761.0 | 1,192.5 |
| Single repair | Script with reuse | 63.5 | 258,976.0 | 35,828.5 | 1,131.5 |
| Follow-up | Current script | 121.6 | 492,942.5 | 72,199.0 | 2,134.5 |
| Follow-up | Prompted | 133.4 | 490,785.5 | 48,563.0 | 2,493.0 |
| Follow-up | Script with reuse | 122.9 | 581,962.5 | 53,973.5 | 2,390.0 |

Compare the ratio within each matched repetition before taking the median. This is
different from dividing the medians above. A ratio below 1 favours the first arm.

| Case | Comparison | Wall ratio, median [range] | Faster pairs | Total-token ratio, median [range] |
| --- | --- | --- | ---: | --- |
| Single repair | Current / prompted | 0.890 [0.774, 1.267] | 3/4 | 1.037 [0.996, 1.223] |
| Follow-up | Current / prompted | 0.918 [0.854, 0.992] | 4/4 | 1.004 [0.897, 1.051] |
| Single repair | Reuse / current | 1.095 [0.863, 1.460] | 1/4 | 1.062 [1.004, 1.110] |
| Follow-up | Reuse / current | 0.995 [0.939, 1.280] | 2/4 | 1.181 [1.019, 1.284] |

The current script was about 11% faster on the single repair and 8% faster on follow-up
by median paired time. Total-token use was about 4% higher and essentially equal,
respectively. On follow-up it used about 49% more uncached input than the prompted
baseline by median paired ratio, while producing about 15% fewer output tokens. That
tradeoff prevents a simple cost-saving claim.

Retaining context reduced follow-up uncached input by about 25% relative to the current
script, but increased total tokens by about 18% and produced nearly the same paired
wall time. It used more total tokens than the current script in every matched pair.

### Correctness and effort

| Measure | Outcome across all arms |
| --- | --- |
| Correct completion | 24/24 |
| Complete usage accounting | 24/24 |
| First observed repair passed current requirements | 24/24 |
| False completion | 0 |
| Regressions at feedback observations | 0 |
| Observed repair cycles | One in every single repair; two in every follow-up |
| Native reviewers | One per single repair; two per follow-up; 36 total |
| Final source variants | Exactly one per case, identical across all arms and repetitions |

An attempt is a changed candidate observed at a feedback boundary, not every edit or
test command. The second follow-up cycle addresses a newly supplied requirement and
does not count as a failed first attempt. False completion means a completed report
despite failing independent acceptance checks; it is not a general honesty measure.

The final grader passed 15 checks per single repair and 18 per follow-up. A post-run
read of native transcripts confirmed all 36 reviewer sessions completed, contained
substantive no-finding reviews of the relevant changes, and had no recorded file edits.
Some reviewers ran extra checks; others inspected the code and used the maker's reported
test evidence. Participation does not establish reviewer accuracy. None of these cases
required a reviewer to catch a new mistake.

## Decision

**Opinion [high]: keep production behavior unchanged.** The retained-context candidate
failed the rule written before running. Its follow-up median paired time improved by
only 0.5%, below the required 15%, and only two of four pairs were faster. In confirmation
repetition 3 it took 154.4 seconds versus 120.6 for the current script. Repetition 4 favoured reuse;
the two confirmation observations therefore did not both favour reuse. The single-repair median wall time and
per-case median output increases also exceeded their limits.

This changes if a new fixed candidate meets the quality and performance thresholds on
representative, previously unused tasks. Do not tune against these 24 results and call
the same fixtures an independent confirmation set.

**Opinion [medium]: these results suggest a modest local time benefit from scripted
control, but do not establish better coding, lower cost, or a superior software factory.**
Four pairs per case on one small parser cannot support those broader claims. The
single-call reuse control also varied substantially despite similar mechanics, showing
that individual timing differences are noisy. This changes if a wider set of tasks
repeats the time benefit while preserving quality and controlling cost.

## What to measure next

Keep correctness, first-pass success, regressions, repair cycles, false completion,
wall time, and separate token categories. On harder tasks, also record unresolved
valid findings, unnecessary changes caused by invalid feedback, and human intervention
minutes. Those outcomes were not exercised or measured here.

A post-run diagnostic counted increases in cumulative native usage counters as model
responses. This was not a predefined primary metric. The median totals were 11, 10,
and 11.5 responses for current, prompted, and reuse on the single repair; 22, 21, and
23 on follow-up. Context reuse did not remove model responses in this sample. The
first model request already contained a median 19,794 input tokens for makers and
24,857 for reviewers. Those include supplied context and native instructions, not
just the short task prompt. Token costs need to be traced through the whole session.

**Next experiment, not yet run:** keep the current script as the reference and test one
change: provide a compact, code-generated repair brief with the relevant source and
current check results. Let the agent inspect more when needed. The hypothesis is fewer
discovery/tool round trips without sacrificing judgment; these traces do not prove it
will work. Use held-out repository tasks including a valid CI failure, stale or incorrect
review feedback, and a tempting fix that would break an existing requirement. Validate
each grader against known wrong patches before spending model runs.

Initial implementation, PR creation, real CI/reviewer delays, interactive human chat,
and workflow maintenance costs remain outside this local repair experiment. A later
end-to-end comparison is needed for claims about the complete ticket-to-PR flow.

## Evidence and reproducibility

The checked-in [trial data](agent-benchmark-experiment-2.csv) contain all 24 observations
in execution order, token categories, timing, quality metrics, and final source hashes.
A blank `follow_up_delivered` cell means the single-repair case has no follow-up requirement.
Model-response and command counts are post-run diagnostics. The local run directory
`.machinist/benchmarks/context-reuse-24/` retains the manifest, prompts, workspaces,
feedback history, logs, result JSON, report, and response profile. Native session records
remain local. No GitHub issues, PRs, or worktrees were created by the measured trials.

A post-run read confirmed every recorded final token counter matched its native session
counter. Retained maker counters were counted once per conversation, alongside all
native descendants. All 28 maker conversations and 36 reviewers used matching settings.
No further inference was required for the transcript audit.

The protocol file was last modified at 13:09:52 UTC; the manifest was created at
13:12:27 UTC and the first trial inputs at 13:12:28 UTC. The numerical thresholds
were in place before the first measured trial.

Source hashes recorded before measurement:

- `agent_sha256`: `3edb86f9a2fd6986886c09919eabc2b0218a7a307d068a543b8153b57b0a539d`
- `harness_sha256`: `38250ba3b88ff9c2dfd86e88e89e9ece4b573911809d0e2e8fced7985f60837e`
- `fixture_sha256`: `b18a5bc9d7dae33aaa53262b0a3c0a297b131890307436f0d1af257bd0c323b4`
- `checker_sha256`: `18121831d12de624f8e264beff1e3f5e1b810dd4293888606b3c89eb48d25336`

After the run, the report's per-trial time display and progress line were aligned with
the already-recorded parent wall time. Prompts, worker behavior, graders, and source
measurements were unchanged. CSV values come from the original result files.
