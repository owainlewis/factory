package controlplane

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
	"github.com/owainlewis/factory/migrations"
	_ "modernc.org/sqlite"
)

var ErrNotFound = &ServiceError{Code: "not_found", Message: "resource not found", Status: 404}

type ServiceError struct {
	Code    string
	Message string
	Status  int
	Err     error
}

func (e *ServiceError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *ServiceError) Unwrap() error { return e.Err }

func conflict(code, message string) error {
	return &ServiceError{Code: code, Message: message, Status: 409}
}

func invalid(code, message string) error {
	return &ServiceError{Code: code, Message: message, Status: 400}
}

type Store struct {
	db         *sql.DB
	now        func() time.Time
	sweepEvery time.Duration
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if path != ":memory:" {
		if err := prepareDatabasePath(path); err != nil {
			return nil, err
		}
	}

	dsn := "file::memory:?cache=shared"
	if path != ":memory:" {
		u := &url.URL{Scheme: "file", Path: path}
		dsn = u.String()
		if strings.Contains(dsn, "?") {
			dsn += "&"
		} else {
			dsn += "?"
		}
		dsn += "_pragma=busy_timeout%285000%29&_pragma=foreign_keys%281%29&_pragma=journal_mode%28WAL%29&_txlock=immediate"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(8)
	store := &Store{db: db, now: time.Now, sweepEvery: 5 * time.Second}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func prepareDatabasePath(path string) error {
	info, err := os.Lstat(path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect database: %w", err)
	}
	if exists && !info.Mode().IsRegular() {
		return fmt.Errorf("database must be a regular non-symlink file: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	marker := path + ".v2-control-plane"
	if exists {
		return validateDatabaseMarker(marker)
	}
	err = createDatabaseMarker(marker)
	if errors.Is(err, os.ErrExist) {
		return validateDatabaseMarker(marker)
	}
	return err
}

type databaseMarkerFile interface {
	WriteString(string) (int, error)
	Sync() error
	Close() error
}

func createDatabaseMarker(marker string) error {
	return createDatabaseMarkerWith(marker, func(path string) (databaseMarkerFile, error) {
		return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	})
}

func createDatabaseMarkerWith(marker string, open func(string) (databaseMarkerFile, error)) error {
	file, err := open(marker)
	if err != nil {
		return fmt.Errorf("create Factory V2 database marker: %w", err)
	}
	if _, err := file.WriteString("factory-v2-control-plane\n"); err != nil {
		return cleanFailedDatabaseMarker(
			file,
			marker,
			fmt.Errorf("write Factory V2 database marker: %w", err),
		)
	}
	if err := file.Sync(); err != nil {
		return cleanFailedDatabaseMarker(
			file,
			marker,
			fmt.Errorf("sync Factory V2 database marker: %w", err),
		)
	}
	if err := file.Close(); err != nil {
		closeErr := fmt.Errorf("close Factory V2 database marker: %w", err)
		if removeErr := os.Remove(marker); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(closeErr, fmt.Errorf("remove failed Factory V2 database marker: %w", removeErr))
		}
		return closeErr
	}
	return nil
}

func cleanFailedDatabaseMarker(file databaseMarkerFile, marker string, cause error) error {
	errs := []error{cause}
	if err := file.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close failed Factory V2 database marker: %w", err))
	}
	if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("remove failed Factory V2 database marker: %w", err))
	}
	return errors.Join(errs...)
}

func validateDatabaseMarker(marker string) error {
	body, err := os.ReadFile(marker)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("refusing an existing database without the Factory V2 control-plane marker")
	}
	if err != nil {
		return fmt.Errorf("read Factory V2 database marker: %w", err)
	}
	if string(body) != "factory-v2-control-plane\n" {
		return errors.New("refusing an existing database with an invalid Factory V2 control-plane marker")
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	names, err := migrations.Files.ReadDir(".")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Slice(names, func(i, j int) bool { return names[i].Name() < names[j].Name() })
	for version, entry := range names {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		v := version + 1
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, v).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		if exists != 0 {
			continue
		}
		body, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), err)
		}
		if _, err = tx.ExecContext(ctx, string(body)); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, v, time.Now().UnixMilli())
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (s *Store) Close() error {
	checkpointErr := retrySQLiteContention(func() error {
		_, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
		return err
	})
	closeErr := s.db.Close()
	if checkpointErr != nil {
		return checkpointErr
	}
	return closeErr
}

func retrySQLiteContention(operation func() error) error {
	deadline := time.Now().Add(time.Second)
	for {
		err := operation()
		if err == nil || !isSQLiteContention(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func isSQLiteContention(err error) bool {
	var coded interface{ Code() int }
	if !errors.As(err, &coded) {
		return false
	}
	// SQLite extended result codes retain the primary code in the low byte.
	code := coded.Code() & 0xff
	return code == 5 || code == 6 // SQLITE_BUSY or SQLITE_LOCKED
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func digestToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func validateToken(token string) error {
	if len(token) < 32 || len(token) > 1024 {
		return invalid("invalid_lease_token", "lease_token must contain at least 32 bytes")
	}
	return nil
}

func normalizeRemote(value string) string {
	return strings.TrimSpace(value)
}

func (s *Store) RegisterWorker(ctx context.Context, workerID string, input protocol.WorkerRegistration) (protocol.Worker, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || len(workerID) > 200 {
		return protocol.Worker{}, invalid("invalid_worker_id", "worker_id is required and must be at most 200 bytes")
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 200 {
		return protocol.Worker{}, invalid("invalid_worker", "worker name is required and must be at most 200 bytes")
	}
	if input.Capacity < 1 || input.Capacity > 4 || input.ActiveCount < 0 || input.ActiveCount > input.Capacity {
		return protocol.Worker{}, invalid("invalid_capacity", "capacity must be 1 through 4 and active_count cannot exceed it")
	}
	if input.Health != "healthy" && input.Health != "unhealthy" {
		return protocol.Worker{}, invalid("invalid_health", "health must be healthy or unhealthy")
	}
	if input.CapacityHandoffVersion < 0 || input.CapacityHandoffVersion > 1 {
		return protocol.Worker{}, invalid(
			"invalid_capacity_handoff", "capacity_handoff_version must be 0 or 1")
	}
	if len(input.Repositories) == 0 {
		return protocol.Worker{}, invalid("invalid_repositories", "at least one repository is required")
	}
	seenKeys := map[string]bool{}
	seenRemotes := map[string]bool{}
	for i := range input.Repositories {
		repo := &input.Repositories[i]
		repo.Key = strings.TrimSpace(repo.Key)
		repo.RemoteIdentity = normalizeRemote(repo.RemoteIdentity)
		if repo.Key == "" || repo.RemoteIdentity == "" || len(repo.Key) > 200 || len(repo.RemoteIdentity) > 2048 {
			return protocol.Worker{}, invalid("invalid_repository", "repository key and remote_identity are required")
		}
		if repo.RetainedCount < 0 {
			return protocol.Worker{}, invalid("invalid_repository", "retained_count cannot be negative")
		}
		if seenKeys[repo.Key] || seenRemotes[repo.RemoteIdentity] {
			return protocol.Worker{}, invalid("duplicate_repository", "repository keys and identities must be unique per worker")
		}
		seenKeys[repo.Key], seenRemotes[repo.RemoteIdentity] = true, true
	}
	retainedCounts := make(map[string]int)
	retainedAttemptIDs := make(map[string][]string)
	seenRetainedAttempts := make(map[string]string)
	hasUnattributedRetainedSummary := false
	for index := range input.RetainedWorktrees {
		worktree := &input.RetainedWorktrees[index]
		worktree.AttemptID = strings.TrimSpace(worktree.AttemptID)
		worktree.RepositoryID = strings.TrimSpace(worktree.RepositoryID)
		// Older API clients may send display-only summaries before they know the
		// control-plane repository ID. Preserve those summaries, but do not use
		// incomplete or duplicate entries as capacity-handoff evidence.
		if worktree.AttemptID == "" || worktree.RepositoryID == "" {
			hasUnattributedRetainedSummary = true
			continue
		}
		if repositoryID, seen := seenRetainedAttempts[worktree.AttemptID]; seen {
			if repositoryID != worktree.RepositoryID {
				hasUnattributedRetainedSummary = true
			}
			continue
		}
		seenRetainedAttempts[worktree.AttemptID] = worktree.RepositoryID
		retainedCounts[worktree.RepositoryID]++
		retainedAttemptIDs[worktree.RepositoryID] = append(
			retainedAttemptIDs[worktree.RepositoryID], worktree.AttemptID)
	}
	disposedAttemptIDs := make([]string, 0, len(input.DisposedAttemptIDs))
	seenDisposedAttempts := make(map[string]bool, len(input.DisposedAttemptIDs))
	for _, value := range input.DisposedAttemptIDs {
		attemptID := strings.TrimSpace(value)
		if attemptID == "" || len(attemptID) > 200 {
			return protocol.Worker{}, invalid(
				"invalid_disposed_attempts", "disposed attempt IDs must be non-empty and at most 200 bytes")
		}
		if !seenDisposedAttempts[attemptID] {
			seenDisposedAttempts[attemptID] = true
			disposedAttemptIDs = append(disposedAttemptIDs, attemptID)
		}
	}
	retained, err := json.Marshal(input.RetainedWorktrees)
	if err != nil || len(retained) > protocol.MaxBodyBytes {
		return protocol.Worker{}, invalid("invalid_retained_worktrees", "retained worktree summaries are too large")
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Worker{}, unavailable(err)
	}
	defer tx.Rollback()
	advertisedRepositoryIDs := make(map[string]bool, len(input.Repositories))
	for _, repo := range input.Repositories {
		var existingID string
		err := tx.QueryRowContext(ctx, `
			SELECT repository_id FROM worker_repositories
			WHERE worker_id = ? AND display_key = ?
		`, workerID, repo.Key).Scan(&existingID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return protocol.Worker{}, unavailable(err)
		}
		if err == nil {
			var identity string
			if err := tx.QueryRowContext(ctx, `SELECT remote_identity FROM repositories WHERE id = ?`, existingID).Scan(&identity); err != nil {
				return protocol.Worker{}, unavailable(err)
			}
			if identity != repo.RemoteIdentity {
				return protocol.Worker{}, conflict("repository_key_changed", "a repository key cannot be reassigned to a different remote identity")
			}
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO workers(id, name, worker_version, codex_version, capacity, active_count, health, retained_worktrees_json, registered_at, last_heartbeat)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, worker_version=excluded.worker_version, codex_version=excluded.codex_version,
			capacity=excluded.capacity, active_count=excluded.active_count, health=excluded.health,
			retained_worktrees_json=excluded.retained_worktrees_json, last_heartbeat=excluded.last_heartbeat
	`, workerID, input.Name, input.WorkerVersion, input.CodexVersion, input.Capacity, input.ActiveCount, input.Health, retained, now, now)
	if err != nil {
		return protocol.Worker{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worker_repositories SET advertised = 0, updated_at = ? WHERE worker_id = ?`, now, workerID); err != nil {
		return protocol.Worker{}, unavailable(err)
	}
	for _, repo := range input.Repositories {
		var repositoryID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM repositories WHERE remote_identity = ?`, repo.RemoteIdentity).Scan(&repositoryID)
		if errors.Is(err, sql.ErrNoRows) {
			repositoryID, err = newID()
			if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT INTO repositories(id, remote_identity, created_at) VALUES (?, ?, ?)`, repositoryID, repo.RemoteIdentity, now)
			}
		}
		if err != nil {
			return protocol.Worker{}, unavailable(err)
		}
		advertisedRepositoryIDs[repositoryID] = true
		effectiveRetainedCount := repo.RetainedCount
		if retainedCounts[repositoryID] > effectiveRetainedCount {
			effectiveRetainedCount = retainedCounts[repositoryID]
		}
		var existingKey string
		mappingErr := tx.QueryRowContext(ctx, `
			SELECT display_key FROM worker_repositories
			WHERE worker_id = ? AND repository_id = ?
		`, workerID, repositoryID).Scan(&existingKey)
		switch {
		case mappingErr == nil && existingKey != repo.Key:
			_, err = tx.ExecContext(ctx, `
				UPDATE worker_repositories
				SET display_key = ?, retained_count = ?, advertised = 1, updated_at = ?
				WHERE worker_id = ? AND repository_id = ?
			`, repo.Key, effectiveRetainedCount, now, workerID, repositoryID)
		case mappingErr == nil:
			_, err = tx.ExecContext(ctx, `
				UPDATE worker_repositories
				SET retained_count = ?, advertised = 1, updated_at = ?
				WHERE worker_id = ? AND repository_id = ?
			`, effectiveRetainedCount, now, workerID, repositoryID)
		case errors.Is(mappingErr, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx, `
				INSERT INTO worker_repositories(worker_id, display_key, repository_id, retained_count, advertised, updated_at)
				VALUES (?, ?, ?, ?, 1, ?)
			`, workerID, repo.Key, repositoryID, effectiveRetainedCount, now)
		default:
			err = mappingErr
		}
		if err != nil {
			return protocol.Worker{}, unavailable(err)
		}
	}
	canBulkAcknowledge := input.CapacityHandoffVersion == 0 && !hasUnattributedRetainedSummary
	for repositoryID := range retainedCounts {
		if !advertisedRepositoryIDs[repositoryID] {
			canBulkAcknowledge = false
		}
	}
	for _, attemptID := range disposedAttemptIDs {
		var ownerID string
		err := tx.QueryRowContext(ctx, `
			SELECT worker_id
			FROM attempts
			WHERE id = ?
		`, attemptID).Scan(&ownerID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && ownerID != workerID) {
			return protocol.Worker{}, invalid(
				"invalid_disposed_attempts", "disposed attempts must exist and be owned by this worker")
		}
		if err != nil {
			return protocol.Worker{}, unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE attempts
			SET capacity_acknowledged = 1
			WHERE id = ?
		`, attemptID); err != nil {
			return protocol.Worker{}, unavailable(err)
		}
	}
	for repositoryID := range advertisedRepositoryIDs {
		if _, err := tx.ExecContext(ctx, `
			UPDATE attempts
			SET capacity_acknowledged = 1
			WHERE worker_id = ?
			  AND capacity_acknowledged = 0
			  AND state IN ('succeeded', 'failed', 'cancelled', 'lost')
			  AND ? = 0
			  AND ?
			  AND execution_id IN (
			      SELECT e.id
			      FROM executions e
			      JOIN tasks t ON t.id = e.task_id
			      WHERE t.repository_id = ?
			  )
		`, workerID, input.ActiveCount, canBulkAcknowledge, repositoryID); err != nil {
			return protocol.Worker{}, unavailable(err)
		}
		for _, attemptID := range retainedAttemptIDs[repositoryID] {
			if _, err := tx.ExecContext(ctx, `
				UPDATE attempts
				SET capacity_acknowledged = 1
				WHERE id = ?
				  AND worker_id = ?
				  AND state IN ('succeeded', 'failed', 'cancelled', 'lost')
				  AND execution_id IN (
				      SELECT e.id
				      FROM executions e
				      JOIN tasks t ON t.id = e.task_id
				      WHERE t.repository_id = ?
				  )
			`, attemptID, workerID, repositoryID); err != nil {
				return protocol.Worker{}, unavailable(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return protocol.Worker{}, unavailable(err)
	}
	return s.Worker(ctx, workerID)
}

func (s *Store) Workers(ctx context.Context) ([]protocol.Worker, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.id, w.name, w.worker_version, w.codex_version, w.capacity, w.active_count, w.health,
		       w.retained_worktrees_json, w.registered_at, w.last_heartbeat,
		       COALESCE((
		           SELECT t.title
		           FROM executions e JOIN tasks t ON t.id = e.task_id
		           WHERE e.assigned_worker_id = w.id AND e.state IN ('preparing', 'running')
		           ORDER BY e.updated_at DESC, e.id DESC
		           LIMIT 1
		       ), '')
		FROM workers w ORDER BY w.registered_at, w.id
	`)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	var workers []protocol.Worker
	for rows.Next() {
		worker, err := scanWorker(rows, s.now())
		if err != nil {
			return nil, unavailable(err)
		}
		workers = append(workers, worker)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	if err := rows.Close(); err != nil {
		return nil, unavailable(err)
	}
	for index := range workers {
		repos, err := s.workerRepositories(ctx, workers[index].ID)
		if err != nil {
			return nil, err
		}
		workers[index].Repositories = repos
	}
	return workers, nil
}

func (s *Store) Worker(ctx context.Context, id string) (protocol.Worker, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT w.id, w.name, w.worker_version, w.codex_version, w.capacity, w.active_count, w.health,
		       w.retained_worktrees_json, w.registered_at, w.last_heartbeat,
		       COALESCE((
		           SELECT t.title
		           FROM executions e JOIN tasks t ON t.id = e.task_id
		           WHERE e.assigned_worker_id = w.id AND e.state IN ('preparing', 'running')
		           ORDER BY e.updated_at DESC, e.id DESC
		           LIMIT 1
		       ), '')
		FROM workers w WHERE w.id = ?
	`, id)
	worker, err := scanWorker(row, s.now())
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Worker{}, ErrNotFound
	}
	if err != nil {
		return protocol.Worker{}, unavailable(err)
	}
	worker.Repositories, err = s.workerRepositories(ctx, id)
	return worker, err
}

type scanner interface {
	Scan(...any) error
}

func scanWorker(row scanner, now time.Time) (protocol.Worker, error) {
	var worker protocol.Worker
	var retained []byte
	var registered, heartbeat int64
	if err := row.Scan(&worker.ID, &worker.Name, &worker.WorkerVersion, &worker.CodexVersion,
		&worker.Capacity, &worker.ActiveCount, &worker.Health, &retained, &registered, &heartbeat,
		&worker.CurrentTaskTitle); err != nil {
		return worker, err
	}
	if err := json.Unmarshal(retained, &worker.RetainedWorktrees); err != nil {
		return worker, err
	}
	worker.RegisteredAt = fromMillis(registered)
	worker.LastHeartbeat = fromMillis(heartbeat)
	worker.Online = now.Sub(worker.LastHeartbeat) <= protocol.WorkerOnlineWindow
	return worker, nil
}

func (s *Store) workerRepositories(ctx context.Context, workerID string) ([]protocol.Repository, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, wr.display_key, r.remote_identity, wr.retained_count
		FROM worker_repositories wr JOIN repositories r ON r.id = wr.repository_id
		WHERE wr.worker_id = ? AND wr.advertised = 1 ORDER BY wr.display_key
	`, workerID)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	var repos []protocol.Repository
	for rows.Next() {
		var repo protocol.Repository
		if err := rows.Scan(&repo.ID, &repo.Key, &repo.RemoteIdentity, &repo.RetainedCount); err != nil {
			return nil, unavailable(err)
		}
		repos = append(repos, repo)
	}
	return repos, rows.Err()
}

func (s *Store) CreateTask(ctx context.Context, input protocol.CreateTaskRequest) (protocol.TaskDetail, bool, error) {
	input.RequestKey = strings.TrimSpace(input.RequestKey)
	input.Title = strings.TrimSpace(input.Title)
	if input.RequestKey == "" || len(input.RequestKey) > 200 {
		return protocol.TaskDetail{}, false, invalid("invalid_request_key", "request_key is required")
	}
	if input.Title == "" || utf8.RuneCountInString(input.Title) > 200 {
		return protocol.TaskDetail{}, false, invalid("invalid_title", "title is required and limited to 200 Unicode characters")
	}
	if strings.TrimSpace(input.Description) == "" || len([]byte(input.Description)) > protocol.MaxDescriptionBytes {
		return protocol.TaskDetail{}, false, invalid("invalid_description", "description is required and limited to 64 KiB")
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = int(protocol.DefaultTimeout.Seconds())
	}
	if input.TimeoutSeconds < 1 || input.TimeoutSeconds > int(protocol.MaxTimeout/time.Second) {
		return protocol.TaskDetail{}, false, invalid("invalid_timeout", "timeout_seconds must be between 1 and 28800")
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.TaskDetail{}, false, unavailable(err)
	}
	defer tx.Rollback()
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE request_key = ?`, input.RequestKey).Scan(&existingID)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return protocol.TaskDetail{}, false, unavailable(err)
		}
		detail, err := s.Task(ctx, existingID)
		return detail, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.TaskDetail{}, false, unavailable(err)
	}
	var valid int
	err = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM worker_repositories
		WHERE worker_id = ? AND repository_id = ? AND advertised = 1
	`, input.WorkerID, input.RepositoryID).Scan(&valid)
	if err != nil {
		return protocol.TaskDetail{}, false, unavailable(err)
	}
	if valid == 0 {
		return protocol.TaskDetail{}, false, invalid("repository_not_advertised", "repository is not advertised by the assigned worker")
	}
	taskID, err := newID()
	if err != nil {
		return protocol.TaskDetail{}, false, unavailable(err)
	}
	executionID, err := newID()
	if err != nil {
		return protocol.TaskDetail{}, false, unavailable(err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO tasks(id, request_key, title, description, repository_id, timeout_seconds, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, taskID, input.RequestKey, input.Title, input.Description, input.RepositoryID, input.TimeoutSeconds, now)
	if err == nil {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO executions(id, task_id, assigned_worker_id, required_runtime, state, created_at, updated_at)
			VALUES (?, ?, ?, 'codex', 'queued', ?, ?)
		`, executionID, taskID, input.WorkerID, now, now)
	}
	if err != nil {
		return protocol.TaskDetail{}, false, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.TaskDetail{}, false, unavailable(err)
	}
	detail, err := s.Task(ctx, taskID)
	return detail, true, err
}

func (s *Store) Tasks(ctx context.Context) ([]protocol.Task, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.request_key, t.title, t.repository_id, t.timeout_seconds,
		       e.assigned_worker_id, e.state, t.created_at
		FROM tasks t JOIN executions e ON e.task_id = t.id
		ORDER BY t.created_at DESC, t.id DESC
	`)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	var tasks []protocol.Task
	for rows.Next() {
		task, err := scanTask(rows, false)
		if err != nil {
			return nil, unavailable(err)
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func scanTask(row scanner, detail bool) (protocol.Task, error) {
	var task protocol.Task
	var created int64
	var err error
	if detail {
		err = row.Scan(&task.ID, &task.RequestKey, &task.Title, &task.Description, &task.RepositoryID,
			&task.TimeoutSeconds, &task.WorkerID, &task.State, &created)
	} else {
		err = row.Scan(&task.ID, &task.RequestKey, &task.Title, &task.RepositoryID,
			&task.TimeoutSeconds, &task.WorkerID, &task.State, &created)
	}
	task.CreatedAt = fromMillis(created)
	if task.State == "preparing" {
		task.State = "running"
	}
	return task, err
}

func (s *Store) Task(ctx context.Context, id string) (protocol.TaskDetail, error) {
	var detail protocol.TaskDetail
	row := s.db.QueryRowContext(ctx, `
		SELECT t.id, t.request_key, t.title, t.description, t.repository_id, t.timeout_seconds,
		       e.assigned_worker_id, e.state, t.created_at
		FROM tasks t JOIN executions e ON e.task_id = t.id WHERE t.id = ?
	`, id)
	task, err := scanTask(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return detail, ErrNotFound
	}
	if err != nil {
		return detail, unavailable(err)
	}
	detail.Task = task
	row = s.db.QueryRowContext(ctx, `
		SELECT id, task_id, assigned_worker_id, required_runtime, state,
		       cancellation_requested, created_at, updated_at
		FROM executions WHERE task_id = ?
	`, id)
	detail.Execution, err = scanExecution(row)
	if err != nil {
		return detail, unavailable(err)
	}
	var advertised int
	err = s.db.QueryRowContext(ctx, `
		SELECT r.id, wr.display_key, r.remote_identity, wr.retained_count, wr.advertised
		FROM repositories r JOIN worker_repositories wr ON wr.repository_id = r.id
		WHERE r.id = ? AND wr.worker_id = ?
	`, detail.Task.RepositoryID, detail.Execution.AssignedWorkerID).Scan(
		&detail.Repository.ID, &detail.Repository.Key, &detail.Repository.RemoteIdentity,
		&detail.Repository.RetainedCount, &advertised)
	if err != nil {
		return detail, unavailable(err)
	}
	detail.RepositoryAvailable = advertised != 0
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, execution_id, worker_id, attempt_number, state, lease_expires_at,
		       supervisor_pid, process_identity, process_group_id, result, error,
		       started_at, completed_at, created_at
		FROM attempts WHERE execution_id = ? ORDER BY attempt_number
	`, detail.Execution.ID)
	if err != nil {
		return detail, unavailable(err)
	}
	defer rows.Close()
	for rows.Next() {
		attempt, err := scanAttempt(rows)
		if err != nil {
			return detail, unavailable(err)
		}
		detail.Attempts = append(detail.Attempts, attempt)
	}
	return detail, rows.Err()
}

func scanExecution(row scanner) (protocol.Execution, error) {
	var value protocol.Execution
	var cancel int
	var created, updated int64
	err := row.Scan(&value.ID, &value.TaskID, &value.AssignedWorkerID, &value.RequiredRuntime,
		&value.State, &cancel, &created, &updated)
	value.CancellationRequested = cancel != 0
	value.CreatedAt, value.UpdatedAt = fromMillis(created), fromMillis(updated)
	return value, err
}

func fromMillis(value int64) time.Time { return time.UnixMilli(value).UTC() }

func nullableTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	v := fromMillis(value.Int64)
	return &v
}

func scanAttempt(row scanner) (protocol.Attempt, error) {
	var value protocol.Attempt
	var expiry, created int64
	var supervisor, group sql.NullInt64
	var identity, result, failure sql.NullString
	var started, completed sql.NullInt64
	err := row.Scan(&value.ID, &value.ExecutionID, &value.WorkerID, &value.AttemptNumber,
		&value.State, &expiry, &supervisor, &identity, &group, &result, &failure,
		&started, &completed, &created)
	if supervisor.Valid {
		value.SupervisorPID = &supervisor.Int64
	}
	if group.Valid {
		value.ProcessGroupID = &group.Int64
	}
	value.ProcessIdentity, value.Result, value.Error = identity.String, result.String, failure.String
	value.LeaseExpiresAt, value.CreatedAt = fromMillis(expiry), fromMillis(created)
	value.StartedAt, value.CompletedAt = nullableTime(started), nullableTime(completed)
	return value, err
}

func unavailable(err error) error {
	return &ServiceError{Code: "storage_unavailable", Message: "control-plane storage is unavailable", Status: 503, Err: err}
}

func equalDigest(a, b []byte) bool { return bytes.Equal(a, b) }
