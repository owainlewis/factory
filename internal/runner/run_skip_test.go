package runner

import (
	"context"
	"testing"

	"github.com/owainlewis/factory/internal/config"
)

// When a repo is already locked by another run, Run must skip cleanly: it
// records a final status and returns no error, rather than blocking or
// touching the repo.
func TestRunSkipsWhenRepoLocked(t *testing.T) {
	dataDir := t.TempDir()
	app := &App{
		cfg: config.Config{
			Factory: config.FactoryConfig{DataDir: dataDir},
			Repos: map[string]config.RepoConfig{
				"push": {URL: "git@github.com:owner/push.git", Branch: "main", Agent: "claude"},
			},
		},
	}

	// Hold the lock as if another run owns it.
	lock, busy, err := app.tryAcquireRepoLock("push")
	if err != nil {
		t.Fatal(err)
	}
	if busy {
		t.Fatal("setup acquire should not be busy")
	}
	defer lock.Release()

	record, err := app.Run(context.Background(), "push", "standards-check", ModePlan)
	if err != nil {
		t.Fatalf("Run should skip cleanly, got error: %v", err)
	}
	if record.Status != "skipped" {
		t.Fatalf("expected status skipped, got %q", record.Status)
	}
	if record.FinishedAt.IsZero() {
		t.Fatal("skipped run record must have a final timestamp")
	}
}
