package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

type managedAcquisitionFixture struct {
	manager *Manager
	syncDir string
	first   protocol.Repository
	second  protocol.Repository
}

func newManagedAcquisitionFixture(t *testing.T, mode string) managedAcquisitionFixture {
	t.Helper()
	firstOrigin := createRepository(t, "managed-first")
	secondOrigin := createRepository(t, "managed-second")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	toolDirectory := t.TempDir()
	syncDirectory := filepath.Join(t.TempDir(), "clone-sync")
	if err := os.Mkdir(syncDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_TEST_REAL_GIT", realGit)
	t.Setenv("FACTORY_TEST_FIRST_ORIGIN", firstOrigin.origin)
	t.Setenv("FACTORY_TEST_SECOND_ORIGIN", secondOrigin.origin)
	t.Setenv("FACTORY_TEST_CLONE_SYNC", syncDirectory)
	t.Setenv("FACTORY_TEST_CLONE_MODE", mode)
	githubPath := filepath.Join(toolDirectory, "gh")
	githubScript := `#!/bin/sh
set -eu
if [ "${1:-}" = "auth" ] && [ "${2:-}" = "status" ]; then
  exit 0
fi
slug="${3:-}"
case "$slug" in
  example/first) origin="$FACTORY_TEST_FIRST_ORIGIN"; name=first ;;
  example/second) origin="$FACTORY_TEST_SECOND_ORIGIN"; name=second ;;
  *) exit 91 ;;
esac
printf '%s\n' "$slug" >> "$FACTORY_TEST_CLONE_SYNC/calls"
touch "$FACTORY_TEST_CLONE_SYNC/$name.started"
case "$FACTORY_TEST_CLONE_MODE:$name" in
  overlap:*)
    while [ ! -f "$FACTORY_TEST_CLONE_SYNC/first.started" ] ||
          [ ! -f "$FACTORY_TEST_CLONE_SYNC/second.started" ]; do sleep 0.01; done
    ;;
  block-first:first|block-all:*)
    while [ ! -f "$FACTORY_TEST_CLONE_SYNC/release" ]; do sleep 0.01; done
    ;;
esac
"$FACTORY_TEST_REAL_GIT" clone --no-checkout "$origin" "$4"
"$FACTORY_TEST_REAL_GIT" -C "$4" remote set-url origin "https://github.com/$slug.git"
if [ "$FACTORY_TEST_CLONE_MODE:$name" = "cleanup-error:first" ]; then
  touch "$(dirname "$4")/unexpected"
fi
`
	if err := os.WriteFile(githubPath, []byte(githubScript), 0o700); err != nil {
		t.Fatal(err)
	}
	gitPath := filepath.Join(toolDirectory, "git")
	gitScript := `#!/bin/sh
set -eu
if [ "${1:-}" = "remote" ] && [ "${2:-}" = "get-url" ] && [ "${3:-}" = "origin" ]; then
	  exec "$FACTORY_TEST_REAL_GIT" "$@"
fi
exec "$FACTORY_TEST_REAL_GIT" \
  -c "url.$FACTORY_TEST_FIRST_ORIGIN.insteadOf=https://github.com/example/first.git" \
  -c "url.$FACTORY_TEST_SECOND_ORIGIN.insteadOf=https://github.com/example/second.git" "$@"
`
	if err := os.WriteFile(gitPath, []byte(gitScript), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		dataDirectory:                 filepath.Join(t.TempDir(), "worker"),
		options:                       Options{GitExecutable: gitPath, GitHubExecutable: githubPath},
		managedRepositoryIDs:          make(map[string]bool),
		managedRepositoryReservations: make(map[string]bool),
	}
	return managedAcquisitionFixture{
		manager: manager,
		syncDir: syncDirectory,
		first: protocol.Repository{
			ID: fixtureUUID(801), Key: "github.com/example/first",
			RemoteIdentity: "github.com/example/first",
		},
		second: protocol.Repository{
			ID: fixtureUUID(802), Key: "github.com/example/second",
			RemoteIdentity: "github.com/example/second",
		},
	}
}

func acquireManagedAsync(manager *Manager, repository protocol.Repository) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := manager.acquireManagedRepository(context.Background(), repository)
		done <- err
	}()
	return done
}

func TestUnrelatedManagedRepositoryClonesOverlap(t *testing.T) {
	fixture := newManagedAcquisitionFixture(t, "overlap")
	firstDone := acquireManagedAsync(fixture.manager, fixture.first)
	secondDone := acquireManagedAsync(fixture.manager, fixture.second)
	for _, done := range []<-chan error{firstDone, secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("unrelated managed repository clones did not overlap")
		}
	}
	if fixture.manager.repositoryLocks.count() != 0 {
		t.Fatal("repository locks remained after overlapping clones")
	}
}

func TestBlockedCloneDoesNotPreventCachedRepositoryAcquisition(t *testing.T) {
	fixture := newManagedAcquisitionFixture(t, "block-first")
	if _, err := fixture.manager.acquireManagedRepository(context.Background(), fixture.second); err != nil {
		t.Fatal(err)
	}
	firstDone := acquireManagedAsync(fixture.manager, fixture.first)
	waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(fixture.syncDir, "first.started"))
		return err == nil
	})
	cachedContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := fixture.manager.acquireManagedRepository(cachedContext, fixture.second); err != nil {
		t.Fatalf("cached unrelated acquisition waited behind blocked clone: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.syncDir, "release"), []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestBlockedCloneDoesNotPreventCachedTaskFromReachingRuntime(t *testing.T) {
	acquisition := newManagedAcquisitionFixture(t, "block-first")
	server := newServerFixture(t, nil)
	first, _, err := server.store.CreateManagedRepository(context.Background(),
		protocol.CreateManagedRepositoryRequest{RemoteIdentity: acquisition.first.RemoteIdentity})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := server.store.CreateManagedRepository(context.Background(),
		protocol.CreateManagedRepositoryRequest{RemoteIdentity: acquisition.second.RemoteIdentity})
	if err != nil {
		t.Fatal(err)
	}
	acquisition.first.ID = first.ID
	acquisition.second.ID = second.ID
	if _, err := acquisition.manager.acquireManagedRepository(context.Background(), acquisition.second); err != nil {
		t.Fatal(err)
	}
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	t.Setenv("FACTORY_TEST_SUPERVISOR", "1")
	t.Setenv("FACTORY_TEST_CODEX_LOG", filepath.Join(t.TempDir(), "codex-log"))
	manager := newManagedWorkerForConcurrencyTest(t, server, acquisition, codexPath, 2)
	if _, err := server.store.RegisterWorker(context.Background(), manager.ID(), protocol.WorkerRegistration{
		Name: "managed-concurrency-worker", WorkerVersion: "test",
		Runtime: protocol.RuntimeCodex, RuntimeVersion: "codex-cli test-1.0",
		Capacity: 2, Health: "healthy",
		SourceAccess:               []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
		AcceptsManagedRepositories: true, CapacityHandoffVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	reservationTask, _, err := server.store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "reserve-cached-second", Title: "Reserve cached second",
		Description: "Reserve dynamic repository mapping",
		Route: &protocol.TaskRoute{
			RepositoryRemoteIdentity: acquisition.second.RemoteIdentity,
			SourceAccess:             protocol.SourceAccess{Provider: "github", Hostname: "github.com"},
		},
		TimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reservationTask.Execution.AssignedWorkerID != manager.ID() {
		t.Fatalf("reservation task worker = %q, want %q", reservationTask.Execution.AssignedWorkerID, manager.ID())
	}
	if _, err := server.store.CancelTask(context.Background(), reservationTask.Task.ID); err != nil {
		t.Fatal(err)
	}
	startManager(t, manager)
	worker := waitForWorker(t, server.store, manager.ID(), func(worker protocol.Worker) bool {
		return worker.Health == "healthy" && worker.AcceptsManagedRepositories && len(worker.Repositories) == 1
	})
	firstTask, _, err := server.store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "blocked-first-clone", Title: "Blocked first clone",
		Description: "Exercise worker behavior.\nFAKE_MODE=success",
		Route: &protocol.TaskRoute{
			RepositoryRemoteIdentity: acquisition.first.RemoteIdentity,
			SourceAccess:             protocol.SourceAccess{Provider: "github", Hostname: "github.com"},
		},
		TimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(acquisition.syncDir, "first.started"))
		return err == nil
	})
	secondTask, _, err := server.store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "cached-second-runtime", Title: "Cached second runtime",
		Description: "Exercise worker behavior.\nFAKE_MODE=success",
		WorkerID:    worker.ID, RepositoryID: second.ID, TimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondTask = waitForTaskState(t, server.store, secondTask.Task.ID, "succeeded")
	if len(secondTask.Attempts) != 1 || secondTask.Attempts[0].Result != "completed by fake Codex" {
		t.Fatalf("cached task did not reach runtime: %#v", secondTask.Attempts)
	}
	if err := os.WriteFile(filepath.Join(acquisition.syncDir, "release"), []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForTaskState(t, server.store, firstTask.Task.ID, "succeeded")
}

func TestDuplicateManagedRepositoryAcquisitionClonesOnce(t *testing.T) {
	fixture := newManagedAcquisitionFixture(t, "block-first")
	firstDone := acquireManagedAsync(fixture.manager, fixture.first)
	waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(fixture.syncDir, "first.started"))
		return err == nil
	})
	secondDone := acquireManagedAsync(fixture.manager, fixture.first)
	time.Sleep(100 * time.Millisecond)
	if calls := cloneCalls(t, fixture.syncDir); len(calls) != 1 {
		t.Fatalf("clone calls while duplicate waited = %q", calls)
	}
	if err := os.WriteFile(filepath.Join(fixture.syncDir, "release"), []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, done := range []<-chan error{firstDone, secondDone} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if calls := cloneCalls(t, fixture.syncDir); len(calls) != 1 {
		t.Fatalf("duplicate acquisition clone calls = %q", calls)
	}
}

func TestManagedRepositoryCacheReservationIsAtomic(t *testing.T) {
	fixture := newManagedAcquisitionFixture(t, "block-first")
	for index := 0; index < protocol.MaxRepositoryCacheEntries-1; index++ {
		fixture.manager.managedRepositoryIDs[fixtureUUID(index+1)] = true
	}
	firstDone := acquireManagedAsync(fixture.manager, fixture.first)
	waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(fixture.syncDir, "first.started"))
		return err == nil
	})
	_, err := fixture.manager.acquireManagedRepository(context.Background(), fixture.second)
	if err == nil || !strings.Contains(err.Error(), "cache limit") {
		t.Fatalf("concurrent cache reservation error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.syncDir, "release"), []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	fixture.manager.stateMutex.Lock()
	installed := len(fixture.manager.managedRepositoryIDs)
	reservations := len(fixture.manager.managedRepositoryReservations)
	fixture.manager.stateMutex.Unlock()
	if installed != protocol.MaxRepositoryCacheEntries || reservations != 0 {
		t.Fatalf("cache accounting = %d installed, %d reservations", installed, reservations)
	}
}

func TestCancelledManagedRepositoryCloneReleasesCoordination(t *testing.T) {
	fixture := newManagedAcquisitionFixture(t, "block-first")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := fixture.manager.acquireManagedRepository(ctx, fixture.first)
		done <- err
	}()
	waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(fixture.syncDir, "first.started"))
		return err == nil
	})
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled clone succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled clone did not return")
	}
	fixture.manager.stateMutex.Lock()
	reservations := len(fixture.manager.managedRepositoryReservations)
	fixture.manager.stateMutex.Unlock()
	if fixture.manager.repositoryLocks.count() != 0 || reservations != 0 {
		t.Fatalf("cancelled clone leaked %d locks and %d reservations",
			fixture.manager.repositoryLocks.count(), reservations)
	}
}

func TestInstalledCloneCountsWhenTemporaryCleanupFails(t *testing.T) {
	fixture := newManagedAcquisitionFixture(t, "cleanup-error")
	_, err := fixture.manager.acquireManagedRepository(context.Background(), fixture.first)
	if err == nil || !strings.Contains(err.Error(), "remove managed repository clone directory") {
		t.Fatalf("post-install cleanup error = %v", err)
	}
	cachePath := filepath.Join(fixture.manager.dataDirectory, "repositories", fixture.first.ID)
	if info, statErr := os.Stat(cachePath); statErr != nil || !info.IsDir() {
		t.Fatalf("installed cache after cleanup error = %v, %v", info, statErr)
	}
	fixture.manager.stateMutex.Lock()
	installed := fixture.manager.managedRepositoryIDs[fixture.first.ID]
	reservations := len(fixture.manager.managedRepositoryReservations)
	fixture.manager.stateMutex.Unlock()
	if !installed || reservations != 0 {
		t.Fatalf("post-install cleanup accounting = installed %t, reservations %d", installed, reservations)
	}
}

func TestRepositoryLockSetRemovesIdleAndCancelledEntries(t *testing.T) {
	var locks repositoryLockSet
	release, err := locks.acquire(context.Background(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := locks.acquire(ctx, "shared"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter error = %v", err)
	}
	release()
	for index := 0; index < 1000; index++ {
		release, err := locks.acquire(context.Background(), fixtureUUID(index+1))
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	if locks.count() != 0 {
		t.Fatalf("idle keyed lock entries = %d", locks.count())
	}
}

func TestRepositoryLockSetSerializesOneKeyOnly(t *testing.T) {
	var locks repositoryLockSet
	release, err := locks.acquire(context.Background(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	wait.Add(1)
	acquired := make(chan struct{})
	go func() {
		defer wait.Done()
		secondRelease, err := locks.acquire(context.Background(), "shared")
		if err == nil {
			close(acquired)
			secondRelease()
		}
	}()
	otherRelease, err := locks.acquire(context.Background(), "other")
	if err != nil {
		t.Fatal(err)
	}
	otherRelease()
	select {
	case <-acquired:
		t.Fatal("same repository lock overlapped")
	default:
	}
	release()
	wait.Wait()
}

func TestRepositoryLockSetPoisonStopsWaiters(t *testing.T) {
	var locks repositoryLockSet
	release, err := locks.acquire(context.Background(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := locks.acquire(context.Background(), "shared")
		done <- err
	}()
	locks.poison("shared", errors.New("partial Git metadata"))
	release()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "partial Git metadata") {
			t.Fatalf("poisoned waiter error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("poisoned waiter remained blocked")
	}
	if locks.count() != 1 {
		t.Fatalf("poisoned lock entries = %d", locks.count())
	}
}

func cloneCalls(t *testing.T, directory string) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(directory, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(body))
}

func newManagedWorkerForConcurrencyTest(
	t *testing.T,
	fixture *serverFixture,
	acquisition managedAcquisitionFixture,
	codexPath string,
	maxConcurrent int,
) *Manager {
	t.Helper()
	options := testOptions(codexPath)
	options.GitExecutable = acquisition.manager.options.GitExecutable
	options.GitHubExecutable = acquisition.manager.options.GitHubExecutable
	manager, err := New(Config{
		Server: fixture.server.URL, Name: "managed-concurrency-worker", MaxConcurrent: maxConcurrent,
		DataDirectory: acquisition.manager.dataDirectory, SourceAccess: []string{"github"},
	}, options, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return manager
}
