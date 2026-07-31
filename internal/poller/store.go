package poller

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type observation struct {
	QueueID    string
	IssueKey   string
	RequestKey string
	Request    []byte
	TaskID     string
	State      string
}

type Store struct {
	db *sql.DB
}

const maxObservations = 10_000

func openStore(ctx context.Context, directory string) (*Store, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve poller data directory: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create poller data directory: %w", err)
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("protect poller data directory: %w", err)
	}
	path := filepath.Join(absolute, "poller.sqlite3")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("poller database must be a regular non-symlink file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect poller database: %w", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create poller database: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, fmt.Errorf("protect poller database: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close poller database bootstrap: %w", err)
	}
	databaseURL := &url.URL{Scheme: "file", Path: path}
	dsn := databaseURL.String() +
		"?_pragma=busy_timeout%285000%29&_pragma=journal_mode%28DELETE%29&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open poller database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (store *Store) migrate(ctx context.Context) error {
	if _, err := store.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS observations (
			queue_id TEXT NOT NULL,
			issue_key TEXT NOT NULL,
			request_key TEXT NOT NULL UNIQUE,
			request_json BLOB NOT NULL,
			task_id TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL CHECK (state IN ('pending', 'submitted')),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (queue_id, issue_key)
		);
		CREATE INDEX IF NOT EXISTS observations_pending
		ON observations(state, created_at, queue_id, issue_key);
		UPDATE observations SET request_json = X''
		WHERE state = 'submitted' AND length(request_json) > 0;
	`); err != nil {
		return fmt.Errorf("migrate poller database: %w", err)
	}
	return nil
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func (store *Store) observation(
	ctx context.Context,
	queueID string,
	issueKey string,
) (observation, bool, error) {
	var value observation
	err := store.db.QueryRowContext(ctx, `
		SELECT queue_id, issue_key, request_key, request_json, task_id, state
		FROM observations WHERE queue_id = ? AND issue_key = ?
	`, queueID, issueKey).Scan(
		&value.QueueID, &value.IssueKey, &value.RequestKey, &value.Request,
		&value.TaskID, &value.State,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return observation{}, false, nil
	}
	if err != nil {
		return observation{}, false, fmt.Errorf("read poller observation: %w", err)
	}
	return value, true, nil
}

func (store *Store) insertPending(ctx context.Context, value observation) error {
	return store.insertPendingWithLimit(ctx, value, maxObservations)
}

func (store *Store) insertPendingWithLimit(
	ctx context.Context,
	value observation,
	limit int,
) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin pending poller observation: %w", err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM observations`).Scan(&count); err != nil {
		return fmt.Errorf("count poller observations: %w", err)
	}
	if count >= limit {
		return fmt.Errorf(
			"poller observation limit of %d reached; stop the poller and archive its database before resetting the ledger",
			limit,
		)
	}
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO observations(
			queue_id, issue_key, request_key, request_json, state, created_at, updated_at
		) VALUES (?, ?, ?, ?, 'pending', ?, ?)
		ON CONFLICT(queue_id, issue_key) DO NOTHING
	`, value.QueueID, value.IssueKey, value.RequestKey, value.Request, now, now); err != nil {
		return fmt.Errorf("persist pending poller observation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit pending poller observation: %w", err)
	}
	return nil
}

func (store *Store) pending(ctx context.Context) ([]observation, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT queue_id, issue_key, request_key, request_json, task_id, state
		FROM observations WHERE state = 'pending'
		ORDER BY created_at, queue_id, issue_key
	`)
	if err != nil {
		return nil, fmt.Errorf("list pending poller observations: %w", err)
	}
	defer rows.Close()
	var values []observation
	for rows.Next() {
		var value observation
		if err := rows.Scan(
			&value.QueueID, &value.IssueKey, &value.RequestKey, &value.Request,
			&value.TaskID, &value.State,
		); err != nil {
			return nil, fmt.Errorf("scan pending poller observation: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read pending poller observations: %w", err)
	}
	return values, nil
}

func (store *Store) markSubmitted(ctx context.Context, requestKey, taskID string) error {
	result, err := store.db.ExecContext(ctx, `
		UPDATE observations
		SET state = 'submitted', task_id = ?, request_json = X'', updated_at = ?
		WHERE request_key = ? AND state = 'pending'
	`, taskID, time.Now().UnixMilli(), requestKey)
	if err != nil {
		return fmt.Errorf("record submitted poller task: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("pending poller observation changed before submission was recorded")
	}
	return nil
}
