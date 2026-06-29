package runner

import (
	"context"
	"errors"
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

func TestLockFileNameUsesSafeCharacters(t *testing.T) {
	name := lockFileName("owner/repo with spaces")
	if name != "owner_repo_with_spaces.lock" {
		t.Fatalf("lock name = %q", name)
	}
	if strings.Contains(name, "/") {
		t.Fatalf("lock name contains path separator: %q", name)
	}
}
