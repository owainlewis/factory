package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/config"
)

func TestRepoLockBlocksConcurrentAccess(t *testing.T) {
	app := &App{
		cfg: config.Config{
			Factory: config.FactoryConfig{DataDir: t.TempDir()},
		},
	}

	lock, err := app.acquireRepoLock(context.Background(), "push")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err = app.acquireRepoLock(ctx, "push")
	if err == nil {
		t.Fatal("expected locked repo to block")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func TestRepoLockCanBeReacquiredAfterRelease(t *testing.T) {
	app := &App{
		cfg: config.Config{
			Factory: config.FactoryConfig{DataDir: t.TempDir()},
		},
	}

	lock, err := app.acquireRepoLock(context.Background(), "push")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}

	lock, err = app.acquireRepoLock(context.Background(), "push")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
}

func TestTryAcquireReportsBusyWhenHeld(t *testing.T) {
	app := &App{
		cfg: config.Config{
			Factory: config.FactoryConfig{DataDir: t.TempDir()},
		},
	}

	lock, busy, err := app.tryAcquireRepoLock("push")
	if err != nil {
		t.Fatal(err)
	}
	if busy {
		t.Fatal("first acquire should not be busy")
	}
	defer lock.Release()

	_, busy, err = app.tryAcquireRepoLock("push")
	if err != nil {
		t.Fatal(err)
	}
	if !busy {
		t.Fatal("second acquire should report busy")
	}
}

func TestReclaimStaleLockFromDeadOwner(t *testing.T) {
	app := &App{
		cfg: config.Config{
			Factory: config.FactoryConfig{DataDir: t.TempDir()},
		},
	}

	// Simulate a lock left behind by a process that no longer exists.
	lockDir := filepath.Join(app.cfg.Factory.DataDir, "locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(lockDir, lockFileName("push"))
	if err := os.Mkdir(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	owner := fmt.Sprintf("pid: %d\nrepo: push\n", deadPID(t))
	if err := os.WriteFile(filepath.Join(stale, "owner"), []byte(owner), 0o644); err != nil {
		t.Fatal(err)
	}

	lock, busy, err := app.tryAcquireRepoLock("push")
	if err != nil {
		t.Fatal(err)
	}
	if busy {
		t.Fatal("stale lock from a dead owner should be reclaimed, not reported busy")
	}
	defer lock.Release()
}

func TestUnknownOwnerLockIsNotStolen(t *testing.T) {
	app := &App{
		cfg: config.Config{
			Factory: config.FactoryConfig{DataDir: t.TempDir()},
		},
	}

	lockDir := filepath.Join(app.cfg.Factory.DataDir, "locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	held := filepath.Join(lockDir, lockFileName("push"))
	if err := os.Mkdir(held, 0o755); err != nil {
		t.Fatal(err)
	}
	// No owner file: owner is unknown, so the lock must not be stolen.

	_, busy, err := app.tryAcquireRepoLock("push")
	if err != nil {
		t.Fatal(err)
	}
	if !busy {
		t.Fatal("lock with unknown owner must be treated as busy")
	}
}

// deadPID returns a process id that is not currently running.
func deadPID(t *testing.T) int {
	t.Helper()
	for pid := 999999; pid > 99000; pid-- {
		if !processAlive(pid) {
			return pid
		}
	}
	t.Fatal("could not find a dead pid")
	return 0
}

func TestLockFileNameUsesSafeCharacters(t *testing.T) {
	name := lockFileName("owner/repo with spaces")
	if name != "owner_repo_with_spaces.lock" {
		t.Fatalf("lock name = %q", name)
	}
	if strings.Contains(name, "/") {
		t.Fatalf("lock name contains path separator: %q", name)
	}
}
