# Software pipelines

> **Status:** Proposed for review

## 1. Executive summary

Factory runs one saved prompt across one or more repositories. Its execution
and recovery machinery is strong, but every repository Session is independent.
An operator cannot express the software process that surrounds one agent run,
such as specify, implement, test, review, and publish. The current Run board
shows machine state rather than progress through that process.

This design turns a Task into a **Pipeline** containing one or more ordered
agent **Stages**. One Pipeline Run creates one repository **Track** per selected
repository. Each Track advances through the same Stages in order. A Stage runs
as one existing Session with its own Attempts. A successful Stage publishes an
immutable Git checkpoint before the next Stage becomes eligible, so another
Worker can continue from the exact code it produced.

The browser lands on a software-work view. It groups active Pipeline Runs and
shows their Stage graph, repository Tracks, blocked work, current agent
activity, and recent outcomes in one place. Slate and Agent OS remain the place
for general human and business task tracking. Factory owns repository work,
agent execution, code handoff, and delivery evidence.

The main downside is scope. Sequencing makes Factory responsible for durable
dependencies and Git handoff, not only independent dispatch. V1 therefore
supports a linear Pipeline. Branching, fan-in, approval nodes, and automatic
pull-request publication remain later work.

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
which stage needs attention.

This design changes the authoring model, execution lifecycle, Worker Git
contract, and primary browser surface. It preserves the current control-plane
authority, leases, Attempt supervision, per-repository isolation, local
operator boundary, remote Worker authentication, and runtime adapters.

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
  Session admission, immutable checkpoint identity, SQLite
                 ^
                 | claim, lease, events, checkpoint completion
                 |
Persistent Worker -------------------------- Git origin
  isolated Attempt worktree                  immutable checkpoint refs
  Pi, Codex, or Claude Code                  shared between Workers
```

The control plane owns desired order and durable state. A Worker still owns
the agent process and worktree. Git origin is the handoff boundary between
Workers. The control plane records checkpoint identity but never receives
repository contents.

## 4. Proposed design

### How it works

An operator creates a Pipeline named `Ship issue` with three Stages:
`Implement`, `Test`, and `Review`. Each Stage has its own prompt, runtime,
execution profile, and timeout. The Pipeline selects two repositories and may
be run manually or on its existing schedule.

Admission freezes the complete Pipeline version and current repository
identities. It creates one Run, two Tracks, and six Sessions. The first Session
in each Track is routeable. Later Sessions start in `waiting` and identify
their predecessor. The two Tracks can run at the same time within the
Pipeline's concurrency limit.

The Implement agent starts from the repository base commit in an isolated
worktree. When the agent exits successfully, the Worker commits tracked changes
and new files the agent explicitly staged, then pushes the resulting commit to
a Factory-owned, origin-protected checkpoint ref. Only after the control plane
accepts that checkpoint does it mark Implement succeeded and make Test eligible
in the same transaction.

Test may run on another Worker. It fetches the exact Implement checkpoint,
creates a fresh Attempt worktree, and receives the Test prompt plus trusted
Pipeline, Stage, Track, predecessor, input commit, and checkpoint context. The
same handoff occurs from Test to Review. A Stage that makes no changes still
publishes a checkpoint ref pointing at its input commit, so every successful
handoff has one uniform proof.

Git is the only V1 data channel between Stages. A Stage that produces a
specification, report, or other successor input writes it into the repository
before success. The bounded agent result remains a human-readable summary and
is not injected into the next Stage prompt. The Pipeline editor states this
beside every non-final Stage prompt.

The browser shows one Pipeline Run section with Stage columns and one card per
repository in its current Stage. A card carries the repository, Stage, runtime,
elapsed time, current activity, and inline failure or blocked reason. Selecting
it opens the existing Run detail at that Track and Session. Completed Tracks
remain visible in a quiet final column for the recent-history window.

```text
Work                                                2 need you
[ Needs you 2 ] [ Active 5 ] [ Recent ]

Ship issue · Run 91c2                         2 repositories
             Implement          Test               Review
factory      succeeded  ───────  running 6m  ─────  waiting
handbook     failed
             Checkpoint push was denied by origin.

Dependency review · Run e173                   1 repository
api          succeeded  ───────  succeeded  ──────  review running
```

The visual system follows the useful parts of Agent OS: a quiet fixed sidebar,
one clear page heading, compact filters, restrained status colour, readable
cards, and generous empty space. It removes the current metrics wall. Pipeline
Run groups carry the hierarchy, Stage headers carry sequence, and repository
cards carry live state. The graph is not a free-form canvas and does not ask an
operator to position nodes.

### Components and responsibilities

The Pipeline service owns Pipeline validation, immutable versions, Stages,
repository scope, schedules, and admission. It depends on repository and
execution-profile readiness. It does not claim work, supervise agents, or
inspect Git contents.

The Run service owns Tracks, Stage dependencies, Session promotion, aggregate
state, cancellation, retry, and checkpoint identity. It depends on the
existing routing and Attempt services. It does not create commits or decide
whether a checkpoint exists remotely.

The Worker owns base and checkpoint fetches, isolated worktrees, runtime
execution, checkpoint commit creation, publication proof, and safe retention.
It depends on its existing repository credentials. It does not choose the next
Stage or change dependency state.

The browser owns the Pipeline editor, software-work graph, filters, and links
to Run and Session detail. It reads server projections and does not derive
dependency truth from local state.

### Decisions

**Extend the existing lifecycle instead of adding child Runs.** A Run remains
one immutable invocation and a Session remains one independently retriable
agent execution for one repository. Sessions gain Stage identity and
dependencies. A separate parent Pipeline Run with child Task Runs would create
two cancellation, aggregate, retry, and history models.

**Rename Task to Pipeline.** The saved operator object becomes a Pipeline, and
a current Task migrates to a Pipeline with one Stage. Keeping both Task and
Pipeline would leave operators deciding which resource starts software work.
The repository is in developer preview, so the API and UI may make this clean
break with a documented database migration.

**Ship a linear model first.** Stage order is a list and every Stage has zero
or one predecessor. This proves sequencing, retry, code handoff, and the graph
UI without inventing merge semantics for two code-producing predecessors. The
schema uses stable Stage IDs and explicit predecessor IDs so a later DAG does
not require replacing identity.

**Advance each repository independently.** A Track is the ordered Sessions for
one repository within a Run. A failed repository does not hold unrelated
repositories at an earlier Stage. Lockstep execution was rejected because one
bad repository would waste healthy fleet capacity and obscure partial
progress.

**Use immutable remote Git refs for handoff.** Worker affinity was rejected
because an offline Worker would strand the Pipeline and cloud execution could
not continue it. Patch blobs in SQLite were rejected because they would move
repository contents into the control plane and duplicate Git's object model.

**Limit multi-stage V1 to persistent execution profiles.** The current cloud
profile returns result and patch artifacts but cannot publish a durable Git
checkpoint. Pipeline validation and admission reject a multi-stage Pipeline
when any Stage uses a non-persistent profile. One-stage Pipelines retain every
current profile. Cloud checkpoint publication requires a separate design.

**Let the Worker create the checkpoint commit.** Agents may leave committed or
uncommitted changes. On a successful exit, the Worker stages modifications and
deletions to tracked files, but never automatically adds an untracked file. A
new file enters the checkpoint only when the agent explicitly staged or
committed it. Any remaining unignored, untracked file fails checkpoint creation
with a bounded reason instead of being silently omitted or uploaded. The Worker
creates a commit when the index is dirty and publishes the final HEAD. Before
publication it revalidates that `origin` still matches the snapshotted
repository identity and proves the frozen input commit is an ancestor of the
output commit. A reset, replacement history, changed origin, or unresolved new
file fails the Attempt and retains the worktree. This makes success observable
without automatically disclosing scratch files or credentials.

**Keep publication separate from an agent's branch.** The immutable checkpoint
ref is Worker-owned even if an agent pushed its working branch or opened a pull
request. This prevents an agent side effect from silently selecting the input
for the next Stage. Built-in pull-request publication is a later Stage type.

**Give every Attempt its own checkpoint ref.** A successful push followed by
response loss or lease expiry leaves an orphan ref. A retry uses a new ref and
cannot conflict with that orphan. The control plane records only the winning
Attempt ref when it accepts completion, and only that ref can feed the next
Stage.

**Require origin-enforced checkpoint protection.** Create-only Worker behavior
and read-back prove one instant, not future immutability. Every repository used
by a multi-stage Pipeline must protect
`refs/heads/factory/checkpoints/**` at the Git origin against every update or
deletion of an existing ref. Creation is permitted only to a dedicated Factory
checkpoint principal. Agents and other repository writers cannot create,
update, or delete refs in the namespace. The Worker uses a separate credential
for checkpoint publication and never exposes it to the agent process or
worktree Git configuration. Factory stores the provider-verified policy
identity and version on the repository and rejects multi-stage admission when
that evidence is missing or stale.

**Group the graph by Pipeline Run.** A global set of status columns was rejected
because `running` does not say whether code is being implemented, tested, or
reviewed. Each Run renders its frozen Stage order, and each repository Track
occupies that graph. This keeps sequence and project context visible together.

## 5. Invariants and requirements

### Invariants

- `INV-1`: A Run contains one immutable Pipeline snapshot, including ordered
  Stages, repository identities, execution settings, and source.
- `INV-2`: A Track belongs to exactly one Run and one snapshotted repository.
- `INV-3`: A Session belongs to exactly one Track and one snapshotted Stage.
- `INV-4`: One Track contains at most one Session for a Stage.
- `INV-5`: A non-first Session cannot become routeable until its predecessor
  succeeded with a recorded output commit and checkpoint ref.
- `INV-6`: A Session input commit is the frozen repository base commit for the
  first Stage or the exact output commit of its predecessor.
- `INV-7`: A successful Session in a multi-stage Pipeline has one full output
  commit ID and the winning Attempt's write-once checkpoint ref, which the
  completing Worker proved resolves to that commit at origin.
- `INV-8`: Failed, cancelled, lost, or timed-out Attempts cannot advance a
  Track, replace its last successful checkpoint, or have a pushed orphan ref
  accepted as a Session checkpoint.
- `INV-9`: Retrying a failed Session reuses its frozen input commit and cannot
  change a downstream Session that already ran.
- `INV-10`: Editing a Pipeline never changes an admitted Run.
- `INV-11`: A Track advances independently of every other Track in the Run.
- `INV-12`: Pipeline and Stage concurrency limits are enforced before a
  Session becomes claimable.
- `INV-13`: The control plane stores checkpoint identity and bounded metadata,
  never repository file contents or Git credentials.
- `INV-14`: Existing Tasks, Runs, Sessions, Attempts, events, results, and
  retained-worktree links migrate without loss.
- `INV-15`: Committed Run or Track cancellation prevents every later
  completion or retry from promoting a successor.
- `INV-16`: A pending scheduled occurrence freezes the same complete ordered
  Pipeline snapshot as manual admission.
- `INV-17`: An accepted output commit descends from the frozen input commit and
  its checkpoint ref exists at the snapshotted repository origin.
- `INV-18`: The Git origin permits only the dedicated Factory checkpoint
  principal to create checkpoint refs and prevents every principal from
  updating or deleting an accepted checkpoint ref.

### Requirements

- A Pipeline has 1 to 20 ordered Stages and 0 to 100 repositories. A draft may
  have no repositories, but admission requires at least one.
- The product of Stages and repositories must not exceed 500 planned Sessions
  in one Run.
- One resolved Stage prompt is at most 64 KiB and all Stage prompts in one
  Pipeline are at most 256 KiB. The canonical formatter reserves the remaining
  space in the versioned 72 KiB complete agent-input limit for bounded trusted
  Pipeline, Stage, Track, checkpoint, repository, and branch context. Save,
  schedule, admission, claim validation, and Worker startup reject a complete
  input that exceeds 72 KiB.
- Stage names are unique within a Pipeline after Unicode case folding and
  whitespace normalization.
- A Stage chooses Pi, Codex, or Claude Code, one execution profile, a timeout,
  and an optional concurrency limit.
- Every Stage in a multi-stage Pipeline uses a persistent execution profile.
  A one-stage Pipeline retains current persistent, cloud, and fake-cloud
  profile support.
- Every repository in a multi-stage Pipeline has current evidence for an
  provider-verifiable, origin-enforced protected checkpoint namespace.
- Pipeline concurrency limits Sessions holding an execution slot in the Run.
  Stage concurrency further limits slot holders for that Stage. Exactly
  `queued`, `preparing`, and `running` Sessions hold slots; `waiting`,
  concurrency-blocked, and terminal Sessions do not.
- The software-work view sorts actionable blocked and failed Sessions first,
  then active work, then recent successful or cancelled work.
- Cards always show a Pipeline name, Stage name, repository identity, state,
  and relative time. A card that needs attention shows the reason inline.
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

Worker claims add `track_id`, `stage_id`, `stage_name`, `stage_position`, and
the winning predecessor checkpoint ref when one exists. For a later Stage, the
Worker first fetches that ref, verifies it resolves to `input_commit`, and
creates or resets the isolated worktree at that exact commit. Before launching
the runtime, the Worker calls a new lease-protected prepared endpoint with the
resolved base branch, exact worktree HEAD, input commit, worktree identity, and
working branch. For a first Stage, the transaction freezes the Track base
commit if it is not already set. For a retry or later Stage, the endpoint
rejects a worktree HEAD or input identity that differs from the stored input.
Only a successful prepared response authorizes process startup. The existing
start endpoint continues to record supervisor process identity.

If preparation fails, the Worker calls the lease-protected
`POST /api/v1/attempts/{attempt_id}/preparation-fail` endpoint with a bounded
failure code and message. In one transaction, the server terminalizes the
Attempt as a preparation failure, releases Worker capacity, clears the
assignment, blocks the Session with the actionable reason, and sets its next
routing time. Routing prefers another eligible Worker. If none exists, it may
retry the same Worker after `min(2^failure_count, 60)` seconds. Five automatic
preparation failures exhaust the execution cycle and fail the Session. A
manual retry starts a new cycle and keeps the prior Attempt history.

The claim protocol version increases. A Worker on the prior version may renew
and complete an Attempt it already owns for a migrated one-stage Pipeline, but
it cannot receive another claim until it registers with Pipeline support. A
new Worker understands both one-stage Sessions without checkpoints and
multi-stage Sessions with the prepared and checkpoint contracts.

Attempt completion adds `output_commit` and `checkpoint_ref` for success in a
multi-stage Pipeline. The server rejects missing, malformed, or unexpected
checkpoint identity before changing Session state. A one-stage Pipeline uses
the current completion contract and does not require an unused remote write.
The Worker must revalidate canonical origin identity and commit ancestry after
the agent exits and again as part of checkpoint read-back proof.

Pipeline authoring and admission use a versioned canonical agent-input
formatter shared with the Worker. It calculates the complete UTF-8 payload,
including maximum branch fields and every trusted Pipeline context field,
against the 72 KiB protocol limit. The claim protocol version changes whenever
that formatter or bound changes.

The database gains Pipeline Stage and Track tables. Sessions gain Track and
Stage foreign keys, `waiting` and `skipped` states, input and output commit
columns, checkpoint ref, skip reason, and the Session whose outcome caused the
skip. The current unique Run and repository constraint becomes a unique Track
and Stage constraint.

Runs and Tracks gain cancellation timestamps used as promotion fences.
Executions gain a preparation-failure count and next-routing time. A Session
blocked only by Pipeline or Stage concurrency uses one canonical,
non-actionable reason so the claim transaction can promote it without operator
intervention.

Repositories gain checkpoint-protection policy identity, version, digest,
verification source, and verification time. Runs snapshot that evidence.
Provider-backed verification reads effective origin ref rules and permissions
for both the dedicated Factory checkpoint principal and the agent credential.
V1 does not admit multi-stage work for an origin without a supported policy
verifier. Changing repository identity clears the evidence.

Admission refreshes the policy and stores its digest on the Run. The control
plane refreshes it before every later-Stage claim and checkpoint acceptance,
and reconciles active multi-stage Runs at least once per minute. A provider
webhook triggers the same check sooner when available. Missing protection or a
digest change blocks the affected Run and every not-yet-started Session with an
actionable policy-drift reason. No new checkpoint can be accepted and no
successor can start until the exact admitted protection is restored or the
operator cancels the Run. Origin administrators remain inside the repository
trust boundary, but policy drift is never treated as a healthy handoff.

Each pending scheduled occurrence stores the complete immutable Pipeline
generation it will admit: ordered Stages, repository identities, execution
settings, and scheduled instant. Editing a Pipeline only changes occurrences
created after the edit.

The migration renames current Tasks and Task repositories to Pipelines and
Pipeline repositories. It creates one Stage named `Execute` for every current
Task, using the Task ID as the Stage ID in its separate namespace. Current Run
snapshots become one-Stage Pipeline snapshots. Each current Session receives a
Track and points to that one Stage. Existing Sessions have no synthetic output
checkpoint because no Worker proved one. Historical state remains read-only
where later sequencing would require missing proof. Pre-Pipeline historical
Sessions expose null input commit, output commit, and checkpoint ref unless the
old record already proved a value; the UI labels those fields unavailable
rather than inventing them.

Every existing pending or paused scheduled occurrence is converted to a
one-Stage `Execute` Pipeline generation from its own frozen Task snapshot, not
from the current mutable Pipeline. The migration preserves its due instant,
retry state, health, enabled or paused status, and association with an archived
source. Resume after upgrade therefore admits the same prompt, repositories,
runtime, and execution settings that were pending before upgrade.

Upgrade creates and validates the existing owner-only database backup before
the Pipeline migration starts. The migration refuses a name collision,
unsupported source schema, invalid snapshot, or row it cannot map without
changing the live database. The embedded UI and operator API change in the same
server build, so there is no mixed Task and Pipeline operator surface.

Rollback is offline. The operator stops the new server and Workers and restores
the pre-upgrade backup with the supported restore command. Factory does not
dual-write the old schema, so Runs admitted after upgrade are not present in
that backup. The release guide must state that data boundary before upgrade.

The Worker publishes checkpoint refs with this shape:

```text
refs/heads/factory/checkpoints/<run-id>/<track-id>/<stage-position>-<stage-id>/<attempt-id>
```

The Worker creates a ref only when it does not already exist. Factory treats
the ref as write-once and never updates it. A completion replay for the same
Attempt may reuse the ref only when it resolves to the same output commit. A
new Attempt always receives a new ref, so an orphan from response loss or lease
expiry cannot poison a retry. A later Worker fetches only the winning ref
recorded on the predecessor Session and verifies it still resolves to the
stored input commit before it starts. The origin's protected checkpoint
namespace prevents every update or deletion after creation; Worker read-back
detects misconfiguration but is not the immutability mechanism. A provider
policy check immediately before creation proves that only the dedicated
checkpoint principal can create the Attempt ref.

In this design, **pushed** means the Worker created a ref at Git origin, while
**accepted** means the control plane committed that ref as the winning Session
checkpoint under a valid lease and cancellation fence. Response loss, lease
expiry, or cancellation may leave a pushed orphan ref. Such a ref is never
accepted, never stored as the Session checkpoint, and never advances the Track.

### Naming and identity

Pipeline, Run, Track, Session, Attempt, and newly authored Stage IDs are random
UUIDs created by the control plane. A migrated Stage reuses its Pipeline ID in
the Stage table so migration needs no unstable generated value. IDs are scoped
by table and never inferred from names.

Pipeline names keep the current normalized uniqueness rule. Stage names are
unique only inside one Pipeline. Renaming either changes the next Pipeline
generation; historical snapshots keep the prior name.

Checkpoint refs use stored IDs, zero-based Stage position, and Attempt ID.
Operators cannot provide a ref. A Pipeline edit cannot change refs for an
admitted Run because the Run snapshot freezes Stage identity and position.

## 7. Failure behavior and lifecycle

Admission writes the Run, every Track, and every planned Session in one
transaction. First-stage Sessions begin blocked or queued through existing
routing. Later Sessions begin waiting. A partial write creates no Run.

The first Worker resolves the base branch and commit during preparation, then
freezes both through the prepared endpoint before launching the agent. A retry
must fetch that exact commit even if the remote base branch moved. Preparation
failure uses the preparation-fail endpoint and cannot leave Worker capacity
leased. The Session records the bounded Git reason and next-routing time. A
prepared request replay is idempotent for the same lease and identity and
conflicts for a different identity. Integration tests must prove that one
Worker can reject preparation and a second eligible Worker can then succeed.

An agent exit of zero is provisional until checkpoint publication succeeds.
Commit creation, remote push, or remote read-back failure completes the
Attempt and Session as failed with a bounded actionable reason. The worktree is
retained. No downstream Session changes state.

Checkpoint-policy drift requests cancellation of an active Attempt. If the
agent already exited or raced the request, completion terminalizes the Attempt,
releases capacity, leaves the Session blocked on the policy reason, and retains
the worktree. A ref pushed before the final policy check is an unaccepted
orphan. After the admitted policy is restored, retry creates a new Attempt from
the same input; no successor is promoted from the drifted Attempt.

Success completion records output identity, marks the Session succeeded, and
promotes its direct successor in one database transaction. There is no
background gap to recover. With capacity available, the successor becomes
routeable in that transaction. Without capacity, it becomes blocked with the
canonical non-actionable concurrency reason. The existing claim transaction
rechecks such Sessions on each healthy Worker poll and makes one routeable when
both Pipeline and Stage capacity exist, within the current two-second polling
interval. Promotion is idempotent. An expired lease cannot publish completion
even if the Worker pushed its Attempt ref; reconciliation reports the orphan,
but it does not advance the Track or conflict with a later Attempt.

When a Session fails, every later waiting Session in that Track becomes
skipped and records the failed Session as its cause. Retrying the failed
Session reopens the Run, returns only never-started successors skipped by that
failure to waiting, and uses the same input commit. A successor that has an
Attempt is never reset. Run cancellation records a different skip cause, and a
Session retry cannot reopen a Run cancelled by the operator.

Cancelling a Run first commits its cancellation timestamp, then requests
cancellation for active Sessions and marks waiting or concurrency-blocked
Sessions skipped. Cancelling one Session commits a Track cancellation timestamp
and applies the same rule to that Track. These timestamps are promotion fences:
a late completion is rejected and cannot store a winning checkpoint or promote
a successor. The Worker may already have pushed an orphan Attempt ref before it
learns of cancellation; the server records only a bounded orphan event for
audit and the Session resolves cancelled. A cancelled Run or Track cannot be
reopened by Session retry; the operator starts a new Pipeline Run. Editing,
archiving, or disabling the schedule affects future scheduled occurrences only.

`waiting` is neither claimable nor terminal. `skipped` is terminal. A Run is
active while any Session is blocked, queued, preparing, running, or waiting.
The canonical concurrency block counts as active but not actionable. A Run
needs attention when another actionable blocked or failed Session exists. It
becomes terminal when every Session is succeeded, failed, cancelled, or
skipped. Terminal aggregate state uses these ordered, mutually exclusive
predicates:

1. `succeeded` when every Track completed every Stage successfully.
2. `partial` when at least one Track completed every Stage successfully but
   not every Track did.
3. `failed` when no Track completed every Stage and at least one Session
   failed.
4. `cancelled` when no Track completed every Stage, no Session failed, and a
   Run or Track cancellation timestamp exists.

An accepted intermediate checkpoint does not by itself complete a Track.
Therefore cancellation before any checkpoint and cancellation after an
intermediate checkpoint are both `cancelled`; completed plus cancelled or
failed Tracks are `partial`; and failed plus cancelled Tracks with no completed
Track are `failed`.

A Worker that cannot fetch an input checkpoint rejects the claim before agent
startup. The Session becomes blocked with the exact Git failure and may route
to another eligible Worker. If no Worker can write checkpoint refs, the first
Stage may run but cannot succeed. The UI explains the missing repository write
capability and retains the worktree.

Server shutdown stops admission and scheduling before closing HTTP listeners,
as it does today. Active Worker leases and Attempt recovery are unchanged.

## 8. Security, privacy, and operations

The local operator and remote Worker trust boundaries do not change. Only the
authenticated Worker that owns the active Attempt lease may report checkpoint
identity. The server validates IDs, commit format, ref shape, Stage position,
and predecessor equality. It does not trust agent output to name the next
commit.

Repository prompts, results, events, branch names, and checkpoint metadata may
contain sensitive project information and keep their current local retention
rules. Git contents and credentials remain on Workers and origins. Checkpoint
refs expose code to everyone who can already read that repository.

Multi-stage Pipelines require repository write access from eligible Workers.
Single-stage Pipelines keep the current behavior and do not require an
automatic checkpoint unless another Stage consumes it. A repository without
write access is reported as incompatible before a multi-stage Run when the
Worker can prove that fact, and at checkpoint publication otherwise.

The dedicated checkpoint credential is available only to the Worker parent
process and its checkpoint publisher. The runtime supervisor and agent receive
the normal repository credential, whose effective origin policy denies every
checkpoint-namespace operation. Redaction, process-environment tests, and
worktree-config tests prove the dedicated secret does not cross that boundary.

At most 500 Sessions are planned per Run, 100 are active, 20 Stages are stored
per Pipeline, and Pipeline prompts total at most 256 KiB. List APIs remain
cursor bounded. Attempt event and result limits remain unchanged.

Checkpoint refs are durable delivery evidence and are not deleted
automatically in V1. The repository detail page reports their count and age.
Factory provides no deletion operation in V1. Retention and
pull-request-aware cleanup require a separate design that preserves accepted
commit reachability and updates the protected origin policy safely.

## 9. Acceptance criteria

- `AC-1`: An operator can create a three-Stage Pipeline for two repositories
  and start one Run without creating or invoking separate Tasks.
- `AC-2`: With capacity available, each repository makes Stage 2 claimable in
  the same transaction that accepts its Stage 1 checkpoint, independent of the
  other repository.
- `AC-3`: Stage 2 starts from the exact full commit reported by Stage 1, even
  when another Worker claims it.
- `AC-4`: A failed checkpoint publication leaves Stage 2 unstarted, shows an
  actionable reason, and retains the Stage 1 worktree.
- `AC-5`: Retrying a failed Stage reuses its frozen input and advances only
  successors in the same Track after success.
- `AC-6`: The landing view shows Pipeline Run groups, Stage columns,
  repository cards, attention reasons, runtime activity, and recent outcomes
  at desktop and 390-pixel widths.
- `AC-7`: Selecting a repository card opens Run detail with every Stage,
  Session, and Attempt for that Track. Input, output, and checkpoint fields are
  shown when the Session contract recorded them. A one-stage or pre-Pipeline
  historical Session shows unavailable fields without synthetic values.
- `AC-8`: Editing or reordering a Pipeline leaves an active Run and all its
  Stage identities unchanged.
- `AC-9`: Manual and scheduled admission create the same Pipeline Run, Track,
  and Session shape.
- `AC-10`: A current database upgrades each Task and historical Run to a
  one-Stage Pipeline without losing Attempts, events, results, failures, or
  retained-worktree links.
- `AC-11`: The UI calls the authoring resource Pipeline and does not expose a
  second general-purpose Task board.
- `AC-12`: A current one-Stage Pipeline still runs successfully without
  requiring repository write access for an unused downstream checkpoint.
- `AC-13`: A pushed checkpoint followed by response loss or lease expiry does
  not block a new Attempt; only the winning Attempt ref feeds the successor.
- `AC-14`: A pending scheduled occurrence retains its frozen Stage order and
  settings after the Pipeline is edited.
- `AC-15`: Cancellation committed concurrently with Stage success never makes
  the successor claimable.
- `AC-16`: A preparation failure releases capacity and allows a second eligible
  Worker to claim and complete the same Session execution cycle.
- `AC-17`: Checkpoint publication fails and retains the worktree when an agent
  replaces the frozen input history or changes the repository origin.
- `AC-18`: Multi-stage admission rejects a repository unless a supported
  provider proves that only the dedicated Factory principal can create
  checkpoint refs and no principal can update or delete an existing ref.
- `AC-19`: An untracked file is never uploaded automatically; checkpoint
  creation fails until the agent stages, commits, ignores, or removes it.
- `AC-20`: The largest valid resolved Stage prompt plus trusted context fits the
  72 KiB complete-input limit, and the next byte is rejected before execution.
- `AC-21`: With Pipeline and Stage concurrency set to one, a promoted successor
  runs after its predecessor releases the slot; waiting and blocked Sessions do
  not deadlock the Track.
- `AC-22`: Changing the admitted checkpoint policy during an active Run blocks
  checkpoint acceptance and later-Stage claims until the admitted policy is
  restored or the Run is cancelled.

## 10. Test approach

Store tests prove admission, independent Track promotion, aggregate state,
retry, cancellation races, every terminal aggregate predicate, generation
snapshots, concurrency-block promotion, persisted policy fences, and
persistence-related limits for `INV-1` through `INV-17`, `AC-1`, `AC-2`,
`AC-5`, `AC-8`, `AC-9`, `AC-14`, `AC-15`, and `AC-21`.
Aggregate fixtures include cancellation before any checkpoint, cancellation
after an intermediate checkpoint, all Tracks cancelled, completed plus
cancelled Tracks, and failed plus cancelled Tracks.

Worker integration tests use two local clones of one bare origin. Stage 1 runs
on the first Worker, publishes a checkpoint, and Stage 2 runs on the second.
The test compares full commit IDs and file contents for `INV-5` through
`INV-9`, `AC-3`, and `AC-4`. Failure cases cover a rejected push, an existing
conflicting ref, an orphaned Attempt ref after response loss or lease expiry,
preparation failover without a capacity leak, dirty work, no-change success,
an agent-pushed working branch, reset or rebased history, an amended input
commit, a changed origin, an untracked credential-like file, origin rejection
of checkpoint update or deletion, denial of checkpoint creation with the agent
credential, absence of the dedicated credential from the agent environment and
worktree, and a staged specification file consumed by the next Stage. These
cases also prove `INV-18`, `AC-13`, `AC-16`, `AC-17`, `AC-18`, and `AC-19`.

Migration tests build the last pre-Pipeline schema with active, succeeded,
failed, cancelled, retried, and retained Sessions. They compare every source
row and payload after migration for `INV-14` and `AC-10`, and prove the
migration refuses name collisions or incomplete source state without changing
the database. Pending-occurrence fixtures cover enabled, disabled, archived,
blocked, paused, and retrying schedules, preserve their frozen source snapshot,
and prove `INV-16` and `AC-14`.

HTTP contract tests cover Pipeline validation, pagination, immutable snapshots,
claim fields, malformed checkpoint rejection, checkpoint-protection evidence,
complete-input byte boundaries, and local or remote route boundaries for
`INV-1`, `INV-7`, `INV-13`, `INV-18`, `AC-18`, and `AC-20`. Provider-policy
integration tests change protection after admission and prove the periodic,
pre-claim, and pre-acceptance fences for `AC-22`.

Schedule tests prove a pending occurrence retains its complete immutable
Pipeline generation across later edits for `INV-16` and `AC-14`.

React tests prove card grouping, sorting, attention reasons, links, empty and
failure states, and keyboard names. A real Chromium test runs the three-Stage,
two-repository flow at desktop and 390-pixel widths, checks console and failed
requests, and proves `AC-1`, `AC-6`, `AC-7`, `AC-9`, `AC-11`, and `AC-12`.
At both widths it asserts logical focus order, visible focus indicators, Stage
and Track navigation, and Enter and Space activation.

## 11. Risks and tradeoffs

- Automatic checkpoint commits may include an unintended tracked change. The
  prompt names the contract, the Worker shows the diff summary in events, and
  the immutable ref makes the exact handoff inspectable.
- Remote checkpoint refs accumulate. V1 reports them and keeps them because
  deleting code evidence incorrectly is worse than storage growth. Retention
  needs a pull-request-aware follow-up design.
- Protected checkpoint namespaces and a dedicated credential add repository
  setup. V1 accepts that cost because ordinary write credentials cannot provide
  immutable handoff. Origins without a supported policy verifier remain
  one-stage only.
- Another process could create an Attempt checkpoint ref first. Create-only
  push and read-back turn that race into a visible conflict for that Attempt.
  A retry receives a different ref and is not blocked by the conflict.
- A linear model cannot express parallel tests or approval fan-in. It still
  validates the hard parts of sequencing. Explicit predecessor identity keeps
  a later DAG migration possible.
- Renaming Task again is disruptive. Keeping the old name would preserve a
  short-lived API while making the product boundary unclear. Developer preview
  status and a lossless migration make the clean name preferable.
- Multi-repository Runs may show many cards. The 500-Session admission bound,
  collapsed completed Tracks, filters, and virtualized detail lists keep the
  first view bounded.

## 12. Open questions

- Should the Worker use a fixed Factory Git author or preserve the agent's
  configured author for automatic checkpoint commits? Recommend a fixed
  `Factory Pipeline <factory@local>` identity so provenance is explicit. This
  does not block task breakdown.
- Should the final checkpoint ref be renamed to a human branch when a Pipeline
  succeeds? Recommend no. A later Publish Stage should create or update the
  human branch and pull request explicitly. This does not block task breakdown.

## 13. Out of scope

- Branching DAGs, parallel Stage execution inside one Track, and fan-in.
- Human approval, question, and resume nodes.
- Conditional Stages or prompt expressions based on earlier results.
- Automatic merge, deployment, rollback, or pull-request publication.
- Multi-stage cloud and fake-cloud execution profiles.
- Multi-stage Pipelines on Git origins without a supported protection-policy
  verifier.
- General business tasks, people assignment, due dates, and personal planning.
- Cross-repository dependencies inside one Track.
- Passing agent result prose directly into a successor Stage prompt.
- Moving Git file contents or credentials through the control plane.
- Automatic checkpoint-ref retention or deletion.
