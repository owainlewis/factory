package controlplane

import (
	"context"
	"database/sql"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
	"github.com/owainlewis/factory/migrations"
)

func createTestDefinition(t *testing.T, store *Store, requestKey, name string) protocol.Definition {
	t.Helper()
	definition, created, err := store.CreateDefinition(context.Background(), protocol.CreateDefinitionRequest{
		RequestKey: requestKey, Name: name, Prompt: "Inspect the repository and report bugs.",
		Runtime: protocol.RuntimeCodex, AllowedTools: []string{"git", "gh", "git"},
		TimeoutSeconds: 3600, Inputs: map[string]string{"severity": "high"},
	})
	if err != nil || !created {
		t.Fatalf("create Definition: created=%t err=%v", created, err)
	}
	return definition
}

func registerDefinitionWorker(
	t *testing.T,
	store *Store,
	workerID string,
	repository protocol.RepositoryRegistration,
	githubStatus string,
	sourceAccess []protocol.SourceAccess,
) protocol.Worker {
	t.Helper()
	worker, err := store.RegisterWorker(context.Background(), workerID, protocol.WorkerRegistration{
		Name: workerID, WorkerVersion: "test", Runtime: protocol.RuntimeCodex, RuntimeVersion: "codex-test",
		Capabilities: []protocol.Capability{
			{Kind: protocol.CapabilityKindTool, Name: "git", Status: protocol.CapabilityReady},
			{Kind: protocol.CapabilityKindTool, Name: "gh", Status: githubStatus},
			{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimeCodex, Status: protocol.CapabilityReady},
		},
		Capacity: 1, Health: "healthy", Repositories: []protocol.RepositoryRegistration{repository},
		SourceAccess: sourceAccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func TestDefinitionLifecycleIsSharedIdempotentAndConflictSafe(t *testing.T) {
	store := newTestStore(t)
	created := createTestDefinition(t, store, "definition-create", "  Find Bugs  ")
	if created.Name != "Find Bugs" || created.Generation != 1 || created.Archived ||
		len(created.AllowedTools) != 2 || created.AllowedTools[0] != "gh" || created.AllowedTools[1] != "git" {
		t.Fatalf("normalized Definition = %#v", created)
	}
	replayed, wasCreated, err := store.CreateDefinition(context.Background(), protocol.CreateDefinitionRequest{
		RequestKey: "definition-create", Name: "Find Bugs", Prompt: "Inspect the repository and report bugs.",
		Runtime: protocol.RuntimeCodex, AllowedTools: []string{"gh", "git"},
		TimeoutSeconds: 3600, Inputs: map[string]string{"severity": "high"},
	})
	if err != nil || wasCreated || replayed.ID != created.ID {
		t.Fatalf("create replay = created=%t err=%v Definition=%#v", wasCreated, err, replayed)
	}
	_, _, err = store.CreateDefinition(context.Background(), protocol.CreateDefinitionRequest{
		RequestKey: "definition-create", Name: "Different", Prompt: "Different prompt.",
		Runtime: protocol.RuntimeCodex, TimeoutSeconds: 60,
	})
	assertErrorCode(t, err, "request_key_conflict")
	_, _, err = store.CreateDefinition(context.Background(), protocol.CreateDefinitionRequest{
		RequestKey: "duplicate-name", Name: "find bugs", Prompt: "Another prompt.",
		Runtime: protocol.RuntimePi, TimeoutSeconds: 60,
	})
	assertErrorCode(t, err, "definition_name_conflict")

	snapshot, err := store.DefinitionSnapshot(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated, changed, err := store.UpdateDefinition(context.Background(), created.ID, protocol.UpdateDefinitionRequest{
		RequestKey: "definition-update", ExpectedGeneration: created.Generation,
		Name: "Find Important Bugs", Prompt: "Inspect the repository and open an issue for each confirmed bug.",
		Runtime: protocol.RuntimePi, AllowedTools: []string{"gh"}, TimeoutSeconds: 1800,
		Inputs: map[string]string{"severity": "critical", "branch": "main"},
	})
	if err != nil || !changed || updated.Generation != 2 || updated.Runtime != protocol.RuntimePi {
		t.Fatalf("update Definition = changed=%t err=%v Definition=%#v", changed, err, updated)
	}
	if snapshot.Name != "Find Bugs" || snapshot.Prompt != "Inspect the repository and report bugs." ||
		snapshot.Generation != 1 || snapshot.Inputs["severity"] != "high" {
		t.Fatalf("historical snapshot changed after edit: %#v", snapshot)
	}
	replayedUpdate, changed, err := store.UpdateDefinition(context.Background(), created.ID, protocol.UpdateDefinitionRequest{
		RequestKey: "definition-update", ExpectedGeneration: created.Generation,
		Name: "Find Important Bugs", Prompt: "Inspect the repository and open an issue for each confirmed bug.",
		Runtime: protocol.RuntimePi, AllowedTools: []string{"gh"}, TimeoutSeconds: 1800,
		Inputs: map[string]string{"severity": "critical", "branch": "main"},
	})
	if err != nil || changed || replayedUpdate.Generation != 2 {
		t.Fatalf("update replay = changed=%t err=%v Definition=%#v", changed, err, replayedUpdate)
	}
	_, _, err = store.UpdateDefinition(context.Background(), created.ID, protocol.UpdateDefinitionRequest{
		RequestKey: "stale-definition-update", ExpectedGeneration: 1,
		Name: "Stale", Prompt: "Overwrite a newer edit.", Runtime: protocol.RuntimeCodex, TimeoutSeconds: 60,
	})
	assertErrorCode(t, err, "definition_generation_conflict")

	archived, err := store.SetDefinitionArchived(context.Background(), created.ID, true, updated.Generation)
	if err != nil || !archived.Archived || archived.Generation != 3 {
		t.Fatalf("archive Definition: err=%v Definition=%#v", err, archived)
	}
	replayedArchive, err := store.SetDefinitionArchived(context.Background(), created.ID, true, updated.Generation)
	if err != nil || replayedArchive.Generation != archived.Generation || !replayedArchive.Archived {
		t.Fatalf("archive replay after lost response: err=%v Definition=%#v", err, replayedArchive)
	}
	_, _, err = store.UpdateDefinition(context.Background(), created.ID, protocol.UpdateDefinitionRequest{
		RequestKey: "archived-definition-update", ExpectedGeneration: archived.Generation,
		Name: "Archived edit", Prompt: "Do not edit archived content.", Runtime: protocol.RuntimeCodex, TimeoutSeconds: 60,
	})
	assertErrorCode(t, err, "definition_archived")
	active, err := store.Definitions(context.Background(), protocol.DefinitionPageRequest{Limit: 50})
	if err != nil || len(active.Definitions) != 0 {
		t.Fatalf("active Definitions after archive: err=%v page=%#v", err, active)
	}
	archive, err := store.Definitions(context.Background(), protocol.DefinitionPageRequest{Limit: 50, Archived: true})
	if err != nil || len(archive.Definitions) != 1 || archive.Definitions[0].ID != created.ID {
		t.Fatalf("archived Definitions: err=%v page=%#v", err, archive)
	}
}

func TestDefinitionValidationRejectsUnsafeAndOversizedFields(t *testing.T) {
	store := newTestStore(t)
	tests := map[string]struct {
		input protocol.CreateDefinitionRequest
		code  string
	}{
		"runtime": {
			input: protocol.CreateDefinitionRequest{RequestKey: "bad-runtime", Name: "Bad", Prompt: "Prompt", Runtime: "shell", TimeoutSeconds: 60},
			code:  "invalid_definition_runtime",
		},
		"tools": {
			input: protocol.CreateDefinitionRequest{RequestKey: "bad-tools", Name: "Bad", Prompt: "Prompt", Runtime: protocol.RuntimeCodex, AllowedTools: []string{"gh\nrm"}, TimeoutSeconds: 60},
			code:  "invalid_definition_tools",
		},
		"inputs": {
			input: protocol.CreateDefinitionRequest{RequestKey: "bad-inputs", Name: "Bad", Prompt: "Prompt", Runtime: protocol.RuntimeCodex, TimeoutSeconds: 60, Inputs: map[string]string{"bad name": "value"}},
			code:  "invalid_definition_inputs",
		},
		"unicode input name": {
			input: protocol.CreateDefinitionRequest{RequestKey: "unicode-input", Name: "Bad", Prompt: "Prompt", Runtime: protocol.RuntimeCodex, TimeoutSeconds: 60, Inputs: map[string]string{"séverity": "high"}},
			code:  "invalid_definition_inputs",
		},
		"multiline input value": {
			input: protocol.CreateDefinitionRequest{RequestKey: "multiline-input", Name: "Bad", Prompt: "Prompt", Runtime: protocol.RuntimeCodex, TimeoutSeconds: 60, Inputs: map[string]string{"severity": "high\ncritical"}},
			code:  "invalid_definition_inputs",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := store.CreateDefinition(context.Background(), test.input)
			assertErrorCode(t, err, test.code)
		})
	}
}

func TestTaskPersistsCompleteDefinitionSnapshot(t *testing.T) {
	store := newTestStore(t)
	repository := protocol.RepositoryRegistration{Key: "factory", RemoteIdentity: "github.com/owainlewis/factory"}
	worker := registerDefinitionWorker(t, store, workerA, repository, protocol.CapabilityReady, nil)
	definition := createTestDefinition(t, store, "task-definition", "Find Bugs")
	task, created, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "definition-task", Title: "Find Bugs", DefinitionID: definition.ID,
		WorkerID: workerA, RepositoryID: worker.Repositories[0].ID,
	})
	if err != nil || !created {
		t.Fatalf("create task from Definition: created=%t err=%v", created, err)
	}
	if task.Definition == nil || task.Definition.ID != definition.ID ||
		task.Definition.Generation != 1 || task.Definition.Prompt != definition.Prompt ||
		task.Definition.Runtime != definition.Runtime || task.Definition.TimeoutSeconds != definition.TimeoutSeconds ||
		task.Definition.AllowedTools[0] != "gh" || task.Definition.Inputs["severity"] != "high" ||
		task.ResolvedPrompt != definition.Prompt || task.Task.RequiredRuntime != definition.Runtime ||
		task.Task.TimeoutSeconds != definition.TimeoutSeconds {
		t.Fatalf("task Definition snapshot = %#v", task)
	}
	registerDefinitionWorker(t, store, workerA, repository, protocol.CapabilityUnauthenticated, nil)
	claim, err := store.Claim(context.Background(), workerA, protocol.ClaimRequest{
		RequestID: "missing-tool-claim", LeaseToken: tokenA,
	})
	if err != nil || claim != nil {
		t.Fatalf("claim without required gh = %#v, err=%v", claim, err)
	}
	_, _, err = store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "missing-tool-task", Title: "Blocked", DefinitionID: definition.ID,
		WorkerID: workerA, RepositoryID: worker.Repositories[0].ID,
	})
	assertErrorCode(t, err, "tools_unavailable")
	cancelled, err := store.CancelTask(context.Background(), task.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.RetryExecution(context.Background(), cancelled.Execution.ID)
	assertErrorCode(t, err, "retry_tools_unavailable")

	updated, _, err := store.UpdateDefinition(context.Background(), definition.ID, protocol.UpdateDefinitionRequest{
		RequestKey: "edit-task-definition", ExpectedGeneration: definition.Generation,
		Name: "Find Critical Bugs", Prompt: "Find only confirmed critical bugs.", Runtime: protocol.RuntimeCodex,
		AllowedTools: []string{"git"}, TimeoutSeconds: 600, Inputs: map[string]string{"severity": "critical"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetDefinitionArchived(context.Background(), definition.ID, true, updated.Generation); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Task(context.Background(), task.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Definition == nil || persisted.Definition.Name != "Find Bugs" ||
		persisted.Definition.Prompt != definition.Prompt || persisted.Definition.Generation != 1 ||
		persisted.Definition.Inputs["severity"] != "high" {
		t.Fatalf("Definition edit changed past task snapshot: %#v", persisted.Definition)
	}
	_, _, err = store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "archived-definition-task", Title: "Blocked", DefinitionID: definition.ID,
		WorkerID: workerA, RepositoryID: worker.Repositories[0].ID,
	})
	assertErrorCode(t, err, "definition_archived")
}

func TestDefinitionRouteSelectsRunnerWithRequiredTools(t *testing.T) {
	store := newTestStore(t)
	remote := "github.com/example/factory"
	createManagedTestRepository(t, store, remote)
	repository := protocol.RepositoryRegistration{Key: "factory", RemoteIdentity: remote}
	access := []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}}
	registerDefinitionWorker(t, store, workerA, repository, protocol.CapabilityUnauthenticated, access)
	ready := registerDefinitionWorker(t, store, workerB, repository, protocol.CapabilityReady, access)
	definition := createTestDefinition(t, store, "route-definition", "Triage Issues")
	task, created, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "definition-route", Title: definition.Name, DefinitionID: definition.ID,
		Route: &protocol.TaskRoute{
			RepositoryRemoteIdentity: remote,
			SourceAccess:             access[0],
		},
	})
	if err != nil || !created {
		t.Fatalf("route Definition task: created=%t err=%v", created, err)
	}
	if task.Execution.AssignedWorkerID != ready.ID {
		t.Fatalf("assigned Runner = %q; want %q", task.Execution.AssignedWorkerID, ready.ID)
	}
}

func TestDefinitionsMigrationUpgradesRunnerCapabilitiesV17(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/factory.sqlite3"
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: database}
	names := []string{
		"001_controlplane.sql", "002_attempt_capacity_handoff.sql", "003_task_list_pagination.sql",
		"004_worker_runtime.sql", "005_metrics_indexes.sql", "006_execution_retries.sql",
		"007_worker_source_access.sql", "008_managed_repositories.sql", "009_workflows.sql",
		"010_github_issue_automations.sql", "011_github_pull_request_automations.sql",
		"012_schedule_automations.sql", "013_legacy_poller_migration.sql",
		"014_workflow_automation_titles.sql", "015_codex_weekly_limit.sql",
		"016_worker_capacity.sql", "017_runner_capabilities.sql",
	}
	for index, name := range names {
		body, readErr := migrations.Files.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		version := index + 1
		if version == 13 || version == 16 || version == 17 {
			if err := legacy.applyForeignKeyRebuildMigration(ctx, name, version, body); err != nil {
				t.Fatalf("apply %s: %v", name, err)
			}
			continue
		}
		tx, beginErr := database.BeginTx(ctx, nil)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if _, err = tx.ExecContext(ctx, string(body)); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, 0)`, version)
		}
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply %s: %v", name, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := createDatabaseMarker(path + ".v2-control-plane"); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("upgrade v17 database: %v", err)
	}
	defer upgraded.Close()
	for _, check := range []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM schema_migrations WHERE version = 18`, 1},
		{`SELECT COUNT(*) FROM pragma_table_info('workers') WHERE name = 'capabilities_json'`, 1},
		{`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'definitions'`, 1},
		{`SELECT COUNT(*) FROM pragma_table_info('tasks') WHERE name = 'definition_snapshot'`, 1},
	} {
		var got int
		if err := upgraded.db.QueryRow(check.query).Scan(&got); err != nil || got != check.want {
			t.Fatalf("upgrade check %q = %d, err=%v; want %d", check.query, got, err, check.want)
		}
	}
}
