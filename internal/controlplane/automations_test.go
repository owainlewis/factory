package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

var testPullRequest = protocol.GitHubPullRequestMatch{
	Number:     185,
	Title:      "Add typed GitHub pull-request Automations",
	URL:        "https://github.com/owainlewis/factory/pull/185",
	State:      "open",
	IsDraft:    false,
	BaseBranch: "main",
	HeadCommit: strings.Repeat("a", 40),
	Labels:     []string{"enhancement", "factory:review"},
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

type blockingGitHubIssueLister struct {
	started chan<- struct{}
	release <-chan struct{}
}

type fakeGitHubAutomationLister struct {
	fakeGitHubIssueLister
	pullRequests []protocol.GitHubPullRequestMatch
	pullError    error
}

func (fake fakeGitHubAutomationLister) ListPullRequests(
	context.Context,
	string,
	protocol.GitHubPullRequestTrigger,
) ([]protocol.GitHubPullRequestMatch, error) {
	return append([]protocol.GitHubPullRequestMatch(nil), fake.pullRequests...), fake.pullError
}

func (fake blockingGitHubIssueLister) ListIssues(
	ctx context.Context,
	_ string,
	_ protocol.GitHubIssueTrigger,
) ([]protocol.GitHubIssueMatch, error) {
	select {
	case fake.started <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-fake.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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
		Trigger: protocol.AutomationTrigger{
			Type: protocol.AutomationTriggerGitHubIssue, State: "open",
			RequiredLabels: []string{"factory:ready"}, PollIntervalSeconds: 10,
		},
	})
	if err != nil || !created {
		t.Fatalf("create Automation = created %v, error %v", created, err)
	}
	return store, detail
}

func createPullRequestAutomationFixture(
	t *testing.T,
	withWorker bool,
) (*Store, protocol.AutomationDetail) {
	t.Helper()
	store := newTestStore(t)
	workflow := createTestWorkflow(t, store, "pull-request-automation-workflow", "Review pull request", "Review and verify the pull request.")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/factory")
	if withWorker {
		_, err := store.RegisterWorker(context.Background(), "pull-request-automation-worker", protocol.WorkerRegistration{
			Name: "pull-request-automation-worker", WorkerVersion: "test", RuntimeVersion: "test",
			Capacity: 1, Health: "healthy", AcceptsManagedRepositories: true,
			ManagedRepositoryIDs: []string{repository.ID},
			SourceAccess:         []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	detail, created, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "pull-request-automation-create", Name: "Review pull requests",
		WorkflowID: workflow.Workflow.ID, RepositoryID: repository.ID,
		Context: "Review the live pull request without merging it.", TimeoutSeconds: 3600,
		Trigger: protocol.AutomationTrigger{
			Type: protocol.AutomationTriggerGitHubPullRequest, State: "open",
			IncludeDrafts: false, RequiredLabels: []string{"factory:review"},
			BaseBranches: []string{"main"}, PollIntervalSeconds: 10,
		},
	})
	if err != nil || !created {
		t.Fatalf("create pull-request Automation = created %v, error %v", created, err)
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
		Trigger: protocol.AutomationTrigger{
			Type: "github_issue", State: "open", RequiredLabels: []string{"factory:ready"}, PollIntervalSeconds: 10,
		},
	})
	if err != nil || created || replayed.Automation.ID != detail.Automation.ID {
		t.Fatalf("create replay = created %v, error %v, detail %#v", created, err, replayed)
	}
	updated, err := store.UpdateAutomation(context.Background(), detail.Automation.ID, protocol.UpdateAutomationRequest{
		ExpectedVersion: 1, Name: "Ready implementation issues",
		WorkflowID: detail.Automation.WorkflowID, Context: "Use live state.", TimeoutSeconds: 7200,
		Trigger: protocol.AutomationTrigger{
			Type: "github_issue", State: "open", RequiredLabels: []string{"bug", "factory:ready"}, PollIntervalSeconds: 30,
		},
	})
	if err != nil || updated.Automation.Version != 2 || updated.Automation.RepositoryID != detail.Automation.RepositoryID {
		t.Fatalf("updated Automation = error %v, detail %#v", err, updated)
	}
	replayedUpdate, err := store.UpdateAutomation(context.Background(), detail.Automation.ID, protocol.UpdateAutomationRequest{
		ExpectedVersion: 1, Name: "Ready implementation issues",
		WorkflowID: detail.Automation.WorkflowID, Context: "Use live state.", TimeoutSeconds: 7200,
		Trigger: protocol.AutomationTrigger{
			Type: "github_issue", State: "open", RequiredLabels: []string{"bug", "factory:ready"}, PollIntervalSeconds: 30,
		},
	})
	if err != nil || replayedUpdate.Automation.Version != 2 {
		t.Fatalf("lost-response update replay = error %v, detail %#v", err, replayedUpdate)
	}
	_, err = store.UpdateAutomation(context.Background(), detail.Automation.ID, protocol.UpdateAutomationRequest{
		ExpectedVersion: 1, Name: "stale", WorkflowID: detail.Automation.WorkflowID,
		TimeoutSeconds: 60, Trigger: protocol.AutomationTrigger{Type: "github_issue", State: "open", PollIntervalSeconds: 10},
	})
	assertErrorCode(t, err, "automation_version_conflict")
	enabled, err := store.SetAutomationEnabled(context.Background(), detail.Automation.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Automation.Enabled || enabled.Automation.NextCheckAt == nil {
		t.Fatalf("enabled Automation = %#v", enabled.Automation)
	}
	_, err = store.UpdateAutomation(context.Background(), detail.Automation.ID, protocol.UpdateAutomationRequest{
		ExpectedVersion: 2, Name: "cannot edit", WorkflowID: detail.Automation.WorkflowID,
		TimeoutSeconds: 60, Trigger: protocol.AutomationTrigger{Type: "github_issue", State: "open", PollIntervalSeconds: 10},
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

func TestPullRequestAutomationPersistsTypedOccurrenceAndDeduplicatesAcrossRestart(t *testing.T) {
	store, detail := createPullRequestAutomationFixture(t, true)
	if detail.Automation.Trigger.Type != protocol.AutomationTriggerGitHubPullRequest ||
		detail.Automation.Trigger.IncludeDrafts || len(detail.Automation.Trigger.BaseBranches) != 1 {
		t.Fatalf("created pull-request Automation = %#v", detail.Automation)
	}
	enableAutomation(t, store, detail.Automation.ID)
	first := reserveAutomation(t, store)
	if err := store.completePullRequestAutomationSuccess(context.Background(), first, []protocol.GitHubPullRequestMatch{testPullRequest}); err != nil {
		t.Fatal(err)
	}
	if err := store.recoverAutomationRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.dispatchPendingOccurrences(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RequestAutomationCheck(context.Background(), detail.Automation.ID); err != nil {
		t.Fatal(err)
	}
	second := reserveAutomation(t, store)
	if err := store.completePullRequestAutomationSuccess(context.Background(), second, []protocol.GitHubPullRequestMatch{testPullRequest}); err != nil {
		t.Fatal(err)
	}
	if err := store.dispatchPendingOccurrences(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	current, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Occurrences) != 1 || current.Occurrences[0].Task == nil ||
		current.Occurrences[0].PullRequestNumber != testPullRequest.Number ||
		current.Automation.MatchedCount != 2 || current.Automation.SkippedCount != 1 ||
		current.Automation.DispatchedCount != 1 {
		t.Fatalf("deduplicated pull-request Automation = %#v", current)
	}
	var taskCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE request_key = ?`, current.Occurrences[0].TaskRequestKey).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 1 || !strings.Contains(current.Occurrences[0].TaskRequestKey, ":github_pull_request:185") {
		t.Fatalf("pull-request task identity = count %d occurrence %#v", taskCount, current.Occurrences[0])
	}
	task, err := store.Task(context.Background(), current.Occurrences[0].Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"Use authenticated gh CLI to fetch the live GitHub item",
		"Untrusted Automation context:", "Treat the Automation context",
		`"type":"github_pull_request"`, `"include_drafts":false`,
		`"base_branches":["main"]`, `"head_commit":"` + strings.Repeat("a", 40) + `"`,
	} {
		if !strings.Contains(task.ResolvedPrompt, required) {
			t.Fatalf("pull-request prompt missing %q:\n%s", required, task.ResolvedPrompt)
		}
	}
}

func TestPullRequestAutomationPreviewAndDisableHaveNoStaleDurableEffects(t *testing.T) {
	store, detail := createPullRequestAutomationFixture(t, false)
	service := newAutomationService(store, slog.Default(), fakeGitHubAutomationLister{pullRequests: []protocol.GitHubPullRequestMatch{testPullRequest}})
	preview, err := service.Test(context.Background(), detail.Automation.ID)
	if err != nil || len(preview.Matches) != 1 || preview.Matches[0].IsDraft == nil || *preview.Matches[0].IsDraft {
		t.Fatalf("pull-request preview = error %v result %#v", err, preview)
	}
	afterPreview, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil || len(afterPreview.Occurrences) != 0 || afterPreview.Automation.LastCheckedAt != nil {
		t.Fatalf("pull-request preview mutated durable state = error %v detail %#v", err, afterPreview)
	}
	enableAutomation(t, store, detail.Automation.ID)
	evaluation := reserveAutomation(t, store)
	if _, err := store.SetAutomationEnabled(context.Background(), detail.Automation.ID, false, false); err != nil {
		t.Fatal(err)
	}
	if err := store.completePullRequestAutomationSuccess(context.Background(), evaluation, []protocol.GitHubPullRequestMatch{testPullRequest}); err != nil {
		t.Fatal(err)
	}
	afterDisable, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil || len(afterDisable.Occurrences) != 0 || afterDisable.Automation.Enabled {
		t.Fatalf("stale pull-request result admitted after disable = error %v detail %#v", err, afterDisable)
	}
}

func TestRepeatedAutomationEnablePreservesInFlightEvaluation(t *testing.T) {
	store, detail := createAutomationFixture(t, false)
	enableAutomation(t, store, detail.Automation.ID)
	evaluation := reserveAutomation(t, store)

	before, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	replayed, invalidatedToken, err := store.setAutomationEnabled(
		context.Background(), detail.Automation.ID, true, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if invalidatedToken != "" || replayed.Automation.Health.Status != "checking" ||
		replayed.Automation.UpdatedAt != before.Automation.UpdatedAt {
		t.Fatalf("repeated enable changed Automation = token %q, before %#v, after %#v", invalidatedToken, before.Automation, replayed.Automation)
	}
	var token string
	if err := store.db.QueryRow(`SELECT evaluation_token FROM automations WHERE id = ?`, detail.Automation.ID).Scan(&token); err != nil {
		t.Fatal(err)
	}
	if token != evaluation.Token {
		t.Fatalf("evaluation token = %q, want %q", token, evaluation.Token)
	}
	if _, found, err := store.reserveDueAutomation(context.Background()); err != nil || found {
		t.Fatalf("duplicate reservation = found %v, error %v", found, err)
	}
}

func TestRepeatedDependencyEnablePreservesInFlightAutomationEvaluation(t *testing.T) {
	store, detail := createAutomationFixture(t, false)
	enableAutomation(t, store, detail.Automation.ID)
	evaluation := reserveAutomation(t, store)

	if _, err := store.SetWorkflowEnabled(context.Background(), detail.Automation.WorkflowID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetManagedRepositoryEnabled(context.Background(), detail.Automation.RepositoryID, true); err != nil {
		t.Fatal(err)
	}
	var token string
	if err := store.db.QueryRow(`SELECT evaluation_token FROM automations WHERE id = ?`, detail.Automation.ID).Scan(&token); err != nil {
		t.Fatal(err)
	}
	if token != evaluation.Token {
		t.Fatalf("evaluation token = %q, want %q", token, evaluation.Token)
	}
	if _, found, err := store.reserveDueAutomation(context.Background()); err != nil || found {
		t.Fatalf("duplicate reservation = found %v, error %v", found, err)
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

func TestAutomationEvaluatorRevalidatesTokenAfterPreRegistrationDisable(t *testing.T) {
	store, detail := createAutomationFixture(t, false)
	enableAutomation(t, store, detail.Automation.ID)
	evaluation := reserveAutomation(t, store)
	if _, err := store.SetAutomationEnabled(context.Background(), detail.Automation.ID, false, false); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	close(release)
	service := newAutomationService(store, slog.Default(), blockingGitHubIssueLister{started: started, release: release})
	service.evaluate(context.Background(), evaluation)
	select {
	case <-started:
		t.Fatal("stale evaluation invoked GitHub after disable")
	default:
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

func TestAutomationPreviewSharesEvaluatorCapacity(t *testing.T) {
	store, detail := createAutomationFixture(t, false)
	started := make(chan struct{}, maxConcurrentAutomationChecks+1)
	release := make(chan struct{})
	service := newAutomationService(store, slog.Default(), blockingGitHubIssueLister{started: started, release: release})

	errorsByCheck := make(chan error, maxConcurrentAutomationChecks)
	for range maxConcurrentAutomationChecks {
		go func() {
			_, err := service.Test(context.Background(), detail.Automation.ID)
			errorsByCheck <- err
		}()
	}
	for range maxConcurrentAutomationChecks {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("preview did not occupy evaluator capacity")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := service.Test(ctx, detail.Automation.ID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fifth preview error = %v", err)
	}
	select {
	case <-started:
		t.Fatal("fifth preview bypassed evaluator capacity")
	default:
	}
	close(release)
	for range maxConcurrentAutomationChecks {
		if err := <-errorsByCheck; err != nil {
			t.Fatal(err)
		}
	}
}

func TestAutomationListReleasesRowsBeforeLatestTaskLookups(t *testing.T) {
	store, first := createAutomationFixture(t, false)
	_, created, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "automation-list-second", Name: "Second Automation",
		WorkflowID: first.Automation.WorkflowID, RepositoryID: first.Automation.RepositoryID,
		TimeoutSeconds: 60,
		Trigger: protocol.AutomationTrigger{
			Type: protocol.AutomationTriggerGitHubIssue, State: "open", PollIntervalSeconds: 10,
		},
	})
	if err != nil || !created {
		t.Fatalf("create second Automation = created %v, error %v", created, err)
	}
	store.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	page, err := store.Automations(ctx, 10)
	if err != nil || len(page.Automations) != 2 {
		t.Fatalf("Automation list with one connection = error %v, page %#v", err, page)
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

func TestGitHubPullRequestRunnerUsesTypedFiltersAndCompleteBaseBranchPasses(t *testing.T) {
	trigger := protocol.GitHubPullRequestTrigger{
		Type: protocol.AutomationTriggerGitHubPullRequest, State: "open",
		IncludeDrafts: false, RequiredLabels: []string{"factory:review"},
		BaseBranches: []string{"main", "release"}, PollIntervalSeconds: 10,
	}
	calls := make([]string, 0, 2)
	runner := githubIssueRunner{
		lookPath: fakeGHPath,
		run: func(_ context.Context, command string, arguments ...string) ([]byte, []byte, bool, bool, error) {
			calls = append(calls, command+" "+strings.Join(arguments, " "))
			match := testPullRequest
			if slicesContain(arguments, "release") {
				match.Number, match.URL, match.BaseBranch, match.HeadCommit = 186,
					"https://github.com/owainlewis/factory/pull/186", "release", strings.Repeat("b", 40)
			}
			return fakePullRequestJSON(match), nil, false, false, nil
		},
	}
	matches, err := runner.ListPullRequests(context.Background(), "github.com/owainlewis/factory", trigger)
	if err != nil || len(matches) != 2 {
		t.Fatalf("pull-request matches = %#v, error %v", matches, err)
	}
	if len(calls) != 2 {
		t.Fatalf("gh call count = %d, calls %#v", len(calls), calls)
	}
	for _, fragment := range []string{
		"gh pr list --repo owainlewis/factory --state open --limit 101",
		"--json number,title,url,labels,state,isDraft,baseRefName,headRefOid",
		"--search draft:false", "--label factory:review",
	} {
		if !strings.Contains(calls[0], fragment) {
			t.Fatalf("first pull-request command %q missing %q", calls[0], fragment)
		}
	}
	if !strings.Contains(calls[0], "--base main") || !strings.Contains(calls[1], "--base release") {
		t.Fatalf("base-branch calls = %#v", calls)
	}
}

func TestGitHubPullRequestRunnerValidatesDraftLabelsBasesLimitsAndErrors(t *testing.T) {
	baseTrigger := protocol.GitHubPullRequestTrigger{
		Type: protocol.AutomationTriggerGitHubPullRequest, State: "open",
		RequiredLabels: []string{"factory:review"}, BaseBranches: []string{"main"},
		PollIntervalSeconds: 10,
	}
	tests := []struct {
		name    string
		trigger protocol.GitHubPullRequestTrigger
		match   protocol.GitHubPullRequestMatch
		code    string
	}{
		{name: "draft excluded", trigger: baseTrigger, match: func() protocol.GitHubPullRequestMatch { value := testPullRequest; value.IsDraft = true; return value }(), code: "gh_invalid_output"},
		{name: "required label missing", trigger: baseTrigger, match: func() protocol.GitHubPullRequestMatch {
			value := testPullRequest
			value.Labels = []string{"enhancement"}
			return value
		}(), code: "gh_invalid_output"},
		{name: "base branch mismatch", trigger: baseTrigger, match: func() protocol.GitHubPullRequestMatch {
			value := testPullRequest
			value.BaseBranch = "develop"
			return value
		}(), code: "gh_invalid_output"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := githubIssueRunner{lookPath: fakeGHPath, run: fakeGHRun(fakePullRequestJSON(test.match), nil, false, false, nil)}
			_, err := runner.ListPullRequests(context.Background(), "github.com/owainlewis/factory", test.trigger)
			var checkErr *automationCheckError
			if !errors.As(err, &checkErr) || checkErr.code != test.code {
				t.Fatalf("error = %#v, want %q", err, test.code)
			}
		})
	}
	includeDrafts := baseTrigger
	includeDrafts.IncludeDrafts = true
	draft := testPullRequest
	draft.IsDraft = true
	var draftArguments []string
	draftRunner := githubIssueRunner{lookPath: fakeGHPath, run: func(_ context.Context, _ string, arguments ...string) ([]byte, []byte, bool, bool, error) {
		draftArguments = append([]string(nil), arguments...)
		return fakePullRequestJSON(draft), nil, false, false, nil
	}}
	if matches, err := draftRunner.ListPullRequests(context.Background(), "github.com/owainlewis/factory", includeDrafts); err != nil || len(matches) != 1 || !matches[0].IsDraft {
		t.Fatalf("included draft = %#v, error %v", matches, err)
	}
	if slicesContain(draftArguments, "draft:false") {
		t.Fatalf("include-drafts command unexpectedly filters drafts: %#v", draftArguments)
	}

	for _, test := range []struct {
		name   string
		runner githubIssueRunner
		code   string
	}{
		{name: "malformed", runner: githubIssueRunner{lookPath: fakeGHPath, run: fakeGHRun([]byte("{"), nil, false, false, nil)}, code: "gh_malformed_output"},
		{name: "null array", runner: githubIssueRunner{lookPath: fakeGHPath, run: fakeGHRun([]byte("null"), nil, false, false, nil)}, code: "gh_malformed_output"},
		{name: "missing draft field", runner: githubIssueRunner{lookPath: fakeGHPath, run: fakeGHRun([]byte(`[{"number":185,"title":"Pull request","url":"https://github.com/owainlewis/factory/pull/185","state":"OPEN","baseRefName":"main","headRefOid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","labels":[{"id":"1","name":"factory:review","description":"","color":"ffffff"}]}]`), nil, false, false, nil)}, code: "gh_malformed_output"},
		{name: "permission", runner: githubIssueRunner{lookPath: fakeGHPath, run: fakeGHRun(nil, []byte("GraphQL: Resource not accessible by personal access token"), false, false, errors.New("exit 1"))}, code: "gh_permission_denied"},
		{name: "oversized", runner: githubIssueRunner{lookPath: fakeGHPath, run: fakeGHRun(nil, nil, true, false, nil)}, code: "gh_output_too_large"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.runner.ListPullRequests(context.Background(), "github.com/owainlewis/factory", baseTrigger)
			var checkErr *automationCheckError
			if !errors.As(err, &checkErr) || checkErr.code != test.code || checkErr.message == "" {
				t.Fatalf("error = %#v, want actionable %q", err, test.code)
			}
		})
	}

	manyTrigger := baseTrigger
	manyTrigger.BaseBranches = []string{"main", "release"}
	manyRunner := githubIssueRunner{lookPath: fakeGHPath, run: func(_ context.Context, _ string, arguments ...string) ([]byte, []byte, bool, bool, error) {
		branch := "main"
		if slicesContain(arguments, "release") {
			branch = "release"
		}
		matches := make([]protocol.GitHubPullRequestMatch, 51)
		for index := range matches {
			number := index + 1
			if branch == "release" {
				number += 1000
			}
			matches[index] = testPullRequest
			matches[index].Number = number
			matches[index].URL = "https://github.com/owainlewis/factory/pull/" + strconv.Itoa(number)
			matches[index].BaseBranch = branch
			matches[index].HeadCommit = fmt.Sprintf("%040x", number)
		}
		return fakePullRequestJSON(matches...), nil, false, false, nil
	}}
	_, err := manyRunner.ListPullRequests(context.Background(), "github.com/owainlewis/factory", manyTrigger)
	var limitErr *automationCheckError
	if !errors.As(err, &limitErr) || limitErr.code != "gh_match_limit" {
		t.Fatalf("cross-base match limit error = %#v", err)
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
			Trigger: protocol.AutomationTrigger{
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

func fakePullRequestJSON(matches ...protocol.GitHubPullRequestMatch) []byte {
	values := make([]map[string]any, 0, len(matches))
	for _, match := range matches {
		labels := make([]map[string]string, 0, len(match.Labels))
		for index, label := range match.Labels {
			labels = append(labels, map[string]string{
				"id": strconv.Itoa(index + 1), "name": label, "description": "", "color": "ffffff",
			})
		}
		values = append(values, map[string]any{
			"number": match.Number, "title": match.Title, "url": match.URL,
			"state": strings.ToUpper(match.State), "isDraft": match.IsDraft,
			"baseRefName": match.BaseBranch, "headRefOid": match.HeadCommit,
			"labels": labels,
		})
	}
	body, _ := json.Marshal(values)
	return body
}

func slicesContain(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
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
		Trigger: protocol.AutomationTrigger{Type: "github_issue", State: "open", RequiredLabels: []string{"factory:ready"}, PollIntervalSeconds: 10},
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

func TestHTTPPullRequestAutomationUsesStrictTypedTrigger(t *testing.T) {
	store := newTestStore(t)
	workflow := createTestWorkflow(t, store, "http-pull-request-workflow", "Review", "Review safely.")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/factory")
	service := newAutomationService(store, slog.Default(), fakeGitHubAutomationLister{pullRequests: []protocol.GitHubPullRequestMatch{testPullRequest}})
	server := httptest.NewServer(NewHandlerWithAutomation(store, slog.Default(), service))
	defer server.Close()
	post := func(body string) *http.Response {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/automations", strings.NewReader(body))
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
	invalidBody := fmt.Sprintf(`{
		"request_key":"invalid-mixed-trigger","name":"Invalid mixed trigger",
		"workflow_id":%q,"repository_id":%q,"context":"","timeout_seconds":60,
		"trigger":{"type":"github_issue","state":"open","required_labels":[],"base_branches":["main"],"poll_interval_seconds":10}
	}`, workflow.Workflow.ID, repository.ID)
	response := post(invalidBody)
	if response.StatusCode != http.StatusBadRequest {
		body := decodeResponse[protocol.ErrorBody](t, response)
		t.Fatalf("mixed typed trigger status = %d body %#v", response.StatusCode, body)
	}
	response.Body.Close()
	validBody := fmt.Sprintf(`{
		"request_key":"http-pull-request-create","name":"HTTP pull requests",
		"workflow_id":%q,"repository_id":%q,"context":"Review only.","timeout_seconds":60,
		"trigger":{"type":"github_pull_request","state":"open","include_drafts":false,
		"required_labels":["factory:review"],"base_branches":["main"],"poll_interval_seconds":10}
	}`, workflow.Workflow.ID, repository.ID)
	response = post(validBody)
	requireStatus(t, response, http.StatusCreated)
	created := decodeResponse[protocol.AutomationDetail](t, response)
	if created.Automation.Trigger.Type != protocol.AutomationTriggerGitHubPullRequest || created.Automation.Trigger.BaseBranches[0] != "main" {
		t.Fatalf("HTTP pull-request Automation = %#v", created.Automation)
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/automations/"+created.Automation.ID+"/test", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, response, http.StatusOK)
	preview := decodeResponse[protocol.TestAutomationResult](t, response)
	if len(preview.Matches) != 1 || preview.Matches[0].BaseBranch != "main" {
		t.Fatalf("HTTP pull-request preview = %#v", preview)
	}
}
