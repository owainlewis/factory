package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

var testIssue = protocol.GitHubIssueMatch{
	Number: 184,
	Title:  "Add control-plane GitHub issue Automations",
	URL:    "https://github.com/owainlewis/factory/issues/184",
	State:  "open",
	Labels: []string{"enhancement", "factory:ready"},
}

type fakeGitHubIssueLister struct {
	matches  []protocol.GitHubIssueMatch
	err      error
	started  chan struct{}
	canceled chan struct{}
}

func (fake fakeGitHubIssueLister) ListIssues(
	ctx context.Context,
	_ string,
	_ protocol.GitHubIssueTrigger,
) ([]protocol.GitHubIssueMatch, error) {
	if fake.started != nil {
		select {
		case fake.started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		if fake.canceled != nil {
			select {
			case fake.canceled <- struct{}{}:
			default:
			}
		}
		return nil, ctx.Err()
	}
	return append([]protocol.GitHubIssueMatch(nil), fake.matches...), fake.err
}

func createAutomationFixture(
	t *testing.T,
	withWorker bool,
) (*Store, protocol.AutomationDetail) {
	t.Helper()
	store := newTestStore(t)
	workflow := createTestWorkflow(t, store, "automation-workflow", "Implement issue", "Implement and verify the issue.")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/factory")
	if withWorker {
		_, err := store.RegisterWorker(context.Background(), "automation-worker", protocol.WorkerRegistration{
			Name: "automation-worker", WorkerVersion: "test", RuntimeVersion: "test",
			Capacity: 1, Health: "healthy", AcceptsManagedRepositories: true,
			ManagedRepositoryIDs: []string{repository.ID},
			SourceAccess:         []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	detail, created, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "automation-create", Name: "Ready issues",
		WorkflowID: workflow.Workflow.ID, RepositoryID: repository.ID,
		Context: "Open a reviewed pull request.", TimeoutSeconds: 3600,
		Trigger: protocol.GitHubIssueTrigger{
			Type: protocol.AutomationTriggerGitHubIssue, State: "open",
			RequiredLabels: []string{"factory:ready"}, PollIntervalSeconds: 10,
		},
	})
	if err != nil || !created {
		t.Fatalf("create Automation = created %v, error %v", created, err)
	}
	return store, detail
}

func enableAutomation(t *testing.T, store *Store, id string) protocol.AutomationDetail {
	t.Helper()
	detail, err := store.SetAutomationEnabled(context.Background(), id, true, true)
	if err != nil {
		t.Fatal(err)
	}
	return detail
}

func reserveAutomation(t *testing.T, store *Store) automationEvaluation {
	t.Helper()
	evaluation, found, err := store.reserveDueAutomation(context.Background())
	if err != nil || !found {
		t.Fatalf("reserve due Automation = found %v, error %v", found, err)
	}
	return evaluation
}

func TestAutomationStoreLifecycleIsTypedDisabledFirstAndOptimistic(t *testing.T) {
	store, detail := createAutomationFixture(t, false)
	if detail.Automation.Enabled || detail.Automation.Trigger.Type != "github_issue" || detail.Automation.Version != 1 {
		t.Fatalf("created Automation = %#v", detail.Automation)
	}
	replayed, created, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "automation-create", Name: "Ready issues",
		WorkflowID: detail.Automation.WorkflowID, RepositoryID: detail.Automation.RepositoryID,
		Context: "Open a reviewed pull request.", TimeoutSeconds: 3600,
		Trigger: protocol.GitHubIssueTrigger{
			Type: "github_issue", State: "open", RequiredLabels: []string{"factory:ready"}, PollIntervalSeconds: 10,
		},
	})
	if err != nil || created || replayed.Automation.ID != detail.Automation.ID {
		t.Fatalf("create replay = created %v, error %v, detail %#v", created, err, replayed)
	}
	updated, err := store.UpdateAutomation(context.Background(), detail.Automation.ID, protocol.UpdateAutomationRequest{
		ExpectedVersion: 1, Name: "Ready implementation issues",
		WorkflowID: detail.Automation.WorkflowID, Context: "Use live state.", TimeoutSeconds: 7200,
		Trigger: protocol.GitHubIssueTrigger{
			Type: "github_issue", State: "open", RequiredLabels: []string{"bug", "factory:ready"}, PollIntervalSeconds: 30,
		},
	})
	if err != nil || updated.Automation.Version != 2 || updated.Automation.RepositoryID != detail.Automation.RepositoryID {
		t.Fatalf("updated Automation = error %v, detail %#v", err, updated)
	}
	_, err = store.UpdateAutomation(context.Background(), detail.Automation.ID, protocol.UpdateAutomationRequest{
		ExpectedVersion: 1, Name: "stale", WorkflowID: detail.Automation.WorkflowID,
		TimeoutSeconds: 60, Trigger: protocol.GitHubIssueTrigger{Type: "github_issue", State: "open", PollIntervalSeconds: 10},
	})
	assertErrorCode(t, err, "automation_version_conflict")
	_, err = store.SetAutomationEnabled(context.Background(), detail.Automation.ID, true, false)
	assertErrorCode(t, err, "legacy_poller_confirmation_required")
	enabled := enableAutomation(t, store, detail.Automation.ID)
	if !enabled.Automation.Enabled || enabled.Automation.NextCheckAt == nil {
		t.Fatalf("enabled Automation = %#v", enabled.Automation)
	}
	_, err = store.UpdateAutomation(context.Background(), detail.Automation.ID, protocol.UpdateAutomationRequest{
		ExpectedVersion: 2, Name: "cannot edit", WorkflowID: detail.Automation.WorkflowID,
		TimeoutSeconds: 60, Trigger: protocol.GitHubIssueTrigger{Type: "github_issue", State: "open", PollIntervalSeconds: 10},
	})
	assertErrorCode(t, err, "automation_enabled")
}

func TestAutomationEvaluationPersistsBeforeAtomicIdempotentDispatch(t *testing.T) {
	store, detail := createAutomationFixture(t, true)
	enableAutomation(t, store, detail.Automation.ID)
	evaluation := reserveAutomation(t, store)
	if err := store.completeAutomationSuccess(context.Background(), evaluation, []protocol.GitHubIssueMatch{testIssue}); err != nil {
		t.Fatal(err)
	}
	occurrences, err := store.AutomationOccurrences(context.Background(), detail.Automation.ID, 50)
	if err != nil || len(occurrences) != 1 || occurrences[0].State != "pending" || occurrences[0].Task != nil {
		t.Fatalf("persisted occurrence before dispatch = error %v, %#v", err, occurrences)
	}
	if err := store.dispatchPendingOccurrences(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if err := store.dispatchPendingOccurrences(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	first := detail.Automation.ID
	if _, err := store.RequestAutomationCheck(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	secondEvaluation := reserveAutomation(t, store)
	if err := store.completeAutomationSuccess(context.Background(), secondEvaluation, []protocol.GitHubIssueMatch{testIssue}); err != nil {
		t.Fatal(err)
	}
	if err := store.dispatchPendingOccurrences(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	occurrences, err = store.AutomationOccurrences(context.Background(), detail.Automation.ID, 50)
	if err != nil || len(occurrences) != 1 || occurrences[0].Task == nil || occurrences[0].State != "dispatched" {
		t.Fatalf("idempotent occurrence = error %v, %#v", err, occurrences)
	}
	var taskCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE request_key = ?`, occurrences[0].TaskRequestKey).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 1 {
		t.Fatalf("task count = %d, want 1", taskCount)
	}
	current, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Automation.MatchedCount != 2 || current.Automation.SkippedCount != 1 || current.Automation.DispatchedCount != 1 {
		t.Fatalf("Automation counters = %#v", current.Automation)
	}
	if !stringsContain(current.Occurrences[0].TaskRequestKey, "automation:"+detail.Automation.ID+":github_issue:184") {
		t.Fatalf("request key = %q", current.Occurrences[0].TaskRequestKey)
	}
	if !stringsContain(current.Occurrences[0].Task.Title, "GitHub issue #184") {
		t.Fatalf("task title = %q", current.Occurrences[0].Task.Title)
	}
	task, err := store.Task(context.Background(), current.Occurrences[0].Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"Trusted trigger conditions:", "Untrusted trigger observation:", "Use gh to fetch the live GitHub item", `"required_labels":["factory:ready"]`} {
		if !stringsContain(task.ResolvedPrompt, required) {
			t.Fatalf("resolved prompt missing %q:\n%s", required, task.ResolvedPrompt)
		}
	}
}

func TestAutomationRestartRecoversReservedCheckAndPendingOccurrence(t *testing.T) {
	store, detail := createAutomationFixture(t, true)
	enableAutomation(t, store, detail.Automation.ID)
	first := reserveAutomation(t, store)
	if first.Token == "" {
		t.Fatal("missing evaluation token")
	}
	if err := store.recoverAutomationRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := reserveAutomation(t, store)
	if second.Token == first.Token {
		t.Fatal("restart reused the stale evaluation token")
	}
	if err := store.completeAutomationSuccess(context.Background(), second, []protocol.GitHubIssueMatch{testIssue}); err != nil {
		t.Fatal(err)
	}
	if err := store.recoverAutomationRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.dispatchPendingOccurrences(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	occurrences, err := store.AutomationOccurrences(context.Background(), detail.Automation.ID, 50)
	if err != nil || len(occurrences) != 1 || occurrences[0].Task == nil {
		t.Fatalf("restart recovery = error %v, occurrences %#v", err, occurrences)
	}
}

func TestAutomationTaskDeletionLeavesOccurrenceTombstoneAndDeduplication(t *testing.T) {
	store, detail := createAutomationFixture(t, true)
	enableAutomation(t, store, detail.Automation.ID)
	evaluation := reserveAutomation(t, store)
	if err := store.completeAutomationSuccess(context.Background(), evaluation, []protocol.GitHubIssueMatch{testIssue}); err != nil {
		t.Fatal(err)
	}
	if err := store.dispatchPendingOccurrences(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	occurrences, err := store.AutomationOccurrences(context.Background(), detail.Automation.ID, 50)
	if err != nil || len(occurrences) != 1 || occurrences[0].Task == nil {
		t.Fatalf("dispatched occurrence = error %v, %#v", err, occurrences)
	}
	taskID := occurrences[0].Task.ID
	if _, err := store.db.Exec(`UPDATE executions SET state = 'succeeded' WHERE task_id = ?`, taskID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTask(context.Background(), taskID); err != nil {
		t.Fatal(err)
	}
	occurrences, err = store.AutomationOccurrences(context.Background(), detail.Automation.ID, 50)
	if err != nil || len(occurrences) != 1 || occurrences[0].State != "task_deleted" ||
		occurrences[0].Task != nil || occurrences[0].TaskIDSnapshot != taskID {
		t.Fatalf("Occurrence tombstone = error %v, %#v", err, occurrences)
	}
	if _, err := store.RequestAutomationCheck(context.Background(), detail.Automation.ID); err != nil {
		t.Fatal(err)
	}
	second := reserveAutomation(t, store)
	if err := store.completeAutomationSuccess(context.Background(), second, []protocol.GitHubIssueMatch{testIssue}); err != nil {
		t.Fatal(err)
	}
	occurrences, err = store.AutomationOccurrences(context.Background(), detail.Automation.ID, 50)
	if err != nil || len(occurrences) != 1 {
		t.Fatalf("deleted Task rearmed issue = error %v, %#v", err, occurrences)
	}
}

func TestPublicTaskAPIReservesAutomationNamespaceAfterExactReplay(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	existing := createTestTask(t, store, "ordinary-key", worker.ID, worker.Repositories[0].ID)
	reservedKey := "automation:existing:github_issue:184"
	if _, err := store.db.Exec(`UPDATE tasks SET request_key = ? WHERE id = ?`, reservedKey, existing.Task.ID); err != nil {
		t.Fatal(err)
	}
	replayed, created, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: reservedKey, Title: "different replay body", Description: "valid body",
		WorkerID: worker.ID, RepositoryID: worker.Repositories[0].ID, TimeoutSeconds: 60,
	})
	if err != nil || created || replayed.Task.ID != existing.Task.ID {
		t.Fatalf("reserved exact replay = created %v, error %v, task %#v", created, err, replayed.Task)
	}
	_, _, err = store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "automation:new:github_issue:185", Title: "new reserved key", Description: "valid body",
		WorkerID: worker.ID, RepositoryID: worker.Repositories[0].ID, TimeoutSeconds: 60,
	})
	assertErrorCode(t, err, "reserved_request_key_prefix")
}

func TestAutomationDisableInvalidatesInFlightCheckAndPausesDispatch(t *testing.T) {
	store, detail := createAutomationFixture(t, true)
	enableAutomation(t, store, detail.Automation.ID)
	evaluation := reserveAutomation(t, store)
	if _, err := store.SetAutomationEnabled(context.Background(), detail.Automation.ID, false, false); err != nil {
		t.Fatal(err)
	}
	if err := store.completeAutomationSuccess(context.Background(), evaluation, []protocol.GitHubIssueMatch{testIssue}); err != nil {
		t.Fatal(err)
	}
	occurrences, err := store.AutomationOccurrences(context.Background(), detail.Automation.ID, 50)
	if err != nil || len(occurrences) != 0 {
		t.Fatalf("stale result admitted after disable = error %v, %#v", err, occurrences)
	}
}

func TestFailedAutomationDisableDoesNotCancelOrStrandActiveEvaluation(t *testing.T) {
	store, detail := createAutomationFixture(t, false)
	enableAutomation(t, store, detail.Automation.ID)
	started := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	service := newAutomationService(store, slog.Default(), fakeGitHubIssueLister{started: started, canceled: canceled})
	serviceContext, stopService := context.WithCancel(context.Background())
	serviceDone := make(chan struct{})
	go func() {
		service.Run(serviceContext)
		close(serviceDone)
	}()
	defer func() {
		stopService()
		<-serviceDone
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("GitHub check did not start")
	}
	if _, err := store.db.Exec(`
		CREATE TRIGGER reject_automation_disable
		BEFORE UPDATE OF enabled ON automations
		WHEN OLD.id = '` + detail.Automation.ID + `' AND NEW.enabled = 0
		BEGIN SELECT RAISE(FAIL, 'disable rejected'); END
	`); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandlerWithAutomation(store, slog.Default(), service))
	defer server.Close()
	disable := func() *http.Response {
		request, err := http.NewRequest(http.MethodPut, server.URL+"/api/v1/automations/"+detail.Automation.ID+"/enabled", bytes.NewReader([]byte(`{"enabled":false}`)))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := disable()
	if response.StatusCode != http.StatusServiceUnavailable {
		body := decodeResponse[protocol.ErrorBody](t, response)
		t.Fatalf("failed disable status = %d, body %#v", response.StatusCode, body)
	}
	response.Body.Close()
	select {
	case <-canceled:
		t.Fatal("failed disable canceled the active evaluator")
	case <-time.After(100 * time.Millisecond):
	}
	stillEnabled, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil || !stillEnabled.Automation.Enabled || stillEnabled.Automation.Health.Status != "checking" {
		t.Fatalf("Automation after failed disable = error %v, %#v", err, stillEnabled.Automation)
	}
	if _, err := store.db.Exec(`DROP TRIGGER reject_automation_disable`); err != nil {
		t.Fatal(err)
	}
	response = disable()
	requireStatus(t, response, http.StatusOK)
	response.Body.Close()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("successful disable did not cancel the active evaluator")
	}
}

func TestDisableCancellationCannotCancelReenabledEvaluation(t *testing.T) {
	store, detail := createAutomationFixture(t, false)
	enableAutomation(t, store, detail.Automation.ID)
	oldEvaluation := reserveAutomation(t, store)
	_, invalidatedToken, err := store.setAutomationEnabled(context.Background(), detail.Automation.ID, false, false)
	if err != nil || invalidatedToken != oldEvaluation.Token {
		t.Fatalf("disable invalidated token = %q, want %q, error %v", invalidatedToken, oldEvaluation.Token, err)
	}
	enableAutomation(t, store, detail.Automation.ID)
	newEvaluation := reserveAutomation(t, store)
	if newEvaluation.Token == oldEvaluation.Token {
		t.Fatal("re-enable reused the invalidated evaluation token")
	}
	newContext, cancelNew := context.WithCancel(context.Background())
	service := newAutomationService(store, slog.Default(), fakeGitHubIssueLister{})
	service.cancel[detail.Automation.ID] = automationCancellation{token: newEvaluation.Token, cancel: cancelNew}
	service.Cancel(detail.Automation.ID, invalidatedToken)
	select {
	case <-newContext.Done():
		t.Fatal("old disable token canceled the re-enabled evaluation")
	default:
	}
	service.Cancel(detail.Automation.ID, newEvaluation.Token)
	select {
	case <-newContext.Done():
	case <-time.After(time.Second):
		t.Fatal("matching evaluation token did not cancel")
	}
}

func TestAutomationPreviewIsBoundedAndHasNoDurableSideEffects(t *testing.T) {
	store, detail := createAutomationFixture(t, false)
	service := newAutomationService(store, slog.Default(), fakeGitHubIssueLister{matches: []protocol.GitHubIssueMatch{testIssue}})
	before := detail.Automation
	result, err := service.Test(context.Background(), detail.Automation.ID)
	if err != nil || len(result.Matches) != 1 {
		t.Fatalf("test trigger = error %v, result %#v", err, result)
	}
	after, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Occurrences) != 0 || after.Automation.MatchedCount != before.MatchedCount ||
		after.Automation.Health != before.Health || after.Automation.LastCheckedAt != nil {
		t.Fatalf("preview mutated durable state: before %#v after %#v", before, after.Automation)
	}
}

func TestAutomationServiceShutdownCancelsGitHubAndAdmitsNoOccurrence(t *testing.T) {
	store, detail := createAutomationFixture(t, false)
	enableAutomation(t, store, detail.Automation.ID)
	started := make(chan struct{}, 1)
	service := newAutomationService(store, slog.Default(), fakeGitHubIssueLister{started: started})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		service.Run(ctx)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("GitHub check did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Automation service did not stop")
	}
	occurrences, err := store.AutomationOccurrences(context.Background(), detail.Automation.ID, 50)
	if err != nil || len(occurrences) != 0 {
		t.Fatalf("shutdown admitted work = error %v, %#v", err, occurrences)
	}
}

func TestGitHubIssueRunnerReportsActionableDependencyTimeoutAndOutputFailures(t *testing.T) {
	trigger := protocol.GitHubIssueTrigger{Type: "github_issue", State: "open", RequiredLabels: []string{"factory:ready"}, PollIntervalSeconds: 10}
	tests := []struct {
		name   string
		runner githubIssueRunner
		code   string
	}{
		{
			name:   "missing",
			runner: githubIssueRunner{lookPath: func(string) (string, error) { return "", fs.ErrNotExist }},
			code:   "gh_not_found",
		},
		{
			name:   "unauthenticated",
			runner: githubIssueRunner{lookPath: fakeGHPath, run: fakeGHRun(nil, []byte("not logged into github.com"), false, false, errors.New("exit 1"))},
			code:   "gh_unauthenticated",
		},
		{
			name:   "timeout",
			runner: githubIssueRunner{lookPath: fakeGHPath, run: fakeGHRun(nil, nil, false, false, context.DeadlineExceeded)},
			code:   "gh_timed_out",
		},
		{
			name:   "malformed",
			runner: githubIssueRunner{lookPath: fakeGHPath, run: fakeGHRun([]byte("not-json"), nil, false, false, nil)},
			code:   "gh_malformed_output",
		},
		{
			name:   "oversized",
			runner: githubIssueRunner{lookPath: fakeGHPath, run: fakeGHRun(nil, nil, true, false, nil)},
			code:   "gh_output_too_large",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.runner.ListIssues(context.Background(), "github.com/owainlewis/factory", trigger)
			var checkErr *automationCheckError
			if !errors.As(err, &checkErr) || checkErr.code != test.code || checkErr.message == "" {
				t.Fatalf("error = %#v, want actionable %q", err, test.code)
			}
		})
	}
	values := make([]map[string]any, protocol.MaxAutomationMatches+1)
	for index := range values {
		number := index + 1
		values[index] = map[string]any{
			"number": number, "title": "Issue", "state": "OPEN",
			"url":    "https://github.com/owainlewis/factory/issues/" + strconvItoa(number),
			"labels": []map[string]string{{"id": "1", "name": "factory:ready", "description": "", "color": "ffffff"}},
		}
	}
	body, _ := json.Marshal(values)
	runner := githubIssueRunner{lookPath: fakeGHPath, run: fakeGHRun(body, nil, false, false, nil)}
	_, err := runner.ListIssues(context.Background(), "github.com/owainlewis/factory", trigger)
	var checkErr *automationCheckError
	if !errors.As(err, &checkErr) || checkErr.code != "gh_match_limit" {
		t.Fatalf("101 match error = %#v", err)
	}
}

func TestGitHubIssueRunnerUsesFixedBoundedArguments(t *testing.T) {
	trigger := protocol.GitHubIssueTrigger{
		Type: protocol.AutomationTriggerGitHubIssue, State: "open",
		RequiredLabels: []string{"factory:ready", "triage"}, PollIntervalSeconds: 10,
	}
	var executable string
	var arguments []string
	runner := githubIssueRunner{
		lookPath: fakeGHPath,
		run: func(_ context.Context, command string, values ...string) ([]byte, []byte, bool, bool, error) {
			executable = command
			arguments = append([]string(nil), values...)
			return []byte(`[{"number":184,"title":"Issue","url":"https://github.com/owainlewis/factory/issues/184","state":"OPEN","labels":[{"id":"1","name":"factory:ready","description":"","color":"fff"},{"id":"2","name":"triage","description":"","color":"fff"}]}]`), nil, false, false, nil
		},
	}
	if _, err := runner.ListIssues(context.Background(), "github.com/owainlewis/factory", trigger); err != nil {
		t.Fatal(err)
	}
	want := "issue list --repo owainlewis/factory --state open --limit 101 --json number,title,url,labels,state --label factory:ready --label triage"
	if executable != "gh" || strings.Join(arguments, " ") != want {
		t.Fatalf("command = %q %q, want gh %q", executable, strings.Join(arguments, " "), want)
	}
	if automationCommandTimeout != 30*time.Second || automationStdoutLimit != 4<<20 || automationStderrLimit != 64<<10 {
		t.Fatalf("command bounds = %s, %d, %d", automationCommandTimeout, automationStdoutLimit, automationStderrLimit)
	}
}

func TestRunAutomationCommandEnforcesTimeoutAndOutputLimits(t *testing.T) {
	started := time.Now()
	_, _, _, _, err := runAutomationCommandWithLimits(
		context.Background(), 20*time.Millisecond, 1024, 1024,
		os.Args[0], "-test.run=TestAutomationCommandHelperProcess", "--", "automation-command-timeout",
	)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("timeout = %v after %s", err, time.Since(started))
	}
	stdout, stderr, stdoutTooLarge, stderrTooLarge, err := runAutomationCommandWithLimits(
		context.Background(), 5*time.Second, 4, 3,
		os.Args[0], "-test.run=TestAutomationCommandHelperProcess", "--", "automation-command-output",
	)
	if err != nil || string(stdout) != "1234" || string(stderr) != "678" || !stdoutTooLarge || !stderrTooLarge {
		t.Fatalf("bounded command = stdout %q stderr %q flags %v/%v error %v", stdout, stderr, stdoutTooLarge, stderrTooLarge, err)
	}
}

func TestAutomationCommandHelperProcess(t *testing.T) {
	mode := ""
	for _, argument := range os.Args {
		if strings.HasPrefix(argument, "automation-command-") {
			mode = argument
		}
	}
	switch mode {
	case "":
		return
	case "automation-command-timeout":
		time.Sleep(5 * time.Second)
	case "automation-command-output":
		_, _ = os.Stdout.WriteString("12345")
		_, _ = os.Stderr.WriteString("67890")
	default:
		os.Exit(2)
	}
}

func TestAutomationAndOccurrencePagesUseStableCursors(t *testing.T) {
	store, first := createAutomationFixture(t, false)
	for index := 2; index <= 3; index++ {
		_, created, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
			RequestKey: "automation-page-" + strconv.Itoa(index), Name: "Ready issues " + strconv.Itoa(index),
			WorkflowID: first.Automation.WorkflowID, RepositoryID: first.Automation.RepositoryID,
			TimeoutSeconds: 60,
			Trigger: protocol.GitHubIssueTrigger{
				Type: protocol.AutomationTriggerGitHubIssue, State: "open",
				RequiredLabels: []string{"factory:ready"}, PollIntervalSeconds: 10,
			},
		})
		if err != nil || !created {
			t.Fatalf("create paged Automation %d = created %v, error %v", index, created, err)
		}
	}
	head, err := store.AutomationsPage(context.Background(), 2, nil)
	if err != nil || len(head.Automations) != 2 || head.NextCursor == nil {
		t.Fatalf("Automation head = %#v, error %v", head, err)
	}
	tail, err := store.AutomationsPage(context.Background(), 2, head.NextCursor)
	if err != nil || len(tail.Automations) != 1 || tail.NextCursor != nil {
		t.Fatalf("Automation tail = %#v, error %v", tail, err)
	}
	seen := map[string]bool{}
	for _, automation := range append(head.Automations, tail.Automations...) {
		seen[automation.ID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("paged Automation IDs = %#v", seen)
	}

	enableAutomation(t, store, first.Automation.ID)
	evaluation := reserveAutomation(t, store)
	matches := make([]protocol.GitHubIssueMatch, 3)
	for index := range matches {
		number := index + 1
		matches[index] = protocol.GitHubIssueMatch{
			Number: number, Title: "Issue " + strconv.Itoa(number), State: "open",
			URL:    "https://github.com/owainlewis/factory/issues/" + strconv.Itoa(number),
			Labels: []string{"factory:ready"},
		}
	}
	if err := store.completeAutomationSuccess(context.Background(), evaluation, matches); err != nil {
		t.Fatal(err)
	}
	occurrenceHead, err := store.AutomationOccurrencesPage(context.Background(), first.Automation.ID, 2, nil)
	if err != nil || len(occurrenceHead.Occurrences) != 2 || occurrenceHead.NextCursor == nil {
		t.Fatalf("Occurrence head = %#v, error %v", occurrenceHead, err)
	}
	occurrenceTail, err := store.AutomationOccurrencesPage(context.Background(), first.Automation.ID, 2, occurrenceHead.NextCursor)
	if err != nil || len(occurrenceTail.Occurrences) != 1 || occurrenceTail.NextCursor != nil {
		t.Fatalf("Occurrence tail = %#v, error %v", occurrenceTail, err)
	}

	server := httptest.NewServer(NewHandlerWithAutomation(store, slog.Default(), newAutomationService(store, slog.Default(), fakeGitHubIssueLister{})))
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/api/v1/automations?limit=2")
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, response, http.StatusOK)
	automationPage := decodeResponse[struct {
		Automations []protocol.Automation `json:"automations"`
		NextCursor  *string               `json:"next_cursor"`
	}](t, response)
	if len(automationPage.Automations) != 2 || automationPage.NextCursor == nil {
		t.Fatalf("HTTP Automation head = %#v", automationPage)
	}
	response, err = server.Client().Get(server.URL + "/api/v1/automations?limit=2&cursor=" + url.QueryEscape(*automationPage.NextCursor))
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, response, http.StatusOK)
	if page := decodeResponse[struct {
		Automations []protocol.Automation `json:"automations"`
	}](t, response); len(page.Automations) != 1 {
		t.Fatalf("HTTP Automation tail = %#v", page)
	}
	response, err = server.Client().Get(server.URL + "/api/v1/automations/" + first.Automation.ID + "/occurrences?limit=2")
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, response, http.StatusOK)
	occurrencePage := decodeResponse[struct {
		Occurrences []protocol.AutomationOccurrence `json:"occurrences"`
		NextCursor  *string                         `json:"next_cursor"`
	}](t, response)
	if len(occurrencePage.Occurrences) != 2 || occurrencePage.NextCursor == nil {
		t.Fatalf("HTTP Occurrence head = %#v", occurrencePage)
	}
	response, err = server.Client().Get(server.URL + "/api/v1/automations/" + first.Automation.ID + "/occurrences?limit=2&cursor=" + url.QueryEscape(*occurrencePage.NextCursor))
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, response, http.StatusOK)
	if page := decodeResponse[struct {
		Occurrences []protocol.AutomationOccurrence `json:"occurrences"`
	}](t, response); len(page.Occurrences) != 1 {
		t.Fatalf("HTTP Occurrence tail = %#v", page)
	}
}

func fakeGHPath(string) (string, error) { return "/test/gh", nil }

func fakeGHRun(
	stdout, stderr []byte,
	stdoutTooLarge, stderrTooLarge bool,
	err error,
) func(context.Context, string, ...string) ([]byte, []byte, bool, bool, error) {
	return func(context.Context, string, ...string) ([]byte, []byte, bool, bool, error) {
		return stdout, stderr, stdoutTooLarge, stderrTooLarge, err
	}
}

func stringsContain(value, fragment string) bool { return strings.Contains(value, fragment) }
func strconvItoa(value int) string               { return strconv.Itoa(value) }

func TestHTTPAutomationLifecycleAndPreview(t *testing.T) {
	store := newTestStore(t)
	workflow := createTestWorkflow(t, store, "http-automation-workflow", "Implement", "Implement safely.")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/factory")
	service := newAutomationService(store, slog.Default(), fakeGitHubIssueLister{matches: []protocol.GitHubIssueMatch{testIssue}})
	server := httptest.NewServer(NewHandlerWithAutomation(store, slog.Default(), service))
	defer server.Close()
	client := server.Client()
	postJSON := func(method, path string, body any) *http.Response {
		encoded, _ := json.Marshal(body)
		request, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(encoded))
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := postJSON(http.MethodPost, "/api/v1/automations", protocol.CreateAutomationRequest{
		RequestKey: "http-create", Name: "HTTP ready issues", WorkflowID: workflow.Workflow.ID,
		RepositoryID: repository.ID, Context: "Use live state.", TimeoutSeconds: 60,
		Trigger: protocol.GitHubIssueTrigger{Type: "github_issue", State: "open", RequiredLabels: []string{"factory:ready"}, PollIntervalSeconds: 10},
	})
	requireStatus(t, response, http.StatusCreated)
	created := decodeResponse[protocol.AutomationDetail](t, response)
	response = postJSON(http.MethodPost, "/api/v1/automations/"+created.Automation.ID+"/test", struct{}{})
	requireStatus(t, response, http.StatusOK)
	preview := decodeResponse[protocol.TestAutomationResult](t, response)
	if len(preview.Matches) != 1 {
		t.Fatalf("preview = %#v", preview)
	}
	response = postJSON(http.MethodPut, "/api/v1/automations/"+created.Automation.ID+"/enabled", map[string]any{"enabled": true})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("enable without poller confirmation status = %d", response.StatusCode)
	}
	response.Body.Close()
	response = postJSON(http.MethodPut, "/api/v1/automations/"+created.Automation.ID+"/enabled", map[string]any{"enabled": true, "confirm_legacy_poller_stopped": true})
	requireStatus(t, response, http.StatusOK)
	enabled := decodeResponse[protocol.AutomationDetail](t, response)
	if !enabled.Automation.Enabled {
		t.Fatal("Automation was not enabled")
	}
	response, err := client.Get(server.URL + "/api/v1/automations?limit=50")
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, response, http.StatusOK)
	page := decodeResponse[protocol.AutomationPage](t, response)
	if len(page.Automations) != 1 {
		t.Fatalf("Automation list = %#v", page)
	}
}
