package controlplane

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

const RunnerEnrollmentLifetime = 10 * time.Minute

func randomRunnerSecret(prefix string) (string, error) {
	body := make([]byte, 32)
	if _, err := rand.Read(body); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(body), nil
}

func (s *Store) CreateRunnerEnrollment(ctx context.Context, workerID string) (protocol.RunnerEnrollment, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || len(workerID) > 200 {
		return protocol.RunnerEnrollment{}, invalid("invalid_worker_id", "worker_id is required and must be at most 200 bytes")
	}
	token, err := randomRunnerSecret("factory_enroll_")
	if err != nil {
		return protocol.RunnerEnrollment{}, unavailable(err)
	}
	id, err := newID()
	if err != nil {
		return protocol.RunnerEnrollment{}, unavailable(err)
	}
	now := s.now().UTC()
	expiresAt := now.Add(RunnerEnrollmentLifetime)
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO runner_enrollments(id, worker_id, token_digest, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, id, workerID, digestToken(token), expiresAt.UnixMilli(), now.UnixMilli()); err != nil {
		return protocol.RunnerEnrollment{}, unavailable(err)
	}
	return protocol.RunnerEnrollment{WorkerID: workerID, EnrollmentToken: token, ExpiresAt: expiresAt}, nil
}

func (s *Store) ExchangeRunnerEnrollment(
	ctx context.Context,
	workerID, enrollmentToken, credential string,
) (protocol.RunnerCredential, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || len(workerID) > 200 || len(enrollmentToken) < 32 || len(enrollmentToken) > 1024 ||
		len(credential) < 32 || len(credential) > 1024 || credential != strings.TrimSpace(credential) ||
		!strings.HasPrefix(credential, "factory_runner_") {
		return protocol.RunnerCredential{}, unauthorizedRunner()
	}
	now := s.now().UTC().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.RunnerCredential{}, unavailable(err)
	}
	defer tx.Rollback()
	var enrollmentID string
	var usedAt sql.NullInt64
	var expiresAt int64
	err = tx.QueryRowContext(ctx, `
		SELECT id, used_at, expires_at FROM runner_enrollments
		WHERE worker_id = ? AND token_digest = ?
	`, workerID, digestToken(enrollmentToken)).Scan(&enrollmentID, &usedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.RunnerCredential{}, unauthorizedRunner()
	}
	if err != nil {
		return protocol.RunnerCredential{}, unavailable(err)
	}
	if usedAt.Valid {
		var storedDigest []byte
		err = tx.QueryRowContext(ctx, `
			SELECT token_digest FROM remote_runner_credentials WHERE worker_id = ?
		`, workerID).Scan(&storedDigest)
		if err != nil || !equalDigest(storedDigest, digestToken(credential)) {
			return protocol.RunnerCredential{}, unauthorizedRunner()
		}
		if err := tx.Commit(); err != nil {
			return protocol.RunnerCredential{}, unavailable(err)
		}
		return protocol.RunnerCredential{Credential: credential}, nil
	}
	if expiresAt < now {
		return protocol.RunnerCredential{}, unauthorizedRunner()
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE runner_enrollments SET used_at = ?
		WHERE id = ? AND used_at IS NULL AND expires_at >= ?
	`, now, enrollmentID, now)
	if err != nil {
		return protocol.RunnerCredential{}, unavailable(err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return protocol.RunnerCredential{}, unavailable(err)
	} else if count != 1 {
		return protocol.RunnerCredential{}, unauthorizedRunner()
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO remote_runner_credentials(worker_id, token_digest, created_at, last_used_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(worker_id) DO UPDATE SET
			token_digest = excluded.token_digest,
			created_at = excluded.created_at,
			last_used_at = excluded.last_used_at
	`, workerID, digestToken(credential), now, now); err != nil {
		return protocol.RunnerCredential{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.RunnerCredential{}, unavailable(err)
	}
	return protocol.RunnerCredential{Credential: credential}, nil
}

func (s *Store) AuthenticateRunnerCredential(ctx context.Context, credential string) (string, error) {
	if len(credential) < 32 || len(credential) > 1024 {
		return "", unauthorizedRunner()
	}
	var workerID string
	err := s.db.QueryRowContext(ctx, `
		SELECT worker_id FROM remote_runner_credentials WHERE token_digest = ?
	`, digestToken(credential)).Scan(&workerID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", unauthorizedRunner()
	}
	if err != nil {
		return "", unavailable(err)
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE remote_runner_credentials SET last_used_at = ? WHERE worker_id = ?
	`, s.now().UTC().UnixMilli(), workerID); err != nil {
		return "", unavailable(err)
	}
	return workerID, nil
}

func unauthorizedRunner() error {
	return &ServiceError{Code: "runner_unauthorized", Message: "valid Runner credentials are required", Status: 401}
}
