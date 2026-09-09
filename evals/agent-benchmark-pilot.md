# First repair-stage pilot

Run on 8 September 2026 using Codex CLI 0.153.4, SDK 0.147.0, and Python 3.11.11.
Every participating maker and reviewer used `gpt-6-astra` with medium reasoning effort.
One matched pair per case, seed 1, 600-second trial limit. See the
[benchmark protocol](agent-benchmark.md) for the boundaries and commands.

All six trials passed all 15 final acceptance checks. Each of the four changed-code
trials used one native review agent. Usage accounting was complete for all six trials.
The clean scripted trial made no agent call.

| Case | Arm | Total tokens | Uncached input | Output | Seconds |
| --- | --- | ---: | ---: | ---: | ---: |
| repair | scripted | 223,669 | 34,871 | 894 | 56.7 |
| repair | prompted | 216,721 | 36,400 | 993 | 58.1 |
| review-only | prompted | 266,017 | 50,203 | 1,286 | 59.3 |
| review-only | scripted | 218,543 | 35,789 | 994 | 50.5 |
| clean | scripted | 0 | 0 | 0 | 0.0 |
| clean | prompted | 39,683 | 20,138 | 217 | 15.4 |

Total tokens include cached input. Uncached input and output are shown to avoid treating
a token total as a bill. No prices or dollar savings were calculated.

The scripted arm used 3.2% more total tokens on the CI repair and 17.8% fewer on the
review-only repair. It used fewer uncached input and output tokens in both. These are
single observations, not evidence of a stable cost advantage. The clean case demonstrates
that Python can skip an agent call entirely when the supplied state needs no judgment.
Do not average these cases into a general savings claim.

This pilot tests the wiring and graders. It does not establish the performance of full
issue-to-PR delivery, larger repositories, repeated repair passes, or interactive human
chat. The useful next experiment is repeated matched runs on real failures, changing one
prompt or context-reuse decision at a time.

An earlier instrumentation attempt completed three agent trials with unreadable usage
records, then was stopped during the fourth. Those trials are excluded because the SDK
could not decode a newer CLI review event. The adapter and a checker-path instruction
were corrected before rerunning all cases. The invalid attempt was retained locally;
no per-trial result was selected for being cheaper.

Source provenance for the measured pilot:

- `agent_sha256`: `3edb86f9a2fd6986886c09919eabc2b0218a7a307d068a543b8153b57b0a539d`
- `harness_sha256`: `1ac97d5f61a7c6d779eadeec06dd008fa21e696cb39409d71447f54d65591762`
- `fixture_sha256`: `b18a5bc9d7dae33aaa53262b0a3c0a297b131890307436f0d1af257bd0c323b4`
- `checker_sha256`: `aa43158a4095d65ace65a8e3b4f0ec9289235d8f24de2701c7870544f076d451`

The final harness adds report validity and worker-failure handling after this pilot;
the agent prompts, fixtures, and measured execution path are unchanged from the corrected
pilot. These numbers describe that recorded run, not a second measurement of the final
reporting code.
