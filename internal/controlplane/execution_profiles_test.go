package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestExecutionProfileAndManualOverrideAPI(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	routine := createProfileRoutine(t, store, worker.Repositories[0].ID, "")
	handler := NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	doJSON := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequestWithContext(context.Background(), method, "http://localhost"+path, bytes.NewReader(encoded))
		request.Host = "localhost"
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	profileResponse := doJSON(http.MethodPost, "/api/v1/execution-profiles", protocol.SaveExecutionProfileRequest{
		Name: "API cloud", Kind: protocol.BackendFakeCloudRun, Runtime: protocol.RuntimeCodex,
		Provider: "openrouter", Model: "deepseek/test", TimeoutSeconds: 600,
		ResourceClass: "standard", MaxConcurrent: 2, Enabled: true, Healthy: true,
		FakeOutcome: "succeeded",
	})
	if profileResponse.Code != http.StatusCreated {
		t.Fatalf("create profile status %d: %s", profileResponse.Code, profileResponse.Body.String())
	}
	var profile protocol.ExecutionProfile
	if err := json.Unmarshal(profileResponse.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	runResponse := doJSON(http.MethodPost, "/api/v1/routines/"+routine.ID+"/run", protocol.RunRoutineRequest{
		RequestKey: "api-cloud-override", ExecutionProfileID: profile.ID,
	})
	if runResponse.Code != http.StatusCreated {
		t.Fatalf("run status %d: %s", runResponse.Code, runResponse.Body.String())
	}
	var work protocol.WorkDetail
	if err := json.Unmarshal(runResponse.Body.Bytes(), &work); err != nil {
		t.Fatal(err)
	}
	if work.Work.Execution.ProfileID != profile.ID || work.Targets[0].AssignedWorkerID != profile.SyntheticWorkerID {
		t.Fatalf("API override Work = %#v", work)
	}
}

func createFakeProfile(t *testing.T, store *Store, name, runtime, outcome string) protocol.ExecutionProfile {
	t.Helper()
	profile, err := store.CreateExecutionProfile(context.Background(), protocol.SaveExecutionProfileRequest{
		Name: name, Kind: protocol.BackendFakeCloudRun, Runtime: runtime,
		Provider: "openrouter", Model: "deepseek/test", TimeoutSeconds: 900,
		ResourceClass: "1cpu-2gib", MaxConcurrent: 4, Enabled: true, Healthy: true,
		FakeOutcome: outcome,
	})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func createProfileRoutine(t *testing.T, store *Store, repositoryID, profileID string) protocol.Routine {
	t.Helper()
	routine, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Profile routing", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		TimeoutSeconds: 3600, ConcurrencyLimit: 2, RepositoryIDs: []string{repositoryID},
		ExecutionProfileID: profileID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return routine
}

func TestExecutionProfileManualOverrideUsesExistingLifecycle(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile := createFakeProfile(t, store, "Cloud burst", protocol.RuntimeCodex, "succeeded")
	routine := createProfileRoutine(t, store, worker.Repositories[0].ID, "")

	persistent, _, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{RequestKey: "persistent-default"})
	if err != nil {
		t.Fatal(err)
	}
	if persistent.Work.Execution.ProfileID != protocol.PersistentAutoProfileID ||
		persistent.Targets[0].AssignedWorkerID != worker.ID || persistent.Work.State != protocol.WorkQueued {
		t.Fatalf("persistent default = %#v", persistent)
	}

	cloud, created, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{
		RequestKey: "cloud-override", ExecutionProfileID: profile.ID,
	})
	if err != nil || !created {
		t.Fatalf("cloud admission = %#v, created %v, err %v", cloud, created, err)
	}
	if cloud.Work.Execution.ProfileID != profile.ID || cloud.Work.Execution.ProfileVersion != 1 ||
		cloud.Work.Execution.Backend != protocol.BackendFakeCloudRun ||
		cloud.Targets[0].AssignedWorkerID != profile.SyntheticWorkerID || cloud.Work.State != protocol.WorkQueued {
		t.Fatalf("cloud override = %#v", cloud)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	cloud, err = store.Work(context.Background(), cloud.Work.ID)
	if err != nil || cloud.Work.State != protocol.WorkSucceeded || len(cloud.Targets[0].Attempts) != 1 ||
		cloud.Targets[0].Attempts[0].WorkerID != profile.SyntheticWorkerID {
		t.Fatalf("completed cloud lifecycle = %#v, err %v", cloud, err)
	}

	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{RequestID: "persistent-claim", LeaseToken: tokenA})
	if err != nil || claim == nil || claim.Target.WorkID != persistent.Work.ID {
		t.Fatalf("persistent claim after cloud run = %#v, err %v", claim, err)
	}
}

func TestExecutionProfileRunReplayIncludesManualOverride(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile := createFakeProfile(t, store, "Replay cloud", protocol.RuntimeCodex, "succeeded")
	routine := createProfileRoutine(t, store, worker.Repositories[0].ID, "")
	input := protocol.RunRoutineRequest{RequestKey: "profile-replay", ExecutionProfileID: profile.ID}
	created, wasCreated, err := store.RunRoutine(context.Background(), routine.ID, input)
	if err != nil || !wasCreated {
		t.Fatalf("create = %#v, %v, %v", created, wasCreated, err)
	}
	replayed, wasCreated, err := store.RunRoutine(context.Background(), routine.ID, input)
	if err != nil || wasCreated || replayed.Work.ID != created.Work.ID {
		t.Fatalf("replay = %#v, %v, %v", replayed, wasCreated, err)
	}
	if _, _, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{
		RequestKey: "profile-replay", ExecutionProfileID: protocol.PersistentAutoProfileID,
	}); !serviceErrorCode(err, "request_key_conflict") {
		t.Fatalf("changed override replay error = %v", err)
	}
	persistent, wasCreated, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{
		RequestKey: "persistent-replay",
	})
	if err != nil || !wasCreated {
		t.Fatalf("persistent create = %#v, %v, %v", persistent, wasCreated, err)
	}
	replayed, wasCreated, err = store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{
		RequestKey: "persistent-replay", ExecutionProfileID: protocol.PersistentAutoProfileID,
	})
	if err != nil || wasCreated || replayed.Work.ID != persistent.Work.ID {
		t.Fatalf("persistent sentinel replay = %#v, %v, %v", replayed, wasCreated, err)
	}
}

func TestFakeCloudRetryReusesFrozenProfileVersion(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile := createFakeProfile(t, store, "Immutable cloud", protocol.RuntimeCodex, "failed")
	routine := createProfileRoutine(t, store, worker.Repositories[0].ID, profile.ID)
	work, _, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{RequestKey: "frozen-profile"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	work, _ = store.Work(context.Background(), work.Work.ID)
	if work.Work.State != protocol.WorkFailed || len(work.Targets[0].Attempts) != 1 {
		t.Fatalf("first failed Attempt = %#v", work)
	}
	if processed, err := store.DispatchFakeCloud(context.Background(), 10); err != nil || processed != 0 {
		t.Fatalf("native retry occurred: processed %d, err %v", processed, err)
	}

	disabled, err := store.UpdateExecutionProfile(context.Background(), profile.ID, protocol.SaveExecutionProfileRequest{
		Name: profile.Name, Kind: profile.Kind, Runtime: profile.Runtime, Provider: profile.Provider,
		Model: profile.Model, TimeoutSeconds: profile.TimeoutSeconds, ResourceClass: profile.ResourceClass,
		MaxConcurrent: profile.MaxConcurrent, Enabled: false, Healthy: true, FakeOutcome: profile.FakeOutcome,
		ExpectedVersion: profile.Version,
	})
	if err != nil || disabled.Version != 2 {
		t.Fatalf("disabled profile update = %#v, err %v", disabled, err)
	}
	if _, err := store.RetryWorkTarget(context.Background(), work.Work.ID, work.Targets[0].ID); !serviceErrorCode(err, "execution_profile_version_unavailable") {
		t.Fatalf("retry with disabled profile error = %v", err)
	}
	updated, err := store.UpdateExecutionProfile(context.Background(), profile.ID, protocol.SaveExecutionProfileRequest{
		Name: profile.Name, Kind: profile.Kind, Runtime: profile.Runtime, Provider: profile.Provider,
		Model: "deepseek/new", TimeoutSeconds: profile.TimeoutSeconds, ResourceClass: profile.ResourceClass,
		MaxConcurrent: profile.MaxConcurrent, Enabled: true, Healthy: true, FakeOutcome: "succeeded",
		ExpectedVersion: disabled.Version,
	})
	if err != nil || updated.Version != 3 {
		t.Fatalf("profile update = %#v, err %v", updated, err)
	}
	if _, err := store.RetryWorkTarget(context.Background(), work.Work.ID, work.Targets[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	work, _ = store.Work(context.Background(), work.Work.ID)
	if work.Work.Execution.ProfileVersion != 1 || work.Work.Execution.Model != profile.Model ||
		work.Work.State != protocol.WorkFailed || len(work.Targets[0].Attempts) != 2 {
		t.Fatalf("retry did not reuse frozen version = %#v", work)
	}

	newWork, _, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{RequestKey: "new-profile-version"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	newWork, _ = store.Work(context.Background(), newWork.Work.ID)
	if newWork.Work.Execution.ProfileVersion != 3 || newWork.Work.Execution.Model != "deepseek/new" ||
		newWork.Work.State != protocol.WorkSucceeded {
		t.Fatalf("new Work did not freeze new version = %#v", newWork)
	}
}

func TestFakeCloudDoesNotStartQueuedWorkWhileProfileIsUnready(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile := createFakeProfile(t, store, "Dispatch health", protocol.RuntimeCodex, "succeeded")
	routine := createProfileRoutine(t, store, worker.Repositories[0].ID, profile.ID)
	work, _, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{RequestKey: "dispatch-health"})
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := store.UpdateExecutionProfile(context.Background(), profile.ID, protocol.SaveExecutionProfileRequest{
		Name: profile.Name, Kind: profile.Kind, Runtime: profile.Runtime, Provider: profile.Provider,
		Model: profile.Model, TimeoutSeconds: profile.TimeoutSeconds, ResourceClass: profile.ResourceClass,
		MaxConcurrent: profile.MaxConcurrent, Enabled: false, Healthy: true, FakeOutcome: profile.FakeOutcome,
		ExpectedVersion: profile.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := store.DispatchFakeCloud(context.Background(), 10); err != nil || processed != 0 {
		t.Fatalf("disabled dispatch = %d, err %v", processed, err)
	}
	work, _ = store.Work(context.Background(), work.Work.ID)
	if work.Work.State != protocol.WorkQueued || len(work.Targets[0].Attempts) != 0 {
		t.Fatalf("Work started while profile disabled = %#v", work)
	}
	if _, err := store.UpdateExecutionProfile(context.Background(), profile.ID, protocol.SaveExecutionProfileRequest{
		Name: profile.Name, Kind: profile.Kind, Runtime: profile.Runtime, Provider: profile.Provider,
		Model: profile.Model, TimeoutSeconds: profile.TimeoutSeconds, ResourceClass: profile.ResourceClass,
		MaxConcurrent: profile.MaxConcurrent, Enabled: true, Healthy: true, FakeOutcome: profile.FakeOutcome,
		ExpectedVersion: disabled.Version,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	work, _ = store.Work(context.Background(), work.Work.ID)
	if work.Work.State != protocol.WorkSucceeded || work.Work.Execution.ProfileVersion != 1 ||
		len(work.Targets[0].Attempts) != 1 {
		t.Fatalf("re-enabled frozen Work = %#v", work)
	}
}

func TestFakeCloudCancellationIsFactoryOwned(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile := createFakeProfile(t, store, "Cancellable cloud", protocol.RuntimeCodex, "running")
	routine := createProfileRoutine(t, store, worker.Repositories[0].ID, profile.ID)
	work, _, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{RequestKey: "cancel-cloud"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	work, _ = store.Work(context.Background(), work.Work.ID)
	if work.Work.State != protocol.WorkRunning || work.Targets[0].Attempts[0].State != "running" {
		t.Fatalf("running fake Attempt = %#v", work)
	}
	if _, err := store.CancelWork(context.Background(), work.Work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	work, _ = store.Work(context.Background(), work.Work.ID)
	if work.Work.State != protocol.WorkCancelled || work.Targets[0].Attempts[0].State != "cancelled" {
		t.Fatalf("cancelled fake Attempt = %#v", work)
	}
}

func TestFakeCloudRunningAttemptHonorsFrozenTimeout(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile, err := store.CreateExecutionProfile(context.Background(), protocol.SaveExecutionProfileRequest{
		Name: "Timed cloud", Kind: protocol.BackendFakeCloudRun, Runtime: protocol.RuntimeCodex,
		Provider: "openrouter", Model: "deepseek/test", TimeoutSeconds: 1,
		ResourceClass: "1cpu-2gib", MaxConcurrent: 1, Enabled: true, Healthy: true,
		FakeOutcome: "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	routine := createProfileRoutine(t, store, worker.Repositories[0].ID, profile.ID)
	work, _, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{RequestKey: "timeout-cloud"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	work, _ = store.Work(context.Background(), work.Work.ID)
	if work.Work.State != protocol.WorkRunning || work.Targets[0].TimeoutSeconds != 1 {
		t.Fatalf("running fake Attempt = %#v", work)
	}

	now = now.Add(2 * time.Second)
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	work, _ = store.Work(context.Background(), work.Work.ID)
	if work.Work.State != protocol.WorkFailed || work.Targets[0].FailureReason != fakeCloudTimeoutReason ||
		len(work.Targets[0].Attempts) != 1 || work.Targets[0].Attempts[0].State != "failed" {
		t.Fatalf("timed-out fake Attempt = %#v", work)
	}
}

func TestUnhealthyAndIncompatibleProfilesBlockWithoutAttempt(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	unhealthy, err := store.CreateExecutionProfile(context.Background(), protocol.SaveExecutionProfileRequest{
		Name: "Unhealthy cloud", Kind: protocol.BackendFakeCloudRun, Runtime: protocol.RuntimeCodex,
		Provider: "openrouter", Model: "test", Enabled: true, Healthy: false, HealthReason: "model secret missing",
	})
	if err != nil {
		t.Fatal(err)
	}
	incompatible := createFakeProfile(t, store, "Pi only cloud", protocol.RuntimePi, "succeeded")
	routine := createProfileRoutine(t, store, worker.Repositories[0].ID, "")
	for _, test := range []struct {
		key, profile, reason string
	}{
		{key: "unhealthy", profile: unhealthy.ID, reason: "model secret missing"},
		{key: "incompatible", profile: incompatible.ID, reason: "does not support runtime codex"},
	} {
		work, _, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{
			RequestKey: test.key, ExecutionProfileID: test.profile,
		})
		if err != nil {
			t.Fatal(err)
		}
		if work.Work.State != protocol.WorkBlocked || len(work.Targets[0].Attempts) != 0 ||
			work.Targets[0].AssignedWorkerID != "" || !strings.Contains(work.Targets[0].BlockedReason, test.reason) {
			t.Fatalf("blocked profile Work = %#v", work)
		}
	}
}

func TestFakeCloudProfileBlockedWorkRecoversWhenProfileIsReady(t *testing.T) {
	for _, test := range []struct {
		name          string
		enabled       bool
		healthy       bool
		healthReason  string
		blockedReason string
	}{
		{name: "disabled", enabled: false, healthy: true, blockedReason: "is disabled"},
		{name: "unhealthy", enabled: true, healthy: false, healthReason: "model secret missing", blockedReason: "model secret missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
				Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
			})
			profile, err := store.CreateExecutionProfile(context.Background(), protocol.SaveExecutionProfileRequest{
				Name: "Recovering cloud", Kind: protocol.BackendFakeCloudRun, Runtime: protocol.RuntimeCodex,
				Provider: "openrouter", Model: "deepseek/frozen", TimeoutSeconds: 900,
				ResourceClass: "1cpu-2gib", MaxConcurrent: 1, Enabled: test.enabled, Healthy: test.healthy,
				HealthReason: test.healthReason, FakeOutcome: "succeeded", FakeResult: "frozen result",
			})
			if err != nil {
				t.Fatal(err)
			}
			routine := createProfileRoutine(t, store, worker.Repositories[0].ID, profile.ID)
			work, _, err := store.RunRoutine(context.Background(), routine.ID, protocol.RunRoutineRequest{RequestKey: "recover-cloud"})
			if err != nil {
				t.Fatal(err)
			}
			if work.Work.State != protocol.WorkBlocked || len(work.Targets[0].Attempts) != 0 ||
				!strings.Contains(work.Targets[0].BlockedReason, test.blockedReason) {
				t.Fatalf("profile-blocked Work = %#v", work)
			}

			updated, err := store.UpdateExecutionProfile(context.Background(), profile.ID, protocol.SaveExecutionProfileRequest{
				Name: profile.Name, Kind: profile.Kind, Runtime: profile.Runtime, Provider: profile.Provider,
				Model: "deepseek/current", TimeoutSeconds: profile.TimeoutSeconds, ResourceClass: profile.ResourceClass,
				MaxConcurrent: profile.MaxConcurrent, Enabled: true, Healthy: true, FakeOutcome: "failed",
				FakeError: "new version result", ExpectedVersion: profile.Version,
			})
			if err != nil || updated.Version != 2 {
				t.Fatalf("profile recovery = %#v, err %v", updated, err)
			}
			if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
				t.Fatal(err)
			}
			work, _ = store.Work(context.Background(), work.Work.ID)
			if work.Work.State != protocol.WorkSucceeded || work.Work.Execution.ProfileVersion != 1 ||
				work.Work.Execution.Model != "deepseek/frozen" || work.Targets[0].Result != "frozen result" ||
				len(work.Targets[0].Attempts) != 1 {
				t.Fatalf("recovered frozen Work = %#v", work)
			}
		})
	}
}

func TestScheduledWorkUsesSavedExecutionProfile(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 15, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile := createFakeProfile(t, store, "Scheduled cloud", protocol.RuntimeCodex, "succeeded")
	_, err := store.CreateRoutine(context.Background(), protocol.SaveRoutineRequest{
		Name: "Scheduled profile", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID}, ExecutionProfileID: profile.ID,
		Schedule: protocol.RoutineSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if err := store.AdmitDueRoutines(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	page, err := store.WorkPage(context.Background(), 10, "")
	if err != nil || len(page.Work) != 1 || page.Work[0].Source != "schedule" ||
		page.Work[0].Execution.ProfileID != profile.ID {
		t.Fatalf("scheduled profile Work = %#v, err %v", page, err)
	}
}

func TestSyntheticWorkerCannotUseWorkerRoutes(t *testing.T) {
	store := newTestStore(t)
	profile := createFakeProfile(t, store, "Isolated cloud", protocol.RuntimeCodex, "succeeded")
	worker, err := store.Worker(context.Background(), profile.SyntheticWorkerID)
	if err != nil || !worker.Synthetic || worker.ID != profile.SyntheticWorkerID {
		t.Fatalf("synthetic Worker = %#v, err %v", worker, err)
	}
	if _, err := store.CreateWorkerEnrollment(context.Background(), worker.ID); !serviceErrorCode(err, "synthetic_worker_isolated") {
		t.Fatalf("synthetic enrollment error = %v", err)
	}
	if _, err := store.RegisterWorker(context.Background(), worker.ID, protocol.WorkerRegistration{
		Name: "spoof", WorkerVersion: "test", WorkClaimProtocolVersion: protocol.WorkClaimProtocolVersion,
		Runtime: protocol.RuntimeCodex, RuntimeVersion: "test", Capacity: 1, Health: "healthy",
	}); !serviceErrorCode(err, "synthetic_worker_isolated") {
		t.Fatalf("synthetic registration error = %v", err)
	}
	if _, err := store.HeartbeatWorker(context.Background(), worker.ID); !serviceErrorCode(err, "synthetic_worker_isolated") {
		t.Fatalf("synthetic heartbeat error = %v", err)
	}
	if _, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "synthetic-claim", LeaseToken: tokenA,
	}); !serviceErrorCode(err, "synthetic_worker_isolated") {
		t.Fatalf("synthetic claim error = %v", err)
	}
}

func TestOverviewUsesSyntheticWorkerHealthForOnlineState(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	profile := createFakeProfile(t, store, "Overview cloud", protocol.RuntimeCodex, "succeeded")
	now = now.Add(protocol.WorkerOnlineWindow + time.Second)
	worker, err := store.Worker(context.Background(), profile.SyntheticWorkerID)
	if err != nil || !worker.Online {
		t.Fatalf("healthy synthetic Worker = %#v, err %v", worker, err)
	}
	overview, err := store.Overview(context.Background())
	if err != nil || overview.WorkersTotal != 1 || overview.WorkersOnline != 1 {
		t.Fatalf("healthy synthetic Overview = %#v, err %v", overview, err)
	}

	if _, err := store.UpdateExecutionProfile(context.Background(), profile.ID, protocol.SaveExecutionProfileRequest{
		Name: profile.Name, Kind: profile.Kind, Runtime: profile.Runtime, Provider: profile.Provider,
		Model: profile.Model, TimeoutSeconds: profile.TimeoutSeconds, ResourceClass: profile.ResourceClass,
		MaxConcurrent: profile.MaxConcurrent, Enabled: true, Healthy: false,
		HealthReason: "validation failed", FakeOutcome: profile.FakeOutcome, ExpectedVersion: profile.Version,
	}); err != nil {
		t.Fatal(err)
	}
	overview, err = store.Overview(context.Background())
	if err != nil || overview.WorkersTotal != 1 || overview.WorkersOnline != 0 {
		t.Fatalf("unhealthy synthetic Overview = %#v, err %v", overview, err)
	}
	worker, err = store.Worker(context.Background(), profile.SyntheticWorkerID)
	if err != nil || worker.Online {
		t.Fatalf("unhealthy synthetic Worker = %#v, err %v", worker, err)
	}
}
