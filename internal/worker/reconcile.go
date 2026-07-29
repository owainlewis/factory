package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

type worktreeInspection struct {
	Repository Repository
	PathExists bool
	Registered bool
	Entry      gitWorktreeEntry
	Status     string
}

type reconciliationRetryError struct{ error }
type reconciliationUnsafeError struct{ error }
type worktreeMismatchError struct{ error }

func retryReconciliation(err error) error {
	if err == nil {
		return nil
	}
	return reconciliationRetryError{error: err}
}

func unsafeReconciliation(err error) error {
	if err == nil {
		return nil
	}
	return reconciliationUnsafeError{error: err}
}

func worktreeMismatch(message string) error {
	return worktreeMismatchError{error: errors.New(message)}
}

func reconciliationNeedsRetry(err error) bool {
	var retry reconciliationRetryError
	return errors.As(err, &retry)
}

func isWorktreeMismatch(err error) bool {
	var mismatch worktreeMismatchError
	return errors.As(err, &mismatch)
}

func (manager *Manager) reconcile(ctx context.Context) error {
	manifests, err := manager.manifests.loadAll()
	var reconciliationErrors []error
	if err != nil {
		reconciliationErrors = append(reconciliationErrors, unsafeReconciliation(err))
	}
	for _, manifest := range manifests {
		if err := manager.reconcileManifest(ctx, manifest); err != nil {
			reconciliationErrors = append(reconciliationErrors,
				fmt.Errorf("attempt %s: %w", manifest.AttemptID, err))
		}
	}
	return errors.Join(reconciliationErrors...)
}

func (manager *Manager) reconcileManifest(ctx context.Context, manifest attemptManifest) error {
	if manifest.ProcessActive {
		if err := stopManifestProcesses(manifest); err != nil {
			_, _ = manager.manifests.update(manifest.AttemptID, func(value *attemptManifest) error {
				value.Lifecycle = manifestInconsistent
				value.RetentionReason = boundedText(err.Error(), 1000)
				return nil
			})
			return unsafeReconciliation(err)
		}
		updated, err := manager.manifests.update(manifest.AttemptID, func(value *attemptManifest) error {
			value.ProcessActive = false
			return nil
		})
		if err != nil {
			return retryReconciliation(fmt.Errorf("record stopped process group: %w", err))
		}
		manifest = updated
	}

	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	attempt, err := manager.client.attempt(requestContext, manifest.AttemptID)
	cancel()
	if err != nil {
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.Status < 500 {
			return unsafeReconciliation(fmt.Errorf("read control-plane attempt during reconciliation: %w", err))
		}
		return retryReconciliation(fmt.Errorf("read control-plane attempt during reconciliation: %w", err))
	}
	if err := verifyServerAttempt(manifest, attempt); err != nil {
		_, persistErr := manager.manifests.update(manifest.AttemptID, func(value *attemptManifest) error {
			value.Lifecycle = manifestInconsistent
			value.RetentionReason = boundedText(err.Error(), 1000)
			return nil
		})
		return errors.Join(unsafeReconciliation(err), retryReconciliation(persistErr))
	}

	inspection, inspectErr := inspectManifestWorktree(ctx, manager.options.GitExecutable, manager.dataDirectory, manifest)
	if inspectErr != nil {
		if !isWorktreeMismatch(inspectErr) {
			return retryReconciliation(inspectErr)
		}
		_, persistErr := manager.manifests.update(manifest.AttemptID, func(value *attemptManifest) error {
			value.Lifecycle = manifestInconsistent
			value.RetentionReason = boundedText(inspectErr.Error(), 1000)
			return nil
		})
		return errors.Join(unsafeReconciliation(inspectErr), retryReconciliation(persistErr))
	}

	switch {
	case manifest.Lifecycle == manifestCleanupStarted && !inspection.PathExists && !inspection.Registered:
		_, err = manager.manifests.update(manifest.AttemptID, func(value *attemptManifest) error {
			value.Lifecycle = manifestCleaned
			value.CleanupResult = "worktree was already absent during startup cleanup recovery"
			value.RetentionReason = ""
			return nil
		})
		return retryReconciliation(err)
	case manifest.Lifecycle == manifestCleanupStarted && inspection.PathExists && inspection.Registered:
		if err := removeInspectedWorktree(ctx, manager.options.GitExecutable, inspection, true); err != nil {
			return retryReconciliation(err)
		}
		_, err = manager.manifests.update(manifest.AttemptID, func(value *attemptManifest) error {
			value.Lifecycle = manifestCleaned
			value.CleanupResult = "startup finished interrupted cleanup"
			value.RetentionReason = ""
			return nil
		})
		return retryReconciliation(err)
	case inspection.PathExists != inspection.Registered:
		reason := "worktree exists in only one of the filesystem and Git worktree registry"
		_, err = manager.manifests.update(manifest.AttemptID, func(value *attemptManifest) error {
			value.Lifecycle = manifestInconsistent
			value.RetentionReason = reason
			return nil
		})
		if err != nil {
			return retryReconciliation(err)
		}
		return unsafeReconciliation(errors.New(reason))
	case !inspection.PathExists && !inspection.Registered:
		lifecycle := manifestMissing
		reason := "previously created worktree is absent"
		if manifest.Lifecycle == manifestPreparing || manifest.Lifecycle == manifestNotCreated {
			lifecycle = manifestNotCreated
			reason = "worktree was never created"
		} else if manifest.Lifecycle == manifestCleaned {
			lifecycle = manifestCleaned
			reason = ""
		}
		_, err = manager.manifests.update(manifest.AttemptID, func(value *attemptManifest) error {
			value.Lifecycle = lifecycle
			value.RetentionReason = reason
			return nil
		})
		return retryReconciliation(err)
	default:
		if manifest.Lifecycle == manifestCleaned || manifest.Lifecycle == manifestNotCreated {
			reason := "worktree exists for a manifest recorded as absent"
			_, err = manager.manifests.update(manifest.AttemptID, func(value *attemptManifest) error {
				value.Lifecycle = manifestInconsistent
				value.RetentionReason = reason
				return nil
			})
			if err != nil {
				return retryReconciliation(err)
			}
			return unsafeReconciliation(errors.New(reason))
		}
		reason := firstNonEmpty(manifest.RetentionReason, "worktree retained after worker restart")
		updated, err := manager.manifests.update(manifest.AttemptID, func(value *attemptManifest) error {
			value.Lifecycle = manifestRetained
			value.RetentionReason = reason
			return nil
		})
		if err != nil {
			return retryReconciliation(err)
		}
		manager.recordRetained(updated)
		return nil
	}
}

func verifyServerAttempt(manifest attemptManifest, attempt protocol.Attempt) error {
	if attempt.ID != manifest.AttemptID || attempt.ExecutionID != manifest.ExecutionID ||
		attempt.WorkerID != manifest.WorkerID {
		return errors.New("control-plane attempt identity does not match the manifest")
	}
	if attempt.SupervisorPID != nil {
		if manifest.SupervisorPID != *attempt.SupervisorPID ||
			manifest.SupervisorIdentity != attempt.ProcessIdentity ||
			attempt.ProcessGroupID == nil || manifest.ProcessGroupID != *attempt.ProcessGroupID {
			return errors.New("control-plane process identity does not match the manifest")
		}
	} else if attempt.ProcessIdentity != "" || attempt.ProcessGroupID != nil {
		return errors.New("control-plane process identity is partial")
	}
	return nil
}

func stopManifestProcesses(manifest attemptManifest) error {
	var stopErrors []error
	if processGroupAlive(int(manifest.ProcessGroupID)) {
		if err := stopOwnedProcessGroup(int(manifest.ProcessGroupID), manifest.ProcessGroupIdentity, terminationGrace); err != nil {
			stopErrors = append(stopErrors, fmt.Errorf("stop recorded Codex process group: %w", err))
		}
	}
	if processGroupAlive(int(manifest.SupervisorPID)) {
		if err := stopOwnedProcessGroup(int(manifest.SupervisorPID), manifest.SupervisorIdentity, terminationGrace); err != nil {
			stopErrors = append(stopErrors, fmt.Errorf("stop recorded supervisor process group: %w", err))
		}
	}
	return errors.Join(stopErrors...)
}

func inspectManifestWorktree(
	ctx context.Context,
	gitExecutable string,
	dataDirectory string,
	manifest attemptManifest,
) (worktreeInspection, error) {
	expectedPath := filepath.Join(dataDirectory, "worktrees", manifest.AttemptID)
	if manifest.WorktreePath != expectedPath {
		return worktreeInspection{}, worktreeMismatch("manifest worktree path escapes the V2 worktree root")
	}
	repository, err := resolveRepository(manifest.RepositoryKey, manifest.RepositoryPath, gitExecutable)
	if err != nil {
		return worktreeInspection{}, fmt.Errorf("verify manifest repository: %w", err)
	}
	if repository.Path != manifest.RepositoryPath || repository.RemoteIdentity != manifest.RemoteIdentity {
		return worktreeInspection{}, worktreeMismatch("manifest repository identity no longer matches")
	}
	inspection := worktreeInspection{Repository: repository}
	info, err := os.Lstat(manifest.WorktreePath)
	switch {
	case err == nil:
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return inspection, worktreeMismatch("manifest worktree path is not a real directory")
		}
		canonical, canonicalErr := filepath.EvalSymlinks(manifest.WorktreePath)
		if canonicalErr != nil || canonical != manifest.WorktreePath {
			return inspection, worktreeMismatch("manifest worktree path is not canonical")
		}
		inspection.PathExists = true
	case errors.Is(err, os.ErrNotExist):
	default:
		return inspection, fmt.Errorf("inspect manifest worktree path: %w", err)
	}

	entries, err := listGitWorktrees(ctx, gitExecutable, repository.Path)
	if err != nil {
		return inspection, err
	}
	for _, entry := range entries {
		entryPath, pathErr := filepath.Abs(entry.Path)
		if pathErr != nil {
			continue
		}
		entryPath = filepath.Clean(entryPath)
		if entryPath != manifest.WorktreePath {
			continue
		}
		if inspection.Registered {
			return inspection, worktreeMismatch("Git lists the manifest worktree more than once")
		}
		inspection.Registered = true
		inspection.Entry = entry
	}
	if inspection.Registered {
		if inspection.Entry.Branch != manifest.Branch {
			return inspection, worktreeMismatch("Git worktree branch does not match the manifest")
		}
		if !commitPattern.MatchString(inspection.Entry.Head) {
			return inspection, worktreeMismatch("Git worktree commit identity is invalid")
		}
	}
	if inspection.PathExists && inspection.Registered {
		stdout, stderr, statusErr := runGitCommand(ctx, gitExecutable, manifest.WorktreePath, 1<<20,
			"--no-optional-locks", "status", "--porcelain=v1")
		if statusErr != nil {
			return inspection, commandFailure("inspect retained worktree status", stdout, stderr, statusErr)
		}
		inspection.Status = strings.TrimSpace(string(stdout))
	}
	return inspection, nil
}

func removeInspectedWorktree(
	ctx context.Context,
	gitExecutable string,
	inspection worktreeInspection,
	force bool,
) error {
	if !inspection.PathExists || !inspection.Registered {
		return errors.New("refuse cleanup without matching filesystem and Git worktree identity")
	}
	arguments := []string{"worktree", "remove"}
	if force {
		arguments = append(arguments, "--force")
	}
	arguments = append(arguments, inspection.Entry.Path)
	stdout, stderr, err := runGitCommand(ctx, gitExecutable, inspection.Repository.Path, 256<<10, arguments...)
	if err != nil {
		return commandFailure("remove manifest-owned worktree", stdout, stderr, err)
	}
	if _, err := os.Lstat(inspection.Entry.Path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("Git reported cleanup success but the worktree path remains")
	}
	entries, err := listGitWorktrees(ctx, gitExecutable, inspection.Repository.Path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path, pathErr := filepath.Abs(entry.Path)
		if pathErr == nil && filepath.Clean(path) == inspection.Entry.Path {
			return errors.New("Git reported cleanup success but the worktree registration remains")
		}
	}
	return nil
}

func automaticCleanupEligible(
	ctx context.Context,
	gitExecutable string,
	manifest attemptManifest,
	inspection worktreeInspection,
) error {
	if !inspection.PathExists || !inspection.Registered {
		return errors.New("worktree identity is incomplete")
	}
	if inspection.Status != "" {
		return errors.New("worktree is dirty")
	}
	if inspection.Entry.Head == manifest.BaseCommit {
		return nil
	}
	stdout, stderr, err := runGitCommand(ctx, gitExecutable, manifest.WorktreePath, 256<<10,
		"for-each-ref", "--format=%(refname)", "--contains", inspection.Entry.Head, "refs/remotes")
	if err != nil {
		return commandFailure("inspect published refs", stdout, stderr, err)
	}
	if strings.TrimSpace(string(stdout)) == "" {
		return errors.New("worktree contains unpublished commits")
	}
	return nil
}

func (manager *Manager) cleanCompletedWorktree(attemptID string) error {
	manifest, err := manager.manifests.load(attemptID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*gitCommandTimeout)
	defer cancel()
	inspection, err := inspectManifestWorktree(ctx, manager.options.GitExecutable, manager.dataDirectory, manifest)
	if err != nil {
		return err
	}
	if err := automaticCleanupEligible(ctx, manager.options.GitExecutable, manifest, inspection); err != nil {
		return err
	}
	if err := manager.persistLifecycle(attemptID, manifestCleanupStarted, func(value *attemptManifest) {
		value.CleanupResult = "automatic cleanup started after successful completion"
	}); err != nil {
		return err
	}
	manifest, err = manager.manifests.load(attemptID)
	if err != nil {
		return err
	}
	inspection, err = inspectManifestWorktree(ctx, manager.options.GitExecutable, manager.dataDirectory, manifest)
	if err != nil {
		return err
	}
	if err := automaticCleanupEligible(ctx, manager.options.GitExecutable, manifest, inspection); err != nil {
		return err
	}
	if err := removeInspectedWorktree(ctx, manager.options.GitExecutable, inspection, false); err != nil {
		return err
	}
	return manager.persistLifecycle(attemptID, manifestCleaned, func(value *attemptManifest) {
		value.CleanupResult = "automatic cleanup completed"
		value.RetentionReason = ""
	})
}

func (manager *Manager) recordRetained(manifest attemptManifest) {
	cleanupCommand := "factory-worker cleanup " + manifest.AttemptID
	if manager.config.path != "" {
		cleanupCommand += " --config " + shellQuote(manager.config.path)
	}
	manager.stateMutex.Lock()
	if _, exists := manager.retained[manifest.AttemptID]; !exists {
		manager.retainedCounts[manifest.RemoteIdentity]++
	}
	manager.retained[manifest.AttemptID] = protocol.RetainedWorktree{
		AttemptID: manifest.AttemptID, RepositoryID: manifest.RepositoryID,
		Path: manifest.WorktreePath, Reason: boundedText(manifest.RetentionReason, 1000),
		CleanupCommand: cleanupCommand,
	}
	manager.stateMutex.Unlock()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (manager *Manager) persistLifecycle(attemptID, lifecycle string, change func(*attemptManifest)) error {
	_, err := manager.manifests.update(attemptID, func(manifest *attemptManifest) error {
		manifest.Lifecycle = lifecycle
		if change != nil {
			change(manifest)
		}
		return nil
	})
	return err
}
