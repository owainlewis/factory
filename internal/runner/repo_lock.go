package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type repoLock struct {
	path string
}

func (a *App) acquireRepoLock(ctx context.Context, repoName string) (repoLock, error) {
	lockDir := filepath.Join(a.cfg.Factory.DataDir, "locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return repoLock{}, err
	}

	lock := repoLock{path: filepath.Join(lockDir, lockFileName(repoName))}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		err := os.Mkdir(lock.path, 0o755)
		if err == nil {
			owner := fmt.Sprintf("pid: %d\nrepo: %s\n", os.Getpid(), repoName)
			_ = os.WriteFile(filepath.Join(lock.path, "owner"), []byte(owner), 0o644)
			return lock, nil
		}
		if !os.IsExist(err) {
			return repoLock{}, err
		}

		select {
		case <-ctx.Done():
			return repoLock{}, fmt.Errorf("repo %q is locked: %w", repoName, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (l repoLock) Release() error {
	if l.path == "" {
		return nil
	}
	return os.RemoveAll(l.path)
}

func lockFileName(repoName string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, repoName) + ".lock"
}
