# Software pipelines

> **Status:** Proposed for review

## 1. Executive summary

Factory runs one saved prompt across one or more repositories. Its execution
and recovery machinery is useful, but every repository Session is independent.
An operator cannot express a software process such as specify, implement, test,
review, and publish, or see where repository work sits in that sequence.

This design turns a Task into a **Pipeline** containing one or more ordered
agent **Stages**. One Pipeline Run creates one repository **Track** per selected
repository. Each Track advances through the same Stages in order. A Stage runs
as one existing Session with its own Attempts.

V1 keeps every Track on one persistent Worker. Each successful Stage records a
local Git commit, and the next Stage starts from that exact commit in a fresh
Attempt worktree on the same Worker. This proves sequencing and code lineage
without first requiring cross-Worker Git publication, protected remote refs,
new credentials, or provider-specific policy integration.

The browser lands on a software-work view. It groups active Pipeline Runs and
shows their Stage graph, repository Tracks, blocked work, current agent
activity, and recent outcomes in one place. Slate and Agent OS remain the place
for general human and business task tracking. Factory owns repository work,
agent execution, code lineage, and delivery evidence.

The main downside is availability. If a Track's Worker or local repository
state is unavailable, later Stages wait rather than moving to another Worker.
Cross-Worker checkpoint publication and failover require a later design. That
is an acceptable V1 trade because the product question is whether ordered agent
Stages and a software-work graph make Factory useful.

## 2. Context and scope

The [current architecture](../../ARCHITECTURE.md) uses Task, Run, Session, and
Attempt as its execution lifecycle, supported by Workers and repositories. A
Task stores one prompt, runtime, repository set, and optional schedule. A Run
freezes the Task and creates one Session per repository. Sessions do not depend
on each other and every Attempt starts in a fresh worktree from the repository
base branch.

This is sufficient for fleet operations such as reviewing many repositories.
It is insufficient for one software outcome that needs several agents or
checks in a known order. Starting five Tasks manually does not record the
dependency, preserve one code lineage, prevent an early review, or explain
which Stage needs attention.

This design changes the authoring model, Run lifecycle, Worker worktree input,
and primary browser surface. It preserves the current control-plane authority,
leases, Attempt supervision, per-repository isolation, local operator boundary,
remote Worker authentication, repository cache, and runtime adapters.

V1 Pipelines are linear. Every Stage after the first has exactly one
predecessor. Each repository advances independently, so a failure in one Track
does not stop healthy Tracks. A Pipeline may still contain one Stage, which is
the exact replacement for a current Task.

## 3. System context

```text
Operator browser
  Pipeline editor and software-work graph
                 |
                 | local HTTP and JSON
                 v
Factory control plane
  Pipeline versions, Run and Track state, Stage dependencies,
  Session admission, Worker affinity, local commit identity, SQLite
                 ^
                 | claim, lease, events, prepared input, completion
                 |
Persistent Worker
  repository cache and Track lineage
  fresh Attempt worktree per Stage or retry
  Pi, Codex, or Claude Code
```

The control plane owns desired order and durable state. The Track owner Worker
owns repository objects, local checkpoint refs, Attempt worktrees, and agent
processes. The control plane records full commit IDs but never receives
repository contents or Git credentials.

## 4. Proposed design

### How it works

An operator creates a Pipeline named `Ship issue` with three Stages:
`Implement`, `Test`, and `Review`. Each Stage has its own prompt, runtime,
persistent execution profile, and timeout. The Pipeline selects two
repositories and may run manually or on its existing schedule.

Admission freezes the complete Pipeline generation and repository identities.
It creates one Run, two Tracks, and six Sessions. First-Stage Sessions enter
existing routing. Later Sessions start in `waiting` and identify their
predecessor. The two Tracks may use different Workers and run concurrently
within Pipeline and Stage limits.

First-Stage routing considers the whole frozen Pipeline, not only Stage 1. An
owner candidate must be eligible for the repository and every Stage runtime and
execution profile in that Track. Admission fails before creating a Run when any
repository has no candidate with the complete capability intersection.

The first Worker prepares an Attempt worktree from the repository base commit.
Its prepared call atomically freezes the Track owner Worker, base commit, and
Stage input before agent startup. Worker affinity starts at that point. A failed
preparation before the call commits may release capacity and route to another
eligible Worker; no later operation changes the owner.

When the Implement agent exits successfully, the Worker stages modifications
and deletions to tracked files. It never automatically adds an untracked file.
A new file enters the Stage output only when the agent explicitly staged or
committed it. Any remaining unignored, untracked file fails completion with a
bounded reason. The Worker disables hooks for its own commit, verifies the
output descends from the frozen input in a fixed verification environment, and
creates an Attempt-scoped local ref to keep the commit reachable. Commit and
ancestry verification use a fresh temporary Git directory pointed only at the
repository cache's object directory. They ignore replacement refs, grafts,
alternates, hooks, config includes, system and user config, and agent-written
repository config. The Worker then creates and reads back the exact ref with
direct ref operations under the same fixed config. It proves both IDs name
commit objects, output descends from input, and the ref resolves to output
without executing agent-controlled behavior.

Only after the control plane accepts the full output commit under the active
lease does it mark Implement succeeded and make Test eligible. Test is
claimable only by the Track owner Worker. That Worker creates a fresh Attempt
worktree at the exact Implement commit. Test never reuses Implement's worktree,
so failed or dirty work remains inspectable without contaminating its input.
The same handoff occurs from Test to Review.

A Stage that makes no changes records its exact input commit as output and
creates the same Attempt-scoped local ref. Every successful handoff therefore
has one uniform commit proof.

Git is the only V1 data channel between Stages. A Stage that produces a
specification, report, or other successor input writes it into the repository
and stages or commits new files before success. The bounded agent result remains
a human-readable summary and is not injected into the next Stage prompt. The
Pipeline editor states this beside every non-final Stage prompt.

The browser shows one Pipeline Run section with Stage columns and one card per
repository in its current Stage. A card carries the repository, Stage, runtime,
elapsed time, current activity, and inline failure or blocked reason. Selecting
it opens Run detail at that Track and Session. Completed Tracks remain visible
in a quiet final column for the recent-history window.

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

The visual system follows the useful parts of Agent OS: a quiet fixed sidebar,
one clear page heading, compact filters, restrained status colour, readable
cards, and generous empty space. It removes the current metrics wall. Pipeline
Run groups carry hierarchy, Stage headers carry sequence, and repository cards
carry live state. The graph is not a free-form canvas and does not ask an
operator to position nodes.

### Components and responsibilities

The Pipeline service owns Pipeline validation, immutable generations, Stages,
repository scope, schedules, and admission. It depends on repository and
execution-profile readiness. It does not claim work or inspect Git contents.

The Run service owns Tracks, Stage dependencies, Worker affinity, Session
promotion, aggregate state, cancellation, retry, and commit identity. It
depends on existing routing and Attempt services. It does not create commits or
decide whether local Git objects exist.

The Worker owns repository preparation, Attempt worktrees, local commit and ref
creation, exact-input verification, runtime execution, retention, and recovery.
It does not choose the next Stage or change dependency state.

The browser owns the Pipeline editor, software-work graph, filters, and links
to Run and Session detail. It reads server projections and does not derive
dependency truth from local state.

### Decisions

**Extend the existing lifecycle instead of adding child Runs.** A Run remains
one immutable invocation and a Session remains one independently retriable
agent execution for one repository. Sessions gain Stage and Track identity. A
parent Run with child Task Runs would create two cancellation, retry, history,
and aggregate models.

**Rename Task to Pipeline.** The saved operator object becomes a Pipeline, and
a current Task migrates to a Pipeline with one Stage. Keeping both would leave
operators deciding which resource starts software work. Factory is in developer
preview, so the API and UI may make this clean break with a lossless database
migration.

**Ship a linear model first.** Stage order is a list and every Stage has zero
or one predecessor. This proves sequencing, retry, lineage, and the graph UI
without inventing merge semantics for two code-producing predecessors. Stable
Stage IDs and explicit predecessors keep a later DAG migration possible.

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

**Use a fresh worktree for every Attempt.** Reusing one directory would mix
failed changes with a retry or successor. A successful Attempt contributes only
its accepted output commit. Failed, cancelled, lost, and timed-out worktrees
retain current inspection and cleanup behavior.

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
  succeeded with a recorded full output commit.
- `INV-6`: A Session input is the frozen base commit for the first Stage or the
  exact accepted output commit of its predecessor.
- `INV-7`: One Worker owns a Track from the first successful prepared call
  until the Track is terminal.
- `INV-8`: Every Attempt starts in a fresh worktree at its Session's exact input
  commit on the Track owner Worker.
- `INV-9`: A successful multi-stage Session records one full output commit that
  descends from its input and one Attempt-scoped local ref keeping it reachable.
- `INV-10`: Failed, cancelled, lost, or timed-out Attempts cannot advance a
  Track or replace its last accepted commit.
- `INV-11`: Retrying a failed Session reuses its frozen input and cannot change
  a downstream Session that already started.
- `INV-12`: Editing a Pipeline never changes an admitted Run.
- `INV-13`: A Track advances independently of every other Track in the Run.
- `INV-14`: Pipeline and Stage concurrency limits count only Sessions holding
  execution slots.
- `INV-15`: The control plane stores commit identity and bounded metadata,
  never repository contents or Git credentials.
- `INV-16`: Existing Tasks, Runs, Sessions, Attempts, events, results, and
  retained-worktree links migrate without loss.
- `INV-17`: Committed Run or Track cancellation prevents every later
  completion or retry from promoting a successor.
- `INV-18`: A pending scheduled occurrence freezes the same complete ordered
  Pipeline snapshot as manual admission.
- `INV-19`: Commit ancestry and local-ref proof ignore every agent-writable Git
  replacement, graft, hook, helper, include, alternate, and configuration path.

### Requirements

- A Pipeline has 1 to 20 ordered Stages and 0 to 100 repositories. A draft may
  have no repositories, but admission requires at least one.
- The product of Stages and repositories must not exceed 500 planned Sessions
  in one Run.
- One resolved Stage prompt is at most 64 KiB and all Stage prompts in one
  Pipeline are at most 256 KiB. The versioned canonical formatter reserves the
  remaining space in the 72 KiB complete-input limit for bounded trusted
  Pipeline, Stage, Track, repository, branch, and commit context.
- Stage names are unique within a Pipeline after Unicode case folding and
  whitespace normalization.
- A Stage chooses Pi, Codex, or Claude Code, one execution profile, a timeout,
  and an optional concurrency limit.
- Every Stage in a multi-stage Pipeline uses a persistent profile. A one-stage
  Pipeline retains current persistent and fake-cloud profile support.
- For each repository in a multi-stage Run, at least one persistent Worker is
  eligible for every frozen Stage runtime and execution profile. First-Stage
  routing selects only from that complete capability intersection.
- Pipeline concurrency limits `queued`, `preparing`, and `running` Sessions in
  the Run. Stage concurrency limits the same states for that Stage. `waiting`,
  concurrency-blocked, and terminal Sessions consume no slot.
- The software-work view sorts actionable blocked and failed Sessions first,
  then active work, then recent successful or cancelled work.
- Cards always show Pipeline, Stage, repository, state, and relative time. A
  card that needs attention shows the reason inline.
- The graph remains keyboard navigable and usable at 390 CSS pixels without
  truncating failure or blocked reasons.

## 6. Interfaces and data

The operator API replaces `/api/v1/tasks` with `/api/v1/pipelines`. Create and
update bodies contain Pipeline fields plus an ordered `stages` array. Run-now,
schedule, archive, generation conflict, idempotency, and pagination behavior
remain the same under Pipeline names.

`GET /api/v1/runs` adds bounded Track and current-Stage summary data for the
software-work projection. `GET /api/v1/runs/{run_id}` returns the immutable
Pipeline snapshot, Tracks, Sessions grouped by Track and Stage, and existing
Attempt summaries. The detail response does not inline Attempt events.

Worker claims add `track_id`, `stage_id`, `stage_name`, `stage_position`,
`track_owner_worker_id`, and `input_commit`. A first-Stage claim has no owner
yet. A later-Stage or retry claim is available only to the stored owner Worker.

Before runtime launch, the Worker calls a lease-protected prepared endpoint
with resolved base branch, exact worktree HEAD, worktree identity, and working
branch. For the first Stage, the transaction freezes Track owner, base commit,
and input commit if unset. For a retry or later Stage, it rejects a different
Worker or commit. Only a successful prepared response authorizes startup. The
existing start endpoint continues to record supervisor process identity.

Preparation failure before owner freeze terminalizes the Attempt, releases
capacity, clears assignment, and lets routing choose another eligible Worker.
Preparation failure after owner freeze releases capacity and blocks the Session
on that Worker with a bounded reason and retry time. It never reroutes the
Track. Five automatic preparation failures exhaust the execution cycle and fail
the Session; manual retry begins a new cycle and keeps Attempt history.

The claim protocol version increases. A prior Worker may renew and complete an
Attempt it already owns for a migrated one-stage Pipeline, but it cannot receive
another claim until it registers with Pipeline support. New Workers understand
one-stage Sessions without Track lineage and multi-stage owner-affine Sessions.

Attempt completion adds `output_commit` for multi-stage success. The server
rejects a missing, malformed, unexpected-input, or wrong-Worker identity before
changing Session state. The authenticated owner Worker attests that it proved
ancestry and local-ref read-back because the control plane does not hold Git
objects. A one-stage Pipeline keeps the current completion contract and does
not require a local handoff ref.

Pipeline authoring and admission share a versioned canonical prompt formatter
with the Worker. It validates the complete UTF-8 payload, including maximum
branch fields and trusted Pipeline context, against the 72 KiB protocol limit.
Save, schedule, admission, claim validation, and Worker startup use the same
formatter and bound.

The database gains Pipeline Stage and Track tables. Sessions gain Track and
Stage foreign keys, `waiting` and `skipped` states, input and output commits,
skip reason, and the Session whose outcome caused the skip. Tracks store owner
Worker, frozen base commit, current accepted commit, and workspace health. A
Worker repository-ref projection stores only the latest complete inventory scan
and its health metadata. The current unique Run and repository constraint
becomes a unique Track and Stage constraint.

Runs and Tracks gain cancellation timestamps used as promotion fences.
Executions gain preparation-failure count and next-routing time. A Session
blocked only by Pipeline or Stage concurrency uses one canonical,
non-actionable reason so the claim transaction can promote it without operator
intervention.

Each pending scheduled occurrence stores the complete immutable Pipeline
generation it will admit: ordered Stages, repository identities, execution
settings, and scheduled instant. Editing a Pipeline only changes occurrences
created after the edit.

For scheduled admission, absence of one complete owner candidate is transient
while the frozen repositories and profiles remain valid. The pending occurrence
keeps its snapshot, records the bounded `no_complete_pipeline_worker` health
reason, and retries with the existing scheduler backoff until an eligible fleet
returns. Invalid, deleted, or incompatible frozen repository or profile data is
permanent and blocks the occurrence under current scheduler rules. Manual
admission returns the same reason immediately without creating a Run.

The migration renames current Tasks and Task repositories to Pipelines and
Pipeline repositories. It creates one Stage named `Execute` for every current
Task, using the Task ID as Stage ID in its separate namespace. Current Run
snapshots become one-Stage Pipeline snapshots. Each current Session receives a
Track and points to that Stage. Historical Sessions expose null input and output
commits unless the old record already proved a value; the UI labels them
unavailable rather than inventing them.

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

The Worker creates local refs with this shape in its repository cache:

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

After the control plane confirms acceptance, an intermediate successful Stage
worktree may be removed when it is clean and its local ref resolves to the
accepted commit. The manifest records this Pipeline-specific evidence before
cleanup. Response loss, rejection, or ref mismatch retains the worktree. The
final successful Stage keeps the current clean-and-published cleanup rule so the
operator does not lose its visible delivery worktree merely because Pipeline
sequencing finished.

Workers reconcile local Pipeline refs through a cursor-bounded inventory API.
One scan has a random ID and enumerates the union of the complete local ref
namespace and manifest entries, in pages of at most 500. Paired records contain
stored Run, Track, Stage, Attempt, repository, full commit, creation time, and
manifest state. Before reporting a pair, the Worker reads the exact ref under
the fixed config. A ref without a manifest is still reported as an orphan with
unknown age. A manifest without a ref is reported as incomplete. A conflicting
or malformed pair reports corrupt health. The server matches valid pairs to
accepted Session output and publishes aggregate repository health only after
the Worker marks the scan complete. An interrupted scan never replaces the
last complete projection.

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
No downstream Session changes state.

Success completion records output identity, marks the Session succeeded, and
promotes its direct successor in one database transaction. With capacity
available, the successor becomes routeable to the owner Worker in that
transaction. Without capacity, it becomes blocked with the canonical
nonactionable concurrency reason. Existing claim materialization rechecks it
on each healthy owner Worker poll and promotes it when capacity exists.

An idempotent completion replay returns stored completion. An expired lease
cannot publish completion even if the Worker created a local ref. That orphan
ref is reported by reconciliation but does not advance the Track or conflict
with a later Attempt.

When a Session fails, every later waiting Session in that Track becomes skipped
and records the failed Session as cause. Retrying the failed Session reopens the
Run, returns only never-started successors skipped by that failure to waiting,
and uses the same input. A successor with an Attempt is never reset.

Cancelling a Run first commits its cancellation timestamp, then requests
cancellation for active Sessions and marks waiting or concurrency-blocked
Sessions skipped. Cancelling one Session commits a Track cancellation timestamp
and applies the same rule to that Track. These timestamps are promotion fences:
a late completion is rejected, stores no accepted output, and cannot promote a
successor. A cancelled Run or Track cannot reopen through Session retry; the
operator starts a new Pipeline Run.

`waiting` is neither claimable nor terminal. `skipped` is terminal. A Run is
active while any Session is blocked, queued, preparing, running, or waiting.
The canonical concurrency block counts as active but not actionable. A Run
needs attention when another actionable blocked or failed Session exists. It
becomes terminal when every Session is succeeded, failed, cancelled, or
skipped. Terminal aggregate state uses these ordered predicates:

1. `succeeded` when every Track completed every Stage successfully.
2. `partial` when at least one Track completed every Stage successfully but not
   every Track did.
3. `failed` when no Track completed every Stage and at least one Session failed.
4. `cancelled` when no Track completed every Stage, no Session failed, and a
   Run or Track cancellation timestamp exists.

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

Editing, archiving, or disabling a schedule affects future occurrences only.
Server shutdown stops admission and scheduling before HTTP shutdown. Active
Worker leases and Attempt recovery remain otherwise unchanged.

## 8. Security, privacy, and operations

The local operator and remote Worker trust boundaries do not change. Current
persistent coding runtimes are not a security sandbox. This design introduces
no new origin credential, privileged publisher, or remote ref permission.
Multi-stage execution therefore has the same trust level as current one-stage
execution on that Worker.

Only the authenticated owner Worker with the active Attempt lease may report an
output commit. The server validates IDs, Worker identity, commit format, Stage
position, predecessor equality, and cancellation fences. The Worker disables
hooks for its own automatic commit. It verifies objects and ancestry in a fresh
Git directory with `GIT_NO_REPLACE_OBJECTS`, no graft file, no alternates, no
repository config, and fixed empty system and global config. It creates and
reads back the exact local ref through direct ref operations under that fixed
config. It does not trust agent output to name a successor commit.

Prompts, results, events, branches, local refs, and commit metadata may contain
sensitive project information and keep current local retention rules. Git
contents and credentials remain on Workers and origins. No local Pipeline ref
is pushed automatically.

Multi-stage Pipelines require one persistent Worker that can prepare the
repository for the Track. One-stage Pipelines keep current behavior. A
repository unavailable to a Worker is handled by existing routing before owner
freeze and by owner-affinity failure afterward.

Local Pipeline refs keep accepted commits reachable and are not deleted
automatically in V1. Repository detail reports count and age. Retention and
safe cleanup require a later design because deleting a ref while a Run or
retained Attempt depends on it would lose evidence.

At most 500 Sessions are planned per Run, 100 hold execution slots, 20 Stages
are stored per Pipeline, and prompts total at most 256 KiB. List APIs remain
cursor bounded. Attempt event and result limits remain unchanged.

## 9. Acceptance criteria

- `AC-1`: An operator can create a three-Stage Pipeline for two repositories
  and start one Run without creating or invoking separate Tasks.
- `AC-2`: With capacity available, each repository makes Stage 2 claimable in
  the transaction that accepts its Stage 1 commit, independent of the other
  repository.
- `AC-3`: Stage 2 starts from the exact full commit reported by Stage 1 in a
  fresh worktree on the Track owner Worker.
- `AC-4`: A failed commit or local-ref operation leaves Stage 2 unstarted,
  shows an actionable reason, and retains the Stage 1 worktree.
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
  settings after the Pipeline is edited.
- `AC-15`: Cancellation committed concurrently with Stage success never makes
  the successor claimable.
- `AC-16`: Preparation failure before owner freeze releases capacity and may
  route to a second eligible Worker; failure afterward stays owner-affine.
- `AC-17`: A reset or replacement history cannot succeed because the output
  commit must descend from the frozen input.
- `AC-18`: An untracked file is never added automatically; completion fails
  until the agent stages, commits, ignores, or removes it.
- `AC-19`: The largest valid resolved Stage prompt plus trusted context fits the
  72 KiB complete-input limit, and the next byte is rejected before execution.
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
- `AC-24`: Replacement refs, grafts, alternates, or agent-written Git config
  cannot make an unrelated output pass ancestry or local-ref verification.
- `AC-25`: A scheduled occurrence with no complete owner candidate keeps its
  frozen snapshot and admits successfully after the eligible fleet returns.
- `AC-26`: Repository detail shows the latest complete local-ref count, oldest
  known age, unknown-age count, orphan, incomplete, and corrupt counts, scan
  health, and freshness, and never replaces it with a partial scan.
- `AC-27`: A crash before or after every manifest and ref transition recovers a
  matching pair or reports the one-sided or conflicting state; no local ref is
  omitted from inventory.

## 10. Test approach

Store tests prove atomic admission, independent Track promotion, owner freeze,
owner-only routing, aggregate state, retry, cancellation races, generation
snapshots, concurrency-block promotion, schedule snapshots, limits, and every
terminal predicate for `INV-1` through `INV-18`.

Worker integration tests use two Workers and local bare origins. They prove a
Track freezes its first prepared Worker, each Stage uses a fresh worktree, the
successor starts from the exact accepted commit, and another Track may choose a
different Worker. Failure cases cover preparation before and after owner freeze,
dirty tracked work, explicitly staged new files, untracked credential-like
files, no-change success, response loss, lease expiry, reset history, malformed
commit IDs, replacement refs, grafts, config includes, object alternates,
local-ref conflict, missing objects, and corrupt cache state. Verification tests
prove fixed Git configuration and `INV-19` through `AC-24`.
Capability-intersection tests reject admission when no one Worker can run every
Stage and block an owner whose later runtime becomes unhealthy for `AC-23`.

Recovery tests restart the owner Worker between Stages and prove it recovers
repository cache, local refs, retained worktrees, and manifest state before
claiming the successor. Offline-owner tests prove no other Worker may claim and
that the lost threshold changes the reason from operational to actionable.
Crash-injection tests stop before and after each manifest write, directory
fsync, ref compare-and-swap, and ready transition. They prove recovery and the
union inventory contract for `AC-27`, including agent-created ref-only state.

Workspace-loss tests fail the earliest incomplete Session, fence a concurrent
completion, and skip its successors. They prove `failed` for a single Track and
`partial` when another Track completed for `AC-22`.

Migration tests build the last pre-Pipeline schema with active, succeeded,
failed, cancelled, retried, and retained Sessions. They compare every source
row and payload after migration for `INV-16` and `AC-10`, and prove migration
refuses collisions or incomplete source state without changing the database.
Pending-occurrence fixtures cover enabled, disabled, archived, blocked, paused,
and retrying schedules and preserve their frozen source snapshot. A complete
fleet outage remains transient and admits the same occurrence after Worker
health returns for `AC-25`.

HTTP contract tests cover Pipeline validation, pagination, immutable snapshots,
claim fields, owner conflicts, commit validation, complete-input byte
boundaries, and local or remote route boundaries. Inventory tests page more
than 500 refs, interrupt a scan, complete a later scan, classify accepted and
orphan refs, report failure and staleness, and prove the repository projection
for `AC-26`.

React tests prove card grouping, sorting, attention reasons, links, empty and
failure states, and accessible names. A real Chromium test runs the three-Stage,
two-repository flow at desktop and 390-pixel widths, checks console and failed
requests, and verifies focus order, visible focus, Stage and Track navigation,
and Enter and Space activation.

## 11. Risks and tradeoffs

- An offline owner Worker blocks its Tracks. The UI makes that explicit and the
  existing Worker recovery path resumes exact local state. Cross-Worker
  continuation needs a later remote-checkpoint design.
- A lost local repository cache can lose intermediate lineage. V1 fails visibly
  rather than continuing from the wrong code. Operators who need disaster
  recovery should include Worker data directories in host backup.
- Automatic commits may include unintended tracked changes. The prompt names
  the contract, the Worker shows a diff summary in events, and every Stage uses
  a fresh worktree. Untracked files require explicit staging.
- Local refs accumulate. V1 reports and retains them because unsafe cleanup can
  remove code evidence. Retention needs a Track-aware follow-up design.
- A linear model cannot express parallel tests or approval fan-in. It still
  validates sequencing. Explicit predecessor identity keeps a DAG migration
  possible.
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

## 13. Out of scope

- Cross-Worker continuation, remote checkpoint publication, and Track failover.
- Branching DAGs, parallel Stage execution inside one Track, and fan-in.
- Human approval, question, and resume nodes.
- Conditional Stages or prompt expressions based on earlier results.
- Automatic merge, deployment, rollback, or pull-request publication.
- Multi-stage fake-cloud or future remote-cloud execution profiles.
- General business tasks, people assignment, due dates, and personal planning.
- Cross-repository dependencies inside one Track.
- Passing agent result prose directly into a successor Stage prompt.
- Moving Git file contents or credentials through the control plane.
- Automatic local-ref retention or deletion.
