package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const manifestSchemaVersion = 1

const (
	manifestPreparing       = "preparing"
	manifestWorktreeCreated = "worktree_created"
	manifestSupervisorReady = "supervisor_ready"
	manifestRunning         = "running"
	manifestCompleted       = "completed"
	manifestRetained        = "retained"
	manifestNotCreated      = "not_created"
	manifestInconsistent    = "inconsistent"
	manifestMissing         = "missing"
	manifestCleanupStarted  = "cleanup_started"
	manifestCleaned         = "cleaned"
)

const (
	cleanupIntentAutomatic = "automatic"
	cleanupIntentOperator  = "operator_confirmed"
)

type attemptManifest struct {
	SchemaVersion int `json:"schema_version"`

	WorkerID    string `json:"worker_id"`
	TaskID      string `json:"task_id"`
	ExecutionID string `json:"execution_id"`
	AttemptID   string `json:"attempt_id"`

	RepositoryID   string `json:"repository_id"`
	RepositoryKey  string `json:"repository_key"`
	RepositoryPath string `json:"repository_path"`
	RemoteIdentity string `json:"remote_identity"`
	BaseCommit     string `json:"base_commit"`
	WorktreePath   string `json:"worktree_path"`
	Branch         string `json:"branch"`

	SupervisorPID        int64     `json:"supervisor_pid,omitempty"`
	SupervisorIdentity   string    `json:"supervisor_identity,omitempty"`
	ProcessGroupID       int64     `json:"process_group_id,omitempty"`
	ProcessGroupIdentity string    `json:"process_group_identity,omitempty"`
	ProcessActive        bool      `json:"process_active"`
	LeaseDeadline        time.Time `json:"lease_deadline,omitempty"`

	Lifecycle       string    `json:"lifecycle"`
	TerminalState   string    `json:"terminal_state,omitempty"`
	RetentionReason string    `json:"retention_reason,omitempty"`
	CleanupIntent   string    `json:"cleanup_intent,omitempty"`
	CleanupResult   string    `json:"cleanup_result,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type manifestStore struct {
	dataDirectory string
	workerID      string
	now           func() time.Time

	mutex          sync.Mutex
	hook           func(string) error
	directoryReady bool
}

func newManifestStore(dataDirectory, workerID string) *manifestStore {
	return &manifestStore{dataDirectory: dataDirectory, workerID: workerID, now: time.Now}
}

func (store *manifestStore) attemptsDirectory() string {
	return filepath.Join(store.dataDirectory, "attempts")
}

func (store *manifestStore) path(attemptID string) (string, error) {
	if !uuidPattern.MatchString(attemptID) {
		return "", errors.New("invalid attempt ID")
	}
	return filepath.Join(store.attemptsDirectory(), attemptID+".json"), nil
}

func (store *manifestStore) create(manifest attemptManifest) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	path, err := store.path(manifest.AttemptID)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("attempt manifest already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect attempt manifest: %w", err)
	}
	now := store.now().UTC()
	manifest.SchemaVersion = manifestSchemaVersion
	manifest.WorkerID = store.workerID
	manifest.CreatedAt = now
	manifest.UpdatedAt = now
	if err := store.validate(manifest); err != nil {
		return err
	}
	return store.writeLocked(path, manifest)
}

func (store *manifestStore) update(attemptID string, change func(*attemptManifest) error) (attemptManifest, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	path, err := store.path(attemptID)
	if err != nil {
		return attemptManifest{}, err
	}
	manifest, err := store.readLocked(path)
	if err != nil {
		return attemptManifest{}, err
	}
	if err := change(&manifest); err != nil {
		return attemptManifest{}, err
	}
	manifest.UpdatedAt = store.now().UTC()
	if err := store.validate(manifest); err != nil {
		return attemptManifest{}, err
	}
	if err := store.writeLocked(path, manifest); err != nil {
		return attemptManifest{}, err
	}
	return manifest, nil
}

func (store *manifestStore) load(attemptID string) (attemptManifest, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	path, err := store.path(attemptID)
	if err != nil {
		return attemptManifest{}, err
	}
	return store.readLocked(path)
}

func (store *manifestStore) loadAll() ([]attemptManifest, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	directory, err := store.ensureDirectory()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read attempt manifests: %w", err)
	}
	manifests := make([]attemptManifest, 0, len(entries))
	removedTemporary := false
	var loadErrors []error
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") && strings.HasSuffix(entry.Name(), ".tmp") {
			info, infoErr := entry.Info()
			if infoErr != nil {
				loadErrors = append(loadErrors, fmt.Errorf("inspect stale temporary manifest: %w", infoErr))
				continue
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				loadErrors = append(loadErrors, fmt.Errorf("unsafe stale temporary manifest entry: %s", entry.Name()))
				continue
			}
			if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
				loadErrors = append(loadErrors, fmt.Errorf("remove stale temporary manifest: %w", err))
				continue
			}
			removedTemporary = true
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			loadErrors = append(loadErrors,
				fmt.Errorf("unexpected entry in attempt manifest directory: %s", entry.Name()))
			continue
		}
		attemptID := strings.TrimSuffix(entry.Name(), ".json")
		path, pathErr := store.path(attemptID)
		if pathErr != nil {
			loadErrors = append(loadErrors, fmt.Errorf("invalid attempt manifest filename %q", entry.Name()))
			continue
		}
		manifest, readErr := store.readLocked(path)
		if readErr != nil {
			loadErrors = append(loadErrors, fmt.Errorf("%s: %w", entry.Name(), readErr))
			continue
		}
		manifests = append(manifests, manifest)
	}
	if removedTemporary {
		if err := syncDirectory(directory); err != nil {
			loadErrors = append(loadErrors, fmt.Errorf("sync stale manifest cleanup: %w", err))
		}
	}
	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].AttemptID < manifests[j].AttemptID
	})
	return manifests, errors.Join(loadErrors...)
}

func (store *manifestStore) readLocked(path string) (attemptManifest, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return attemptManifest{}, errors.New("attempt manifest does not exist")
		}
		return attemptManifest{}, fmt.Errorf("inspect attempt manifest: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return attemptManifest{}, errors.New("attempt manifest must be a regular non-symlink file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return attemptManifest{}, errors.New("attempt manifest permissions must not allow group or other access")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return attemptManifest{}, fmt.Errorf("read attempt manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var manifest attemptManifest
	if err := decoder.Decode(&manifest); err != nil {
		return attemptManifest{}, fmt.Errorf("decode attempt manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return attemptManifest{}, errors.New("attempt manifest contains trailing JSON")
	}
	if err := store.validate(manifest); err != nil {
		return attemptManifest{}, err
	}
	return manifest, nil
}

func (store *manifestStore) writeLocked(path string, manifest attemptManifest) error {
	directory, err := store.ensureDirectory()
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode attempt manifest: %w", err)
	}
	body = append(body, '\n')
	file, err := os.CreateTemp(directory, "."+manifest.AttemptID+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary attempt manifest: %w", err)
	}
	temporary := file.Name()
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("protect temporary attempt manifest: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		return fmt.Errorf("write temporary attempt manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temporary attempt manifest: %w", err)
	}
	if err := store.callHook("after_file_sync"); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary attempt manifest: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace attempt manifest: %w", err)
	}
	removeTemporary = false
	if err := store.callHook("after_rename"); err != nil {
		return err
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync attempt manifest directory: %w", err)
	}
	return store.callHook("after_directory_sync")
}

func (store *manifestStore) ensureDirectory() (string, error) {
	directory := store.attemptsDirectory()
	if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return "", fmt.Errorf("create attempt manifest directory: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("inspect attempt manifest directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", fmt.Errorf("inspect attempt manifest directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("attempt manifest directory must be a real directory, not a symlink")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", fmt.Errorf("protect attempt manifest directory: %w", err)
	}
	if !store.directoryReady {
		if err := store.callHook("before_attempts_parent_sync"); err != nil {
			return "", err
		}
		if err := syncDirectory(store.dataDirectory); err != nil {
			return "", fmt.Errorf("sync worker data directory after creating attempts: %w", err)
		}
		if err := store.callHook("after_attempts_parent_sync"); err != nil {
			return "", err
		}
		store.directoryReady = true
	}
	return directory, nil
}

func (store *manifestStore) callHook(stage string) error {
	if store.hook == nil {
		return nil
	}
	return store.hook(stage)
}

func (store *manifestStore) validate(manifest attemptManifest) error {
	if manifest.SchemaVersion != manifestSchemaVersion {
		return fmt.Errorf("unsupported attempt manifest schema version %d", manifest.SchemaVersion)
	}
	for name, value := range map[string]string{
		"worker_id": manifest.WorkerID, "task_id": manifest.TaskID,
		"execution_id": manifest.ExecutionID, "attempt_id": manifest.AttemptID,
		"repository_id": manifest.RepositoryID,
	} {
		if !uuidPattern.MatchString(value) {
			return fmt.Errorf("attempt manifest %s is not a valid UUID", name)
		}
	}
	if manifest.WorkerID != store.workerID {
		return errors.New("attempt manifest belongs to a different worker")
	}
	if strings.TrimSpace(manifest.RepositoryKey) == "" || strings.TrimSpace(manifest.RemoteIdentity) == "" {
		return errors.New("attempt manifest repository identity is incomplete")
	}
	if !filepath.IsAbs(manifest.RepositoryPath) || filepath.Clean(manifest.RepositoryPath) != manifest.RepositoryPath {
		return errors.New("attempt manifest repository path is not canonical")
	}
	if !commitPattern.MatchString(manifest.BaseCommit) {
		return errors.New("attempt manifest base commit is invalid")
	}
	expectedPath := filepath.Join(store.dataDirectory, "worktrees", manifest.AttemptID)
	if manifest.WorktreePath != expectedPath {
		return errors.New("attempt manifest worktree path is not the owned V2 path")
	}
	expectedBranch := "factory-v2/" + manifest.TaskID[:12] + "-" + manifest.AttemptID[:12]
	if manifest.Branch != expectedBranch {
		return errors.New("attempt manifest branch does not match its task and attempt")
	}
	allowedLifecycle := map[string]bool{
		manifestPreparing: true, manifestWorktreeCreated: true, manifestSupervisorReady: true,
		manifestRunning: true, manifestCompleted: true, manifestRetained: true,
		manifestNotCreated: true, manifestInconsistent: true, manifestMissing: true,
		manifestCleanupStarted: true, manifestCleaned: true,
	}
	if !allowedLifecycle[manifest.Lifecycle] {
		return fmt.Errorf("attempt manifest lifecycle %q is invalid", manifest.Lifecycle)
	}
	if manifest.CleanupIntent != "" &&
		manifest.CleanupIntent != cleanupIntentAutomatic &&
		manifest.CleanupIntent != cleanupIntentOperator {
		return fmt.Errorf("attempt manifest cleanup intent %q is invalid", manifest.CleanupIntent)
	}
	processEmpty := manifest.SupervisorPID == 0 && manifest.SupervisorIdentity == "" &&
		manifest.ProcessGroupID == 0 && manifest.ProcessGroupIdentity == ""
	processComplete := manifest.SupervisorPID > 0 && manifest.SupervisorIdentity != "" &&
		manifest.ProcessGroupID > 0 && manifest.ProcessGroupIdentity != ""
	if !processEmpty && !processComplete {
		return errors.New("attempt manifest process identity is partial")
	}
	if manifest.ProcessActive && !processComplete {
		return errors.New("attempt manifest active process identity is incomplete")
	}
	if manifest.CreatedAt.IsZero() || manifest.UpdatedAt.IsZero() {
		return errors.New("attempt manifest timestamps are incomplete")
	}
	return nil
}
