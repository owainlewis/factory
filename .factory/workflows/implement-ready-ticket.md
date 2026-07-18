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
latest default branch. Implement the complete acceptance criteria with the
smallest coherent change. Run the repository's required formatting, lint,
build, test, and acceptance checks. Do not leave placeholders.

Commit intentionally, push, and open one linked draft pull request. Include
the ticket, summary, acceptance proof, checks, and anything not verified.
Watch GitHub CI and automated review. Fix valid in-scope failures and feedback,
rerun affected checks, push, and repeat until the draft pull request is green
or a human decision is required.

When green, apply `factory:needs-review` and post the pull request link and
verification evidence on the ticket. Leave the pull request for a human to
review and merge. Never merge it and never enable automatic merge.
