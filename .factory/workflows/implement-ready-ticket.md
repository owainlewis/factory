+++
label = "factory:ready"
runtime = "codex"
timeout = "4h"
+++

# Take the ready ticket to a green draft pull request

Reread the current GitHub issue and discussion before acting. Remove
`factory:ready` and post a concise claim update. If the requirements are
unclear or need a human decision, apply `factory:needs-review`, ask focused
questions, and stop cleanly.

Create or reuse one isolated ticket-numbered branch and worktree from the
latest default branch. Translate each acceptance criterion into an
implementation task and a verification check. Implement the complete
acceptance criteria with the smallest coherent change. Do not leave
placeholders.

Add or update automated tests when existing coverage does not prove the
acceptance criteria or protect changed behavior from regression. Prefer the
narrowest useful test level. Record any criterion that cannot be automated
and verify it manually.

Run the repository's required formatting, lint, type, build, test, and
acceptance checks. For UI changes, start the application and verify the
affected workflow in a real browser. Exercise the relevant states,
interactions, error paths, and responsive layouts. Capture concise browser
verification evidence for the pull request.

After implementation and local checks, ask a subagent to independently review
the diff, acceptance-criteria coverage, test quality, likely regressions, and
browser evidence when applicable. Give it the issue requirements and changed
files without prescribing conclusions. Ask it to run the relevant targeted
checks and, for UI changes, independently exercise the affected workflow in a
real browser when practical. Fix valid findings, rerun affected checks, and
request another review when the fixes materially change the implementation.
Do not proceed with unresolved high-confidence findings.

Commit intentionally, push, and open one linked draft pull request. Include
the ticket, summary, acceptance proof mapped to each criterion, tests added or
updated, browser verification when applicable, checks, subagent review
findings, and anything not verified. Watch GitHub CI and automated review. Fix
valid in-scope failures and feedback, rerun affected checks, push, and repeat
until the draft pull request is green or a human decision is required.

When green, apply `factory:needs-review` and post the pull request link and
verification evidence on the ticket. Leave the pull request for a human to
review and merge. Never merge it and never enable automatic merge.
