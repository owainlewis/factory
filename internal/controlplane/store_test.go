package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
	"github.com/owainlewis/factory/migrations"
)

const (
	workerA = "worker-a"
	workerB = "worker-b"
	tokenA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tokenB  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), t.TempDir()+"/controlplane.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func TestTaskPaginationMigrationProvidesTheOrderingIndex(t *testing.T) {
	store := newTestStore(t)
	rows, err := store.db.Query(`
		EXPLAIN QUERY PLAN
		SELECT t.id, t.request_key, t.title, t.repository_id, t.timeout_seconds,
		       e.assigned_worker_id, e.state, t.created_at
		FROM tasks t JOIN executions e ON e.task_id = t.id
		ORDER BY t.created_at DESC, t.id DESC
		LIMIT 51
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "tasks_created_at_id_desc") {
		t.Fatalf("task pagination query plan does not use ordering index: %v", plan)
	}
}

func TestTitleMigrationPreservesWorkflowAutomationAndTaskSnapshots(t *testing.T) {
	database, err := sql.Open("sqlite", t.TempDir()+"/title-migration.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, migrationName := range []string{
		"001_controlplane.sql", "002_attempt_capacity_handoff.sql", "003_task_list_pagination.sql",
		"004_worker_runtime.sql", "005_metrics_indexes.sql", "006_execution_retries.sql",
		"007_worker_source_access.sql", "008_managed_repositories.sql", "009_workflows.sql",
		"010_github_issue_automations.sql", "011_github_pull_request_automations.sql",
		"012_schedule_automations.sql", "013_legacy_poller_migration.sql",
	} {
		body, readErr := migrations.Files.ReadFile(migrationName)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, execErr := database.Exec(string(body)); execErr != nil {
			t.Fatalf("apply %s: %v", migrationName, execErr)
		}
	}
	if _, err := database.Exec(`
		INSERT INTO repositories(id, remote_identity, created_at, enabled, updated_at, centrally_managed)
		VALUES ('repository', 'github.com/example/repository', 1, 1, 1, 1);
		INSERT INTO workflows(id, enabled, current_revision_id, current_name_key, created_at, updated_at)
		VALUES ('workflow', 1, 'revision', 'implement', 1, 1);
		INSERT INTO workflow_revisions(
			id, workflow_id, revision_number, request_key, request_digest,
			name, summary, instructions, created_at
		) VALUES ('revision', 'workflow', 1, 'workflow-request', X'00',
			'Implement', 'Implement one change.', 'Implement safely.', 1);
		INSERT INTO tasks(
			id, request_key, title, description, repository_id, timeout_seconds, created_at,
			workflow_id, workflow_revision_id, workflow_name, workflow_revision_number, context
		) VALUES ('task', 'task-request', 'Task', 'Resolved prompt', 'repository', 60, 1,
			'workflow', 'revision', 'Implement', 1, 'Ticket 123');
		INSERT INTO automations(
			id, request_key, request_digest, name, name_key, workflow_id, repository_id,
			context, timeout_seconds, trigger_type, created_at, updated_at
		) VALUES ('automation', 'automation-request', X'00', 'Ready issues', 'ready issues',
			'workflow', 'repository', '', 60, 'github_issue', 1, 1);
		INSERT INTO automation_github_issue_triggers(
			automation_id, issue_state, required_labels_json, poll_interval_seconds
		) VALUES ('automation', 'open', '["factory:ready"]', 10);
		INSERT INTO automation_occurrences(
			id, automation_id, automation_version, automation_name, workflow_revision_id,
			repository_id, repository_identity, context, timeout_seconds, state,
			resolved_prompt, task_request_key, created_at, updated_at
		) VALUES ('occurrence', 'automation', 1, 'Ready issues', 'revision', 'repository',
			'github.com/example/repository', '', 60, 'pending', 'Prompt', 'occurrence-request', 1, 1);
	`); err != nil {
		t.Fatal(err)
	}
	migration, err := migrations.Files.ReadFile("014_workflow_automation_titles.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var workflowKey, workflowTitle, taskWorkflowTitle, automationTitle, automationKey, occurrenceTitle string
	if err := database.QueryRow(`
		SELECT workflow.current_title_key, revision.title, task.workflow_title,
		       automation.title, automation.title_key, occurrence.automation_title
		FROM workflows workflow
		JOIN workflow_revisions revision ON revision.id = workflow.current_revision_id
		JOIN tasks task ON task.workflow_id = workflow.id
		JOIN automations automation ON automation.workflow_id = workflow.id
		JOIN automation_occurrences occurrence ON occurrence.automation_id = automation.id
	`).Scan(&workflowKey, &workflowTitle, &taskWorkflowTitle, &automationTitle, &automationKey, &occurrenceTitle); err != nil {
		t.Fatal(err)
	}
	if workflowKey != "implement" || workflowTitle != "Implement" || taskWorkflowTitle != "Implement" ||
		automationTitle != "Ready issues" || automationKey != "ready issues" || occurrenceTitle != "Ready issues" {
		t.Fatalf("title migration lost data: %q %q %q %q %q %q", workflowKey, workflowTitle,
			taskWorkflowTitle, automationTitle, automationKey, occurrenceTitle)
	}
	for table, retired := range map[string][]string{
		"workflows":              {"current_name_key"},
		"workflow_revisions":     {"name"},
		"tasks":                  {"workflow_name"},
		"automations":            {"name", "name_key"},
		"automation_occurrences": {"automation_name"},
	} {
		rows, err := database.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		columns := make(map[string]bool)
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			columns[column] = true
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		for _, column := range retired {
			if columns[column] {
				t.Fatalf("%s still has retired column %s", table, column)
			}
		}
	}
}

func TestPullRequestAutomationMigrationPreservesGitHubIssueAutomations(t *testing.T) {
	database, err := sql.Open("sqlite", t.TempDir()+"/pull-request-automation-migration.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, name := range []string{
		"001_controlplane.sql", "002_attempt_capacity_handoff.sql", "003_task_list_pagination.sql",
		"004_worker_runtime.sql", "005_metrics_indexes.sql", "006_execution_retries.sql",
		"007_worker_source_access.sql", "008_managed_repositories.sql", "009_workflows.sql",
		"010_github_issue_automations.sql",
	} {
		body, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if _, err := database.Exec(`
		INSERT INTO repositories(id, remote_identity, created_at, enabled, updated_at, centrally_managed)
		VALUES ('repository', 'github.com/example/repository', 1, 1, 1, 1);
		INSERT INTO workflows(id, enabled, current_revision_id, current_name_key, created_at, updated_at)
		VALUES ('workflow', 1, 'revision', 'workflow', 1, 1);
		INSERT INTO workflow_revisions(
			id, workflow_id, revision_number, request_key, request_digest,
			name, summary, instructions, created_at
		) VALUES ('revision', 'workflow', 1, 'revision-request', X'00', 'Workflow', '', 'Review.', 1);
		INSERT INTO automations(
			id, request_key, request_digest, name, name_key, workflow_id, repository_id,
			context, timeout_seconds, trigger_type, created_at, updated_at
		) VALUES ('automation', 'automation-request', X'00', 'Issues', 'issues', 'workflow',
			'repository', '', 60, 'github_issue', 1, 1);
		INSERT INTO automation_github_issue_triggers(
			automation_id, issue_state, required_labels_json, poll_interval_seconds
		) VALUES ('automation', 'open', '["factory:ready"]', 10);
	`); err != nil {
		t.Fatal(err)
	}
	migration, err := migrations.Files.ReadFile("011_github_pull_request_automations.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	var triggerType, state, labels string
	if err := database.QueryRow(`
		SELECT automation.trigger_type, trigger.issue_state, trigger.required_labels_json
		FROM automations automation
		JOIN automation_github_issue_triggers trigger ON trigger.automation_id = automation.id
		WHERE automation.id = 'automation'
	`).Scan(&triggerType, &state, &labels); err != nil {
		t.Fatal(err)
	}
	if triggerType != "github_issue" || state != "open" || labels != `["factory:ready"]` {
		t.Fatalf("migrated issue Automation = type %q state %q labels %q", triggerType, state, labels)
	}
	if _, err := database.Exec(`
		INSERT INTO automation_github_pull_request_triggers(
			automation_id, pull_request_state, include_drafts, required_labels_json,
			base_branches_json, poll_interval_seconds
		) VALUES ('automation', 'open', 0, '[]', '[]', 10)
	`); err == nil {
		t.Fatal("inserted a pull-request Trigger for a GitHub issue Automation")
	}
	if _, err := database.Exec(`UPDATE automations SET trigger_type = 'github_pull_request' WHERE id = 'automation'`); err == nil {
		t.Fatal("changed an Automation trigger type after creation")
	}
	if _, err := database.Exec(`
		INSERT INTO automations(
			id, request_key, request_digest, name, name_key, workflow_id, repository_id,
			context, timeout_seconds, trigger_type, created_at, updated_at
		) VALUES
			('issue-other', 'issue-other-request', X'00', 'Other issues', 'other issues',
			 'workflow', 'repository', '', 60, 'github_issue', 1, 1),
			('pr-a', 'pr-a-request', X'00', 'PR A', 'pr a',
			 'workflow', 'repository', '', 60, 'github_pull_request', 1, 1),
			('pr-b', 'pr-b-request', X'00', 'PR B', 'pr b',
			 'workflow', 'repository', '', 60, 'github_pull_request', 1, 1);
		INSERT INTO automation_github_issue_triggers(
			automation_id, issue_state, required_labels_json, poll_interval_seconds
		) VALUES ('issue-other', 'open', '[]', 10);
		INSERT INTO automation_github_pull_request_triggers(
			automation_id, pull_request_state, include_drafts, required_labels_json,
			base_branches_json, poll_interval_seconds
		) VALUES
			('pr-a', 'open', 0, '[]', '[]', 10),
			('pr-b', 'open', 0, '[]', '[]', 10);
		INSERT INTO automation_occurrences(
			id, automation_id, automation_version, automation_name, workflow_revision_id,
			repository_id, repository_identity, context, timeout_seconds, state,
			resolved_prompt, task_request_key, created_at, updated_at
		) VALUES
			('issue-occurrence', 'automation', 1, 'Issues', 'revision', 'repository',
			 'github.com/example/repository', '', 60, 'pending', 'prompt', 'issue-occurrence-key', 1, 1),
			('pr-occurrence', 'pr-a', 1, 'PR A', 'revision', 'repository',
			 'github.com/example/repository', '', 60, 'pending', 'prompt', 'pr-occurrence-key', 1, 1);
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		INSERT INTO automation_github_issue_occurrences(
			occurrence_id, automation_id, issue_number, issue_url, issue_title,
			observed_state, observed_labels_json, configured_state, required_labels_json
		) VALUES ('issue-occurrence', 'issue-other', 1, 'https://github.com/example/repository/issues/1',
			'Issue', 'open', '[]', 'open', '[]')
	`); err == nil {
		t.Fatal("inserted GitHub issue metadata for a different Automation than its parent Occurrence")
	}
	if _, err := database.Exec(`UPDATE automation_occurrences SET automation_id = 'pr-b' WHERE id = 'pr-occurrence'`); err == nil {
		t.Fatal("changed an Occurrence Automation identity after creation")
	}
	if _, err := database.Exec(`
		INSERT INTO automation_github_pull_request_occurrences(
			occurrence_id, automation_id, pull_request_number, pull_request_url,
			pull_request_title, observed_state, observed_draft, observed_base_branch,
			observed_head_commit, observed_labels_json, configured_state,
			include_drafts, required_labels_json, base_branches_json
		) VALUES ('pr-occurrence', 'pr-b', 1, 'https://github.com/example/repository/pull/1',
			'PR', 'open', 0, 'main', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'[]', 'open', 0, '[]', '[]')
	`); err == nil {
		t.Fatal("inserted GitHub pull-request metadata for a different Automation than its parent Occurrence")
	}
	rows, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("pull-request Automation migration left a foreign-key violation")
	}
}

func TestScheduleAutomationMigrationPreservesExistingTypedAutomationsAndOccurrences(t *testing.T) {
	database, err := sql.Open("sqlite", t.TempDir()+"/schedule-automation-migration.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, name := range []string{
		"001_controlplane.sql", "002_attempt_capacity_handoff.sql", "003_task_list_pagination.sql",
		"004_worker_runtime.sql", "005_metrics_indexes.sql", "006_execution_retries.sql",
		"007_worker_source_access.sql", "008_managed_repositories.sql", "009_workflows.sql",
		"010_github_issue_automations.sql", "011_github_pull_request_automations.sql",
	} {
		body, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if _, err := database.Exec(`
		INSERT INTO repositories(id, remote_identity, created_at, enabled, updated_at, centrally_managed)
		VALUES ('repository', 'github.com/example/repository', 1, 1, 1, 1);
		INSERT INTO workflows(id, enabled, current_revision_id, current_name_key, created_at, updated_at)
		VALUES ('workflow', 1, 'revision', 'workflow', 1, 1);
		INSERT INTO workflow_revisions(
			id, workflow_id, revision_number, request_key, request_digest,
			name, summary, instructions, created_at
		) VALUES ('revision', 'workflow', 1, 'revision-request', X'00', 'Workflow', '', 'Review.', 1);
		INSERT INTO automations(
			id, request_key, request_digest, name, name_key, workflow_id, repository_id,
			context, timeout_seconds, trigger_type, created_at, updated_at
		) VALUES
			('issue', 'issue-request', X'00', 'Issues', 'issues', 'workflow', 'repository', '', 60, 'github_issue', 1, 1),
			('pull-request', 'pr-request', X'00', 'Pull requests', 'pull requests', 'workflow', 'repository', '', 60, 'github_pull_request', 1, 1);
		INSERT INTO automation_github_issue_triggers(
			automation_id, issue_state, required_labels_json, poll_interval_seconds
		) VALUES ('issue', 'open', '["factory:ready"]', 10);
		INSERT INTO automation_github_pull_request_triggers(
			automation_id, pull_request_state, include_drafts, required_labels_json,
			base_branches_json, poll_interval_seconds
		) VALUES ('pull-request', 'open', 0, '["factory:review"]', '["main"]', 10);
		INSERT INTO automation_occurrences(
			id, automation_id, automation_version, automation_name, workflow_revision_id,
			repository_id, repository_identity, context, timeout_seconds, state,
			resolved_prompt, task_request_key, created_at, updated_at
		) VALUES
			('issue-occurrence', 'issue', 1, 'Issues', 'revision', 'repository',
			 'github.com/example/repository', '', 60, 'pending', 'issue prompt', 'issue-key', 1, 1),
			('pr-occurrence', 'pull-request', 1, 'Pull requests', 'revision', 'repository',
			 'github.com/example/repository', '', 60, 'pending', 'pr prompt', 'pr-key', 1, 1);
		INSERT INTO automation_github_issue_occurrences(
			occurrence_id, automation_id, issue_number, issue_url, issue_title,
			observed_state, observed_labels_json, configured_state, required_labels_json
		) VALUES ('issue-occurrence', 'issue', 186, 'https://github.com/example/repository/issues/186',
			'Issue', 'open', '["factory:ready"]', 'open', '["factory:ready"]');
		INSERT INTO automation_github_pull_request_occurrences(
			occurrence_id, automation_id, pull_request_number, pull_request_url,
			pull_request_title, observed_state, observed_draft, observed_base_branch,
			observed_head_commit, observed_labels_json, configured_state,
			include_drafts, required_labels_json, base_branches_json
		) VALUES ('pr-occurrence', 'pull-request', 191, 'https://github.com/example/repository/pull/191',
			'Pull request', 'open', 0, 'main', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'["factory:review"]', 'open', 0, '["factory:review"]', '["main"]');
	`); err != nil {
		t.Fatal(err)
	}
	migration, err := migrations.Files.ReadFile("012_schedule_automations.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	var automations, occurrences int
	if err := database.QueryRow(`
		SELECT COUNT(*) FROM automations automation
		WHERE (automation.id = 'issue' AND automation.trigger_type = 'github_issue')
		   OR (automation.id = 'pull-request' AND automation.trigger_type = 'github_pull_request')
	`).Scan(&automations); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM automation_github_issue_occurrences WHERE occurrence_id = 'issue-occurrence') +
			(SELECT COUNT(*) FROM automation_github_pull_request_occurrences WHERE occurrence_id = 'pr-occurrence')
	`).Scan(&occurrences); err != nil {
		t.Fatal(err)
	}
	if automations != 2 || occurrences != 2 {
		t.Fatalf("schedule migration preserved %d Automations and %d typed Occurrences", automations, occurrences)
	}
	rows, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("schedule Automation migration left a foreign-key violation")
	}
}

func TestRetryCountMigrationBackfillsHistoricalAttempts(t *testing.T) {
	database, err := sql.Open("sqlite", t.TempDir()+"/retry-migration.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	schema, err := migrations.Files.ReadFile("001_controlplane.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		INSERT INTO workers(
			id, name, worker_version, codex_version, capacity, active_count, health,
			retained_worktrees_json, registered_at, last_heartbeat
		) VALUES ('worker', 'worker', 'test', 'test', 1, 0, 'healthy', '[]', 1, 1);
		INSERT INTO repositories(id, remote_identity, created_at)
		VALUES ('repository', 'github.com/example/repository', 1);
		INSERT INTO tasks(
			id, request_key, title, description, repository_id, timeout_seconds, created_at
		) VALUES ('task', 'task', 'task', 'task', 'repository', 60, 1);
		INSERT INTO tasks(
			id, request_key, title, description, repository_id, timeout_seconds, created_at
		) VALUES ('queued-task', 'queued-task', 'queued', 'queued', 'repository', 60, 1);
		INSERT INTO executions(
			id, task_id, assigned_worker_id, required_runtime, state,
			cancellation_requested, created_at, updated_at
		) VALUES ('execution', 'task', 'worker', 'codex', 'succeeded', 0, 1, 3);
		INSERT INTO executions(
			id, task_id, assigned_worker_id, required_runtime, state,
			cancellation_requested, created_at, updated_at
		) VALUES (
			'queued-execution', 'queued-task', 'worker', 'codex', 'queued', 0, 1, 2
		);
		INSERT INTO attempts(
			id, execution_id, worker_id, attempt_number, state, lease_digest,
			lease_expires_at, completed_at, created_at
		) VALUES ('attempt-1', 'execution', 'worker', 1, 'failed', X'00', 2, 2, 1);
		INSERT INTO attempts(
			id, execution_id, worker_id, attempt_number, state, lease_digest,
			lease_expires_at, completed_at, created_at
		) VALUES ('attempt-2', 'execution', 'worker', 2, 'succeeded', X'00', 3, 3, 2);
	`); err != nil {
		t.Fatal(err)
	}
	migration, err := migrations.Files.ReadFile("006_execution_retries.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var retryCount int
	if err := database.QueryRow(`
		SELECT retry_count FROM executions WHERE id = 'execution'
	`).Scan(&retryCount); err != nil {
		t.Fatal(err)
	}
	if retryCount != 1 {
		t.Fatalf("backfilled retry count = %d; want 1", retryCount)
	}
	if err := database.QueryRow(`
		SELECT retry_count FROM executions WHERE id = 'queued-execution'
	`).Scan(&retryCount); err != nil {
		t.Fatal(err)
	}
	if retryCount != 1 {
		t.Fatalf("queued retry count = %d; want 1", retryCount)
	}
}

func TestCapacityMigrationDerivesOnlyAttributableLegacyRetainedCounts(t *testing.T) {
	database, err := sql.Open("sqlite", t.TempDir()+"/migration.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	if _, err := database.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	schema, err := migrations.Files.ReadFile("001_controlplane.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	retained := make([]protocol.RetainedWorktree, protocol.MaxRetainedPerRepo-1)
	for index := range retained {
		retained[index] = protocol.RetainedWorktree{
			AttemptID:    fmt.Sprintf("00000000-0000-4000-8000-%012d", index),
			RepositoryID: "case-alias-repository",
			Path:         fmt.Sprintf("/tmp/legacy-%d", index),
			Reason:       "legacy retained worktree",
		}
	}
	retainedJSON, err := json.Marshal(retained)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		INSERT INTO workers(
			id, name, worker_version, codex_version, capacity, active_count, health,
			retained_worktrees_json, registered_at, last_heartbeat
		) VALUES ('worker-a', 'legacy', 'legacy', 'legacy', 2, 0, 'healthy', ?, 100, 100)
	`, retainedJSON); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		INSERT INTO workers(
			id, name, worker_version, codex_version, capacity, active_count, health,
			retained_worktrees_json, registered_at, last_heartbeat
		) VALUES (
			'worker-b', 'legacy-invalid', 'legacy', 'legacy', 1, 0, 'healthy',
			'[{"attempt_id":"stale-attempt","repository_id":"stale-repository"}]',
			100, 100
		)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		INSERT INTO workers(
			id, name, worker_version, codex_version, capacity, active_count, health,
			retained_worktrees_json, registered_at, last_heartbeat
		) VALUES (
			'worker-c', 'legacy-malformed', 'legacy', 'legacy', 1, 0, 'healthy',
			'not-json', 100, 100
		)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		INSERT INTO schema_migrations(version, applied_at) VALUES (1, 1);
		INSERT INTO repositories(id, remote_identity, created_at)
		VALUES ('legacy-repository', 'github.com/example/migration', 1);
		INSERT INTO repositories(id, remote_identity, created_at)
		VALUES ('case-alias-repository', 'github.com/Example/Migration', 2);
		INSERT INTO repositories(id, remote_identity, created_at)
		VALUES ('invalid-repository', 'github.com/example/invalid-migration', 1);
		INSERT INTO repositories(id, remote_identity, created_at)
		VALUES ('malformed-repository', 'github.com/example/malformed-migration', 1);
		INSERT INTO worker_repositories(
			worker_id, display_key, repository_id, retained_count, advertised, updated_at
		) VALUES ('worker-a', 'factory', 'legacy-repository', 0, 0, 1);
		INSERT INTO worker_repositories(
			worker_id, display_key, repository_id, retained_count, advertised, updated_at
		) VALUES ('worker-a', 'factory-case-alias', 'case-alias-repository', 3, 1, 2);
		INSERT INTO worker_repositories(
			worker_id, display_key, repository_id, retained_count, advertised, updated_at
		) VALUES ('worker-b', 'invalid', 'invalid-repository', 0, 1, 1);
		INSERT INTO worker_repositories(
			worker_id, display_key, repository_id, retained_count, advertised, updated_at
		) VALUES ('worker-c', 'malformed', 'malformed-repository', 0, 1, 1);
		INSERT INTO tasks(
			id, request_key, title, description, repository_id, timeout_seconds, created_at
		) VALUES ('historical-task', 'historical-task', 'historical', 'legacy prompt', 'legacy-repository', 60, 1);
		INSERT INTO tasks(
			id, request_key, title, description, repository_id, timeout_seconds, created_at
		) VALUES ('case-alias-task', 'case-alias-task', 'case alias', '', 'case-alias-repository', 60, 2);
		INSERT INTO tasks(
			id, request_key, title, description, repository_id, timeout_seconds, created_at
		) VALUES (
			'invalid-task', 'invalid-task', 'invalid', '', 'invalid-repository', 60, 3
		);
		INSERT INTO tasks(
			id, request_key, title, description, repository_id, timeout_seconds, created_at
		) VALUES (
			'malformed-task', 'malformed-task', 'malformed', '', 'malformed-repository', 60, 4
		);
		INSERT INTO executions(
			id, task_id, assigned_worker_id, required_runtime, state,
			cancellation_requested, created_at, updated_at
		) VALUES ('historical-execution', 'historical-task', 'worker-a', 'codex', 'failed', 0, 1, 1);
		INSERT INTO executions(
			id, task_id, assigned_worker_id, required_runtime, state,
			cancellation_requested, created_at, updated_at
		) VALUES ('case-alias-execution', 'case-alias-task', 'worker-a', 'codex', 'queued', 0, 2, 2);
		INSERT INTO executions(
			id, task_id, assigned_worker_id, required_runtime, state,
			cancellation_requested, created_at, updated_at
		) VALUES (
			'invalid-execution', 'invalid-task', 'worker-b', 'codex', 'failed', 0, 3, 3
		);
		INSERT INTO executions(
			id, task_id, assigned_worker_id, required_runtime, state,
			cancellation_requested, created_at, updated_at
		) VALUES (
			'malformed-execution', 'malformed-task', 'worker-c', 'codex', 'failed', 0, 4, 4
		);
		INSERT INTO attempts(
			id, execution_id, worker_id, attempt_number, state, lease_digest,
			lease_expires_at, error, completed_at, created_at
		) VALUES (
			'historical-attempt', 'historical-execution', 'worker-a', 1, 'failed',
			X'00', 1, 'legacy failure', 1, 1
		);
		INSERT INTO attempts(
			id, execution_id, worker_id, attempt_number, state, lease_digest,
			lease_expires_at, error, completed_at, created_at
		) VALUES (
			'invalid-attempt', 'invalid-execution', 'worker-b', 1, 'failed',
			X'00', 1, 'legacy invalid snapshot', 3, 3
		);
		INSERT INTO attempts(
			id, execution_id, worker_id, attempt_number, state, lease_digest,
			lease_expires_at, error, completed_at, created_at
		) VALUES (
			'malformed-attempt', 'malformed-execution', 'worker-c', 1, 'failed',
			X'00', 1, 'legacy malformed snapshot', 4, 4
		);
	`); err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(100)
	store := &Store{db: database, now: func() time.Time { return now }, sweepEvery: 5 * time.Second}
	if err := store.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var retainedCount, acknowledged int
	if err := database.QueryRow(`
		SELECT retained_count FROM worker_repositories
		WHERE worker_id = 'worker-a' AND repository_id = 'legacy-repository'
	`).Scan(&retainedCount); err != nil {
		t.Fatal(err)
	}
	if retainedCount != protocol.MaxRetainedPerRepo-1 {
		t.Fatalf("migrated retained count = %d", retainedCount)
	}
	if err := database.QueryRow(`
		SELECT COUNT(*) FROM attempts WHERE capacity_acknowledged = 1
	`).Scan(&acknowledged); err != nil {
		t.Fatal(err)
	}
	if acknowledged != 1 {
		t.Fatalf("migrated acknowledged attempts = %d; want 1", acknowledged)
	}
	var runtime, runtimeVersion string
	if err := database.QueryRow(`
		SELECT runtime, runtime_version FROM workers WHERE id = 'worker-a'
	`).Scan(&runtime, &runtimeVersion); err != nil {
		t.Fatal(err)
	}
	if runtime != protocol.RuntimeCodex || runtimeVersion != "legacy" {
		t.Fatalf("migrated worker runtime = %q %q", runtime, runtimeVersion)
	}
	var enabled, updatedAt, acceptsManagedRepositories, dynamic int
	if err := database.QueryRow(`
		SELECT r.enabled, r.updated_at, w.accepts_managed_repositories, wr.dynamic
		FROM repositories r
		JOIN worker_repositories wr ON wr.repository_id = r.id
		JOIN workers w ON w.id = wr.worker_id
		WHERE r.id = 'legacy-repository' AND w.id = 'worker-a'
	`).Scan(&enabled, &updatedAt, &acceptsManagedRepositories, &dynamic); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 || updatedAt != 1 || acceptsManagedRepositories != 0 || dynamic != 0 {
		t.Fatalf(
			"managed repository migration = enabled %d, updated %d, accepts %d, dynamic %d",
			enabled, updatedAt, acceptsManagedRepositories, dynamic,
		)
	}
	var historicalResolvedPrompt, historicalContext string
	var historicalWorkflowID sql.NullString
	if err := database.QueryRow(`
		SELECT description, context, workflow_id FROM tasks WHERE id = 'historical-task'
	`).Scan(&historicalResolvedPrompt, &historicalContext, &historicalWorkflowID); err != nil {
		t.Fatal(err)
	}
	if historicalResolvedPrompt != "legacy prompt" || historicalContext != historicalResolvedPrompt || historicalWorkflowID.Valid {
		t.Fatalf("historical task workflow backfill = context %q, prompt %q, workflow %#v",
			historicalContext, historicalResolvedPrompt, historicalWorkflowID)
	}
	var canonicalCount, aliasMappings, canonicalTasks int
	if err := database.QueryRow(`
		SELECT COUNT(*) FROM repositories
		WHERE lower(remote_identity) = 'github.com/example/migration'
	`).Scan(&canonicalCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`
		SELECT COUNT(*) FROM repository_aliases
		WHERE alias_id = 'case-alias-repository' AND repository_id = 'legacy-repository'
	`).Scan(&aliasMappings); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`
		SELECT COUNT(*) FROM tasks
		WHERE id = 'case-alias-task' AND repository_id = 'legacy-repository'
	`).Scan(&canonicalTasks); err != nil {
		t.Fatal(err)
	}
	if canonicalCount != 1 || aliasMappings != 1 || canonicalTasks != 1 {
		t.Fatalf(
			"GitHub alias migration = repositories %d, aliases %d, tasks %d",
			canonicalCount, aliasMappings, canonicalTasks,
		)
	}
	claim := claimTestTask(t, store, workerA, "case-alias-claim", tokenB)
	if claim.Task.ID != "case-alias-task" {
		t.Fatalf("claim selected %q; want case-alias-task", claim.Task.ID)
	}
	if claim.Repository.Key != "factory-case-alias" ||
		claim.Repository.RemoteIdentity != "github.com/Example/Migration" {
		t.Fatalf("claim repository = %#v; want the currently advertised alias", claim.Repository)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenB, State: "failed", Error: "migration alias verified",
	}); err != nil {
		t.Fatal(err)
	}
	retainedAliasWorker := protocol.WorkerRegistration{
		Name: "legacy", WorkerVersion: "upgraded", RuntimeVersion: "upgraded",
		Capacity: 2, Health: "healthy",
		Repositories: []protocol.RepositoryRegistration{
			{Key: "factory", RemoteIdentity: "github.com/Example/Migration"},
			{Key: "factory-case-alias", RemoteIdentity: "github.com/example/migration"},
		},
		RetainedWorktrees: []protocol.RetainedWorktree{{
			AttemptID: "historical-attempt", RepositoryID: "case-alias-repository",
		}},
		CapacityHandoffVersion: 1,
	}
	registeredAliasWorker, err := store.RegisterWorker(context.Background(), workerA, retainedAliasWorker)
	if err != nil {
		t.Fatalf("register worker with historical repository alias: %v", err)
	}
	if len(registeredAliasWorker.Repositories) != 1 || registeredAliasWorker.Repositories[0].Key != "factory" {
		t.Fatalf("coalesced historical repository aliases = %#v", registeredAliasWorker.Repositories)
	}
	foreignKeys, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if foreignKeys.Next() {
		t.Fatal("runtime migration left a foreign key violation")
	}
	if err := foreignKeys.Close(); err != nil {
		t.Fatal(err)
	}
	createTestTask(t, store, "post-migration-task", workerA, "legacy-repository")
	postMigrationClaim := claimTestTask(t, store, workerA, "post-migration-claim", tokenA)
	if postMigrationClaim.Task.RequestKey != "post-migration-task" {
		t.Fatalf("post-migration claim selected %q", postMigrationClaim.Task.RequestKey)
	}
}

func TestManagedRepositoryMigrationPreservesLegacyClaimRemoteSpelling(t *testing.T) {
	database, err := sql.Open("sqlite", t.TempDir()+"/legacy-remote.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	schema, err := migrations.Files.ReadFile("001_controlplane.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if _, err := database.Exec(`
		INSERT INTO schema_migrations(version, applied_at) VALUES (1, 1);
		INSERT INTO workers(
			id, name, worker_version, codex_version, capacity, active_count, health,
			retained_worktrees_json, registered_at, last_heartbeat
		) VALUES ('legacy-case-worker', 'legacy', 'legacy', 'legacy', 1, 0, 'healthy', '[]', ?, ?);
		INSERT INTO repositories(id, remote_identity, created_at)
		VALUES ('legacy-case-repository', 'github.com/Owner/Repository', 1);
		INSERT INTO worker_repositories(
			worker_id, display_key, repository_id, retained_count, advertised, updated_at
		) VALUES ('legacy-case-worker', 'legacy', 'legacy-case-repository', 0, 1, 1);
		INSERT INTO tasks(
			id, request_key, title, description, repository_id, timeout_seconds, created_at
		) VALUES ('legacy-case-task', 'legacy-case-task', 'legacy case', '', 'legacy-case-repository', 60, 1);
		INSERT INTO executions(
			id, task_id, assigned_worker_id, required_runtime, state,
			cancellation_requested, created_at, updated_at
		) VALUES (
			'legacy-case-execution', 'legacy-case-task', 'legacy-case-worker',
			'codex', 'queued', 0, 1, 1
		);
	`, now.UnixMilli(), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: database, now: func() time.Time { return now }, sweepEvery: 5 * time.Second}
	if err := store.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), "legacy-case-worker", protocol.ClaimRequest{
		RequestID: "legacy-case-claim", LeaseToken: tokenA,
	})
	if err != nil || claim == nil {
		t.Fatalf("legacy claim = %#v, err %v", claim, err)
	}
	if claim.Repository.RemoteIdentity != "github.com/Owner/Repository" {
		t.Fatalf("legacy claim remote = %q", claim.Repository.RemoteIdentity)
	}
	detail, err := store.Task(context.Background(), "legacy-case-task")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Repository.RemoteIdentity != "github.com/owner/repository" {
		t.Fatalf("canonical control-plane remote = %q", detail.Repository.RemoteIdentity)
	}
}

func TestCapacityMigrationRequiresDrainedLegacyWorkers(t *testing.T) {
	database, err := sql.Open("sqlite", t.TempDir()+"/active-migration.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	schema, err := migrations.Files.ReadFile("001_controlplane.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		INSERT INTO schema_migrations(version, applied_at) VALUES (1, 1);
		INSERT INTO workers(
			id, name, worker_version, codex_version, capacity, active_count, health,
			retained_worktrees_json, registered_at, last_heartbeat
		) VALUES ('worker-a', 'legacy', 'legacy', 'legacy', 1, 1, 'healthy', '[]', 1, 1);
		INSERT INTO repositories(id, remote_identity, created_at)
		VALUES ('legacy-repository', 'github.com/example/active-migration', 1);
		INSERT INTO tasks(
			id, request_key, title, description, repository_id, timeout_seconds, created_at
		) VALUES ('active-task', 'active-task', 'active', '', 'legacy-repository', 60, 1);
		INSERT INTO executions(
			id, task_id, assigned_worker_id, required_runtime, state,
			cancellation_requested, created_at, updated_at
		) VALUES ('active-execution', 'active-task', 'worker-a', 'codex', 'running', 0, 1, 1);
		INSERT INTO attempts(
			id, execution_id, worker_id, attempt_number, state, lease_digest,
			lease_expires_at, started_at, created_at
		) VALUES (
			'active-attempt', 'active-execution', 'worker-a', 1, 'running',
			X'00', 100, 1, 1
		);
	`); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: database, now: time.Now, sweepEvery: 5 * time.Second}
	err = store.migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "drain_active_v2_attempts_before_upgrade") {
		t.Fatalf("active migration error = %v", err)
	}
	var capacityColumn int
	rows, err := database.Query(`PRAGMA table_info(attempts)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "capacity_acknowledged" {
			capacityColumn++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if capacityColumn != 0 {
		t.Fatal("failed drain guard left a partial capacity migration")
	}
	if _, err := database.Exec(`
		UPDATE attempts
		SET state = 'failed', error = 'legacy terminal result', completed_at = 2
		WHERE id = 'active-attempt';
		UPDATE executions
		SET state = 'failed', updated_at = 2
		WHERE id = 'active-execution';
	`); err != nil {
		t.Fatal(err)
	}
	err = store.migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "drain_active_v2_attempts_before_upgrade") {
		t.Fatalf("stale active-count migration error = %v", err)
	}
	if _, err := database.Exec(`UPDATE workers SET active_count = 0 WHERE id = 'worker-a'`); err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('attempts')
		WHERE name = 'capacity_acknowledged'
	`).Scan(&capacityColumn); err != nil {
		t.Fatal(err)
	}
	if capacityColumn != 1 {
		t.Fatalf("drained retry capacity columns = %d; want 1", capacityColumn)
	}
}

func registerTestWorker(t *testing.T, store *Store, id string, capacity int, repositories ...protocol.RepositoryRegistration) protocol.Worker {
	t.Helper()
	worker, err := store.RegisterWorker(context.Background(), id, protocol.WorkerRegistration{
		Name:           id,
		WorkerVersion:  "test",
		RuntimeVersion: "codex-test",
		Capacity:       capacity,
		ActiveCount:    0,
		Health:         "healthy",
		Repositories:   repositories,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func createManagedTestRepository(t *testing.T, store *Store, remoteIdentity string) protocol.ManagedRepository {
	t.Helper()
	repository, _, err := store.CreateManagedRepository(
		context.Background(),
		protocol.CreateManagedRepositoryRequest{RemoteIdentity: remoteIdentity},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !repository.Enabled {
		repository, err = store.SetManagedRepositoryEnabled(context.Background(), repository.ID, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	return repository
}

func requireWorkerRepositoryOption(
	t *testing.T,
	store *Store,
	workerID string,
	repositoryID string,
) protocol.WorkerRepositoryOption {
	t.Helper()
	options, err := store.WorkerRepositoryOptions(context.Background(), workerID)
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range options {
		if option.ID == repositoryID {
			return option
		}
	}
	t.Fatalf("worker %q repository options do not contain %q: %#v", workerID, repositoryID, options)
	return protocol.WorkerRepositoryOption{}
}

func TestWorkerRuntimeDeterminesExecutionAndCannotChange(t *testing.T) {
	store := newTestStore(t)
	worker, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name:           "claude-worker",
		WorkerVersion:  "test",
		Runtime:        protocol.RuntimeClaudeCode,
		RuntimeVersion: "2.1.220",
		Capacity:       1,
		Health:         "healthy",
		Repositories: []protocol.RepositoryRegistration{{
			Key: "factory", RemoteIdentity: "github.com/example/factory",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	task := createTestTask(t, store, "claude-task", worker.ID, worker.Repositories[0].ID)
	if task.Execution.RequiredRuntime != protocol.RuntimeClaudeCode {
		t.Fatalf("execution runtime = %q", task.Execution.RequiredRuntime)
	}
	_, err = store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: "changed-worker", WorkerVersion: "test", Runtime: protocol.RuntimeCodex,
		Capacity: 1, Health: "healthy",
		Repositories: []protocol.RepositoryRegistration{{
			Key: "factory", RemoteIdentity: "github.com/example/factory",
		}},
	})
	assertErrorCode(t, err, "worker_runtime_changed")
}

func createTestTask(t *testing.T, store *Store, requestKey, workerID, repositoryID string) protocol.TaskDetail {
	t.Helper()
	task, created, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey:     requestKey,
		Title:          "  Test task  ",
		Description:    "preserve this prompt\n",
		WorkerID:       workerID,
		RepositoryID:   repositoryID,
		TimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected task to be created")
	}
	return task
}

func claimTestTask(t *testing.T, store *Store, workerID, requestID, token string) protocol.Claim {
	t.Helper()
	claim, err := store.Claim(context.Background(), workerID, protocol.ClaimRequest{
		RequestID:  requestID,
		LeaseToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if claim == nil {
		t.Fatal("expected a claim")
	}
	return *claim
}

func assertErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var service *ServiceError
	if !errors.As(err, &service) {
		t.Fatalf("expected ServiceError %q, got %v", code, err)
	}
	if service.Code != code {
		t.Fatalf("expected error code %q, got %q", code, service.Code)
	}
}

func TestDeleteTaskRequiresTerminalUnretainedHistoryAndPreservesUnrelatedData(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 2, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	target := createTestTask(t, store, "delete-target", workerA, worker.Repositories[0].ID)

	assertErrorCode(t, store.DeleteTask(context.Background(), target.Task.ID), "task_not_terminal")
	claim := claimTestTask(t, store, workerA, "delete-claim", tokenA)
	if claim.Task.ID != target.Task.ID {
		t.Fatalf("claimed task = %s, want %s", claim.Task.ID, target.Task.ID)
	}
	unrelated := createTestTask(t, store, "delete-unrelated", workerA, worker.Repositories[0].ID)
	assertErrorCode(t, store.DeleteTask(context.Background(), target.Task.ID), "task_not_terminal")
	if err := store.AppendEvents(context.Background(), claim.Attempt.ID, protocol.EventBatchRequest{
		LeaseToken: tokenA,
		Events: []protocol.AttemptEvent{{
			Sequence: 0, Kind: "progress", Payload: json.RawMessage(`{"text":"durable event"}`),
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "retain for inspection",
	}); err != nil {
		t.Fatal(err)
	}
	assertErrorCode(
		t,
		store.DeleteTask(context.Background(), target.Task.ID),
		"worktree_disposition_pending",
	)
	if _, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: "worker-a", WorkerVersion: "test", RuntimeVersion: "codex-test",
		Capacity: 2, Health: "healthy",
		Repositories: []protocol.RepositoryRegistration{{
			Key: "factory", RemoteIdentity: "github.com/owainlewis/factory", RetainedCount: 1,
		}},
		RetainedWorktrees: []protocol.RetainedWorktree{{
			AttemptID: claim.Attempt.ID, RepositoryID: worker.Repositories[0].ID,
			Path: "/tmp/factory-retained", Reason: "failed",
			CleanupCommand: "factory-worker cleanup " + claim.Attempt.ID,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	assertErrorCode(t, store.DeleteTask(context.Background(), target.Task.ID), "retained_worktree")

	disposedRegistration := protocol.WorkerRegistration{
		Name: "worker-a", WorkerVersion: "test", RuntimeVersion: "codex-test",
		Capacity: 2, Health: "healthy", CapacityHandoffVersion: 1,
		Repositories: []protocol.RepositoryRegistration{{
			Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
		}},
		DisposedAttemptIDs: []string{claim.Attempt.ID},
	}
	if _, err := store.RegisterWorker(context.Background(), workerA, disposedRegistration); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTask(context.Background(), target.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterWorker(context.Background(), workerA, disposedRegistration); err != nil {
		t.Fatalf("replaying disposed registration after history deletion: %v", err)
	}
	if _, err := store.Task(context.Background(), target.Task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted task read returned %v", err)
	}
	if _, err := store.Task(context.Background(), unrelated.Task.ID); err != nil {
		t.Fatalf("unrelated task was affected: %v", err)
	}
	for table, query := range map[string]string{
		"tasks":          `SELECT COUNT(*) FROM tasks WHERE id = ?`,
		"executions":     `SELECT COUNT(*) FROM executions WHERE id = ?`,
		"attempts":       `SELECT COUNT(*) FROM attempts WHERE id = ?`,
		"claim_requests": `SELECT COUNT(*) FROM claim_requests WHERE attempt_id = ?`,
		"attempt_events": `SELECT COUNT(*) FROM attempt_events WHERE attempt_id = ?`,
	} {
		argument := target.Task.ID
		if table == "executions" {
			argument = target.Execution.ID
		} else if table == "attempts" || table == "claim_requests" || table == "attempt_events" {
			argument = claim.Attempt.ID
		}
		var count int
		if err := store.db.QueryRow(query, argument).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d deleted records", table, count)
		}
	}
	var workerCount, repositoryCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM workers WHERE id = ?`, workerA).Scan(&workerCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM repositories WHERE id = ?`, worker.Repositories[0].ID).Scan(&repositoryCount); err != nil {
		t.Fatal(err)
	}
	if workerCount != 1 || repositoryCount != 1 {
		t.Fatalf("shared data changed: workers=%d repositories=%d", workerCount, repositoryCount)
	}
}

func TestTaskCreationIsNormalizedAndIdempotent(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})

	first := createTestTask(t, store, "request-1", workerA, worker.Repositories[0].ID)
	if first.Task.Title != "Test task" {
		t.Fatalf("title was not normalized: %q", first.Task.Title)
	}
	if first.Task.Description != "preserve this prompt\n" {
		t.Fatalf("description whitespace changed: %q", first.Task.Description)
	}
	duplicate, created, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey:     "request-1",
		Title:          "different title",
		Description:    "different description",
		WorkerID:       workerA,
		RepositoryID:   worker.Repositories[0].ID,
		TimeoutSeconds: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created || duplicate.Task.ID != first.Task.ID || duplicate.Task.Description != first.Task.Description {
		t.Fatalf("duplicate did not return original task: %#v", duplicate)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one task, got %d", count)
	}
}

func TestRoutedTaskChoosesLeastLoadedEligibleWorkerAndFreezesAssignment(t *testing.T) {
	store := newTestStore(t)
	fixed := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return fixed }
	repository := protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	}
	createManagedTestRepository(t, store, repository.RemoteIdentity)
	access := []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}}
	for _, workerID := range []string{workerA, workerB} {
		if _, err := store.RegisterWorker(context.Background(), workerID, protocol.WorkerRegistration{
			Name: workerID, WorkerVersion: "test", RuntimeVersion: "test",
			Capacity: 1, Health: "healthy", Repositories: []protocol.RepositoryRegistration{repository},
			SourceAccess: access,
		}); err != nil {
			t.Fatal(err)
		}
	}

	createRouted := func(requestKey string) protocol.TaskDetail {
		t.Helper()
		detail, created, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
			RequestKey: requestKey, Title: "GitHub issue", Description: "Fetch the live issue.",
			Route: &protocol.TaskRoute{
				RepositoryRemoteIdentity: repository.RemoteIdentity,
				SourceAccess:             access[0],
			},
			TimeoutSeconds: 60,
		})
		if err != nil || !created {
			t.Fatalf("create routed task: created %t, err %v", created, err)
		}
		return detail
	}

	first := createRouted("route-first")
	second := createRouted("route-second")
	third := createRouted("route-third")
	if first.Execution.AssignedWorkerID != workerA ||
		second.Execution.AssignedWorkerID != workerB ||
		third.Execution.AssignedWorkerID != workerA {
		t.Fatalf(
			"routed assignments = %s, %s, %s",
			first.Execution.AssignedWorkerID,
			second.Execution.AssignedWorkerID,
			third.Execution.AssignedWorkerID,
		)
	}
	if first.Repository.RemoteIdentity != repository.RemoteIdentity {
		t.Fatalf("routed repository = %#v", first.Repository)
	}

	if _, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: workerA, WorkerVersion: "test", RuntimeVersion: "test",
		Capacity: 1, Health: "healthy", Repositories: []protocol.RepositoryRegistration{repository},
	}); err != nil {
		t.Fatal(err)
	}
	replayed, created, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "route-first", Title: "Changed", Description: "Changed",
		Route: &protocol.TaskRoute{
			RepositoryRemoteIdentity: repository.RemoteIdentity,
			SourceAccess:             access[0],
		},
		TimeoutSeconds: 60,
	})
	if err != nil || created || replayed.Execution.AssignedWorkerID != workerA {
		t.Fatalf("replayed route = %#v, created %t, err %v", replayed, created, err)
	}
}

func TestRoutedTaskRequiresRepositoryAndSourceAccessOnHealthyOnlineWorker(t *testing.T) {
	store := newTestStore(t)
	repository := protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	}
	registerTestWorker(t, store, workerA, 1, repository)
	request := protocol.CreateTaskRequest{
		RequestKey: "route-ineligible", Title: "GitHub issue", Description: "Fetch live issue.",
		Route: &protocol.TaskRoute{
			RepositoryRemoteIdentity: repository.RemoteIdentity,
			SourceAccess: protocol.SourceAccess{
				Provider: "github", Hostname: "github.com",
			},
		},
		TimeoutSeconds: 60,
	}
	_, _, err := store.CreateTask(context.Background(), request)
	assertErrorCode(t, err, "repository_not_managed")
	createManagedTestRepository(t, store, repository.RemoteIdentity)
	_, _, err = store.CreateTask(context.Background(), request)
	assertErrorCode(t, err, "no_eligible_worker")

	if _, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: workerA, WorkerVersion: "test", RuntimeVersion: "test",
		Capacity: 1, Health: "healthy", Repositories: []protocol.RepositoryRegistration{repository},
		SourceAccess: []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	}); err != nil {
		t.Fatal(err)
	}
	request.RequestKey = "route-wrong-repository"
	request.Route.RepositoryRemoteIdentity = "github.com/owainlewis/other"
	_, _, err = store.CreateTask(context.Background(), request)
	assertErrorCode(t, err, "repository_not_managed")

	request.RequestKey = "route-unsafe-repository"
	request.Route.RepositoryRemoteIdentity = "https://github.com/owainlewis/factory"
	_, _, err = store.CreateTask(context.Background(), request)
	assertErrorCode(t, err, "invalid_route")
}

func TestManagedRepositoryCatalogIsCanonicalIdempotentAndDisableable(t *testing.T) {
	store := newTestStore(t)
	createdRepository, created, err := store.CreateManagedRepository(
		context.Background(),
		protocol.CreateManagedRepositoryRequest{RemoteIdentity: " GitHub.com/OwainLewis/Factory.git "},
	)
	if err != nil || !created {
		t.Fatalf("create managed repository: created %t, err %v", created, err)
	}
	if createdRepository.RemoteIdentity != "github.com/owainlewis/factory" || !createdRepository.Enabled {
		t.Fatalf("created managed repository = %#v", createdRepository)
	}
	replayed, created, err := store.CreateManagedRepository(
		context.Background(),
		protocol.CreateManagedRepositoryRequest{RemoteIdentity: "github.com/owainlewis/factory"},
	)
	if err != nil || created || replayed.ID != createdRepository.ID {
		t.Fatalf("replayed managed repository = %#v, created %t, err %v", replayed, created, err)
	}
	repositories, err := store.ManagedRepositories(context.Background())
	if err != nil || len(repositories) != 1 || repositories[0].ID != createdRepository.ID {
		t.Fatalf("managed repositories = %#v, err %v", repositories, err)
	}
	disabled, err := store.SetManagedRepositoryEnabled(context.Background(), createdRepository.ID, false)
	if err != nil || disabled.Enabled || disabled.UpdatedAt.Before(disabled.CreatedAt) {
		t.Fatalf("disabled managed repository = %#v, err %v", disabled, err)
	}
	if _, _, err := store.CreateManagedRepository(
		context.Background(),
		protocol.CreateManagedRepositoryRequest{RemoteIdentity: "https://github.com/owainlewis/factory"},
	); err == nil {
		t.Fatal("URL-shaped managed repository identity was accepted")
	} else {
		assertErrorCode(t, err, "invalid_repository")
	}
}

func TestManagedRepositoryCatalogPromotesOnlyWorkerDiscoveredRows(t *testing.T) {
	store := newTestStore(t)
	remoteIdentity := "github.com/example/discovered"
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "discovered", RemoteIdentity: remoteIdentity,
	})
	if len(worker.Repositories) != 1 {
		t.Fatalf("worker repositories = %#v", worker.Repositories)
	}
	discovered, err := store.ManagedRepository(context.Background(), worker.Repositories[0].ID)
	if err != nil || discovered.Enabled {
		t.Fatalf("worker-discovered repository = %#v, err %v", discovered, err)
	}
	promoted, created, err := store.CreateManagedRepository(
		context.Background(), protocol.CreateManagedRepositoryRequest{RemoteIdentity: remoteIdentity},
	)
	if err != nil || created || !promoted.Enabled || promoted.ID != discovered.ID {
		t.Fatalf("promoted repository = %#v, created %t, err %v", promoted, created, err)
	}
	disabled, err := store.SetManagedRepositoryEnabled(context.Background(), promoted.ID, false)
	if err != nil || disabled.Enabled {
		t.Fatalf("disable promoted repository = %#v, err %v", disabled, err)
	}
	replayed, created, err := store.CreateManagedRepository(
		context.Background(), protocol.CreateManagedRepositoryRequest{RemoteIdentity: remoteIdentity},
	)
	if err != nil || created || replayed.Enabled {
		t.Fatalf("replayed explicitly disabled repository = %#v, created %t, err %v", replayed, created, err)
	}
}

func TestManagedRepositoryCatalogEnforcesItsHardLimit(t *testing.T) {
	store := newTestStore(t)
	for index := 0; index < protocol.MaxManagedRepositories; index++ {
		if _, err := store.db.Exec(`
			INSERT INTO repositories(id, remote_identity, enabled, created_at, updated_at)
			VALUES (?, ?, 1, 1, 1)
		`, fmt.Sprintf("repository-%04d", index), fmt.Sprintf("github.com/example/repository-%04d", index)); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := store.CreateManagedRepository(
		context.Background(),
		protocol.CreateManagedRepositoryRequest{RemoteIdentity: "github.com/example/over-limit"},
	)
	assertErrorCode(t, err, "repository_limit_reached")
	_, err = store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: workerA, WorkerVersion: "test", RuntimeVersion: "test",
		Capacity: 1, Health: "healthy",
		Repositories: []protocol.RepositoryRegistration{{
			Key: "over-limit", RemoteIdentity: "github.com/example/worker-over-limit",
		}},
	})
	assertErrorCode(t, err, "repository_limit_reached")
	var repositoryCount, workerCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM repositories`).Scan(&repositoryCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM workers WHERE id = ?`, workerA).Scan(&workerCount); err != nil {
		t.Fatal(err)
	}
	if repositoryCount != protocol.MaxManagedRepositories || workerCount != 0 {
		t.Fatalf("limit rollback repositories=%d workers=%d", repositoryCount, workerCount)
	}
}

func TestRoutedTaskCanFreezeAZeroRepositoryCattleWorker(t *testing.T) {
	store := newTestStore(t)
	repository, _, err := store.CreateManagedRepository(
		context.Background(),
		protocol.CreateManagedRepositoryRequest{RemoteIdentity: "github.com/owainlewis/factory"},
	)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: workerA, WorkerVersion: "test", RuntimeVersion: "codex-test",
		Capacity: 1, Health: "healthy",
		SourceAccess:               []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
		AcceptsManagedRepositories: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(worker.Repositories) != 0 || !worker.AcceptsManagedRepositories {
		t.Fatalf("initial cattle worker = %#v", worker)
	}
	detail, created, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "cattle-route", Title: "Cattle route", Description: "Fetch the live issue.",
		Route: &protocol.TaskRoute{
			RepositoryRemoteIdentity: repository.RemoteIdentity,
			SourceAccess:             protocol.SourceAccess{Provider: "github", Hostname: "github.com"},
		},
		TimeoutSeconds: 60,
	})
	if err != nil || !created {
		t.Fatalf("create cattle task: created %t, err %v", created, err)
	}
	if detail.Execution.AssignedWorkerID != workerA || detail.Repository.ID != repository.ID {
		t.Fatalf("cattle route detail = %#v", detail)
	}
	worker, err = store.Worker(context.Background(), workerA)
	if err != nil {
		t.Fatal(err)
	}
	if len(worker.Repositories) != 1 || worker.Repositories[0].ID != repository.ID ||
		worker.Repositories[0].Key != repository.RemoteIdentity {
		t.Fatalf("frozen dynamic repository = %#v", worker.Repositories)
	}

	if _, err := store.SetManagedRepositoryEnabled(context.Background(), repository.ID, false); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "disabled-cattle-route", Title: "Disabled route", Description: "Do not run.",
		Route: &protocol.TaskRoute{
			RepositoryRemoteIdentity: repository.RemoteIdentity,
			SourceAccess:             protocol.SourceAccess{Provider: "github", Hostname: "github.com"},
		},
		TimeoutSeconds: 60,
	})
	assertErrorCode(t, err, "repository_not_managed")
}

func TestRoutedTaskCanTargetOneEligibleWorker(t *testing.T) {
	store := newTestStore(t)
	repository := createManagedTestRepository(t, store, "github.com/example/selected")
	registration := protocol.WorkerRegistration{
		WorkerVersion: "test", RuntimeVersion: "test", Capacity: 1, Health: "healthy",
		SourceAccess:               []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
		AcceptsManagedRepositories: true,
	}
	registration.Name = workerA
	if _, err := store.RegisterWorker(context.Background(), workerA, registration); err != nil {
		t.Fatal(err)
	}
	registration.Name = workerB
	if _, err := store.RegisterWorker(context.Background(), workerB, registration); err != nil {
		t.Fatal(err)
	}

	detail, created, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "selected-worker-route", Title: "Selected worker route",
		Description: "Acquire the configured repository on the selected worker.",
		WorkerID:    workerB,
		Route: &protocol.TaskRoute{
			RepositoryRemoteIdentity: repository.RemoteIdentity,
			SourceAccess:             protocol.SourceAccess{Provider: "github", Hostname: "github.com"},
		},
		TimeoutSeconds: 60,
	})
	if err != nil || !created {
		t.Fatalf("create selected worker route: created %t, err %v", created, err)
	}
	if detail.Execution.AssignedWorkerID != workerB || detail.Repository.ID != repository.ID {
		t.Fatalf("selected worker route detail = %#v", detail)
	}
	worker, err := store.Worker(context.Background(), workerB)
	if err != nil {
		t.Fatal(err)
	}
	if len(worker.Repositories) != 1 || worker.Repositories[0].ID != repository.ID {
		t.Fatalf("selected worker repositories = %#v", worker.Repositories)
	}

	registration.Name = workerA
	registration.Health = "unhealthy"
	if _, err := store.RegisterWorker(context.Background(), workerA, registration); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "ineligible-selected-worker-route", Title: "Do not fall back",
		Description: "The selected worker constraint must remain authoritative.",
		WorkerID:    workerA,
		Route: &protocol.TaskRoute{
			RepositoryRemoteIdentity: repository.RemoteIdentity,
			SourceAccess:             protocol.SourceAccess{Provider: "github", Hostname: "github.com"},
		},
		TimeoutSeconds: 60,
	})
	assertErrorCode(t, err, "no_eligible_worker")
}

func TestRoutedTaskSkipsWorkerWithConflictingDynamicDisplayKey(t *testing.T) {
	store := newTestStore(t)
	target := createManagedTestRepository(t, store, "github.com/example/target")
	common := protocol.WorkerRegistration{
		WorkerVersion: "test", RuntimeVersion: "test", Capacity: 1, Health: "healthy",
		SourceAccess:               []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
		AcceptsManagedRepositories: true,
	}
	workerARegistration := common
	workerARegistration.Name = workerA
	workerARegistration.Repositories = []protocol.RepositoryRegistration{{
		Key: target.RemoteIdentity, RemoteIdentity: "github.com/example/different",
	}}
	if _, err := store.RegisterWorker(context.Background(), workerA, workerARegistration); err != nil {
		t.Fatal(err)
	}
	workerBRegistration := common
	workerBRegistration.Name = workerB
	if _, err := store.RegisterWorker(context.Background(), workerB, workerBRegistration); err != nil {
		t.Fatal(err)
	}
	option := requireWorkerRepositoryOption(t, store, workerA, target.ID)
	if option.Ready || option.Reason != "Another advertised repository uses this routing identity." {
		t.Fatalf("conflicting repository option = %#v", option)
	}
	detail, created, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "display-key-collision", Title: "Display key collision",
		Description: "Route around the collision.",
		Route: &protocol.TaskRoute{
			RepositoryRemoteIdentity: target.RemoteIdentity,
			SourceAccess:             protocol.SourceAccess{Provider: "github", Hostname: "github.com"},
		},
		TimeoutSeconds: 60,
	})
	if err != nil || !created {
		t.Fatalf("route around display key collision: created %t, err %v", created, err)
	}
	if detail.Execution.AssignedWorkerID != workerB {
		t.Fatalf("collision route assigned worker %q; want %q", detail.Execution.AssignedWorkerID, workerB)
	}
}

func TestWorkerRepositoryOptionsRejectUnsupportedManagedSourceButKeepDirectCheckout(t *testing.T) {
	store := newTestStore(t)
	legacyRemote := "gitlab.com/example/legacy"
	registration := protocol.WorkerRegistration{
		Name: workerA, WorkerVersion: "test", RuntimeVersion: "test",
		Capacity: 1, Health: "healthy",
		Repositories:               []protocol.RepositoryRegistration{{Key: "legacy", RemoteIdentity: legacyRemote}},
		SourceAccess:               []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
		AcceptsManagedRepositories: true,
	}
	advertisingWorker, err := store.RegisterWorker(context.Background(), workerA, registration)
	if err != nil {
		t.Fatal(err)
	}
	legacyRepositoryID := advertisingWorker.Repositories[0].ID
	if _, err := store.db.Exec(`UPDATE repositories SET enabled = 1 WHERE id = ?`, legacyRepositoryID); err != nil {
		t.Fatal(err)
	}

	advertised := requireWorkerRepositoryOption(t, store, workerA, legacyRepositoryID)
	if !advertised.Advertised || !advertised.Ready {
		t.Fatalf("advertised legacy repository option = %#v", advertised)
	}
	if _, _, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "legacy-direct-checkout", Title: "Use direct checkout",
		Description: "Keep advertised legacy checkouts assignable.",
		WorkerID:    workerA, RepositoryID: legacyRepositoryID, TimeoutSeconds: 60,
	}); err != nil {
		t.Fatalf("direct legacy checkout assignment: %v", err)
	}

	registration.Name = workerB
	registration.Repositories = nil
	if _, err := store.RegisterWorker(context.Background(), workerB, registration); err != nil {
		t.Fatal(err)
	}
	managed := requireWorkerRepositoryOption(t, store, workerB, legacyRepositoryID)
	if managed.Advertised || managed.Ready || managed.Reason != "Repository source is not supported for managed acquisition." {
		t.Fatalf("unsupported managed repository option = %#v", managed)
	}
}

func TestRoutedTaskReservesManagedRepositoryCacheHeadroom(t *testing.T) {
	store := newTestStore(t)
	firstRepository := createManagedTestRepository(t, store, "github.com/example/first")
	secondRepository := createManagedTestRepository(t, store, "github.com/example/second")
	cachedRepositoryIDs := make([]string, 0, protocol.MaxRepositoryCacheEntries-1)
	for index := 0; index < protocol.MaxRepositoryCacheEntries-1; index++ {
		cachedRepositoryIDs = append(cachedRepositoryIDs, fmt.Sprintf(
			"%08x-0000-4000-8000-%012x", index+1, index+1,
		))
	}
	registration := protocol.WorkerRegistration{
		Name: workerA, WorkerVersion: "test", RuntimeVersion: "test",
		Capacity: 1, Health: "healthy",
		SourceAccess:               []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
		AcceptsManagedRepositories: true,
		ManagedRepositoryIDs:       cachedRepositoryIDs,
	}
	if _, err := store.RegisterWorker(context.Background(), workerA, registration); err != nil {
		t.Fatal(err)
	}
	createRouted := func(requestKey string, repository protocol.ManagedRepository) (protocol.TaskDetail, error) {
		detail, _, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
			RequestKey: requestKey, Title: requestKey, Description: "Exercise cache routing.",
			Route: &protocol.TaskRoute{
				RepositoryRemoteIdentity: repository.RemoteIdentity,
				SourceAccess:             protocol.SourceAccess{Provider: "github", Hostname: "github.com"},
			},
			TimeoutSeconds: 60,
		})
		return detail, err
	}
	first, err := createRouted("cache-reservation-first", firstRepository)
	if err != nil || first.Execution.AssignedWorkerID != workerA {
		t.Fatalf("first cache reservation = %#v, err %v", first, err)
	}
	_, err = createRouted("cache-reservation-blocked", secondRepository)
	assertErrorCode(t, err, "no_eligible_worker")
	readiness, err := store.ManagedRepositoryReadiness(context.Background(), secondRepository.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.RoutingReady || len(readiness.Workers) != 1 || readiness.Workers[0].Ready ||
		readiness.Workers[0].Reason != "Managed repository cache and reservations are full." {
		t.Fatalf("reserved cache readiness = %#v", readiness)
	}
	option := requireWorkerRepositoryOption(t, store, workerA, secondRepository.ID)
	if option.Ready || option.Reason != readiness.Workers[0].Reason {
		t.Fatalf("reserved cache repository option = %#v, readiness = %#v", option, readiness.Workers[0])
	}

	registration.ManagedRepositoryIDs = append(registration.ManagedRepositoryIDs, firstRepository.ID)
	if _, err := store.RegisterWorker(context.Background(), workerA, registration); err != nil {
		t.Fatal(err)
	}
	if _, err := createRouted("cache-reservation-reuse", firstRepository); err != nil {
		t.Fatalf("route to cached repository: %v", err)
	}
	_, err = createRouted("cache-reservation-full", secondRepository)
	assertErrorCode(t, err, "no_eligible_worker")
}

func TestRegistrationReleasesFailedUncachedRepositoryReservation(t *testing.T) {
	store := newTestStore(t)
	failedRepository := createManagedTestRepository(t, store, "github.com/example/failed-clone")
	nextRepository := createManagedTestRepository(t, store, "github.com/example/next-clone")
	cachedRepositoryIDs := make([]string, 0, protocol.MaxRepositoryCacheEntries-1)
	for index := 0; index < protocol.MaxRepositoryCacheEntries-1; index++ {
		cachedRepositoryIDs = append(cachedRepositoryIDs, fmt.Sprintf(
			"%08x-0000-4000-8000-%012x", index+1, index+1,
		))
	}
	registration := protocol.WorkerRegistration{
		Name: workerA, WorkerVersion: "test", RuntimeVersion: "test",
		Capacity: 1, Health: "healthy",
		SourceAccess:               []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
		AcceptsManagedRepositories: true,
		ManagedRepositoryIDs:       cachedRepositoryIDs,
	}
	if _, err := store.RegisterWorker(context.Background(), workerA, registration); err != nil {
		t.Fatal(err)
	}
	createRouted := func(requestKey string, repository protocol.ManagedRepository) (protocol.TaskDetail, error) {
		detail, _, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
			RequestKey: requestKey, Title: requestKey, Description: "Exercise failed acquisition recovery.",
			Route: &protocol.TaskRoute{
				RepositoryRemoteIdentity: repository.RemoteIdentity,
				SourceAccess:             protocol.SourceAccess{Provider: "github", Hostname: "github.com"},
			},
			TimeoutSeconds: 60,
		})
		return detail, err
	}
	failed, err := createRouted("failed-cache-reservation", failedRepository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createRouted("blocked-by-failed-reservation", nextRepository); err == nil {
		t.Fatal("cache headroom ignored the outstanding acquisition reservation")
	} else {
		assertErrorCode(t, err, "no_eligible_worker")
	}
	claim := claimTestTask(t, store, workerA, "failed-cache-claim", tokenA)
	if claim.Task.ID != failed.Task.ID {
		t.Fatalf("claimed task = %s; want %s", claim.Task.ID, failed.Task.ID)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "clone failed",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterWorker(context.Background(), workerA, registration); err != nil {
		t.Fatal(err)
	}
	detail, err := store.Task(context.Background(), failed.Task.ID)
	if err != nil || detail.Repository.ID != failedRepository.ID || detail.RepositoryAvailable {
		t.Fatalf("failed task after reservation release = %#v, err %v", detail, err)
	}
	next, err := createRouted("route-after-failed-reservation", nextRepository)
	if err != nil {
		t.Fatalf("failed reservation still consumes cache headroom: %v", err)
	}
	var remaining, advertised int
	if err := store.db.QueryRow(`
		SELECT COUNT(*), COALESCE(MAX(advertised), 0)
		FROM worker_repositories
		WHERE worker_id = ? AND repository_id = ? AND dynamic = 1
	`, workerA, failedRepository.ID).Scan(&remaining, &advertised); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 || advertised != 0 {
		t.Fatalf("released historical association count = %d, advertised = %d", remaining, advertised)
	}
	if _, err := store.RetryExecution(context.Background(), failed.Execution.ID); err == nil {
		t.Fatal("retry ignored managed repository cache headroom")
	} else {
		assertErrorCode(t, err, "retry_repository_unavailable")
	}
	failedAfterBlockedRetry, err := store.Task(context.Background(), failed.Task.ID)
	if err != nil || failedAfterBlockedRetry.Execution.State != "failed" || failedAfterBlockedRetry.RepositoryAvailable {
		t.Fatalf("blocked retry changed failed execution = %#v, err %v", failedAfterBlockedRetry, err)
	}
	if _, err := store.CancelTask(context.Background(), next.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterWorker(context.Background(), workerA, registration); err != nil {
		t.Fatal(err)
	}
	retried, err := store.RetryExecution(context.Background(), failed.Execution.ID)
	if err != nil {
		t.Fatalf("retry released failed acquisition: %v", err)
	}
	if retried.Execution.State != "queued" || !retried.RepositoryAvailable {
		t.Fatalf("retried failed acquisition = %#v", retried)
	}
	retryClaim := claimTestTask(t, store, workerA, "failed-cache-retry-claim", tokenB)
	if retryClaim.Task.ID != failed.Task.ID || retryClaim.Attempt.AttemptNumber != 2 {
		t.Fatalf("retry claim = %#v", retryClaim)
	}
}

func TestRoutedTaskMatchesGitHubRepositoryIdentityWithoutCaseSensitivity(t *testing.T) {
	store := newTestStore(t)
	repository := protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	}
	createManagedTestRepository(t, store, repository.RemoteIdentity)
	if _, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: workerA, WorkerVersion: "test", RuntimeVersion: "test",
		Capacity: 1, Health: "healthy", Repositories: []protocol.RepositoryRegistration{repository},
		SourceAccess: []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	}); err != nil {
		t.Fatal(err)
	}
	detail, created, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "route-github-case", Title: "GitHub issue", Description: "Fetch live issue.",
		Route: &protocol.TaskRoute{
			RepositoryRemoteIdentity: "github.com/OwainLewis/Factory",
			SourceAccess: protocol.SourceAccess{
				Provider: "github", Hostname: "github.com",
			},
		},
		TimeoutSeconds: 60,
	})
	if err != nil || !created || detail.Execution.AssignedWorkerID != workerA {
		t.Fatalf("case-insensitive GitHub route = %#v, created %t, err %v", detail, created, err)
	}
}

func TestRoutedTaskExcludesWorkersWithoutRepositoryCapacity(t *testing.T) {
	for _, capacityTerm := range []string{"retained", "active", "terminal-unacknowledged"} {
		t.Run(capacityTerm, func(t *testing.T) {
			store := newTestStore(t)
			repository := protocol.RepositoryRegistration{
				Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
				RetainedCount: protocol.MaxRetainedPerRepo - 1,
			}
			createManagedTestRepository(t, store, repository.RemoteIdentity)
			if capacityTerm == "retained" {
				repository.RetainedCount = protocol.MaxRetainedPerRepo
			}
			access := []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}}
			fullWorker, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
				Name: workerA, WorkerVersion: "test", RuntimeVersion: "test",
				Capacity: 2, Health: "healthy", Repositories: []protocol.RepositoryRegistration{repository},
				SourceAccess: access,
			})
			if err != nil {
				t.Fatal(err)
			}
			repository.RetainedCount = 0
			if _, err := store.RegisterWorker(context.Background(), workerB, protocol.WorkerRegistration{
				Name: workerB, WorkerVersion: "test", RuntimeVersion: "test",
				Capacity: 1, Health: "healthy", Repositories: []protocol.RepositoryRegistration{repository},
				SourceAccess: access,
			}); err != nil {
				t.Fatal(err)
			}

			if capacityTerm != "retained" {
				createTestTask(t, store, "capacity-reservation", workerA, fullWorker.Repositories[0].ID)
				claim := claimTestTask(t, store, workerA, "capacity-reservation", tokenA)
				if capacityTerm == "terminal-unacknowledged" {
					if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID,
						protocol.CompleteAttemptRequest{
							LeaseToken: tokenA, State: "failed", Error: "retained",
						}); err != nil {
						t.Fatal(err)
					}
				}
			}

			request := protocol.CreateTaskRequest{
				RequestKey: "route-repository-capacity", Title: "GitHub issue", Description: "Fetch live issue.",
				Route: &protocol.TaskRoute{
					RepositoryRemoteIdentity: repository.RemoteIdentity,
					SourceAccess:             access[0],
				},
				TimeoutSeconds: 60,
			}
			detail, created, err := store.CreateTask(context.Background(), request)
			if err != nil || !created || detail.Execution.AssignedWorkerID != workerB {
				t.Fatalf("repository-capacity route = %#v, created %t, err %v", detail, created, err)
			}

			repository.RetainedCount = protocol.MaxRetainedPerRepo
			if _, err := store.RegisterWorker(context.Background(), workerB, protocol.WorkerRegistration{
				Name: workerB, WorkerVersion: "test", RuntimeVersion: "test",
				Capacity: 1, Health: "healthy", Repositories: []protocol.RepositoryRegistration{repository},
				SourceAccess: access,
			}); err != nil {
				t.Fatal(err)
			}
			request.RequestKey = "route-no-repository-capacity"
			_, _, err = store.CreateTask(context.Background(), request)
			assertErrorCode(t, err, "no_eligible_worker")
		})
	}
}

func TestTasksPagesEqualTimestampsByIDWithoutDuplicates(t *testing.T) {
	store := newTestStore(t)
	store.now = func() time.Time {
		return time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	}
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	var expected []string
	for index := 0; index < 5; index++ {
		task := createTestTask(t, store, fmt.Sprintf("page-task-%d", index), workerA, worker.Repositories[0].ID)
		expected = append(expected, task.Task.ID)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(expected)))

	var actual []string
	request := protocol.TaskPageRequest{Limit: 2}
	for {
		page, err := store.Tasks(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Tasks) > request.Limit {
			t.Fatalf("page returned %d tasks with limit %d", len(page.Tasks), request.Limit)
		}
		for _, task := range page.Tasks {
			actual = append(actual, task.ID)
		}
		if page.NextCursor == nil {
			break
		}
		request.Cursor = page.NextCursor
	}
	if fmt.Sprint(actual) != fmt.Sprint(expected) {
		t.Fatalf("paged task IDs = %v; want %v", actual, expected)
	}
}

func TestDatabaseUsesWALAndRefusesAnUnmarkedExistingDatabase(t *testing.T) {
	root := t.TempDir()
	path := root + "/restart.sqlite3"
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	var journalMode string
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal mode = %q, want wal", journalMode)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen marked database: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	unknown := root + "/unknown.sqlite3"
	original := []byte("existing unknown bytes must stay untouched")
	if err := os.WriteFile(unknown, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), unknown); err == nil {
		t.Fatal("opened an unmarked existing database")
	}
	after, err := os.ReadFile(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("unmarked database was modified")
	}
}

type failingDatabaseMarkerFile struct {
	*os.File
	failure    string
	err        error
	closeCalls int
}

func (f *failingDatabaseMarkerFile) WriteString(value string) (int, error) {
	if f.failure == "write" {
		const partial = "factory-v2"
		n, writeErr := f.File.WriteString(partial)
		if writeErr != nil {
			return n, writeErr
		}
		return n, f.err
	}
	return f.File.WriteString(value)
}

func (f *failingDatabaseMarkerFile) Sync() error {
	if f.failure == "sync" {
		return f.err
	}
	return f.File.Sync()
}

func (f *failingDatabaseMarkerFile) Close() error {
	f.closeCalls++
	closeErr := f.File.Close()
	if f.failure == "close" {
		return errors.Join(closeErr, f.err)
	}
	return closeErr
}

func TestDatabaseMarkerInitializationFailuresAreRecoverable(t *testing.T) {
	for _, failure := range []string{"write", "sync", "close"} {
		t.Run(failure, func(t *testing.T) {
			path := t.TempDir() + "/controlplane.sqlite3"
			marker := path + ".v2-control-plane"
			injectedErr := fmt.Errorf("injected %s failure", failure)
			var failedFile *failingDatabaseMarkerFile

			err := createDatabaseMarkerWith(marker, func(path string) (databaseMarkerFile, error) {
				file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				if err != nil {
					return nil, err
				}
				failedFile = &failingDatabaseMarkerFile{File: file, failure: failure, err: injectedErr}
				return failedFile, nil
			})
			if !errors.Is(err, injectedErr) {
				t.Fatalf("create marker error = %v, want injected failure", err)
			}
			if failedFile.closeCalls != 1 {
				t.Fatalf("close calls = %d, want 1", failedFile.closeCalls)
			}
			if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed marker still exists: %v", err)
			}

			if err := prepareDatabasePath(path); err != nil {
				t.Fatalf("retry marker initialization: %v", err)
			}
			body, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != "factory-v2-control-plane\n" {
				t.Fatalf("retry marker contents = %q", body)
			}
		})
	}
}

func TestRepositoryAssignmentAndWorkerCapacityAreEnforced(t *testing.T) {
	store := newTestStore(t)
	a := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	registerTestWorker(t, store, workerB, 2, protocol.RepositoryRegistration{
		Key: "other", RemoteIdentity: "github.com/owainlewis/other",
	})
	if _, _, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "wrong-repo", Title: "Wrong", Description: "Wrong worker repository",
		WorkerID: workerB, RepositoryID: a.Repositories[0].ID,
	}); err == nil {
		t.Fatal("cross-worker repository assignment succeeded")
	} else {
		assertErrorCode(t, err, "repository_not_advertised")
	}

	task := createTestTask(t, store, "assigned-a", workerA, a.Repositories[0].ID)
	claim, err := store.Claim(context.Background(), workerB, protocol.ClaimRequest{RequestID: "worker-b-claim", LeaseToken: tokenB})
	if err != nil {
		t.Fatal(err)
	}
	if claim != nil {
		t.Fatalf("worker B claimed task assigned to worker A: %#v", claim)
	}
	owned := claimTestTask(t, store, workerA, "worker-a-claim", tokenA)
	if owned.Task.ID != task.Task.ID {
		t.Fatalf("claimed wrong task: got %s want %s", owned.Task.ID, task.Task.ID)
	}

	createTestTask(t, store, "second-a", workerA, a.Repositories[0].ID)
	claim, err = store.Claim(context.Background(), workerA, protocol.ClaimRequest{RequestID: "at-capacity", LeaseToken: tokenB})
	if err != nil {
		t.Fatal(err)
	}
	if claim != nil {
		t.Fatal("worker claimed above its capacity")
	}
}

func TestConcurrentWorkerListsDoNotExhaustTheConnectionPool(t *testing.T) {
	store := newTestStore(t)
	store.db.SetMaxOpenConns(1)
	registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	start := make(chan struct{})
	errs := make(chan error, 4)
	var wait sync.WaitGroup
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			workers, err := store.Workers(ctx)
			if err == nil && (len(workers) != 1 || len(workers[0].Repositories) != 1) {
				err = fmt.Errorf("incomplete worker list: %#v", workers)
			}
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnhealthyAndOfflineWorkersDoNotClaim(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	worker, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: workerA, WorkerVersion: "test", RuntimeVersion: "test", Capacity: 1, Health: "unhealthy",
		Repositories: []protocol.RepositoryRegistration{{Key: "factory", RemoteIdentity: "github.com/owainlewis/factory"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	createTestTask(t, store, "health-gated", workerA, worker.Repositories[0].ID)
	claim, err := store.Claim(context.Background(), workerA, protocol.ClaimRequest{RequestID: "unhealthy", LeaseToken: tokenA})
	if err != nil || claim != nil {
		t.Fatalf("unhealthy worker claim = %#v, %v", claim, err)
	}
	worker = registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	now = now.Add(protocol.WorkerOnlineWindow + time.Millisecond)
	claim, err = store.Claim(context.Background(), workerA, protocol.ClaimRequest{RequestID: "offline", LeaseToken: tokenA})
	if err != nil || claim != nil {
		t.Fatalf("offline worker claim = %#v, %v", claim, err)
	}
	now = now.Add(time.Millisecond)
	registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	claimTestTask(t, store, workerA, "online-again", tokenA)
}

func TestTaskDetailReportsRepositoryAvailability(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 1,
		protocol.RepositoryRegistration{Key: "factory", RemoteIdentity: "github.com/owainlewis/factory"},
		protocol.RepositoryRegistration{Key: "other", RemoteIdentity: "github.com/owainlewis/other"},
	)
	task := createTestTask(t, store, "repository-availability", workerA, worker.Repositories[0].ID)
	if !task.RepositoryAvailable || task.Repository.ID != task.Task.RepositoryID {
		t.Fatalf("new task repository detail: %#v", task)
	}
	registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "other", RemoteIdentity: "github.com/owainlewis/other",
	})
	detail, err := store.Task(context.Background(), task.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.RepositoryAvailable {
		t.Fatal("task detail reported a removed repository as available")
	}
	claim, err := store.Claim(context.Background(), workerA, protocol.ClaimRequest{
		RequestID: "unavailable-repository", LeaseToken: tokenA,
	})
	if err != nil || claim != nil {
		t.Fatalf("claim for unavailable repository = %#v, %v", claim, err)
	}
}

func TestClaimOrderingRepositoryFilteringAndReplay(t *testing.T) {
	store := newTestStore(t)
	fixed := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return fixed }
	worker := registerTestWorker(t, store, workerA, 2,
		protocol.RepositoryRegistration{Key: "full", RemoteIdentity: "github.com/example/full", RetainedCount: 10},
		protocol.RepositoryRegistration{Key: "open", RemoteIdentity: "github.com/example/open"},
	)
	repositories := map[string]string{}
	for _, repo := range worker.Repositories {
		repositories[repo.Key] = repo.ID
	}
	createTestTask(t, store, "oldest-but-full", workerA, repositories["full"])
	fixed = fixed.Add(time.Millisecond)
	eligible := createTestTask(t, store, "eligible", workerA, repositories["open"])

	claim := claimTestTask(t, store, workerA, "claim-replay", tokenA)
	if claim.Task.ID != eligible.Task.ID {
		t.Fatalf("retained-capacity filter chose %s, want %s", claim.Task.ID, eligible.Task.ID)
	}
	if claim.Task.State != "running" || claim.Execution.State != "preparing" {
		t.Fatalf("task-level preparing mapping is wrong: task=%s execution=%s", claim.Task.State, claim.Execution.State)
	}
	replay := claimTestTask(t, store, workerA, "claim-replay", tokenA)
	if replay.Attempt.ID != claim.Attempt.ID {
		t.Fatalf("claim replay created a different attempt: %s != %s", replay.Attempt.ID, claim.Attempt.ID)
	}
	if _, err := store.Claim(context.Background(), workerA, protocol.ClaimRequest{
		RequestID: "claim-replay", LeaseToken: tokenB,
	}); err == nil {
		t.Fatal("conflicting claim token succeeded")
	} else {
		assertErrorCode(t, err, "claim_request_conflict")
	}
	var attempts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("expected one attempt after replay, got %d", attempts)
	}
}

func TestClaimReservesRepositoryRetainedHeadroom(t *testing.T) {
	store := newTestStore(t)
	fixed := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return fixed }
	worker := registerTestWorker(t, store, workerA, 2,
		protocol.RepositoryRegistration{
			Key: "nearly-full", RemoteIdentity: "github.com/example/nearly-full",
			RetainedCount: protocol.MaxRetainedPerRepo - 1,
		},
		protocol.RepositoryRegistration{Key: "open", RemoteIdentity: "github.com/example/open"},
	)
	repositories := map[string]string{}
	for _, repository := range worker.Repositories {
		repositories[repository.Key] = repository.ID
	}
	first := createTestTask(t, store, "nearly-full-first", workerA, repositories["nearly-full"])
	fixed = fixed.Add(time.Millisecond)
	createTestTask(t, store, "nearly-full-second", workerA, repositories["nearly-full"])
	fixed = fixed.Add(time.Millisecond)
	other := createTestTask(t, store, "open-while-reserved", workerA, repositories["open"])

	claimedFirst := claimTestTask(t, store, workerA, "reserve-first", tokenA)
	if claimedFirst.Task.ID != first.Task.ID {
		t.Fatalf("first claim = %s; want %s", claimedFirst.Task.ID, first.Task.ID)
	}
	claimedOther := claimTestTask(t, store, workerA, "reserve-other", tokenB)
	if claimedOther.Task.ID != other.Task.ID {
		t.Fatalf("second claim = %s; want open repository task %s", claimedOther.Task.ID, other.Task.ID)
	}
}

func TestTerminalAttemptReservesRetainedHeadroomUntilRegistration(t *testing.T) {
	store := newTestStore(t)
	fixed := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return fixed }
	worker := registerTestWorker(t, store, workerA, 2,
		protocol.RepositoryRegistration{
			Key: "nearly-full", RemoteIdentity: "github.com/example/nearly-full",
			RetainedCount: protocol.MaxRetainedPerRepo - 1,
		},
		protocol.RepositoryRegistration{Key: "open", RemoteIdentity: "github.com/example/open"},
	)
	repositories := map[string]string{}
	for _, repository := range worker.Repositories {
		repositories[repository.Key] = repository.ID
	}
	first := createTestTask(t, store, "terminal-reservation", workerA, repositories["nearly-full"])
	fixed = fixed.Add(time.Millisecond)
	blocked := createTestTask(t, store, "blocked-during-handoff", workerA, repositories["nearly-full"])
	fixed = fixed.Add(time.Millisecond)
	other := createTestTask(t, store, "other-during-handoff", workerA, repositories["open"])

	claim := claimTestTask(t, store, workerA, "terminal-reservation", tokenA)
	if claim.Task.ID != first.Task.ID {
		t.Fatalf("first claim = %s; want %s", claim.Task.ID, first.Task.ID)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "retained",
	}); err != nil {
		t.Fatal(err)
	}
	next := claimTestTask(t, store, workerA, "other-repository", tokenB)
	if next.Task.ID != other.Task.ID {
		t.Fatalf("terminal handoff admitted %s; want other repository %s", next.Task.ID, other.Task.ID)
	}
	blockedDetail, err := store.Task(context.Background(), blocked.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blockedDetail.Execution.State != "queued" || len(blockedDetail.Attempts) != 0 {
		t.Fatalf("terminal handoff failed to reserve capacity: %#v", blockedDetail)
	}

	fixed = fixed.Add(time.Millisecond)
	registerTestWorker(t, store, workerA, 2,
		protocol.RepositoryRegistration{
			Key: "nearly-full", RemoteIdentity: "github.com/example/nearly-full",
			RetainedCount: protocol.MaxRetainedPerRepo,
		},
		protocol.RepositoryRegistration{Key: "open", RemoteIdentity: "github.com/example/open"},
	)
}

func TestRegistrationAcknowledgesSameMillisecondTerminalHandoff(t *testing.T) {
	store := newTestStore(t)
	fixed := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return fixed }
	worker := registerTestWorker(t, store, workerA, 2, protocol.RepositoryRegistration{
		Key: "nearly-full", RemoteIdentity: "github.com/example/same-millisecond",
		RetainedCount: protocol.MaxRetainedPerRepo - 1,
	})
	first := createTestTask(t, store, "same-millisecond-first", workerA, worker.Repositories[0].ID)
	fixed = fixed.Add(time.Millisecond)
	second := createTestTask(t, store, "same-millisecond-second", workerA, worker.Repositories[0].ID)
	fixed = fixed.Add(time.Millisecond)
	claim := claimTestTask(t, store, workerA, "same-millisecond-first", tokenA)
	if claim.Task.ID != first.Task.ID {
		t.Fatalf("first claim = %s; want %s", claim.Task.ID, first.Task.ID)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "no worktree was created",
	}); err != nil {
		t.Fatal(err)
	}
	blocked, err := store.Claim(context.Background(), workerA, protocol.ClaimRequest{
		RequestID: "same-millisecond-blocked", LeaseToken: tokenB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked != nil {
		t.Fatalf("terminal attempt was not reserved before registration: %#v", blocked)
	}

	registerTestWorker(t, store, workerA, 2, protocol.RepositoryRegistration{
		Key: "nearly-full", RemoteIdentity: "github.com/example/same-millisecond",
		RetainedCount: protocol.MaxRetainedPerRepo - 1,
	})
	next := claimTestTask(t, store, workerA, "same-millisecond-after-registration", tokenB)
	if next.Task.ID != second.Task.ID {
		t.Fatalf("same-millisecond registration left %s blocked; claimed %s", second.Task.ID, next.Task.ID)
	}
}

func TestActiveRegistrationDoesNotAcknowledgeSweptAttempt(t *testing.T) {
	store := newTestStore(t)
	fixed := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return fixed }
	repository := protocol.RepositoryRegistration{
		Key: "nearly-full", RemoteIdentity: "github.com/example/swept-handoff",
		RetainedCount: protocol.MaxRetainedPerRepo - 1,
	}
	worker := registerTestWorker(t, store, workerA, 2, repository)
	first := createTestTask(t, store, "swept-handoff-first", workerA, worker.Repositories[0].ID)
	fixed = fixed.Add(time.Millisecond)
	second := createTestTask(t, store, "swept-handoff-second", workerA, worker.Repositories[0].ID)
	claim := claimTestTask(t, store, workerA, "swept-handoff-first", tokenA)
	if claim.Task.ID != first.Task.ID {
		t.Fatalf("first claim = %s; want %s", claim.Task.ID, first.Task.ID)
	}
	fixed = fixed.Add(protocol.LeaseDuration + time.Millisecond)
	expired, err := store.SweepExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].AttemptID != claim.Attempt.ID {
		t.Fatalf("swept attempts = %#v", expired)
	}
	if _, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: "worker-a", WorkerVersion: "test", RuntimeVersion: "test",
		Capacity: 2, ActiveCount: 1, Health: "healthy", Repositories: []protocol.RepositoryRegistration{repository},
	}); err != nil {
		t.Fatal(err)
	}
	blocked, err := store.Claim(context.Background(), workerA, protocol.ClaimRequest{
		RequestID: "swept-handoff-blocked", LeaseToken: tokenB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked != nil {
		t.Fatalf("active registration acknowledged an unclassified swept attempt: %#v", blocked)
	}

	if _, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: "worker-a", WorkerVersion: "test", RuntimeVersion: "test",
		Capacity: 2, ActiveCount: 0, Health: "healthy", Repositories: []protocol.RepositoryRegistration{repository},
	}); err != nil {
		t.Fatal(err)
	}
	next := claimTestTask(t, store, workerA, "swept-handoff-after-idle", tokenB)
	if next.Task.ID != second.Task.ID {
		t.Fatalf("idle registration left swept handoff blocked; claimed %s, want %s", next.Task.ID, second.Task.ID)
	}
}

func TestLegacyRegistrationDerivesRetainedCountBeforeAcknowledgingHandoff(t *testing.T) {
	store := newTestStore(t)
	fixed := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return fixed }
	worker := registerTestWorker(t, store, workerA, 2, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/example/legacy-worker",
	})
	first := createTestTask(t, store, "legacy-worker-first", workerA, worker.Repositories[0].ID)
	fixed = fixed.Add(time.Millisecond)
	second := createTestTask(t, store, "legacy-worker-second", workerA, worker.Repositories[0].ID)
	claim := claimTestTask(t, store, workerA, "legacy-worker-first", tokenA)
	if claim.Task.ID != first.Task.ID {
		t.Fatalf("first claim = %s; want %s", claim.Task.ID, first.Task.ID)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "legacy retained worktree",
	}); err != nil {
		t.Fatal(err)
	}
	retained := make([]protocol.RetainedWorktree, protocol.MaxRetainedPerRepo)
	for index := range retained {
		retained[index] = protocol.RetainedWorktree{
			AttemptID:    fmt.Sprintf("legacy-attempt-%d", index),
			RepositoryID: worker.Repositories[0].ID,
			Path:         fmt.Sprintf("/tmp/legacy-%d", index),
			Reason:       "legacy retained worktree",
		}
	}
	registered, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: "legacy-worker", WorkerVersion: "legacy", RuntimeVersion: "test",
		Capacity: 2, ActiveCount: 0, Health: "healthy",
		Repositories: []protocol.RepositoryRegistration{{
			Key: "factory", RemoteIdentity: "github.com/example/legacy-worker",
			RetainedCount: 0,
		}},
		RetainedWorktrees: retained,
	})
	if err != nil {
		t.Fatal(err)
	}
	if registered.Repositories[0].RetainedCount != protocol.MaxRetainedPerRepo {
		t.Fatalf("derived retained count = %d", registered.Repositories[0].RetainedCount)
	}
	blocked, err := store.Claim(context.Background(), workerA, protocol.ClaimRequest{
		RequestID: "legacy-worker-blocked", LeaseToken: tokenB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked != nil {
		t.Fatalf("legacy registration bypassed retained capacity: %#v", blocked)
	}
	detail, err := store.Task(context.Background(), second.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Execution.State != "queued" || len(detail.Attempts) != 0 {
		t.Fatalf("legacy retained capacity did not keep task queued: %#v", detail)
	}
}

func TestActiveLegacyRegistrationCannotAcknowledgeUnlistedHandoff(t *testing.T) {
	store := newTestStore(t)
	fixed := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return fixed }
	worker := registerTestWorker(t, store, workerA, 2, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/example/legacy-overlap",
	})
	retained := make([]protocol.RetainedWorktree, protocol.MaxRetainedPerRepo-1)
	for index := range retained {
		retained[index] = protocol.RetainedWorktree{
			AttemptID:    fmt.Sprintf("legacy-existing-%d", index),
			RepositoryID: worker.Repositories[0].ID,
			Path:         fmt.Sprintf("/tmp/legacy-existing-%d", index),
			Reason:       "legacy retained worktree",
		}
	}
	legacyRegistration := protocol.WorkerRegistration{
		Name: "legacy-worker", WorkerVersion: "legacy", RuntimeVersion: "test",
		Capacity: 2, ActiveCount: 0, Health: "healthy",
		Repositories: []protocol.RepositoryRegistration{{
			Key: "factory", RemoteIdentity: "github.com/example/legacy-overlap",
		}},
		RetainedWorktrees: retained,
	}
	if _, err := store.RegisterWorker(context.Background(), workerA, legacyRegistration); err != nil {
		t.Fatal(err)
	}
	first := createTestTask(t, store, "legacy-overlap-first", workerA, worker.Repositories[0].ID)
	fixed = fixed.Add(time.Millisecond)
	second := createTestTask(t, store, "legacy-overlap-second", workerA, worker.Repositories[0].ID)
	claim := claimTestTask(t, store, workerA, "legacy-overlap-first", tokenA)
	if claim.Task.ID != first.Task.ID {
		t.Fatalf("first claim = %s; want %s", claim.Task.ID, first.Task.ID)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "legacy worktree not yet summarized",
	}); err != nil {
		t.Fatal(err)
	}
	legacyRegistration.ActiveCount = 1
	if _, err := store.RegisterWorker(context.Background(), workerA, legacyRegistration); err != nil {
		t.Fatal(err)
	}
	blocked, err := store.Claim(context.Background(), workerA, protocol.ClaimRequest{
		RequestID: "legacy-overlap-blocked", LeaseToken: tokenB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked != nil {
		t.Fatalf("active legacy snapshot acknowledged an unlisted handoff: %#v", blocked)
	}

	legacyRegistration.RetainedWorktrees = append(legacyRegistration.RetainedWorktrees, protocol.RetainedWorktree{
		AttemptID: claim.Attempt.ID, RepositoryID: worker.Repositories[0].ID,
		Path: "/tmp/legacy-new", Reason: "legacy retained worktree",
	})
	registered, err := store.RegisterWorker(context.Background(), workerA, legacyRegistration)
	if err != nil {
		t.Fatal(err)
	}
	if registered.Repositories[0].RetainedCount != protocol.MaxRetainedPerRepo {
		t.Fatalf("legacy retained count after handoff = %d", registered.Repositories[0].RetainedCount)
	}
	blocked, err = store.Claim(context.Background(), workerA, protocol.ClaimRequest{
		RequestID: "legacy-overlap-still-full", LeaseToken: tokenB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked != nil {
		t.Fatalf("legacy retained repository exceeded capacity: %#v", blocked)
	}
	detail, err := store.Task(context.Background(), second.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Execution.State != "queued" || len(detail.Attempts) != 0 {
		t.Fatalf("legacy overlap task did not remain queued: %#v", detail)
	}
}

func TestActiveRegistrationAcknowledgesExplicitDisposedAttempt(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 2, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/example/explicit-disposal",
	})
	first := createTestTask(t, store, "explicit-disposal-first", workerA, worker.Repositories[0].ID)
	claim := claimTestTask(t, store, workerA, "explicit-disposal-first", tokenA)
	if claim.Task.ID != first.Task.ID {
		t.Fatalf("first claim = %s; want %s", claim.Task.ID, first.Task.ID)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "disposed without a worktree",
	}); err != nil {
		t.Fatal(err)
	}
	second := createTestTask(t, store, "explicit-disposal-second", workerA, worker.Repositories[0].ID)

	if _, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: "modern-worker", WorkerVersion: "test", RuntimeVersion: "test",
		Capacity: 2, ActiveCount: 1, Health: "healthy",
		Repositories: []protocol.RepositoryRegistration{{
			Key: "factory", RemoteIdentity: "github.com/example/explicit-disposal",
			RetainedCount: protocol.MaxRetainedPerRepo - 1,
		}},
		DisposedAttemptIDs: []string{claim.Attempt.ID},
	}); err != nil {
		t.Fatal(err)
	}
	next := claimTestTask(t, store, workerA, "explicit-disposal-second", tokenB)
	if next.Task.ID != second.Task.ID {
		t.Fatalf("claim after disposal = %s; want %s", next.Task.ID, second.Task.ID)
	}
}

func TestVersionedRegistrationDoesNotBulkAcknowledgeUnlistedAttempt(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 2, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/example/versioned-handoff",
	})
	createTestTask(t, store, "versioned-handoff-first", workerA, worker.Repositories[0].ID)
	claim := claimTestTask(t, store, workerA, "versioned-handoff-first", tokenA)
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "disposition not published",
	}); err != nil {
		t.Fatal(err)
	}
	createTestTask(t, store, "versioned-handoff-second", workerA, worker.Repositories[0].ID)

	if _, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: "modern-worker", WorkerVersion: "test", RuntimeVersion: "test",
		Capacity: 2, ActiveCount: 0, Health: "healthy", CapacityHandoffVersion: 1,
		Repositories: []protocol.RepositoryRegistration{{
			Key: "factory", RemoteIdentity: "github.com/example/versioned-handoff",
			RetainedCount: protocol.MaxRetainedPerRepo - 1,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	blocked, err := store.Claim(context.Background(), workerA, protocol.ClaimRequest{
		RequestID: "versioned-handoff-blocked", LeaseToken: tokenB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked != nil {
		t.Fatalf("versioned snapshot bulk-acknowledged an unlisted attempt: %#v", blocked)
	}
}

func TestRegistrationCanPreAcknowledgeDisposedAttemptBeforeLeaseSweep(t *testing.T) {
	store := newTestStore(t)
	fixed := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return fixed }
	worker := registerTestWorker(t, store, workerA, 2, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/example/preacknowledged-disposal",
	})
	createTestTask(t, store, "preacknowledged-first", workerA, worker.Repositories[0].ID)
	claim := claimTestTask(t, store, workerA, "preacknowledged-first", tokenA)
	second := createTestTask(t, store, "preacknowledged-second", workerA, worker.Repositories[0].ID)

	registration := protocol.WorkerRegistration{
		Name: "modern-worker", WorkerVersion: "test", RuntimeVersion: "test",
		Capacity: 2, ActiveCount: 1, Health: "healthy", CapacityHandoffVersion: 1,
		Repositories: []protocol.RepositoryRegistration{{
			Key: "factory", RemoteIdentity: "github.com/example/preacknowledged-disposal",
			RetainedCount: protocol.MaxRetainedPerRepo - 1,
		}},
		DisposedAttemptIDs: []string{claim.Attempt.ID},
	}
	if _, err := store.RegisterWorker(context.Background(), workerA, registration); err != nil {
		t.Fatal(err)
	}
	blocked, err := store.Claim(context.Background(), workerA, protocol.ClaimRequest{
		RequestID: "preacknowledged-active-blocked", LeaseToken: tokenB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked != nil {
		t.Fatalf("disposed active attempt stopped reserving active capacity: %#v", blocked)
	}

	fixed = fixed.Add(protocol.LeaseDuration + time.Millisecond)
	blocked, err = store.Claim(context.Background(), workerA, protocol.ClaimRequest{
		RequestID: "preacknowledged-expired-blocked", LeaseToken: tokenB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked != nil {
		t.Fatalf("expired attempt stopped reserving capacity before sweep: %#v", blocked)
	}
	if _, err := store.SweepExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	registration.ActiveCount = 0
	registration.DisposedAttemptIDs = nil
	if _, err := store.RegisterWorker(context.Background(), workerA, registration); err != nil {
		t.Fatal(err)
	}
	next := claimTestTask(t, store, workerA, "preacknowledged-after-sweep", tokenB)
	if next.Task.ID != second.Task.ID {
		t.Fatalf("claim after sweep = %s; want %s", next.Task.ID, second.Task.ID)
	}
}

func TestRegistrationAcceptsMissingDisposedAttemptForIdempotentHistoryDeletion(t *testing.T) {
	store := newTestStore(t)
	registerTestWorker(t, store, workerA, 2, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/example/unproven-disposal",
	})

	if _, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: "modern-worker", WorkerVersion: "test", RuntimeVersion: "test",
		Capacity: 2, ActiveCount: 1, Health: "healthy",
		Repositories: []protocol.RepositoryRegistration{{
			Key: "factory", RemoteIdentity: "github.com/example/unproven-disposal",
		}},
		DisposedAttemptIDs: []string{"missing-attempt"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUnattributedRetainedSummaryPreservesRegistrationContractWithoutAcknowledgingHandoff(t *testing.T) {
	testCases := []struct {
		name         string
		repositoryID string
	}{
		{name: "display-only"},
		{name: "unadvertised-repository", repositoryID: "stale-repository-id"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := newTestStore(t)
			remote := "github.com/example/" + testCase.name
			worker := registerTestWorker(t, store, workerA, 2, protocol.RepositoryRegistration{
				Key: "factory", RemoteIdentity: remote,
			})
			first := createTestTask(t, store, testCase.name+"-first", workerA, worker.Repositories[0].ID)
			claim := claimTestTask(t, store, workerA, testCase.name+"-first", tokenA)
			if claim.Task.ID != first.Task.ID {
				t.Fatalf("first claim = %s; want %s", claim.Task.ID, first.Task.ID)
			}
			if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
				LeaseToken: tokenA, State: "failed", Error: "retained before repository attribution",
			}); err != nil {
				t.Fatal(err)
			}
			second := createTestTask(t, store, testCase.name+"-second", workerA, worker.Repositories[0].ID)

			registered, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
				Name: "legacy-display-client", WorkerVersion: "legacy", RuntimeVersion: "test",
				Capacity: 2, ActiveCount: 0, Health: "healthy",
				Repositories: []protocol.RepositoryRegistration{{
					Key: "factory", RemoteIdentity: remote,
					RetainedCount: protocol.MaxRetainedPerRepo - 1,
				}},
				RetainedWorktrees: []protocol.RetainedWorktree{{
					AttemptID:    claim.Attempt.ID,
					RepositoryID: testCase.repositoryID,
					Path:         "/tmp/" + testCase.name,
					Reason:       "repository ID is not attributable",
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(registered.RetainedWorktrees) != 1 {
				t.Fatalf("unattributed summaries = %d; want 1", len(registered.RetainedWorktrees))
			}
			if registered.Repositories[0].RetainedCount != protocol.MaxRetainedPerRepo-1 {
				t.Fatalf("unattributed summary changed retained count to %d", registered.Repositories[0].RetainedCount)
			}
			blocked, err := store.Claim(context.Background(), workerA, protocol.ClaimRequest{
				RequestID: testCase.name + "-blocked", LeaseToken: tokenB,
			})
			if err != nil {
				t.Fatal(err)
			}
			if blocked != nil {
				t.Fatalf("unattributed summary acknowledged the handoff: %#v", blocked)
			}
			detail, err := store.Task(context.Background(), second.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if detail.Execution.State != "queued" || len(detail.Attempts) != 0 {
				t.Fatalf("unattributed summary did not keep task queued: %#v", detail)
			}
		})
	}
}

func TestConcurrentSQLiteClaimsCreateOneOwner(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 2, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	createTestTask(t, store, "concurrent", workerA, worker.Repositories[0].ID)

	start := make(chan struct{})
	results := make(chan *protocol.Claim, 2)
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for i, token := range []string{tokenA, tokenB} {
		wait.Add(1)
		go func(index int, leaseToken string) {
			defer wait.Done()
			<-start
			claim, err := store.Claim(context.Background(), workerA, protocol.ClaimRequest{
				RequestID:  fmt.Sprintf("concurrent-%d", index),
				LeaseToken: leaseToken,
			})
			results <- claim
			errs <- err
		}(i, token)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	claimed := 0
	for result := range results {
		if result != nil {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("expected exactly one successful claim, got %d", claimed)
	}
	var attempts, active int
	if err := store.db.QueryRow(`SELECT COUNT(*), COUNT(*) FILTER (WHERE state IN ('preparing','running')) FROM attempts`).Scan(&attempts, &active); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || active != 1 {
		t.Fatalf("expected one active attempt, got attempts=%d active=%d", attempts, active)
	}
}

func TestAttemptLifecycleEventsCancellationRetryAndMonotonicity(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task := createTestTask(t, store, "lifecycle", workerA, worker.Repositories[0].ID)
	claim := claimTestTask(t, store, workerA, "lifecycle-claim", tokenA)

	started, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{
		LeaseToken: tokenA, ProcessIdentity: "pid-start-time", SupervisorPID: pointer(int64(101)), ProcessGroupID: pointer(int64(202)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.State != "running" || started.StartedAt == nil {
		t.Fatalf("attempt did not start: %#v", started)
	}
	payload := json.RawMessage(`{"message":"safe progress"}`)
	batch := protocol.EventBatchRequest{LeaseToken: tokenA, Events: []protocol.AttemptEvent{
		{Sequence: 0, Kind: "progress", Payload: payload},
		{Sequence: 1, Kind: "progress", Payload: json.RawMessage(`{"percent":50}`)},
		{Sequence: 2, Kind: "progress", Payload: json.RawMessage(`{"number":9007199254740992}`)},
	}}
	if err := store.AppendEvents(context.Background(), claim.Attempt.ID, batch); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvents(context.Background(), claim.Attempt.ID, batch); err != nil {
		t.Fatalf("event replay failed: %v", err)
	}
	if err := store.AppendEvents(context.Background(), claim.Attempt.ID, protocol.EventBatchRequest{
		LeaseToken: tokenA,
		Events: []protocol.AttemptEvent{{
			Sequence: 2, Kind: "progress", Payload: json.RawMessage(`{"number":9007199254740993}`),
		}},
	}); err == nil {
		t.Fatal("large-integer event conflict succeeded")
	} else {
		assertErrorCode(t, err, "event_conflict")
	}
	eventPage, err := store.Events(context.Background(), claim.Attempt.ID, 0, protocol.DefaultEventPageSize)
	if err != nil {
		t.Fatal(err)
	}
	events := eventPage.Events
	if len(events) != 2 || events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Fatalf("event polling returned %#v", events)
	}
	if err := store.AppendEvents(context.Background(), claim.Attempt.ID, protocol.EventBatchRequest{
		LeaseToken: tokenA,
		Events:     []protocol.AttemptEvent{{Sequence: 4, Kind: "progress", Payload: json.RawMessage(`{}`)}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvents(context.Background(), claim.Attempt.ID, protocol.EventBatchRequest{
		LeaseToken: tokenA,
		Events:     []protocol.AttemptEvent{{Sequence: 3, Kind: "late", Payload: json.RawMessage(`{}`)}},
	}); err == nil {
		t.Fatal("out-of-order event succeeded")
	} else {
		assertErrorCode(t, err, "event_out_of_order")
	}
	if _, err := store.CancelTask(context.Background(), task.Task.ID); err != nil {
		t.Fatal(err)
	}
	heartbeat, err := store.Heartbeat(context.Background(), claim.Attempt.ID, tokenA)
	if err != nil {
		t.Fatal(err)
	}
	if !heartbeat.CancellationRequested {
		t.Fatal("active cancellation was not returned by heartbeat")
	}
	completed, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "cancelled", Error: "cancelled by operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "cancelled" {
		t.Fatalf("completion state = %s", completed.State)
	}
	replayed, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "different late completion",
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.State != "cancelled" || replayed.Error != "cancelled by operator" {
		t.Fatalf("terminal state changed on replay: %#v", replayed)
	}
	retried, err := store.RetryExecution(context.Background(), task.Execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Execution.State != "queued" || len(retried.Attempts) != 1 {
		t.Fatalf("retry created an eager attempt: %#v", retried)
	}
	second := claimTestTask(t, store, workerA, "retry-claim", tokenB)
	if second.Attempt.AttemptNumber != 2 {
		t.Fatalf("retry attempt number = %d", second.Attempt.AttemptNumber)
	}
	if _, err := store.RetryExecution(context.Background(), task.Execution.ID); err == nil {
		t.Fatal("active execution was retried")
	} else {
		assertErrorCode(t, err, "retry_not_allowed")
	}
}

func TestEveryAcceptedTerminalTransitionAndFailedRetry(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	cases := []struct {
		name        string
		start       bool
		terminal    string
		expectStart bool
	}{
		{name: "preparing to failed", terminal: "failed"},
		{name: "preparing to cancelled", terminal: "cancelled"},
		{name: "running to succeeded", start: true, terminal: "succeeded", expectStart: true},
		{name: "running to failed", start: true, terminal: "failed", expectStart: true},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			task := createTestTask(t, store, fmt.Sprintf("terminal-%d", index), workerA, worker.Repositories[0].ID)
			claim := claimTestTask(t, store, workerA, fmt.Sprintf("terminal-claim-%d", index), tokenA)
			if test.start {
				if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
					t.Fatal(err)
				}
			}
			attempt, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
				LeaseToken: tokenA, State: test.terminal, Result: "bounded result", Error: "bounded error",
			})
			if err != nil {
				t.Fatal(err)
			}
			detail, err := store.Task(context.Background(), task.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if attempt.State != test.terminal || detail.Execution.State != test.terminal || attempt.CompletedAt == nil {
				t.Fatalf("terminal transition: attempt=%#v execution=%#v", attempt, detail.Execution)
			}
			if (attempt.StartedAt != nil) != test.expectStart {
				t.Fatalf("started_at mismatch: %#v", attempt.StartedAt)
			}
			if index == 0 {
				retried, err := store.RetryExecution(context.Background(), task.Execution.ID)
				if err != nil {
					t.Fatal(err)
				}
				if retried.Execution.State != "queued" || len(retried.Attempts) != 1 {
					t.Fatalf("failed retry created eager attempt: %#v", retried)
				}
				second := claimTestTask(t, store, workerA, "failed-retry-claim", tokenB)
				if second.Attempt.AttemptNumber != 2 {
					t.Fatalf("failed retry attempt number = %d", second.Attempt.AttemptNumber)
				}
				if _, err := store.CompleteAttempt(context.Background(), second.Attempt.ID, protocol.CompleteAttemptRequest{
					LeaseToken: tokenB, State: "cancelled",
				}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestDocumentedTaskEventAndResultLimits(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	for name, request := range map[string]protocol.CreateTaskRequest{
		"title": {
			RequestKey: "long-title", Title: strings.Repeat("界", 201), Description: "prompt",
			WorkerID: workerA, RepositoryID: worker.Repositories[0].ID,
		},
		"description": {
			RequestKey: "long-description", Title: "title", Description: strings.Repeat("x", protocol.MaxDescriptionBytes+1),
			WorkerID: workerA, RepositoryID: worker.Repositories[0].ID,
		},
		"timeout": {
			RequestKey: "long-timeout", Title: "title", Description: "prompt",
			WorkerID: workerA, RepositoryID: worker.Repositories[0].ID, TimeoutSeconds: int(protocol.MaxTimeout.Seconds()) + 1,
		},
		"overflow timeout": {
			RequestKey: "overflow-timeout", Title: "title", Description: "prompt",
			WorkerID: workerA, RepositoryID: worker.Repositories[0].ID, TimeoutSeconds: math.MaxInt,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := store.CreateTask(context.Background(), request); err == nil {
				t.Fatal("oversized task input succeeded")
			}
		})
	}
	task := createTestTask(t, store, "limits", workerA, worker.Repositories[0].ID)
	claim := claimTestTask(t, store, workerA, "limits-claim", tokenA)
	var storedDigest []byte
	if err := store.db.QueryRow(`SELECT lease_digest FROM attempts WHERE id = ?`, claim.Attempt.ID).Scan(&storedDigest); err != nil {
		t.Fatal(err)
	}
	if len(storedDigest) != 32 || string(storedDigest) == tokenA {
		t.Fatal("lease token was not stored as a SHA-256 digest")
	}
	tooMany := make([]protocol.AttemptEvent, protocol.MaxEventsPerBatch+1)
	for index := range tooMany {
		tooMany[index] = protocol.AttemptEvent{Sequence: int64(index), Kind: "progress", Payload: json.RawMessage(`{}`)}
	}
	if err := store.AppendEvents(context.Background(), claim.Attempt.ID, protocol.EventBatchRequest{
		LeaseToken: tokenA, Events: tooMany,
	}); err == nil {
		t.Fatal("oversized event count succeeded")
	}
	if err := store.AppendEvents(context.Background(), claim.Attempt.ID, protocol.EventBatchRequest{
		LeaseToken: tokenA,
		Events: []protocol.AttemptEvent{{
			Sequence: 0, Kind: "progress", Payload: json.RawMessage(`"` + strings.Repeat("x", protocol.MaxEventBytes) + `"`),
		}},
	}); err == nil {
		t.Fatal("oversized event succeeded")
	}
	if _, err := store.db.Exec(`
		INSERT INTO attempt_events(attempt_id, sequence, kind, payload, payload_bytes, server_time)
		VALUES (?, 0, 'progress', '{}', ?, ?)
	`, claim.Attempt.ID, protocol.MaxAttemptEventBytes, store.now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvents(context.Background(), claim.Attempt.ID, protocol.EventBatchRequest{
		LeaseToken: tokenA,
		Events:     []protocol.AttemptEvent{{Sequence: 1, Kind: "progress", Payload: json.RawMessage(`{}`)}},
	}); err == nil {
		t.Fatal("attempt event budget was exceeded")
	} else {
		assertErrorCode(t, err, "event_budget_exceeded")
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Result: strings.Repeat("x", protocol.MaxResultBytes+1),
	}); err == nil {
		t.Fatal("oversized result succeeded")
	} else {
		assertErrorCode(t, err, "result_too_large")
	}
	detail, err := store.Task(context.Background(), task.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Execution.State != "preparing" {
		t.Fatalf("rejected limits changed execution state to %s", detail.Execution.State)
	}
}

func TestEventsPagesAreBoundedAndAdvanceDeterministically(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	createTestTask(t, store, "paged-events", workerA, worker.Repositories[0].ID)
	claim := claimTestTask(t, store, workerA, "paged-events-claim", tokenA)
	events := []protocol.AttemptEvent{
		{Sequence: 0, Kind: "progress", Payload: json.RawMessage(`{"step":0}`)},
		{Sequence: 1, Kind: "progress", Payload: json.RawMessage(`{"step":1}`)},
		{Sequence: 2, Kind: "progress", Payload: json.RawMessage(`{"step":2}`)},
	}
	if err := store.AppendEvents(context.Background(), claim.Attempt.ID, protocol.EventBatchRequest{
		LeaseToken: tokenA,
		Events:     events,
	}); err != nil {
		t.Fatal(err)
	}

	first, err := store.Events(context.Background(), claim.Attempt.ID, -1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 2 || first.Events[0].Sequence != 0 || first.Events[1].Sequence != 1 ||
		first.NextAfter != 1 || !first.HasMore {
		t.Fatalf("first event page = %#v", first)
	}
	second, err := store.Events(context.Background(), claim.Attempt.ID, first.NextAfter, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 || second.Events[0].Sequence != 2 ||
		second.NextAfter != 2 || second.HasMore {
		t.Fatalf("second event page = %#v", second)
	}
	empty, err := store.Events(context.Background(), claim.Attempt.ID, second.NextAfter, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Events) != 0 || empty.NextAfter != 2 || empty.HasMore {
		t.Fatalf("empty event page = %#v", empty)
	}
}

func TestQueuedCancellationCreatesNoAttempt(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task := createTestTask(t, store, "cancel-queued", workerA, worker.Repositories[0].ID)
	cancelled, err := store.CancelTask(context.Background(), task.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Execution.State != "cancelled" || len(cancelled.Attempts) != 0 {
		t.Fatalf("queued cancellation result: %#v", cancelled)
	}
}

func TestExpiredLeaseIsFencedAndSwept(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task := createTestTask(t, store, "expiry", workerA, worker.Repositories[0].ID)
	claim := claimTestTask(t, store, workerA, "expiry-claim", tokenA)
	now = now.Add(protocol.LeaseDuration + time.Millisecond)
	if _, err := store.Heartbeat(context.Background(), claim.Attempt.ID, tokenA); err == nil {
		t.Fatal("expired heartbeat succeeded")
	} else {
		assertErrorCode(t, err, "lease_not_owner")
	}
	if _, err := store.Claim(context.Background(), workerA, protocol.ClaimRequest{
		RequestID: "expiry-claim", LeaseToken: tokenA,
	}); err == nil {
		t.Fatal("expired claim replay succeeded")
	} else {
		assertErrorCode(t, err, "lease_not_owner")
	}
	expired, err := store.SweepExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].AttemptID != claim.Attempt.ID {
		t.Fatalf("swept %#v, want attempt %s", expired, claim.Attempt.ID)
	}
	detail, err := store.Task(context.Background(), task.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Execution.State != "failed" || detail.Attempts[0].State != "lost" {
		t.Fatalf("expired state not recorded: %#v", detail)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded",
	}); err == nil {
		t.Fatal("late completion after lease loss succeeded")
	} else {
		assertErrorCode(t, err, "lease_not_owner")
	}
	after, _ := store.Attempt(context.Background(), claim.Attempt.ID)
	if after.State != "lost" {
		t.Fatalf("lost attempt changed to %s", after.State)
	}
}

func TestSweepPrunesOnlyExpiredEmptyClaimRecords(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	first, err := store.Claim(context.Background(), workerA, protocol.ClaimRequest{
		RequestID: "empty-old", LeaseToken: tokenA,
	})
	if err != nil || first != nil {
		t.Fatalf("first empty claim = %#v, %v", first, err)
	}
	now = now.Add(protocol.EmptyClaimTTL)
	replay, err := store.Claim(context.Background(), workerA, protocol.ClaimRequest{
		RequestID: "empty-old", LeaseToken: tokenA,
	})
	if err != nil || replay != nil {
		t.Fatalf("five-minute empty replay = %#v, %v", replay, err)
	}
	second, err := store.Claim(context.Background(), workerA, protocol.ClaimRequest{
		RequestID: "empty-fresh", LeaseToken: tokenB,
	})
	if err != nil || second != nil {
		t.Fatalf("fresh empty claim = %#v, %v", second, err)
	}
	now = now.Add(time.Millisecond)
	if _, err := store.SweepExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	var oldCount, freshCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM claim_requests WHERE request_id = 'empty-old'`).Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM claim_requests WHERE request_id = 'empty-fresh'`).Scan(&freshCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 || freshCount != 1 {
		t.Fatalf("empty claim TTL pruning old=%d fresh=%d", oldCount, freshCount)
	}
}

func TestWorkerRepositoryIdentityCannotChangeForAKey(t *testing.T) {
	store := newTestStore(t)
	registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	_, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: "worker-a", WorkerVersion: "test", RuntimeVersion: "test", Capacity: 1, Health: "healthy",
		Repositories: []protocol.RepositoryRegistration{
			{Key: "factory", RemoteIdentity: "github.com/owainlewis/different"},
		},
	})
	if err == nil {
		t.Fatal("repository key was reassigned")
	}
	assertErrorCode(t, err, "repository_key_changed")
}

func TestWorkerCanRenameAKeyForTheSameRepository(t *testing.T) {
	store := newTestStore(t)
	original := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	renamed := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory-renamed", RemoteIdentity: "github.com/owainlewis/factory",
	})
	if len(renamed.Repositories) != 1 || renamed.Repositories[0].Key != "factory-renamed" {
		t.Fatalf("renamed repository: %#v", renamed.Repositories)
	}
	if renamed.Repositories[0].ID != original.Repositories[0].ID {
		t.Fatalf("repository identity changed from %s to %s", original.Repositories[0].ID, renamed.Repositories[0].ID)
	}
	var mappings int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM worker_repositories WHERE worker_id = ?`, workerA).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if mappings != 1 {
		t.Fatalf("repository rename left %d mappings", mappings)
	}
}

type testSQLiteError int

func (err testSQLiteError) Error() string { return fmt.Sprintf("sqlite code %d", err) }
func (err testSQLiteError) Code() int     { return int(err) }

func TestRetrySQLiteContention(t *testing.T) {
	calls := 0
	err := retrySQLiteContention(func() error {
		calls++
		if calls < 3 {
			return testSQLiteError(6)
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("locked retry: calls=%d err=%v", calls, err)
	}

	calls = 0
	err = retrySQLiteContention(func() error {
		calls++
		return testSQLiteError(1)
	})
	if err == nil || calls != 1 {
		t.Fatalf("non-contention retry: calls=%d err=%v", calls, err)
	}
}

func pointer[T any](value T) *T { return &value }
