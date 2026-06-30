package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/owainlewis/factory/internal/agent"
	"github.com/owainlewis/factory/internal/config"
)

// fakeAdapter simulates an agent that edits files inside the run directory.
type fakeAdapter struct{}

func (fakeAdapter) Run(_ context.Context, spec agent.RunSpec) (agent.RunResult, error) {
	// Write a file into the run directory to mimic an agent edit.
	if err := os.WriteFile(filepath.Join(spec.RepoPath, "agent-edit.txt"), []byte("edited\n"), 0o644); err != nil {
		return agent.RunResult{}, err
	}
	return agent.RunResult{Status: "success"}, nil
}

// TestExecuteRunUsesIsolatedWorktree proves an execute run edits an isolated
// worktree, not the base checkout, and records the worktree path and branch.
func TestExecuteRunUsesIsolatedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dataDir := t.TempDir()
	origin := newBareOrigin(t)

	app := &App{
		cfg: config.Config{
			Factory: config.FactoryConfig{DataDir: dataDir},
			Repos: map[string]config.RepoConfig{
				"demo": {URL: origin, Branch: "main", Agent: "fake"},
			},
		},
		newAdapter: func(string) (agent.Adapter, error) { return fakeAdapter{}, nil },
	}

	record, err := app.Run(context.Background(), "demo", "hello", ModeExecute)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if record.Status != "success" {
		t.Fatalf("status = %q, want success", record.Status)
	}
	if record.Worktree == "" {
		t.Fatal("execute run record must include a worktree path")
	}
	if record.Branch != "main" {
		t.Fatalf("branch = %q, want main", record.Branch)
	}

	// The agent's edit must land in the worktree.
	if _, err := os.Stat(filepath.Join(record.Worktree, "agent-edit.txt")); err != nil {
		t.Fatalf("expected agent edit in worktree: %v", err)
	}

	// The base checkout must stay clean.
	baseCheckout := filepath.Join(dataDir, "repos", "demo")
	if _, err := os.Stat(filepath.Join(baseCheckout, "agent-edit.txt")); !os.IsNotExist(err) {
		t.Fatalf("base checkout was dirtied by the run (err=%v)", err)
	}
}

// TestPlanRunHasNoWorktree confirms plan runs do not allocate a worktree.
func TestPlanRunHasNoWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dataDir := t.TempDir()
	origin := newBareOrigin(t)

	app := &App{
		cfg: config.Config{
			Factory: config.FactoryConfig{DataDir: dataDir},
			Repos: map[string]config.RepoConfig{
				"demo": {URL: origin, Branch: "main", Agent: "fake"},
			},
		},
		newAdapter: func(string) (agent.Adapter, error) { return fakeAdapter{}, nil },
	}

	record, err := app.Run(context.Background(), "demo", "hello", ModePlan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if record.Worktree != "" {
		t.Fatalf("plan run must not allocate a worktree, got %q", record.Worktree)
	}
}

// newBareOrigin creates a bare git repo with one commit on main and returns its
// path, suitable as a clone URL.
func newBareOrigin(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")

	gitInit(t, origin, "--bare")
	gitInit(t, seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "init")
	git(t, seed, "branch", "-M", "main")
	git(t, seed, "remote", "add", "origin", origin)
	git(t, seed, "push", "origin", "main")
	return origin
}

func gitInit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, append([]string{"init"}, args...)...)
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{
		"-c", "user.email=test@example.com",
		"-c", "user.name=Test",
		"-c", "init.defaultBranch=main",
		"-C", dir,
	}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
