package gitrepo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func Ensure(ctx context.Context, path string, url string, branch string) error {
	if isGitRepo(path) {
		if err := run(ctx, path, "git", "fetch", "--prune", "origin"); err != nil {
			return err
		}
		if err := run(ctx, path, "git", "checkout", branch); err != nil {
			return err
		}
		return run(ctx, path, "git", "pull", "--ff-only", "origin", branch)
	}

	if url == "" {
		return fmt.Errorf("repo path %s does not exist and no url is configured", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return run(ctx, "", "git", "clone", "--branch", branch, url, path)
}

func AddWorktree(ctx context.Context, repoPath string, worktreePath string, branch string) error {
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return err
	}
	return run(ctx, repoPath, "git", "worktree", "add", "--detach", worktreePath, "origin/"+branch)
}

func isGitRepo(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && info != nil
}

func run(ctx context.Context, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v failed: %w\n%s", name, args, err, string(out))
	}
	return nil
}
