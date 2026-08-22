# Software pipelines

> **Status:** Proposed for review

## 1. Executive summary

Factory runs one saved prompt across one or more repositories. That is the
right cheap path when one capable agent can plan, build, and test inside one
context window. It is insufficient when an operator deliberately wants
separate planning, implementation, verification, pull-request, and feedback
runs with different prompts, runtimes, budgets, and failure boundaries.

This design turns a Task into a **Pipeline** containing one or more ordered
**Stages**. A one-Stage Pipeline is the current one-agent loop with no extra
handoff cost. Adding Stages opts into more agent starts, context resets, token
spend, checkpoints, and control. Agent Stages run coding agents. Action Stages
perform bounded deterministic work such as opening a pull request. Gate Stages
wait without consuming an agent slot, for example until a pull-request review
has either approved the change or requested feedback.

One Pipeline Run creates one repository **Track** per selected repository. Each
Track owns one logical working branch and advances through the frozen Stage
order. A successful code-producing Stage records an accepted commit. The next
Stage starts from that exact commit in a fresh Attempt worktree on the same
Worker, so several agent runs continue one branch without sharing a dirty
directory.

V1 keeps every multi-stage Track on one persistent Worker. This proves
sequencing and code lineage without requiring general cross-Worker Git handoff.
An explicit pull-request Action may publish the Track branch to its configured
origin and record the pull request. Publication is a visible Stage boundary,
not a hidden side effect of every agent run.

The browser has two related surfaces. The Pipeline editor is a polished linear
graph that begins with one large Stage card, supports inserting and reordering
Stages, and shows guaranteed and conditional agent starts and token ceilings
before a Run. The
software-work view groups active Runs and shows their live Stage graph,
repository Tracks, blocked work, current agent activity, cost, branch, pull
request, and recent outcomes. Slate and Agent OS remain the place for general
human and business task tracking. Factory owns repository work, agent
execution, code lineage, and delivery evidence.

The main downside is cost and availability. Separate Agent Stages deliberately
repeat agent startup and context, so a Plan, Build, and Test Pipeline can use
far more tokens than one Agent Stage. If a Track's Worker or local repository
state is unavailable, later Stages wait rather than moving to another Worker.
The editor must make both costs explicit instead of presenting more Stages as
automatically better.

## 2. Context and scope

The [current architecture](../../ARCHITECTURE.md) uses Task, Run, Session, and
Attempt as its execution lifecycle, supported by Workers and repositories. A
Task stores one prompt, runtime, repository set, and optional schedule. A Run
freezes the Task and creates one Session per repository. Sessions do not depend
on each other and every Attempt starts in a fresh worktree from the repository
base branch.

This is sufficient for fleet operations and for work that fits one agent loop.
It is insufficient for one software outcome that needs several independent
agents or provider actions in a known order. Starting five Tasks manually does
not record the dependency, preserve one code lineage, prevent an early review,
or explain which Stage needs attention.

Traditional CI systems provide a useful interaction model. CircleCI workflows
make dependencies visible, keep downstream jobs on hold until requirements are
met, move data through a workflow workspace, and let an operator rerun from a
failure. Its usage views break spend down by workflow and job. Factory adapts
the visible graph, explicit dependencies, gates, and per-Stage cost. It does
not copy CircleCI's YAML-first authoring, arbitrary DAGs, or container-artifact
model. Factory's shared workspace is a Git branch and accepted commit lineage.
See [CircleCI workflow orchestration](https://circleci.com/docs/guides/orchestrate/workflows/)
and [project usage](https://circleci.com/docs/guides/insights/project-usage-dashboard/).

This design changes the authoring model, Run lifecycle, Worker worktree input,
provider-action boundary, and primary browser surface. It preserves the current
control-plane authority, leases, Attempt supervision, per-repository isolation,
local operator boundary, remote Worker authentication, repository cache, and
runtime adapters.

V1 Pipelines are linear. Every Stage after the first has exactly one
predecessor. Agent, Action, and Gate Stage types cover the first useful delivery
flows without a general expression language. Each repository advances
independently, so a failure in one Track does not stop healthy Tracks. A
Pipeline may contain one Agent Stage, which is the exact replacement for a
current Task.

## 3. System context

```text
Operator browser
  Pipeline graph editor, cost preview, and live software-work graph
                 |
                 | local HTTP and JSON
                 v
Factory control plane
  Pipeline versions, Run and Track state, Stage dependencies and gates,
  Session admission, Worker affinity, provider metadata, SQLite
                 ^
                 | claim, lease, events, prepared input, completion,
                 | branch publication and provider actions
                 |
Persistent Worker
  repository cache, Track branch, and accepted commit lineage
  fresh Attempt worktree per Stage or retry
  Pi, Codex, Claude Code, Git, and authenticated gh
```

The control plane owns desired order, Gate evaluation state, bounded provider
metadata, and durable state. The Track owner Worker owns repository objects,
the generated working branch, local checkpoint refs, Attempt worktrees, agent
processes, and origin credentials. The control plane records full commit IDs
and pull-request identity but never receives repository contents or Git
credentials.

## 4. Proposed design

### How it works

An operator opens the Pipeline editor. It starts with one Agent Stage, not a
wizard asking them to choose between a Task and a Pipeline. For a small change
they name that Stage `Build`, enter one prompt, and run it. The estimate reads
`1 agent start per repository`. Admission and execution then match the current
one-Stage Task path.

For a controlled delivery the operator adds Stages between visible connectors:

```text
Plan       Build       Test       Open PR    PR review    Address feedback    Update PR
Agent   -> Agent   -> Agent   -> Action  -> Gate     -?-> Agent           -> Action
40k        120k        50k        0 tokens   0 tokens     80k max             0 tokens
```

This Pipeline has three guaranteed agent starts and one conditional maximum.
Plan, Build, Test, and, when needed, Address feedback each get a fresh context.
Open PR deterministically publishes the accepted branch. PR review waits
without holding a Worker execution slot until the review is approved or has
bounded actionable feedback. Address feedback runs only for the latter outcome,
receives the frozen feedback snapshot, and continues from the same accepted
branch head. Update PR publishes only that accepted output. Approval skips both
conditional Stages successfully. If the operator instead asks one
Build agent to plan, implement, and test, Factory runs one agent loop and pays
none of the intermediate context-reset cost.

Admission freezes the complete Pipeline generation and repository identities.
It creates one Run, one Track per repository, and one Stage record per Track
and frozen Stage. Immediate Agent Stages enter routing when their predecessor
succeeds. Later Agent and Action Stages start in `waiting`. Gate Stages enter
`waiting` until their typed condition is satisfied. Tracks may use different
owner Workers and run concurrently within Pipeline and Stage limits.

First-Agent routing considers every frozen Agent and Worker-executed Action in
the Pipeline, not only Stage 1. An owner candidate must be eligible for the
repository, every required runtime and execution profile, and any required
`git` or `gh` capability. Admission fails before creating a manual Run when any
repository has no candidate with the complete capability intersection.

The first Worker receives the generated Track branch name and prepares an
Attempt worktree from the repository base commit. Its prepared call atomically
freezes the Track owner Worker, base commit, and Stage input before agent
startup. Worker affinity starts at that point. A failed preparation before the
call commits may release capacity and route to another eligible Worker; no
later operation changes the owner or branch name.

When the Implement agent exits successfully, the Worker stages modifications
and deletions to tracked files. It never automatically adds an untracked file.
A new file enters the Stage output only when the agent explicitly staged or
committed it. Any remaining unignored, untracked file fails completion with a
bounded reason. The Worker disables hooks for its own commit, verifies the
output descends from the frozen input in a fixed verification environment, and
creates an Attempt-scoped local ref to keep the commit reachable. Commit and
ancestry verification use a validated temporary object snapshot. Under the
repository mutation lock, the Worker freezes a manifest of canonical loose
objects and regular pack and index files, opens every source without following
symlinks, and copies it to a private store. It does not wait for unrelated
agents in other worktrees. Worker fetch, repack, ref, and cleanup operations use
the same lock. Before copying, the Worker sums manifest files and bytes and
atomically reserves temporary capacity. V1 permits at most 1,000,000 files,
8 GiB, and 10 minutes per snapshot, three race retries, and 16 GiB of concurrent
snapshot reservations per Worker. Insufficient free space or a limit breach
fails actionably with `object_snapshot_too_large` before agent output is
accepted. Source identity and size are read back after each copy. A changed or
vanished source fails this snapshot attempt with `object_snapshot_raced` and
bounded retry; it cannot produce a partial accepted store. The snapshot copies
no `objects/info` metadata, including local or HTTP alternates. A fresh
temporary Git directory uses only that snapshot. It ignores replacement refs,
grafts, hooks, config includes, system and user config, and agent-written
repository config. Before ancestry evaluation, it verifies every pack and index
checksum and runs strict object-integrity and connectivity checks. It enumerates
the complete object closure reachable from the input and output commits,
rehashes each canonical `type length\0content` byte sequence with the
object format frozen in trusted Worker cache metadata before agent launch, not
from agent-writable Git configuration, and requires the result to equal its
requested object ID. The Worker then creates and reads back the exact ref with
direct ref operations under the same fixed config. It proves both IDs name
cryptographically valid commit objects available without an alternate, output
descends from input, the complete successor checkout is present, and the ref
resolves to output without executing agent-controlled behavior.

Snapshot directories and reservations have owner-only manifests. They are
released after verification whether completion is accepted or rejected.
Startup recovery removes an incomplete snapshot only when no live completion
owns its manifest, then releases the recorded reservation. The Attempt worktree
and local ref follow their separate retention rules and are never deleted by
snapshot cleanup.

Only after the control plane accepts the full output commit under the active
lease does it mark Implement succeeded and make Test eligible. Test is
claimable only by the Track owner Worker. That Worker creates a fresh Attempt
worktree at the exact Implement commit. Test never reuses Implement's worktree,
so failed or dirty work remains inspectable without contaminating its input.
The same handoff occurs at every Agent boundary. The Track's logical branch
head advances only to an accepted output commit. Attempt worktrees and local
checkpoint refs are implementation details beneath that one branch lineage.

An Open PR Action runs on the Track owner after a code-producing predecessor.
It first obtains a durable publication authorization ordered against Run and
Track cancellation. That transaction freezes the candidate and expected remote
head, provider cursor baselines, and an idempotency key. Only then may the
Worker fast-forward the generated Track branch and create or find the pull
request through authenticated `gh`. The Stage records provider, repository,
pull request number, URL, remote branch, and published commit. Replaying uses
the same authorization and identity. It never force-pushes, merges, or changes
the base branch. The pull request targets the Track's frozen base branch. Its title
and body come from bounded templates frozen in the Pipeline generation, with
canonical defaults using Pipeline, Run, Track, and repository names. Provider
calls are noninteractive.

An Update PR Action is valid only after an Agent in a conditional review block.
It uses the same write-ahead authorization, publishes that Agent's accepted
commit with an expected-old-head check, and records the new published commit.
Agent success only promotes Update PR, never
a later review or completion. Update PR success is the durable publication
boundary that may advance the Track. Response loss is replayed by verifying
that the recorded pull request branch equals the candidate commit. A different
remote head fails the Action with `remote_diverged`; Factory never moves a
succeeded Agent Session backward.

A PR review Gate is enabled only after an Open PR or Update PR Action. It does
not hold a Worker slot. The owner Worker receives a bounded, non-execution Gate
check lease on its normal outbound poll, performs a rate-limited `gh` check,
and returns the result under that lease. The Gate records one of two typed
outcomes from the newest qualifying provider event. `feedback_requested` means
that event is a changes-requested review, review comment, or top-level
conversation comment. `approved` means it is an explicit approved review
submission for the exact target commit. Events from Factory's own account and
provider system events are excluded. The actor must also satisfy the frozen
`repository_write` policy: GitHub must confirm write, maintain, or admin
permission for the target repository. Other public events may be displayed but
cannot choose an outcome or start an agent. Otherwise the Gate remains waiting.
Provider text is untrusted and any feedback snapshot is bounded.

The following conditional block may use the typed condition
`run_if: feedback_requested`. Address feedback receives the frozen snapshot,
starts from the exact published branch head, and makes changes locally. A
required Update PR Action then fast-forwards the same remote branch with its
accepted output. An approved outcome marks every Stage in that contiguous
conditional block skipped as a success-like transition and advances to the
next unconditional Stage or completes the Track. A timeout fails the Gate.
Factory never starts an Agent with an empty invented feedback set.

An Agent Stage that makes no changes records its exact input commit as output
and creates the same Attempt-scoped local ref. Every successful Agent handoff
therefore has one uniform commit proof.

Git is the code and file channel between Stages. A Stage that produces a
specification, report, or other successor input writes it into the repository
and stages or commits new files before success. The bounded agent result remains
a human-readable summary and is not injected into the next Stage prompt. Typed
Gate output, such as a frozen pull-request feedback snapshot, is the only V1
non-Git successor context. The Pipeline editor states this beside every
non-final Stage prompt.

The Pipeline editor uses a graph and inspector layout rather than a long form:

```text
Pipelines / Ship a change                         Save     Run
2 repositories · 3 guaranteed + 1 conditional agent starts/repository
210k guaranteed + 80k conditional token ceiling/repository

  [ Plan ] ── + ── [ Build ] ── + ── [ Test ] ── + ── [ Open PR ]
   Agent          Agent            Agent             Action
   Codex · 40k    Codex · 120k     Pi · 50k          0 tokens

                                     Selected Stage
                                     Name       Test
                                     Type       Agent
                                     Runtime    Pi
                                     Prompt     Review and run...
                                     Max tokens 50,000
                                     Input      Accepted Track head
```

The first visit contains one full-width Agent card and one `Add Stage` button.
Adding a Stage never changes resource type or moves the operator into an
advanced mode. Connectors contain insert buttons, cards drag horizontally,
and keyboard users use Move earlier, Move later, Insert before, Insert after,
and Delete actions. Selecting a card opens one inspector on the right. The
canvas scrolls horizontally on desktop and becomes a vertical sequence at 720
CSS pixels. It is not a free-form canvas, and connections cannot cross.

`Add review round` is an atomic editor operation. It inserts PR review, Address
feedback, and Update PR with one shared visual bracket and valid `run_if`
references. Each card remains separately configurable, but move and delete act
on the complete review-round block. Pointer and keyboard commands announce the
three-Stage change before applying it. The browser may hold an incomplete local
drag preview, but Save and Run remain disabled until the server preview accepts
the resulting grammar.

The estimate is structural and honest, not a model-price promise. It shows
unconditional starts and token ceilings, the conditional maximum, deterministic
Action count, Gate count, and the fact that retries can increase actual usage.
One-Stage Pipelines show a
quiet `Single agent loop` label. Two or more Agent Stages show `Multiple agent
contexts` and explain that each Stage starts with a new context. Historical Run
detail shows actual input, output, cached, and total tokens per Agent Stage and
for the Run when the runtime reports them. Missing telemetry is `Unavailable`,
never zero.

The editor offers examples as copyable starting points, not special execution
modes: `Quick change` has one Build Agent; `Controlled change` has Plan, Build,
and Test Agents; `Pull request delivery` adds Open PR, PR review, and
Address feedback. Once copied, every example is an ordinary Pipeline.

The browser shows one Pipeline Run section with Stage columns and one card per
repository in its current Stage. A card carries the repository, Stage, runtime,
elapsed time, tokens when available, working branch, pull request, current
activity, and inline failure or blocked reason. Selecting it opens Run detail
at that Track and Stage. Completed Tracks remain visible in a quiet final
column for the recent-history window.

```text
Work                                                2 need you
[ Needs you 2 ] [ Active 5 ] [ Recent ]

Ship issue · Run 91c2                         2 repositories
             Implement          Test               Review
factory      succeeded  ───────  running 6m  ─────  waiting
handbook     failed
             New file report.md was not staged.

Dependency review · Run e173                   1 repository
api          succeeded  ───────  succeeded  ──────  review running
```

The visual system follows the useful parts of Agent OS and the DeepSeek Kanban
experiment: a quiet fixed sidebar, one clear page heading, compact filters,
restrained status colour, readable cards, generous empty space, and urgent work
at the left edge. It removes the current metrics wall. Pipeline Run groups
carry hierarchy, Stage headers carry sequence, and repository cards carry live
state. Empty Stages remain visible because `nothing is waiting here` is useful
information. The graph is not a free-form canvas and does not ask an operator
to position nodes.

### Components and responsibilities

The Pipeline service owns Pipeline validation, immutable generations, Stages,
repository scope, schedules, and admission. It depends on repository and
execution-profile readiness. It does not claim work or inspect Git contents.

The Run service owns Tracks, Stage dependencies, Worker affinity, Session
promotion, Gate cursors, aggregate state, cancellation, retry, branch and
commit identity, and bounded provider metadata. It depends on existing routing
and Attempt services. It does not create commits, hold origin credentials, or
decide whether local Git objects exist.

The Worker owns repository preparation, Attempt worktrees, local commit and ref
creation, exact-input verification, runtime execution, branch publication,
bounded `gh` actions, retention, and recovery. It does not choose the next
Stage, interpret provider text as trusted instructions, or change dependency
state.

The browser owns the Pipeline graph editor, structural cost estimate,
software-work graph, filters, and links to Run and Stage detail. It reads server
projections and does not derive dependency, cost, or provider truth from local
state.

### Decisions

**Extend the existing lifecycle instead of adding child Runs.** A Run remains
one immutable invocation and a Session remains one independently retriable
Stage execution for one repository. Agent Sessions have Worker Attempts.
Action Sessions have bounded idempotent action attempts. Gate Sessions wait on
typed durable conditions and hold no execution slot. Sessions gain Stage and
Track identity. A parent Run with child Task Runs would create two
cancellation, retry, history, and aggregate models.

**Make one Stage the simple path.** One Agent Stage uses the current claim,
execution, result, and fake-cloud contracts and does not create an intermediate
commit solely for a nonexistent successor. A separate `quick task` resource or
mode was rejected because it would split history and make adding a second Stage
a migration. The cost is that Pipeline becomes the name even when there is no
visible sequence.

**Rename Task to Pipeline.** The saved operator object becomes a Pipeline, and
a current Task migrates to a Pipeline with one Stage. Keeping both would leave
operators deciding which resource starts software work. Factory is in developer
preview, so the API and UI may make this clean break with a lossless database
migration.

**Ship a linear model first.** Stage order is a list and every Stage has zero
or one predecessor. Agent, Action, and Gate are typed Stage executors, not
arbitrary graph nodes. This proves sequencing, retry, lineage, provider waits,
and the graph UI without inventing merge semantics for two code-producing
predecessors. Stable Stage IDs and explicit predecessors keep a later DAG
migration possible.

**Use a stable logical branch with isolated Attempt worktrees.** Every
multi-stage Track has one generated working branch and one accepted head. Each
Agent Attempt still gets a fresh worktree and private ref, then advances the
logical branch only after acceptance. Reusing one directory was rejected
because failed work would contaminate successors. Creating unrelated branches
per Stage was rejected because it breaks the operator's expectation that Plan,
Build, Test, and feedback address one deliverable.

**Treat provider operations as typed Stages.** Open PR, Update PR, and PR review
are explicit Action and Gate Stages. Hiding publication inside an agent prompt was
rejected because it spends tokens on deterministic work and cannot give Factory
an idempotent provider identity. A generic shell Stage and arbitrary webhook
expression language were rejected for V1 because they weaken validation and
make the editor impossible to explain.

**Show structural cost before execution.** The editor reports guaranteed and
conditional-maximum starts and token ceilings per Stage and repository. It does not
estimate currency when provider pricing or runtime telemetry is absent. Hiding
cost was rejected because a visually attractive multi-stage graph can otherwise
push operators toward unnecessary context resets.

**Advance each repository independently.** A Track is the ordered Sessions for
one repository within a Run. A failed repository does not hold unrelated
repositories at an earlier Stage. Lockstep execution was rejected because one
bad repository would waste fleet capacity and hide partial progress.

**Use Worker affinity for V1 handoff.** A Track freezes one owner Worker before
its first agent starts. Local commit IDs and refs hand work between fresh
Attempt worktrees on that Worker. Remote Git checkpoints were rejected for V1
because secure cross-Worker handoff requires origin-side immutable refs,
separate credentials, runtime isolation, provider-policy verification, and
drift handling. Those systems should be justified by proven demand for
failover, not required to test the Pipeline product.

**Keep automatic commits conservative.** The Worker stages tracked changes but
does not discover and upload untracked files. This avoids silently including
credentials, downloads, or scratch files. The Stage prompt tells the agent to
stage intentional new files.

**Group the graph by Pipeline Run.** Global status columns were rejected
because `running` does not say whether code is being implemented, tested, or
reviewed. Each Run renders its frozen Stage order and repository Tracks.

## 5. Invariants and requirements

### Invariants

- `INV-1`: A Run contains one immutable Pipeline snapshot, including ordered
  Stages, repository identities, execution settings, and source.
- `INV-2`: A Track belongs to exactly one Run and one snapshotted repository.
- `INV-3`: A Session belongs to exactly one Track and one snapshotted Stage.
- `INV-4`: One Track contains at most one Session for a Stage.
- `INV-5`: A non-first Session cannot become routeable until its predecessor
  is dependency-satisfied by success or a success-like conditional skip and
  every required typed output is durably recorded or explicitly passed through.
- `INV-6`: An Agent Session input is the frozen base commit for the first
  code-producing Stage or the Track's exact latest accepted commit.
- `INV-7`: In a multi-stage Pipeline, one Worker owns a Track from the first
  successful prepared call until the Track is terminal. A one-stage Pipeline
  retains its current backend ownership contract.
- `INV-8`: Every multi-stage Agent Attempt starts in a fresh worktree at its
  Session's exact input commit on the Track owner Worker. One-stage fake-cloud
  execution retains its current dispatcher lifecycle.
- `INV-9`: A successful code-producing Agent Session records one full output
  commit that descends from its input and one Attempt-scoped local ref keeping
  it reachable.
- `INV-10`: Failed, cancelled, lost, or timed-out Attempts cannot advance a
  Track or replace its last accepted commit.
- `INV-11`: Retrying a failed Session reuses its frozen Agent input, Action
  authorization, or Gate target and per-feed baselines and cannot change a
  downstream Session that already started.
- `INV-12`: Editing a Pipeline never changes an admitted Run.
- `INV-13`: A Track advances independently of every other Track in the Run.
- `INV-14`: Pipeline and Stage concurrency limits count only Agent or Action
  Sessions holding execution slots. Gate Sessions never consume one.
- `INV-15`: The control plane stores commit identity and bounded metadata,
  never repository contents or Git credentials.
- `INV-16`: Existing Tasks, Runs, Sessions, Attempts, events, results, and
  retained-worktree links migrate without loss.
- `INV-17`: Committed Run or Track cancellation prevents every later
  completion or retry from promoting a successor. It may record the observed
  result of one publication authorized before the cancellation fence.
- `INV-18`: A pending scheduled occurrence freezes the same complete ordered
  Pipeline snapshot as manual admission.
- `INV-19`: Commit ancestry and local-ref proof ignore every agent-writable Git
  replacement, graft, hook, helper, include, alternate, and configuration path.
- `INV-20`: A one-Agent-Stage Pipeline preserves the current persistent and
  fake-cloud execution contracts and creates no intermediate handoff solely for
  sequencing.
- `INV-21`: One multi-stage Track has one immutable working-branch name, and
  its accepted head advances only by descendant commits accepted from that
  Track.
- `INV-22`: A Gate successor receives only the Gate's typed bounded snapshot;
  absence, timeout, or provider failure cannot be presented as satisfied input.
- `INV-23`: Open PR and Update PR Actions are idempotent for one Track and never
  force-push, merge, or change the Track's frozen base branch.
- `INV-24`: Planned agent starts show an unconditional minimum and conditional
  maximum, each multiplied by selected repository count. Action, Gate, and
  retry counts are shown separately.
- `INV-25`: A conditional skip is success-like only when its frozen `run_if`
  references a prior PR review Gate whose outcome does not match. Every Stage
  in the contiguous block references that same Gate and outcome, is dependency-
  satisfied, and passes through the prior typed publication state.
- `INV-26`: A remote publication has one durable authorization ordered against
  cancellation. Only its frozen candidate may be written or reconciled.
- `INV-27`: A PR review Gate records `approved` only after every provider feed
  reaches its current end and the newest qualifying ordered event is an
  explicit approval for the target commit. It never infers approval from the
  absence of mutable thread state.
- `INV-28`: A provider event can choose a Gate outcome only when its actor
  satisfies the frozen approval policy through provider-confirmed repository
  permission.
- `INV-29`: A PR review Gate can choose an outcome only when the provider head
  observed under its Gate-check lease equals the exact target published commit.

### Requirements

- A Pipeline has 1 to 20 ordered Stages and 0 to 100 repositories. A draft may
  have no repositories, but admission requires at least one.
- The product of Stages and repositories must not exceed 500 planned Sessions
  in one Run.
- One resolved Agent Stage prompt is at most 64 KiB and all Agent Stage prompts
  in one Pipeline are at most 256 KiB. An Agent directly after a PR review Gate
  has a 32 KiB prompt limit. The versioned canonical formatter
  reserves 8 KiB of the 72 KiB complete-input limit for trusted Pipeline,
  Stage, Track, repository, branch, and commit context and 32 KiB for the
  Gate's canonical agent view.
- Stage names are unique within a Pipeline after Unicode case folding and
  whitespace normalization.
- An Agent Stage chooses Pi, Codex, or Claude Code, one execution profile, a
  prompt, an optional requested token ceiling, a timeout, and an optional
  concurrency limit. A missing ceiling is displayed as `No token ceiling`.
- An Action Stage chooses one supported deterministic adapter. V1 supports
  `open_pull_request` and `update_pull_request`.
- Open PR uses the Track's frozen base branch. Optional title and body templates
  are frozen with the Pipeline generation, resolve only documented Factory
  fields, and are limited to 256 bytes and 16 KiB. Omission uses canonical
  noninteractive defaults.
- A Gate Stage chooses one supported durable condition, poll interval, timeout,
  and the fixed `fail` timeout policy. V1 supports `pull_request_review` after
  an Open PR or Update PR Action in the same Pipeline. Its typed outcome is
  `feedback_requested` or `approved`.
- A PR review Gate freezes `actor_policy: repository_write`. A qualifying event
  must have an actor whom GitHub confirms currently has write, maintain, or
  admin repository permission. Unknown, missing, or unverifiable permission
  cannot satisfy the Gate.
- Omitted `run_if` means always. A conditional Stage references one prior PR
  review Gate ID and `feedback_requested`. No other condition, boolean
  expression, or provider field is accepted in V1.
- Server validation and editor mutations enforce the V1 sequence grammar. The
  first Stage is Agent. Open PR follows at least one Agent and occurs once. A
  PR review targets the Track's latest successful publication, including state
  passed through a conditionally skipped block. Any
  Agent after Open PR must be the first Stage in a contiguous feedback block
  and must be followed immediately by Update PR. Both reference the same prior
  PR review Gate and `feedback_requested`. Another PR review may follow Update
  PR for an explicit bounded round. Insert, reorder, kind change, and delete
  operations that break this grammar are rejected before save. The editor adds,
  moves, and deletes each three-Stage review block atomically.
- Every Agent Stage in a multi-stage Pipeline uses a persistent profile. A
  one-Agent-Stage Pipeline retains current persistent and fake-cloud support.
- For each repository in a multi-stage Run, at least one persistent Worker is
  eligible for every frozen Agent runtime, execution profile, and
  Worker-executed Action capability. First-Agent routing selects only from that
  complete capability intersection.
- Pipeline concurrency limits `queued`, `preparing`, and `running` Sessions in
  the Run. Stage concurrency limits the same states for that Stage. `waiting`,
  concurrency-blocked, and terminal Sessions consume no slot.
- The software-work view sorts actionable blocked and failed Sessions first,
  then active work, then recent successful or cancelled work.
- The editor always shows guaranteed and conditional-maximum Agent starts and
  token ceilings per repository and across the current repository selection.
  It labels missing ceilings and runtime telemetry unavailable.
- Cards always show Pipeline, Stage, repository, state, and relative time. A
  card that needs attention shows the reason inline. Branch, pull request, and
  token telemetry appear when applicable.
- The graph remains keyboard navigable and usable at 390 CSS pixels without
  truncating failure or blocked reasons.

## 6. Interfaces and data

The operator API replaces `/api/v1/tasks` with `/api/v1/pipelines`. Create and
update bodies contain Pipeline fields plus an ordered `stages` array. Run-now,
schedule, archive, generation conflict, idempotency, and pagination behavior
remain the same under Pipeline names.

Every Stage has common identity, name, position, kind, `run_if`, and optional
concurrency limit, plus one kind-specific configuration:

```json
{
  "stages": [
    {
      "id": "5c0e4bb0-1d35-4eb2-a2f3-25e9a971c318",
      "name": "Build",
      "kind": "agent",
      "agent": {
        "runtime": "codex",
        "execution_profile_id": "profile-local",
        "prompt": "Implement and verify the change.",
        "max_tokens": 120000,
        "timeout_seconds": 3600
      }
    },
    {
      "id": "3dc45e0b-628f-43b5-829d-da4d920a6a57",
      "name": "Open PR",
      "kind": "action",
      "action": {
        "type": "open_pull_request",
        "draft": true,
        "title_template": "{{pipeline}}: {{repository}}",
        "body_template": "Created by Factory run {{run_id}}."
      }
    },
    {
      "id": "26c686e4-e0cb-4269-aed4-c90fe0a65239",
      "name": "PR review",
      "kind": "gate",
      "gate": {
        "type": "pull_request_review",
        "actor_policy": "repository_write",
        "poll_seconds": 120,
        "timeout_seconds": 604800,
        "timeout_policy": "fail"
      }
    },
    {
      "id": "a06e0e60-5a37-4e6a-81aa-beb92ec467fa",
      "name": "Address feedback",
      "kind": "agent",
      "run_if": {
        "gate_stage_id": "26c686e4-e0cb-4269-aed4-c90fe0a65239",
        "outcome": "feedback_requested"
      },
      "agent": {
        "runtime": "codex",
        "execution_profile_id": "profile-local",
        "prompt": "Address the frozen review feedback and verify the change.",
        "max_tokens": 80000,
        "timeout_seconds": 3600
      }
    },
    {
      "id": "8f12de10-4ed9-4730-9335-870ae3fbf752",
      "name": "Update PR",
      "kind": "action",
      "run_if": {
        "gate_stage_id": "26c686e4-e0cb-4269-aed4-c90fe0a65239",
        "outcome": "feedback_requested"
      },
      "action": {
        "type": "update_pull_request"
      }
    }
  ]
}
```

Exactly one of `agent`, `action`, and `gate` must match `kind`. Unknown kinds,
adapter types, or fields fail validation. Reordering changes positions but not
Stage IDs. Changing kind creates a new Stage ID because historical identity
must not imply compatible behavior. Omitted `run_if` means `always`.

Stage IDs are omitted on create and returned by the server. Updates must supply
the returned IDs for unchanged Stages. A missing or foreign ID conflicts rather
than silently creating or adopting a Stage.

`POST /api/v1/pipelines/preview` accepts the same bounded draft body as create,
performs canonical grammar and capability-independent validation, and returns
unconditional and conditional-maximum Agent starts and token ceilings plus
Action and Gate counts. It writes nothing. The editor debounces this request
and renders only the response matching its latest draft hash. `GET
/api/v1/pipelines/{pipeline_id}` returns the same estimate for saved state.
`GET /api/v1/runs` adds bounded Track and
current-Stage summary data for the software-work projection. `GET
/api/v1/runs/{run_id}` returns the immutable Pipeline snapshot, Tracks,
Sessions grouped by Track and Stage, provider identities, token aggregates,
and existing Attempt summaries. The detail response does not inline Attempt
events or provider comment bodies.

Worker claims add `track_id`, `stage_id`, `stage_name`, `stage_kind`,
`stage_position`, `track_owner_worker_id`, `working_branch`, `input_commit`,
and any bounded typed provider input. A first Agent or Worker Action claim
before owner freeze has no owner or frozen input and may go to any eligible
Worker in the complete capability intersection. A later claim, or any retry
after owner freeze, is available only to the stored owner Worker and carries
the frozen input commit. Gate Sessions are never returned by the claim API.

Gate checks use a separate Worker-initiated lease protocol on the existing
outbound Worker poll. A healthy owner Worker asks for at most one due Gate
check. The server atomically grants a short lease containing Gate, Track,
provider, pull-request, target published commit, baseline cursor, deadline, and
outcome version. It grants no second live lease for that Gate and the check does
not consume Pipeline, Stage, or Worker execution capacity. The authenticated
owner returns a bounded result with the lease token, observed PR head, and
observed cursor. The server accepts it only when the lease, owner, target commit, outcome version,
deadline, and Run and Track cancellation fences still match. Duplicate results
return the stored outcome; stale or late results cannot satisfy the Gate. A
lost lease expires and becomes eligible for another owner-Worker poll. At most
one Gate check runs per Track and a Worker has a bounded Gate-check concurrency
of four. Factory never opens an inbound connection to a Worker.

Before an Open PR or Update PR remote write, the owner calls a lease-protected
publication-authorization endpoint with candidate commit, expected remote head,
idempotency key, and conservative per-feed cursors observed by a bounded
provider read. Open PR supplies empty feed cursors because no pull request yet
exists. The transaction checks the Action lease and cancellation fences and
stores the authorization exactly once. Replays return the frozen authorization;
a different candidate, expected head, or cursor set conflicts.

Before runtime launch, the Worker calls a lease-protected prepared endpoint
with resolved base branch, exact worktree HEAD, worktree identity, and working
branch. For the first Stage, the transaction freezes Track owner, base commit,
and input commit if unset. For a retry or later Stage, it rejects a different
Worker or commit. Only a successful prepared response authorizes startup. The
existing start endpoint continues to record supervisor process identity.

Preparation failure before owner freeze terminalizes the Attempt, releases
capacity, clears assignment, and lets the first-Stage retry choose any eligible
Worker and resolve the base input again. Preparation failure after owner freeze
releases capacity and blocks the Session on that Worker with a bounded reason
and retry time. Every later automatic or manual retry stays on the owner and
uses the stored input commit. It never reroutes the Track. Five automatic
preparation failures exhaust the execution cycle and fail the Session; manual
retry begins a new cycle and keeps Attempt history.

The claim protocol version increases. A prior Worker may renew and complete an
Attempt it already owns for a migrated one-stage Pipeline, but it cannot receive
another claim until it registers with Pipeline support. New Workers understand
one-stage Sessions without Track lineage and multi-stage owner-affine Sessions.

Agent Attempt completion adds `output_commit` for multi-stage success. The server
rejects a missing, malformed, unexpected-input, or wrong-Worker identity before
changing Session state. The authenticated owner Worker attests that it proved
ancestry and local-ref read-back because the control plane does not hold Git
objects. A one-stage Pipeline keeps the current completion contract and does
not require a local handoff ref.

Pipeline authoring and admission share a versioned canonical Agent prompt
formatter with the Worker. It validates the complete UTF-8 payload, including
maximum branch fields, typed Gate context, and trusted Pipeline context,
against the 72 KiB protocol limit. Save, schedule, admission, claim validation,
and Worker startup use the same formatter and bound.

The database gains Pipeline Stage and Track tables. Sessions gain Track and
Stage foreign keys, kind, `waiting` and `skipped` states, input and output
commits, bounded typed output, skip class, reason, and the Session whose outcome
caused the skip. Skip class distinguishes `conditional_success` from
`causal_failure` and cancellation. Tracks store owner Worker, immutable working-branch name, frozen base
commit, current accepted commit, published commit, pull-request identity,
publication authorization and per-feed baselines, review outcome, and workspace health. A Worker repository-ref projection
stores only the latest complete inventory scan and its health metadata. The
current unique Run and repository constraint becomes a unique Track and Stage
constraint.

Runs and Tracks gain cancellation timestamps used as promotion fences.
Executions gain preparation-failure count and next-routing time. Gate Sessions
store next poll time, deadline, target published commit, inclusive baseline and
observed cursors per provider event feed, page continuations, deduplicated item
IDs, chosen observation event, actor permission verification, outcome version,
bounded satisfaction snapshot, and any
active Gate-check lease identity, owner, and expiry. A Session blocked only by
Pipeline or Stage concurrency uses one
canonical, non-actionable reason so the claim transaction can promote it
without operator intervention.

Each pending scheduled occurrence stores the complete immutable Pipeline
generation it will admit: ordered Stages, repository identities, execution
settings, and scheduled instant. Editing a Pipeline only changes occurrences
created after the edit. The Pipeline's current archive state and schedule
enabled state still gate admission. Disabling the schedule or archiving the
Pipeline pauses an existing pending occurrence without changing its frozen
generation, retry state, or due instant. Admission resumes only when the
Pipeline is unarchived and its schedule is explicitly enabled. Restoring either
gate alone leaves the occurrence paused. An explicit discard removes it.

The occurrence-admission transaction reads and locks the source Pipeline row,
then rechecks both `archived = false` and `schedule_enabled = true` immediately
before inserting a Run. Disable, archive, and admission therefore have one
database order. If either pause mutation commits first, admission creates no
Run. If admission commits first, that Run was admitted before the pause and the
pause prevents only later admission.

For scheduled admission, absence of one complete owner candidate is transient
while the frozen repositories and profiles remain valid. The pending occurrence
keeps its snapshot, records the bounded `no_complete_pipeline_worker` health
reason, and retries with the existing scheduler backoff until an eligible fleet
returns. Invalid, deleted, or incompatible frozen repository or profile data is
permanent and blocks the occurrence under current scheduler rules. Manual
admission returns the same reason immediately without creating a Run.

The migration renames current Tasks and Task repositories to Pipelines and
Pipeline repositories. It creates one Stage named `Execute` for every current
Task, using the Task ID as Stage ID in its separate namespace. The Stage kind is
Agent, it preserves current prompt and runtime settings, and its requested token
ceiling is null. Current Run snapshots become one-Stage Pipeline snapshots.
Each current Session receives a Track and points to that Stage. Historical
Sessions expose null input and output commits unless the old record already
proved a value; the UI labels them unavailable rather than inventing them.

Every existing pending or paused scheduled occurrence converts to a one-Stage
`Execute` Pipeline generation from its own frozen Task snapshot, not the current
mutable Pipeline. Migration preserves due instant, retry state, health, enabled
or paused status, and association with an archived source.

Upgrade creates and validates the existing owner-only database backup before
migration starts. It refuses a name collision, unsupported schema, invalid
snapshot, or row it cannot map without changing the live database. The embedded
UI and API change in the same server build, so there is no mixed Task and
Pipeline operator surface.

Rollback is offline. The operator stops the new server and Workers and restores
the pre-upgrade backup with the supported restore command. Runs admitted after
upgrade are not present in that backup. The release guide states that data
boundary before upgrade.

The Worker creates local refs for code-producing Agent Attempts with this shape
in its repository cache:

```text
refs/factory/pipelines/<run-id>/<track-id>/<stage-position>-<stage-id>/<attempt-id>
```

The Worker creates a ref only when it does not exist and never updates it. Ref
publication is a recoverable three-step state machine. The Worker first writes
and fsyncs an owner-only `pending` manifest entry and its directory beside the
repository cache. The entry contains the full ref identity, repository
identity, output commit, and creation time. It then creates the exact ref with a
compare-and-swap from missing to output commit and finally atomically replaces
the entry with `ready` and fsyncs its directory. Recovery finishes a pending
entry when its ref is missing or matches, and marks repository health corrupt
when the ref conflicts. A completion replay may reuse the pair only when both
resolve to the same output commit. A new Attempt receives a new pair, so
response loss or lease expiry cannot poison retry. Only the accepted Attempt's
commit becomes the successor input. Local refs are not advertised or pushed
automatically.

After the control plane confirms acceptance, an intermediate successful Agent
worktree may be removed when it is clean and its local ref resolves to the
accepted commit. The manifest records this Pipeline-specific evidence before
cleanup. Response loss, rejection, or ref mismatch retains the worktree. The
final successful Agent Stage keeps the current clean-and-published cleanup rule
so the operator does not lose its visible delivery worktree merely because
Pipeline sequencing finished.

Workers reconcile local Pipeline refs through a cursor-bounded inventory API.
One scan has a random ID. Before sending page one, the Worker takes its
repository mutation lock and materializes the complete union of the local ref
namespace and manifest entries into an immutable, owner-only scan snapshot.
Attempt preparation, completion, ref publication, recovery, and cleanup use the
same lock. Snapshot creation waits until no agent process for that repository is
preparing, running, or finishing; if work remains active, the prior projection
becomes visibly stale instead of claiming a complete scan. The Worker releases
the lock after the snapshot is durable, and all pages of at most 500 records
read only that snapshot. Ref publication after the snapshot appears in the next
scan and cannot alter or disappear from the current pages.

Paired records contain stored Run, Track, Stage, Attempt, repository, full
commit, creation time, and manifest state. Before adding a pair to the
snapshot, the Worker reads the exact ref under the fixed config. A ref without a
manifest is still recorded as an orphan with unknown age. A manifest without a
ref is recorded as incomplete. A conflicting or malformed pair records corrupt
health. The server matches valid pairs to accepted Session output and publishes
aggregate repository health only after the Worker marks every snapshotted page
complete. An interrupted scan never replaces the last complete projection.

The stored projection contains ref count, oldest known creation time, unknown
age count, orphan count, incomplete count, corrupt count, scan status, last
complete scan time, and Worker freshness per repository. A Worker also reports
an explicit failed scan with a bounded reason. Repository detail reads this
projection and labels stale or incomplete data; the control plane never
inspects Worker files directly. Inventory is reporting only and does not
authorize deletion.

### Naming and identity

Pipeline, Run, Track, Session, Attempt, and newly authored Stage IDs are random
UUIDs created by the control plane. A migrated Stage reuses its Pipeline ID in
the Stage table so migration needs no unstable generated value. IDs are scoped
by table and never inferred from names.

Pipeline names keep the current normalized uniqueness rule. Stage names are
unique only inside one Pipeline. Renaming either changes the next Pipeline
generation; historical snapshots keep the prior name.

Local refs use stored IDs and zero-based Stage position. Operators cannot
provide a ref. A Pipeline edit cannot change refs for an admitted Run because
the Run snapshot freezes Stage identity and position.

The control plane generates the Track working branch once at admission from
stored identities, with the bounded shape
`factory/<pipeline-slug>/<run-short-id>/<repository-slug>`. Normalized slugs are
display aids; the immutable Run fragment prevents identity from depending on a
later rename. A collision with an existing local or remote branch fails
preparation visibly. Factory never silently adopts, deletes, or overwrites that
branch. Provider records use the provider repository identity plus pull-request
number as durable identity; a changed title or URL does not create another
record.

## 7. Failure behavior and lifecycle

Admission writes the Run, every Track, and every planned Session in one
transaction. First-stage Sessions begin blocked or queued through existing
routing. Later Sessions begin waiting. A partial write creates no Run.

The first Worker resolves base branch and commit during preparation, then
freezes owner and input through the prepared endpoint before launching the
agent. Retry fetches that exact commit from local cache even if the remote base
branch moved. A prepared request replay is idempotent for the same lease and
identity and conflicts for a different identity.

An agent exit of zero is provisional until commit and ref creation succeed.
Commit, ancestry, local ref, or read-back failure completes the Attempt and
Session as failed with a bounded actionable reason. The worktree is retained.
The same transaction skips every later waiting Session with this failure as
cause. No downstream Session becomes routeable.

Success completion records output identity, marks the Session succeeded, and
promotes its direct successor in one database transaction. With capacity
available, the successor becomes routeable to the owner Worker in that
transaction. Without capacity, it becomes blocked with the canonical
nonactionable concurrency reason. Existing claim materialization rechecks it
on each healthy owner Worker poll and promotes it when capacity exists.

An Open PR Action fails without advancing when branch publication is rejected,
the remote branch moved unexpectedly, `gh` is unauthenticated, provider rate
limits are exhausted, or the provider returns a conflicting pull request. A
retry reuses the same Track branch and provider lookup key. If branch
publication and PR creation succeeded but the response was lost, the Worker
finds and verifies that same pull request before reporting success. It never
creates a second PR for the Track. Success records the provider identity and
exact published commit but never replaces the pre-publication baselines.

A PR review Gate binds to the Track's latest successfully published commit and
the conservative baselines frozen before that publication. A success-like skip
passes the prior publication identity through unchanged. Each publication
authorization stores separate inclusive baselines for reviews, review comments,
and conversation comments before the remote write. Open PR starts from empty
feed IDs and the authorization time because the pull request does not yet
exist. The GitHub adapter reads reviews, pull-request review comments, and issue
comments through feeds with stable provider creation time and database ID. It
queries from each inclusive baseline with overlap, paginates to the current
feed end, and deduplicates immutable IDs. Mutable thread `isResolved` state is
not an outcome input because GitHub supplies no ordered resolution event or
feed-wide mutation version.

The same bounded provider observation reads the current pull-request head. If
it differs from the Gate's target published commit, the Gate fails actionably
with `remote_diverged` and cannot accept an approval or feedback outcome.
Normal retry keeps the target and baselines after the operator restores the
expected head or starts a new Run.

The adapter chooses the newest qualifying event by the total key
`(provider_created_at, provider_database_id, feed_kind)`. A review submission
qualifies only when its provider commit ID equals the target commit. Review and
conversation comments qualify when created after the frozen publication
boundary. Before ordering candidates, the Worker checks each distinct actor
through GitHub's repository-permission endpoint. It returns the actor login,
provider permission, and verification time as typed metadata. Missing access,
rate limiting, or an unverifiable response leaves the Gate waiting; the control
plane never infers eligibility from author association or event text. A
changes-requested review or comment from an eligible actor yields
`feedback_requested`.
An explicit approved review yields `approved` and is the observation boundary
for that Gate. Earlier feedback is superseded by that explicit approval. An
event ordered after it belongs to another explicit review round or later Run;
Factory does not claim a live snapshot of mutable GitHub thread state. More
than 2,000 events or 4 MiB of scan metadata remains waiting with
`review_state_too_large` and cannot choose an outcome.

Provider unavailability or rate limiting updates Gate health and uses bounded
exponential backoff without consuming an execution slot. The result transaction
advances each observed feed cursor and page continuation, then either remains
waiting or freezes `approved` or `feedback_requested` and promotes or
conditionally skips the complete block.
Deadline expiry marks the Gate failed and skips successors. Manual retry keeps
the target commit and baseline, records a new outcome version, clears the old
lease and failure, and sets a new deadline to `retry time + frozen timeout`.
The retry transaction conflicts with a concurrent accepted result or
cancellation. V1 has no deadline-extension endpoint or `keep_waiting` policy.

An Address feedback Agent starts from the Track's published commit and the
Gate's frozen snapshot. If the Gate outcome is `approved`, Address feedback and
Update PR are conditionally skipped without an Attempt. If feedback was
requested, the accepted Agent output promotes only Update PR. That Action
fast-forwards the remote branch with an expected-old-head check and promotes
later work only after durable publication succeeds. If the remote branch moved
independently, Update PR fails actionably with `remote_diverged` and does not
overwrite it. After external reconciliation, normal failed-Session retry reuses
the same authorization identity and frozen candidate, or the operator cancels
and starts a new Run.

An idempotent completion replay returns stored completion. An expired lease
cannot publish completion even if the Worker created a local ref. That orphan
ref is reported by reconciliation but does not advance the Track or conflict
with a later Attempt.

When a Session fails, every later waiting Session in that Track becomes skipped
and records the failed Session as cause. This is distinct from a success-like
typed conditional skip. Retrying the failed Session reopens the
Run, returns only never-started successors skipped by that failure to waiting,
and uses the same input. A successor with an Attempt is never reset.

Cancelling a Run first commits its cancellation timestamp, marks queued Sessions
cancelled, marks waiting or concurrency-blocked Sessions skipped, and then
requests cancellation for preparing or running Sessions. Cancelling one Session
commits a Track cancellation timestamp and applies the same rule to that Track.
Claim, prepared, start, completion, promotion, and retry transactions all
recheck both cancellation timestamps. A queued Session is never claimable after
the fence; an Attempt prepared concurrently cannot start after it. After the
fence, a completion endpoint first returns any matching terminal result that
committed before the fence, including a successful result whose response was
lost. That replay performs no new state change. With no stored result, it
accepts only an idempotent cancellation acknowledgement from the active
Attempt. That acknowledgement stores no output and marks the Session cancelled.
New success, failure, and unauthorised output publication are rejected and
cannot promote a successor. Publication authorization and cancellation are
ordered in the database. If cancellation commits first, authorization is
rejected and the Worker cannot begin the remote write. If authorization commits
first, cancellation records that one publication may already be in flight. The
Worker may report or reconcile only that frozen authorized candidate after the
fence. The server records whether the remote side effect occurred, terminalizes
the Action and Track as cancelled, and never promotes a successor. Response
loss retains the authorization for read-only reconciliation; it cannot
authorize another candidate. The UI says `cancelled after publication was
authorised` until the remote state is known.

If the active Attempt instead loses its lease, recovery terminalizes
the Session as cancelled because the committed cancellation fence is
authoritative, not failed. A cancelled Run or Track cannot reopen through
Session retry; the operator starts a new Pipeline Run.

`waiting` is neither claimable nor terminal. `skipped` is terminal. A Run is
active while any Session is blocked, queued, preparing, running, or waiting.
The canonical concurrency block counts as active but not actionable. A Run
needs attention when another actionable blocked or failed Session exists. It
becomes terminal when every Session is succeeded, failed, cancelled, or
skipped. Terminal aggregate state uses these ordered predicates:

1. `succeeded` when every Track completed every Stage by success or a
   success-like conditional skip.
2. `partial` when at least one Track completed every Stage that way but not
   every Track did.
3. `failed` when no Track completed and at least one Session failed.
4. `cancelled` when no Track completed, no Session failed, and a Run or Track
   cancellation timestamp exists.

An intermediate commit does not complete a Track. Cancellation before Stage 1
and cancellation after an intermediate Stage are both `cancelled`; completed
plus cancelled or failed Tracks are `partial`; failed plus cancelled Tracks
with no completed Track are `failed`.

If the owner Worker is offline, every later or retried Session remains blocked
with `Waiting for Track owner Worker <name>.` This is operational, not
actionable, while the Worker is registered and within its recovery window. It
becomes actionable after the existing lost-Worker threshold. Returning the
same Worker resumes claims.

If the owner remains online but loses a runtime or profile capability required
by a later Stage, that Session blocks on the owner with the exact health reason.
It does not route elsewhere. Restoring the capability resumes it; otherwise the
operator cancels the Track or Run.

If the owner Worker reports the Track commit or repository cache missing or
corrupt, one transaction fails the earliest incomplete Session with
`workspace_lost`, fences its active Attempt, and skips every later Session with
that failed Session as cause. Track workspace diagnostics retain the specific
missing or corrupt evidence. Normal terminal predicates then make a
single-Track Run `failed` and a mixed Run with another completed Track
`partial`. The UI explains that V1 cannot move local lineage to another Worker.
The operator may inspect retained state and start a new Run from the repository
base. Factory never silently restarts a later Stage from a different commit.

Editing Pipeline content or timing affects future occurrences only. Disabling
the schedule or archiving the Pipeline pauses admission of its existing pending
occurrence as well as preventing new ones, without mutating the occurrence
snapshot.
Server shutdown stops admission and scheduling before HTTP shutdown. Active
Worker leases and Attempt recovery remain otherwise unchanged.

## 8. Security, privacy, and operations

The local operator boundary does not change. Current persistent coding runtimes
are not a security sandbox. Multi-stage Agent execution has the same trust
level as current one-stage execution on that Worker. Pull-request Stages use the
Worker's existing authenticated `gh` and Git credentials. Credentials remain on
the Worker and are never returned to the control plane or browser.

Only the authenticated owner Worker with the active Attempt lease may report an
output commit. The server validates IDs, Worker identity, commit format, Stage
position, predecessor equality, and cancellation fences. The Worker disables
hooks for its own automatic commit. It verifies objects and ancestry in a fresh
Git directory with `GIT_NO_REPLACE_OBJECTS`, no graft file, no repository
config, and fixed empty system and global config. That directory reads a
manifest-frozen, copy-verified object snapshot containing only regular canonical
loose objects and pack files from the cache. It contains no `objects/info` directory, alternate
metadata, or symlink. Pack checksums, strict connectivity, and a canonical
rehash of every object reachable from input and output must pass before lineage
is accepted. It creates and reads back the exact local ref through direct ref
operations under that fixed config. It does not trust agent output to name a
successor commit.

Prompts, results, events, branches, local refs, provider metadata, feedback
snapshots, and commit metadata may contain sensitive project information and
keep current local retention rules. Git contents and credentials remain on
Workers and origins. Local checkpoint refs are never pushed automatically. An
Open PR Action pushes only the generated Track branch with an expected-old-head
check. It cannot push the base branch, tags, or unrelated refs.

Pull-request titles, bodies, reviews, and comments are untrusted input. Provider
responses are byte and item bounded before storage. The successor prompt labels
feedback as untrusted data, places it after Factory's fixed execution contract,
and never interprets comment text as Pipeline configuration, tool approval, or
credentials. Links are rendered as text or allowlisted provider URLs.
Public-repository events from actors without provider-confirmed write,
maintain, or admin permission never satisfy a Gate. Author association, display
name, and self-asserted role are not authorization evidence.

Multi-stage Pipelines require one persistent Worker that can prepare the
repository for the Track. Pull-request Stages additionally require healthy
`git` and authenticated `gh` capabilities with repository access. One-stage
Pipelines keep current behavior. A repository unavailable to a Worker is
handled by existing routing before owner freeze and by owner-affinity failure
afterward.

Local Pipeline refs keep accepted commits reachable and are not deleted
automatically in V1. Repository detail reports count and age. Retention and
safe cleanup require a later design because deleting a ref while a Run or
retained Attempt depends on it would lose evidence.

At most 500 Sessions are planned per Run, 100 hold execution slots, 20 Stages
are stored per Pipeline, and prompts total at most 256 KiB. One feedback
snapshot contains at most 100 items and 256 KiB after UTF-8 encoding. Its
canonical successor view is at most 32 KiB and records omitted item and byte
counts plus provider identities for explicit lookup. One Gate-check lease reads
at most 20 provider pages or 500 items. Persisted event-scan metadata is capped
at 2,000 items and 4 MiB per cycle; overflow is actionable and cannot choose an
outcome. Gate polls start at the
configured interval, back off to at most 15 minutes on provider failure, honor
provider retry hints, and never run more than one poll per Track at once. List
APIs remain cursor bounded. Attempt event and result limits remain unchanged.
Token ceilings stop new model requests when the runtime can enforce them;
otherwise they remain a visible requested budget and actual telemetry is
authoritative.

Object verification snapshots are limited to 1,000,000 files, 8 GiB, and 10
minutes each, with three race retries and a 16 GiB Worker-wide reservation.
Workers require the full reservation plus their configured free-space floor
before copy. These V1 constants are reported in Worker capabilities and any
overflow is actionable rather than retried indefinitely.

## 9. Acceptance criteria

- `AC-1`: An operator can create a three-Stage Pipeline for two repositories
  and start one Run without creating or invoking separate Tasks.
- `AC-2`: With capacity available, each repository makes Stage 2 claimable in
  the transaction that accepts its Stage 1 commit, independent of the other
  repository.
- `AC-3`: Stage 2 starts from the exact full commit reported by Stage 1 in a
  fresh worktree on the Track owner Worker.
- `AC-4`: A failed commit or local-ref operation leaves Stage 2 unstarted,
  shows an actionable reason, retains the Stage 1 worktree, and skips every
  later waiting Session with that failure as cause.
- `AC-5`: Retrying a failed Stage uses a fresh worktree at its frozen input and
  advances only successors in the same Track after success.
- `AC-6`: The landing view shows Pipeline Run groups, Stage columns,
  repository cards, attention reasons, runtime activity, and recent outcomes
  at desktop and 390-pixel widths.
- `AC-7`: Selecting a repository card opens Run detail with every Stage,
  Session, and Attempt for that Track. Commit fields appear when the Session
  contract recorded them; historical unavailable fields are not synthesized.
- `AC-8`: Editing or reordering a Pipeline leaves an active Run and all Stage
  identities unchanged.
- `AC-9`: Manual and scheduled admission create the same Run, Track, and
  Session shape.
- `AC-10`: A current database upgrades each Task and historical Run to a
  one-Stage Pipeline without losing Attempts, events, results, failures, or
  retained-worktree links.
- `AC-11`: The UI calls the authoring resource Pipeline and does not expose a
  second general-purpose Task board.
- `AC-12`: A current one-Stage Pipeline still runs successfully on persistent
  and fake-cloud profiles without local Stage handoff fields.
- `AC-13`: Response loss or lease expiry after local-ref creation does not block
  a new Attempt; only the accepted commit feeds the successor.
- `AC-14`: A pending scheduled occurrence retains frozen Stage order and
  settings after the Pipeline is edited, pauses while its schedule is disabled
  or its Pipeline is archived, and resumes unchanged only when the Pipeline is
  unarchived and the schedule is explicitly enabled.
- `AC-15`: Cancellation committed concurrently with claim, preparation, start,
  or Stage success prevents new execution and never makes the successor
  claimable. Queued work becomes terminal, and active work becomes cancelled by
  acknowledgement or lease recovery without turning the Run failed.
- `AC-16`: Preparation failure before owner freeze releases capacity and may
  route to a second eligible Worker and resolve base again; failure afterward
  stays owner-affine and reuses the frozen input.
- `AC-17`: A reset or replacement history cannot succeed because the output
  commit must descend from the frozen input.
- `AC-18`: An untracked file is never added automatically; completion fails
  until the agent stages, commits, ignores, or removes it.
- `AC-19`: The largest valid normal Agent prompt plus trusted context, and the
  largest feedback Agent prompt plus canonical Gate context plus trusted
  context, each fit the 72 KiB complete-input limit; the next byte is rejected
  before execution.
- `AC-20`: With Pipeline and Stage concurrency set to one, a promoted successor
  runs after its predecessor releases the slot; waiting and blocked Sessions do
  not deadlock the Track.
- `AC-21`: When the owner Worker goes offline and returns, the Track waits and
  then resumes from its exact local commit without running elsewhere.
- `AC-22`: Missing local commit or cache state fails the earliest incomplete
  Session with `workspace_lost`, skips its successors, produces the defined Run
  aggregate, and never starts from repository base or another Worker.
- `AC-23`: Admission rejects a repository with no Worker eligible for every
  Stage, and owner capability loss later blocks visibly without rerouting.
- `AC-24`: Replacement refs, grafts, cache-level local or HTTP alternates,
  symlinked object storage, or agent-written Git config cannot make an unrelated
  or externally stored output pass ancestry or local-ref verification.
- `AC-25`: A scheduled occurrence with no complete owner candidate keeps its
  frozen snapshot and admits successfully after the eligible fleet returns.
- `AC-26`: Repository detail shows the latest complete local-ref count, oldest
  known age, unknown-age count, orphan, incomplete, and corrupt counts, scan
  health, and freshness. A publication during paginated reporting appears only
  in the next immutable scan and cannot make the current projection partial.
- `AC-27`: A crash before or after every manifest and ref transition recovers a
  matching pair or reports the one-sided or conflicting state; no local ref is
  omitted from inventory.
- `AC-28`: If scheduled admission races with disabling the schedule or
  archiving the Pipeline, the transaction that commits first determines whether
  a Run exists; no Run is created after a pause commits.
- `AC-29`: A new Pipeline opens with one Agent Stage, and running it produces
  the same persistent or fake-cloud Worker behavior as a current Task without
  creating a sequencing checkpoint.
- `AC-30`: An operator can add, insert, reorder, configure, and remove Agent,
  Action, and Gate Stages in the graph editor with pointer or keyboard input at
  desktop and 390-pixel widths. Review-round blocks add, move, and delete as
  atomic PR review, Address feedback, and Update PR units.
- `AC-31`: Before saving or running, the server preview and editor show exact
  unconditional and conditional-maximum Agent starts and token ceilings,
  Actions, and Gates for one and multiple repositories. Missing ceilings are
  explicit, and retries are identified as additional usage.
- `AC-32`: A Plan, Build, and Test Agent sequence uses three fresh Attempt
  worktrees but one immutable Track branch name, and every accepted head is a
  descendant of the prior accepted head.
- `AC-33`: An Open PR Action fast-forwards only the generated Track branch,
  creates or finds exactly one pull request against the frozen Track base with
  bounded noninteractive title and body, records its identity, and uses no
  model tokens.
- `AC-34`: A PR review Gate holds no Worker execution slot, honors provider
  rate limits through a Worker-initiated fenced lease, scans current state for
  its exact target commit, and freezes exactly one `approved` or
  `feedback_requested` outcome without losing pre-poll feedback.
- `AC-35`: For `feedback_requested`, an Address feedback Agent receives the
  frozen untrusted feedback snapshot, starts at the published Track commit, and
  promotes only Update PR after accepted success. Update PR durably
  fast-forwards the same remote branch before later work advances. For
  `approved`, both conditional Stages are skipped without an Attempt.
- `AC-36`: An independently moved remote Track branch fails Update PR
  actionably with `remote_diverged`; normal retry reuses the frozen candidate
  and Factory never force-pushes or silently replaces it.
- `AC-37`: Run detail shows actual per-Stage and aggregate tokens when reported,
  and shows `Unavailable` rather than zero when runtime telemetry is absent.
- `AC-38`: Cancellation before publication authorization prevents any remote
  write. Authorization before cancellation permits only its frozen in-flight
  candidate to be reconciled, records the observed remote result, and never
  promotes a successor.
- `AC-39`: Review events created during publication response loss are included
  through frozen inclusive baselines and provider ID deduplication. The newest
  qualifying event at the Gate's explicit observation boundary determines the
  outcome; later events belong to a later review round or Run.
- `AC-40`: Approval conditionally skips a complete feedback block, passes
  through the prior publication identity, and lets a later explicit review
  round bind that identity. A feedback path binds the newly updated commit.
- `AC-41`: A multi-page review event scan chooses the newest qualifying event
  by the documented provider order only after all three feeds reach their
  current ends. Overflow remains visibly waiting with
  `review_state_too_large`; mutable thread-resolution state is never presented
  as snapshot proof.
- `AC-42`: A public user without write permission cannot approve a Gate or
  trigger Address feedback. A GitHub-confirmed write, maintain, or admin actor
  can, and permission lookup failure leaves the Gate waiting.
- `AC-43`: Object verification snapshots a bounded manifest without waiting for
  unrelated repository agents. A source changed during copy retries visibly
  and can never produce an accepted partial snapshot.
- `AC-44`: A PR head different from the Gate target fails with
  `remote_diverged`; an approval for the stale target cannot advance the Track.
- `AC-45`: Snapshot file, byte, time, retry, free-space, and Worker reservation
  limits fail before acceptance with an actionable reason. Success, failure,
  crash, and restart release the temporary directory and reservation without
  removing retained Attempt evidence.

## 10. Test approach

Store tests prove atomic admission, independent Track promotion, owner freeze,
owner-only routing, aggregate state, retry, cancellation races, generation
snapshots, typed Stage validation, Gate polling and outcomes, conditional
promotion and success-like skip, concurrency-block promotion, schedule
snapshots, structural cost estimates, limits, and every terminal predicate.
They prove the persisted portions of `INV-1` through `INV-29`;
Worker integration tests prove the Git and worktree portions.

Worker integration tests use two Workers and local bare origins. They prove a
Track freezes its first prepared Worker, each Stage uses a fresh worktree, the
successor starts from the exact accepted commit, and another Track may choose a
different Worker. Failure cases cover preparation before and after owner freeze,
dirty tracked work, explicitly staged new files, untracked credential-like
files, no-change success, response loss, lease expiry, reset history, malformed
commit IDs, replacement refs, grafts, config includes, object alternates,
local-ref conflict, missing objects, and corrupt cache state. Verification tests
put the reported commit only behind cache-level local and HTTP alternates and
symlinked object paths, then prove the validated snapshot rejects it. A forged
regular pack and index pair with mismatched object identity also fails checksum,
strict-connectivity, or canonical rehash validation. The tests prove fixed Git
configuration and `INV-19` through `AC-24`. Another agent remains running in a
different worktree while verification completes. Copy-time source replacement
or removal yields `object_snapshot_raced`, retries from a new manifest, and
never accepts the partial snapshot for `AC-43`. Fixtures cross each file, byte,
time, retry, free-space, and concurrent-reservation boundary by one unit and
prove cleanup and reservation recovery after success, failure, crash, and
restart for `AC-45`.
Capability-intersection tests reject admission when no one Worker can run every
Stage and block an owner whose later runtime becomes unhealthy for `AC-23`.

Recovery tests restart the owner Worker between Stages and prove it recovers
repository cache, local refs, retained worktrees, and manifest state before
claiming the successor. Offline-owner tests prove no other Worker may claim and
that the lost threshold changes the reason from operational to actionable.
Crash-injection tests stop before and after each manifest write, directory
fsync, ref compare-and-swap, and ready transition. They prove recovery and the
union inventory contract for `AC-27`, including agent-created ref-only state.
Inventory tests paginate an immutable snapshot while publishing another ref,
prove the current projection stays internally complete, and prove the next scan
contains the new ref for `AC-26`.

Workspace-loss tests fail the earliest incomplete Session, fence a concurrent
completion, and skip its successors. They prove `failed` for a single Track and
`partial` when another Track completed for `AC-22`.

Migration tests build the last pre-Pipeline schema with active, succeeded,
failed, cancelled, retried, and retained Sessions. They compare every source
row and payload after migration for `INV-16` and `AC-10`, and prove migration
refuses collisions or incomplete source state without changing the database.
Pending-occurrence fixtures cover enabled, disabled, archived, blocked, paused,
and retrying schedules. They preserve each frozen source snapshot, prevent
admission while disabled or archived, and resume the same occurrence after the
Pipeline is unarchived and the schedule is explicitly enabled. Disable,
archive, unarchive, and enable operations run in both orders and preserve the
due instant and retry state. Admission races both pause mutations and proves the
commit-order fence for `AC-28`. A complete fleet outage remains transient and
admits the same occurrence after Worker health returns for `AC-25`.

Cancellation race tests pause at claim, prepared, start, and completion
boundaries. They prove queued work terminalizes, active work cannot start after
the fence, cancellation acknowledgement is accepted without output, lease loss
after the fence becomes cancelled rather than failed, and no late success
promotes a successor for `AC-15`. Completion-first ordering replays the stored
success without mutation after a later fence; fence-first ordering rejects a
new success and accepts only cancellation.
Publication tests pause before and after authorization for Open PR and Update
PR. Cancellation-first issues no authorization or remote command.
Authorization-first permits only the frozen candidate, records whether it
reached the remote after cancellation, and never promotes later work.

HTTP contract tests cover Pipeline validation, pagination, immutable snapshots,
kind-specific Stage unions, legal sequence grammar, unsaved preview estimates,
claim fields, owner conflicts, commit validation, complete-input byte
boundaries, Gate-check lease grant, expiry, duplicate and stale result fencing,
Gate event-scan continuation and overflow, snapshots, provider metadata bounds,
and local or remote route boundaries. Inventory tests page
more than 500 refs, interrupt a scan, complete a later scan, classify accepted
and orphan refs, report failure and staleness, and prove the repository
projection for `AC-26`.

Provider integration tests use a fake `gh` executable and local bare origin.
They prove generated-branch publication, expected-old-head conflicts,
idempotent pull-request discovery after response loss, review cursors, explicit
approval and changes-requested events, qualifying review and conversation
comments, authorized and unauthorized public actors, permission lookup failure,
empty polls, events during publication response loss, inclusive
overlap and provider-ID deduplication, multi-page feed completion, explicit
event ordering before and after approval, bounded snapshots, rate-limit backoff,
retry deadlines, untrusted-text prompt framing, response loss before Update PR
completion, stale approved commit with a moved PR head, remote divergence, and
same-branch feedback updates for `AC-33` through `AC-44`. They prove no command can push the base branch, tags,
or an operator-owned ref. A two-round fixture proves approval passes through the
prior publication while requested feedback makes round two target the commit
published by round one.

React tests prove one-Stage defaults, graph insertion and reorder, inspector
fields, atomic review-block add, move, and delete, structural cost calculations, telemetry fallback, card grouping,
sorting, attention reasons, links, empty and failure states, and accessible
names. A real Chromium test authors both a one-Stage Pipeline and the Plan,
Build, Test, Open PR, PR review, Address feedback, Update PR sequence. It runs
approval skip and feedback execution paths for two repositories at desktop and
390-pixel widths, checks
console and failed requests, and verifies focus order, visible focus, editor,
Stage and Track navigation, and Enter and Space activation.

## 11. Risks and tradeoffs

- More Agent Stages can spend substantially more tokens than one capable agent
  loop. The editor shows starts and token ceilings before execution, and Run
  detail shows actual telemetry. It does not claim that more Stages are higher
  quality.
- An offline owner Worker blocks its Tracks. The UI makes that explicit and the
  existing Worker recovery path resumes exact local state. Cross-Worker
  continuation needs a later remote-checkpoint design.
- A lost local repository cache can lose intermediate lineage. V1 fails visibly
  rather than continuing from the wrong code. Operators who need disaster
  recovery should include Worker data directories in host backup.
- Automatic commits may include unintended tracked changes. The prompt names
  the contract, the Worker shows a diff summary in events, and every Agent
  Attempt uses a fresh worktree. Untracked files require explicit staging.
- Local refs accumulate. V1 reports and retains them because unsafe cleanup can
  remove code evidence. Retention needs a Track-aware follow-up design.
- Pull-request Actions add a write-capable provider boundary. They use existing
  Worker credentials, publish only generated Track branches, require expected
  remote heads, and never merge or force-push.
- Review text can contain prompt injection or secrets. Feedback is bounded,
  labelled untrusted, stored under current local retention, and never treated as
  configuration or approval.
- GitHub exposes no atomic snapshot across reviews and mutable thread state. V1
  therefore resolves a Gate at an explicit ordered review or comment event. A
  later event needs another review round or Run; Factory does not claim that an
  approved outcome proves all future or mutable thread state is quiet.
- Provider polling can consume rate limits. Gates poll no faster than their
  configured interval, honor retry hints, back off to 15 minutes, and show stale
  provider health instead of occupying Worker slots.
- A linear model cannot express parallel tests or approval fan-in. It still
  validates sequencing. A Stage runs at most once apart from retry. An operator
  can add another explicit PR review and Address feedback pair for a bounded
  second review round, but V1 has no automatic cycle. Explicit predecessor
  identity keeps a DAG or bounded-cycle migration possible.
- Renaming Task is disruptive. Keeping the old name would preserve a short API
  while making the product boundary unclear. Developer preview status and a
  lossless migration make the clean name preferable.
- Multi-repository Runs may show many cards. The 500-Session admission bound,
  collapsed completed Tracks, filters, and virtualized detail lists keep the
  first view bounded.

## 12. Open questions

- Should the Worker use a fixed Factory Git author or preserve the agent's
  configured author for automatic Stage commits? Recommend a fixed
  `Factory Pipeline <factory@local>` identity so provenance is explicit. This
  does not block task breakdown.
- Should a completed Track keep every accepted local ref or only the final ref?
  Recommend every ref in V1. Stage-level lineage is useful evidence, and safe
  cleanup deserves a separate retention design.
- Should repeated pull-request feedback automatically loop back to Address
  feedback? Recommend no for V1. One Stage should have one durable outcome and
  retries must reuse one input. An operator can add explicit review and address
  pairs or start a new Run. A later design can add a bounded review-cycle
  construct after the linear execution and cost model are proven. This does
  not block task breakdown.

## 13. Out of scope

- Cross-Worker continuation, remote checkpoint publication, and Track failover.
- Branching DAGs, parallel Stage execution inside one Track, and fan-in.
- Backward edges, general loops, and automatic repeated review cycles.
- Human approval, question, and resume nodes.
- Arbitrary conditional expressions beyond the typed PR review outcome.
- Automatic merge, deployment, rollback, pull-request approval, or pull-request
  closure.
- Provider adapters other than GitHub through authenticated `gh`.
- Multi-stage fake-cloud or future remote-cloud execution profiles.
- General business tasks, people assignment, due dates, and personal planning.
- Cross-repository dependencies inside one Track.
- Passing agent result prose directly into a successor Stage prompt.
- Moving Git file contents or credentials through the control plane.
- Automatic local-ref retention or deletion.
