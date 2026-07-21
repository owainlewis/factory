# Factory operation

`factory daemon` discovers the enclosing Git root, loads
`.factory/config.toml`, validates `gh` and Codex subscription authentication,
evaluates scheduled workflows, polls that repository, and dispatches durable
scheduled and ready-ticket tasks. `max_concurrent_runs` controls concurrency.
Each clone has its own SQLite database and worktree directory outside the
checkout.

In continuous mode, Factory writes concise lifecycle events to standard error.
It reports startup validation, polls that queue work, claimed tasks, runtime
delegation, and terminal run outcomes. Runtime delegation includes the initial
working directory and marks the worktree as Factory-owned. Before Codex starts,
Factory queries GitHub's default branch, fetches its exact commit, reserves a
stable `factory/<issue>-<slug>` branch and worktree, records them durably, and
launches Codex there. The canonical checkout is never used for agent changes.

The ready label is a wake signal, not authority. `factory approve ISSUE_NUMBER`
binds the current title, body, delivery workflow hash, stable trusted GitHub
user ID, and a fresh ready-label event into a versioned approval artifact. The
daemon re-fetches and validates that evidence immediately before runtime
delegation, atomically consumes it, removes the label, and posts a claim record.
Changed, malformed, untrusted, or replayed evidence fails closed without
starting Codex. Comments and attachments are never copied into the execution
prompt. `factory workflow create --label LABEL` creates a missing label without
changing an existing definition. `factory init` does not inspect or mutate
GitHub labels.

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
for inspection. Scheduled proposal workspaces are detached and removed at a
terminal outcome. Failed, cancelled, dirty, or unpublished delivery worktrees
are retained, up to ten. Preview cleanup with `factory cleanup RUN_ID`; removal
requires `factory cleanup RUN_ID --confirm` and preserves the local branch.
Factory never merges software pull requests.

`factory run --once` evaluates one schedule tick, performs one discovery poll,
persists eligible tasks, and exits without claiming or launching them. It is
intended for setup checks and safe polling smoke tests. An empty evaluation
does not invoke Codex.

## Prove an idle poll

When no configured Project item is ready and no scheduled workflow is due,
capture the task list before and after one poll and list all Factory-managed
containers:

```sh
factory tasks --json
factory run --once
factory tasks --json
docker ps --all --filter label=dev.factory.managed=true
```

The two task listings should show zero new tasks, and the Docker listing should
show zero Factory containers. This proves the empty poll persisted and launched
nothing.

Before first repository-local startup, Factory reads the old
`~/.factory/factory.sqlite3` database without modifying it. If that database
contains queued or running work for this repository, startup stops with
instructions to stop the old daemon and finish or cancel the work. Terminal
legacy history is not imported.
