package controlplane

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func setupRunTest(
	t *testing.T,
	withWorker bool,
) (*Store, protocol.Definition, protocol.ManagedRepository, *protocol.Worker) {
	t.Helper()
	store := newTestStore(t)
	repository := createManagedTestRepository(t, store, "github.com/example/run-target")
	definition := createTestDefinition(t, store, "run-definition", "Find Bugs")
	if !withWorker {
		return store, definition, repository, nil
	}
	worker := registerDefinitionWorker(
		t, store, workerA,
		protocol.RepositoryRegistration{Key: "run-target", RemoteIdentity: repository.RemoteIdentity},
		protocol.CapabilityReady,
		[]protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	)
	return store, definition, repository, &worker
}

func TestRunAdmissionSnapshotsOneDefinitionAndOneRepositoryAtomically(t *testing.T) {
	store, definition, repository, worker := setupRunTest(t, true)
	detail, created, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "manual-run-one", DefinitionID: definition.ID, RepositoryID: repository.ID,
		Parameters: map[string]string{"severity": "critical"},
	})
	if err != nil || !created {
		t.Fatalf("create Run: created=%t err=%v", created, err)
	}
	if detail.Run.State != "queued" || detail.Run.SourceKind != "manual" || len(detail.Jobs) != 1 {
		t.Fatalf("created Run = %#v", detail)
	}
	job := detail.Jobs[0].Job
	if job.RepositoryID != repository.ID || job.AssignedWorkerID != worker.ID ||
		job.RequiredRuntime != protocol.RuntimeCodex || job.TaskID == "" || job.ExecutionID == "" {
		t.Fatalf("created Job = %#v", job)
	}
	claim := claimTestTask(t, store, worker.ID, "manual-run-one-claim", tokenA)
	if claim.RunID != detail.Run.ID || claim.JobID != job.ID {
		t.Fatalf("Run claim identities = run %q job %q; want run %q job %q",
			claim.RunID, claim.JobID, detail.Run.ID, job.ID)
	}
	wantPrompt, err := protocol.ResolveDefinitionPrompt(definition.Prompt, map[string]string{"severity": "critical"})
	if err != nil || claim.Task.Description != wantPrompt || detail.Jobs[0].ResolvedPrompt != wantPrompt {
		t.Fatalf("resolved Run prompt = claim %q detail %q, err=%v; want %q",
			claim.Task.Description, detail.Jobs[0].ResolvedPrompt, err, wantPrompt)
	}
	if detail.Run.Definition.Generation != definition.Generation ||
		detail.Run.Definition.Prompt != definition.Prompt || detail.Parameters["severity"] != "critical" {
		t.Fatalf("frozen Run inputs = definition %#v parameters %#v", detail.Run.Definition, detail.Parameters)
	}

	replayed, replayCreated, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "manual-run-one", DefinitionID: definition.ID, RepositoryID: repository.ID,
		Parameters: map[string]string{"severity": "critical"},
	})
	if err != nil || replayCreated || replayed.Run.ID != detail.Run.ID || replayed.Jobs[0].Job.ID != job.ID {
		t.Fatalf("Run replay: created=%t err=%v detail=%#v", replayCreated, err, replayed)
	}
	_, _, err = store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "manual-run-one", DefinitionID: definition.ID, RepositoryID: repository.ID,
		Parameters: map[string]string{"severity": "low"},
	})
	assertErrorCode(t, err, "request_key_conflict")

	updated, changed, err := store.UpdateDefinition(context.Background(), definition.ID, protocol.UpdateDefinitionRequest{
		RequestKey: "run-definition-update", ExpectedGeneration: definition.Generation,
		Name: "Find New Bugs", Prompt: "Use a changed prompt.", Runtime: protocol.RuntimePi,
		AllowedTools: []string{"git"}, TimeoutSeconds: 60, Inputs: map[string]string{"severity": "low"},
	})
	if err != nil || !changed || updated.Generation == definition.Generation {
		t.Fatalf("update Definition: changed=%t err=%v Definition=%#v", changed, err, updated)
	}
	frozen, err := store.Run(context.Background(), detail.Run.ID)
	if err != nil || frozen.Run.Definition.Generation != definition.Generation || frozen.Run.Definition.Prompt != definition.Prompt {
		t.Fatalf("Run snapshot changed: err=%v Run=%#v", err, frozen.Run)
	}
}

func TestRunOnceCanUseARepositoryConfiguredOnALocalRunner(t *testing.T) {
	store := newTestStore(t)
	definition := createTestDefinition(t, store, "local-run-definition", "Review Local Checkout")
	worker := registerDefinitionWorker(
		t, store, workerA,
		protocol.RepositoryRegistration{Key: "local-checkout", RemoteIdentity: "file:///tmp/factory-local-checkout"},
		protocol.CapabilityReady,
		nil,
	)
	if len(worker.Repositories) != 1 {
		t.Fatalf("local Runner repositories = %#v", worker.Repositories)
	}
	repositories, err := store.RunRepositories(context.Background())
	if err != nil || len(repositories) != 1 || repositories[0].ID != worker.Repositories[0].ID {
		t.Fatalf("Run repository choices: err=%v repositories=%#v", err, repositories)
	}
	detail, created, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "local-run-once", DefinitionID: definition.ID, RepositoryID: worker.Repositories[0].ID,
	})
	if err != nil || !created || detail.Run.State != "queued" || len(detail.Jobs) != 1 {
		t.Fatalf("create local Run: created=%t err=%v detail=%#v", created, err, detail)
	}
	if detail.Jobs[0].Job.AssignedWorkerID != worker.ID ||
		detail.Jobs[0].Job.RepositoryRemoteIdentity != "file:///tmp/factory-local-checkout" {
		t.Fatalf("local Run Job = %#v", detail.Jobs[0].Job)
	}
}

func TestRunOnceRoutesAStaticRepositoryOnlyToItsAdvertisingRunner(t *testing.T) {
	store := newTestStore(t)
	definition := createTestDefinition(t, store, "static-route-definition", "Review Static Checkout")
	staticWorker := registerDefinitionWorker(
		t, store, workerB,
		protocol.RepositoryRegistration{Key: "local-checkout", RemoteIdentity: "file:///tmp/factory-static-route"},
		protocol.CapabilityReady,
		nil,
	)
	repository := staticWorker.Repositories[0]
	if _, err := store.db.Exec(`UPDATE repositories SET enabled = 1 WHERE id = ?`, repository.ID); err != nil {
		t.Fatal(err)
	}
	_, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: workerA, WorkerVersion: "test", Runtime: protocol.RuntimeCodex, RuntimeVersion: "codex-test",
		Capabilities: []protocol.Capability{
			{Kind: protocol.CapabilityKindTool, Name: "git", Status: protocol.CapabilityReady},
			{Kind: protocol.CapabilityKindTool, Name: "gh", Status: protocol.CapabilityReady},
			{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimeCodex, Status: protocol.CapabilityReady},
		},
		Capacity: 1, Health: "healthy", AcceptsManagedRepositories: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	detail, created, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "static-route-run", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if err != nil || !created {
		t.Fatalf("create static Run: created=%t err=%v", created, err)
	}
	if got := detail.Jobs[0].Job.AssignedWorkerID; got != staticWorker.ID {
		t.Fatalf("assigned worker = %q; want advertising worker %q", got, staticWorker.ID)
	}
}

func TestRunOnceTreatsAMalformedGitHubIdentityAsAnAdvertisedStaticRepository(t *testing.T) {
	store := newTestStore(t)
	definition := createTestDefinition(t, store, "malformed-static-definition", "Review Static Checkout")
	staticWorker := registerDefinitionWorker(
		t, store, workerB,
		protocol.RepositoryRegistration{Key: "local-checkout", RemoteIdentity: "github.com/team"},
		protocol.CapabilityReady,
		nil,
	)
	repository := staticWorker.Repositories[0]
	if _, err := store.db.Exec(`UPDATE repositories SET enabled = 1 WHERE id = ?`, repository.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: workerA, WorkerVersion: "test", Runtime: protocol.RuntimeCodex, RuntimeVersion: "codex-test",
		Capabilities: []protocol.Capability{
			{Kind: protocol.CapabilityKindTool, Name: "git", Status: protocol.CapabilityReady},
			{Kind: protocol.CapabilityKindTool, Name: "gh", Status: protocol.CapabilityReady},
			{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimeCodex, Status: protocol.CapabilityReady},
		},
		Capacity: 1, Health: "healthy", AcceptsManagedRepositories: true,
		SourceAccess: []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	}); err != nil {
		t.Fatal(err)
	}

	detail, created, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "malformed-static-run", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if err != nil || !created {
		t.Fatalf("create malformed static Run: created=%t err=%v", created, err)
	}
	if got := detail.Jobs[0].Job.AssignedWorkerID; got != staticWorker.ID {
		t.Fatalf("assigned worker = %q; want advertising worker %q", got, staticWorker.ID)
	}
}

func TestRunOnceExcludesAnEnabledStaticRepositoryAfterItsRunnerStopsAdvertising(t *testing.T) {
	store := newTestStore(t)
	definition := createTestDefinition(t, store, "unavailable-static-definition", "Review Static Checkout")
	worker := registerDefinitionWorker(
		t, store, workerB,
		protocol.RepositoryRegistration{Key: "local-checkout", RemoteIdentity: "file:///tmp/factory-unavailable-static"},
		protocol.CapabilityReady,
		nil,
	)
	repository := worker.Repositories[0]
	if _, err := store.db.Exec(`UPDATE repositories SET enabled = 1 WHERE id = ?`, repository.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterWorker(context.Background(), workerB, protocol.WorkerRegistration{
		Name: workerB, WorkerVersion: "test", Runtime: protocol.RuntimeCodex, RuntimeVersion: "codex-test",
		Capabilities: []protocol.Capability{
			{Kind: protocol.CapabilityKindTool, Name: "git", Status: protocol.CapabilityReady},
			{Kind: protocol.CapabilityKindTool, Name: "gh", Status: protocol.CapabilityReady},
			{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimeCodex, Status: protocol.CapabilityReady},
		},
		Capacity: 1, Health: "healthy",
	}); err != nil {
		t.Fatal(err)
	}

	repositories, err := store.RunRepositories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range repositories {
		if option.ID == repository.ID {
			t.Fatalf("unadvertised static repository remained selectable: %#v", option)
		}
	}
	_, created, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "unavailable-static-run", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if created {
		t.Fatal("unadvertised static repository created a Run")
	}
	assertErrorCode(t, err, "repository_not_available")
}

func TestRunOnceRejectsADisabledManagedRepositoryEvenWhenAdvertised(t *testing.T) {
	store, definition, repository, _ := setupRunTest(t, false)
	blocked, created, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "disabled-managed-blocked", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if err != nil || !created || blocked.Run.State != "blocked" {
		t.Fatalf("create blocked Run: created=%t err=%v detail=%#v", created, err, blocked)
	}
	if _, err := store.SetManagedRepositoryEnabled(context.Background(), repository.ID, false); err != nil {
		t.Fatal(err)
	}
	registerDefinitionWorker(
		t, store, workerA,
		protocol.RepositoryRegistration{Key: "run-target", RemoteIdentity: repository.RemoteIdentity},
		protocol.CapabilityReady,
		[]protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	)

	repositories, err := store.RunRepositories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range repositories {
		if option.ID == repository.ID {
			t.Fatalf("disabled managed repository remained selectable: %#v", option)
		}
	}
	preserved, err := store.Run(context.Background(), blocked.Run.ID)
	if err != nil || preserved.Run.State != "blocked" || preserved.Jobs[0].Job.TaskID != "" {
		t.Fatalf("disabled managed repository materialized blocked Run: err=%v detail=%#v", err, preserved)
	}
	_, created, err = store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "disabled-managed-new", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if created {
		t.Fatal("disabled managed repository created a new Run")
	}
	assertErrorCode(t, err, "repository_not_available")
}

func TestRunOnceRoutesAnUpgradedManagedRepositoryWithAGitSuffix(t *testing.T) {
	store, definition, repository, _ := setupRunTest(t, false)
	if _, err := store.db.Exec(`UPDATE repositories SET remote_identity = remote_identity || '.git' WHERE id = ?`, repository.ID); err != nil {
		t.Fatal(err)
	}
	worker, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: workerA, WorkerVersion: "test", Runtime: protocol.RuntimeCodex, RuntimeVersion: "codex-test",
		Capabilities: []protocol.Capability{
			{Kind: protocol.CapabilityKindTool, Name: "git", Status: protocol.CapabilityReady},
			{Kind: protocol.CapabilityKindTool, Name: "gh", Status: protocol.CapabilityReady},
			{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimeCodex, Status: protocol.CapabilityReady},
		},
		Capacity: 1, Health: "healthy", AcceptsManagedRepositories: true,
		SourceAccess: []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, created, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "upgraded-git-suffix", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if err != nil || !created || detail.Run.State != "queued" {
		t.Fatalf("create upgraded Run: created=%t err=%v detail=%#v", created, err, detail)
	}
	if job := detail.Jobs[0].Job; job.RepositoryID != repository.ID || job.AssignedWorkerID != worker.ID {
		t.Fatalf("upgraded Run Job = %#v", job)
	}
}

func TestRunHistoryUsesAStableCursorWithoutDroppingOlderRuns(t *testing.T) {
	store, definition, repository, _ := setupRunTest(t, true)
	fixed := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return fixed }
	createdIDs := make(map[string]bool)
	for _, requestKey := range []string{"run-page-a", "run-page-b", "run-page-c"} {
		detail, created, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
			RequestKey: requestKey, DefinitionID: definition.ID, RepositoryID: repository.ID,
		})
		if err != nil || !created {
			t.Fatalf("create paged Run %q: created=%t err=%v", requestKey, created, err)
		}
		createdIDs[detail.Run.ID] = true
	}
	first, err := store.Runs(context.Background(), protocol.RunPageRequest{Limit: 2})
	if err != nil || len(first.Runs) != 2 || first.NextCursor == nil {
		t.Fatalf("first Run page: err=%v page=%#v", err, first)
	}
	second, err := store.Runs(context.Background(), protocol.RunPageRequest{Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(second.Runs) != 1 || second.NextCursor != nil {
		t.Fatalf("second Run page: err=%v page=%#v", err, second)
	}
	seen := make(map[string]bool)
	for _, run := range append(first.Runs, second.Runs...) {
		if seen[run.ID] {
			t.Fatalf("Run %q appeared on more than one page", run.ID)
		}
		seen[run.ID] = true
	}
	if len(seen) != len(createdIDs) {
		t.Fatalf("paged Run IDs = %#v, want %#v", seen, createdIDs)
	}
}

func TestBlockedRunRoutesWhenACompatibleRunnerClaims(t *testing.T) {
	store, definition, repository, _ := setupRunTest(t, false)
	detail, created, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "blocked-run", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if err != nil || !created || detail.Run.State != "blocked" || len(detail.Jobs) != 1 {
		t.Fatalf("blocked Run: created=%t err=%v detail=%#v", created, err, detail)
	}
	if detail.Jobs[0].Job.TaskID != "" || detail.Jobs[0].Job.BlockedReason == "" {
		t.Fatalf("blocked Job = %#v", detail.Jobs[0].Job)
	}
	worker := registerDefinitionWorker(
		t, store, workerA,
		protocol.RepositoryRegistration{Key: "run-target", RemoteIdentity: repository.RemoteIdentity},
		protocol.CapabilityReady,
		[]protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	)
	claim := claimTestTask(t, store, worker.ID, "blocked-run-claim", tokenA)
	wantPrompt, err := protocol.ResolveDefinitionPrompt(definition.Prompt, definition.Inputs)
	if err != nil || claim.Task.Title != definition.Name || claim.Task.Description != wantPrompt {
		t.Fatalf("materialized claim = %#v", claim.Task)
	}
	routed, err := store.Run(context.Background(), detail.Run.ID)
	if err != nil || routed.Run.State != "running" || routed.Jobs[0].Job.State != "preparing" ||
		routed.Jobs[0].Job.TaskID != claim.Task.ID {
		t.Fatalf("routed Run: err=%v detail=%#v", err, routed)
	}
}

func TestQueuedRunReroutesWhenItsAssignedRunnerGoesOffline(t *testing.T) {
	store, definition, repository, assigned := setupRunTest(t, true)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	registerDefinitionWorker(
		t, store, workerA,
		protocol.RepositoryRegistration{Key: "run-target", RemoteIdentity: repository.RemoteIdentity},
		protocol.CapabilityReady,
		[]protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	)
	registerDefinitionWorker(
		t, store, workerB,
		protocol.RepositoryRegistration{Key: "run-target", RemoteIdentity: repository.RemoteIdentity},
		protocol.CapabilityReady,
		[]protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	)
	detail, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "rerouted-run", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if err != nil || detail.Jobs[0].Job.AssignedWorkerID != assigned.ID {
		t.Fatalf("initial Run assignment: err=%v detail=%#v", err, detail)
	}

	now = now.Add(protocol.WorkerOnlineWindow + time.Millisecond)
	registerDefinitionWorker(
		t, store, workerB,
		protocol.RepositoryRegistration{Key: "run-target", RemoteIdentity: repository.RemoteIdentity},
		protocol.CapabilityReady,
		[]protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	)
	claim := claimTestTask(t, store, workerB, "reroute-run-claim", tokenB)
	if claim.Execution.ID != detail.Jobs[0].Job.ExecutionID {
		t.Fatalf("rerouted claim execution = %q, want %q", claim.Execution.ID, detail.Jobs[0].Job.ExecutionID)
	}
	rerouted, err := store.Run(context.Background(), detail.Run.ID)
	if err != nil || rerouted.Jobs[0].Job.AssignedWorkerID != workerB || rerouted.Jobs[0].Job.State != "preparing" {
		t.Fatalf("rerouted Run: err=%v detail=%#v", err, rerouted)
	}
}

func TestQueuedRunReroutingScansPastEligibleCandidatePages(t *testing.T) {
	store, definition, repository, _ := setupRunTest(t, true)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	registerDefinitionWorker(
		t, store, workerA,
		protocol.RepositoryRegistration{Key: "run-target", RemoteIdentity: repository.RemoteIdentity},
		protocol.CapabilityReady,
		[]protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	)
	for index := 0; index < 200; index++ {
		if _, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
			RequestKey:   fmt.Sprintf("eligible-run-%03d", index),
			DefinitionID: definition.ID, RepositoryID: repository.ID,
		}); err != nil {
			t.Fatalf("create eligible Run %d: %v", index, err)
		}
	}
	registerDefinitionWorker(
		t, store, workerB,
		protocol.RepositoryRegistration{Key: "run-target", RemoteIdentity: repository.RemoteIdentity},
		protocol.CapabilityReady,
		[]protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	)
	if _, err := store.db.Exec(`UPDATE workers SET health = 'unhealthy' WHERE id = ?`, workerA); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Millisecond)
	target, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "reroute-after-first-page", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if err != nil || target.Jobs[0].Job.AssignedWorkerID != workerB {
		t.Fatalf("target Run assignment: err=%v detail=%#v", err, target)
	}

	now = now.Add(protocol.WorkerOnlineWindow + time.Millisecond)
	for _, workerID := range []string{workerA, "worker-c"} {
		registerDefinitionWorker(
			t, store, workerID,
			protocol.RepositoryRegistration{Key: "run-target", RemoteIdentity: repository.RemoteIdentity},
			protocol.CapabilityReady,
			[]protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
		)
	}
	claim := claimTestTask(t, store, "worker-c", "reroute-second-page-claim", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	if claim.Execution.ID != target.Jobs[0].Job.ExecutionID {
		t.Fatalf("second-page rerouted claim = %q, want %q", claim.Execution.ID, target.Jobs[0].Job.ExecutionID)
	}
}

func TestBlockedRunMaterializationScansPastIncompatibleCandidatePages(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	repository := createManagedTestRepository(t, store, "github.com/example/paged-blocked-run")
	piDefinition, created, err := store.CreateDefinition(context.Background(), protocol.CreateDefinitionRequest{
		RequestKey: "paged-pi-definition", Name: "Pi-only review", Prompt: "Inspect with Pi.",
		Runtime: protocol.RuntimePi, AllowedTools: []string{"git"}, TimeoutSeconds: 600,
	})
	if err != nil || !created {
		t.Fatalf("create Pi Definition: created=%t err=%v", created, err)
	}
	for index := 0; index < 200; index++ {
		if _, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
			RequestKey:   fmt.Sprintf("blocked-pi-run-%03d", index),
			DefinitionID: piDefinition.ID, RepositoryID: repository.ID,
		}); err != nil {
			t.Fatalf("create blocked Pi Run %d: %v", index, err)
		}
	}
	now = now.Add(time.Millisecond)
	codexDefinition := createTestDefinition(t, store, "paged-codex-definition", "Codex review")
	target, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "blocked-codex-after-first-page", DefinitionID: codexDefinition.ID, RepositoryID: repository.ID,
	})
	if err != nil || target.Run.State != "blocked" {
		t.Fatalf("target blocked Run: err=%v detail=%#v", err, target)
	}
	registerDefinitionWorker(
		t, store, workerA,
		protocol.RepositoryRegistration{Key: "paged-blocked-run", RemoteIdentity: repository.RemoteIdentity},
		protocol.CapabilityReady,
		[]protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	)
	claim := claimTestTask(t, store, workerA, "materialize-second-page-claim", tokenA)
	if claim.Task.Title != codexDefinition.Name {
		t.Fatalf("second-page materialized claim = %#v", claim.Task)
	}
	routed, err := store.Run(context.Background(), target.Run.ID)
	if err != nil || routed.Jobs[0].Job.ExecutionID != claim.Execution.ID {
		t.Fatalf("second-page materialized Run: err=%v detail=%#v", err, routed)
	}
}

func TestRunLifecycleCapturesResultAndWarnsBeforeRetry(t *testing.T) {
	store, definition, repository, worker := setupRunTest(t, true)
	detail, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "failed-run", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim := claimTestTask(t, store, worker.ID, "failed-run-claim", tokenA)
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{
		LeaseToken: tokenA, ProcessIdentity: "fake-agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Result: "Partial review", Error: "fake agent failed",
	}); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Run(context.Background(), detail.Run.ID)
	if err != nil || failed.Run.State != "failed" {
		t.Fatalf("failed Run: err=%v detail=%#v", err, failed)
	}
	job := failed.Jobs[0].Job
	if job.Result != "Partial review" || job.FailureReason != "fake agent failed" ||
		job.StartedAt == nil || job.TerminalAt == nil || !job.RetryMayRepeatEffects {
		t.Fatalf("failed Job evidence = %#v", job)
	}
	if err := store.DeleteTask(context.Background(), job.TaskID); err == nil {
		t.Fatal("legacy deletion unexpectedly accepted a Run Job Task")
	} else {
		assertErrorCode(t, err, "task_delete_not_allowed")
	}
	if preserved, err := store.Run(context.Background(), detail.Run.ID); err != nil || preserved.Run.State != "failed" {
		t.Fatalf("Run changed after rejected legacy deletion: err=%v detail=%#v", err, preserved)
	}
	if _, err := store.RetryExecution(context.Background(), job.ExecutionID); err == nil {
		t.Fatal("legacy retry unexpectedly accepted a Run Job execution")
	} else {
		assertErrorCode(t, err, "retry_not_allowed")
	}
	retried, err := store.RetryJob(context.Background(), job.ID)
	if err != nil || retried.Run.State != "queued" || retried.Jobs[0].Job.State != "queued" {
		t.Fatalf("retry Run: err=%v detail=%#v", err, retried)
	}
}

func TestQueuedCancellationAfterRetryDoesNotReuseStaleAttemptEvidence(t *testing.T) {
	store, definition, repository, worker := setupRunTest(t, true)
	now := store.now().UTC()
	store.now = func() time.Time { return now }
	detail, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "cancel-retried-run", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim := claimTestTask(t, store, worker.ID, "cancel-retried-run-claim", tokenA)
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Result: "partial stale result", Error: "stale failure",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetryJob(context.Background(), detail.Jobs[0].Job.ID); err != nil {
		t.Fatal(err)
	}
	cancelledAt := now
	cancelled, err := store.CancelJob(context.Background(), detail.Jobs[0].Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	job := cancelled.Jobs[0].Job
	if job.State != "cancelled" || job.Result != "" || job.FailureReason != "" ||
		job.TerminalAt == nil || job.TerminalAt.UnixMilli() != cancelledAt.UnixMilli() {
		t.Fatalf("cancelled retried Job projected stale evidence: %#v", job)
	}
}

func TestFailedRunRetrySelectsACurrentlyEligibleRunner(t *testing.T) {
	store, definition, repository, assigned := setupRunTest(t, true)
	detail, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "reroute-failed-run", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim := claimTestTask(t, store, assigned.ID, "reroute-failed-run-claim", tokenA)
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "fake agent failed",
	}); err != nil {
		t.Fatal(err)
	}
	registerDefinitionWorker(
		t, store, assigned.ID,
		protocol.RepositoryRegistration{Key: "run-target", RemoteIdentity: repository.RemoteIdentity},
		protocol.CapabilityMissing,
		[]protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	)
	replacement := registerDefinitionWorker(
		t, store, workerB,
		protocol.RepositoryRegistration{Key: "replacement-target", RemoteIdentity: repository.RemoteIdentity},
		protocol.CapabilityReady,
		[]protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	)
	retried, err := store.RetryJob(context.Background(), detail.Jobs[0].Job.ID)
	if err != nil || retried.Jobs[0].Job.State != "queued" ||
		retried.Jobs[0].Job.AssignedWorkerID != replacement.ID {
		t.Fatalf("rerouted failed Job: err=%v detail=%#v", err, retried)
	}
}

func TestBlockedJobCanBeCancelledWithoutCreatingLegacyExecution(t *testing.T) {
	store, definition, repository, _ := setupRunTest(t, false)
	detail, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "cancel-blocked-run", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.CancelJob(context.Background(), detail.Jobs[0].Job.ID)
	if err != nil || cancelled.Run.State != "cancelled" || cancelled.Jobs[0].Job.TaskID != "" ||
		cancelled.Jobs[0].Job.TerminalAt == nil {
		t.Fatalf("cancel blocked Job: err=%v detail=%#v", err, cancelled)
	}
	var tasks int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&tasks); err != nil || tasks != 0 {
		t.Fatalf("blocked cancellation created %d Tasks, err=%v", tasks, err)
	}
}

func TestActiveJobExposesPendingCancellation(t *testing.T) {
	store, definition, repository, worker := setupRunTest(t, true)
	detail, _, err := store.CreateRun(context.Background(), protocol.CreateRunRequest{
		RequestKey: "cancel-active-run", DefinitionID: definition.ID, RepositoryID: repository.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim := claimTestTask(t, store, worker.ID, "cancel-active-run-claim", tokenA)
	if claim.JobID != detail.Jobs[0].Job.ID {
		t.Fatalf("claimed Job = %q; want %q", claim.JobID, detail.Jobs[0].Job.ID)
	}
	cancelled, err := store.CancelJob(context.Background(), claim.JobID)
	if err != nil || cancelled.Jobs[0].Job.State != "preparing" ||
		!cancelled.Jobs[0].Job.CancellationRequested {
		t.Fatalf("pending Job cancellation: err=%v detail=%#v", err, cancelled)
	}
}
