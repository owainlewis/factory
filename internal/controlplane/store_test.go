package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
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

func registerTestWorker(t *testing.T, store *Store, id string, capacity int, repositories ...protocol.RepositoryRegistration) protocol.Worker {
	t.Helper()
	worker, err := store.RegisterWorker(context.Background(), id, protocol.WorkerRegistration{
		Name:          id,
		WorkerVersion: "test",
		CodexVersion:  "codex-test",
		Capacity:      capacity,
		ActiveCount:   0,
		Health:        "healthy",
		Repositories:  repositories,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
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
		t.Fatalf("reopen marked V2 database: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	unknown := root + "/v1.sqlite3"
	original := []byte("existing V1 bytes must stay untouched")
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
		Name: workerA, WorkerVersion: "test", CodexVersion: "test", Capacity: 1, Health: "unhealthy",
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
	events, err := store.Events(context.Background(), claim.Attempt.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
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
		Name: "worker-a", WorkerVersion: "test", CodexVersion: "test", Capacity: 1, Health: "healthy",
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
