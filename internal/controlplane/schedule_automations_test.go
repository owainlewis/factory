package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestCronScheduleValidationAndVixieDaySemantics(t *testing.T) {
	for _, test := range []struct {
		name       string
		cron       string
		timezone   string
		errorMatch string
	}{
		{name: "seconds", cron: "0 0 9 * * *", timezone: "UTC", errorMatch: "five fields"},
		{name: "alias", cron: "@daily", timezone: "UTC", errorMatch: "five fields"},
		{name: "question", cron: "0 9 ? * *", timezone: "UTC", errorMatch: "unsupported"},
		{name: "embedded timezone", cron: "CRON_TZ=UTC 0 9 * * *", timezone: "UTC", errorMatch: "five fields"},
		{name: "bad step", cron: "*/0 9 * * *", timezone: "UTC", errorMatch: "positive"},
		{name: "bad zone", cron: "0 9 * * *", timezone: "Mars/Olympus", errorMatch: "IANA"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, err := parseCronSchedule(test.cron, test.timezone)
			if err == nil || !stringsContain(err.Error(), test.errorMatch) {
				t.Fatalf("parse error = %v, want %q", err, test.errorMatch)
			}
		})
	}

	schedule, _, _, err := parseCronSchedule("0 9 1 * MON", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	next, err := schedule.Next(time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("Vixie OR next = %s, want %s", next, want)
	}

	steppedWildcard, _, _, err := parseCronSchedule("0 9 */2 * MON", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	next, err = steppedWildcard.Next(time.Date(2026, 6, 7, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want = time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("Vixie stepped wildcard next = %s, want %s", next, want)
	}
}

func TestCronScheduleDSTOverlapAndMissingLocalTime(t *testing.T) {
	overlap, _, _, err := parseCronSchedule("30 1 25 OCT *", "Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	first, err := overlap.Next(time.Date(2026, 10, 24, 23, 59, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	second, err := overlap.Next(first)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Equal(time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)) ||
		!second.Equal(time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC)) {
		t.Fatalf("overlap instants = %s and %s", first, second)
	}

	missing, _, _, err := parseCronSchedule("30 1 29 MAR *", "Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	next, err := missing.Next(time.Date(2026, 3, 28, 23, 59, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if next.Year() == 2026 {
		t.Fatalf("nonexistent 2026 local time unexpectedly matched %s", next)
	}
}

func createScheduleAutomationFixture(
	t *testing.T,
	now *time.Time,
	withWorker bool,
) (*Store, protocol.AutomationDetail) {
	t.Helper()
	store := newTestStore(t)
	store.now = func() time.Time { return *now }
	workflow := createTestWorkflow(t, store, "schedule-workflow", "Scheduled maintenance", "Inspect the repository and perform the scheduled maintenance.")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/factory")
	if withWorker {
		_, err := store.RegisterWorker(context.Background(), "schedule-worker", protocol.WorkerRegistration{
			Name: "schedule-worker", WorkerVersion: "test", RuntimeVersion: "test",
			Capacity: 1, Health: "healthy", AcceptsManagedRepositories: true,
			ManagedRepositoryIDs: []string{repository.ID},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	detail, created, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "schedule-create", Name: "Daily maintenance",
		WorkflowID: workflow.Workflow.ID, RepositoryID: repository.ID,
		Context: "Use the safe managed repository only.", TimeoutSeconds: 3600,
		Trigger: protocol.AutomationTrigger{
			Type: protocol.AutomationTriggerSchedule, Cron: "0 9 * * *", Timezone: "Europe/London",
		},
	})
	if err != nil || !created {
		t.Fatalf("create schedule = created %v, error %v", created, err)
	}
	return store, detail
}

func TestSchedulePreviewEnableDueDispatchAndIdempotencyUseFakeClock(t *testing.T) {
	now := time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC) // 08:00 BST.
	store, detail := createScheduleAutomationFixture(t, &now, true)
	service := newAutomationService(store, nil, fakeGitHubIssueLister{})
	preview, err := service.Test(context.Background(), detail.Automation.ID)
	if err != nil || preview.NextDueAt == nil || !preview.NextDueAt.Equal(time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("schedule preview = %#v, error %v", preview, err)
	}
	afterPreview, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil || len(afterPreview.Occurrences) != 0 || afterPreview.Automation.NextDueAt != nil {
		t.Fatalf("preview mutated state = %#v, error %v", afterPreview, err)
	}
	enabled, err := store.SetAutomationEnabled(context.Background(), detail.Automation.ID, true, false)
	if err != nil || enabled.Automation.NextDueAt == nil || !enabled.Automation.NextDueAt.Equal(*preview.NextDueAt) {
		t.Fatalf("enable schedule = %#v, error %v", enabled, err)
	}
	now = *preview.NextDueAt
	if _, err := store.RegisterWorker(context.Background(), "schedule-worker", protocol.WorkerRegistration{
		Name: "schedule-worker", WorkerVersion: "test", RuntimeVersion: "test",
		Capacity: 1, Health: "healthy", AcceptsManagedRepositories: true,
		ManagedRepositoryIDs: []string{detail.Automation.RepositoryID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.processDueSchedules(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if err := store.processDueSchedules(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if err := store.dispatchPendingOccurrences(context.Background(), 100); err != nil {
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
		current.Occurrences[0].Kind != "scheduled" || current.Occurrences[0].ScheduledAt == nil ||
		!current.Occurrences[0].ScheduledAt.Equal(now) {
		t.Fatalf("scheduled occurrence = %#v", current.Occurrences)
	}
	if current.Automation.NextDueAt == nil || !current.Automation.NextDueAt.After(now) || current.Automation.DispatchedCount != 1 {
		t.Fatalf("schedule counters/cursor = %#v", current.Automation)
	}
	task, err := store.Task(context.Background(), current.Occurrences[0].Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"Schedule instruction:", `"type":"schedule"`, `"kind":"scheduled"`, `"timezone":"Europe/London"`} {
		if !stringsContain(task.ResolvedPrompt, required) {
			t.Fatalf("schedule prompt missing %q:\n%s", required, task.ResolvedPrompt)
		}
	}
	if stringsContain(task.ResolvedPrompt, "Use authenticated gh") || stringsContain(task.ResolvedPrompt, "provider item") && !stringsContain(task.ResolvedPrompt, "There is no provider item") {
		t.Fatalf("schedule prompt contains provider revalidation: %s", task.ResolvedPrompt)
	}
}

func TestScheduleAutomationUpdateNormalizesAndRecoversLostResponse(t *testing.T) {
	now := time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC)
	store, detail := createScheduleAutomationFixture(t, &now, false)
	input := protocol.UpdateAutomationRequest{
		ExpectedVersion: 1, Name: "Weekday maintenance", WorkflowID: detail.Automation.WorkflowID,
		Context: "Updated schedule context.", TimeoutSeconds: 7200,
		Trigger: protocol.AutomationTrigger{Type: protocol.AutomationTriggerSchedule, Cron: " 30  10 * * MON-FRI ", Timezone: " UTC "},
	}
	updated, err := store.UpdateAutomation(context.Background(), detail.Automation.ID, input)
	if err != nil || updated.Automation.Version != 2 || updated.Automation.Trigger.Cron != "30 10 * * MON-FRI" || updated.Automation.Trigger.Timezone != "UTC" {
		t.Fatalf("update schedule = %#v, error %v", updated.Automation, err)
	}
	replayed, err := store.UpdateAutomation(context.Background(), detail.Automation.ID, input)
	if err != nil || replayed.Automation.Version != 2 {
		t.Fatalf("lost update replay = %#v, error %v", replayed.Automation, err)
	}
	input.ExpectedVersion = 2
	input.Trigger.Cron = "0 0 9 * * *"
	_, err = store.UpdateAutomation(context.Background(), detail.Automation.ID, input)
	assertErrorCode(t, err, "invalid_cron")
	input.Trigger.Cron = "0 9 * * *"
	input.Trigger.Timezone = "Not/AZone"
	_, err = store.UpdateAutomation(context.Background(), detail.Automation.ID, input)
	assertErrorCode(t, err, "invalid_timezone")
}

func TestScheduleStartupCatchUpAdvancesToFirstFutureInstant(t *testing.T) {
	now := time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC)
	store, detail := createScheduleAutomationFixture(t, &now, false)
	enabled := enableAutomation(t, store, detail.Automation.ID)
	storedDue := *enabled.Automation.NextDueAt
	now = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	if err := store.recoverAutomationRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.processDueSchedules(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if err := store.processDueSchedules(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	current, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Occurrences) != 1 || current.Occurrences[0].ScheduledAt == nil ||
		!current.Occurrences[0].ScheduledAt.Equal(storedDue) || current.Occurrences[0].Diagnostic != "catch_up; 3 later scheduled instants skipped" {
		t.Fatalf("catch-up occurrence = %#v", current.Occurrences)
	}
	if current.Automation.SkippedCount != 3 {
		t.Fatalf("catch-up skipped count = %d, want 3", current.Automation.SkippedCount)
	}
	if current.Automation.NextDueAt == nil || !current.Automation.NextDueAt.After(now) {
		t.Fatalf("catch-up cursor = %#v", current.Automation.NextDueAt)
	}
}

func TestScheduleCatchUpClassificationStartsAfterDueMinute(t *testing.T) {
	for _, test := range []struct {
		name           string
		offset         time.Duration
		wantDiagnostic string
	}{
		{name: "exact due instant"},
		{name: "milliseconds after due", offset: 100 * time.Millisecond},
		{name: "end of due minute", offset: 59*time.Second + 999*time.Millisecond},
		{name: "start of next minute", offset: time.Minute, wantDiagnostic: "catch_up"},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC)
			store, detail := createScheduleAutomationFixture(t, &now, false)
			enabled := enableAutomation(t, store, detail.Automation.ID)
			now = enabled.Automation.NextDueAt.Add(test.offset)
			if err := store.recoverAutomationRuntime(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := store.processDueSchedules(context.Background(), 100); err != nil {
				t.Fatal(err)
			}
			current, err := store.Automation(context.Background(), detail.Automation.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(current.Occurrences) != 1 || current.Occurrences[0].Diagnostic != test.wantDiagnostic {
				t.Fatalf("occurrences = %#v, want diagnostic %q", current.Occurrences, test.wantDiagnostic)
			}
			if current.Automation.SkippedCount != 0 {
				t.Fatalf("skipped count = %d, want 0", current.Automation.SkippedCount)
			}
		})
	}
}

func TestScheduleDisabledDependencyCatchUpCountsMissedInstants(t *testing.T) {
	now := time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC)
	store, detail := createScheduleAutomationFixture(t, &now, false)
	enabled := enableAutomation(t, store, detail.Automation.ID)
	if _, err := store.SetWorkflowEnabled(context.Background(), detail.Automation.WorkflowID, false); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	if err := store.processDueSchedules(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	current, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Occurrences) != 1 || current.Occurrences[0].ScheduledAt == nil ||
		!current.Occurrences[0].ScheduledAt.Equal(*enabled.Automation.NextDueAt) ||
		current.Occurrences[0].Diagnostic != "workflow_disabled; catch_up; 3 later scheduled instants skipped" {
		t.Fatalf("disabled catch-up occurrence = %#v", current.Occurrences)
	}
	if current.Automation.SkippedCount != 4 {
		t.Fatalf("disabled catch-up skipped count = %d, want 4", current.Automation.SkippedCount)
	}
	if current.Automation.NextDueAt == nil || !current.Automation.NextDueAt.After(now) {
		t.Fatalf("disabled catch-up cursor = %#v", current.Automation.NextDueAt)
	}
}

func TestScheduleRunNowConcurrentReplayDisableAndCursorIsolation(t *testing.T) {
	now := time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC)
	store, detail := createScheduleAutomationFixture(t, &now, true)
	enabled := enableAutomation(t, store, detail.Automation.ID)
	due := *enabled.Automation.NextDueAt
	const callers = 8
	var wait sync.WaitGroup
	errorsSeen := make(chan error, callers)
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.RunAutomationNow(context.Background(), detail.Automation.ID, protocol.RunAutomationRequest{RequestKey: "concurrent-run"})
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent Run now: %v", err)
		}
	}
	if err := store.dispatchPendingOccurrences(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetAutomationEnabled(context.Background(), detail.Automation.ID, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RunAutomationNow(context.Background(), detail.Automation.ID, protocol.RunAutomationRequest{RequestKey: "concurrent-run"}); err != nil {
		t.Fatalf("lost-response replay after disable: %v", err)
	}
	_, err := store.RunAutomationNow(context.Background(), detail.Automation.ID, protocol.RunAutomationRequest{RequestKey: "new-run"})
	assertErrorCode(t, err, "automation_disabled")
	current, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Occurrences) != 1 || current.Occurrences[0].Kind != "run_now" || current.Occurrences[0].Task == nil {
		t.Fatalf("concurrent Run now occurrences = %#v", current.Occurrences)
	}
	task, err := store.Task(context.Background(), current.Occurrences[0].Task.ID)
	if err != nil || task.Task.State != "queued" {
		t.Fatalf("disable changed the admitted task = %#v, error %v", task.Task, err)
	}
	if current.Automation.NextDueAt != nil {
		t.Fatalf("disabled schedule retained due cursor %s", current.Automation.NextDueAt)
	}
	var scheduledCursorCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM automation_schedule_occurrences WHERE automation_id = ? AND kind = 'run_now'`, detail.Automation.ID).Scan(&scheduledCursorCount); err != nil {
		t.Fatal(err)
	}
	if scheduledCursorCount != 1 || due.IsZero() {
		t.Fatalf("Run now identity count = %d, original due = %s", scheduledCursorCount, due)
	}
}

func TestInvalidStoredScheduleDegradesOnlyItsAutomation(t *testing.T) {
	now := time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC)
	store, detail := createScheduleAutomationFixture(t, &now, true)
	enabled := enableAutomation(t, store, detail.Automation.ID)
	now = *enabled.Automation.NextDueAt
	if _, err := store.db.Exec(`UPDATE automation_schedule_triggers SET cron = 'invalid' WHERE automation_id = ?`, detail.Automation.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.processDueSchedules(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	degraded, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil || degraded.Automation.Health.Code != "stored_schedule_invalid" || degraded.Automation.NextDueAt != nil {
		t.Fatalf("degraded schedule = %#v, error %v", degraded.Automation, err)
	}

	provider, created, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "provider-after-schedule-degradation", Name: "Provider remains available",
		WorkflowID: detail.Automation.WorkflowID, RepositoryID: detail.Automation.RepositoryID,
		TimeoutSeconds: 60,
		Trigger: protocol.AutomationTrigger{
			Type: protocol.AutomationTriggerGitHubIssue, State: "open",
			RequiredLabels: []string{"factory:ready"}, PollIntervalSeconds: 30,
		},
	})
	if err != nil || !created {
		t.Fatalf("provider Automation after schedule degradation = created %v, error %v", created, err)
	}
	service := newAutomationService(store, slog.Default(), fakeGitHubIssueLister{matches: []protocol.GitHubIssueMatch{testIssue}})
	preview, err := service.Test(context.Background(), provider.Automation.ID)
	if err != nil || len(preview.Matches) != 1 {
		t.Fatalf("provider preview after schedule degradation = %#v, error %v", preview, err)
	}
	if _, err := store.RegisterWorker(context.Background(), "api-isolation-worker", protocol.WorkerRegistration{
		Name: "api-isolation-worker", WorkerVersion: "test", RuntimeVersion: "test",
		Capacity: 1, Health: "healthy", AcceptsManagedRepositories: true,
		SourceAccess: []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	}); err != nil {
		t.Fatal(err)
	}
	task, created, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "ordinary-task-after-schedule-degradation", Title: "Ordinary task remains available",
		Description: "Prove the ordinary task API remains isolated from scheduler health.",
		Route: &protocol.TaskRoute{
			RepositoryRemoteIdentity: detail.Automation.RepositoryIdentity,
			SourceAccess:             protocol.SourceAccess{Provider: "github", Hostname: "github.com"},
		},
		TimeoutSeconds: 60,
	})
	if err != nil || !created || task.Task.State != "queued" {
		t.Fatalf("ordinary task after schedule degradation = created %v, task %#v, error %v", created, task.Task, err)
	}
}

func TestScheduleDisableAndShutdownAdmitNoFutureOccurrences(t *testing.T) {
	now := time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC)
	store, detail := createScheduleAutomationFixture(t, &now, false)
	enableAutomation(t, store, detail.Automation.ID)
	if _, err := store.SetAutomationEnabled(context.Background(), detail.Automation.ID, false, false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(48 * time.Hour)
	if err := store.processDueSchedules(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := newAutomationService(store, nil, fakeGitHubIssueLister{})
	done := make(chan struct{})
	go func() { defer close(done); service.Run(ctx) }()
	cancel()
	<-done
	service.Wake()
	current, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil || len(current.Occurrences) != 0 {
		t.Fatalf("disable/shutdown admitted occurrences = %#v, error %v", current.Occurrences, err)
	}
}

func TestScheduleShutdownDrainsOccurrenceCommittedBeforeCancellation(t *testing.T) {
	now := time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC)
	store, detail := createScheduleAutomationFixture(t, &now, true)
	enabled := enableAutomation(t, store, detail.Automation.ID)
	now = *enabled.Automation.NextDueAt
	if _, err := store.RegisterWorker(context.Background(), "schedule-worker", protocol.WorkerRegistration{
		Name: "schedule-worker", WorkerVersion: "test", RuntimeVersion: "test",
		Capacity: 1, Health: "healthy", AcceptsManagedRepositories: true,
		ManagedRepositoryIDs: []string{detail.Automation.RepositoryID},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := newAutomationService(store, nil, fakeGitHubIssueLister{})
	service.afterScheduleAdmission = cancel
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.Run(ctx)
	}()
	<-done

	current, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Occurrences) != 1 || current.Occurrences[0].Kind != "scheduled" || current.Occurrences[0].Task == nil {
		t.Fatalf("shutdown did not drain committed occurrence = %#v", current.Occurrences)
	}
	task, err := store.Task(context.Background(), current.Occurrences[0].Task.ID)
	if err != nil || task.Task.State != "queued" {
		t.Fatalf("shutdown-drained task = %#v, error %v", task.Task, err)
	}
}

func TestScheduleShutdownAdmissionGateRejectsRunNowAndNewDueInstants(t *testing.T) {
	now := time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC)
	store, detail := createScheduleAutomationFixture(t, &now, false)
	enabled := enableAutomation(t, store, detail.Automation.ID)
	service := newAutomationService(store, slog.Default(), fakeGitHubIssueLister{})
	server := httptest.NewServer(NewHandlerWithAutomation(store, slog.Default(), service))
	defer server.Close()
	service.StopAdmission()
	now = *enabled.Automation.NextDueAt
	if err := service.admitDueSchedules(context.Background(), 100); err != nil {
		t.Fatal(err)
	}

	const callers = 8
	statuses := make(chan int, callers)
	var wait sync.WaitGroup
	for index := range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			body, err := json.Marshal(protocol.RunAutomationRequest{RequestKey: fmt.Sprintf("shutdown-run-%d", index)})
			if err != nil {
				t.Error(err)
				return
			}
			response, err := server.Client().Post(
				server.URL+"/api/v1/automations/"+detail.Automation.ID+"/run",
				"application/json", bytes.NewReader(body),
			)
			if err != nil {
				t.Error(err)
				return
			}
			defer response.Body.Close()
			statuses <- response.StatusCode
		}()
	}
	wait.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusConflict {
			t.Fatalf("Run now during shutdown status = %d", status)
		}
	}
	current, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil || len(current.Occurrences) != 0 {
		t.Fatalf("shutdown gate admitted occurrences = %#v, error %v", current.Occurrences, err)
	}
}

func TestHTTPSchedulePreviewEnableAndRunNowAreStrictAndIdempotent(t *testing.T) {
	now := time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC)
	store, detail := createScheduleAutomationFixture(t, &now, false)
	service := newAutomationService(store, slog.Default(), fakeGitHubIssueLister{})
	server := httptest.NewServer(NewHandlerWithAutomation(store, slog.Default(), service))
	defer server.Close()
	request := func(method, path string, body any) *http.Response {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := request(http.MethodPost, "/api/v1/automations/"+detail.Automation.ID+"/test", struct{}{})
	requireStatus(t, response, http.StatusOK)
	preview := decodeResponse[protocol.TestAutomationResult](t, response)
	if preview.NextDueAt == nil || len(preview.Matches) != 0 {
		t.Fatalf("HTTP schedule preview = %#v", preview)
	}
	response = request(http.MethodPut, "/api/v1/automations/"+detail.Automation.ID+"/enabled", map[string]any{"enabled": true})
	requireStatus(t, response, http.StatusOK)
	response.Body.Close()
	response = request(http.MethodPost, "/api/v1/automations/"+detail.Automation.ID+"/check", struct{}{})
	requireStatus(t, response, http.StatusConflict)
	response.Body.Close()
	for range 2 {
		response = request(http.MethodPost, "/api/v1/automations/"+detail.Automation.ID+"/run", protocol.RunAutomationRequest{RequestKey: "http-run"})
		requireStatus(t, response, http.StatusAccepted)
		response.Body.Close()
	}
	current, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil || len(current.Occurrences) != 1 || current.Occurrences[0].RunRequestKey != "http-run" {
		t.Fatalf("HTTP Run now replay = %#v, error %v", current.Occurrences, err)
	}

	workflow := createTestWorkflow(t, store, "invalid-schedule-workflow", "Invalid", "Do not run.")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/invalid-schedule")
	response = request(http.MethodPost, "/api/v1/automations", map[string]any{
		"request_key": "invalid-schedule-shape", "name": "Invalid schedule shape",
		"workflow_id": workflow.Workflow.ID, "repository_id": repository.ID,
		"context": "", "timeout_seconds": 60,
		"trigger": map[string]any{"type": "schedule", "cron": "0 9 * * *", "timezone": "UTC", "state": "open"},
	})
	requireStatus(t, response, http.StatusBadRequest)
	response.Body.Close()
}
