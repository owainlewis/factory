package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestWorkerManagerHelperProcess(t *testing.T) {
	if os.Getenv("FACTORY_TEST_MANAGER_HELPER") != "1" {
		return
	}
	config, err := LoadConfig(os.Getenv("FACTORY_TEST_MANAGER_CONFIG"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	options := testOptions(os.Getenv("FACTORY_TEST_MANAGER_CODEX"))
	options.SupervisorCommand = []string{os.Args[0], "-test.run=TestWorkerSupervisorHelperProcess", "--"}
	manager, err := New(config, options, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := manager.Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func fixtureUUID(value int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", value)
}

func fixtureManifest(dataDirectory, workerID string, value int) attemptManifest {
	taskID := fixtureUUID(value + 1)
	attemptID := fixtureUUID(value + 2)
	return attemptManifest{
		SchemaVersion:  manifestSchemaVersion,
		WorkerID:       workerID,
		TaskID:         taskID,
		ExecutionID:    fixtureUUID(value + 3),
		AttemptID:      attemptID,
		RepositoryID:   fixtureUUID(value + 4),
		RepositoryKey:  "factory",
		RepositoryPath: filepath.Join(dataDirectory, "source"),
		RemoteIdentity: "example.invalid/factory",
		BaseCommit:     strings.Repeat("a", 40),
		WorktreePath:   filepath.Join(dataDirectory, "worktrees", attemptID),
		Branch:         "factory/" + taskID[:12] + "-" + attemptID[:12],
		Lifecycle:      manifestPreparing,
		CreatedAt:      time.Unix(100, 0).UTC(),
		UpdatedAt:      time.Unix(100, 0).UTC(),
	}
}

func TestManifestWritesAreAtomicAndDurable(t *testing.T) {
	dataDirectory := t.TempDir()
	workerID := fixtureUUID(1)
	store := newManifestStore(dataDirectory, workerID)
	store.now = func() time.Time { return time.Unix(200, 0) }
	manifest := fixtureManifest(dataDirectory, workerID, 10)
	store.hook = func(stage string) error {
		if stage == "before_attempts_parent_sync" {
			return errors.New("simulated crash before attempts parent sync")
		}
		return nil
	}
	if err := store.create(manifest); err == nil {
		t.Fatal("initial manifest proceeded before attempts directory parent sync")
	}
	path, _ := store.path(manifest.AttemptID)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manifest exists before attempts directory was durable: %v", err)
	}
	store.hook = nil
	if err := store.create(manifest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest permissions = %o", info.Mode().Perm())
	}

	store.hook = func(stage string) error {
		if stage == "after_file_sync" {
			return errors.New("simulated crash before rename")
		}
		return nil
	}
	if _, err := store.update(manifest.AttemptID, func(value *attemptManifest) error {
		value.Lifecycle = manifestWorktreeCreated
		return nil
	}); err == nil {
		t.Fatal("manifest update unexpectedly survived pre-rename crash")
	}
	persisted, err := store.load(manifest.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Lifecycle != manifestPreparing {
		t.Fatalf("pre-rename crash persisted lifecycle %q", persisted.Lifecycle)
	}

	store.hook = func(stage string) error {
		if stage == "after_rename" {
			return errors.New("simulated crash before directory sync")
		}
		return nil
	}
	if _, err := store.update(manifest.AttemptID, func(value *attemptManifest) error {
		value.Lifecycle = manifestWorktreeCreated
		return nil
	}); err == nil {
		t.Fatal("manifest update unexpectedly reported a completed directory sync")
	}
	persisted, err = store.load(manifest.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Lifecycle != manifestWorktreeCreated {
		t.Fatalf("post-rename manifest is not complete JSON: %#v", persisted)
	}

	store.hook = nil
	for _, lifecycle := range []string{
		manifestSupervisorReady, manifestRunning, manifestCompleted, manifestRetained,
		manifestCleanupStarted, manifestCleaned,
	} {
		if _, err := store.update(manifest.AttemptID, func(value *attemptManifest) error {
			value.Lifecycle = lifecycle
			return nil
		}); err != nil {
			t.Fatalf("persist lifecycle %s: %v", lifecycle, err)
		}
		persisted, err = store.load(manifest.AttemptID)
		if err != nil || persisted.Lifecycle != lifecycle {
			t.Fatalf("lifecycle %s persisted as %q: %v", lifecycle, persisted.Lifecycle, err)
		}
	}
	entries, err := os.ReadDir(store.attemptsDirectory())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != manifest.AttemptID+".json" {
		t.Fatalf("manifest directory contains temporary debris: %v", entries)
	}
	stale := filepath.Join(store.attemptsDirectory(), "."+manifest.AttemptID+"-stale.tmp")
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.loadAll(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale temporary manifest was not reconciled: %v", err)
	}
}

func TestStartupReconciliationClassifiesManifestAndFilesystemState(t *testing.T) {
	cases := []struct {
		name          string
		initial       string
		cleanupIntent string
		createGit     bool
		removeBefore  bool
		makeDirty     bool
		createPartial bool
		previewBranch bool
		want          string
		wantError     bool
	}{
		{name: "never created", initial: manifestPreparing, want: manifestNotCreated},
		{name: "missing", initial: manifestWorktreeCreated, want: manifestMissing},
		{name: "cleaned", initial: manifestCleaned, want: manifestCleaned},
		{name: "retained", initial: manifestWorktreeCreated, createGit: true, want: manifestRetained},
		{
			name: "preview branch retained", initial: manifestWorktreeCreated,
			createGit: true, previewBranch: true, want: manifestRetained,
		},
		{
			name: "automatic cleanup already absent", initial: manifestCleanupStarted,
			cleanupIntent: cleanupIntentAutomatic, createGit: true, removeBefore: true,
			want: manifestCleaned,
		},
		{
			name: "operator cleanup still present", initial: manifestCleanupStarted,
			cleanupIntent: cleanupIntentOperator, createGit: true, want: manifestCleaned,
		},
		{
			name: "automatic cleanup still eligible", initial: manifestCleanupStarted,
			cleanupIntent: cleanupIntentAutomatic, createGit: true, want: manifestCleaned,
		},
		{
			name: "automatic cleanup became dirty", initial: manifestCleanupStarted,
			cleanupIntent: cleanupIntentAutomatic, createGit: true, makeDirty: true, want: manifestRetained,
		},
		{
			name: "ambiguous cleanup remains safe", initial: manifestCleanupStarted,
			createGit: true, want: manifestRetained,
		},
		{name: "partial", initial: manifestWorktreeCreated, createPartial: true, want: manifestInconsistent, wantError: true},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newServerFixture(t, nil)
			repository := createRepository(t, "reconcile")
			dataDirectory := filepath.Join(t.TempDir(), "worker")
			manager := newTestManager(t, fixture, filepath.Join(t.TempDir(), "codex"), dataDirectory,
				map[string]repositoryFixture{"factory": repository}, 1)
			t.Cleanup(func() { _ = manager.Close() })
			workerValue, err := fixture.store.RegisterWorker(context.Background(), manager.ID(), protocol.WorkerRegistration{
				Name: "test-worker", WorkerVersion: "test", RuntimeVersion: "test", Capacity: 1,
				Health: "healthy", Repositories: []protocol.RepositoryRegistration{{
					Key: "factory", RemoteIdentity: repositoryIdentity(t, repository.path),
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			task, _, err := fixture.store.CreateTask(context.Background(), protocol.CreateTaskRequest{
				RequestKey: fmt.Sprintf("reconcile-%d", index), Title: "reconcile",
				Description: "reconcile", WorkerID: manager.ID(),
				RepositoryID: workerValue.Repositories[0].ID, TimeoutSeconds: 60,
			})
			if err != nil {
				t.Fatal(err)
			}
			claim, err := fixture.store.Claim(context.Background(), manager.ID(), protocol.ClaimRequest{
				RequestID: fmt.Sprintf("claim-%d", index), LeaseToken: strings.Repeat("l", 43),
			})
			if err != nil || claim == nil {
				t.Fatalf("claim = %#v, %v", claim, err)
			}
			value, err := prepareWorktree(context.Background(), "git",
				filepath.Join(dataDirectory, "worktrees"), manager.repositories[0], task.Task.ID, claim.Attempt.ID)
			if err != nil {
				t.Fatal(err)
			}
			if test.previewBranch {
				value.Branch = strings.Replace(value.Branch, "factory/", "factory-v2/", 1)
			}
			manifest := attemptManifest{
				TaskID: task.Task.ID, ExecutionID: claim.Execution.ID, AttemptID: claim.Attempt.ID,
				RepositoryID: workerValue.Repositories[0].ID, RepositoryKey: "factory",
				RepositoryPath: manager.repositories[0].Path, RemoteIdentity: manager.repositories[0].RemoteIdentity,
				BaseCommit: value.BaseCommit, WorktreePath: value.Path, Branch: value.Branch,
				LeaseDeadline: claim.Attempt.LeaseExpiresAt, Lifecycle: test.initial,
				CleanupIntent: test.cleanupIntent,
			}
			if err := manager.manifests.create(manifest); err != nil {
				t.Fatal(err)
			}
			if test.createGit {
				if err := addPreparedWorktree(context.Background(), "git", manager.repositories[0], value); err != nil {
					t.Fatal(err)
				}
			}
			if test.makeDirty {
				if err := os.WriteFile(filepath.Join(value.Path, "dirty.txt"), []byte("retain me\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.removeBefore {
				inspection, inspectErr := inspectManifestWorktree(
					context.Background(), "git", manager.dataDirectory, manifest,
				)
				if inspectErr != nil {
					t.Fatal(inspectErr)
				}
				if err := removeInspectedWorktree(context.Background(), "git", inspection, false); err != nil {
					t.Fatal(err)
				}
			}
			if test.createPartial {
				if err := os.MkdirAll(value.Path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			persisted, _ := manager.manifests.load(claim.Attempt.ID)
			err = manager.reconcileManifest(context.Background(), persisted)
			if test.wantError != (err != nil) {
				t.Fatalf("reconciliation error = %v; wantError=%v", err, test.wantError)
			}
			persisted, loadErr := manager.manifests.load(claim.Attempt.ID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if persisted.Lifecycle != test.want {
				t.Fatalf("lifecycle = %q; want %q", persisted.Lifecycle, test.want)
			}
			_, retained := manager.retained[claim.Attempt.ID]
			if retained != (test.want == manifestRetained) {
				t.Fatalf("retained=%v for lifecycle %s", retained, persisted.Lifecycle)
			}
			if test.initial == manifestCleanupStarted && test.createGit && !test.removeBefore {
				_, statErr := os.Stat(value.Path)
				if test.want == manifestCleaned && !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("startup cleanup left worktree: %v", statErr)
				}
				if test.want == manifestRetained && statErr != nil {
					t.Fatalf("startup cleanup removed retained worktree: %v", statErr)
				}
			}
			if test.initial == manifestCleanupStarted && test.createGit {
				command := exec.Command("git", "show-ref", "--verify", "refs/heads/"+value.Branch)
				command.Dir = repository.path
				err := command.Run()
				branchShouldRemain := test.want == manifestRetained ||
					test.cleanupIntent == cleanupIntentOperator
				if branchShouldRemain != (err == nil) {
					t.Fatalf("branch presence = %v; want %v", err == nil, branchShouldRemain)
				}
			}
		})
	}
}

func TestSafeManagedBranchDeletionRequiresBaseOrPublishedHead(t *testing.T) {
	for _, test := range []struct {
		name       string
		published  bool
		wantDelete bool
	}{
		{name: "base", wantDelete: true},
		{name: "published commit", published: true, wantDelete: true},
		{name: "unpublished commit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := createRepository(t, "branch-"+strings.ReplaceAll(test.name, " ", "-"))
			resolved, err := resolveRepository("factory", repository.path, "git")
			if err != nil {
				t.Fatal(err)
			}
			taskID, attemptID := fixtureUUID(300), fixtureUUID(301)
			root := filepath.Join(t.TempDir(), "worktrees")
			value, err := createWorktree(
				context.Background(), "git", root, resolved, taskID, attemptID,
			)
			if err != nil {
				t.Fatal(err)
			}
			if test.name != "base" {
				if err := os.WriteFile(filepath.Join(value.Path, "result.txt"), []byte(test.name+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				runGitTest(t, value.Path, "add", "result.txt")
				runGitTest(t, value.Path, "commit", "-m", test.name)
				if test.published {
					runGitTest(t, value.Path, "push", "origin", "HEAD:refs/heads/published-result")
					runGitTest(t, repository.path, "fetch", "origin")
				}
			}
			manifest := attemptManifest{
				TaskID: taskID, AttemptID: attemptID, BaseCommit: value.BaseCommit,
				Branch: value.Branch,
			}
			inspection := worktreeInspection{
				Repository: resolved, PathExists: true, Registered: true,
				Entry: gitWorktreeEntry{Path: value.Path, Branch: value.Branch},
			}
			if err := removeInspectedWorktree(context.Background(), "git", inspection, false); err != nil {
				t.Fatal(err)
			}
			deleted, err := deleteSafeManagedBranch(
				context.Background(), "git", resolved.Path, manifest,
			)
			if err != nil {
				t.Fatal(err)
			}
			if deleted != test.wantDelete {
				t.Fatalf("deleted = %v; want %v", deleted, test.wantDelete)
			}
			command := exec.Command("git", "show-ref", "--verify", "refs/heads/"+value.Branch)
			command.Dir = repository.path
			branchExists := command.Run() == nil
			if branchExists == test.wantDelete {
				t.Fatalf("branch exists = %v after deleted=%v", branchExists, deleted)
			}
		})
	}
}

func TestSafeManagedBranchDeletionPreservesBranchCheckedOutElsewhere(t *testing.T) {
	repository := createRepository(t, "branch-checked-out-elsewhere")
	resolved, err := resolveRepository("factory", repository.path, "git")
	if err != nil {
		t.Fatal(err)
	}
	taskID, attemptID := fixtureUUID(302), fixtureUUID(303)
	value, err := createWorktree(
		context.Background(), "git", filepath.Join(t.TempDir(), "managed"), resolved, taskID, attemptID,
	)
	if err != nil {
		t.Fatal(err)
	}
	inspection := worktreeInspection{
		Repository: resolved, PathExists: true, Registered: true,
		Entry: gitWorktreeEntry{Path: value.Path, Branch: value.Branch},
	}
	if err := removeInspectedWorktree(context.Background(), "git", inspection, false); err != nil {
		t.Fatal(err)
	}
	operatorWorktree := filepath.Join(t.TempDir(), "operator")
	runGitTest(t, repository.path, "worktree", "add", operatorWorktree, value.Branch)

	deleted, err := deleteSafeManagedBranch(
		context.Background(),
		"git",
		resolved.Path,
		attemptManifest{
			TaskID: taskID, AttemptID: attemptID, BaseCommit: value.BaseCommit, Branch: value.Branch,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if deleted {
		t.Fatal("deleted a managed branch checked out in another worktree")
	}
	runGitTest(t, repository.path, "show-ref", "--verify", "refs/heads/"+value.Branch)
}

func seedReconciliationManifest(
	t *testing.T,
	fixture *serverFixture,
	manager *Manager,
	lifecycle string,
) protocol.Claim {
	t.Helper()
	repository := manager.repositories[0]
	workerValue, err := fixture.store.RegisterWorker(context.Background(), manager.ID(), protocol.WorkerRegistration{
		Name: "test-worker", WorkerVersion: "test", RuntimeVersion: "test", Capacity: manager.config.MaxConcurrent,
		Health: "healthy", Repositories: []protocol.RepositoryRegistration{{
			Key: repository.Key, RemoteIdentity: repository.RemoteIdentity,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := fixture.store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: fmt.Sprintf("reconcile-retry-%d", time.Now().UnixNano()), Title: "reconcile retry",
		Description: "reconcile retry", WorkerID: manager.ID(),
		RepositoryID: workerValue.Repositories[0].ID, TimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := fixture.store.Claim(context.Background(), manager.ID(), protocol.ClaimRequest{
		RequestID:  fmt.Sprintf("reconcile-retry-claim-%d", time.Now().UnixNano()),
		LeaseToken: strings.Repeat("r", 43),
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, %v", claim, err)
	}
	value, err := prepareWorktree(context.Background(), "git",
		filepath.Join(manager.dataDirectory, "worktrees"), repository, task.Task.ID, claim.Attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := attemptManifest{
		TaskID: task.Task.ID, ExecutionID: claim.Execution.ID, AttemptID: claim.Attempt.ID,
		RepositoryID: workerValue.Repositories[0].ID, RepositoryKey: repository.Key,
		RepositoryPath: repository.Path, RemoteIdentity: repository.RemoteIdentity,
		BaseCommit: value.BaseCommit, WorktreePath: value.Path, Branch: value.Branch,
		LeaseDeadline: claim.Attempt.LeaseExpiresAt, Lifecycle: lifecycle,
	}
	if err := manager.manifests.create(manifest); err != nil {
		t.Fatal(err)
	}
	if err := addPreparedWorktree(context.Background(), "git", repository, value); err != nil {
		t.Fatal(err)
	}
	return *claim
}

func TestStartupReconciliationRetriesControlPlaneInterruptionBeforeRegistration(t *testing.T) {
	var unavailable atomic.Bool
	unavailable.Store(true)
	fixture := newServerFixture(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if unavailable.Load() && request.Method == http.MethodGet &&
				strings.HasPrefix(request.URL.Path, "/api/v1/attempts/") {
				http.Error(writer, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(writer, request)
		})
	})
	repository := createRepository(t, "server-recovery")
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"factory": repository}, 1)
	seedReconciliationManifest(t, fixture, manager, manifestWorktreeCreated)
	startManager(t, manager)
	time.Sleep(250 * time.Millisecond)
	manager.stateMutex.Lock()
	registered := manager.registered
	fatal := manager.fatalHealth
	manager.stateMutex.Unlock()
	if registered || fatal != nil {
		t.Fatalf("transient reconciliation registered=%v fatal=%v", registered, fatal)
	}
	unavailable.Store(false)
	waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool {
		return worker.Health == "healthy" && len(worker.RetainedWorktrees) == 1
	})
}

func TestStartupReconciliationRetriesGitInterruptionBeforeRegistration(t *testing.T) {
	fixture := newServerFixture(t, nil)
	repository := createRepository(t, "git-recovery")
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"factory": repository}, 1)
	seedReconciliationManifest(t, fixture, manager, manifestWorktreeCreated)
	gitPath := filepath.Join(t.TempDir(), "git")
	manager.options.GitExecutable = gitPath
	startManager(t, manager)
	time.Sleep(250 * time.Millisecond)
	manager.stateMutex.Lock()
	registered := manager.registered
	fatal := manager.fatalHealth
	manager.stateMutex.Unlock()
	if registered || fatal != nil {
		t.Fatalf("transient Git reconciliation registered=%v fatal=%v", registered, fatal)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realGit, gitPath); err != nil {
		t.Fatal(err)
	}
	waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool {
		return worker.Health == "healthy" && len(worker.RetainedWorktrees) == 1
	})
}

func TestCleanupPreviewAndConfirmationAreSafe(t *testing.T) {
	repository := createRepository(t, "cleanup")
	dataDirectory, err := resolveDataDirectory(filepath.Join(t.TempDir(), "worker"))
	if err != nil {
		t.Fatal(err)
	}
	workerID, err := loadOrCreateWorkerID(dataDirectory, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := newManifestStore(dataDirectory, workerID)
	taskID := fixtureUUID(100)
	attemptID := fixtureUUID(101)
	resolved, err := resolveRepository("factory", repository.path, "git")
	if err != nil {
		t.Fatal(err)
	}
	value, err := createWorktree(context.Background(), "git", filepath.Join(dataDirectory, "worktrees"),
		resolved,
		taskID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := attemptManifest{
		TaskID: taskID, ExecutionID: fixtureUUID(102), AttemptID: attemptID,
		RepositoryID: fixtureUUID(103), RepositoryKey: "factory", RepositoryPath: resolved.Path,
		RemoteIdentity: resolved.RemoteIdentity, BaseCommit: value.BaseCommit,
		WorktreePath: value.Path, Branch: value.Branch, Lifecycle: manifestRetained,
		RetentionReason: "failed attempt retained for inspection",
	}
	if err := store.create(manifest); err != nil {
		t.Fatal(err)
	}
	config := Config{
		Server: "http://127.0.0.1:7337", Name: "cleanup", MaxConcurrent: 1,
		DataDirectory: dataDirectory,
		Repositories:  map[string]RepositoryConfig{"factory": {Path: repository.path}},
	}
	path, _ := store.path(attemptID)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var preview bytes.Buffer
	if err := Cleanup(config, CleanupOptions{AttemptID: attemptID}, &preview); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{attemptID, value.Branch, resolved.RemoteIdentity,
		"failed attempt retained for inspection", `"git_status": "clean"`} {
		if !strings.Contains(preview.String(), expected) {
			t.Fatalf("cleanup preview does not contain %q:\n%s", expected, preview.String())
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("cleanup preview mutated the manifest")
	}
	if _, err := os.Stat(value.Path); err != nil {
		t.Fatalf("cleanup preview mutated the worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(value.Path, "uncommitted.txt"), []byte("operator reviewed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var confirmed bytes.Buffer
	if err := Cleanup(config, CleanupOptions{AttemptID: attemptID, Confirm: true}, &confirmed); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(value.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("confirmed cleanup left worktree: %v", err)
	}
	runGitTest(t, repository.path, "show-ref", "--verify", "refs/heads/"+value.Branch)
	cleaned, err := store.load(attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned.Lifecycle != manifestCleaned || cleaned.CleanupIntent != cleanupIntentOperator ||
		!strings.Contains(cleaned.CleanupResult, "completed") {
		t.Fatalf("cleaned manifest = %#v", cleaned)
	}
}

func TestCleanupFailsClosedOnIdentityMismatchAndPathEscape(t *testing.T) {
	repository := createRepository(t, "cleanup-unsafe")
	dataDirectory, err := resolveDataDirectory(filepath.Join(t.TempDir(), "worker"))
	if err != nil {
		t.Fatal(err)
	}
	workerID, err := loadOrCreateWorkerID(dataDirectory, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := newManifestStore(dataDirectory, workerID)
	taskID, attemptID := fixtureUUID(200), fixtureUUID(201)
	repo, err := resolveRepository("factory", repository.path, "git")
	if err != nil {
		t.Fatal(err)
	}
	value, err := createWorktree(context.Background(), "git", filepath.Join(dataDirectory, "worktrees"),
		repo, taskID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := attemptManifest{
		TaskID: taskID, ExecutionID: fixtureUUID(202), AttemptID: attemptID,
		RepositoryID: fixtureUUID(203), RepositoryKey: "factory", RepositoryPath: repo.Path,
		RemoteIdentity: repo.RemoteIdentity, BaseCommit: value.BaseCommit, WorktreePath: value.Path,
		Branch: value.Branch, Lifecycle: manifestRetained, RetentionReason: "inspect",
	}
	if err := store.create(manifest); err != nil {
		t.Fatal(err)
	}
	config := Config{
		Server: "http://127.0.0.1:7337", Name: "cleanup", MaxConcurrent: 1,
		DataDirectory: dataDirectory,
		Repositories:  map[string]RepositoryConfig{"factory": {Path: repository.path}},
	}
	if err := Cleanup(config, CleanupOptions{AttemptID: fixtureUUID(999), Confirm: true}, io.Discard); err == nil {
		t.Fatal("cleanup accepted a missing manifest")
	}
	if _, err := store.update(attemptID, func(value *attemptManifest) error {
		value.SupervisorPID = 123
		value.SupervisorIdentity = "unreconciled supervisor"
		value.ProcessGroupID = 456
		value.ProcessGroupIdentity = "unreconciled group"
		value.ProcessActive = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Cleanup(config, CleanupOptions{AttemptID: attemptID, Confirm: true}, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "unreconciled process identity") {
		t.Fatalf("cleanup process identity error = %v", err)
	}
	if _, err := store.update(attemptID, func(value *attemptManifest) error {
		value.ProcessActive = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.update(attemptID, func(value *attemptManifest) error {
		value.RemoteIdentity = "example.invalid/different"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Cleanup(config, CleanupOptions{AttemptID: attemptID, Confirm: true}, io.Discard); err == nil {
		t.Fatal("cleanup accepted a repository identity mismatch")
	}
	if _, err := os.Stat(value.Path); err != nil {
		t.Fatalf("identity mismatch changed worktree: %v", err)
	}

	if _, err := store.update(attemptID, func(value *attemptManifest) error {
		value.RemoteIdentity = repo.RemoteIdentity
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	current, err := store.load(attemptID)
	if err != nil {
		t.Fatal(err)
	}
	current.WorktreePath = filepath.Join(t.TempDir(), "escaped")
	body, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path, _ := store.path(attemptID)
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Cleanup(config, CleanupOptions{AttemptID: attemptID, Confirm: true}, io.Discard); err == nil {
		t.Fatal("cleanup accepted an escaped manifest path")
	}
	if _, err := os.Stat(value.Path); err != nil {
		t.Fatalf("path escape changed worktree: %v", err)
	}
	current.WorktreePath = value.Path
	body, err = json.MarshalIndent(current, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository.path, "worktree", "remove", "--force", value.Path)
	if err := os.MkdirAll(value.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Cleanup(config, CleanupOptions{AttemptID: attemptID, Confirm: true}, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "filesystem and Git worktree identity disagree") {
		t.Fatalf("cleanup partial worktree error = %v", err)
	}
}

func TestParseCleanupArgumentsAcceptsDocumentedOrder(t *testing.T) {
	attemptID := fixtureUUID(300)
	configPath, options, err := ParseCleanupArguments(
		[]string{attemptID, "--confirm", "--config", "/tmp/worker.toml"}, "/default.toml")
	if err != nil {
		t.Fatal(err)
	}
	if configPath != "/tmp/worker.toml" || options.AttemptID != attemptID || !options.Confirm {
		t.Fatalf("parsed cleanup arguments = %q, %#v", configPath, options)
	}
	if _, _, err := ParseCleanupArguments([]string{attemptID, "--unsafe"}, "/default.toml"); err == nil {
		t.Fatal("cleanup parser accepted an unknown option")
	}
}

func TestRepositoryRetainedCapacityDoesNotBlockAnotherRepository(t *testing.T) {
	first := createRepository(t, "full")
	second := createRepository(t, "open")
	fixture := newServerFixture(t, nil)
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"full": first, "open": second}, 1)
	manager.retainedCounts[repositoryIdentity(t, first.path)] = protocol.MaxRetainedPerRepo
	startManager(t, manager)
	registered := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool {
		if worker.Health != "healthy" {
			return false
		}
		for _, repository := range worker.Repositories {
			if repository.Key == "full" {
				return repository.RetainedCount == protocol.MaxRetainedPerRepo
			}
		}
		return false
	})
	full := createTask(t, fixture.store, registered, "full", "success", 60)
	open := createTask(t, fixture.store, registered, "open", "success", 60)
	waitForTaskState(t, fixture.store, open.Task.ID, "succeeded")
	time.Sleep(250 * time.Millisecond)
	fullDetail, err := fixture.store.Task(context.Background(), full.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fullDetail.Execution.State != "queued" || len(fullDetail.Attempts) != 0 {
		t.Fatalf("full repository was claimed: %#v", fullDetail)
	}
}

func TestDisposedAttemptHandoffDoesNotWaitForIdleWorker(t *testing.T) {
	repository := createRepository(t, "disposed-handoff")
	fixture := newServerFixture(t, nil)
	manager := newTestManager(t, fixture, filepath.Join(t.TempDir(), "codex"),
		filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"factory": repository}, 2)
	t.Cleanup(func() { _ = manager.Close() })
	manager.setHealth(health{State: "healthy", GitVersion: "test", RuntimeVersion: "test"})
	manager.stateMutex.Lock()
	manager.retainedCounts[manager.repositories[0].RemoteIdentity] = protocol.MaxRetainedPerRepo - 1
	manager.stateMutex.Unlock()
	manager.register(context.Background())
	workerValue := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool {
		return worker.Health == "healthy" &&
			worker.Repositories[0].RetainedCount == protocol.MaxRetainedPerRepo-1
	})
	first := createTask(t, fixture.store, workerValue, "factory", "fail", 60)
	claim, err := fixture.store.Claim(context.Background(), manager.ID(), protocol.ClaimRequest{
		RequestID: "disposed-handoff-first", LeaseToken: strings.Repeat("j", 43),
	})
	if err != nil || claim == nil || claim.Task.ID != first.Task.ID {
		t.Fatalf("claim = %#v, %v", claim, err)
	}

	manager.slots <- struct{}{}
	manager.finishWithoutWorktree(*claim, strings.Repeat("j", 43), &attemptHandle{
		expiry: claim.Attempt.LeaseExpiresAt,
	}, "failed", errors.New("failed before worktree creation"))
	if len(manager.disposed) != 0 {
		t.Fatalf("successful registration left disposed attempts pending: %#v", manager.disposed)
	}
	second := createTask(t, fixture.store, workerValue, "factory", "success", 60)
	next, err := fixture.store.Claim(context.Background(), manager.ID(), protocol.ClaimRequest{
		RequestID: "disposed-handoff-second", LeaseToken: strings.Repeat("k", 43),
	})
	if err != nil || next == nil || next.Task.ID != second.Task.ID {
		t.Fatalf("claim after disposal = %#v, %v", next, err)
	}
	<-manager.slots
}

func TestDisposedAttemptHandoffSurvivesFailedRegistrationAndRestart(t *testing.T) {
	var failNextRegistration atomic.Bool
	fixture := newServerFixture(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method == http.MethodPut &&
				strings.HasPrefix(request.URL.Path, "/api/v1/workers/") &&
				failNextRegistration.CompareAndSwap(true, false) {
				http.Error(writer, "control plane interrupted", http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(writer, request)
		})
	})
	repository := createRepository(t, "durable-disposed-handoff")
	dataDirectory := filepath.Join(t.TempDir(), "worker")
	codexPath := filepath.Join(t.TempDir(), "codex")
	firstManager := newTestManager(t, fixture, codexPath, dataDirectory,
		map[string]repositoryFixture{"factory": repository}, 2)
	firstManager.setHealth(health{State: "healthy", GitVersion: "test", RuntimeVersion: "test"})
	firstManager.stateMutex.Lock()
	firstManager.retainedCounts[firstManager.repositories[0].RemoteIdentity] = protocol.MaxRetainedPerRepo - 1
	firstManager.stateMutex.Unlock()
	firstManager.register(context.Background())
	workerValue := waitForWorker(t, fixture.store, firstManager.ID(), func(worker protocol.Worker) bool {
		return worker.Health == "healthy" &&
			worker.Repositories[0].RetainedCount == protocol.MaxRetainedPerRepo-1
	})
	createTask(t, fixture.store, workerValue, "factory", "fail", 60)
	claim, err := fixture.store.Claim(context.Background(), firstManager.ID(), protocol.ClaimRequest{
		RequestID: "durable-disposed-first", LeaseToken: strings.Repeat("m", 43),
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, %v", claim, err)
	}

	firstManager.slots <- struct{}{}
	failNextRegistration.Store(true)
	firstManager.finishWithoutWorktree(*claim, strings.Repeat("m", 43), &attemptHandle{
		expiry: claim.Attempt.LeaseExpiresAt,
	}, "failed", errors.New("failed before manifest creation"))
	<-firstManager.slots
	pending, err := firstManager.manifests.loadDisposals()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0] != claim.Attempt.ID {
		t.Fatalf("durable pending disposals = %#v", pending)
	}
	if err := firstManager.Close(); err != nil {
		t.Fatal(err)
	}

	secondManager := newTestManager(t, fixture, codexPath, dataDirectory,
		map[string]repositoryFixture{"factory": repository}, 2)
	t.Cleanup(func() { _ = secondManager.Close() })
	secondManager.setHealth(health{State: "healthy", GitVersion: "test", RuntimeVersion: "test"})
	secondManager.stateMutex.Lock()
	secondManager.retainedCounts[secondManager.repositories[0].RemoteIdentity] = protocol.MaxRetainedPerRepo - 1
	secondManager.stateMutex.Unlock()
	if err := secondManager.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	secondManager.register(context.Background())
	pending, err = secondManager.manifests.loadDisposals()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("successful restart registration left pending disposals: %#v", pending)
	}
	second := createTask(t, fixture.store, workerValue, "factory", "success", 60)
	next, err := fixture.store.Claim(context.Background(), secondManager.ID(), protocol.ClaimRequest{
		RequestID: "durable-disposed-second", LeaseToken: strings.Repeat("n", 43),
	})
	if err != nil || next == nil || next.Task.ID != second.Task.ID {
		t.Fatalf("claim after restart handoff = %#v, %v", next, err)
	}
}

func TestDisposalJournalFailureDoesNotCompleteAttempt(t *testing.T) {
	repository := createRepository(t, "disposal-journal-failure")
	fixture := newServerFixture(t, nil)
	manager := newTestManager(t, fixture, filepath.Join(t.TempDir(), "codex"),
		filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"factory": repository}, 1)
	t.Cleanup(func() { _ = manager.Close() })
	manager.setHealth(health{State: "healthy", GitVersion: "test", RuntimeVersion: "test"})
	manager.register(context.Background())
	workerValue := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool {
		return worker.Health == "healthy"
	})
	createTask(t, fixture.store, workerValue, "factory", "fail", 60)
	claim, err := fixture.store.Claim(context.Background(), manager.ID(), protocol.ClaimRequest{
		RequestID: "disposal-journal-failure", LeaseToken: strings.Repeat("o", 43),
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, %v", claim, err)
	}
	manager.manifests.disposalHook = func(string) error {
		return errors.New("simulated disposal journal failure")
	}
	manager.slots <- struct{}{}
	manager.finishWithoutWorktree(*claim, strings.Repeat("o", 43), &attemptHandle{
		expiry: claim.Attempt.LeaseExpiresAt,
	}, "failed", errors.New("failed before manifest creation"))
	<-manager.slots
	attempt, err := fixture.store.Attempt(context.Background(), claim.Attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != "preparing" {
		t.Fatalf("journal failure completed attempt as %s", attempt.State)
	}
	manager.stateMutex.Lock()
	pending := len(manager.disposed)
	manager.stateMutex.Unlock()
	if pending != 0 {
		t.Fatalf("successful in-memory fallback left %d disposal acknowledgments pending", pending)
	}
}

func TestAcknowledgedDisposedManifestIsPrunedBeforeSecondRestart(t *testing.T) {
	fixture := newServerFixture(t, nil)
	repository := createRepository(t, "disposed-compaction")
	dataDirectory := filepath.Join(t.TempDir(), "worker")
	codexPath := filepath.Join(t.TempDir(), "codex")
	seedManager := newTestManager(t, fixture, codexPath, dataDirectory,
		map[string]repositoryFixture{"factory": repository}, 1)
	claim := seedReconciliationManifest(t, fixture, seedManager, manifestCleanupStarted)
	if _, err := fixture.store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: strings.Repeat("r", 43), State: "failed", Error: "disposed during restart",
	}); err != nil {
		t.Fatal(err)
	}
	manifest, err := seedManager.manifests.load(claim.Attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := inspectManifestWorktree(context.Background(), "git", seedManager.dataDirectory, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeInspectedWorktree(context.Background(), "git", inspection, true); err != nil {
		t.Fatal(err)
	}
	if err := seedManager.Close(); err != nil {
		t.Fatal(err)
	}

	firstRestart := newTestManager(t, fixture, codexPath, dataDirectory,
		map[string]repositoryFixture{"factory": repository}, 1)
	firstRestart.setHealth(health{State: "healthy", GitVersion: "test", RuntimeVersion: "test"})
	if err := firstRestart.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := firstRestart.registration().DisposedAttemptIDs; len(got) != 1 || got[0] != claim.Attempt.ID {
		t.Fatalf("first restart disposals = %#v", got)
	}
	firstRestart.register(context.Background())
	if _, err := firstRestart.manifests.load(claim.Attempt.ID); err == nil {
		t.Fatal("successful registration left the disposed manifest on disk")
	}
	if err := firstRestart.Close(); err != nil {
		t.Fatal(err)
	}

	secondRestart := newTestManager(t, fixture, codexPath, dataDirectory,
		map[string]repositoryFixture{"factory": repository}, 1)
	t.Cleanup(func() { _ = secondRestart.Close() })
	if err := secondRestart.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := secondRestart.registration().DisposedAttemptIDs; len(got) != 0 {
		t.Fatalf("second restart republished acknowledged disposals: %#v", got)
	}
}

func TestDeletedDisposedAttemptDoesNotPoisonStartupReconciliation(t *testing.T) {
	fixture := newServerFixture(t, nil)
	repository := createRepository(t, "deleted-disposed-attempt")
	dataDirectory := filepath.Join(t.TempDir(), "worker")
	codexPath := filepath.Join(t.TempDir(), "codex")
	seedManager := newTestManager(t, fixture, codexPath, dataDirectory,
		map[string]repositoryFixture{"factory": repository}, 1)
	claim := seedReconciliationManifest(t, fixture, seedManager, manifestCleanupStarted)
	if _, err := fixture.store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: strings.Repeat("r", 43), State: "failed", Error: "disposed before deletion",
	}); err != nil {
		t.Fatal(err)
	}
	manifest, err := seedManager.manifests.load(claim.Attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := inspectManifestWorktree(context.Background(), "git", seedManager.dataDirectory, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeInspectedWorktree(context.Background(), "git", inspection, true); err != nil {
		t.Fatal(err)
	}
	if err := seedManager.persistLifecycle(claim.Attempt.ID, manifestCleaned, func(value *attemptManifest) {
		value.TerminalState = "failed"
		value.ProcessActive = false
	}); err != nil {
		t.Fatal(err)
	}
	if err := seedManager.recordDisposed(claim.Attempt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.RegisterWorker(
		context.Background(), seedManager.ID(), seedManager.registration(),
	); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.DeleteTask(context.Background(), claim.Task.ID); err != nil {
		t.Fatal(err)
	}
	if err := seedManager.Close(); err != nil {
		t.Fatal(err)
	}

	restart := newTestManager(t, fixture, codexPath, dataDirectory,
		map[string]repositoryFixture{"factory": repository}, 1)
	t.Cleanup(func() { _ = restart.Close() })
	restart.setHealth(health{State: "healthy", GitVersion: "test", RuntimeVersion: "test"})
	if err := restart.reconcile(context.Background()); err != nil {
		t.Fatalf("journaled deleted disposal caused reconciliation failure: %v", err)
	}
	restart.stateMutex.Lock()
	currentHealth, fatal := restart.health, restart.fatalHealth
	restart.stateMutex.Unlock()
	if fatal != nil || currentHealth.State != "healthy" {
		t.Fatalf("deleted disposal poisoned worker health: health=%#v fatal=%v", currentHealth, fatal)
	}
	if got := restart.registration().DisposedAttemptIDs; len(got) != 1 || got[0] != claim.Attempt.ID {
		t.Fatalf("deleted disposal registration = %#v", got)
	}
	restart.register(context.Background())
	if _, err := restart.manifests.load(claim.Attempt.ID); err == nil {
		t.Fatal("acknowledged deleted disposal left its manifest on disk")
	}
	pending, err := restart.manifests.loadDisposals()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("acknowledged deleted disposal remained journaled: %#v", pending)
	}
}

func TestDisposedManifestWaitsForHeartbeatBeforePruning(t *testing.T) {
	var blockHeartbeat atomic.Bool
	var watchRegistration atomic.Bool
	heartbeatReached := make(chan struct{}, 1)
	releaseHeartbeat := make(chan struct{}, 1)
	defer func() {
		select {
		case releaseHeartbeat <- struct{}{}:
		default:
		}
	}()
	registrationReached := make(chan struct{}, 1)
	fixture := newServerFixture(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if strings.HasSuffix(request.URL.Path, "/heartbeat") && blockHeartbeat.Load() {
				response := httptest.NewRecorder()
				next.ServeHTTP(response, request)
				heartbeatReached <- struct{}{}
				<-releaseHeartbeat
				for key, values := range response.Header() {
					writer.Header()[key] = append([]string(nil), values...)
				}
				writer.WriteHeader(response.Code)
				_, _ = writer.Write(response.Body.Bytes())
				return
			}
			if request.Method == http.MethodPut &&
				strings.HasPrefix(request.URL.Path, "/api/v1/workers/") &&
				watchRegistration.Load() {
				registrationReached <- struct{}{}
			}
			next.ServeHTTP(writer, request)
		})
	})
	repository := createRepository(t, "heartbeat-pruning")
	manager := newTestManager(t, fixture, filepath.Join(t.TempDir(), "codex"),
		filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"factory": repository}, 1)
	t.Cleanup(func() { _ = manager.Close() })
	claim := seedReconciliationManifest(t, fixture, manager, manifestWorktreeCreated)
	manifest, err := manager.manifests.load(claim.Attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := inspectManifestWorktree(context.Background(), "git", manager.dataDirectory, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeInspectedWorktree(context.Background(), "git", inspection, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.persistLifecycle(claim.Attempt.ID, manifestCleaned, func(value *attemptManifest) {
		value.TerminalState = "failed"
		value.ProcessActive = false
	}); err != nil {
		t.Fatal(err)
	}
	manager.setHealth(health{State: "healthy", GitVersion: "test", RuntimeVersion: "test"})
	manager.options.LeaseRenewInterval = time.Millisecond
	attemptContext, cancel := context.WithCancel(context.Background())
	handle := &attemptHandle{
		context: attemptContext, cancel: cancel, done: make(chan struct{}),
		heartbeatDone: make(chan struct{}), expiry: claim.Attempt.LeaseExpiresAt,
		manifestReady: true,
	}
	defer cancel()
	blockHeartbeat.Store(true)
	go manager.heartbeatAttempt(handle, claim.Attempt.ID, strings.Repeat("r", 43))
	select {
	case <-heartbeatReached:
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat did not reach the server")
	}
	if _, err := fixture.store.CompleteAttempt(context.Background(), claim.Attempt.ID,
		protocol.CompleteAttemptRequest{
			LeaseToken: strings.Repeat("r", 43), State: "failed", Error: "complete",
		}); err != nil {
		t.Fatal(err)
	}
	if err := manager.recordDisposed(claim.Attempt.ID); err != nil {
		t.Fatal(err)
	}

	watchRegistration.Store(true)
	registrationDone := make(chan struct{})
	go func() {
		manager.registrationMutex.Lock()
		defer manager.registrationMutex.Unlock()
		manager.registerAfterAttempt(handle)
		close(registrationDone)
	}()
	select {
	case <-registrationReached:
		t.Fatal("registration pruned the manifest before the heartbeat stopped")
	case <-time.After(100 * time.Millisecond):
	}
	releaseHeartbeat <- struct{}{}
	select {
	case <-registrationReached:
	case <-time.After(5 * time.Second):
		t.Fatal("registration did not proceed after the heartbeat stopped")
	}
	select {
	case <-registrationDone:
	case <-time.After(5 * time.Second):
		t.Fatal("registration did not finish")
	}
	if _, err := manager.manifests.load(claim.Attempt.ID); err == nil {
		t.Fatal("acknowledged manifest was not pruned")
	}
	manager.stateMutex.Lock()
	workerHealth := manager.health.State
	fatalHealth := manager.fatalHealth
	manager.stateMutex.Unlock()
	if workerHealth != "healthy" || fatalHealth != nil {
		t.Fatalf("late heartbeat made worker unhealthy: health=%s error=%v", workerHealth, fatalHealth)
	}
}

func TestPeriodicRegistrationCannotOvertakeRetainedCapacityHandoff(t *testing.T) {
	var blockNextRegistration atomic.Bool
	registrationReached := make(chan struct{}, 1)
	releaseRegistration := make(chan struct{})
	fixture := newServerFixture(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method == http.MethodPut &&
				strings.HasPrefix(request.URL.Path, "/api/v1/workers/") &&
				blockNextRegistration.CompareAndSwap(true, false) {
				registrationReached <- struct{}{}
				<-releaseRegistration
			}
			next.ServeHTTP(writer, request)
		})
	})
	repository := createRepository(t, "registration-handoff")
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"factory": repository}, 2)
	t.Cleanup(func() { _ = manager.Close() })
	manager.setHealth(health{State: "healthy", GitVersion: "test", RuntimeVersion: "test"})
	manager.stateMutex.Lock()
	manager.retainedCounts[manager.repositories[0].RemoteIdentity] = protocol.MaxRetainedPerRepo - 1
	manager.stateMutex.Unlock()
	manager.register(context.Background())
	workerValue := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool {
		return worker.Health == "healthy" &&
			worker.Repositories[0].RetainedCount == protocol.MaxRetainedPerRepo-1
	})
	task := createTask(t, fixture.store, workerValue, "factory", "fail", 60)
	claim, err := fixture.store.Claim(context.Background(), manager.ID(), protocol.ClaimRequest{
		RequestID: "registration-handoff-claim", LeaseToken: strings.Repeat("h", 43),
	})
	if err != nil || claim == nil || claim.Task.ID != task.Task.ID {
		t.Fatalf("claim = %#v, %v", claim, err)
	}
	value, err := prepareWorktree(context.Background(), "git",
		filepath.Join(manager.dataDirectory, "worktrees"), manager.repositories[0], task.Task.ID, claim.Attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.manifests.create(attemptManifest{
		TaskID: claim.Task.ID, ExecutionID: claim.Execution.ID, AttemptID: claim.Attempt.ID,
		RepositoryID: claim.Repository.ID, RepositoryKey: manager.repositories[0].Key,
		RepositoryPath: manager.repositories[0].Path, RemoteIdentity: manager.repositories[0].RemoteIdentity,
		BaseCommit: value.BaseCommit, WorktreePath: value.Path, Branch: value.Branch,
		LeaseDeadline: claim.Attempt.LeaseExpiresAt, Lifecycle: manifestPreparing,
	}); err != nil {
		t.Fatal(err)
	}
	if err := addPreparedWorktree(context.Background(), "git", manager.repositories[0], value); err != nil {
		t.Fatal(err)
	}
	if err := manager.persistLifecycle(claim.Attempt.ID, manifestWorktreeCreated, nil); err != nil {
		t.Fatal(err)
	}

	blockNextRegistration.Store(true)
	registrationDone := make(chan struct{})
	go func() {
		manager.register(context.Background())
		close(registrationDone)
	}()
	select {
	case <-registrationReached:
	case <-time.After(5 * time.Second):
		t.Fatal("periodic registration did not reach the server")
	}
	finishDone := make(chan struct{})
	go func() {
		manager.finishWithWorktree(*claim, strings.Repeat("h", 43), &attemptHandle{
			expiry: claim.Attempt.LeaseExpiresAt,
		}, manager.repositories[0], value, "failed", "", "retained")
		close(finishDone)
	}()
	select {
	case <-finishDone:
		t.Fatal("capacity handoff overtook an in-flight registration")
	case <-time.After(100 * time.Millisecond):
	}
	attempt, err := fixture.store.Attempt(context.Background(), claim.Attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != "preparing" {
		t.Fatalf("attempt completed before registration serialization: %s", attempt.State)
	}
	close(releaseRegistration)
	select {
	case <-registrationDone:
	case <-time.After(5 * time.Second):
		t.Fatal("periodic registration did not finish")
	}
	select {
	case <-finishDone:
	case <-time.After(10 * time.Second):
		t.Fatal("capacity handoff did not finish")
	}
	registered := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool {
		return worker.Repositories[0].RetainedCount == protocol.MaxRetainedPerRepo &&
			len(worker.RetainedWorktrees) == 1
	})
	blocked := createTask(t, fixture.store, registered, "factory", "fail", 60)
	next, err := fixture.store.Claim(context.Background(), manager.ID(), protocol.ClaimRequest{
		RequestID: "registration-handoff-blocked", LeaseToken: strings.Repeat("i", 43),
	})
	if err != nil {
		t.Fatal(err)
	}
	if next != nil {
		t.Fatalf("claim exceeded retained-worktree cap: %#v", next)
	}
	blockedDetail, err := fixture.store.Task(context.Background(), blocked.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blockedDetail.Execution.State != "queued" {
		t.Fatalf("capacity-blocked task state = %s", blockedDetail.Execution.State)
	}
}

func TestManifestProcessIdentityIsDurableBeforeCodexStarts(t *testing.T) {
	startReached := make(chan struct{}, 1)
	releaseStart := make(chan struct{})
	fixture := newServerFixture(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if strings.HasSuffix(request.URL.Path, "/start") {
				select {
				case startReached <- struct{}{}:
				default:
				}
				<-releaseStart
			}
			next.ServeHTTP(writer, request)
		})
	})
	repository := createRepository(t, "manifest-start")
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	manager := newTestManager(t, fixture, codexPath, filepath.Join(t.TempDir(), "worker"),
		map[string]repositoryFixture{"factory": repository}, 1)
	startManager(t, manager)
	workerValue := waitForWorker(t, fixture.store, manager.ID(), func(worker protocol.Worker) bool {
		return worker.Health == "healthy"
	})
	task := createTask(t, fixture.store, workerValue, "factory", "success", 60)
	select {
	case <-startReached:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not reach attempt start")
	}
	detail, err := fixture.store.Task(context.Background(), task.Task.ID)
	if err != nil || len(detail.Attempts) != 1 {
		t.Fatalf("task detail = %#v, %v", detail, err)
	}
	manifest, err := manager.manifests.load(detail.Attempts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Lifecycle != manifestSupervisorReady || !manifest.ProcessActive ||
		manifest.SupervisorPID <= 0 || manifest.ProcessGroupID <= 0 ||
		manifest.SupervisorIdentity == "" || manifest.ProcessGroupIdentity == "" {
		t.Fatalf("process identity was not durable before start: %#v", manifest)
	}
	if entries, err := os.ReadDir(os.Getenv("FACTORY_TEST_CODEX_LOG")); err == nil && len(entries) != 0 {
		t.Fatalf("Codex started before durable identity and accepted start: %v", entries)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	close(releaseStart)
	waitForTaskState(t, fixture.store, task.Task.ID, "succeeded")
}

func TestKillingMainWorkerStopsCodexAndRestartDoesNotOverlap(t *testing.T) {
	fixture := newServerFixture(t, nil)
	repository := createRepository(t, "worker-crash")
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	dataDirectory := filepath.Join(t.TempDir(), "worker")
	configPath := filepath.Join(t.TempDir(), "worker.toml")
	configBody := fmt.Sprintf(`server = %q
name = "crash-worker"
max_concurrent = 1
data_directory = %q

[repositories.factory]
path = %q
`, fixture.server.URL, dataDirectory, repository.path)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_TEST_SUPERVISOR", "1")
	logDirectory := filepath.Join(t.TempDir(), "codex-log")
	t.Setenv("FACTORY_TEST_CODEX_LOG", logDirectory)
	initializer, err := New(config, testOptions(codexPath), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	workerID := initializer.ID()
	if err := initializer.Close(); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=TestWorkerManagerHelperProcess", "--")
	command.Env = append(os.Environ(),
		"FACTORY_TEST_MANAGER_HELPER=1",
		"FACTORY_TEST_MANAGER_CONFIG="+configPath,
		"FACTORY_TEST_MANAGER_CODEX="+codexPath,
	)
	var childOutput bytes.Buffer
	command.Stdout = &childOutput
	command.Stderr = &childOutput
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	childReaped := false
	t.Cleanup(func() {
		if !childReaped {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	workerValue := waitForWorker(t, fixture.store, workerID, func(worker protocol.Worker) bool {
		return worker.Health == "healthy"
	})
	task := createTask(t, fixture.store, workerValue, "factory", "fork", 60)
	running := waitForTaskState(t, fixture.store, task.Task.ID, "running")
	childPath := filepath.Join(logDirectory, running.Attempts[0].ID+".child")
	waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(childPath)
		return err == nil
	})
	codexChildPID := readPID(t, childPath)
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed worker process exited successfully")
	}
	childReaped = true
	waitForProcessGone(t, codexChildPID, 8*time.Second)

	restarted, err := New(config, testOptions(codexPath), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	_, restartedDone := startManager(t, restarted)
	waitForWorker(t, fixture.store, workerID, func(worker protocol.Worker) bool {
		return worker.Health == "healthy" && len(worker.RetainedWorktrees) == 1 &&
			strings.Contains(worker.RetainedWorktrees[0].CleanupCommand, "--config") &&
			strings.Contains(worker.RetainedWorktrees[0].CleanupCommand, configPath)
	})
	time.Sleep(300 * time.Millisecond)
	detail, err := fixture.store.Task(context.Background(), task.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Attempts) != 1 {
		t.Fatalf("worker restart created overlapping attempts: %#v", detail.Attempts)
	}
	_ = restartedDone
}

func TestSupervisorStopsAtLastRenewalWithoutPipeClosure(t *testing.T) {
	t.Setenv("FACTORY_TEST_SUPERVISOR", "1")
	logDirectory := t.TempDir()
	t.Setenv("FACTORY_TEST_CODEX_LOG", logDirectory)
	repository := createRepository(t, "renewal-deadline")
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	process, err := startSupervisor(
		[]string{os.Args[0], "-test.run=TestWorkerSupervisorHelperProcess", "--"},
		supervisorInit{
			RuntimeExecutable: codexPath, Worktree: repository.path,
			ResultPath: filepath.Join(t.TempDir(), "result"),
			Prompt:     "FAKE_MODE=fork", TimeoutSeconds: 60,
		}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.awaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := process.renew(time.Now().Add(6 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := process.send("start"); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(logDirectory, filepath.Base(repository.path)+".child")
	waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(childPath)
		return err == nil
	})
	childPID := readPID(t, childPath)
	started := time.Now()
	waitForProcessGone(t, childPID, 8*time.Second)
	if elapsed := time.Since(started); elapsed > 7*time.Second {
		t.Fatalf("supervisor exceeded the last renewal deadline and grace: %s", elapsed)
	}
	if process.control == nil {
		t.Fatal("test closed the parent control pipe before deadline enforcement")
	}
	_ = process.closeControl()
}
