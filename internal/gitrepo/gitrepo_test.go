package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddWorktreeCreatesDetachedWorkspace(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	worktree := filepath.Join(dir, "worktrees", "run-1")

	git(t, dir, "init", "-b", "main", repo)
	git(t, repo, "config", "user.email", "test@example.com")
	git(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "README.md")
	git(t, repo, "commit", "-m", "initial")
	git(t, repo, "remote", "add", "origin", repo)
	git(t, repo, "fetch", "origin", "main:refs/remotes/origin/main")

	if err := AddWorktree(context.Background(), repo, worktree, "main"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(worktree, "README.md")); err != nil {
		t.Fatal(err)
	}
	head := gitOutput(t, worktree, "branch", "--show-current")
	if head != "" {
		t.Fatalf("worktree branch = %q, want detached", head)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}
