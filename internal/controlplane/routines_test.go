package controlplane

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestRoutineAdmissionWorkerLifecycleAndAggregateWork(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Review factory", Prompt: "Review the repository for real bugs.", Runtime: protocol.RuntimeCodex,
		TimeoutSeconds: 3600, ConcurrencyLimit: 10,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if routine.ConcurrencyLimit != 10 || len(routine.Repositories) != 1 {
		t.Fatalf("routine = %#v", routine)
	}
	detail, created, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{RequestKey: "review-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !created || detail.Work.State != protocol.WorkQueued || len(detail.Targets) != 1 {
		t.Fatalf("admitted Work = %#v", detail)
	}
	replayed, created, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{RequestKey: "review-1"})
	if err != nil || created || replayed.Work.ID != detail.Work.ID {
		t.Fatalf("replayed Work = %#v, created %v, err %v", replayed, created, err)
	}
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{RequestID: "claim-1", LeaseToken: tokenA})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, err %v", claim, err)
	}
	if claim.Target.ID != detail.Targets[0].ID || claim.Target.WorkID != detail.Work.ID {
		t.Fatalf("claim does not identify Work Target: %#v", claim)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded", Result: "No blocking bugs.",
	}); err != nil {
		t.Fatal(err)
	}
	detail, err = store.Work(context.Background(), detail.Work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Work.State != protocol.WorkSucceeded || detail.Work.SucceededCount != 1 || detail.Work.TerminalAt == nil {
		t.Fatalf("completed Work = %#v", detail.Work)
	}
	if detail.Targets[0].Result != "No blocking bugs." || len(detail.Targets[0].Attempts) != 1 {
		t.Fatalf("completed Target = %#v", detail.Targets[0])
	}
}

func TestRoutineRunReplayReturnsCommittedWorkAfterRoutineChanges(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Replay after edit", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, wasCreated, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{RequestKey: "ambiguous-run"})
	if err != nil || !wasCreated {
		t.Fatalf("initial Run = %#v, created %v, err %v", created, wasCreated, err)
	}
	if _, err := store.UpdateRoutine(context.Background(), routine.ID, protocol.SaveRoutineRequest{
		Name: "Replay after edit", Prompt: "Use the changed prompt.", Runtime: protocol.RuntimeCodex,
		ExpectedGeneration: routine.Generation,
	}); err != nil {
		t.Fatal(err)
	}
	replayed, wasCreated, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{RequestKey: "ambiguous-run"})
	if err != nil || wasCreated || replayed.Work.ID != created.Work.ID {
		t.Fatalf("replayed Run = %#v, created %v, err %v", replayed, wasCreated, err)
	}
	if replayed.Work.Routine.Prompt != "Review the repository." {
		t.Fatalf("replayed immutable Routine snapshot = %#v", replayed.Work.Routine)
	}
}

func TestRoutineRunReplayRejectsDifferentImmutableIdentity(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	createRoutine := func(name string) protocol.Routine {
		routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
			Name: name, Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
			RepositoryIDs: []string{worker.Repositories[0].ID},
		})
		if err != nil {
			t.Fatal(err)
		}
		return routine
	}
	first := createRoutine("First identity")
	second := createRoutine("Second identity")
	detail, _, err := store.RunRoutine(context.Background(), first.ID, protocol.RunRoutineRequest{RequestKey: "identity-key"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RunRoutine(context.Background(), second.ID, protocol.RunRoutineRequest{RequestKey: "identity-key"}); !serviceErrorCode(err, "request_key_conflict") {
		t.Fatalf("different Routine identity error = %v", err)
	}
	firstDue := time.Date(2026, time.August, 11, 9, 0, 0, 0, time.UTC)
	if _, _, err := store.admitRoutine(context.Background(), first.ID, "schedule", "identity-key", &firstDue, nil); !serviceErrorCode(err, "request_key_conflict") {
		t.Fatalf("different source identity error = %v", err)
	}
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE work SET source = 'schedule', scheduled_at = ? WHERE id = ?
	`, firstDue.UnixMilli(), detail.Work.ID); err != nil {
		t.Fatal(err)
	}
	secondDue := firstDue.Add(time.Hour)
	if _, _, err := store.admitRoutine(context.Background(), first.ID, "schedule", "identity-key", &secondDue, nil); !serviceErrorCode(err, "request_key_conflict") {
		t.Fatalf("different scheduled instant identity error = %v", err)
	}
}

func TestWorkDetailClosesTargetRowsBeforeLoadingAttempts(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Connection-safe detail", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, _, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{RequestKey: "connection-safe-detail"})
	if err != nil {
		t.Fatal(err)
	}
	store.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	loaded, err := store.Work(ctx, detail.Work.ID)
	if err != nil || len(loaded.Targets) != 1 {
		t.Fatalf("Work detail with one connection = %#v, err %v", loaded, err)
	}
}

func TestRoutineLimitCountsOnlyEditableRoutines(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `
		WITH RECURSIVE sequence(value) AS (
			SELECT 1 UNION ALL SELECT value + 1 FROM sequence WHERE value < 500
		)
		INSERT INTO routines(
			id, name, name_key, prompt, runtime, timeout_seconds, concurrency_limit,
			generation, archived, migration_only, read_only, schedule_enabled,
			schedule_health_status, created_at, updated_at
		)
		SELECT 'history-' || value, 'History ' || value, 'history ' || value,
			'Preserved history', 'codex', 7200, 10, 1, 1, 0, 1, 0, 'disabled', 0, 0
		FROM sequence
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRoutine(ctx, protocol.SaveRoutineRequest{
		Name: "First editable", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
	}); err != nil {
		t.Fatalf("read-only history consumed the Routine limit: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		WITH RECURSIVE sequence(value) AS (
			SELECT 1 UNION ALL SELECT value + 1 FROM sequence WHERE value < 499
		)
		INSERT INTO routines(
			id, name, name_key, prompt, runtime, timeout_seconds, concurrency_limit,
			generation, archived, migration_only, read_only, schedule_enabled,
			schedule_health_status, created_at, updated_at
		)
		SELECT 'editable-' || value, 'Editable ' || value, 'editable ' || value,
			'Review.', 'codex', 7200, 10, 1, 0, 0, 0, 0, 'disabled', 0, 0
		FROM sequence
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRoutine(ctx, protocol.SaveRoutineRequest{
		Name: "Over limit", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
	}); !serviceErrorCode(err, "routine_limit_reached") {
		t.Fatalf("editable Routine limit error = %v", err)
	}
}

func TestRoutineNamesUseUnicodeCaseNormalization(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Éclair", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "éCLAIR", Prompt: "Review again.", Runtime: protocol.RuntimeCodex,
	}); !serviceErrorCode(err, "routine_name_conflict") {
		t.Fatalf("Unicode Routine name conflict error = %v", err)
	}
}

func TestRoutineArchiveRequiresExplicitState(t *testing.T) {
	store := newTestStore(t)
	routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Archive guard", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetRoutineArchived(context.Background(), routine.ID, protocol.SetRoutineArchivedRequest{
		ExpectedGeneration: routine.Generation,
	}); !serviceErrorCode(err, "routine_archived_required") {
		t.Fatalf("missing archived state error = %v", err)
	}
	unchanged, err := store.Routine(context.Background(), routine.ID)
	if err != nil || unchanged.Archived || unchanged.Generation != routine.Generation {
		t.Fatalf("Routine changed after malformed archive request = %#v, err %v", unchanged, err)
	}
}

func TestManualRoutineRunRejectsSchedulerRequestKeys(t *testing.T) {
	store := newTestStore(t)
	routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Reserved key guard", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{
		RequestKey: "schedule:" + routine.ID + ":1:1234",
	}); !serviceErrorCode(err, "reserved_request_key") {
		t.Fatalf("reserved scheduler request key error = %v", err)
	}
}

func TestCancelWorkTargetCancelsOnlyTheSelectedTarget(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10,
		protocol.RepositoryRegistration{Key: "factory", RemoteIdentity: "github.com/owainlewis/factory"},
		protocol.RepositoryRegistration{Key: "neo", RemoteIdentity: "github.com/owainlewis/neo"},
	)
	routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Two repository review", Prompt: "Review both repositories.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID, worker.Repositories[1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, _, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{RequestKey: "two-targets"})
	if err != nil {
		t.Fatal(err)
	}
	detail, err = store.CancelWorkTarget(context.Background(), detail.Work.ID, detail.Targets[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Targets[0].State != protocol.WorkTargetCancelled || detail.Targets[1].State != protocol.WorkTargetQueued || detail.Work.State != protocol.WorkRunning {
		t.Fatalf("partially cancelled Work = %#v", detail)
	}
}

func TestWorkAggregateUsesCanonicalPrecedence(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name   string
		states []protocol.WorkTargetState
		expect protocol.WorkState
		active int
	}{
		{name: "blocked", states: []protocol.WorkTargetState{protocol.WorkTargetBlocked}, expect: protocol.WorkBlocked, active: 1},
		{name: "queued", states: []protocol.WorkTargetState{protocol.WorkTargetQueued}, expect: protocol.WorkQueued, active: 1},
		{name: "mixed active", states: []protocol.WorkTargetState{protocol.WorkTargetSucceeded, protocol.WorkTargetQueued}, expect: protocol.WorkRunning, active: 1},
		{name: "failed and cancelled", states: []protocol.WorkTargetState{protocol.WorkTargetFailed, protocol.WorkTargetCancelled}, expect: protocol.WorkFailed},
		{name: "partial", states: []protocol.WorkTargetState{protocol.WorkTargetSucceeded, protocol.WorkTargetFailed}, expect: protocol.WorkPartial},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			targets := make([]protocol.WorkTarget, len(test.states))
			for index, state := range test.states {
				targets[index].State = state
			}
			work := protocol.Work{}
			applyWorkAggregate(&work, targets, now)
			if work.State != test.expect || work.ActiveCount != test.active {
				t.Fatalf("aggregate = state %s active %d, want %s active %d", work.State, work.ActiveCount, test.expect, test.active)
			}
		})
	}
	work := protocol.Work{}
	applyWorkAggregate(&work, []protocol.WorkTarget{{
		State: protocol.WorkTargetBlocked, BlockedReason: routineConcurrencyBlockedReason,
	}}, now)
	if work.NeedsAttention {
		t.Fatal("normal Routine concurrency throttling needs attention")
	}
	applyWorkAggregate(&work, []protocol.WorkTarget{{
		State: protocol.WorkTargetBlocked, BlockedReason: "No compatible Worker is online.",
	}}, now)
	if !work.NeedsAttention {
		t.Fatal("actionable Worker block does not need attention")
	}
}

func TestOverviewDoesNotFlagRoutineConcurrencyThrottling(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10,
		protocol.RepositoryRegistration{Key: "factory", RemoteIdentity: "github.com/owainlewis/factory"},
		protocol.RepositoryRegistration{Key: "neo", RemoteIdentity: "github.com/owainlewis/neo"},
	)
	routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Throttled review", Prompt: "Review both repositories.", Runtime: protocol.RuntimeCodex,
		ConcurrencyLimit: 1,
		RepositoryIDs:    []string{worker.Repositories[0].ID, worker.Repositories[1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RunRoutine(context.Background(), routine.ID,
		protocol.RunRoutineRequest{RequestKey: "throttled-review"}); err != nil {
		t.Fatal(err)
	}
	overview, err := store.Overview(context.Background())
	if err != nil || overview.ActiveWork != 1 || overview.NeedsAttention != 0 {
		t.Fatalf("throttled Overview = %#v, err %v", overview, err)
	}
}

func TestRoutineScheduleUsesFrozenOccurrencePromptAndSkipsMissedInstants(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 10, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Morning review", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		TimeoutSeconds: 3600, ConcurrencyLimit: 10,
		RepositoryIDs: []string{worker.Repositories[0].ID},
		Schedule:      protocol.RoutineSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	if err := store.AdmitDueRoutines(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	page, err := store.WorkPage(context.Background(), 10, "")
	if err != nil || len(page.Work) != 1 {
		t.Fatalf("scheduled Work page = %#v, err %v", page, err)
	}
	detail, err := store.Work(context.Background(), page.Work[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Work.Source != "schedule" || detail.Work.ScheduledAt == nil ||
		!strings.Contains(detail.Targets[0].ResolvedPrompt, "Trusted schedule occurrence") {
		t.Fatalf("scheduled Work = %#v", detail)
	}
	updated, err := store.Routine(context.Background(), routine.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantNext := time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC)
	if updated.Schedule.NextDueAt == nil || !updated.Schedule.NextDueAt.Equal(wantNext) {
		t.Fatalf("next due = %v, want %v", updated.Schedule.NextDueAt, wantNext)
	}
}

func TestRoutineScheduleReservesPromptCapacityForOccurrenceMetadata(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Manual oversized schedule", Prompt: strings.Repeat("x", protocol.MaxRoutinePromptBytes),
		Runtime: protocol.RuntimeCodex, RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateRoutine(context.Background(), routine.ID, protocol.SaveRoutineRequest{
		Name: routine.Name, Prompt: strings.Repeat("x", protocol.MaxRoutinePromptBytes),
		Runtime: protocol.RuntimeCodex, RepositoryIDs: []string{worker.Repositories[0].ID},
		Schedule:           protocol.RoutineSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
		ExpectedGeneration: routine.Generation,
	})
	if !serviceErrorCode(err, "routine_schedule_prompt_too_large") {
		t.Fatalf("schedule edit prompt capacity error = %v", err)
	}

	_, err = store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Oversized schedule", Prompt: strings.Repeat("x", protocol.MaxRoutinePromptBytes),
		Runtime: protocol.RuntimeCodex, RepositoryIDs: []string{worker.Repositories[0].ID},
		Schedule: protocol.RoutineSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if !serviceErrorCode(err, "routine_schedule_prompt_too_large") {
		t.Fatalf("schedule prompt capacity error = %v", err)
	}
}

func TestRoutinesMigrationLeavesOnlyFinalProductTables(t *testing.T) {
	store := newTestStore(t)
	for _, table := range []string{"routines", "routine_repositories", "work", "work_targets"} {
		var exists int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&exists); err != nil || exists != 1 {
			t.Fatalf("final table %s exists = %d, err %v", table, exists, err)
		}
	}
	for _, table := range []string{"runs", "jobs", "tasks", "definitions", "workflows", "automations"} {
		var exists int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&exists); err != nil || exists != 0 {
			t.Fatalf("legacy table %s exists = %d, err %v", table, exists, err)
		}
	}
}

func TestRoutineDraftListAndRunContract(t *testing.T) {
	store := newTestStore(t)
	draft, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Draft review", Prompt: "Review the selected repositories later.", Runtime: protocol.RuntimeCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	if draft.RepositoryCount != 0 || len(draft.Repositories) != 0 {
		t.Fatalf("draft repositories = %#v", draft)
	}
	page, err := store.Routines(context.Background(), false, 10, "")
	if err != nil || len(page.Routines) != 1 {
		t.Fatalf("Routine page = %#v, err %v", page, err)
	}
	if page.Routines[0].Prompt != "" || page.Routines[0].PromptPreview == "" ||
		page.Routines[0].Repositories != nil || page.Routines[0].RepositoryCount != 0 {
		t.Fatalf("Routine list leaked detail or lost summary: %#v", page.Routines[0])
	}
	if _, _, err := store.RunRoutine(context.Background(), draft.ID, protocol.RunRoutineRequest{RequestKey: "draft-run"}); !serviceErrorCode(err, "routine_has_no_repositories") {
		t.Fatalf("draft Run error = %v", err)
	}
	_, err = store.UpdateRoutine(context.Background(), draft.ID, protocol.SaveRoutineRequest{
		Name: draft.Name, Prompt: draft.Prompt, Runtime: draft.Runtime,
		TimeoutSeconds: draft.TimeoutSeconds, ConcurrencyLimit: draft.ConcurrencyLimit,
		ExpectedGeneration: draft.Generation,
		Schedule:           protocol.RoutineSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if !serviceErrorCode(err, "routine_repository_required") {
		t.Fatalf("scheduled draft error = %v", err)
	}
}

func TestOverviewHandlesFreshInstallAndRedactsUpcomingRoutines(t *testing.T) {
	store := newTestStore(t)
	overview, err := store.Overview(context.Background())
	if err != nil || overview.WorkersOnline != 0 || overview.WorkersTotal != 0 {
		t.Fatalf("fresh Overview = %#v, err %v", overview, err)
	}
	if overview.RunMetrics.Window != "24h" || overview.RunMetrics.TotalRuns != 0 ||
		overview.RunMetrics.CompletionRate != nil || overview.RunMetrics.AverageQueueTimeSeconds != nil ||
		overview.RunMetrics.AverageCycleTimeSeconds != nil {
		t.Fatalf("fresh Overview run metrics = %#v", overview.RunMetrics)
	}
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	_, err = store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Private scheduled review", Prompt: "Do not expose this prompt on Overview.",
		Runtime:        protocol.RuntimeCodex,
		TimeoutSeconds: 3600, ConcurrencyLimit: 10,
		RepositoryIDs: []string{worker.Repositories[0].ID},
		Schedule:      protocol.RoutineSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	overview, err = store.Overview(context.Background())
	if err != nil || len(overview.UpcomingRoutines) != 1 {
		t.Fatalf("scheduled Overview = %#v, err %v", overview, err)
	}
	summary := overview.UpcomingRoutines[0]
	if summary.Prompt != "" || summary.Repositories != nil || summary.RepositoryCount != 1 {
		t.Fatalf("Overview leaked Routine detail: %#v", summary)
	}
}

func TestOverviewReportsRunPerformanceForWorkAdmittedInLastDay(t *testing.T) {
	store := newTestStore(t)
	base := time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC)
	now := base
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Measured review", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	measured, _, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{RequestKey: "measured-run"})
	if err != nil {
		t.Fatal(err)
	}
	now = base.Add(20 * time.Second)
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{RequestID: "measured-claim", LeaseToken: tokenA})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, err %v", claim, err)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	now = base.Add(30 * time.Second)
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "Retry this run.",
	}); err != nil {
		t.Fatal(err)
	}
	now = base.Add(35 * time.Second)
	if _, err := store.HeartbeatWorker(context.Background(), worker.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetryWorkTarget(context.Background(), measured.Work.ID, measured.Targets[0].ID); err != nil {
		t.Fatal(err)
	}
	now = base.Add(50 * time.Second)
	retryClaim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{RequestID: "retry-claim", LeaseToken: tokenA})
	if err != nil || retryClaim == nil {
		t.Fatalf("retry claim = %#v, err %v", retryClaim, err)
	}
	if _, err := store.StartAttempt(context.Background(), retryClaim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	now = base.Add(60 * time.Second)
	if _, err := store.CompleteAttempt(context.Background(), retryClaim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded", Result: "Done.",
	}); err != nil {
		t.Fatal(err)
	}
	now = base.Add(70 * time.Second)
	if _, _, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{RequestKey: "active-run"}); err != nil {
		t.Fatal(err)
	}
	now = base.Add(90 * time.Second)
	overview, err := store.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	metrics := overview.RunMetrics
	if metrics.Window != "24h" || metrics.TotalRuns != 2 || metrics.CompletedRuns != 1 {
		t.Fatalf("run metric counts = %#v", metrics)
	}
	if metrics.CompletionRate == nil || *metrics.CompletionRate != 0.5 {
		t.Fatalf("completion rate = %#v", metrics.CompletionRate)
	}
	if metrics.AverageQueueTimeSeconds == nil || *metrics.AverageQueueTimeSeconds != 20 {
		t.Fatalf("average queue time = %#v", metrics.AverageQueueTimeSeconds)
	}
	if metrics.AverageCycleTimeSeconds == nil || *metrics.AverageCycleTimeSeconds != 60 {
		t.Fatalf("average cycle time = %#v", metrics.AverageCycleTimeSeconds)
	}
}

func TestOverviewDoesNotFlagFailuresOlderThanOneDay(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 10, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Failing review", Prompt: "Fail this review.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{RequestKey: "failed-review"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{RequestID: "failed-claim", LeaseToken: tokenA})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, err %v", claim, err)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "expected failure",
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Hour)
	overview, err := store.Overview(context.Background())
	if err != nil || overview.NeedsAttention != 0 || overview.CompletedLast24H != 0 {
		t.Fatalf("stale failure Overview = %#v, err %v", overview, err)
	}
}

func TestEditingAndReenablingRoutinePreservesBlockedPendingOccurrence(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 10, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Blocked review", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
		Schedule:      protocol.RoutineSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, time.August, 10, 9, 1, 0, 0, time.UTC)
	_, due, _, found, err := store.claimDueRoutine(context.Background())
	if err != nil || !found {
		t.Fatalf("claim occurrence = due %v, found %v, err %v", due, found, err)
	}
	admissionErr := conflict("routine_repository_missing", "the frozen repository is unavailable")
	if err := store.finishRoutineOccurrence(context.Background(), routine.ID, due, false, admissionErr); err != nil {
		t.Fatal(err)
	}
	blocked, err := store.Routine(context.Background(), routine.ID)
	if err != nil || blocked.Schedule.HealthStatus != "blocked" || blocked.Schedule.HealthCode != "routine_repository_missing" {
		t.Fatalf("blocked Routine = %#v, err %v", blocked, err)
	}
	updated, err := store.UpdateRoutine(context.Background(), routine.ID, protocol.SaveRoutineRequest{
		Name: "Blocked review renamed", Prompt: routine.Prompt, Runtime: routine.Runtime,
		TimeoutSeconds: routine.TimeoutSeconds, ConcurrencyLimit: routine.ConcurrencyLimit,
		RepositoryIDs: []string{worker.Repositories[0].ID}, ExpectedGeneration: blocked.Generation,
		Schedule: protocol.RoutineSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Schedule.HealthStatus != "blocked" || updated.Schedule.HealthCode != "routine_repository_missing" ||
		updated.Schedule.PendingDueAt == nil || !updated.Schedule.PendingDueAt.Equal(due) {
		t.Fatalf("edited blocked Routine = %#v", updated)
	}
	paused, err := store.UpdateRoutine(context.Background(), routine.ID, protocol.SaveRoutineRequest{
		Name: updated.Name, Prompt: updated.Prompt, Runtime: updated.Runtime,
		TimeoutSeconds: updated.TimeoutSeconds, ConcurrencyLimit: updated.ConcurrencyLimit,
		RepositoryIDs: []string{worker.Repositories[0].ID}, ExpectedGeneration: updated.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if paused.Schedule.Enabled || paused.Schedule.HealthStatus != "disabled" ||
		paused.Schedule.HealthCode != "routine_repository_missing" || paused.Schedule.PendingDueAt == nil {
		t.Fatalf("paused blocked Routine = %#v", paused)
	}
	resumed, err := store.UpdateRoutine(context.Background(), routine.ID, protocol.SaveRoutineRequest{
		Name: paused.Name, Prompt: paused.Prompt, Runtime: paused.Runtime,
		TimeoutSeconds: paused.TimeoutSeconds, ConcurrencyLimit: paused.ConcurrencyLimit,
		RepositoryIDs: []string{worker.Repositories[0].ID}, ExpectedGeneration: paused.Generation,
		Schedule: protocol.RoutineSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Schedule.HealthStatus != "blocked" || resumed.Schedule.HealthCode != "routine_repository_missing" ||
		resumed.Schedule.PendingDueAt == nil || !resumed.Schedule.PendingDueAt.Equal(due) {
		t.Fatalf("resumed blocked Routine = %#v", resumed)
	}
	if _, err := store.DiscardRoutineOccurrence(context.Background(), routine.ID,
		protocol.DiscardRoutineOccurrenceRequest{PendingDueAt: due}); err != nil {
		t.Fatalf("discard resumed blocked occurrence: %v", err)
	}
}

func TestIncompatibleWorkerCannotReceiveOrClaimRoutineWork(t *testing.T) {
	store := newTestStore(t)
	_, err := store.RegisterWorker(context.Background(), "legacy-worker", protocol.WorkerRegistration{
		Name: "Legacy Worker", WorkerVersion: "legacy", Runtime: protocol.RuntimeCodex,
		RuntimeVersion: "codex-legacy", Capacity: 1, Health: "healthy",
	})
	if !serviceErrorCode(err, "worker_upgrade_required") {
		t.Fatalf("legacy registration error = %v", err)
	}
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE workers SET work_claim_protocol_version = 0 WHERE id = ?
	`, worker.ID); err != nil {
		t.Fatal(err)
	}
	routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Protocol fence", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, _, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{
		RequestKey: "protocol-fence",
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Targets[0].State != protocol.WorkTargetBlocked || detail.Targets[0].AssignedWorkerID != "" {
		t.Fatalf("Work routed to incompatible Worker = %#v", detail.Targets[0])
	}
	if _, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "legacy-claim", LeaseToken: tokenA,
	}); !serviceErrorCode(err, "worker_upgrade_required") {
		t.Fatalf("legacy claim error = %v", err)
	}
}

func TestClaimScansPastFiftyIncompatibleBlockedTargets(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 100, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	targetIDs := createQueuedRoutineTargets(t, store, worker, 51)
	if _, err := store.db.ExecContext(context.Background(), `DELETE FROM executions`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE work_targets
		SET state = 'blocked', blocked_reason = 'No compatible Worker.',
		    assigned_worker_id = NULL, required_runtime = ?
	`, protocol.RuntimeClaudeCode); err != nil {
		t.Fatal(err)
	}
	wantTarget := targetIDs[len(targetIDs)-1]
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE work_targets SET required_runtime = ? WHERE id = ?
	`, protocol.RuntimeCodex, wantTarget); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "paged-blocked-claim", LeaseToken: tokenA,
	})
	if err != nil || claim == nil || claim.Target.ID != wantTarget {
		t.Fatalf("claim after incompatible prefix = %#v, err %v, want target %s", claim, err, wantTarget)
	}
}

func TestClaimScansPastFiftyHealthyQueuedAssignmentsToReroute(t *testing.T) {
	store := newTestStore(t)
	repository := protocol.RepositoryRegistration{Key: "factory", RemoteIdentity: "github.com/owainlewis/factory"}
	claimingWorker := registerTestWorker(t, store, workerA, 100, repository)
	healthyWorker := registerTestWorker(t, store, "worker-b", 100, repository)
	offlineWorker := registerTestWorker(t, store, "worker-c", 100, repository)
	targetIDs := createQueuedRoutineTargets(t, store, claimingWorker, 51)
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE executions SET assigned_worker_id = ?
	`, healthyWorker.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE work_targets SET assigned_worker_id = ?
	`, healthyWorker.ID); err != nil {
		t.Fatal(err)
	}
	wantTarget := targetIDs[len(targetIDs)-1]
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE executions SET assigned_worker_id = ? WHERE work_target_id = ?
	`, offlineWorker.ID, wantTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE work_targets SET assigned_worker_id = ? WHERE id = ?
	`, offlineWorker.ID, wantTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE workers SET last_heartbeat = 0 WHERE id = ?
	`, offlineWorker.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), claimingWorker.ID, protocol.ClaimRequest{
		RequestID: "paged-reroute-claim", LeaseToken: tokenA,
	})
	if err != nil || claim == nil || claim.Target.ID != wantTarget {
		t.Fatalf("claim after healthy assignment prefix = %#v, err %v, want target %s", claim, err, wantTarget)
	}
}

func createQueuedRoutineTargets(t *testing.T, store *Store, worker protocol.Worker, count int) []string {
	t.Helper()
	routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Paged claim fixture", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < count; index++ {
		if _, _, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{
			RequestKey: fmt.Sprintf("paged-claim-%03d", index),
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.db.QueryContext(context.Background(), `
		SELECT id FROM work_targets ORDER BY admitted_at, id
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != count {
		t.Fatalf("created %d targets, want %d", len(ids), count)
	}
	return ids
}

func TestFrozenOccurrenceRechecksPausedRoutineBeforeAdmission(t *testing.T) {
	for _, test := range []struct {
		name      string
		pause     func(*Store, protocol.Routine, string) error
		errorCode string
	}{
		{
			name: "disabled",
			pause: func(store *Store, routine protocol.Routine, repositoryID string) error {
				_, err := store.UpdateRoutine(context.Background(), routine.ID, protocol.SaveRoutineRequest{
					Name: routine.Name, Prompt: routine.Prompt, Runtime: routine.Runtime,
					TimeoutSeconds: routine.TimeoutSeconds, ConcurrencyLimit: routine.ConcurrencyLimit,
					RepositoryIDs: []string{repositoryID}, ExpectedGeneration: routine.Generation,
				})
				return err
			},
			errorCode: "routine_schedule_disabled",
		},
		{
			name: "archived",
			pause: func(store *Store, routine protocol.Routine, _ string) error {
				_, err := store.SetRoutineArchived(context.Background(), routine.ID, protocol.SetRoutineArchivedRequest{
					Archived: boolPointer(true), ExpectedGeneration: routine.Generation,
				})
				return err
			},
			errorCode: "routine_archived",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			now := time.Date(2026, time.August, 10, 8, 0, 0, 0, time.UTC)
			store.now = func() time.Time { return now }
			worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
				Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
			})
			routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
				Name: "Race guard " + test.name, Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
				RepositoryIDs: []string{worker.Repositories[0].ID},
				Schedule:      protocol.RoutineSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
			})
			if err != nil {
				t.Fatal(err)
			}
			now = time.Date(2026, time.August, 10, 9, 1, 0, 0, time.UTC)
			_, due, snapshot, found, err := store.claimDueRoutine(context.Background())
			if err != nil || !found {
				t.Fatalf("claim occurrence = due %v, found %v, err %v", due, found, err)
			}
			if err := test.pause(store, routine, worker.Repositories[0].ID); err != nil {
				t.Fatal(err)
			}
			_, _, admissionErr := store.admitRoutine(context.Background(), routine.ID, "schedule",
				"schedule:race:"+test.name, &due, &snapshot)
			if !serviceErrorCode(admissionErr, test.errorCode) {
				t.Fatalf("paused occurrence admission error = %v", admissionErr)
			}
			if err := store.finishRoutineOccurrence(context.Background(), routine.ID, due, false, admissionErr); err != nil {
				t.Fatal(err)
			}
			page, err := store.WorkPage(context.Background(), 10, "")
			if err != nil || len(page.Work) != 0 {
				t.Fatalf("paused occurrence admitted Work = %#v, err %v", page, err)
			}
			paused, err := store.Routine(context.Background(), routine.ID)
			if err != nil || paused.Schedule.HealthStatus != "disabled" || paused.Schedule.PendingDueAt == nil {
				t.Fatalf("paused Routine = %#v, err %v", paused, err)
			}
		})
	}
}

func TestDisablingRoutinePausesFrozenOccurrenceUntilDiscard(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 10, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Paused review", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		TimeoutSeconds: 3600, ConcurrencyLimit: 10, RepositoryIDs: []string{worker.Repositories[0].ID},
		Schedule: protocol.RoutineSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, time.August, 10, 9, 1, 0, 0, time.UTC)
	_, due, snapshot, found, err := store.claimDueRoutine(context.Background())
	if err != nil || !found || snapshot.ScheduleCron != "0 9 * * *" || snapshot.ScheduleTimezone != "UTC" {
		t.Fatalf("frozen occurrence = due %v, snapshot %#v, found %v, err %v", due, snapshot, found, err)
	}
	paused, err := store.UpdateRoutine(context.Background(), routine.ID, protocol.SaveRoutineRequest{
		Name: routine.Name, Prompt: routine.Prompt, Runtime: routine.Runtime,
		TimeoutSeconds: routine.TimeoutSeconds, ConcurrencyLimit: routine.ConcurrencyLimit,
		RepositoryIDs: []string{worker.Repositories[0].ID}, ExpectedGeneration: routine.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if paused.Schedule.Enabled || paused.Schedule.PendingDueAt == nil || paused.Schedule.Cron != "0 9 * * *" ||
		paused.Schedule.HealthStatus != "disabled" {
		t.Fatalf("paused Routine = %#v", paused)
	}
	if err := store.AdmitDueRoutines(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	page, err := store.WorkPage(context.Background(), 10, "")
	if err != nil || len(page.Work) != 0 {
		t.Fatalf("paused occurrence admitted Work = %#v, err %v", page, err)
	}
	discarded, err := store.DiscardRoutineOccurrence(context.Background(), routine.ID,
		protocol.DiscardRoutineOccurrenceRequest{PendingDueAt: due})
	if err != nil || discarded.Schedule.PendingDueAt != nil {
		t.Fatalf("discard paused occurrence = %#v, err %v", discarded, err)
	}
	replayed, err := store.DiscardRoutineOccurrence(context.Background(), routine.ID,
		protocol.DiscardRoutineOccurrenceRequest{PendingDueAt: due})
	if err != nil || replayed.Schedule.PendingDueAt != nil || replayed.ID != discarded.ID {
		t.Fatalf("replayed discard = %#v, err %v", replayed, err)
	}
}
