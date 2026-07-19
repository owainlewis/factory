# Factory operation

`factory run` validates `gh` and Codex subscription authentication, evaluates
scheduled workflows, polls all configured repositories, and dispatches durable
scheduled and ready-ticket tasks. Global and
per-repository concurrency are controlled by `max_concurrent_runs` and
`max_concurrent_runs_per_repository`. The per-repository value defaults to 1.

In continuous mode, Factory writes concise lifecycle events to standard error.
It reports startup validation, polls that queue work, claimed tasks, runtime
delegation, and terminal run outcomes. Runtime delegation includes the initial
working directory and marks worktree creation as workflow-managed. The bundled
ready-ticket workflow requires Codex to create or reuse an isolated
ticket-numbered worktree; Factory does not create that worktree before starting
Codex.

Factory requires the conventional `factory:ready` and
`factory:needs-review` labels. The explicit `factory init` setup command creates
missing label definitions without changing existing ones. The daemon does not
create, remove, or otherwise mutate labels itself. The delegated workflow owns
ticket and pull-request updates.

Five-field cron schedules are evaluated in the IANA timezone declared by the
workflow. Factory stores the next occurrence and atomically advances that
cursor when it creates the durable task, so repeated ticks, restarts, and
multiple daemon loops cannot duplicate one UTC scheduled instant. Startup moves
an overdue cursor to the next future occurrence instead of replaying work missed
while Factory was offline. Disabled workflows are not evaluated. Invalid or
failing scheduled workflows are reported and isolated from ready-ticket
polling. A scheduled prompt receives its UTC occurrence, repository path,
inspected commit, and previous successful run time when available. The agent may
use its authenticated `gh` CLI to create or update tickets; Factory does not
hard-code those effects.

Every active run records the Factory owner, a durable supervisor anchor that
owns the Codex process group, the anchor's process-start identity, Codex session
ID as soon as it is observed, working
directory, pull-request URL when safely recognized, start time, and latest
structural runtime activity. Raw event text is not stored. A workflow timeout is
the maximum deadline for one execution.
There is no short fixed idle timeout, so an active agent can keep working until
that configured deadline. Explicit cancellation and deadline expiry terminate
the complete Codex process group.

At startup and periodically while running, Factory checks every database run
still marked `running`. It leaves a run alone when its owning daemon lease and
process are live. The supervisor anchor remains the group leader if Codex exits
while descendants are still running. Otherwise Factory verifies the anchor's
recorded process-start identity, stops the matching orphan process group, closes
the interrupted run, and queues one recovery attempt. Recovery first resumes
the stored Codex session. If that
session cannot be resumed, Factory starts one fresh fallback within the same
execution deadline. The recovery prompt includes the current ticket,
repository, Git worktree and branch inventory, pull-request URL when found, and
bounded previous evidence, and tells Codex to inspect current reality before continuing.
Factory permits at most two durable recovery attempts. Repeated failure leaves
the task failed and inspectable. Terminal runs are never recovered.

On Ctrl-C, Factory stops polling and claiming immediately, cancels active
Codex process trees, waits for the workers to record `cancelled`, and exits.
Queued tasks remain durable for the next start. Failed and cancelled runs keep
their bounded output, error, session, ticket, branch, and pull-request context
for inspection. Factory never merges software pull requests.

`factory run --once` performs one discovery poll and exits without claiming or
launching tasks. It is intended for setup checks and safe polling smoke tests.
