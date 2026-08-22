# Software pipelines

> **Status:** Proposed for review

## 1. Executive summary

Factory runs one saved prompt across one or more repositories. That is the
right cheap path when one capable agent can plan, build, and test inside one
context window. It is insufficient when an operator deliberately wants
separate planning, implementation, verification, pull-request, and feedback
runs with different prompts, runtimes, budgets, and failure boundaries.

This design turns a Task into a **Pipeline** containing an immutable execution
**Graph** of typed **Stage nodes** and directed **Edges**. A one-node Pipeline
is the current one-agent loop with no extra
handoff cost. Adding Stages opts into more agent starts, context resets, token
spend, checkpoints, and control. Agent Stages run coding agents. Action Stages
perform bounded deterministic work such as opening a pull request. Gate Stages
wait without consuming an agent slot, for example until a pull-request review
has either approved the change or requested feedback.

One Pipeline Run creates one repository **Track** per selected repository. Each
Track owns one logical working branch and traverses the frozen Graph. A
successful code-producing Stage records an accepted commit. Each eligible
successor starts from that exact commit in a fresh Attempt worktree on the same
Worker, so several agent runs continue one branch without sharing a dirty
directory.

V1 keeps every multi-stage Track on one persistent Worker. This proves
sequencing and code lineage without requiring general cross-Worker Git handoff.
An explicit pull-request Action may publish the Track branch to its configured
origin and record the pull request. Publication is a visible Stage boundary,
not a hidden side effect of every agent run.

The browser has two related surfaces. The Pipeline editor is a polished graph
that begins with one large Stage card, supports inserting and reordering a
chain, and shows guaranteed and conditional agent starts and token ceilings
before a Run. The durable graph model leaves room for bounded branches and
feedback loops, while V1 exposes only a chain and one structured review branch.
The
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

The [Strands Graph pattern](https://strandsagents.com/docs/user-guide/concepts/multi-agent/graph/)
provides the closer execution reference. It models agents and deterministic
operations as nodes, dependencies and information flow as edges, explicit entry
points, conditional traversal, bounded concurrency, timeouts, and cyclic
execution limits. Factory adopts that graph vocabulary and deterministic state
machine. It does not embed the Strands SDK or accept user code as an edge
condition. Factory must persist every transition, preserve Git lineage, and
recover execution after process or host loss.

This design changes the authoring model, Run lifecycle, Worker worktree input,
provider-action boundary, and primary browser surface. It preserves the current
control-plane authority, leases, Attempt supervision, per-repository isolation,
local operator boundary, remote Worker authentication, repository cache, and
runtime adapters.

V1 validates a small graph subset. It allows a chain plus the exclusive branch
created by `Add review round`; it does not allow general parallel Agent fan-out,
code fan-in, cycles, or a general expression language. Agent, Action, and Gate
Stage types cover the first useful delivery flows. Each repository advances
independently, so a failure in one Track does not stop healthy Tracks. A
Pipeline may contain one Agent Stage node and no Edges, which is the exact
replacement for a current Task.

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
          ^                         |
          | claim, lease, events,   | signed typed source/provider envelopes
          | preparation, completion|
          |                         v
Persistent Worker
  repository cache, Track branch, and accepted commit lineage
  fresh Attempt worktree per Stage or retry
  Pi, Codex, Claude Code, and uncredentialed Git
          |
          | protected local typed requests
          v
Factory host authority
  host-domain identity and mode, source and provider credentials,
  private clone/fetch, Track publication, and bounded gh operations
```

The control plane owns desired order, Gate evaluation state, bounded provider
metadata, and durable state. The Track owner Worker owns repository objects,
the generated working branch, local checkpoint refs, Attempt worktrees, agent
processes, and uncredentialed local Git state. The host authority owns source
and provider credentials and executes bounded remote Git and provider calls.
The control plane records full commit IDs and pull-request identity but never
receives repository contents or Git credentials.

## 4. Proposed design

### How it works

A Pipeline generation freezes one Graph. A Graph contains one to twenty Stage
nodes, zero or more directed Edges, one entry node, and a bounded topology. Nodes
are Agent, Action, or Gate Stages. Edges state dependency and typed traversal
conditions. Every Track gets its own durable Graph execution state while sharing
the immutable Pipeline Graph definition.

A one-node Graph has one Agent Stage and no Edges.
It takes the existing one-agent path. There is no graph scheduler round trip,
handoff commit, or extra prompt. The Graph is a uniform stored shape, not a
reason to make the simple path more expensive.

For every Edge, a Track records `pending`, `traversed`, or `not_traversed`.
Unconditional Edges traverse when their source succeeds. A typed conditional
Edge traverses only when its source records the named outcome. V1 conditions
can inspect only a frozen PR review Gate outcome. They cannot inspect model
prose, repository files, provider fields, environment values, or user code.

Each node declares `activation: all` or `activation: any`. `all` requires every
incoming Edge to traverse. `any` requires one incoming Edge to traverse after
all mutually exclusive candidates are resolved. V1 uses `all` for ordinary
dependencies. The review-round macro uses `any` only at its exclusive
reconvergence point. If every incoming Edge becomes `not_traversed`, the node is
skipped with `unreachable`, and that fact propagates with a typed cause. A node
skipped directly because its mutually exclusive conditional Edge did not match
is `conditional_success`. A successor made unreachable only by
`conditional_success` predecessors inherits `conditional_success`, including
through unconditional Edges. Failure and cancellation causes are never
converted to success-like skips. Factory never inherits an SDK-specific AND or
OR default.

Graph state carries bounded typed outputs. Every successful node records its
status, execution count, duration, and kind-specific output. Git remains the
code and file channel: an Agent output is an accepted commit, an Open PR Action
output is a pull-request identity and published commit, and a review Gate output
is its frozen typed decision. A successor receives only the outputs declared by
its incoming Edges. Model prose is display evidence and never becomes an
implicit successor prompt.

V1 Graphs are acyclic and each Stage node executes at most once apart from a
retry of the same frozen node execution. The control plane derives the maximum
node executions per Track from the frozen node count; it is not an authorable
field. Retries remain Attempts of the same node execution. V1 schedules at most
one node holding an execution slot per Track, so Graph concurrency and a
Graph-wide timeout are not exposed. Pipeline and Stage concurrency still bound
work across Tracks, while each Gate keeps its own explicit timeout. A later
parallel or cyclic Graph design must add its own persisted limits and lifecycle
contract before relaxing these rules.

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
conditional Stages successfully. If another review Gate follows, feedback that
arrives while Address feedback runs is carried into that Gate. A terminal
Update PR never hides such feedback: it publishes the accepted commit but makes
the Track `partial` with a bounded pending-feedback snapshot. If the operator instead asks one
Build agent to plan, implement, and test, Factory runs one agent loop and pays
none of the intermediate context-reset cost.

Admission freezes the complete Pipeline generation and repository identities.
It creates one Run, one Track per repository, and one Stage record per Track
and frozen Stage node. The entry Agent enters routing immediately. Later Agent
and Action Stages start in `waiting` until their activation policy is
satisfied. Gate Stages enter `waiting` once their incoming Edges traverse and
remain there until their typed condition is satisfied. Tracks may use different
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

For this multi-stage handoff, when the Implement agent exits successfully, the
Worker preserves the private Agent Git directory and reads its index under fixed configuration. It stages
tracked modifications and deletions there. It never automatically adds an untracked file.
A new file enters the Stage output only when the agent explicitly staged or
committed it. Any remaining unignored, untracked file fails completion with a
bounded reason. The Worker disables hooks for its own commit, verifies the
output descends from the frozen input in a fixed verification environment, and
creates an Attempt-scoped local ref to keep the commit reachable. Commit and
ancestry verification use a validated temporary object snapshot. Under the
repository mutation lock, the Worker freezes one manifest over the union of the
read-only cache objects and the Attempt's private writable object directory.
It includes canonical loose objects and regular pack and index files, opens
every source without following symlinks, and copies it to a verification store.
It does not wait for unrelated
agents in other worktrees. Authority fetch and Worker repack, ref, and cleanup
operations use the same lock; authenticated fetch executes inside the broker
while holding it. Before copying, the Worker sums manifest files and bytes and
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
requested object ID. The Worker then creates and reads back a temporary
verification ref with direct operations under the same fixed config. It proves both IDs name
cryptographically valid commit objects available without an alternate, output
descends from input, the complete successor checkout is present, and the ref
resolves to output without executing agent-controlled behavior.

After proof, the Worker materializes every reachable output object missing from
the cache as canonical loose-object content in an owner-only quarantine
directory. Under the mutation lock it fsyncs and atomically installs each object
only when a missing or byte-identical destination is observed. It imports no
private config, refs, index, pack, alternate, or unreachable object. It then
proves the output commit resolves from the cache alone and creates the
Attempt-scoped ref. A staged new file and an Agent-created commit therefore
survive container exit only through the same validated reachable-object import.

Snapshot directories and reservations have owner-only manifests. They are
released after verification whether completion is accepted or rejected.
Startup recovery removes an incomplete snapshot or quarantine only when no live
completion owns its manifest, then releases the recorded reservation. The
private Agent Git directory remains with the Attempt until validated import and
completion reconciliation finish. The Attempt worktree and local ref follow
their separate retention rules and are never deleted by snapshot cleanup.

Handoff completion is two-phase. Under the active Attempt lease, the Worker
first proposes the verified output and Attempt ref. The control plane records a
pending branch transition with old head, candidate head, and random transition
ID, but does not accept the output, succeed the Session, or promote Test. The
Worker fsyncs that transition into the owned branch manifest, compare-and-swaps
the local working branch from old to candidate under the mutation lock, marks
the transition ready, and acknowledges it. Only then does one control-plane
transaction accept the output, mark Implement succeeded, update the Track head,
and make Test eligible. Test is
claimable only by the Track owner Worker. That Worker creates a fresh Attempt
worktree at the exact Implement commit. Test never reuses Implement's worktree,
so failed or dirty work remains inspectable without contaminating its input.
The same handoff occurs at every Agent boundary. The Track's logical branch
head advances only to an accepted output commit. Attempt worktrees and local
checkpoint refs are implementation details beneath that one branch lineage.

An Open PR Action runs on the Track owner after a code-producing predecessor.
It first obtains a durable publication authorization ordered against Run and
Track cancellation. That transaction freezes the candidate and expected remote
head, provider observation inputs, successor eligibility floor when applicable,
and an idempotency key. Only then may the
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
succeeded Agent Session backward. Its authorization also freezes a stable
preceding-Gate vector and successor topology. When another PR review Gate
follows, that Gate inherits the vector as its eligibility floor rather than the
new publication-time feed ends. When Update PR is terminal, a stable
post-publication observation compares against that floor. Eligible feedback
makes the Track `partial` with reason `feedback_arrived_during_address` and a
bounded pending snapshot.

A PR review Gate is enabled only after an Open PR or Update PR Action. It does
not hold a Worker slot. The owner Worker receives a bounded, non-execution Gate
check lease on its normal outbound poll, performs a rate-limited `gh` check,
and returns the result under that lease. The Gate records one of two typed
outcomes from the newest qualifying provider event. `feedback_requested` means
that event is a changes-requested review, a `COMMENTED` review with a nonempty
canonical body, a review comment, or a top-level conversation comment.
`approved` means it is an explicit approved review
submission for the exact target commit. Events from Factory's own account and
provider system events are excluded. The actor must also satisfy the frozen
`repository_write` policy: GitHub must confirm write, maintain, or admin
permission for the target repository. Other public events may be displayed but
cannot choose an outcome or start an agent. Otherwise the Gate remains waiting.
Provider text is untrusted and any feedback snapshot is bounded.

The review macro creates exclusive conditional routing. The
`feedback_requested` Edge enters Address feedback, which receives the frozen
snapshot, starts from the exact published branch head, and makes changes
locally. A required Update PR Action then fast-forwards the same remote branch
with its accepted output. When another node follows the review block, an
`approved` Edge bypasses the feedback nodes and reconverges there. When the
review block is terminal, approval completes the Track without another Edge.
Nodes made unreachable by the unmatched feedback Edge are skipped as
`conditional_success`. That class propagates through Address feedback and its
unconditional Edge to Update PR, and through any other exclusively skipped
feedback node, until an approved Edge reconverges or the Track completes. A
failure or cancellation on either path propagates its own non-success class
instead. A timeout fails the Gate. Factory never starts an Agent with an empty
invented feedback set. A terminal Update PR publishes its accepted output, but
late eligible feedback makes the Track partial rather than successfully hiding
work the Agent never saw.

A multi-stage Agent that makes no changes records its exact input commit as
output and creates the same Attempt-scoped local ref. Every successful Agent
handoff therefore has one uniform commit proof. A one-stage Pipeline keeps the
current completion contract and creates no handoff ref.

On a `pipeline_isolated` host, one-stage execution still uses the private Agent
Git directory. Container launch records an Attempt manifest containing the
host-side private Git path, then exposes `/factory/git` only inside the
container. At exit, the Worker treats that directory as untrusted input. Under
fixed config it snapshots and canonically rehashes the complete object closure
reachable from the validated input, final Attempt HEAD, and every index entry.
It rejects invalid index stages, paths, modes, objects, refs, or a HEAD that is
not a cryptographically valid commit. It does not require final HEAD to descend
from input. Reset, detached HEAD, merge, rewritten history, and an unrelated
valid commit remain accepted one-stage results because they advance no shared
Pipeline lineage. Attempt history records final symbolic-ref or detached shape,
commit ID, and its computed relation to input for visible inspection. The same
snapshot file, byte, time, free-space, and reservation limits apply; overflow
retains the untrusted Attempt evidence and fails actionably.

The Worker builds a new Attempt-owned host Git directory from scratch. It copies
the complete verified closure as canonical objects, reconstructs only the
validated final symbolic ref and HEAD, or a detached HEAD, and recreates the
index from validated entries. It writes fixed config with no includes, helpers,
hooks, aliases, replacement
refs, grafts, or alternates. The host directory is therefore self-contained and
does not depend on later cache retention. Only after fsync does the Worker
atomically point the retained worktree at this sanitized directory and report
the current one-stage result. Agent-created commits, staged new files, dirty
tracked files, and an unchanged index remain inspectable; Agent-controlled Git
configuration and extra refs do not cross the boundary.

Sanitized reattachment does not import private objects into shared cache,
advance a Track accepted head, create a sequencing ref, or automatically commit
for handoff. The sanitized Git directory, untrusted evidence directory, and
worktree share one retention manifest and are recovered or deleted as one unit.
Response loss and restart replay the build and pointer swap before completion
or cleanup; a half-written phase leaves the Attempt retained and visibly
failed, never partly cleaned. Factory and ordinary host `git status` and
`git log` use only the sanitized directory and cannot execute Agent hooks,
helpers, aliases, or alternate paths.

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
feedback, and Update PR with one shared visual bracket and valid conditional
Edges. Each card remains separately configurable, but move and delete act
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
Address feedback, then Update PR. Once copied, every example is an ordinary
Pipeline.

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

In the editor and Run detail, Factory auto-lays out the frozen Graph from left
to right. The ordinary chain stays on one line. A review branch opens one short
lower lane and reconverges without crossing another Edge. The selected path is
solid, a conditional path not taken is muted, the active Edge uses restrained
motion, and completed nodes collapse to evidence summaries. Node coordinates
are presentation state and are never stored in the Pipeline API. At narrow
widths the same Graph becomes an indented vertical path with explicit
`If feedback` and `If approved` labels.

### Components and responsibilities

The Pipeline service owns Pipeline validation, immutable generations, Stages,
repository scope, schedules, and admission. It depends on repository and
execution-profile readiness. It does not claim work or inspect Git contents.

The Run service owns Tracks, frozen Graph execution state, Edge decisions,
node activation, Worker affinity, Session promotion, Gate cursors, aggregate
state, cancellation, retry, branch and
commit identity, and bounded provider metadata. It depends on existing routing
and Attempt services. It does not create commits, hold origin credentials, or
decide whether local Git objects exist.

The Worker owns repository preparation, Attempt worktrees, local commit and ref
creation, exact-input verification, runtime execution, provider-operation
and source-operation requests, retention, and recovery. It does not choose the next
Stage, interpret provider text as trusted instructions, or change dependency
state.

The host authority owns security-domain attestation, provider credentials,
typed private-repository clone, fetch, and default-branch reads, remote Track
branch publication and `gh` operations, and mixed-mode exclusion. It has no
role in Stage selection or local commit acceptance.

The browser owns the Pipeline graph editor, automatic layout, structural cost
estimate, software-work graph, filters, and links to Run and Stage detail. It
reads server projections and does not derive Edge decisions, dependency, cost,
or provider truth from local state.

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

**Store a Graph but expose a constrained topology first.** Nodes and Edges are
the canonical saved and snapshotted model. V1 authoring allows a chain plus one
typed, exclusive review branch created by a macro. It rejects parallel Agent
fan-out, code fan-in, cycles, and arbitrary conditions. Storing only an ordered
array was rejected because adding dependencies later would require a second
Pipeline model and a migration of every saved generation. A fully free-form DAG
was rejected because Factory has not defined how two code-producing branches
merge. The cost is that the server must validate a Graph even for a one-node
Pipeline, though that path performs no extra execution work.

**Define traversal semantics in Factory.** Every node explicitly chooses `all`
or `any` activation and every Edge reaches a durable traversed or
not-traversed state. Factory does not inherit the different fan-in defaults of
an agent SDK. `any` is allowed in V1 only for the mutually exclusive review
reconvergence. This makes recovery and skip behavior deterministic, but adds
stored Edge state to each Track.

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

- `INV-1`: A Run contains one immutable Pipeline snapshot, including the Graph
  nodes, Edges, entry point, limits, repository identities, execution settings,
  and source.
- `INV-2`: A Track belongs to exactly one Run and one snapshotted repository.
- `INV-3`: A Session belongs to exactly one Track and one snapshotted Stage.
- `INV-4`: One Track contains at most one Session for a Stage.
- `INV-5`: A non-entry Session cannot become routeable until its frozen node
  activation policy is satisfied by durable incoming Edge states and every
  required typed output is durably recorded.
- `INV-6`: An Agent Session input is the frozen base commit for the first
  code-producing Stage or the Track's exact latest accepted commit.
- `INV-7`: In a multi-stage Pipeline, one Worker owns a Track from the first
  successful prepared call until the Track is terminal. A one-stage Pipeline
  retains its current backend ownership contract.
- `INV-8`: Every multi-stage Agent Attempt starts in a fresh worktree at its
  Session's exact input commit on the Track owner Worker. One-stage fake-cloud
  execution retains its current dispatcher lifecycle.
- `INV-9`: A successful multi-stage code-producing Agent Session records one
  full output commit that descends from its input and one Attempt-scoped local
  ref keeping it reachable.
- `INV-10`: Failed, cancelled, lost, or timed-out Attempts cannot advance a
  Track or replace its last accepted commit.
- `INV-11`: Retrying a failed Session reuses its frozen Agent input, Action
  authorization, or Gate target and per-feed eligibility floors and cannot change a
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
  result of one publication authorized before the cancellation fence or
  reconcile and roll back one local branch transition proposed before it.
- `INV-18`: A pending scheduled occurrence freezes the same complete Pipeline
  Graph snapshot as manual admission.
- `INV-19`: Multi-stage commit ancestry and local-ref proof ignore every
  agent-writable Git replacement, graft, hook, helper, include, alternate, and
  configuration path. Isolated one-stage completion instead applies the
  sanitized ref-shape, HEAD, object-closure, and index proof in `INV-38`.
- `INV-20`: A one-Agent-Stage Pipeline preserves the current persistent and
  fake-cloud execution contracts and creates no handoff ref, including for a
  no-change completion.
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
- `INV-25`: A node skipped directly because every incoming mutually exclusive
  conditional Edge resolves `not_traversed` is `conditional_success`. That
  class propagates transitively through a successor whose possible incoming
  paths are all unreachable solely because of `conditional_success`, including
  across unconditional Edges. A failure or cancellation cause takes precedence
  and retains its own class. Edge conditions may reference only the frozen
  typed outcome of their declared source Gate.
- `INV-26`: A remote publication has one durable authorization ordered against
  cancellation. Only its frozen candidate may be written or reconciled.
- `INV-27`: A PR review Gate records `approved` only after two consecutive,
  complete, bounded sweeps of all three flat provider feeds produce identical
  normalized content digests and observe the target pull-request head. The
  newest unambiguous qualifying event inside that stable feed vector must be an
  explicit approval for the target commit. Conflicting outcomes at the same
  cross-feed timestamp resolve conservatively to feedback. It never infers
  approval from the absence of mutable thread state.
- `INV-28`: A provider event can choose a Gate outcome only when its actor
  satisfies the frozen approval policy through provider-confirmed repository
  permission.
- `INV-29`: A PR review Gate can choose an outcome only when the provider head
  observed under its Gate-check lease equals the exact target published commit.
- `INV-30`: An existing working branch is reusable only when its durable owner
  is the same Track and its local and remote heads match the Track's expected
  commits.
- `INV-31`: An Agent process in a Pipeline with provider Stages has no provider
  write credential or access to the privileged typed helper.
- `INV-32`: A private Agent object becomes a handoff input only after canonical
  rehash, reachable-closure validation, cache import, cache-only resolution,
  and Attempt-ref proof.
- `INV-33`: A Track accepted head and successor advance only after the matching
  write-ahead local branch transition is ready and finalized.
- `INV-34`: Once completion proposal records `finalizing`, Agent-lease or
  transition-lease expiry cannot terminalize or reassign the Session. Only
  successful finalize, owner reconciliation, explicit lost-owner abandonment,
  or a proven ref conflict can leave that state.
- `INV-35`: Lost-owner abandonment permanently fences finalization, never
  accepts the candidate or promotes a successor, releases capacity, and leaves
  a durable rollback-only transition tombstone. Its failure is non-retriable.
- `INV-36`: One authenticated host security domain is permanently either legacy
  or Pipeline isolated. Every co-resident Worker has that mode. A legacy domain
  never runs multi-stage work or receives Factory-managed provider-helper
  credentials; a Pipeline domain never launches an unsandboxed Agent.
- `INV-37`: A Pipeline Worker holds no source or provider credential. Every
  authenticated remote read or write is a typed host-authority operation bound
  to an enrolled repository and signed control-plane authorization.
- `INV-38`: An isolated one-stage Attempt preserves its private Git directory
  only as untrusted evidence and attaches the retained worktree to a separately
  rebuilt, self-contained, sanitized Git directory. It never imports into shared
  cache, advances a Track head, or creates a sequencing ref.
- `INV-39`: A host authority accepts command envelopes only from the mutually
  pinned control-plane signing key generation; request data cannot introduce or
  rotate a verification key.
- `INV-40`: Privileged authority code never executes Git or provider tooling.
  Every remote operation runs as the dedicated unprivileged broker with one
  registered target and one ephemeral operation-scoped credential.
- `INV-41`: On macOS, authority, Workers, caches, locks, worktrees, brokers, and
  Agent containers all reside inside one enrolled Linux VM with no writable
  macOS mount or Agent-accessible management channel.
- `INV-42`: A one-Agent-node Graph with no Edges uses the current one-stage
  execution path and creates no graph handoff, extra agent start, or implicit
  context reset.
- `INV-43`: Every admitted Track records each Edge as exactly one of `pending`,
  `traversed`, or `not_traversed`; a committed Edge decision never changes.
- `INV-44`: Node eligibility uses only the frozen Graph, durable predecessor
  outcomes, declared typed outputs, and explicit `all` or `any` activation. It
  never depends on model prose or an SDK default.
- `INV-45`: V1 Graphs are acyclic, have one entry node, reach every node from
  that entry, and execute each node at most once apart from Attempts that retry
  the same frozen node execution.
- `INV-46`: One Track has at most one accepted code head at a time. V1 rejects
  any topology that could make two code-producing nodes concurrently eligible
  or require merging their outputs.
- `INV-47`: Update PR never advances a following Gate's eligibility floor past
  the preceding Gate's stable consumed vector. Eligible feedback posted or
  edited after that vector and inside the following Gate or terminal
  observation boundary is either consumed by the following Gate or makes a
  terminal Update PR Track visibly partial; it is never silently treated as
  already addressed. Events first observed after that explicit boundary belong
  to a later Run.
- `INV-48`: A floor item whose normalized digest changed and whose current
  representation qualifies as feedback cannot be ordered by its older creation
  or submission time. After actor authorization it conservatively chooses
  `feedback_requested` ahead of unchanged timestamped candidates because GitHub
  supplies no trustworthy ordered mutation event for every supported feed.

### Requirements

- A Pipeline Graph has 1 to 20 Stage nodes, 0 to 24 Edges, exactly one entry
  node, and 0 to 100 repositories. A draft may have no repositories, but
  admission requires at least one.
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
- A review Gate freezes its stable per-feed vector with its outcome. Update PR
  passes that exact vector to a following review Gate as the eligibility floor;
  it cannot substitute the feeds observed at Update PR publication. A terminal
  Update PR records `partial` after publishing when its stable publication
  vector contains eligible feedback absent from the preceding Gate vector.
- Terminal Update PR freezes a one-hour post-publication observation timeout.
  Expiry is actionable failure, never success. Retry repeats only the feedback
  observation against the same published commit, floor, digest version, and
  authorization and sets a fresh one-hour deadline; it cannot publish again.
- A PR review Gate freezes `actor_policy: repository_write`. A qualifying event
  must have an actor whom GitHub confirms currently has write, maintain, or
  admin repository permission. Unknown, missing, or unverifiable permission in
  the newest potentially decisive timestamp group leaves the Gate waiting, and
  the observation cannot advance any feed cursor or deduplication state past
  that unresolved group. Once a newer group has an authorized decisive
  outcome, strictly older groups require no permission lookup and cannot block
  observation progress.
- An omitted Edge condition means unconditional traversal after source success.
  A conditional Edge references its source PR review Gate and one outcome,
  `feedback_requested` or `approved`. No other condition, boolean expression,
  provider field, or executable callback is accepted in V1.
- Server validation and editor mutations enforce the V1 Graph grammar. The
  entry Stage is Agent. Every node is reachable and the Graph is acyclic. Open
  PR follows at least one Agent and occurs once. A
  PR review targets the Track's latest successful publication, including state
  passed through an exclusive review branch. Any Agent after Open PR must be
  Address feedback on the `feedback_requested` Edge and must flow immediately
  to Update PR. The `approved` Edge bypasses both and may reconverge only with
  Update PR and may reconverge with the approved Edge only at one following
  `activation: any` node, or both routes may end at a terminal sink. Another PR review
  may follow that reconvergence for an explicit bounded round. Every other node
  uses `activation: all` and has at most one incoming and one outgoing Edge.
  Insert, reorder, kind change, Edge change, and delete operations that break
  this grammar are rejected before save. The editor adds, moves, and deletes
  each review topology atomically.
- Every Agent Stage in a multi-stage Pipeline uses a persistent Worker
  advertising `agent_isolation`. A one-Agent-Stage Pipeline retains current
  persistent and fake-cloud support through a compatible host security domain.
- For each repository in a multi-stage Run, at least one persistent Worker is
  eligible for every frozen Agent runtime, execution profile, and
  Worker-executed Action capability. First-Agent routing selects only from that
  complete capability intersection.
- Every multi-stage Pipeline requires a persistent Worker advertising
  `agent_isolation`. A Pipeline containing Open PR, Update PR, or PR review also
  requires `provider_credential_isolation`. V1 readiness requires rootless
  Podman 5+, the frozen image digest, keep-ID user namespace, the fixed mount
  layout, isolated runtime secret, no helper or host sockets, an uncredentialed
  remote, and, for provider Stages, the authority-owned typed provider broker.
- A private repository on a Pipeline host also requires an authority advertising
  `source_broker` for its frozen provider identity. Clone, fetch, and default-
  branch resolution are admitted only through that typed capability; a public
  repository may use the same broker without a credential.
- Host readiness refuses Pipeline capability unless the enrolled authority key,
  pinned control-plane key generation, dedicated broker UID, privilege drop,
  mount and syscall policy, registered cache roots, credential minting, and
  one-shot result channel pass their self-tests.
- On macOS, readiness additionally proves authority, Workers, caches, locks,
  worktrees, brokers, and Agent runtime all reside inside the enrolled Linux VM,
  with no writable host mount or Agent-accessible VM management channel.
- Pipeline concurrency limits `queued`, `preparing`, `running`, and `finalizing`
  Sessions across Tracks in the Run. Stage concurrency limits the same states
  for that Stage across Tracks. Within one Track, V1 promotes at most one Agent
  or Action Session into an execution-slot state at a time. `waiting`,
  concurrency-blocked, Gate, and terminal Sessions consume no slot.
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
update bodies contain Pipeline fields plus one `graph` object. Run-now,
schedule, archive, generation conflict, idempotency, and pagination behavior
remain the same under Pipeline names.

The Graph contains `entry_node_id`, `nodes`, and `edges`.
Every Stage node has common identity, name, display order, activation policy,
kind, and optional concurrency limit, plus one kind-specific configuration.
Every Edge has stable identity, source and target node IDs, and an optional
typed condition.

This example is both a valid create or preview draft and the Graph shape in a
saved Pipeline response. The editor creates an immutable UUID for every node
and Edge before submission. `entry_node_id`, Edge `source`, and Edge `target`
therefore resolve inside a draft without a server round trip.

```json
{
  "graph": {
    "entry_node_id": "5c0e4bb0-1d35-4eb2-a2f3-25e9a971c318",
    "nodes": [
      {
        "id": "5c0e4bb0-1d35-4eb2-a2f3-25e9a971c318",
        "name": "Build",
        "display_order": 0,
        "activation": "all",
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
        "display_order": 1,
        "activation": "all",
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
        "display_order": 2,
        "activation": "all",
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
        "display_order": 3,
        "activation": "all",
        "kind": "agent",
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
        "display_order": 4,
        "activation": "all",
        "kind": "action",
        "action": {
          "type": "update_pull_request"
        }
      }
    ],
    "edges": [
      { "id": "d5b7137d-0c81-4bd3-8797-99162a8fdd2c", "source": "5c0e4bb0-1d35-4eb2-a2f3-25e9a971c318", "target": "3dc45e0b-628f-43b5-829d-da4d920a6a57" },
      { "id": "33b15f95-df9d-4b3e-8dca-862e68e69381", "source": "3dc45e0b-628f-43b5-829d-da4d920a6a57", "target": "26c686e4-e0cb-4269-aed4-c90fe0a65239" },
      {
        "id": "202b70fa-492e-43a0-b8c9-8e58556d9f4c",
        "source": "26c686e4-e0cb-4269-aed4-c90fe0a65239",
        "target": "a06e0e60-5a37-4e6a-81aa-beb92ec467fa",
        "condition": { "type": "gate_outcome", "outcome": "feedback_requested" }
      },
      { "id": "4d8bb8dd-2078-412e-81eb-784cadfc1d48", "source": "a06e0e60-5a37-4e6a-81aa-beb92ec467fa", "target": "8f12de10-4ed9-4730-9335-870ae3fbf752" }
    ]
  }
}
```

Exactly one of `agent`, `action`, and `gate` must match `kind`. Unknown kinds,
adapter types, conditions, activation policies, or fields fail validation.
Reordering changes `display_order` but not node or Edge IDs. An omitted
condition means unconditional.

Create, preview, and update requests must supply canonical UUIDs for every node
and Edge. IDs must be unique within the draft. On update, an existing object
keeps its ID; a new object uses a newly generated ID; a deleted ID cannot be
reused in that Pipeline; and an ID already owned by another Pipeline is
rejected. Changing node kind creates a new node ID because the historical
identity does not imply compatible behavior. The server returns the same IDs
and freezes them into Runs so entry resolution, traversal, and conditional
decisions keep stable historical identity.

`POST /api/v1/pipelines/preview` accepts the same bounded draft body as create,
performs canonical Graph, topology, and capability-independent validation, and
returns unconditional and conditional-maximum Agent starts and token ceilings,
Action and Gate counts, reachable nodes, and the longest possible node path.
It writes nothing. The editor debounces this request
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
provider, pull-request, target and preceding published commits, complete
eligibility-floor vector, normalization-digest version, deadline, and outcome
version. It grants no second live lease for that Gate and the check does not
consume Pipeline, Stage, or Worker execution capacity. The authenticated owner
returns a bounded result with the lease token, both sweep digests, stable
second-sweep vector, observed PR head, normalized candidates, permission proofs,
and bounded snapshot. The server accepts it only when the lease, owner, target
commit, floor, digest version, outcome version, deadline, and Run and Track
cancellation fences still match. Duplicate results return the stored outcome;
stale or late results cannot satisfy the Gate. A
lost lease expires and becomes eligible for another owner-Worker poll. At most
one Gate check runs per Track and a Worker has a bounded Gate-check concurrency
of four. Factory never opens an inbound connection to a Worker.

Before an Open PR or Update PR remote write, the owner calls a lease-protected
publication-authorization endpoint with candidate commit, expected remote head,
and idempotency key. The transaction checks the Action lease and cancellation
fences and stores the authorization exactly once. Open PR stores empty floors.
Update PR binds the preceding Gate ID and outcome version, its complete stable
vector as the inherited floor, the normalization-digest version, and whether
the frozen Graph has a following review Gate. For terminal Update PR it also
freezes the V1 one-hour observation timeout. These values come from durable
server state, never a Worker-supplied cursor. Replays return that complete frozen
identity; a different candidate, expected head, preceding outcome, digest
version, or successor topology conflicts.

The publication result reports the exact observed remote commit under that
authorization. For Update PR with a following Gate, one transaction verifies
the remote commit, marks the Action succeeded, and writes the frozen inherited
floor and digest version to the successor Gate. Terminal Update PR instead
records the publication, moves the Action to `observing_terminal_feedback`,
stores a deadline of `publication verified time + 1 hour`, releases its
execution slot, and becomes eligible for the same bounded
Worker-initiated observation lease used by Gates. That lease runs after remote
publication and returns the two-sweep proof, stable vector, normalized late
candidates, permission proofs, and bounded pending snapshot. Only its fenced
result transaction may choose full success or `partial`.

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
Registration includes a short-lived signed host-authority attestation binding
host-domain ID, Worker ID, immutable mode, boot nonce, binary digest, isolation
self-test digest, and expiry. The control plane rejects a missing, expired,
replayed, unknown-key, or mode-conflicting attestation before registering the
Worker. Claim materialization rejects every Stage whose required host mode or
capability does not match, even if runtime names otherwise match.

Multi-stage Agent completion has propose and finalize endpoints. Propose adds
`output_commit` and Attempt-ref proof, rejects malformed, unexpected-input, or
wrong-Worker identity, atomically consumes the Attempt lease, moves the Session
to durable `finalizing`, and returns one pending branch transition. Finalize
requires the same transition ID plus a ready
branch-manifest acknowledgement and atomically accepts output, updates the Track
head, succeeds the Session, and promotes its successor. Replays return the
stored phase result. The authenticated owner attests to object, ancestry,
cache-only resolution, and ref proof because the control plane holds no Git
objects. A one-stage Pipeline keeps the current completion contract and does
not require a local handoff ref. On a Pipeline-isolated host, its existing
completion endpoint additionally requires the idempotent private-Git
reattachment acknowledgement before terminal state.

`finalizing` work uses a separate short owner-only transition lease delivered
on the Worker's outbound poll. Normal Attempt lost-lease recovery sees the
durable state and cannot fail, cancel, or reassign the Session. Transition-lease
expiry only makes the same transition eligible for reconciliation by the Track
owner; it never creates another Agent Attempt. After the existing lost-Worker
threshold, an operator may explicitly abandon finalization through the bounded
control-plane action described in the lifecycle section.

Pipeline authoring and admission share a versioned canonical Agent prompt
formatter with the Worker. It validates the complete UTF-8 payload, including
maximum branch fields, typed Gate context, and trusted Pipeline context,
against the 72 KiB protocol limit. Save, schedule, admission, claim validation,
and Worker startup use the same formatter and bound.

The database gains Pipeline Graph-node, Graph-edge, and Track tables. Each
Track snapshots one Edge-state row per Edge before execution. A
`not_traversed` Edge row records its typed resolution cause and originating
Session so recovery can reproduce transitive skip classification. Sessions gain
Track and Stage-node foreign keys, kind, `waiting`, `skipped`, and
`observing_terminal_feedback` states, input and output commits, durable
`finalizing` state and transition lease, bounded typed output,
skip class, reason, and the Session whose outcome
caused the skip. Skip class distinguishes `conditional_success` from
`causal_failure` and cancellation. Tracks store owner Worker, immutable working
branch, branch owner IDs, expected local and remote heads, frozen base commit,
current accepted commit, published commit, pull-request identity, publication
authorization and per-feed eligibility floors, pending branch transition,
review outcome, terminal disposition and reason, terminal provider vector,
bounded pending-feedback snapshot, workspace health, and any rollback-only
transition tombstone. Publication authorizations store candidate and expected
remote commits, idempotency key, preceding Gate and outcome version, inherited
floor, normalization-digest version, successor Gate identity or terminal flag,
observed remote result, and terminal-observation phase, deadline, and lease. A Worker
repository-ref projection
stores only the latest complete inventory scan and its health metadata. The
current unique Run and repository constraint becomes a unique Track and Stage
constraint. Host-domain registration stores stable host ID, enrolled authority
public key, immutable mode, clean-domain acknowledgement, pinned control-plane
instance and command-key generation, rotation state, and provider-helper
provisioning state. Worker registration stores its host-domain foreign key,
original Worker identity, boot nonce, isolation self-test version and digest,
and attestation expiry. Attempt state records isolated Git mode and the durable
reattachment acknowledgement; local manifests retain the private Git path and
worktree-pointer phase without sending filesystem paths to the browser.

Runs and Tracks gain cancellation timestamps used as promotion fences.
Executions gain preparation-failure count and next-routing time. Gate Sessions
store next poll time, deadline, target and immediately preceding published
commits, immutable per-feed eligibility floor, observed cursors per provider
event feed, page continuations, deduplicated item
IDs, the last stable cross-feed boundary vector, chosen observation event,
actor permission verification, outcome version, bounded satisfaction snapshot,
and any
active Gate-check lease identity, owner, and expiry. A Session blocked only by
Pipeline or Stage concurrency uses one
canonical, non-actionable reason so the claim transaction can promote it
without operator intervention.

Each pending scheduled occurrence stores the complete immutable Pipeline
generation it will admit: Graph nodes, Edges, entry point, limits, repository
identities, execution settings, and scheduled instant. Editing a Pipeline only changes occurrences
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
Pipeline repositories. It creates one Graph with one Stage named `Execute`, no
Edges, and that Stage as entry for every current Task. The derived execution
bound is therefore one.
It uses the Task ID as Stage ID in its separate namespace. The Stage kind is
Agent, it preserves current prompt and runtime settings, and its requested token
ceiling is null. Current Run snapshots become one-node Pipeline Graph snapshots.
Each current Session receives a Track and points to that Stage. Historical
Sessions expose null input and output commits unless the old record already
proved a value; the UI labels them unavailable rather than inventing them.

Every existing pending or paused scheduled occurrence converts to a one-node
`Execute` Pipeline Graph generation from its own frozen Task snapshot, not the
current mutable Pipeline. Migration preserves due instant, retry state, health,
enabled or paused status, and association with an archived source.

Upgrade creates and validates the existing owner-only database backup before
migration starts. It refuses a name collision, unsupported schema, invalid
snapshot, or row it cannot map without changing the live database. The embedded
UI and API change in the same server build, so there is no mixed Task and
Pipeline operator surface.

Rollback is offline. The operator stops the new server and Workers and restores
the pre-upgrade backup with the supported restore command. Runs admitted after
upgrade are not present in that backup. The release guide states that data
boundary before upgrade.

The Worker creates local refs for multi-stage code-producing Agent Attempts with
this shape in its repository cache:

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
One scan has a random ID. Before taking the repository mutation lock, the
Worker checks its process registry. If an Agent for that repository is
preparing, running, or finishing, it defers the scan and marks the prior
projection visibly stale. Otherwise it takes the mutation lock and immediately
checks the process registry again. If activity won the race, the scanner
releases the lock without waiting and defers. It never waits for an Agent while
holding the mutation lock.

An Agent may enter `preparing` only by taking the same mutation lock and first
recording its active process state. Therefore either the scanner wins and no
Agent can start until its snapshot is durable, or the Agent wins and the
scanner's second check defers. With the lock and empty recheck established, the
Worker materializes the complete union of the local ref namespace and manifest
entries into an immutable, owner-only scan snapshot. Attempt preparation,
completion, ref publication, recovery, and cleanup use the same lock. The
Worker releases the lock after the snapshot is durable, and all pages of at
most 500 records read only that snapshot. Ref publication after the snapshot
appears in the next scan and cannot alter or disappear from the current pages.

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

Pipeline, Run, Track, Session, and Attempt IDs are random UUIDs created by the
control plane. Newly authored Stage-node and Edge IDs are client-generated
UUIDs that the server validates and permanently binds to one Pipeline. They
cannot be adopted by another Pipeline or reused after deletion. A migrated
Stage reuses its Pipeline ID in the Stage table so migration needs no unstable
generated value; its Graph has no Edge that needs an ID. IDs are never inferred
from names.

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
later rename. The Track stores branch owner Run and Track IDs plus the expected
local and, after publication, remote head. Before local branch creation, the
Worker writes and fsyncs an owner-only `pending` branch manifest containing
repository, Run, Track, branch, and expected initial commit. Under the
repository mutation lock it creates the ref by compare-and-swap from missing,
then atomically marks the manifest `ready` and fsyncs its directory. It sends
the prepared acknowledgement only after `ready` is durable.

Recovery finishes a pending manifest when the ref is missing or matches its
expected commit and marks repository health corrupt when it conflicts. A ref
without a matching ready manifest is unowned even when its name and commit
happen to match. Every accepted-head advance adds a pending transition with
control-plane transition ID, old head, and candidate head to this manifest,
fsyncs it before the ref CAS, and marks it ready before final completion.
Recovery completes a missing or matching CAS and replays the acknowledgement;
a mismatched ref fails the Session without promotion.

Retry and later Stages reuse an existing branch only when the
ready manifest owner matches this Track and its ref equals the Track's expected
accepted commit. Publication updates expected remote head through the separate
publication authorization and never rewrites branch ownership. A missing,
unowned, or conflicting branch fails without adoption, deletion, reset, or overwrite.
Provider records use the provider repository identity plus pull-request number
as durable identity; a changed title or URL does not create another record.

Before owner freeze, retry on the same Worker treats a ready manifest owned by
the same Track at the expected initial commit as idempotent preparation and
reuses it. Retry on another Worker creates its own local owned branch and never
adopts the first Worker's state. If the first Worker later returns after another
owner froze, its prepared acknowledgement is rejected and its unaccepted local
branch follows retained-Attempt cleanup.

## 7. Failure behavior and lifecycle

Admission writes the Run, every Track, and every planned Session in one
transaction. First-stage Sessions begin blocked or queued through existing
routing. Later Sessions begin waiting. A partial write creates no Run.

The first Worker resolves base branch and commit during preparation, then
freezes owner and input through the prepared endpoint before launching the
agent. For a private repository, default-branch resolution, initial clone, and a
missing-commit fetch use typed authority commands before local verification.
Retry reads that exact commit from local cache even if the remote base branch
moved; an absent object uses the same idempotent `fetch_commit` binding. A
prepared request replay is idempotent for the same lease and identity and
conflicts for a different identity.

For a multi-stage Agent, an exit of zero is provisional until commit and ref
creation succeed. Commit, ancestry, local ref, or read-back failure completes
the Attempt and Session as failed with a bounded actionable reason. The
worktree is retained. The same transaction skips every later waiting Session
with this failure as cause. No downstream Session becomes routeable. A
one-stage Agent uses its current result contract plus the isolated sanitizer
and reattachment contract when applicable; it has no handoff finalization.

Finalizing a ready branch transition records output identity, advances the
Track head, marks the Session succeeded, and promotes its direct successor in
one database transaction. With capacity
available, the successor becomes routeable to the owner Worker in that
transaction. Without capacity, it becomes blocked with the canonical
nonactionable concurrency reason. Existing claim materialization rechecks it
on each healthy owner Worker poll and promotes it when capacity exists.

A `finalizing` branch transition blocks another Attempt and remains owner-affine.
Owner recovery claims its transition lease, finishes or verifies the manifest
and ref CAS, and replays finalize. Expiry of the consumed Agent lease or any
transition lease cannot invoke normal lost-Attempt failure. Crash before propose
leaves no transition; crash after propose, manifest fsync, ref CAS, ready
transition, or finalize response restarts at that exact phase. A conflicting
ref fails the Session and skips successors. No successor observes a candidate
head.

If the owner passes the existing lost-Worker threshold, the Run detail offers
`Abandon finalization`. It requires explicit operator confirmation and is
available only while the Session is finalizing and the owner remains lost. One
database transaction fences the transition ID against finalize, records an
`abandoned_unreconciled` rollback-only tombstone, quarantines the Track
workspace, fails the Session with `owner_lost_during_finalize`, skips its
successors, and releases Pipeline and Stage capacity. If Run or Track
cancellation already committed, it records cancelled instead of failed. It
never accepts the candidate commit or updates the Track accepted head. The
confirmation says this failure cannot be retried and the operator must start a
new Run.

Finalize and abandonment are ordered by that transaction. A finalize that
committed first makes abandonment an idempotent no-op. Abandonment that commits
first makes every late finalize or ready acknowledgement reject permanently.
When the same Worker later returns, recovery may only verify the old head or
compare-and-swap the exact candidate back to the old head, then mark the
tombstone rolled back. Any other ref marks repository health corrupt. It cannot
finalize, reopen the Session, clear the quarantine, or start a successor.

An Open PR Action fails without advancing when branch publication is rejected,
the remote branch moved unexpectedly, `gh` is unauthenticated, provider rate
limits are exhausted, or the provider returns a conflicting pull request. A
retry reuses the same Track branch and provider lookup key. If branch
publication and PR creation succeeded but the response was lost, the Worker
finds and verifies that same pull request before reporting success. It never
creates a second PR for the Track. Success records the provider identity and
exact published commit. Open PR creates empty eligibility floors; Update PR
uses the carry-forward rules below.

A PR review Gate binds to the Track's latest successfully published commit and
an immutable per-feed eligibility floor. A success-like skip passes the prior
publication identity through unchanged. Open PR starts from empty feed floors
because the pull request does not yet exist. Each completed Gate stores its
stable second-sweep vector, including the normalized digest of every item at
that boundary. Update PR captures current feeds for audit and terminal
late-feedback detection, but a following Gate inherits the preceding Gate's
stable vector as its floor. It never replaces that floor with the later
publication-time feed ends. A new item or result-relevant mutation absent from
the inherited vector therefore remains eligible even when it was posted while
Address feedback was running, before the updated commit was published.

Approval still requires an explicit approved review for the new exact target
commit. Feedback after the inherited floor may be a review comment,
conversation comment, changes-requested review, or `COMMENTED` review with a
nonempty canonical body against either the new target or the immediately
preceding published commit; the latter remains actionable because the Address
feedback Agent could not have seen it. Older review rounds are excluded. An
empty or whitespace-only `COMMENTED` body is not feedback. The GitHub adapter
reads
reviews, pull-request review comments, and issue
comments through feeds with stable provider creation time and database ID. It
queries from each inclusive floor with overlap and deduplicates immutable
IDs. Mutable thread `isResolved` state is not an outcome input because GitHub
supplies no ordered resolution event or feed-wide mutation version.

One scan iteration performs two consecutive complete sweeps through GitHub's
three flat REST feeds: pull-request reviews, pull-request review comments, and
issue comments. Each sweep starts from the durable inclusive eligibility
floor, uses stable ascending provider order with overlap, reaches every
current page, and normalizes the decision-relevant fields before hashing them.
The review digest includes immutable ID, submission time, target commit ID,
author login, current review state, canonical body text, and every other bounded
field copied into the Gate result or feedback snapshot, so dismissal or a body
edit changes the digest without requiring a new review ID. Each comment digest
likewise includes immutable ID, creation time, author login, canonical body
text, and every bounded field copied into the result or snapshot. The flat
review-comment endpoint includes replies added to existing old threads.
Deletion, addition, or a result-relevant mutation therefore changes that feed's
digest. Canonical body text normalizes CRLF to LF and otherwise preserves UTF-8;
`COMMENTED` is nonempty only when Unicode whitespace trimming leaves content.

Each sweep also reads the current pull-request head. If either differs from the
Gate's target published commit, the Gate fails actionably with
`remote_diverged` and cannot accept an approval or feedback outcome. Normal
retry keeps the target and eligibility floor after the operator restores the
expected head or starts a new Run.

The adapter may freeze an outcome only when the second sweep produces the same
three digests as the first. The three successful second-sweep responses form an
explicit feed-vector boundary; no wall-clock comparison or claim of an atomic
GitHub snapshot is made. Within that vector, each item's normalized second read
is its component boundary for mutable state and snapshot text; the response
that completes the feed's final page is its component boundary for membership.
A mutation after an item's second read, or an item first observable after its
feed's final-page response, belongs to a later explicit review round or Run,
even if another feed is still being confirmed or the control-plane result
transaction commits afterward.

If a request fails or either sweep differs, the adapter discards the
iteration's ephemeral candidates and restarts from the durable inclusive
floors on the next scheduled poll without advancing a cursor, continuation,
or deduplicated ID. A lease performs exactly two sweeps and never starts a
third. If the feeds do not match, the Gate remains waiting with
`review_boundary_moving` and retries after backoff.

The adapter first separates new immutable IDs from normalized mutations of
items already present in the eligibility floor. GitHub does not expose one
trustworthy ordered mutation event across all three feeds. Therefore, when a
floor item's digest changed and its current representation qualifies as
feedback, the Worker authorizes its actor and conservatively chooses
`feedback_requested` ahead of every unchanged timestamped candidate. An
unverifiable actor follows the normal no-advance waiting rule; a verified actor
without write permission is discarded. This covers an old `COMMENTED` review
or comment whose body is edited after a later approval.
Factory never assigns that mutation the item's older creation time or invents a
local/provider timestamp comparison.

Only when no authorized qualifying mutation exists does the adapter order new
immutable-ID candidates by provider creation time. Within one feed, provider
database ID breaks a timestamp tie. Database IDs from different feeds are never
compared as chronology. When the newest timestamp contains candidates from
different feeds, one shared outcome is accepted; conflicting outcomes resolve
conservatively to `feedback_requested`. A review submission qualifies
for approval only when its provider commit ID equals the target commit. A
changes-requested review, or a `COMMENTED` review with a nonempty canonical
body, qualifies for feedback when it targets the current or immediately
preceding publication and either its ID is absent from the inherited eligibility
floor or its stored normalized digest changed. Review and conversation comments
qualify under the same new-ID or changed-digest rule.
Factory never compares its local publication time with a provider creation
time. Provider timestamps order only new-ID candidates after feed identity
establishes eligibility.

When no authorized qualifying mutation chose feedback, the Worker evaluates
new-ID timestamp groups from newest to oldest. For one group it
checks each distinct actor through GitHub's repository-permission endpoint and
records actor login, provider permission, and verification time as typed
metadata. Verified actors without write permission are discarded. If the group
then has an authorized candidate, it is the decisive group and older groups are
not permission-checked because they cannot supersede it. Conflicting authorized
outcomes inside that group resolve conservatively to feedback. If the group has
no authorized candidate and every lookup was conclusive, evaluation continues
to the next older group.

Missing access, rate limiting, or an unverifiable response for an actor in the
current group leaves the Gate waiting because that event could still affect the
decisive outcome. That result commits only bounded health and backoff state: no
observed feed cursor, page continuation, or deduplicated item ID advances past
the unresolved group, so the next inclusive scan retries its authorization. An
unresolved actor in a strictly older group is never consulted after a newer
decisive group is authorized. The control plane never infers eligibility from
author association or event text. A
changes-requested review, nonempty `COMMENTED` review, or comment from an
eligible actor yields
`feedback_requested`.
An explicit approved review yields `approved` and is the observation boundary
for that Gate only after same-timestamp candidates resolve to approval. Earlier
feedback is superseded by that explicit approval. An event ordered after it
belongs to another explicit review round or later Run;
Factory does not claim a live snapshot of mutable GitHub thread state. More
than 2,000 events or 4 MiB of scan metadata remains waiting with
`review_state_too_large` and cannot choose an outcome.

Provider unavailability or rate limiting updates Gate health and uses bounded
exponential backoff without consuming an execution slot. A complete authorized
result transaction advances each observed feed cursor and page continuation,
then either remains waiting or freezes `approved` or `feedback_requested` and
promotes or conditionally skips the complete block. A scan with an unresolved
permission check follows the no-advance rule above.
Deadline expiry marks the Gate failed and skips successors. Manual retry keeps
the target commit and eligibility floor, records a new outcome version, clears the old
lease and failure, and sets a new deadline to `retry time + frozen timeout`.
The retry transaction conflicts with a concurrent accepted result or
cancellation. V1 has no deadline-extension endpoint or `keep_waiting` policy.

An Address feedback Agent starts from the Track's published commit and the
Gate's frozen snapshot. If the Gate outcome is `approved`, Address feedback and
Update PR are conditionally skipped without an Attempt. If feedback was
requested, the accepted Agent output promotes only Update PR. That Action
fast-forwards the remote branch with an expected-old-head check and promotes
later work only after durable publication succeeds. With a following review
Gate, the publication result atomically stores the preceding Gate boundary as
that successor's eligibility floor; no publication-time observation can replace
it.

With no successor Gate, Update PR publishes or reconciles the frozen candidate
first, enters `observing_terminal_feedback`, and releases its execution slot.
The owner then receives a fenced observation lease and applies the Gate's exact
two-sweep digest, actor-permission, boundedness, and backoff protocol against the
preceding Gate floor. A conclusive stable result with no eligible late feedback
marks the Action and Track succeeded. Eligible late feedback atomically stores
the stable vector, bounded pending snapshot, terminal reason
`feedback_arrived_during_address`, Action success, and Track disposition
`partial`. Missing or unverifiable actor permission leaves the Action observing
and cannot permit full success or advance observation state. Events first
observable after the stable post-publication second sweep belong to a later Run.

If no conclusive stable result commits within one hour of verified publication,
the Action and Track fail actionably with
`terminal_feedback_observation_timeout`. The pull request remains published and
the UI says `PR updated; feedback check timed out`, links the PR, and offers
`Retry feedback check` or cancellation. Retry never republishes: one
transaction verifies the frozen authorization, unchanged published commit, and
cancellation fence, clears the observation failure and lease, preserves the
preceding Gate floor and digest version, and sets a new deadline to
`retry time + 1 hour`. A moved remote head fails retry with
`remote_diverged`. Concurrent retry, accepted observation result, or
cancellation is ordered by that transaction and cannot create two live leases.

If the remote branch moved
independently, Update PR fails actionably with `remote_diverged` and does not
overwrite it. After external reconciliation, normal failed-Session retry reuses
the same authorization identity and frozen candidate, or the operator cancels
and starts a new Run.

An idempotent completion replay returns stored completion. An expired lease
cannot publish multi-stage completion even if the Worker created a local ref.
That orphan ref is reported by reconciliation but does not advance the Track or
conflict with a later Attempt. One-stage replay uses its existing result identity
plus sanitized-reattachment acknowledgement and creates no ref.

When a Session fails, every later waiting Session in that Track becomes skipped
and records the failed Session as cause. This is distinct from a success-like
typed conditional skip. Retrying the failed Session reopens the
Run, returns only never-started successors skipped by that failure to waiting,
and uses the same input. A successor with an Attempt is never reset. The retry
transaction rejects `owner_lost_during_finalize` and every Session or Track
with a rollback-only transition tombstone. Neither automatic nor manual retry
can reopen abandoned work, even after local rollback completes; the operator
starts a new Run.

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
cannot promote a successor.

Branch-transition proposal and cancellation are ordered. Cancellation first
rejects propose. Proposal first records cancellation pending until owner
recovery completes or verifies the local CAS, compare-and-swaps candidate back
to the old head, marks the transition abandoned, and acknowledges cancellation.
After the lost-Worker threshold, the operator may instead confirm lost-owner
abandonment; this terminalizes cancellation with the rollback-only tombstone
and releases capacity without waiting for the Worker. A conflicting ref marks
repository health corrupt and requires repair; it never promotes. Publication
authorization and cancellation are
ordered in the database. If cancellation commits first, authorization is
rejected and the Worker cannot begin the remote write. If authorization commits
first, cancellation records that one publication may already be in flight. The
Worker may report or reconcile only that frozen authorized candidate after the
fence. The server records whether the remote side effect occurred, terminalizes
the Action and Track as cancelled, and never promotes a successor. Response
loss retains the authorization for read-only reconciliation; it cannot
authorize another candidate. The UI says `cancelled after publication was
authorised` until the remote state is known.

Terminal Update PR observation is ordered by a second transaction fence. If its
fenced result commits first, it atomically stores the remote publication,
stable vector, pending snapshot, reason, and succeeded or partial Track
disposition; later cancellation cannot rewrite that terminal disposition. If
cancellation commits first after publication authorization, a late publication
or observation result may record remote and provider evidence but must
terminalize the Action and Track as cancelled, never succeeded or partial, and
cannot promote. Response loss replays the frozen authorization and terminal
observation phase; restart derives no disposition from successful Sessions when
the durable Track disposition is already present.

If a preparing or running Attempt instead loses its lease, recovery
terminalizes the Session as cancelled because the committed cancellation fence
is authoritative, not failed. A finalizing Session is different: its Agent
lease was consumed by proposal, so the normal lost-Attempt sweeper ignores it.
The owner must claim a transition lease and finish the stored cancellation
rollback. A cancelled Run or Track cannot reopen through Session retry; the
operator starts a new Pipeline Run.

`waiting` is neither claimable nor terminal. `skipped` is terminal. A Run is
active while any Session is blocked, queued, preparing, running, finalizing,
waiting, or observing terminal feedback. The canonical concurrency block counts as active but not
actionable. A Run needs attention when another actionable blocked or failed
Session exists. `observing_terminal_feedback` is active but holds no execution
slot. A Run becomes terminal when every Session is succeeded, failed,
cancelled, or skipped. A Track normally derives the matching terminal
disposition from its Sessions; terminal Update PR may override an otherwise
successful Track to `partial` for frozen late feedback. Terminal Run aggregate
state uses these ordered predicates:

1. `succeeded` when every Track has the `succeeded` disposition.
2. `partial` when at least one Track is `succeeded` or `partial` and the Run is
   not fully succeeded.
3. `failed` when no Track is succeeded or partial and at least one Session failed.
4. `cancelled` when no Track is succeeded or partial, no Session failed, and a Run or Track
   cancellation timestamp exists.

An intermediate commit does not complete a Track. Cancellation before Stage 1
and cancellation after an intermediate Stage are both `cancelled`; completed
plus cancelled or failed Tracks are `partial`; failed plus cancelled Tracks
with no completed Track are `failed`.

If the owner Worker is offline, every later or retried Session remains blocked
with `Waiting for Track owner Worker <name>.` This is operational, not
actionable, while the Worker is registered and within its recovery window. It
becomes actionable after the existing lost-Worker threshold. Returning the
same Worker resumes claims. A finalizing Session instead exposes the explicit
abandonment action after that threshold because its local branch may already
contain an unaccepted candidate.

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
are not a general code sandbox, so every multi-stage Agent requires an enforced
isolation boundary. V1 uses one backend: rootless Podman 5 or newer with a
digest-pinned Factory Agent image. A security domain is the complete OS host or
VM plus every socket that can administer it. Linux uses a dedicated host or
admin-owned VM. On macOS, the entire Pipeline host stack runs inside one
admin-provisioned Linux VM: `factory-hostd`, every Worker, repository caches,
locks, Attempt worktrees, operation brokers, and rootless Podman Agent
containers. The control plane may remain on macOS because it holds no Git or
provider credential and Workers initiate its existing outbound protocol.

The Linux VM has no writable macOS filesystem mount. Its disk, console, SSH,
Podman, and lifecycle channels are root-owned administration surfaces and are
unavailable to the Worker and Agent users. Retained-worktree links are VM-scoped
and open through the authenticated admin bridge, never a shared host path. A VM
administered through a socket reachable from a legacy Agent is not an isolated
domain. The execution profile freezes image digest and runtime adapter. Worker
readiness pulls the image, launches a self-test container, and refuses
`agent_isolation` if any required process is not Linux-resident or the runtime,
image, user namespace, mount, or management-channel policy differs.

V1 installs one host-level authority, `factory-hostd`, before any Worker can
register for new claims. It owns a stable signing key, host-domain ID, mode, and
exclusive host lock in root-protected state. Its root-owned Unix socket checks
peer credentials and admits only the installed Worker supervisor and admin
provisioning client. It chooses one immutable mode for the whole domain:

- `legacy` may run current unsandboxed one-stage profiles under their existing
  trust contract. It rejects multi-stage claims, provider Stages, Factory
  provider-helper installation, and Factory-managed credential provisioning.
- `pipeline_isolated` requires a clean dedicated domain and launches every
  Agent, including one-stage Agents, through the isolated container profile. It
  may advertise multi-stage capability and, after the stronger provider
  self-test, receive provider-helper credentials.

The authority fsyncs its mode before enabling any Worker, signs short-lived
Worker attestations, and issues the same mode to every config and data directory
on the domain. It rejects a mixed-mode sibling in either launch order, an
in-place mode change, a second authority, and an unverified binary. The control
plane and authority mutually enroll through an operator-authenticated one-time
flow. The control plane pins the host public key. The authority pins the control
plane instance ID, command-signing public key, and key generation in its
root-protected state. Neither side accepts a key supplied by a normal request.
The control plane requires host attestation even though the existing loopback
Worker API is unauthenticated. A spoofed Worker ID or registration cannot invent
another host ID, mode, isolation capability, or command signer.

Normal control-plane key rotation is two-phase. A rotation certificate signed
by the currently pinned key names the next key and generation; the authority
persists it and returns a signed acknowledgement before the control plane uses
it. Both keys verify only during a bounded ten-minute overlap, after which the
old generation is rejected. Response loss replays the same rotation ID. If the
old key is unavailable or suspected compromised, the operator drains the host,
revokes provider credentials, and repeats mutual enrollment locally; no remote
request can force recovery rotation. A command from another server or key
generation is rejected and marks provider capability unhealthy.

Moving a legacy domain into Pipeline service requires the operator to reimage
the host or VM, remove every Worker and authority data directory, create a new
host-domain key, and enroll it as a new domain. Pipeline initialization requires
an explicit clean-domain acknowledgement. Factory does not claim to detect
persistence on an unreimaged host; the administrative reimage is part of the
operator boundary. Migration initializes the host authority as `legacy` before
any existing Worker can receive another claim. Existing one-stage behavior and
operator-managed ambient credentials remain the current operator risk, but no
Worker on that host can enter the Pipeline capability pool.

The Worker builds a private Agent Git directory with a writable index, refs,
config, and object directory plus a read-only alternate to the repository cache
object directory. The container uses `--userns=keep-id` and mounts only the
Attempt files at `/workspace` read-write, the private Git directory at
`/factory/git` read-write, and cache objects at `/factory/base-objects`
read-only. Its `.git` file points to `/factory/git`; no host repository config,
refs, home, SSH agent, provider helper, Podman socket, or Worker socket is
mounted. Fixed Git configuration uses empty system and global files, disables
prompts and credential helpers, and removes credentialed remotes. The runtime
adapter mounts only model-runtime authentication from an Attempt-scoped
read-only secret into container tmpfs and destroys it at exit. Agent `gh` and
`git push` therefore have no provider credential or writable remote.

In a `pipeline_isolated` domain, the small privileged authority retains the host
signing key, immutable mode, and a GitHub App installation-key reference;
Worker daemons, operation brokers, and containers retain none of them. It
accepts only a signed
control-plane command envelope bound to command ID, signing-key generation,
operation, host, Worker, repository provider identity, allowed remote identity,
Track or preparation lease, local cache identity, ref, expected head, and
expiry. Used command IDs are durably deduplicated. Its fixed typed operations
are `resolve_default_branch`, `clone_cache`, `fetch_commit`,
`publish_track_branch`, `open_pull_request`, `update_pull_request`, and
`read_review_feeds`.

The privileged authority never launches Git or `gh` with its own UID and never
parses repository or provider payloads. After authorization it mints a shortest-
lived repository-scoped installation token with only the permissions required
by that operation, then starts a separate `factory-broker` process as the
dedicated unprivileged broker identity. Before exec it clears supplementary
groups and inherited file descriptors, sets `no_new_privs`, applies an
operation-specific syscall and network policy, and creates a mount namespace
containing read-only trusted binaries and certificates, private temporary space,
and only the registered cache target needed by that operation. Authority state,
admin and authority sockets, Worker roots, other caches, and host home are
absent. The one-operation token arrives through a sealed private descriptor and
is destroyed with the process.

The unprivileged broker runs Git and `gh` with empty home, fixed system and
global config, disabled hooks, helpers, includes, alternates, and prompts, and
bounded input, output, time, memory, and process counts. It returns a bounded
typed result over a one-shot pipe. The authority validates only that fixed
result schema and exit status before recording the command result; it never
loads child Git config or provider objects. A broker escape has neither UID nor
filesystem access to authority keys, credential minting, mode state, or
administration sockets.

Source operations map repository and cache identities through authority-owned
registration; they accept no caller path or arbitrary URL. Clone creates a
quarantined bare cache below the registered Worker repository root. Fetch writes
through the same cross-process repository mutation lock and installs only into
that cache. Credentials are injected ephemerally into the broker process and are
absent from remotes, Git config, logs, errors, and resulting objects. Default-
branch reads return one bounded typed name. Response loss replays the command ID
and returns the recorded result; a different operation or binding conflicts.

Remote write and provider-read operations likewise accept no arbitrary command,
repository, ref, or URL. The authority socket, credentials, and broker code are
never mounted or exposed to a container. It provisions credentials only in
`pipeline_isolated` mode after clean-domain enrollment and the real container
self-test prove workspace Git operation, model-runtime startup, provider-
mutation denial, mount read-only behavior, and cleanup. Only then may a Worker
on that domain advertise source access or `provider_credential_isolation`.
Existing one-stage Pipelines keep their completion contract: legacy Workers may
use the current trust model, while Pipeline Workers use the isolated adapter and
authority source broker only.

Only the authenticated owner Worker with the active Attempt lease may report a
multi-stage output commit. The server validates IDs, Worker identity, commit
format, frozen node eligibility, input-commit equality, and cancellation fences. For that
handoff the Worker disables hooks for its own automatic commit. It verifies
objects and ancestry in a fresh Git directory with `GIT_NO_REPLACE_OBJECTS`, no
graft file, no repository config, and fixed empty system and global config. That directory reads a
manifest-frozen, copy-verified object snapshot containing only regular canonical
loose objects and pack files from the cache and private Agent object directory.
It contains no `objects/info` directory, alternate
metadata, or symlink. Pack checksums, strict connectivity, and a canonical
rehash of every object reachable from input and output must pass before lineage
is accepted. Only verified reachable objects are installed canonically into the
cache, and cache-only resolution must pass before the local ref and branch
transition are acknowledged. It does not trust agent output to name a successor
commit. Isolated one-stage completion performs no ancestry or local-ref proof;
it applies the separate cryptographic HEAD, object-closure, ref-shape, index,
and sanitized-directory validation defined above.

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

Multi-stage Pipelines require one `pipeline_isolated` persistent Worker with
healthy `agent_isolation` that can prepare the repository for the Track.
Private source preparation additionally requires its host authority's healthy
`source_broker` capability. Pull-request Stages require that broker plus
`provider_credential_isolation` with repository access. One-stage Pipelines keep
current behavior only on a compatible domain: an unsandboxed profile routes to
`legacy`, while an isolated profile may route to `pipeline_isolated` and uses
the same source broker for private repositories. A repository unavailable to a
Worker is handled by existing routing before owner freeze and by owner-affinity
failure afterward.

Local Pipeline refs keep accepted commits reachable and are not deleted
automatically in V1. Repository detail reports count and age. Retention and
safe cleanup require a later design because deleting a ref while a Run or
retained Attempt depends on it would lose evidence.

At most 500 Sessions are planned per Run, 100 hold execution slots, 20 Stages
are stored per Pipeline, and prompts total at most 256 KiB. One feedback
snapshot contains at most 100 items and 256 KiB after UTF-8 encoding. Its
canonical successor view is at most 32 KiB and records omitted item and byte
counts plus provider identities for explicit lookup. One Gate-check lease
performs exactly two complete sweeps. Each sweep reads at most 2,000 unique
items and 4 MiB of normalized event data. Because GitHub returns at most 100
items per page and all three feeds require a response even when empty, the
feed-page limit is 22 per sweep. One lease therefore permits
at most 44 feed page reads, two pull-request head reads, 4,000 item
representations, and 8 MiB across verification.
The second read of an unchanged item counts against the verification-read
budget but not the per-sweep unique-item limit. Ephemeral data is discarded
after the result; persisted event-scan metadata remains capped at 2,000 unique
items and 4 MiB. A 2,001st unique item or oversized sweep remains waiting with
`review_state_too_large` and cannot choose an outcome. Gate polls start at the
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
  and fake-cloud profiles without local Stage handoff fields or no-change ref.
- `AC-13`: In a multi-stage handoff, before a successful completion proposal,
  response loss or lease expiry after local-ref creation does not block a new
  Attempt; only an output accepted through finalization feeds the successor.
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
- `AC-17`: A multi-stage reset or replacement history cannot succeed because
  the handoff output commit must descend from the frozen input. One-stage
  history is governed by `AC-54` and advances no shared Track state.
- `AC-18`: During multi-stage handoff, an untracked file is never added
  automatically; completion fails until the agent stages, commits, ignores, or
  removes it.
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
  Stage and the required security domain, and owner capability loss later
  blocks visibly without rerouting.
- `AC-24`: In a multi-stage handoff, replacement refs, grafts, cache-level local
  or HTTP alternates, symlinked object storage, or agent-written Git config
  cannot make an unrelated or externally stored output pass ancestry or local-
  ref verification. One-stage sanitization may retain an unrelated valid HEAD
  only through the self-contained proof in `AC-54`.
- `AC-25`: A scheduled occurrence with no complete owner candidate keeps its
  frozen snapshot and admits successfully after the eligible fleet returns.
- `AC-26`: Repository detail shows the latest complete local-ref count, oldest
  known age, unknown-age count, orphan, incomplete, and corrupt counts, scan
  health, and freshness. A publication during paginated reporting appears only
  in the next immutable scan and cannot make the current projection partial.
  A scan racing with Agent start or finish either snapshots before the Agent
  enters its active state or releases the mutation lock and defers; it never
  waits for Agent completion while holding that lock.
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
  `feedback_requested` outcome without losing pre-poll feedback. A nonempty
  `COMMENTED` review body is feedback; an empty or whitespace-only one is not.
- `AC-35`: For `feedback_requested`, an Address feedback Agent receives the
  frozen untrusted feedback snapshot, starts at the published Track commit, and
  promotes only Update PR after accepted success. Update PR durably
  fast-forwards the same remote branch before later work advances and carries
  the preceding Gate boundary into any following Gate. For `approved`, both
  conditional Stages are skipped without an Attempt.
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
  through frozen inclusive eligibility floors and provider ID deduplication.
  An authorized changed-digest floor item that currently qualifies as feedback
  takes conservative precedence because it has no trustworthy mutation event
  time. Otherwise the newest qualifying new-ID timestamp, with conservative
  cross-feed tie resolution, determines the outcome at the Gate's explicit
  observation boundary; later events belong
  to a later review round or Run. Every unseen provider ID after the frozen
  floor is eligible regardless of Factory/provider clock skew. Provider time
  orders only those unseen candidates, and simultaneous feedback prevents
  approval.
- `AC-40`: Approval conditionally skips a complete feedback block, passes
  through the prior publication identity, and lets a later explicit review
  round bind that identity. A feedback path binds the newly updated commit.
- `AC-41`: A multi-page review event scan chooses the newest qualifying event
  by the documented provider order only after two consecutive complete,
  bounded sweeps of the flat reviews, review-comments, and issue-comments feeds
  produce identical normalized content digests and observe the target head.
  Review state participates in its digest. A changed digest discards the
  iteration without durable scan progress; the next scheduled poll starts two
  fresh sweeps. Each normalized item's second read is its mutable-value boundary
  and each feed's final-page response is its membership boundary. Failure to
  stabilize remains visibly waiting with `review_boundary_moving`.
  Same-timestamp cross-feed candidates with conflicting outcomes resolve to
  feedback requested, never approval. Overflow remains visibly waiting with
  `review_state_too_large`; mutable thread-resolution state is never presented
  as snapshot proof.
- `AC-42`: A public user without write permission cannot approve a Gate or
  trigger Address feedback. A GitHub-confirmed write, maintain, or admin actor
  can. Permission lookup failure leaves the Gate waiting without advancing any
  feed cursor, continuation, or deduplicated ID past an unresolved event in the
  newest potentially decisive timestamp group; a later successful lookup can
  still choose it. An unresolved actor on a strictly older event does not block
  a newer authorized decisive group.
- `AC-43`: Object verification snapshots a bounded manifest without waiting for
  unrelated repository agents. A source changed during copy retries visibly
  and can never produce an accepted partial snapshot.
- `AC-44`: A PR head different from the Gate target fails with
  `remote_diverged`; an approval for the stale target cannot advance the Track.
- `AC-45`: Snapshot file, byte, time, retry, free-space, and Worker reservation
  limits fail before acceptance with an actionable reason. Success, failure,
  crash, and restart release the temporary directory and reservation without
  removing retained Attempt evidence.
- `AC-46`: First preparation rejects an unowned branch collision. Retry and a
  later Stage reuse the owned Track branch only at the stored expected head;
  missing or conflicting state is never adopted, reset, or overwritten. Crash
  recovery completes only the matching write-ahead manifest and ref pair.
- `AC-47`: In a provider Pipeline, Agent attempts to mutate through `git push`,
  `gh`, credential helpers, an SSH agent, or the typed helper are denied. Open
  PR, Update PR, and Gate operations still succeed through the authority-owned
  lease-bound provider broker. The Agent can edit and commit through the specified
  rootless Podman Git mount layout without host repository or provider access.
- `AC-48`: A staged new file and Agent-created commit in the private container
  Git directory are validated with base objects, imported as canonical reachable
  cache objects, resolved without an alternate, and preserved in the successor.
- `AC-49`: Crash or response loss at every propose, transition-manifest, branch
  CAS, ready, and finalize boundary recovers one accepted head or a cancelled
  rollback. No successor starts from a candidate before finalize.
- `AC-50`: Agent-lease and transition-lease expiry after proposal leave the
  Session finalizing and owner-affine at every transition phase. Reconciliation
  is reissued only to that owner and reaches finalize, cancellation rollback,
  or a proven conflict without an early successor or replacement Attempt.
- `AC-51`: After permanent owner loss before or after branch CAS, explicit
  abandonment rejects every late finalize, releases concurrency, terminalizes
  the Track without accepting the candidate, and retains a rollback-only
  tombstone. A returning owner can restore the exact old head or report a
  conflict, but manual and automatic retry can never revive or promote the
  abandoned Session.
- `AC-52`: A multi-stage Pipeline without provider Stages still runs every
  Agent in the isolated container and cannot write the host cache, manifests,
  refs, sibling worktrees, home, or Worker sockets. One signed host-domain mode
  applies across every Worker config and data directory. A legacy host rejects
  all multi-stage and provider work and cannot receive Factory-managed helper
  credentials. A Pipeline host rejects every unsandboxed claim, including
  one-stage work. Mixed-mode siblings, spoofed loopback registration, in-place
  mode changes, and credential provisioning after legacy execution are rejected
  in both launch orders. Pipeline enrollment requires a reimaged domain, new
  host key, and operator-authenticated authority registration.
- `AC-53`: A Pipeline host prepares a private repository through typed
  authority default-branch, clone, and fetch operations. Credentials never
  enter Worker or Agent state, targets cannot escape the registered cache, and
  response loss or retry returns one recorded bounded result without duplicate
  or conflicting cache mutation.
- `AC-54`: An isolated one-stage Attempt preserves committed, staged-new,
  dirty, or unchanged Git state in a sanitized self-contained retained worktree
  without a handoff ref or cache import. Malicious config, hooks, helpers,
  includes, alternates, and extra refs remain untrusted evidence and cannot
  affect ordinary host Git. Reset, detached, merged, rewritten, and unrelated
  valid HEADs are retained and visibly classified without an ancestry failure.
  Completion response loss and restart recover one inspectable worktree, while
  terminal cleanup removes worktree and both Git directories atomically.
- `AC-55`: Mutual host enrollment pins the control-plane command key. Forged,
  replayed, expired, wrong-server, and wrong-generation envelopes fail before
  broker execution. Normal two-phase key rotation tolerates response loss and
  rejects the old generation after its bounded overlap.
- `AC-56`: Every Git and `gh` child runs under the unprivileged broker UID with
  no authority state, admin socket, host key, other cache, Worker root, or home
  access. Hostile repository config, hooks, helpers, includes, alternates, and
  child attempts to read authority files or sockets fail while the single typed
  operation still succeeds against its registered target.
- `AC-57`: On macOS, a real Pipeline Run proves every trusted and Agent process
  and all mutable repository state are inside the enrolled Linux VM. Readiness
  rejects a writable macOS mount, a host-resident Worker or broker, and a
  management channel reachable from the Agent identity. VM restart preserves
  authority identity, locks, cache, and exact Track recovery.
- `AC-58`: Creating and running a Graph with one Agent node, no Edges, that node
  as entry produces the current one-stage
  persistent or fake-cloud lifecycle with one agent start and no handoff ref.
- `AC-59`: Save and preview reject a missing or foreign entry node, duplicate or
  dangling Edge, unreachable node, cycle, node or Edge limit overflow,
  unsupported activation, arbitrary condition, concurrent code path, or code
  fan-in before a Pipeline generation is created. Create and preview accept
  client-generated node and Edge UUIDs and resolve entry and endpoints without
  prior persistence; update preserves existing IDs, accepts fresh IDs for new
  objects, and rejects reused deleted or cross-Pipeline IDs.
- `AC-60`: A Track persists every Edge transition once. Crash or response loss
  before and after each source outcome, Edge decision, node promotion, and skip
  propagation recovers the same eligible, waiting, and unreachable nodes
  without executing a node twice.
- `AC-61`: `activation: all` waits for every incoming Edge to traverse.
  `activation: any` in the structured review reconvergence waits until all
  mutually exclusive incoming Edges resolve and then starts exactly once when
  one traversed. When none traverses, it records one unreachable skip.
- `AC-62`: The review macro stores a conditional `feedback_requested` Edge.
  Approval leaves that Edge not traversed and skips Address feedback and Update
  PR. Feedback traverses it and runs those nodes once with the frozen Gate
  output. Neither path can make both branches eligible. In a terminal review
  block, the transitive approval skips complete the Track successfully. In a
  nonterminal block, those skips resolve before the approved Edge and the
  feedback path reconverge at the following `activation: any` node, which runs
  exactly once.
- `AC-63`: Preview derives guaranteed and conditional-maximum starts, token
  ceilings, reachable nodes, and longest path from the submitted Graph. The
  values are identical after save and in the admitted Run snapshot.
- `AC-64`: V1 never holds execution slots for two nodes in the same Track.
  Pipeline and Stage concurrency still allow work from different Tracks, Gate
  waiting consumes no slot, and each Gate timeout retains its existing terminal
  and retry behavior.
- `AC-65`: Feedback posted or edited after a Gate's stable vector and before
  Update PR publication remains absent from the inherited floor of a following
  Gate and can choose that Gate's outcome. Without a following Gate, Update PR
  publishes the accepted commit but terminalizes the Track as `partial` with
  `feedback_arrived_during_address` and a bounded pending snapshot, never as
  fully successful.
- `AC-66`: Terminal Update PR observes feedback only after publication is
  verified. Its fenced two-sweep result atomically persists the stable vector,
  permission proofs, pending snapshot, reason, and Track disposition. An
  inconclusive actor remains observing without full success. Restart and
  response-loss replay preserve that phase, and cancellation-first prevents a
  later success or partial disposition while result-first remains terminal. A
  one-hour deadline expires to visible actionable failure; retry preserves the
  publication and repeats only observation with a fresh deadline.

## 10. Test approach

Store tests prove atomic admission, independent Track promotion, owner freeze,
owner-only routing, aggregate state, retry, cancellation races, generation
snapshots, typed Graph and Stage validation, entry and reachability checks,
durable Edge traversal, explicit activation semantics, Gate polling and
outcomes, conditional promotion and unreachable skip, concurrency-block promotion, schedule
snapshots, structural cost estimates, limits, and every terminal predicate.
Review-route fixtures cover both a terminal approval skip and a nonterminal
approved-path reconvergence, including crash recovery after each propagated
`conditional_success` Edge decision. They also prove failure and cancellation
causes never become success-like.
They prove the persisted portions of `INV-1` through `INV-46`;
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
Container Git tests stage a new file and create a commit in the private Git
directory, then prove union validation imports only its reachable canonical
objects, resolves the commit from cache alone, and gives the complete checkout
to the successor for `AC-48`.
Isolated one-stage tests finish with an Agent-created commit, a staged new file,
dirty tracked files, no change, reset history, detached HEAD, a merge commit,
and a valid unrelated commit. They verify the recorded HEAD shape and input
relation without imposing ancestry. They lose the completion response and
restart before and after pointer reattachment, then prove the retained link
opens the reconstructed index, trusted ref, complete object closure, and
workspace without a handoff ref, cache import, or alternate. Fixtures add
malicious config includes, hooks, aliases, credential helpers, replacement refs,
and local and HTTP alternates; ordinary host `git status` and `git log` remain
correct after the shared cache is unavailable and execute none of them. Cleanup
crash tests prove the worktree and both Git directories remain together or are
all removed for `AC-54`.
Capability-intersection tests reject admission when no one Worker can run every
Stage in the required security domain and block an owner whose later runtime
becomes unhealthy for `AC-23`.
Branch preparation tests reject unowned collisions and mismatched heads, then
reuse an owned matching branch across preparation loss, retry, and later Stages
for `AC-46`. A pre-freeze lost response retries idempotently on the same Worker;
a retry that selects another Worker never adopts the first Worker's branch.
Crash injection before and after pending-manifest fsync, ref compare-and-swap,
ready transition, and prepared acknowledgement proves recovery finishes only
matching owned state and never adopts a ref-only branch.
Accepted-head tests crash before and after completion propose, transition
manifest fsync, branch CAS, ready acknowledgement, and finalize commit and
response. They prove exact recovery, cancellation rollback, and no early
successor for `AC-49`. At every phase they expire both the consumed Agent lease
and repeated transition leases, then prove the lost-Attempt sweeper leaves the
Session finalizing, reconciliation stays owner-only, and no replacement Attempt
or successor appears before the terminal result for `AC-50`. Permanent-owner-
loss tests abandon before and after branch CAS, with and without a cancellation
fence, and prove terminal aggregate state, immediate slot release, permanent
finalize rejection, rollback-only recovery, and conflict quarantine for
`AC-51`. Retry tests before and after rollback-only reconciliation prove both
manual and automatic paths return the permanent failure and create no Attempt.

Recovery tests restart the owner Worker between Stages and prove it recovers
repository cache, local refs, retained worktrees, and manifest state before
claiming the successor. Offline-owner tests prove no other Worker may claim and
that the lost threshold changes the reason from operational to actionable.
Crash-injection tests stop before and after each manifest write, directory
fsync, ref compare-and-swap, and ready transition. They prove recovery and the
union inventory contract for `AC-27`, including agent-created ref-only state.
Inventory tests paginate an immutable snapshot while publishing another ref,
prove the current projection stays internally complete, and prove the next scan
contains the new ref for `AC-26`. Lock-order tests pause an Agent before start,
while running, and while finishing, then race a scan at both activity checks.
They prove the scanner either snapshots first or releases and defers, and that
Agent completion never waits behind a scanner waiting for that same Agent.

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
approval, changes-requested, nonempty `COMMENTED`, and empty `COMMENTED` review
events, qualifying review and conversation comments, authorized and
unauthorized public actors, permission lookup failure,
empty polls, events during publication response loss, inclusive
overlap and provider-ID deduplication, multi-page feed completion, within-feed
ID ordering, skewed Factory/provider clocks, cross-feed timestamp ties
with matching and conflicting outcomes, explicit event ordering before and
after approval, bounded snapshots, rate-limit backoff, retry deadlines,
untrusted-text prompt framing, response loss before Update PR
completion, stale approved commit with a moved PR head, remote divergence, and
same-branch feedback updates for `AC-33` through `AC-44`. They prove no command can push the base branch, tags,
or an operator-owned ref. A two-round fixture proves approval passes through the
prior publication while requested feedback makes round two target the commit
published by round one. A permission-retry fixture fails authorization once,
proves every feed cursor and deduplicated ID remains unchanged, then succeeds
and chooses that same retained event. Another fixture proves an unresolved
same-time feedback actor blocks approval, while a strictly older unresolved
actor does not block a newer authorized approval. A boundary-race fixture
submits a review after the reviews feed was paged but before the comment feeds
finish. It proves the differing second sweep prevents the older approval from
committing and the restarted iteration chooses the newer feedback. Further
fixtures add a reply to an existing old review thread and dismiss a review
between sweeps; each must change its normalized digest and prevent a stale
outcome. Another dismissal mutates an early page after its second read and
proves that mutation belongs to the next vector, while the exact second-read
value is the one frozen in the current result. Review-body and comment-body
edits between sweeps change their digests and cannot leave stale text in Address
feedback. A continuously changing feed remains waiting without advancing
durable scan state. Exact 1,999-, 2,000-, and 2,001-item fixtures prove both
sweeps fit the verification-read budget through the limit and overflow visibly
beyond it. The accepted fixtures include worst-case distributions across all
three feeds, including one large feed plus two empty or one-item feeds.
Carry-forward fixtures post a new comment, edit a consumed comment, and submit
a changes-requested or nonempty `COMMENTED` review against the preceding commit
while Address feedback runs. With a following Gate, each item remains outside its inherited floor and
can request another feedback round. With terminal Update PR, the accepted commit
is published but the Track becomes partial with the bounded pending snapshot.
Mutation-order fixtures edit an old consumed `COMMENTED` review and an old
consumed comment after a newer approval. Their authorized changed digests must
choose feedback ahead of the approval; a no-write mutation is discarded and an
unverifiable actor keeps the observation waiting without durable advancement.
Permission-failure fixtures keep terminal Update PR observing after publication
without allowing full success, then recover to the same conclusive disposition.
Timeout fixtures advance the frozen hour, prove the published Action becomes
actionably failed without republishing, then retry observation with the same
authorization, floor, digest version, and remote commit and a fresh deadline.
Response-loss and restart fixtures preserve the same authorization, floor,
digest version, observation phase, vector, publication result, and partial
outcome. Cancellation races before authorization, after authorization, after
publication, and before or after the terminal observation transaction prove the
documented precedence and never promote a cancelled Track.
Credential-isolation fixtures give the host authority
broker a working provider credential while a real rootless Podman container can
edit, stage, commit, and run the frozen model adapter through the exact V1 mount
layout. Agent `git push`, `gh` mutation, credential-helper, SSH-agent, host-home,
Podman-socket, and helper-socket attempts all fail for `AC-47`. The self-test
also rejects a mutable image tag, rootful engine, wrong user namespace,
unexpected mount, or writable base-object mount.

Security-domain tests run the same container without any provider Stage and
prove host cache, branch manifests, refs, sibling worktrees, home, and Worker
sockets remain inaccessible. Host-authority integration tests launch Workers
from several config and data directories and reject mixed legacy and Pipeline
siblings in both orders, a second authority, mode change across restart, and an
unreimaged legacy host key presented for Pipeline enrollment. Registration
tests spoof Worker IDs and capabilities through the unauthenticated loopback API
and prove the control plane rejects missing, forged, replayed, expired, or
mode-conflicting signed attestations. Claim and provisioning tests prove every
Worker in a legacy domain rejects multi-stage work and helper credentials, every
Worker in a Pipeline domain rejects unsandboxed work, and only the enrolled
authority broker receives provider credentials and signed command envelopes.
Migration tests prove every existing host domain becomes legacy for `AC-52`.

Private-source tests use a credentialed local origin whose default branch and
base commit are unavailable anonymously. The authority resolves, clones, and
fetches through registered repository and cache identities while Worker and
Agent environments, remotes, configs, logs, and errors remain credential-free.
They inject response loss, retry, concurrent cache mutation, wrong repository,
path traversal, and a changed command binding for `AC-53`.

Privilege-separation tests record the effective and supplementary IDs, open file
descriptors, mounts, environment, and network policy of real Git and `gh`
children. A hostile repository supplies config, hooks, helpers, includes,
alternates, symlinks, and attempts to read the authority key, mode state,
credential vault, admin and authority sockets, another cache, Worker roots, and
host home. Every access fails while the registered clone, fetch, provider read,
and Track publication succeed with one ephemeral token for `AC-56`.

A real macOS topology test provisions the supported Linux VM, runs a one-stage
and multi-stage Pipeline, and asserts authority, Worker, broker, Git, `gh`, cache,
lock, worktree, and Agent process namespaces and paths are VM-local. Negative
fixtures add a writable host mount, move a Worker or broker to macOS, expose the
VM management channel to the Agent UID, and try a legacy sibling; readiness
rejects each case. Reboot during a Stage and during finalization proves exact
host identity, cache, lock, manifest, and Track recovery for `AC-57`.

Mutual-enrollment tests pin different control-plane keys on two servers. They
reject unsigned, forged, replayed, expired, wrong-server, and wrong-generation
attestations and broker commands. Rotation tests stop before persist, after
persist, and after acknowledgement, replay one rotation ID, accept both keys
only during the ten-minute overlap, and reject the old key afterward. Emergency
rotation tests require local operator enrollment and cleared credentials for
`AC-55`.

React tests prove one-node defaults, graph insertion and reorder, inspector
fields, atomic review-branch add, move, and delete, structural cost calculations, telemetry fallback, card grouping,
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
- Pipeline isolation creates a real host boundary. Existing host domains stay
  legacy; using one for multi-stage or provider work requires a reimage and new
  authority enrollment, and no Worker in that domain can run unsandboxed
  profiles.
- macOS Pipeline work runs fully inside a dedicated Linux VM. This adds VM disk,
  startup, administration, and remote-worktree UX cost, but keeps the same
  enforceable Linux isolation contract as native hosts.
- An offline owner Worker blocks its Tracks. The UI makes that explicit and the
  existing Worker recovery path resumes exact local state. A finalizing Track
  can be abandoned after the lost threshold, but that is a one-way failure path
  that quarantines its unresolved local branch. Cross-Worker continuation needs
  a later remote-checkpoint design.
- A lost local repository cache can lose intermediate lineage. V1 fails visibly
  rather than continuing from the wrong code. Operators who need disaster
  recovery should include Worker data directories in host backup.
- Automatic commits may include unintended tracked changes. The prompt names
  the contract, the Worker shows a diff summary in events, and every Agent
  Attempt uses a fresh worktree. Untracked files require explicit staging.
- Local refs accumulate. V1 reports and retains them because unsafe cleanup can
  remove code evidence. Retention needs a Track-aware follow-up design.
- Pull-request Actions add a write-capable provider boundary. They use existing
  authority-broker credentials and signed typed commands, publish only generated
  Track branches, require expected remote heads, and never merge or force-push.
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
- The canonical Graph is more data than a list, while V1 still cannot express
  parallel tests or general fan-in. The explicit model avoids a later Pipeline
  migration and makes every transition inspectable. A Stage runs at most once
  apart from retry. An operator can add another explicit PR review and Address
  feedback branch for a bounded second review round, but V1 has no automatic
  cycle.
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
  construct after the acyclic execution and cost model are proven. This does
  not block task breakdown.

## 13. Out of scope

- Cross-Worker continuation, remote checkpoint publication, and Track failover.
- General branching DAGs, parallel Agent execution inside one Track, and code
  fan-in beyond the structured exclusive review branch.
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
