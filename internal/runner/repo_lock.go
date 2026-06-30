package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type repoLock struct {
	path string
}

// errRepoBusy is returned by tryAcquireRepoLock when the repo is held by
// another live run.
var errRepoBusy = errors.New("repo is busy")

// acquireRepoLock blocks until the repo lock is held or ctx is done. A stale
// lock left by a dead process is reclaimed automatically.
func (a *App) acquireRepoLock(ctx context.Context, repoName string) (repoLock, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		lock, busy, err := a.tryAcquireRepoLock(repoName)
		if err != nil {
			return repoLock{}, err
		}
		if !busy {
			return lock, nil
		}

		select {
		case <-ctx.Done():
			return repoLock{}, fmt.Errorf("repo %q is locked: %w", repoName, ctx.Err())
		case <-ticker.C:
		}
	}
}

// tryAcquireRepoLock makes a single attempt to hold the repo lock. It returns
// busy=true when the lock is held by another live process. A stale lock left
// by a dead process is reclaimed and the attempt retried once.
func (a *App) tryAcquireRepoLock(repoName string) (repoLock, bool, error) {
	lockDir := filepath.Join(a.cfg.Factory.DataDir, "locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return repoLock{}, false, err
	}
	lock := repoLock{path: filepath.Join(lockDir, lockFileName(repoName))}

	for attempt := 0; attempt < 2; attempt++ {
		err := os.Mkdir(lock.path, 0o755)
		if err == nil {
			owner := fmt.Sprintf("pid: %d\nrepo: %s\n", os.Getpid(), repoName)
			_ = os.WriteFile(filepath.Join(lock.path, "owner"), []byte(owner), 0o644)
			return lock, false, nil
		}
		if !os.IsExist(err) {
			return repoLock{}, false, err
		}

		// The lock exists. Reclaim it only if its owner is gone.
		if attempt == 0 && a.reclaimStaleLock(lock.path) {
			continue
		}
		return repoLock{}, true, nil
	}
	return repoLock{}, true, nil
}

// reclaimStaleLock removes the lock directory if its recorded owner process is
// no longer alive. It returns true when the lock was reclaimed.
func (a *App) reclaimStaleLock(lockPath string) bool {
	pid, ok := lockOwnerPID(lockPath)
	if !ok {
		// Unknown owner: do not steal the lock automatically.
		return false
	}
	if processAlive(pid) {
		return false
	}
	return os.RemoveAll(lockPath) == nil
}

func lockOwnerPID(lockPath string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(lockPath, "owner"))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "pid:"); ok {
			pid, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil {
				return 0, false
			}
			return pid, true
		}
	}
	return 0, false
}

// processAlive reports whether a process with pid is currently running.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means the process exists but is owned by another user.
	return errors.Is(err, syscall.EPERM)
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
