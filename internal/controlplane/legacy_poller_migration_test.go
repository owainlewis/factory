package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
	_ "modernc.org/sqlite"
)

type legacyMigrationFixture struct {
	dataHome   string
	configPath string
	ledgerPath string
	queueID    string
}

func newLegacyMigrationFixture(
	t *testing.T,
	observations []legacyPollerObservation,
) legacyMigrationFixture {
	t.Helper()
	dataHome := t.TempDir()
	configPath := filepath.Join(dataHome, "poller.toml")
	config := `server = "http://127.0.0.1:7337"
poll_every = "30s"
data_directory = "poller"

[[queues]]
name = "github-ready"
source = "github"
project = "example/project"
status = "open"
labels = ["needs-agent"]
prompt = "Implement the issue and open a reviewed pull request."
timeout_seconds = 3600
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	dataDirectory := filepath.Join(dataHome, "poller")
	if err := os.MkdirAll(dataDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(dataDirectory, "poller.sqlite3")
	database, err := sql.Open("sqlite", ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		CREATE TABLE observations (
			queue_id TEXT NOT NULL,
			issue_key TEXT NOT NULL,
			request_key TEXT NOT NULL UNIQUE,
			request_json BLOB NOT NULL,
			task_id TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL CHECK (state IN ('pending', 'submitted')),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (queue_id, issue_key)
		);
		CREATE INDEX observations_pending
		ON observations(state, created_at, queue_id, issue_key);
	`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	for _, observation := range observations {
		if _, err := database.Exec(`
			INSERT INTO observations(
				queue_id, issue_key, request_key, request_json, task_id, state, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, observation.QueueID, observation.IssueKey, observation.RequestKey,
			observation.Request, observation.TaskID, observation.State,
			observation.CreatedAt, observation.UpdatedAt); err != nil {
			database.Close()
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return legacyMigrationFixture{
		dataHome: dataHome, configPath: configPath, ledgerPath: ledgerPath,
		queueID: legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"}),
	}
}

func legacyPendingRequest(t *testing.T, queueID string, issueNumber int) legacyPollerObservation {
	t.Helper()
	issueKey := "#" + stringInt(issueNumber)
	request := protocol.CreateTaskRequest{
		RequestKey:     "poll:" + queueID + "-" + stringInt(issueNumber),
		Title:          "Work on github ticket " + issueKey,
		Description:    "Legacy exact prompt for " + issueKey,
		TimeoutSeconds: 3600,
		Route: &protocol.TaskRoute{
			RepositoryRemoteIdentity: "github.com/example/project",
			SourceAccess:             protocol.SourceAccess{Provider: "github", Hostname: "github.com"},
		},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return legacyPollerObservation{
		QueueID: queueID, IssueKey: issueKey, RequestKey: request.RequestKey,
		Request: body, State: "pending", CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
}

func stringInt(value int) string {
	return strconv.Itoa(value)
}

func migrationSelection(fixture legacyMigrationFixture) protocol.LegacyPollerSelection {
	return protocol.LegacyPollerSelection{
		ConfigPath: fixture.configPath, DataHome: fixture.dataHome,
		WorkingDirectory: fixture.dataHome, ConfirmStopped: true,
	}
}

func TestLegacyPollerMigrationPendingResumeFinalizeAndDuplicatePrevention(t *testing.T) {
	queueID := legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"})
	fixture := newLegacyMigrationFixture(t, []legacyPollerObservation{legacyPendingRequest(t, queueID, 42)})
	store := newTestStore(t)
	repository := createManagedTestRepository(t, store, "github.com/example/project")
	_, err := store.RegisterWorker(context.Background(), "migration-worker", protocol.WorkerRegistration{
		Name: "migration-worker", WorkerVersion: "test", Runtime: protocol.RuntimeCodex,
		RuntimeVersion: "test", Capacity: 1, Health: "healthy",
		AcceptsManagedRepositories: true, ManagedRepositoryIDs: []string{repository.ID},
		SourceAccess: []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	selection := migrationSelection(fixture)
	preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Status != "previewed" || preview.Counts.Supported != 1 || preview.Counts.Pending != 1 ||
		len(preview.Queues) != 1 || preview.Queues[0].RepositoryID != repository.ID {
		t.Fatalf("preview = %#v", preview)
	}
	if preview.Automations == nil || preview.Occurrences == nil || preview.Errors == nil {
		t.Fatalf("Preview collection fields must encode as arrays: %#v", preview)
	}
	imported, err := store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
		Mappings: []protocol.LegacyPollerQueueMapping{{
			QueueID: queueID, WorkflowTitle: "Imported issue workflow", AutomationTitle: "Imported ready issues",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if imported.Status != "imported" || len(imported.Automations) != 1 || len(imported.Occurrences) != 1 ||
		imported.Automations[0].Enabled || imported.Occurrences[0].State != "pending" {
		t.Fatalf("imported migration = %#v", imported)
	}
	if _, err := store.SetAutomationEnabled(context.Background(), imported.Automations[0].ID, true, false); err == nil || !strings.Contains(err.Error(), "Finalize") {
		t.Fatalf("enable before Finalize error = %v", err)
	}
	resumed, err := store.ResumeLegacyPollerOccurrence(context.Background(), imported.Occurrences[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != "dispatched" || resumed.Task == nil || resumed.Diagnostic != "legacy_task_resumed" {
		t.Fatalf("resumed occurrence = %#v", resumed)
	}
	finalized, err := store.FinalizeLegacyPoller(context.Background(), protocol.FinalizeLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if finalized.Status != "finalized" || finalized.ArchivePath == "" {
		t.Fatalf("finalized migration = %#v", finalized)
	}
	for _, source := range []string{fixture.configPath, fixture.ledgerPath} {
		if _, err := os.Stat(source); err != nil {
			t.Fatalf("source was not preserved: %s: %v", source, err)
		}
	}
	for _, archived := range []string{"poller.toml", "poller.sqlite3", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(finalized.ArchivePath, archived)); err != nil {
			t.Fatalf("archive is incomplete: %s: %v", archived, err)
		}
	}
	if _, err := store.SetAutomationEnabled(context.Background(), imported.Automations[0].ID, true, false); err != nil {
		t.Fatal(err)
	}
	evaluation, found, err := store.reserveDueAutomation(context.Background())
	if err != nil || !found {
		t.Fatalf("reserve imported Automation = found %v, error %v", found, err)
	}
	if err := store.completeAutomationSuccess(context.Background(), evaluation, []protocol.GitHubIssueMatch{{
		Number: 42, Title: "Issue", URL: "https://github.com/example/project/issues/42",
		State: "open", Labels: []string{"needs-agent"},
	}}); err != nil {
		t.Fatal(err)
	}
	occurrences, err := store.AutomationOccurrences(context.Background(), imported.Automations[0].ID, 50)
	if err != nil || len(occurrences) != 1 {
		t.Fatalf("deduplicated occurrences = %#v, error %v", occurrences, err)
	}
}

func TestLegacyPollerPreviewRejectsLockAndImportRejectsSnapshotChange(t *testing.T) {
	queueID := legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"})
	fixture := newLegacyMigrationFixture(t, []legacyPollerObservation{legacyPendingRequest(t, queueID, 7)})
	store := newTestStore(t)
	createManagedTestRepository(t, store, "github.com/example/project")
	selection := migrationSelection(fixture)
	locker, err := sql.Open("sqlite", "file:"+fixture.ledgerPath+"?_pragma=busy_timeout%280%29&_txlock=exclusive")
	if err != nil {
		t.Fatal(err)
	}
	lockTx, err := locker.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockTx.Exec(`UPDATE observations SET updated_at = updated_at`); err != nil {
		t.Fatal(err)
	}
	_, err = store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
	if err == nil || !strings.Contains(err.Error(), "exclusive legacy ledger lock") {
		t.Fatalf("locked Preview error = %v", err)
	}
	_ = lockTx.Rollback()
	_ = locker.Close()
	preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(fixture.configPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("\n# changed after preview\n")
	_ = file.Close()
	_, err = store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
		Mappings: []protocol.LegacyPollerQueueMapping{{QueueID: queueID, WorkflowTitle: "Workflow", AutomationTitle: "Automation"}},
	})
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.Code != "migration_source_changed" {
		t.Fatalf("source-change Import error = %v", err)
	}
	var imports int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM legacy_poller_imports`).Scan(&imports); err != nil || imports != 0 {
		t.Fatalf("partial imports = %d, error %v", imports, err)
	}
}

func TestLegacyPollerResumeRecoversAfterTaskCommitAndRestart(t *testing.T) {
	queueID := legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"})
	observation := legacyPendingRequest(t, queueID, 19)
	fixture := newLegacyMigrationFixture(t, []legacyPollerObservation{observation})
	store := newTestStore(t)
	repository := createManagedTestRepository(t, store, "github.com/example/project")
	_, err := store.RegisterWorker(context.Background(), "restart-worker", protocol.WorkerRegistration{
		Name: "restart-worker", WorkerVersion: "test", Runtime: protocol.RuntimeCodex,
		RuntimeVersion: "test", Capacity: 1, Health: "healthy",
		AcceptsManagedRepositories: true, ManagedRepositoryIDs: []string{repository.ID},
		SourceAccess: []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	selection := migrationSelection(fixture)
	preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
		Mappings: []protocol.LegacyPollerQueueMapping{{QueueID: queueID, WorkflowTitle: "Restart workflow", AutomationTitle: "Restart issues"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var request protocol.CreateTaskRequest
	if err := json.Unmarshal(observation.Request, &request); err != nil {
		t.Fatal(err)
	}
	committed, _, err := store.CreateTask(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		UPDATE automation_occurrences
		SET state = 'dispatching', diagnostic = 'legacy_resume_in_progress'
		WHERE id = ?
	`, imported.Occurrences[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := store.recoverAutomationRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.ResumeLegacyPollerOccurrence(context.Background(), imported.Occurrences[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != "dispatched" || resumed.Task == nil || resumed.Task.ID != committed.Task.ID {
		t.Fatalf("recovered Resume = %#v", resumed)
	}
	page, err := store.Tasks(context.Background(), protocol.TaskPageRequest{Limit: 50})
	if err != nil || len(page.Tasks) != 1 {
		t.Fatalf("tasks after recovered Resume = %#v, error %v", page.Tasks, err)
	}
}

func TestLegacyPollerResumeResetsAfterCanceledLinkBeginAndRestart(t *testing.T) {
	queueID := legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"})
	observation := legacyPendingRequest(t, queueID, 20)
	fixture := newLegacyMigrationFixture(t, []legacyPollerObservation{observation})
	databasePath := filepath.Join(t.TempDir(), "controlplane.sqlite3")
	store, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repository := createManagedTestRepository(t, store, "github.com/example/project")
	_, err = store.RegisterWorker(context.Background(), "link-failure-worker", protocol.WorkerRegistration{
		Name: "link-failure-worker", WorkerVersion: "test", Runtime: protocol.RuntimeCodex,
		RuntimeVersion: "test", Capacity: 1, Health: "healthy",
		AcceptsManagedRepositories: true, ManagedRepositoryIDs: []string{repository.ID},
		SourceAccess: []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	selection := migrationSelection(fixture)
	preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
		Mappings: []protocol.LegacyPollerQueueMapping{{QueueID: queueID, WorkflowTitle: "Link failure workflow", AutomationTitle: "Link failure issues"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	resumeContext, cancelResume := context.WithCancel(context.Background())
	store.beginLegacyResumeLink = func(context.Context) (*sql.Tx, error) {
		cancelResume()
		return nil, context.Canceled
	}
	_, err = store.ResumeLegacyPollerOccurrence(resumeContext, imported.Occurrences[0].ID)
	if !errors.Is(err, context.Canceled) || resumeContext.Err() != context.Canceled {
		t.Fatalf("injected link failure = %v, context = %v", err, resumeContext.Err())
	}
	var state, diagnostic string
	var retainedRequest []byte
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT state, diagnostic, legacy_task_request_json
		FROM automation_occurrences
		WHERE id = ?
	`, imported.Occurrences[0].ID).Scan(&state, &diagnostic, &retainedRequest); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || !strings.Contains(diagnostic, "context canceled") || len(retainedRequest) == 0 {
		t.Fatalf("occurrence after link failure: state=%q diagnostic=%q request=%q", state, diagnostic, retainedRequest)
	}
	page, err := store.Tasks(context.Background(), protocol.TaskPageRequest{Limit: 50})
	if err != nil || len(page.Tasks) != 1 || page.Tasks[0].RequestKey != observation.RequestKey {
		t.Fatalf("created Task after link failure = %#v, error %v", page.Tasks, err)
	}
	createdTaskID := page.Tasks[0].ID
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	resumed, err := reopened.ResumeLegacyPollerOccurrence(context.Background(), imported.Occurrences[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != "dispatched" || resumed.Task == nil || resumed.Task.ID != createdTaskID {
		t.Fatalf("Resume after restart = %#v", resumed)
	}
	page, err = reopened.Tasks(context.Background(), protocol.TaskPageRequest{Limit: 50})
	if err != nil || len(page.Tasks) != 1 {
		t.Fatalf("Tasks after recovered Resume = %#v, error %v", page.Tasks, err)
	}
}

func TestLegacyPollerImportPreservesSubmittedDeletedAndBlankTaskIDIdentity(t *testing.T) {
	store := newTestStore(t)
	repository := createManagedTestRepository(t, store, "github.com/example/project")
	_, err := store.RegisterWorker(context.Background(), "submitted-worker", protocol.WorkerRegistration{
		Name: "submitted-worker", WorkerVersion: "test", Runtime: protocol.RuntimeCodex,
		RuntimeVersion: "test", Capacity: 2, Health: "healthy",
		AcceptsManagedRepositories: true, ManagedRepositoryIDs: []string{repository.ID},
		SourceAccess: []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	queueID := legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"})
	createTask := func(issue int, requestKey string) protocol.TaskDetail {
		t.Helper()
		detail, _, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
			RequestKey: requestKey, Title: "Work on github ticket #" + strconv.Itoa(issue),
			Description: "Submitted prompt " + strconv.Itoa(issue), TimeoutSeconds: 3600,
			Route: &protocol.TaskRoute{
				RepositoryRemoteIdentity: "github.com/example/project",
				SourceAccess:             protocol.SourceAccess{Provider: "github", Hostname: "github.com"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return detail
	}
	present := createTask(10, "poll:submitted-present")
	reusedDespiteWrongID := createTask(11, "poll:submitted-wrong-id")
	blankID := createTask(12, "poll:submitted-blank-id")
	now := time.Now().UnixMilli()
	fixture := newLegacyMigrationFixture(t, []legacyPollerObservation{
		{QueueID: queueID, IssueKey: "#10", RequestKey: present.Task.RequestKey, TaskID: present.Task.ID, State: "submitted", Request: []byte{}, CreatedAt: now, UpdatedAt: now},
		{QueueID: queueID, IssueKey: "#11", RequestKey: reusedDespiteWrongID.Task.RequestKey, TaskID: "missing-task-id", State: "submitted", Request: []byte{}, CreatedAt: now + 1, UpdatedAt: now + 1},
		{QueueID: queueID, IssueKey: "#12", RequestKey: blankID.Task.RequestKey, TaskID: "", State: "submitted", Request: []byte{}, CreatedAt: now + 2, UpdatedAt: now + 2},
	})
	selection := migrationSelection(fixture)
	preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
		Mappings: []protocol.LegacyPollerQueueMapping{{QueueID: queueID, WorkflowTitle: "Submitted workflow", AutomationTitle: "Submitted issues"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(imported.Occurrences) != 3 {
		t.Fatalf("occurrences = %#v", imported.Occurrences)
	}
	byNumber := make(map[int]protocol.AutomationOccurrence)
	for _, occurrence := range imported.Occurrences {
		byNumber[occurrence.IssueNumber] = occurrence
	}
	if byNumber[10].Task == nil || byNumber[10].Task.ID != present.Task.ID || byNumber[10].Diagnostic != "legacy_task_reused" {
		t.Fatalf("present submitted occurrence = %#v", byNumber[10])
	}
	if byNumber[11].State != "task_deleted" || byNumber[11].Task != nil || byNumber[11].TaskIDSnapshot != "missing-task-id" {
		t.Fatalf("missing stored-ID occurrence = %#v", byNumber[11])
	}
	if byNumber[12].Task == nil || byNumber[12].Task.ID != blankID.Task.ID {
		t.Fatalf("blank-ID fallback occurrence = %#v", byNumber[12])
	}
	page, err := store.Tasks(context.Background(), protocol.TaskPageRequest{Limit: 50})
	if err != nil || len(page.Tasks) != 3 {
		t.Fatalf("historical tasks changed: %#v, error %v", page.Tasks, err)
	}
}

func TestLegacyPollerSkipArchiveFailureRetryAndFinalizeSnapshotGuard(t *testing.T) {
	queueID := legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"})
	fixture := newLegacyMigrationFixture(t, []legacyPollerObservation{legacyPendingRequest(t, queueID, 21)})
	store := newTestStore(t)
	createManagedTestRepository(t, store, "github.com/example/project")
	selection := migrationSelection(fixture)
	preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
		Mappings: []protocol.LegacyPollerQueueMapping{{QueueID: queueID, WorkflowTitle: "Skip workflow", AutomationTitle: "Skip issues"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	skipped, err := store.SkipLegacyPollerOccurrence(context.Background(), imported.Occurrences[0].ID)
	if err != nil || skipped.State != "skipped" || skipped.Diagnostic != "legacy_pending_skipped" {
		t.Fatalf("Skip = %#v, error %v", skipped, err)
	}
	originalRename := legacyArchiveRename
	legacyArchiveRename = func(_, _ string) error { return errors.New("injected rename failure") }
	t.Cleanup(func() { legacyArchiveRename = originalRename })
	_, err = store.FinalizeLegacyPoller(context.Background(), protocol.FinalizeLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
	})
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.Code != "migration_archive_failed" {
		t.Fatalf("archive failure = %v", err)
	}
	status, err := store.LegacyPollerMigration(context.Background(), preview.ID)
	if err != nil || status.Status != "imported" {
		t.Fatalf("status after archive failure = %#v, error %v", status, err)
	}
	legacyArchiveRename = originalRename
	finalized, err := store.FinalizeLegacyPoller(context.Background(), protocol.FinalizeLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
	})
	if err != nil || finalized.Status != "finalized" {
		t.Fatalf("Finalize retry = %#v, error %v", finalized, err)
	}
	if _, err := store.db.Exec(`UPDATE legacy_poller_migrations SET status = 'imported', finalized_at = NULL WHERE id = ?`, preview.ID); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.FinalizeLegacyPoller(context.Background(), protocol.FinalizeLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
	})
	if err != nil || recovered.Status != "finalized" || recovered.ArchivePath != finalized.ArchivePath {
		t.Fatalf("lost-response Finalize recovery = %#v, error %v", recovered, err)
	}

	changedFixture := newLegacyMigrationFixture(t, []legacyPollerObservation{legacyPendingRequest(t, queueID, 22)})
	changedSelection := migrationSelection(changedFixture)
	changedPreview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: changedSelection})
	if err != nil {
		t.Fatal(err)
	}
	changedImport, err := store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
		LegacyPollerSelection: changedSelection, MigrationID: changedPreview.ID, SnapshotDigest: changedPreview.SnapshotDigest,
		Mappings: []protocol.LegacyPollerQueueMapping{{QueueID: queueID, WorkflowTitle: "Changed workflow", AutomationTitle: "Changed issues"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SkipLegacyPollerOccurrence(context.Background(), changedImport.Occurrences[0].ID); err != nil {
		t.Fatal(err)
	}
	ledger, err := sql.Open("sqlite", changedFixture.ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ledger.Exec(`UPDATE observations SET updated_at = updated_at + 1`)
	_ = ledger.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.FinalizeLegacyPoller(context.Background(), protocol.FinalizeLegacyPollerRequest{
		LegacyPollerSelection: changedSelection, MigrationID: changedPreview.ID, SnapshotDigest: changedPreview.SnapshotDigest,
	})
	if !errors.As(err, &serviceErr) || serviceErr.Code != "migration_source_changed" {
		t.Fatalf("Finalize source-change error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(changedFixture.dataHome, "archive", "poller", changedPreview.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive exists after source-change abort: %v", err)
	}
}

func TestLegacyPollerFinalizeRebuildsInvalidStagingFilesAfterRestart(t *testing.T) {
	queueID := legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"})
	fixture := newLegacyMigrationFixture(t, []legacyPollerObservation{legacyPendingRequest(t, queueID, 23)})
	databasePath := filepath.Join(t.TempDir(), "controlplane.sqlite3")
	store, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	createManagedTestRepository(t, store, "github.com/example/project")
	selection := migrationSelection(fixture)
	preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
		Mappings: []protocol.LegacyPollerQueueMapping{{QueueID: queueID, WorkflowTitle: "Staging workflow", AutomationTitle: "Staging issues"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SkipLegacyPollerOccurrence(context.Background(), imported.Occurrences[0].ID); err != nil {
		t.Fatal(err)
	}
	stagingPath := filepath.Join(fixture.dataHome, "archive", "poller", "."+preview.ID+".staging")
	if err := os.MkdirAll(stagingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"poller.toml", "poller.sqlite3", "manifest.json"} {
		if err := os.WriteFile(filepath.Join(stagingPath, name), []byte("incomplete write"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	finalized, err := reopened.FinalizeLegacyPoller(context.Background(), protocol.FinalizeLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
	})
	if err != nil || finalized.Status != "finalized" {
		t.Fatalf("Finalize after partial staging restart = %#v, error %v", finalized, err)
	}
	if _, err := os.Stat(stagingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging path remains after Finalize: %v", err)
	}
	configBody, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatal(err)
	}
	archivedConfig, err := os.ReadFile(filepath.Join(finalized.ArchivePath, "poller.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(archivedConfig) != string(configBody) {
		t.Fatal("Finalize did not rebuild the partial staged configuration")
	}
	sourceLedgerDigest, err := digestFile(fixture.ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	archivedLedgerDigest, err := digestFile(filepath.Join(finalized.ArchivePath, "poller.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	if archivedLedgerDigest != sourceLedgerDigest {
		t.Fatalf("archived ledger digest = %s, want %s", archivedLedgerDigest, sourceLedgerDigest)
	}
}

func TestLegacyPollerFinalizeResyncsArchiveParentAfterRestart(t *testing.T) {
	queueID := legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"})
	fixture := newLegacyMigrationFixture(t, []legacyPollerObservation{legacyPendingRequest(t, queueID, 24)})
	databasePath := filepath.Join(t.TempDir(), "controlplane.sqlite3")
	store, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	createManagedTestRepository(t, store, "github.com/example/project")
	selection := migrationSelection(fixture)
	preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
		Mappings: []protocol.LegacyPollerQueueMapping{{QueueID: queueID, WorkflowTitle: "Parent sync workflow", AutomationTitle: "Parent sync issues"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SkipLegacyPollerOccurrence(context.Background(), imported.Occurrences[0].ID); err != nil {
		t.Fatal(err)
	}

	archiveRoot := filepath.Join(fixture.dataHome, "archive", "poller")
	parentSyncs := 0
	originalSyncDirectory := legacyArchiveSyncDirectory
	legacyArchiveSyncDirectory = func(path string) error {
		if path == archiveRoot {
			parentSyncs++
			if parentSyncs == 1 {
				return errors.New("injected archive parent sync failure")
			}
		}
		return syncDirectory(path)
	}
	t.Cleanup(func() { legacyArchiveSyncDirectory = originalSyncDirectory })
	_, err = store.FinalizeLegacyPoller(context.Background(), protocol.FinalizeLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
	})
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.Code != "migration_archive_failed" {
		t.Fatalf("injected parent sync failure = %v", err)
	}
	finalPath := filepath.Join(archiveRoot, preview.ID)
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("published archive after parent sync failure: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	finalized, err := reopened.FinalizeLegacyPoller(context.Background(), protocol.FinalizeLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
	})
	if err != nil || finalized.Status != "finalized" || finalized.ArchivePath != finalPath {
		t.Fatalf("Finalize after parent sync restart = %#v, error %v", finalized, err)
	}
	if parentSyncs != 2 {
		t.Fatalf("archive parent syncs = %d, want 2", parentSyncs)
	}
}

func TestLegacyPollerMigrationReturnsAndResolvesMoreThanOnePage(t *testing.T) {
	queueID := legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"})
	observations := make([]legacyPollerObservation, protocol.MaxAutomationPageSize+1)
	for index := range observations {
		observations[index] = legacyPendingRequest(t, queueID, index+1)
		observations[index].CreatedAt = int64(index + 1)
		observations[index].UpdatedAt = int64(index + 1)
	}
	fixture := newLegacyMigrationFixture(t, observations)
	store := newTestStore(t)
	createManagedTestRepository(t, store, "github.com/example/project")
	selection := migrationSelection(fixture)
	preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
		Mappings: []protocol.LegacyPollerQueueMapping{{QueueID: queueID, WorkflowTitle: "Paged workflow", AutomationTitle: "Paged issues"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(imported.Occurrences) != len(observations) || imported.Counts.Pending != len(observations) {
		t.Fatalf("imported %d occurrences with counts %#v, want %d", len(imported.Occurrences), imported.Counts, len(observations))
	}
	oldest := imported.Occurrences[len(imported.Occurrences)-1]
	skipped, err := store.SkipLegacyPollerOccurrence(context.Background(), oldest.ID)
	if err != nil || skipped.ID != oldest.ID || skipped.State != "skipped" {
		t.Fatalf("direct lookup after Skip = %#v, error %v", skipped, err)
	}
	status, err := store.LegacyPollerMigration(context.Background(), imported.ID)
	if err != nil || len(status.Occurrences) != len(observations) || status.Counts.Pending != len(observations)-1 {
		t.Fatalf("paged migration status has %d occurrences and counts %#v, error %v", len(status.Occurrences), status.Counts, err)
	}
}

func TestLegacyPollerActiveMigrationSurvivesControlPlaneRestart(t *testing.T) {
	queueID := legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"})
	fixture := newLegacyMigrationFixture(t, []legacyPollerObservation{legacyPendingRequest(t, queueID, 31)})
	databasePath := filepath.Join(t.TempDir(), "controlplane.sqlite3")
	store, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	createManagedTestRepository(t, store, "github.com/example/project")
	selection := migrationSelection(fixture)
	preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
		Mappings: []protocol.LegacyPollerQueueMapping{{QueueID: queueID, WorkflowTitle: "Active workflow", AutomationTitle: "Active issues"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	active, err := reopened.ActiveLegacyPollerMigration(context.Background())
	if err != nil || active == nil || active.ID != imported.ID || len(active.Occurrences) != 1 {
		t.Fatalf("active migration after restart = %#v, error %v", active, err)
	}
	_, err = reopened.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.Code != "migration_in_progress" {
		t.Fatalf("second Preview error = %v", err)
	}
}

func TestLegacyPollerPreviewReportsInvalidPendingPayload(t *testing.T) {
	queueID := legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"})
	observation := legacyPendingRequest(t, queueID, 32)
	var request protocol.CreateTaskRequest
	if err := json.Unmarshal(observation.Request, &request); err != nil {
		t.Fatal(err)
	}
	request.Route.RepositoryRemoteIdentity = "github.com/example/different"
	observation.Request, _ = json.Marshal(request)
	fixture := newLegacyMigrationFixture(t, []legacyPollerObservation{observation})
	store := newTestStore(t)
	createManagedTestRepository(t, store, "github.com/example/project")
	preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: migrationSelection(fixture)})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Counts.Supported != 1 || preview.Counts.Unsupported != 0 || len(preview.Errors) == 0 ||
		!strings.Contains(strings.Join(preview.Errors, " "), "different repository") {
		t.Fatalf("invalid pending Preview = %#v", preview)
	}
	imported, err := store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
		LegacyPollerSelection: migrationSelection(fixture), MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
		Mappings: []protocol.LegacyPollerQueueMapping{{QueueID: queueID, WorkflowTitle: "Invalid pending workflow", AutomationTitle: "Invalid pending issues"}},
	})
	if err != nil || len(imported.Occurrences) != 1 || imported.Occurrences[0].State != "failed" {
		t.Fatalf("invalid pending Import = %#v, error %v", imported, err)
	}
	if _, err := store.ResumeLegacyPollerOccurrence(context.Background(), imported.Occurrences[0].ID); err == nil {
		t.Fatal("Resume accepted a mismatched pending route")
	}
	skipped, err := store.SkipLegacyPollerOccurrence(context.Background(), imported.Occurrences[0].ID)
	if err != nil || skipped.State != "skipped" {
		t.Fatalf("invalid pending Skip = %#v, error %v", skipped, err)
	}
}

func TestLegacyPollerImportEnforcesOccurrenceLimitAtomically(t *testing.T) {
	queueID := legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"})
	fixture := newLegacyMigrationFixture(t, []legacyPollerObservation{legacyPendingRequest(t, queueID, 33)})
	store := newTestStore(t)
	createManagedTestRepository(t, store, "github.com/example/project")
	selection := migrationSelection(fixture)
	preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
	if err != nil {
		t.Fatal(err)
	}
	originalLimit := maxLegacyImportOccurrences
	maxLegacyImportOccurrences = 0
	t.Cleanup(func() { maxLegacyImportOccurrences = originalLimit })
	_, err = store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
		Mappings: []protocol.LegacyPollerQueueMapping{{QueueID: queueID, WorkflowTitle: "Limited workflow", AutomationTitle: "Limited issues"}},
	})
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.Code != "occurrence_limit_reached" {
		t.Fatalf("Occurrence limit error = %v", err)
	}
	var imports, workflows int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM legacy_poller_imports`).Scan(&imports)
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM workflows`).Scan(&workflows)
	if imports != 0 || workflows != 0 {
		t.Fatalf("partial limit import: imports=%d workflows=%d", imports, workflows)
	}
}

func TestLegacyPollerCommandOnlyMigrationArchivesWithoutAutomations(t *testing.T) {
	commandQueueID := legacyQueueID(legacyPollerQueue{Name: "command-history", Source: "linear", Project: "ENG"})
	pending := legacyPendingRequest(t, commandQueueID, 82)
	submitted := legacyPendingRequest(t, commandQueueID, 83)
	submitted.State = "submitted"
	submitted.TaskID = "archived-command-task"
	fixture := newLegacyMigrationFixture(t, []legacyPollerObservation{pending, submitted})
	config := `poll_every = "30s"
data_directory = "poller"

[[queues]]
name = "command-history"
source = "linear"
command = ["linear", "issues"]
project = "ENG"
status = "open"
worker_id = "legacy-worker"
repository_key = "factory"
prompt = "Implement the ticket."
timeout_seconds = 3600
`
	if err := os.WriteFile(fixture.configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(t.TempDir(), "controlplane.sqlite3")
	store, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	selection := migrationSelection(fixture)
	preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Counts.Supported != 0 || preview.Counts.Unsupported != 1 ||
		preview.Counts.Pending != 1 || preview.Counts.Submitted != 1 {
		t.Fatalf("command-only Preview = %#v", preview)
	}
	imported, err := store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
		Mappings: nil,
	})
	if err != nil || imported.Status != "imported" || len(imported.Automations) != 0 ||
		imported.Counts.Queues != 1 || imported.Counts.Supported != 0 || imported.Counts.Unsupported != 1 ||
		imported.Counts.Pending != 1 || imported.Counts.Submitted != 1 {
		t.Fatalf("command-only Import = %#v, error %v", imported, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	active, err := reopened.ActiveLegacyPollerMigration(context.Background())
	if err != nil || active == nil || active.Counts.Queues != 1 || active.Counts.Supported != 0 || active.Counts.Unsupported != 1 ||
		active.Counts.Pending != 1 || active.Counts.Submitted != 1 {
		t.Fatalf("command-only active migration after restart = %#v, error %v", active, err)
	}
	finalized, err := reopened.FinalizeLegacyPoller(context.Background(), protocol.FinalizeLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
	})
	if err != nil || finalized.Status != "finalized" {
		t.Fatalf("command-only Finalize = %#v, error %v", finalized, err)
	}
}

func TestLegacyPollerBlocksImportWhenLedgerContainsRemovedQueue(t *testing.T) {
	configuredQueueID := legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"})
	removedQueueID := "removed-queue-ledger-id"
	fixture := newLegacyMigrationFixture(t, []legacyPollerObservation{legacyPendingRequest(t, removedQueueID, 81)})
	store := newTestStore(t)
	createManagedTestRepository(t, store, "github.com/example/project")
	selection := migrationSelection(fixture)
	preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Counts.Queues != 2 || preview.Counts.Supported != 1 || preview.Counts.Unsupported != 1 ||
		preview.Counts.Pending != 1 || len(preview.Queues) != 2 {
		t.Fatalf("removed-queue Preview = %#v", preview)
	}
	var removed *protocol.LegacyPollerQueue
	for index := range preview.Queues {
		if preview.Queues[index].QueueID == removedQueueID {
			removed = &preview.Queues[index]
		}
	}
	if removed == nil || removed.Source != "ledger-only" || removed.Supported || !removed.Blocking || removed.PendingObservations != 1 ||
		len(removed.Errors) == 0 || !strings.Contains(removed.Errors[0], "restore the matching queue") {
		t.Fatalf("removed ledger queue = %#v", removed)
	}
	_, err = store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
		LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
		Mappings: []protocol.LegacyPollerQueueMapping{{
			QueueID: configuredQueueID, WorkflowTitle: "Configured workflow", AutomationTitle: "Configured issues",
		}},
	})
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.Code != "legacy_ledger_queue_missing" {
		t.Fatalf("removed-queue Import error = %v", err)
	}
	for _, table := range []string{"legacy_poller_imports", "workflows", "automations", "automation_occurrences"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s changed after blocked Import: count=%d, error %v", table, count, err)
		}
	}
}

func TestLegacyPollerSnapshotBindsPragmasAndLedgerInode(t *testing.T) {
	queueID := legacyQueueID(legacyPollerQueue{Name: "github-ready", Source: "github", Project: "example/project"})
	for _, test := range []struct {
		name   string
		change func(*testing.T, legacyMigrationFixture)
	}{
		{name: "pragma", change: func(t *testing.T, fixture legacyMigrationFixture) {
			database, err := sql.Open("sqlite", fixture.ledgerPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = database.Exec(`PRAGMA user_version = 187`)
			_ = database.Close()
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "identical-byte replacement", change: func(t *testing.T, fixture legacyMigrationFixture) {
			body, err := os.ReadFile(fixture.ledgerPath)
			if err != nil {
				t.Fatal(err)
			}
			replacement := fixture.ledgerPath + ".replacement"
			if err := os.WriteFile(replacement, body, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, fixture.ledgerPath); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLegacyMigrationFixture(t, []legacyPollerObservation{legacyPendingRequest(t, queueID, 34)})
			store := newTestStore(t)
			createManagedTestRepository(t, store, "github.com/example/project")
			selection := migrationSelection(fixture)
			preview, err := store.PreviewLegacyPoller(context.Background(), protocol.PreviewLegacyPollerRequest{LegacyPollerSelection: selection})
			if err != nil {
				t.Fatal(err)
			}
			test.change(t, fixture)
			_, err = store.ImportLegacyPoller(context.Background(), protocol.ImportLegacyPollerRequest{
				LegacyPollerSelection: selection, MigrationID: preview.ID, SnapshotDigest: preview.SnapshotDigest,
				Mappings: []protocol.LegacyPollerQueueMapping{{QueueID: queueID, WorkflowTitle: "Snapshot workflow", AutomationTitle: "Snapshot issues"}},
			})
			var serviceErr *ServiceError
			if !errors.As(err, &serviceErr) || serviceErr.Code != "migration_source_changed" {
				t.Fatalf("snapshot change error = %v", err)
			}
		})
	}
}
