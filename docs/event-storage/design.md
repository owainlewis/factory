# Database-portable attempt-event storage

> **Status:** Proposed, not implemented
>
> **Tracks:** [GitHub issue #175](https://github.com/owainlewis/factory/issues/175)
>
> **Related:** [Event retention issue #174](https://github.com/owainlewis/factory/issues/174)

## 1. Decision summary

Factory will put attempt-event persistence behind a semantic `EventRepository` boundary. The boundary describes append, exact replay, ordered pagination, usage accounting, retention selection, and bounded deletion. It does not expose SQL, a transaction handle, SQLite pragmas, or PostgreSQL types to HTTP handlers or workers.

SQLite remains the only supported database in the current product. The first implementation will move existing SQLite behavior behind the boundary without changing the event protocol. A PostgreSQL implementation is follow-up work, not part of this design's implementation slice.

Each repository implementation owns the complete transaction for an operation. In particular, append atomically checks the active attempt lease, serializes writers for that attempt, classifies replays and conflicts, enforces the byte budget, and inserts new events. This is a semantic guarantee rather than an incidental consequence of SQLite's immediate write transactions.

Portable behavior is tested once as a repository compatibility suite and run against every backend. Database opening, migration execution, SQLite WAL checkpointing and page maintenance remain backend lifecycle concerns outside `EventRepository`.

## 2. Context and current behavior

Attempt events are currently stored in SQLite by `Store.AppendEvents` and read by `Store.Events`. The `attempt_events` table has:

- `(attempt_id, sequence)` as its primary key;
- `kind` as text;
- `payload` as a BLOB containing the exact JSON bytes received;
- `payload_bytes` as the byte-budget value; and
- `server_time` as UTC Unix milliseconds assigned by the control plane.

The current observable contract is:

- A batch contains 1 through 100 events and is at most 256 KiB after request encoding.
- An event sequence is non-negative and strictly increases within a batch. Gaps are allowed.
- `kind` is nonblank and no longer than 100 bytes.
- `payload` is valid JSON and at most 64 KiB. Its original bytes are significant.
- An attempt may store at most 10 MiB of payload bytes. Metadata and database page overhead are not counted.
- The lease must own an attempt in `preparing` or `running`, and `lease_expires_at` must be later than the server's current time.
- Repeating an existing `(attempt_id, sequence, kind, payload bytes)` is successful and adds no bytes. The original `server_time` remains.
- Reusing a sequence with a different kind or different payload bytes is `event_conflict`.
- A new sequence less than or equal to the stored maximum is `event_out_of_order`. New sequences need not be contiguous.
- All new rows in one accepted batch receive one server timestamp.
- Reads use an exclusive sequence cursor, ascending order, and `limit + 1` lookahead. The public default is 100 and maximum is 500.
- Events remain readable after the attempt becomes terminal. Today they are deleted only when the complete terminal task history is deleted.

SQLite file databases use foreign keys, WAL mode, a five-second busy timeout, and immediate transactions. Immediate transactions currently serialize writers before append computes `SUM(payload_bytes)` and `MAX(sequence)`. Payloads scan as `[]byte`; timestamps scan as signed integers. Most unexpected driver errors are reported as `storage_unavailable`.

These details explain current behavior, but they are not all portable requirements. PostgreSQL must not emulate WAL checkpointing, SQLite page reuse, or a global database write lock.

## 3. Goals and non-goals

### Goals

- Preserve the HTTP and worker event protocol and all behavior listed above.
- Give retention work in issue #174 portable selection and deletion operations.
- Preserve ordering, exact-byte replay, lease fencing, and budget enforcement under concurrent writers.
- Isolate placeholder syntax, transaction and locking mechanics, timestamp and binary mappings, and driver error classification.
- Define compatibility tests that both SQLite and PostgreSQL must pass.
- Define a migration path that does not require coordinated worker or browser changes.
- Record PostgreSQL schema, indexes, cleanup, and operational expectations before implementation begins.

### Non-goals

- Shipping or requiring PostgreSQL now.
- Selecting a general ORM or query builder.
- Moving payloads to files, object storage, or another service.
- Changing event JSON, endpoints, cursors, status codes, or worker retry behavior.
- Weakening validation or changing exact-byte JSON equality to semantic JSON equality.
- Designing retention duration, configuration names, or operator UI beyond the storage operations required by issue #174.
- Making all control-plane persistence portable in this slice.

## 4. Boundary and ownership

The control-plane service continues to own HTTP decoding, request-size limits, basic event validation, authentication input, and conversion of repository outcomes to existing `ServiceError` codes. The event repository owns all durable event state and every database operation whose correctness depends on that state.

The proposed internal interface is semantic; exact Go names may change during implementation:

```go
type EventRepository interface {
    Append(context.Context, AppendEventsCommand) (AppendEventsResult, error)
    Page(context.Context, EventPageQuery) (EventPage, error)
    Usage(context.Context, AttemptID) (EventUsage, error)
    SelectExpired(context.Context, ExpiryQuery) ([]ExpiredAttempt, error)
    DeleteExpired(context.Context, DeleteExpiredCommand) (DeleteResult, error)
}
```

`AppendEventsCommand` carries the attempt ID, a digest of the presented lease token, the validated events, the server's UTC `now`, and the configured byte limit. It never carries the raw lease token. `AppendEventsResult` distinguishes accepted new events from a wholly or partly idempotent replay for metrics only; the HTTP result remains unchanged.

`Page` takes `attempt_id`, exclusive `after`, and `limit`, and returns the existing event page shape. It must distinguish a missing attempt from an existing attempt with no retained events.

`Usage` returns logical payload bytes and event count. It is for retention metrics and diagnostics; append correctness must not depend on a non-transactional call to it.

`SelectExpired` returns a stable, bounded page of attempt IDs eligible under the retention cutoff and hold rules. `DeleteExpired` rechecks eligibility, deletes at most the supplied row and byte bounds, is idempotent, and reports rows and logical bytes removed.

Terminal task-history deletion is intentionally not an `EventRepository` operation. The backend-level control-plane repository owns that aggregate transaction, including event rows, claim requests, attempts, the execution, the task, and related automation updates. Its SQLite and PostgreSQL implementations either delete event rows explicitly or rely on a tested `ON DELETE CASCADE` foreign key before committing once. This keeps today's all-or-nothing deletion without exposing a transaction handle or allowing an independently committed event deletion.

The interface uses domain values and typed outcomes, not `*sql.DB`, `*sql.Tx`, SQL fragments, placeholder strings, or driver errors. A repository implementation must not call back into service code while its transaction is open.

### Transaction boundary with attempt state

Append cannot be implemented safely as an event-only transaction followed by a separate lease check. Each backend's event repository therefore has read access to the durable attempt lease fields and performs lease fencing inside the same transaction as replay checks, budget enforcement, and insertion.

Likewise, expiry deletion rechecks terminal completion and retention holds in the deletion transaction. This intentional coupling is narrower and safer than exposing general control-plane transactions through the interface. If a future deployment stores attempts and events in different databases, it will need a new consistency design; that is not implied by this boundary.

## 5. Portable data contract

Both backends persist the same logical columns:

| Value | Portable meaning | SQLite mapping | PostgreSQL mapping |
| --- | --- | --- | --- |
| `attempt_id` | Opaque attempt identifier | `TEXT` | `text` |
| `sequence` | Non-negative signed 64-bit sequence | `INTEGER` | `bigint` |
| `kind` | Validated UTF-8 event kind | `TEXT` | `text` |
| `payload` | Exact, uninterpreted JSON bytes | `BLOB` | `bytea` |
| `payload_bytes` | Length of `payload` at insertion | `INTEGER` | `bigint` |
| `server_time` | Server-assigned UTC instant, millisecond precision | Unix milliseconds `INTEGER` initially | `timestamptz` |

Payload is deliberately binary rather than SQLite text or PostgreSQL `jsonb`. `jsonb` would normalize whitespace, key order, and number spelling and would break exact replay behavior. Application validation guarantees that the bytes are valid JSON before persistence. Implementations copy scanned byte slices before returning them.

The domain boundary uses `time.Time` normalized to UTC and truncated to milliseconds. The SQLite adapter converts to and from Unix milliseconds. The PostgreSQL adapter sends and scans UTC timestamps. Compatibility tests compare instants after millisecond normalization, not database-native formatting.

`payload_bytes` is checked against `len(payload)` on every insert. New schemas add `CHECK (payload_bytes >= 0)` and, where supported without changing behavior, `CHECK (payload_bytes = octet_length(payload))`. Existing SQLite rows are validated during migration before adding stricter constraints. Counters use signed 64-bit values in Go and SQL; no platform-sized `int` is used for stored usage.

## 6. Append, replay, and concurrency

Append is one transaction with this required logical order:

1. Load and lock the attempt row for append serialization.
2. Verify attempt state, constant-time lease-digest equality, and `lease_expires_at > now`.
3. Load current event usage and maximum sequence for that attempt.
4. For every requested sequence, load any existing row.
5. Accept an exact kind-and-payload-byte replay without changing its timestamp or usage.
6. Reject a different existing value as `event_conflict`.
7. Reject a new sequence at or below the stored maximum as `event_out_of_order`.
8. Sum only new payload bytes and reject a total over 10 MiB as `event_budget_exceeded`.
9. Insert all new rows with one server timestamp and commit.

Any failure rolls back the complete batch. A response lost after commit is safe because the worker retries the same sequence and bytes.

The SQLite adapter retains immediate write transactions initially. Its lock remains database-wide, so different-attempt writes may serialize even though their state is independent. The PostgreSQL adapter uses `SELECT ... FOR UPDATE` on the attempt row before reading event state, allowing concurrent appends to different attempts while appends to one attempt serialize. PostgreSQL inserts use `INSERT ... ON CONFLICT DO NOTHING RETURNING` (or an equivalent savepoint) so conflict inspection does not leave the transaction aborted. The adapter then rereads and compares the row before classifying an exact replay or `event_conflict`. Locking the attempt row should prevent this path during normal repository writes, but classification remains safe if pre-existing or externally written data causes a collision.

This design does not require contiguous sequence allocation, a process-local mutex, sticky sessions, or one database connection per attempt.

## 7. Pagination and read behavior

The repository implements the existing query semantics:

```sql
attempt_id = requested_attempt
AND sequence > after
ORDER BY sequence ASC
LIMIT limit + 1
```

The primary key beginning with `(attempt_id, sequence)` supports this scan in both databases. The extra row sets `has_more`; it is not returned. `next_after` is the last returned sequence, or the caller's `after` value when the page is empty.

A missing attempt returns the existing not-found outcome. An existing attempt whose events have expired returns an empty page; issue #174 may expose retention metadata elsewhere, but this design does not change the event endpoint. Database cancellation, timeout, connection, decode, or corruption errors return `storage_unavailable` and must not be disguised as an empty page.

Each page is a committed database snapshot for that statement. Pages are not a snapshot of the whole stream: events appended after one page may appear on the next page, as they do today. Monotonic sequence keys prevent duplicates when the exclusive cursor advances.

## 8. Retention and bounded deletion

Issue #174 can use `SelectExpired` and `DeleteExpired` without embedding backend-specific SQL in a scheduler. Eligibility is based on durable database state, never process memory:

- the attempt is terminal and has a non-null completion time at or before the cutoff;
- worktree capacity/disposition has been acknowledged; and
- no current retention hold exists for the attempt.

The current retained-worktree snapshot is JSON on `workers`. Retention implementation must materialize explicit holds as normalized durable state keyed by attempt ID (for example, an `attempt_event_retention_holds` table). It must also represent an ambiguity barrier for accepted legacy/display-only reports that cannot be attributed to one attempt. The simplest safe representation is one durable barrier per worker; while present, expiry excludes all attempts owned by that worker. A narrower repository barrier is allowed only when attribution is unambiguous. The service replaces keyed holds and the barrier transactionally with each accepted complete worker report. An incomplete, conflicting, or duplicate report cannot silently clear prior holds or a barrier; only a later complete, internally consistent snapshot may clear it in the same transaction that installs its keyed holds.

Cleanup queries must not use SQLite `json_each`, PostgreSQL JSON operators, or application-side snapshots to decide safety. Migration of existing worker snapshots into normalized holds belongs to issue #174 and has backend-specific SQL behind its migration adapter. Migration fails closed: malformed JSON, incomplete entries, duplicate attempt IDs with inconsistent repositories, or conflicting worker reports stop startup with an actionable error or install a conservative worker barrier, and never make ambiguous events eligible for expiry. This preserves today's ability to accept display-only retained-worktree summaries without interpreting missing identifiers as permission to delete.

`SelectExpired` orders candidates by `(completed_at, attempt_id)` and applies a candidate limit. A cleanup loop passes the last tuple as its cursor; it does not use an offset. Selection does not reserve or permanently mark rows.

`DeleteExpired` accepts a small candidate set plus maximum rows and logical bytes for one transaction. It locks/rechecks each candidate, skips attempts that became ineligible, deletes a bounded sequence range, and commits. Partial cleanup is expected: later sweeps continue from remaining rows, and an attempt stays eligible until all its event rows are gone. Repeating a batch after a crash is safe and counts only rows actually removed.

Task deletion remains one backend-level aggregate transaction. While all control-plane records share one database, it removes events and task history together or rolls back both. A backend may use foreign-key cascade, but integration tests must prove event removal, rollback on injected failure, and unrelated-history preservation.

Retention removes rows, not task, execution, attempt, result, error, timing, or lease metadata. SQLite reuses free pages and is not expected to shrink its database file after deletion. WAL checkpoint and optional compaction are separate operator lifecycle actions and must not run inside a retention transaction. PostgreSQL relies on normal autovacuum; cleanup does not issue `VACUUM`.

## 9. SQL and backend adaptation

SQL stays in concrete adapters. Portable service code does not rewrite placeholders. The SQLite adapter uses `?`; the PostgreSQL adapter uses `$1`, `$2`, and so on. Queries are maintained separately when locking, delete limits, time arithmetic, or returned-row syntax differs. A small shared scanner or domain helper is acceptable; a lowest-common-denominator SQL templating layer is not required.

Transactions use backend defaults plus explicit locks needed by the operation:

- SQLite starts an immediate transaction for append and bounded deletion.
- PostgreSQL uses `READ COMMITTED` with row locks for append and candidate deletion. Lock ordering is by attempt ID when multiple attempts are touched.
- Retention workers on PostgreSQL may use `FOR UPDATE SKIP LOCKED` for bounded candidate processing, but correctness cannot depend on all processes seeing the same candidate page.
- Serialization failures, deadlocks, lock timeouts, and transient connection failures are retryable storage failures. The scheduler may retry; the HTTP layer retains its existing `storage_unavailable` response.

Adapters classify errors into a small internal set: not found, replay conflict, out of order, lease not owner, budget exceeded, retryable storage failure, and permanent storage failure. Classification uses driver error codes (`SQLSTATE` in PostgreSQL and extended result codes in SQLite), never error-message substring matching. Domain outcomes are decided from locked data whenever possible; raw unique-constraint errors do not determine replay semantics by themselves.

Database startup, connection pools, migration ledgers, health checks, and shutdown are owned by a backend-level control-plane database component. SQLite-specific marker files, DSN pragmas, WAL checkpoint retries, page reuse, and optional vacuuming do not appear in `EventRepository`. PostgreSQL-specific DSNs, TLS, pool sizing, statement timeouts, advisory operational checks, autovacuum tuning, and connection shutdown stay in its backend component.

## 10. PostgreSQL design target

The PostgreSQL implementation retains a relational `attempt_events` table:

```sql
CREATE TABLE attempt_events (
    attempt_id text NOT NULL REFERENCES attempts(id),
    sequence bigint NOT NULL CHECK (sequence >= 0),
    kind text NOT NULL,
    payload bytea NOT NULL,
    payload_bytes bigint NOT NULL CHECK (
        payload_bytes >= 0 AND payload_bytes = octet_length(payload)
    ),
    server_time timestamptz NOT NULL,
    PRIMARY KEY (attempt_id, sequence)
);
```

Expected indexes are:

1. `PRIMARY KEY (attempt_id, sequence)` for identity, replay lookup, ordered pagination, per-attempt bounded deletion, and maximum-sequence lookup.
2. An attempts-side partial expiry index on terminal rows, beginning with `(completed_at, id)`, for retention candidate order.
3. `PRIMARY KEY (attempt_id)` on normalized retention holds, if issue #174 uses the proposed hold table.

No separate event index is required for kind or timestamp because current APIs do not filter by either. PostgreSQL implementation should verify plans with realistic retained volumes before adding indexes. An event-count/byte summary row may be introduced only if profiling shows repeated aggregation is material; it must be updated in the append/deletion transaction and rebuilt/verifiable from event rows.

Cleanup uses small transactions, deterministic keyset order, row locks, and `DELETE ... RETURNING payload_bytes` to produce accurate metrics. Multiple cleanup processes may divide work with `SKIP LOCKED`. Normal autovacuum reclaims tuples; operators monitor dead tuples, transaction age, and table/index size. Scheduled `VACUUM FULL` is not part of normal cleanup because it takes disruptive locks.

PostgreSQL operational requirements for follow-up implementation include supported server versions, TLS and credential handling, pool and statement timeout defaults, migration serialization (for example, a migration advisory lock), backup/restore validation, high-availability failover behavior, autovacuum monitoring, and documented recovery-point expectations.

## 11. Migration and rollout

The first implementation is an internal SQLite refactor:

1. Add the interface and SQLite adapter around the existing schema.
2. Move append, page, usage, and deletion SQL without changing HTTP handlers or protocol types.
3. Run the compatibility suite against SQLite and preserve existing integration tests.
4. Let issue #174 add normalized retention metadata and bounded cleanup through this boundary.

No event row rewrite is needed for that refactor. A migration may validate `payload_bytes`, sequence range, JSON validity, and timestamp range and must stop with an actionable error rather than silently repair content that determines replay behavior. The retention-hold migration separately tests malformed snapshots, incomplete entries, duplicate attempt IDs, and conflicting reports; every ambiguous case fails startup or remains conservatively held.

PostgreSQL support is split into follow-up work:

- make the wider control-plane database boundary and migrations PostgreSQL-capable;
- implement the PostgreSQL event adapter and run the same compatibility suite;
- add SQLite-to-PostgreSQL data migration tooling with counts, logical byte totals, per-attempt maximum sequences, and payload hashes as verification;
- define a maintenance-window or dual-read rollout and rollback procedure; and
- document PostgreSQL deployment, backup, restore, monitoring, and cleanup operations.

Because attempts and events must share an append transaction, event tables are not migrated independently while writes continue. The initial supported migration is an offline copy: stop writers, take a consistent SQLite checkpoint/snapshot, copy all control-plane tables, verify invariants, then switch the server. Workers need no protocol change; failed in-flight requests retry against the selected database. Rollback is allowed only before PostgreSQL accepts new writes unless a reverse migration is completed.

A future online migration requires a separate reviewed design for write fencing, catch-up, cutover, and rollback. Dual-writing without such a protocol is explicitly rejected.

## 12. Compatibility test contract

Every repository implementation must run the same black-box tests using a real temporary database, not a mocked SQL API. The suite names and proves:

1. **Append and restart durability:** accepted events survive repository close/reopen in ascending sequence order with exact payload bytes and UTC millisecond timestamps.
2. **Exact replay:** full and mixed old/new replays succeed, add bytes once, preserve original timestamps, and tolerate sequence gaps.
3. **Replay conflict:** different kind or any payload-byte difference at an existing sequence returns `event_conflict`, including JSON whitespace, key-order, and integers beyond IEEE-754 exact range.
4. **Out-of-order append:** a previously unused sequence below the stored maximum returns `event_out_of_order` and commits nothing.
5. **Lease fencing:** wrong, expired, inactive, and concurrently superseded leases cannot append.
6. **Concurrent writers:** same-attempt appends serialize without exceeding the byte budget; exact concurrent replay succeeds; conflicting concurrent replay has one winner; different-attempt writes never interfere with each other's event state and eventually progress. Observable parallel progress is PostgreSQL-specific because SQLite immediate transactions serialize database writers.
7. **Atomic batch:** validation, conflict, budget, cancellation, or injected statement/commit failure leaves no partial new batch.
8. **Byte budget:** only exact payload bytes count, exact-limit storage succeeds, one byte over fails, and replay consumes no additional budget.
9. **Ordered pagination:** exclusive cursors, allowed gaps, limit lookahead, empty pages, `next_after`, and concurrent tail appends match the current contract.
10. **Missing versus empty:** a missing attempt is not found, while existing, never-populated, and fully-expired attempts return an empty page.
11. **Expiry selection and hold normalization:** active, recent terminal, unacknowledged-disposition, and held attempts are excluded; old eligible attempts are returned in stable keyset order; malformed, incomplete, duplicate, and conflicting retained-worktree snapshots fail closed during migration and runtime updates; an ambiguity barrier persists until a complete consistent report safely replaces it.
12. **Bounded deletion:** row and byte bounds are honored, eligibility is rechecked, partial and repeated sweeps converge, restart is safe, and unrelated events/history remain.
13. **Task deletion:** all events for deleted task attempts disappear atomically and events belonging to other tasks remain.
14. **Representation:** binary JSON round-trips unchanged, timestamps normalize consistently, and malformed stored payload or inconsistent accounting is reported rather than normalized silently.
15. **Error mapping:** cancellation, timeout, unavailable database, uniqueness, foreign-key, serialization, and deadlock paths produce stable domain or storage classifications without leaking driver text.

SQLite additionally tests WAL/checkpoint shutdown and free-page reuse outside the repository suite. PostgreSQL additionally tests real concurrent connections, deadlock/serialization retry behavior, migration locking, cleanup with `SKIP LOCKED`, and query plans using the expected indexes.

Existing store, HTTP, and worker tests remain. They prove that POST/GET shapes, status codes, page metadata, worker retry with identical sequence/content, and payload truncation do not change when the repository boundary is introduced.

## 13. Acceptance criteria for implementation

The initial SQLite event-boundary refactor is ready when:

- the interface owns append, replay, pagination, and usage, while the backend aggregate transaction continues to own atomic task-history and event removal;
- no portable event service code contains SQLite placeholder, pragma, checkpoint, vacuum, JSON-query, or error-code logic;
- append, replay, lease, budget, pagination, representation, restart, error-mapping, and task-deletion compatibility tests pass against file-backed SQLite;
- concurrent append tests prove semantic behavior independently of process-local locking;
- HTTP and worker protocol fixtures are unchanged; and
- architecture documentation is updated when the implemented boundary becomes current behavior.

The issue #174 retention extension is ready when:

- the interface additionally implements expiry selection and bounded expiry deletion;
- normalized keyed holds and conservative ambiguity barriers are updated transactionally and migrated fail closed;
- expiry selection, bounded/repeated deletion, restart, hold, and retention error-path compatibility tests pass; and
- operators can configure and observe retention using only repository operations, with reclamation behavior documented.

No unsupported stub retention methods are required for the initial refactor. The interface may be split into smaller append/read and retention capabilities in Go so each independently reviewable slice is complete.

PostgreSQL is ready only in its follow-up when the applicable complete suite passes against a supported real PostgreSQL version, migration and rollback are proven, required indexes and query plans are verified, cleanup/autovacuum behavior is documented, and backup/restore plus failure recovery have operational evidence.

## 14. Risks and rejected alternatives

- **A generic `*sql.Tx` interface:** rejected because it leaks SQL dialect and lets callers accidentally split semantic operations.
- **One portable SQL string set:** rejected because lock syntax, placeholders, bounded deletes, returned rows, and timestamp conversions genuinely differ.
- **PostgreSQL `jsonb`:** rejected because normalization weakens exact-byte replay and large-number fidelity.
- **Process-local append locks:** rejected because they fail with multiple server processes and do not protect direct database concurrency.
- **Application-side retention filtering of worker JSON:** rejected because eligibility can race and JSON query syntax is database-specific.
- **Deleting all expired rows in one transaction:** rejected because it can stall normal reads/writes, inflate WAL, and create long PostgreSQL transactions.
- **Dual-write migration by default:** rejected because partial commit and rollback would require a distributed consistency protocol.
- **Maintaining summary counters immediately:** deferred until measurements justify their consistency cost; transactional `SUM` and `MAX` preserve current behavior.

The main implementation risk is accidentally preserving SQLite's SQL while losing its implicit concurrency guarantee. The compatibility suite therefore treats concurrent replay, ordering, fencing, and budget enforcement as first-class contract tests. The main operational risk for PostgreSQL is unbounded dead tuples or long cleanup transactions; bounded deletion and autovacuum evidence are release requirements.

## 15. Follow-up work

1. **SQLite event repository refactor:** implement the interface and compatibility suite without behavior changes.
2. **Portable event retention (#174):** add normalized holds, configuration, selection, bounded sweeps, metrics/logging, and SQLite reclamation documentation.
3. **PostgreSQL control-plane foundation:** choose the supported version/driver, adapt all control-plane migrations and repositories, and define deployment operations.
4. **PostgreSQL event repository:** implement locking, SQLSTATE mapping, indexes, cleanup, and backend-specific tests.
5. **Offline migration tooling:** copy and verify complete control-plane state, with documented cutover and rollback.

Each follow-up is independently reviewable. None requires an HTTP or worker event protocol version change.
