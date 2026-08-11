package controlplane

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
	"github.com/owainlewis/factory/migrations"
)

func TestRoutinesMigrationPreservesPopulatedLegacyHistory(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/legacy.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, now: time.Now, sweepEvery: 5 * time.Second}
	t.Cleanup(func() { _ = db.Close() })
	applyMigrationsBeforeRoutines(t, ctx, store)

	_, err = db.ExecContext(ctx, legacyRoutinesFixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE runs SET request_key = 'shared-legacy-key' WHERE id = 'run-scheduled';
		UPDATE tasks SET request_key = 'shared-legacy-key' WHERE id = 'task-webhook';
	`); err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var routineCount, distinctNames int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT name_key) FROM routines WHERE migration_only = 0`).
		Scan(&routineCount, &distinctNames); err != nil {
		t.Fatal(err)
	}
	if routineCount != 4 || distinctNames != 4 {
		t.Fatalf("operator Routines = %d, distinct names = %d", routineCount, distinctNames)
	}
	var routineToolColumns, targetToolColumns int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('routines') WHERE name = 'allowed_tools'`).
		Scan(&routineToolColumns); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('work_targets') WHERE name = 'allowed_tools'`).
		Scan(&targetToolColumns); err != nil {
		t.Fatal(err)
	}
	if routineToolColumns != 0 || targetToolColumns != 0 {
		t.Fatalf("tool configuration survived in product tables: routines %d, targets %d",
			routineToolColumns, targetToolColumns)
	}
	var migratedTaskKey, originalTaskKey string
	if err := db.QueryRowContext(ctx, `
		SELECT request_key, json_extract(routine_snapshot, '$.legacy_task_request_key')
		FROM work WHERE id = 'task-webhook'
	`).Scan(&migratedTaskKey, &originalTaskKey); err != nil {
		t.Fatal(err)
	}
	if migratedTaskKey != "legacy-task:task-webhook" || originalTaskKey != "shared-legacy-key" {
		t.Fatalf("migrated standalone Task keys = %q, original %q", migratedTaskKey, originalTaskKey)
	}
	var migratedRunKey, originalRunKey string
	if err := db.QueryRowContext(ctx, `
		SELECT request_key, json_extract(routine_snapshot, '$.legacy_run_request_key')
		FROM work WHERE id = 'run-scheduled'
	`).Scan(&migratedRunKey, &originalRunKey); err != nil {
		t.Fatal(err)
	}
	if migratedRunKey != "legacy-run:run-scheduled" || originalRunKey != "shared-legacy-key" {
		t.Fatalf("migrated Run keys = %q, original %q", migratedRunKey, originalRunKey)
	}
	var currentPrompt, historicalPrompt string
	var currentArchived, currentReadOnly, historicalArchived, historicalReadOnly bool
	if err := db.QueryRowContext(ctx, `SELECT prompt, archived, read_only FROM routines WHERE id = 'revision-2'`).
		Scan(&currentPrompt, &currentArchived, &currentReadOnly); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT prompt, archived, read_only FROM routines WHERE id = 'revision-1'`).
		Scan(&historicalPrompt, &historicalArchived, &historicalReadOnly); err != nil {
		t.Fatal(err)
	}
	if currentPrompt != "Runbook instructions:\n\nCurrent instructions\n\nRunbook summary:\n\nCurrent summary" ||
		currentArchived || currentReadOnly ||
		historicalPrompt != "Runbook instructions:\n\nInstructions\n\nRunbook summary:\n\nSummary" ||
		!historicalArchived || !historicalReadOnly {
		t.Fatalf("migrated Runbook revisions = current (%q, archived %v, read-only %v), historical (%q, archived %v, read-only %v)",
			currentPrompt, currentArchived, currentReadOnly,
			historicalPrompt, historicalArchived, historicalReadOnly)
	}
	update := protocol.SaveRoutineRequest{
		Name: "Changed", Prompt: "Changed", Runtime: "codex", TimeoutSeconds: 7200,
		ConcurrencyLimit: 10, ExpectedGeneration: 1,
	}
	if _, err := store.UpdateRoutine(ctx, "revision-1", update); !serviceErrorCode(err, "routine_read_only") {
		t.Fatalf("historical revision update error = %v", err)
	}
	if _, err := store.SetRoutineArchived(ctx, "revision-1", protocol.SetRoutineArchivedRequest{
		Archived: boolPointer(false), ExpectedGeneration: 1,
	}); !serviceErrorCode(err, "routine_read_only") {
		t.Fatalf("historical revision restore error = %v", err)
	}
	if _, _, err := store.RunRoutine(ctx, "revision-1", protocol.RunRoutineRequest{
		RequestKey: "historical-revision-run",
	}); !serviceErrorCode(err, "routine_read_only") {
		t.Fatalf("historical revision run error = %v", err)
	}

	var issueState, issueLabels, pullState, pullLabels, pullBranches string
	var includeDrafts bool
	if err := db.QueryRowContext(ctx, `
		SELECT
			json_extract(provider_snapshot, '$.issue.configured_state'),
			json_extract(provider_snapshot, '$.issue.required_labels[0]')
		FROM work WHERE id = 'task-issue'
	`).Scan(&issueState, &issueLabels); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT
			json_extract(provider_snapshot, '$.pull_request.configured_state'),
			json_extract(provider_snapshot, '$.pull_request.required_labels[0]'),
			json_extract(provider_snapshot, '$.pull_request.base_branches[0]'),
			json_extract(provider_snapshot, '$.pull_request.include_drafts')
		FROM work WHERE id = 'task-pr'
	`).Scan(&pullState, &pullLabels, &pullBranches, &includeDrafts); err != nil {
		t.Fatal(err)
	}
	if issueState != "open" || issueLabels != "ready" || pullState != "open" ||
		pullLabels != "review" || pullBranches != "main" || !includeDrafts {
		t.Fatalf("provider snapshots lost matching criteria: issue=(%q,%q), pull=(%q,%q,%q,%v)",
			issueState, issueLabels, pullState, pullLabels, pullBranches, includeDrafts)
	}
	var webhookTitle, webhookBranch, webhookDefinition, webhookParameter string
	if err := db.QueryRowContext(ctx, `
		SELECT
			json_extract(provider_snapshot, '$.webhook.pull_request_title'),
			json_extract(provider_snapshot, '$.webhook.base_branch'),
			json_extract(provider_snapshot, '$.webhook.definition_id'),
			json_extract(provider_snapshot, '$.webhook.parameters.priority')
		FROM work WHERE id = 'task-webhook'
	`).Scan(&webhookTitle, &webhookBranch, &webhookDefinition, &webhookParameter); err != nil {
		t.Fatal(err)
	}
	if webhookTitle != "Webhook PR" || webhookBranch != "main" ||
		webhookDefinition != "definition-1" || webhookParameter != "high" {
		t.Fatalf("webhook snapshot = %q, %q, %q, %q", webhookTitle, webhookBranch, webhookDefinition, webhookParameter)
	}

	var historyRoutine, workflowID, workflowRevision, workflowTitle string
	if err := db.QueryRowContext(ctx, `
		SELECT routine_id,
			json_extract(routine_snapshot, '$.legacy_workflow_id'),
			json_extract(routine_snapshot, '$.legacy_workflow_revision_id'),
			json_extract(routine_snapshot, '$.legacy_workflow_title')
		FROM work WHERE id = 'task-workflow'
	`).Scan(&historyRoutine, &workflowID, &workflowRevision, &workflowTitle); err != nil {
		t.Fatal(err)
	}
	if historyRoutine != "00000000-0000-4000-8000-000000000103" || workflowID != "workflow-1" ||
		workflowRevision != "revision-1" || workflowTitle != "Shared" {
		t.Fatalf("workflow history = %q, %q, %q, %q", historyRoutine, workflowID, workflowRevision, workflowTitle)
	}

	var pendingDue int64
	var pendingRepository string
	var retryAt sql.NullInt64
	var scheduleHealth string
	if err := db.QueryRowContext(ctx, `
		SELECT pending_due_at, json_extract(pending_snapshot_json, '$.repository_ids[0]'),
			schedule_retry_at, schedule_health_status
		FROM routines WHERE id = 'automation-schedule'
	`).Scan(&pendingDue, &pendingRepository, &retryAt, &scheduleHealth); err != nil {
		t.Fatal(err)
	}
	if pendingDue != 2000 || pendingRepository != "repo-1" || retryAt.Valid || scheduleHealth != "blocked" {
		t.Fatalf("pending schedule = %d, %q, retry %v, health %q", pendingDue, pendingRepository, retryAt, scheduleHealth)
	}
	var scheduledAt int64
	var scheduleOccurrence, scheduleKind, scheduleCron, scheduleTimezone, legacyTool string
	if err := db.QueryRowContext(ctx, `
		SELECT scheduled_at,
			json_extract(routine_snapshot, '$.legacy_schedule_occurrence_id'),
			json_extract(routine_snapshot, '$.legacy_schedule_kind'),
			json_extract(routine_snapshot, '$.cron'),
			json_extract(routine_snapshot, '$.timezone'),
			json_extract(routine_snapshot, '$.legacy_allowed_tools[0]')
		FROM work WHERE id = 'run-scheduled'
	`).Scan(&scheduledAt, &scheduleOccurrence, &scheduleKind, &scheduleCron, &scheduleTimezone, &legacyTool); err != nil {
		t.Fatal(err)
	}
	if scheduledAt != 1500 || scheduleOccurrence != "occurrence-schedule-admitted" ||
		scheduleKind != "scheduled" || scheduleCron != "0 9 * * *" || scheduleTimezone != "UTC" || legacyTool != "git" {
		t.Fatalf("admitted schedule snapshot = %d, %q, %q, %q, %q, tool %q",
			scheduledAt, scheduleOccurrence, scheduleKind, scheduleCron, scheduleTimezone, legacyTool)
	}
	throttled, err := store.Work(ctx, "run-throttled")
	if err != nil {
		t.Fatal(err)
	}
	if throttled.Work.NeedsAttention || len(throttled.Targets) != 1 ||
		throttled.Targets[0].BlockedReason != routineConcurrencyBlockedReason {
		t.Fatalf("migrated concurrency throttle = %#v", throttled)
	}
	overview, err := store.Overview(ctx)
	if err != nil || overview.ActiveWork != 1 || overview.NeedsAttention != 0 {
		t.Fatalf("migrated throttle Overview = %#v, err %v", overview, err)
	}

	var targetID, eventPayload, retained string
	if err := db.QueryRowContext(ctx, `SELECT work_target_id FROM executions WHERE id = 'execution-issue'`).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT CAST(payload AS TEXT) FROM attempt_events WHERE attempt_id = 'attempt-issue'`).Scan(&eventPayload); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT retained_worktrees_json FROM workers WHERE id = 'worker-1'`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if targetID != "task-issue" || eventPayload != `{"message":"kept"}` || !strings.Contains(retained, "attempt-issue") {
		t.Fatalf("lifecycle preservation = target %q, event %q, retained %q", targetID, eventPayload, retained)
	}

	var violations int
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		violations++
	}
	if violations != 0 {
		t.Fatalf("foreign key violations = %d", violations)
	}
}

func TestRoutinesMigrationDoesNotCountReadOnlyRevisionHistoryTowardRoutineLimit(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/legacy.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, now: time.Now, sweepEvery: 5 * time.Second}
	t.Cleanup(func() { _ = db.Close() })
	applyMigrationsBeforeRoutines(t, ctx, store)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for workflowNumber := 1; workflowNumber <= 6; workflowNumber++ {
		workflowID := fmt.Sprintf("workflow-%d", workflowNumber)
		currentRevisionID := fmt.Sprintf("revision-%d-100", workflowNumber)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workflows(id, enabled, current_revision_id, current_title_key, created_at, updated_at)
			VALUES (?, 1, ?, ?, 1, 100)
		`, workflowID, currentRevisionID, workflowID); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		for revisionNumber := 1; revisionNumber <= 100; revisionNumber++ {
			revisionID := fmt.Sprintf("revision-%d-%d", workflowNumber, revisionNumber)
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO workflow_revisions(
					id, workflow_id, revision_number, request_key, request_digest,
					title, summary, instructions, created_at
				) VALUES (?, ?, ?, ?, X'01', ?, '', ?, ?)
			`, revisionID, workflowID, revisionNumber, "request-"+revisionID,
				fmt.Sprintf("Workflow %d", workflowNumber),
				fmt.Sprintf("Instructions %d", revisionNumber), revisionNumber); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var total, editable, historical int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), SUM(read_only = 0), SUM(read_only = 1)
		FROM routines WHERE migration_only = 0
	`).Scan(&total, &editable, &historical); err != nil {
		t.Fatal(err)
	}
	if total != 600 || editable != 6 || historical != 594 {
		t.Fatalf("migrated revisions = total %d, editable %d, historical %d", total, editable, historical)
	}
}

func TestRoutinesMigrationNormalizesUnicodeNameKeys(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/legacy.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, now: time.Now, sweepEvery: 5 * time.Second}
	t.Cleanup(func() { _ = db.Close() })
	applyMigrationsBeforeRoutines(t, ctx, store)
	if _, err := db.ExecContext(ctx, legacyRoutinesFixture); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE definitions SET name = 'Éclair' WHERE id = 'definition-1';
		UPDATE automations SET title = 'Étude' WHERE id = 'automation-schedule';
		UPDATE workflow_revisions SET title = 'Übung' WHERE workflow_id = 'workflow-1';
	`); err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT name, name_key FROM routines WHERE migration_only = 0`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, key string
		if err := rows.Scan(&name, &key); err != nil {
			t.Fatal(err)
		}
		if want := normalizeTitleKey(name); key != want {
			t.Fatalf("migrated Routine %q key = %q, want %q", name, key, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var rewrittenHistoryKeys int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM routines
		WHERE migration_only = 1 AND name_key NOT GLOB '__migration_*_history__'
	`).Scan(&rewrittenHistoryKeys); err != nil {
		t.Fatal(err)
	}
	if rewrittenHistoryKeys != 0 {
		t.Fatalf("rewrote %d reserved migration history keys", rewrittenHistoryKeys)
	}
	if _, err := store.CreateRoutine(ctx, protocol.SaveRoutineRequest{
		Name: "éCLAIR · definition 1", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
	}); !serviceErrorCode(err, "routine_name_conflict") {
		t.Fatalf("migrated Unicode Routine name conflict error = %v", err)
	}
}

func TestRoutinesMigrationKeepsArchivedDefinitionSchedulesDisabled(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/legacy.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, now: time.Now, sweepEvery: 5 * time.Second}
	t.Cleanup(func() { _ = db.Close() })
	applyMigrationsBeforeRoutines(t, ctx, store)
	if _, err := db.ExecContext(ctx, legacyRoutinesFixture); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE definitions SET archived = 1 WHERE id = 'definition-1'`); err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var archived, scheduleEnabled bool
	var cron, timezone, healthCode, healthMessage string
	var nextDue sql.NullInt64
	var pendingDue int64
	if err := db.QueryRowContext(ctx, `
		SELECT archived, schedule_enabled, cron, timezone, next_due_at, pending_due_at,
			schedule_health_code, schedule_health_message
		FROM routines WHERE id = 'automation-schedule'
	`).Scan(&archived, &scheduleEnabled, &cron, &timezone, &nextDue, &pendingDue, &healthCode, &healthMessage); err != nil {
		t.Fatal(err)
	}
	if !archived || scheduleEnabled || cron != "0 9 * * *" || timezone != "UTC" ||
		nextDue.Valid || pendingDue != 2000 || healthCode != "source_archived" ||
		!strings.Contains(healthMessage, "source prompt") || !strings.Contains(healthMessage, "repository unavailable") {
		t.Fatalf("archived-source schedule = archived %v, enabled %v, cron %q, timezone %q, next %v, pending %d, health %q %q",
			archived, scheduleEnabled, cron, timezone, nextDue, pendingDue, healthCode, healthMessage)
	}
}

func TestRoutinesMigrationBlocksInvalidLegacySchedulePrompts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, context.Context, *sql.DB)
	}{
		{
			name: "override removed from current Definition",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB) {
				t.Helper()
				if _, err := db.ExecContext(ctx, `
					UPDATE automation_schedule_triggers
					SET parameters_json = '{"removed":"value"}'
					WHERE automation_id = 'automation-schedule'
				`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "schedule-specific resolved prompt over limit",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB) {
				t.Helper()
				if _, err := db.ExecContext(ctx, `
					UPDATE definitions SET prompt = ?, inputs = '{"scope":""}'
					WHERE id = 'definition-1'
				`, strings.Repeat("x", 65000)); err != nil {
					t.Fatal(err)
				}
				parameters := `{"scope":"` + strings.Repeat("y", 1000) + `"}`
				if _, err := db.ExecContext(ctx, `
					UPDATE automation_schedule_triggers SET parameters_json = ?
					WHERE automation_id = 'automation-schedule'
				`, parameters); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "multiple failed occurrences for one schedule",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB) {
				t.Helper()
				if _, err := db.ExecContext(ctx, `
					INSERT INTO automation_occurrences(
					  id, automation_id, automation_version, automation_title,
					  repository_id, repository_identity, context, timeout_seconds,
					  state, resolved_prompt, task_request_key, task_id_snapshot,
					  diagnostic, created_at, updated_at
					) VALUES (
					  'occurrence-schedule-second-failure', 'automation-schedule', 5,
					  'Shared', 'repo-1', 'github.com/example/factory', '', 3600,
					  'failed', 'Scheduled prompt', 'schedule-second-failure-key', '',
					  'repository still unavailable', 15, 15
					);
					INSERT INTO automation_schedule_occurrences(
					  occurrence_id, automation_id, kind, scheduled_at, cron, timezone,
					  definition_id, definition_snapshot, repository_ids_json,
					  parameters_json, concurrency_limit
					) VALUES (
					  'occurrence-schedule-second-failure', 'automation-schedule',
					  'scheduled', 2100, '0 9 * * *', 'UTC', 'definition-1',
					  '{"name":"Shared","prompt":"Review.","runtime":"codex","allowed_tools":["git"],"timeout_seconds":3600,"inputs":{},"generation":1}',
					  '["repo-1"]', '{}', 10
					)
				`); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := sql.Open("sqlite", t.TempDir()+"/legacy.sqlite3")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			store := &Store{db: db, now: time.Now, sweepEvery: 5 * time.Second}
			applyMigrationsBeforeRoutines(t, ctx, store)
			if _, err := db.ExecContext(ctx, legacyRoutinesFixture); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, ctx, db)
			if err := store.migrate(ctx); err == nil {
				t.Fatal("migration accepted an invalid legacy schedule prompt")
			}
			var definitions int
			if err := db.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'definitions'
			`).Scan(&definitions); err != nil {
				t.Fatal(err)
			}
			if definitions != 1 {
				t.Fatal("failed migration changed the legacy database")
			}
		})
	}
}

func applyMigrationsBeforeRoutines(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for index, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		if entry.Name() == "027_routines_work.sql" {
			return
		}
		body, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		version := index + 1
		if bytes.HasPrefix(body, []byte("-- factory: foreign-keys-off")) {
			if err := store.applyForeignKeyRebuildMigration(ctx, entry.Name(), version, body); err != nil {
				t.Fatal(err)
			}
			continue
		}
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, string(body)); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, 0)`, version)
		}
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply %s: %v", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("027_routines_work.sql not found")
}

const legacyRoutinesFixture = `
INSERT INTO repositories(id, remote_identity, created_at, updated_at)
VALUES
  ('repo-1', 'github.com/example/factory', 1, 1),
  ('repo-2', 'github.com/example/neo', 1, 1);
INSERT INTO workers(
  id, name, worker_version, runtime_version, capacity, active_count, health,
  retained_worktrees_json, registered_at, last_heartbeat, runtime
) VALUES (
  'worker-1', 'Worker', 'test', 'test', 10, 0, 'healthy',
  '[{"attempt_id":"attempt-issue","reason":"kept"}]', 1, 1, 'codex'
);

INSERT INTO definitions(
  id, name, name_key, prompt, runtime, allowed_tools, timeout_seconds, inputs,
  generation, archived, created_at, updated_at
) VALUES ('definition-1', 'Shared', 'shared', 'Review.', 'codex', '["git"]', 3600, '{}', 1, 0, 1, 1);

BEGIN;
INSERT INTO workflows(id, enabled, current_revision_id, current_title_key, created_at, updated_at)
VALUES ('workflow-1', 1, 'revision-1', 'shared', 1, 1);
INSERT INTO workflow_revisions(
  id, workflow_id, revision_number, request_key, request_digest, title, summary, instructions, created_at
) VALUES
  ('revision-1', 'workflow-1', 1, 'revision-key', X'01', 'Shared', 'Summary', 'Instructions', 1),
  ('revision-2', 'workflow-1', 2, 'revision-key-2', X'02', 'Shared', 'Current summary', 'Current instructions', 2);
UPDATE workflows SET current_revision_id = 'revision-2', updated_at = 2 WHERE id = 'workflow-1';
COMMIT;

INSERT INTO automations(
  id, request_key, request_digest, title, title_key, workflow_id, repository_id,
  context, timeout_seconds, enabled, version, trigger_type, health_status,
  health_code, health_message, created_at, updated_at
) VALUES
  ('automation-issue', 'automation-issue-key', X'01', 'Issue review', 'issue review', 'workflow-1', 'repo-1', '', 3600, 0, 2, 'github_issue', 'disabled', '', 'Disabled', 1, 1),
  ('automation-pr', 'automation-pr-key', X'02', 'PR review', 'pr review', 'workflow-1', 'repo-1', '', 3600, 0, 3, 'github_pull_request', 'disabled', '', 'Disabled', 1, 1),
  ('automation-webhook', 'automation-webhook-key', X'03', 'Webhook review', 'webhook review', NULL, 'repo-1', '', 3600, 0, 4, 'github_webhook', 'disabled', '', 'Disabled', 1, 1),
  ('automation-schedule', 'automation-schedule-key', X'04', 'Shared', 'shared', NULL, 'repo-1', '', 3600, 1, 5, 'schedule', 'healthy', '', '', 1, 1);

INSERT INTO automation_schedule_triggers(automation_id, cron, timezone, next_due_at, definition_id, parameters_json, concurrency_limit)
VALUES ('automation-schedule', '0 9 * * *', 'UTC', 3000, 'definition-1', '{}', 10);
INSERT INTO automation_schedule_repositories(automation_id, position, repository_id)
VALUES ('automation-schedule', 0, 'repo-1');

INSERT INTO tasks(
  id, request_key, title, description, repository_id, timeout_seconds, created_at,
  workflow_id, workflow_revision_id, workflow_title, workflow_revision_number, context
) VALUES
  ('task-workflow', 'task-workflow-key', 'Workflow Work', 'Workflow prompt', 'repo-1', 3600, 10, 'workflow-1', 'revision-1', 'Shared', 1, ''),
  ('task-issue', 'task-issue-key', 'Issue Work', 'Issue prompt', 'repo-1', 3600, 11, 'workflow-1', 'revision-1', 'Shared', 1, ''),
  ('task-pr', 'task-pr-key', 'PR Work', 'PR prompt', 'repo-1', 3600, 12, 'workflow-1', 'revision-1', 'Shared', 1, ''),
  ('task-webhook', 'task-webhook-key', 'Webhook Work', 'Webhook prompt', 'repo-1', 3600, 13, NULL, NULL, NULL, NULL, ''),
  ('task-scheduled', 'task-scheduled-key', 'Scheduled Work', 'Scheduled prompt', 'repo-1', 3600, 14, NULL, NULL, NULL, NULL, '');
INSERT INTO executions(id, task_id, assigned_worker_id, required_runtime, state, created_at, updated_at)
VALUES
  ('execution-workflow', 'task-workflow', 'worker-1', 'codex', 'succeeded', 10, 20),
  ('execution-issue', 'task-issue', 'worker-1', 'codex', 'succeeded', 11, 21),
  ('execution-pr', 'task-pr', 'worker-1', 'codex', 'failed', 12, 22),
  ('execution-webhook', 'task-webhook', 'worker-1', 'codex', 'succeeded', 13, 23),
  ('execution-scheduled', 'task-scheduled', 'worker-1', 'codex', 'succeeded', 14, 24);
INSERT INTO attempts(
  id, execution_id, worker_id, attempt_number, state, lease_digest, lease_expires_at,
  result, error, started_at, completed_at, created_at
) VALUES
  ('attempt-workflow', 'execution-workflow', 'worker-1', 1, 'succeeded', X'01', 20, 'done', NULL, 11, 20, 10),
  ('attempt-issue', 'execution-issue', 'worker-1', 1, 'succeeded', X'02', 21, 'done', NULL, 12, 21, 11),
  ('attempt-pr', 'execution-pr', 'worker-1', 1, 'failed', X'03', 22, NULL, 'failed', 13, 22, 12),
  ('attempt-webhook', 'execution-webhook', 'worker-1', 1, 'succeeded', X'04', 23, 'done', NULL, 14, 23, 13),
  ('attempt-scheduled', 'execution-scheduled', 'worker-1', 1, 'succeeded', X'05', 24, 'done', NULL, 15, 24, 14);
INSERT INTO attempt_events(attempt_id, sequence, kind, payload, payload_bytes, server_time)
VALUES ('attempt-issue', 0, 'progress', '{"message":"kept"}', 18, 15);

INSERT INTO runs(
  id, request_key, request_digest, source_kind, definition_id, definition_snapshot,
  parameters, admitted_at, updated_at, concurrency_limit, resolved_prompt
) VALUES
  (
    'run-scheduled', 'run-scheduled-key', X'05', 'schedule', 'definition-1',
    '{"name":"Shared","prompt":"Review.","runtime":"codex","allowed_tools":["git"],"timeout_seconds":3600,"inputs":{},"generation":1}',
    '{}', 14, 24, 10, 'Scheduled resolved prompt'
  ),
  (
    'run-throttled', 'run-throttled-key', X'06', 'manual', 'definition-1',
    '{"name":"Shared","prompt":"Review.","runtime":"codex","allowed_tools":["git"],"timeout_seconds":3600,"inputs":{},"generation":1}',
    '{}', 15, 25, 1, 'Throttled resolved prompt'
  );
INSERT INTO jobs(
  id, run_id, repository_id, task_id, execution_id, state, blocked_reason,
  admitted_at, updated_at, repository_identity
) VALUES
  ('job-scheduled', 'run-scheduled', 'repo-1', 'task-scheduled', 'execution-scheduled', 'queued', NULL, 14, 24, 'github.com/example/factory'),
  ('job-throttled', 'run-throttled', 'repo-2', NULL, NULL, 'blocked', 'Waiting for an available Run concurrency slot.', 15, 25, 'github.com/example/neo');

INSERT INTO automation_occurrences(
  id, automation_id, automation_version, automation_title, workflow_revision_id,
  repository_id, repository_identity, context, timeout_seconds, state,
  resolved_prompt, task_request_key, task_id, task_id_snapshot, diagnostic, retry_at, created_at, updated_at
) VALUES
  ('occurrence-issue', 'automation-issue', 2, 'Issue review', 'revision-1', 'repo-1', 'github.com/example/factory', '', 3600, 'dispatched', 'Issue resolved prompt', 'task-issue-key', 'task-issue', 'task-issue', '', NULL, 11, 21),
  ('occurrence-pr', 'automation-pr', 3, 'PR review', 'revision-1', 'repo-1', 'github.com/example/factory', '', 3600, 'dispatched', 'PR resolved prompt', 'task-pr-key', 'task-pr', 'task-pr', '', NULL, 12, 22),
  ('occurrence-webhook', 'automation-webhook', 4, 'Webhook review', NULL, 'repo-1', 'github.com/example/factory', '', 3600, 'dispatched', 'Webhook resolved prompt', 'task-webhook-key', 'task-webhook', 'task-webhook', '', NULL, 13, 23),
  ('occurrence-schedule-admitted', 'automation-schedule', 5, 'Shared', NULL, 'repo-1', 'github.com/example/factory', '', 3600, 'dispatched', 'Scheduled resolved prompt', 'schedule-admitted-key', NULL, '', '', NULL, 14, 24),
  ('occurrence-schedule', 'automation-schedule', 5, 'Shared', NULL, 'repo-1', 'github.com/example/factory', '', 3600, 'failed', 'Scheduled prompt', 'schedule-pending-key', NULL, '', 'repository unavailable', 2500, 14, 14);
INSERT INTO automation_github_issue_occurrences(
  occurrence_id, automation_id, issue_number, issue_url, issue_title, observed_state,
  observed_labels_json, configured_state, required_labels_json
) VALUES ('occurrence-issue', 'automation-issue', 42, 'https://github.com/example/factory/issues/42', 'Issue', 'open', '["ready","bug"]', 'open', '["ready"]');
INSERT INTO automation_github_pull_request_occurrences(
  occurrence_id, automation_id, pull_request_number, pull_request_url,
  pull_request_title, observed_state, observed_draft, observed_base_branch,
  observed_head_commit, observed_labels_json, configured_state, include_drafts,
  required_labels_json, base_branches_json
) VALUES ('occurrence-pr', 'automation-pr', 7, 'https://github.com/example/factory/pull/7', 'PR', 'open', 1, 'main', 'abc123', '["review"]', 'open', 1, '["review"]', '["main"]');
INSERT INTO github_webhook_deliveries(
  delivery_id, payload_digest, event, action, repository_identity,
  pull_request_number, pull_request_url, pull_request_title, base_branch,
  head_commit, state, created_at, updated_at
) VALUES ('delivery-1', X'01', 'pull_request', 'opened', 'github.com/example/factory', 9,
  'https://github.com/example/factory/pull/9', 'Webhook PR', 'main', 'def456', 'completed', 13, 23);
INSERT INTO automation_github_webhook_occurrences(
  occurrence_id, automation_id, delivery_id, event, action, pull_request_number,
  pull_request_url, pull_request_title, base_branch, head_commit, definition_id,
  definition_snapshot, parameters_json, run_id
) VALUES ('occurrence-webhook', 'automation-webhook', 'delivery-1', 'pull_request', 'opened', 9,
  'https://github.com/example/factory/pull/9', 'Webhook PR', 'main', 'def456', 'definition-1',
  '{"name":"Shared","prompt":"Review.","runtime":"codex","allowed_tools":["git"],"timeout_seconds":3600,"inputs":{},"generation":1}',
  '{"priority":"high"}', NULL);
INSERT INTO automation_schedule_occurrences(
  occurrence_id, automation_id, kind, scheduled_at, cron, timezone, definition_id,
  definition_snapshot, repository_ids_json, parameters_json, concurrency_limit, run_id
) VALUES
  ('occurrence-schedule-admitted', 'automation-schedule', 'scheduled', 1500, '0 9 * * *', 'UTC',
  'definition-1', '{"name":"Shared","prompt":"Review.","runtime":"codex","allowed_tools":["git"],"timeout_seconds":3600,"inputs":{},"generation":1}',
  '["repo-1"]', '{}', 10, 'run-scheduled'),
  (
  'occurrence-schedule', 'automation-schedule', 'scheduled', 2000, '0 9 * * *', 'UTC',
  'definition-1', '{"name":"Shared","prompt":"Review.","runtime":"codex","allowed_tools":["git"],"timeout_seconds":3600,"inputs":{},"generation":1}',
  '["repo-1"]', '{}', 10, NULL
);
`
