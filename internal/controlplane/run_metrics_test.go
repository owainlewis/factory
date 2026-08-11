package controlplane

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestRunHealthMetricsUseOneFilteredJobCohort(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	repositoryA := createManagedTestRepository(t, store, "github.com/example/metrics-a")
	repositoryB := createManagedTestRepository(t, store, "github.com/example/metrics-b")
	repositoryBlocked := createManagedTestRepository(t, store, "github.com/example/metrics-blocked")
	definitionA := createTestDefinition(t, store, "metrics-definition-a", "Metrics A")
	definitionB := createTestDefinition(t, store, "metrics-definition-b", "Metrics B")
	worker, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: "Metrics Worker", WorkerVersion: "test", Runtime: protocol.RuntimeCodex, RuntimeVersion: "codex-test",
		Capabilities: []protocol.Capability{
			{Kind: protocol.CapabilityKindTool, Name: "git", Status: protocol.CapabilityReady},
			{Kind: protocol.CapabilityKindTool, Name: "gh", Status: protocol.CapabilityReady},
			{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimeCodex, Status: protocol.CapabilityReady},
		},
		Capacity: 10, Health: "healthy",
		Repositories: []protocol.RepositoryRegistration{
			{Key: "metrics-a", RemoteIdentity: repositoryA.RemoteIdentity},
			{Key: "metrics-b", RemoteIdentity: repositoryB.RemoteIdentity},
		},
		SourceAccess: []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	store.now = func() time.Time { return now.Add(-2 * time.Hour) }
	terminal, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "metrics-terminal", DefinitionID: definitionA.ID,
		RepositoryIDs: []string{repositoryA.ID, repositoryB.ID}, ConcurrencyLimit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range terminal.Jobs {
		if job.Job.RepositoryID == repositoryA.ID {
			seedRunMetricOutcome(t, store, job.Job.ExecutionID, worker.ID, "succeeded",
				now.Add(-90*time.Minute), now.Add(-30*time.Minute))
		} else {
			seedRunMetricOutcome(t, store, job.Job.ExecutionID, worker.ID, "failed",
				now.Add(-110*time.Minute), now.Add(-time.Hour))
		}
	}
	store.now = func() time.Time { return now.Add(-time.Hour) }
	if _, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "metrics-active-a", DefinitionID: definitionA.ID, RepositoryID: repositoryA.ID,
	}); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(-45 * time.Minute) }
	if _, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "metrics-active-b", DefinitionID: definitionB.ID, RepositoryID: repositoryA.ID,
	}); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(-30 * time.Minute) }
	if _, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "metrics-blocked", DefinitionID: definitionA.ID, RepositoryID: repositoryBlocked.ID,
	}); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now }

	summary, err := store.Metrics(context.Background(), metricsWindow24Hours)
	if err != nil {
		t.Fatal(err)
	}
	metrics := summary.RunHealth
	if metrics.TotalJobs != 5 || metrics.Active != 2 || metrics.Blocked != 1 ||
		metrics.Succeeded != 1 || metrics.Failed != 1 || metrics.Cancelled != 0 || metrics.Throughput != 2 {
		t.Fatalf("Run health counts = %#v", metrics)
	}
	requireRunMetric(t, "success rate", metrics.SuccessRate, 0.5)
	requireRunMetric(t, "average queue", metrics.AverageQueueTimeSeconds, 20*time.Minute.Seconds())
	requireRunMetric(t, "average cycle", metrics.AverageCycleTimeSeconds, 75*time.Minute.Seconds())
	if len(metrics.Jobs) != 3 || len(metrics.Definitions) != 2 || len(metrics.Repositories) != 3 ||
		len(metrics.Workers) != 1 || metrics.Workers[0].Name != "Metrics Worker" {
		t.Fatalf("Run health drill-down/options = %#v", metrics)
	}
	wantRecentAdmissions := []time.Time{
		now.Add(-30 * time.Minute),
		now.Add(-45 * time.Minute),
		now.Add(-time.Hour),
	}
	for index, want := range wantRecentAdmissions {
		if !metrics.Jobs[index].AdmittedAt.Equal(want) {
			t.Fatalf("recent Job %d admitted at %s, want %s", index, metrics.Jobs[index].AdmittedAt, want)
		}
	}

	definitionFiltered, err := store.MetricsFiltered(context.Background(), metricsWindow24Hours, MetricsFilter{
		DefinitionID: definitionA.ID,
	})
	if err != nil || definitionFiltered.RunHealth.TotalJobs != 4 ||
		definitionFiltered.RunHealth.Active != 1 || definitionFiltered.RunHealth.Blocked != 1 {
		t.Fatalf("Definition filter: err=%v metrics=%#v", err, definitionFiltered.RunHealth)
	}
	repositoryFiltered, err := store.MetricsFiltered(context.Background(), metricsWindow24Hours, MetricsFilter{
		RepositoryID: repositoryB.ID,
	})
	if err != nil || repositoryFiltered.RunHealth.TotalJobs != 1 || repositoryFiltered.RunHealth.Failed != 1 {
		t.Fatalf("repository filter: err=%v metrics=%#v", err, repositoryFiltered.RunHealth)
	}
	workerFiltered, err := store.MetricsFiltered(context.Background(), metricsWindow24Hours, MetricsFilter{
		WorkerID: worker.ID,
	})
	if err != nil || workerFiltered.RunHealth.TotalJobs != 4 || workerFiltered.RunHealth.Blocked != 0 {
		t.Fatalf("Worker filter: err=%v metrics=%#v", err, workerFiltered.RunHealth)
	}
	if len(workerFiltered.RunHealth.Definitions) != 2 || len(workerFiltered.RunHealth.Repositories) != 3 {
		t.Fatalf("filter options changed with cohort filter: %#v", workerFiltered.RunHealth)
	}

	updatedDefinition, changed, err := store.UpdateDefinition(context.Background(), definitionB.ID, protocol.UpdateDefinitionRequest{
		RequestKey: "metrics-definition-b-rename", ExpectedGeneration: definitionB.Generation,
		Name: "Metrics B Renamed", Prompt: definitionB.Prompt, Runtime: definitionB.Runtime,
		AllowedTools: definitionB.AllowedTools, TimeoutSeconds: definitionB.TimeoutSeconds, Inputs: definitionB.Inputs,
	})
	if err != nil || !changed || updatedDefinition.Name != "Metrics B Renamed" {
		t.Fatalf("rename Definition: changed=%t err=%v Definition=%#v", changed, err, updatedDefinition)
	}
	if _, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "metrics-active-b-renamed", DefinitionID: definitionB.ID, RepositoryID: repositoryA.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		UPDATE jobs SET repository_identity = ?
		WHERE id = (
			SELECT id FROM jobs WHERE repository_id = ?
			ORDER BY admitted_at, id LIMIT 1
		)
	`, "github.com/example/metrics-a-before-rename", repositoryA.ID); err != nil {
		t.Fatal(err)
	}
	renamedMetrics, err := store.Metrics(context.Background(), metricsWindow24Hours)
	if err != nil {
		t.Fatal(err)
	}
	definitionNames := map[string]string{}
	for _, option := range renamedMetrics.RunHealth.Definitions {
		definitionNames[option.ID] = option.Name
	}
	if len(renamedMetrics.RunHealth.Definitions) != 2 || definitionNames[definitionB.ID] != "Metrics B Renamed" {
		t.Fatalf("deduplicated Definition options = %#v", renamedMetrics.RunHealth.Definitions)
	}
	repositoryNames := map[string]string{}
	for _, option := range renamedMetrics.RunHealth.Repositories {
		repositoryNames[option.ID] = option.Name
	}
	if len(renamedMetrics.RunHealth.Repositories) != 3 || repositoryNames[repositoryA.ID] != repositoryA.RemoteIdentity {
		t.Fatalf("deduplicated repository options = %#v", renamedMetrics.RunHealth.Repositories)
	}
}

func seedRunMetricOutcome(
	t *testing.T,
	store *Store,
	executionID string,
	workerID string,
	state string,
	startedAt time.Time,
	completedAt time.Time,
) {
	t.Helper()
	if _, err := store.db.Exec(
		`UPDATE executions SET state = ?, assigned_worker_id = ?, updated_at = ? WHERE id = ?`,
		state, workerID, completedAt.UnixMilli(), executionID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO attempts(
			id, execution_id, worker_id, attempt_number, state, lease_digest,
			lease_expires_at, started_at, completed_at, created_at
		) VALUES (?, ?, ?, 1, ?, X'00', ?, ?, ?, ?)
	`, "attempt-"+executionID, executionID, workerID, state, completedAt.UnixMilli(),
		startedAt.UnixMilli(), completedAt.UnixMilli(),
		startedAt.UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func registerRunMetricWorker(t *testing.T, store *Store, repository protocol.ManagedRepository) protocol.Worker {
	t.Helper()
	worker, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: "Metrics Worker", WorkerVersion: "test", Runtime: protocol.RuntimeCodex,
		RuntimeVersion: "codex-test", Capacity: 10, Health: "healthy",
		Capabilities: []protocol.Capability{
			{Kind: protocol.CapabilityKindTool, Name: "git", Status: protocol.CapabilityReady},
			{Kind: protocol.CapabilityKindTool, Name: "gh", Status: protocol.CapabilityReady},
			{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimeCodex, Status: protocol.CapabilityReady},
		},
		Repositories: []protocol.RepositoryRegistration{{Key: "metrics", RemoteIdentity: repository.RemoteIdentity}},
		SourceAccess: []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func requireRunMetric(t *testing.T, name string, actual *float64, expected float64) {
	t.Helper()
	if actual == nil || math.Abs(*actual-expected) > 0.000001 {
		t.Fatalf("%s = %v, want %f", name, actual, expected)
	}
}

func TestRunMetricsIndexesExist(t *testing.T) {
	store := newTestStore(t)
	for _, name := range []string{"jobs_metrics_admitted", "runs_metrics_definition"} {
		var count int
		if err := store.db.QueryRow(`
			SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?
		`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("Run metrics index %q: count=%d err=%v", name, count, err)
		}
	}
}

func TestRunMetricsUseFinalExecutionTransitionAsTerminalTime(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	repository := createManagedTestRepository(t, store, "github.com/example/terminal-time")
	definition := createTestDefinition(t, store, "terminal-time-definition", "Terminal time")
	worker := registerRunMetricWorker(t, store, repository)
	store.now = func() time.Time { return now.Add(-2 * time.Hour) }
	run, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "terminal-time-run", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	job := run.Jobs[0].Job
	seedRunMetricOutcome(t, store, job.ExecutionID, worker.ID, "failed", now.Add(-90*time.Minute), now.Add(-time.Hour))
	if _, err := store.db.Exec(`UPDATE executions SET state = 'cancelled', updated_at = ? WHERE id = ?`, now.Add(-30*time.Minute).UnixMilli(), job.ExecutionID); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now }

	metrics, err := store.Metrics(context.Background(), metricsWindow24Hours)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics.RunHealth.Jobs) != 1 || metrics.RunHealth.Jobs[0].TerminalAt == nil ||
		!metrics.RunHealth.Jobs[0].TerminalAt.Equal(now.Add(-30*time.Minute)) {
		t.Fatalf("terminal Job = %#v", metrics.RunHealth.Jobs)
	}
	requireRunMetric(t, "average cycle", metrics.RunHealth.AverageCycleTimeSeconds, 90*time.Minute.Seconds())
}

func TestRunMetricsUseJobTransitionForBlockedCancellation(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	repository := createManagedTestRepository(t, store, "github.com/example/blocked-cancellation")
	definition := createTestDefinition(t, store, "blocked-cancellation-definition", "Blocked cancellation")
	store.now = func() time.Time { return now.Add(-2 * time.Hour) }
	run, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "blocked-cancellation-run", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelledAt := now.Add(-30 * time.Minute)
	store.now = func() time.Time { return cancelledAt }
	if _, err := store.CancelJob(context.Background(), run.Jobs[0].Job.ID); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now }

	metrics, err := store.Metrics(context.Background(), metricsWindow24Hours)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.RunHealth.Cancelled != 1 || len(metrics.RunHealth.Jobs) != 1 ||
		metrics.RunHealth.Jobs[0].TerminalAt == nil || !metrics.RunHealth.Jobs[0].TerminalAt.Equal(cancelledAt) {
		t.Fatalf("blocked cancellation metrics = %#v", metrics.RunHealth)
	}
	requireRunMetric(t, "average cycle", metrics.RunHealth.AverageCycleTimeSeconds, 90*time.Minute.Seconds())
}

func TestRunMetricsApplyJobViewBeforeDrillDownLimit(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	repository := createManagedTestRepository(t, store, "github.com/example/drill-down")
	definition := createTestDefinition(t, store, "drill-down-definition", "Drill down")
	worker := registerRunMetricWorker(t, store, repository)
	store.now = func() time.Time { return now.Add(-3 * time.Hour) }
	failed, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "old-failed-run", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	seedRunMetricOutcome(t, store, failed.Jobs[0].Job.ExecutionID, worker.ID, "failed", now.Add(-170*time.Minute), now.Add(-160*time.Minute))
	for index := 0; index < 101; index++ {
		store.now = func() time.Time { return now.Add(time.Duration(index-101) * time.Minute) }
		if _, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
			RequestKey: fmt.Sprintf("new-active-run-%03d", index), DefinitionID: definition.ID, RepositoryID: repository.ID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	store.now = func() time.Time { return now }

	metrics, err := store.MetricsFiltered(context.Background(), metricsWindow24Hours, MetricsFilter{JobView: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	if metrics.RunHealth.Failed != 1 || len(metrics.RunHealth.Jobs) != 1 || metrics.RunHealth.Jobs[0].State != "failed" {
		t.Fatalf("failed drill-down = %#v", metrics.RunHealth)
	}
}
