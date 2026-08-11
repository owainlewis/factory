package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

const routineSchedulePollInterval = 10 * time.Second

func (s *Store) RunRoutineScheduler(ctx context.Context, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	ticker := time.NewTicker(routineSchedulePollInterval)
	defer ticker.Stop()
	for {
		if err := s.AdmitDueRoutines(ctx, 20); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("routine_schedule_admission_failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Store) AdmitDueRoutines(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		return invalid("invalid_schedule_batch", "schedule batch must be between 1 and 100")
	}
	for index := 0; index < limit; index++ {
		id, due, snapshot, found, err := s.claimDueRoutine(ctx)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		requestKey := fmt.Sprintf("schedule:%s:%d:%d", id, snapshot.Generation, due.UnixMilli())
		_, _, admissionErr := s.admitRoutine(ctx, id, "schedule", requestKey, &due, &snapshot)
		if admissionErr == nil {
			if err := s.finishRoutineOccurrence(ctx, id, due, true, nil); err != nil {
				return err
			}
			continue
		}
		if err := s.finishRoutineOccurrence(ctx, id, due, false, admissionErr); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) claimDueRoutine(ctx context.Context) (string, time.Time, protocol.RoutineSnapshot, bool, error) {
	var empty protocol.RoutineSnapshot
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, empty, false, unavailable(err)
	}
	defer tx.Rollback()
	now := s.now()
	var id string
	var pendingDue, nextDue sql.NullInt64
	var pendingSnapshot []byte
	err = tx.QueryRowContext(ctx, `
		SELECT id, pending_due_at, next_due_at, pending_snapshot_json
		FROM routines
		WHERE migration_only = 0 AND archived = 0 AND schedule_enabled = 1
		  AND (
		      (pending_due_at IS NOT NULL AND schedule_retry_at IS NOT NULL AND schedule_retry_at <= ?)
		      OR (pending_due_at IS NULL AND next_due_at IS NOT NULL AND next_due_at <= ?)
		  )
		ORDER BY CASE WHEN pending_due_at IS NOT NULL THEN 0 ELSE 1 END,
		         COALESCE(schedule_retry_at, next_due_at), id
		LIMIT 1
	`, now.UnixMilli(), now.UnixMilli()).Scan(&id, &pendingDue, &nextDue, &pendingSnapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, empty, false, nil
	}
	if err != nil {
		return "", time.Time{}, empty, false, unavailable(err)
	}
	if pendingDue.Valid {
		snapshot, err := loadPendingRoutineSnapshot(ctx, tx, id, pendingSnapshot)
		if err != nil {
			return "", time.Time{}, empty, false, err
		}
		if err := tx.Commit(); err != nil {
			return "", time.Time{}, empty, false, unavailable(err)
		}
		return id, fromMillis(pendingDue.Int64), snapshot, true, nil
	}
	snapshot, err := loadCurrentRoutineSnapshot(ctx, tx, id)
	if err != nil {
		return "", time.Time{}, empty, false, err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", time.Time{}, empty, false, unavailable(err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE routines SET pending_due_at = next_due_at, pending_snapshot_json = ?,
		       schedule_retry_at = ?, schedule_retry_count = 0
		WHERE id = ? AND pending_due_at IS NULL AND next_due_at = ?
	`, encoded, now.UnixMilli(), id, nextDue.Int64)
	if err != nil {
		return "", time.Time{}, empty, false, unavailable(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return "", time.Time{}, empty, false, nil
	}
	if err := tx.Commit(); err != nil {
		return "", time.Time{}, empty, false, unavailable(err)
	}
	return id, fromMillis(nextDue.Int64), snapshot, true, nil
}

func loadCurrentRoutineSnapshot(ctx context.Context, tx *sql.Tx, id string) (protocol.RoutineSnapshot, error) {
	var snapshot protocol.RoutineSnapshot
	err := tx.QueryRowContext(ctx, `
		SELECT id, name, prompt, runtime, timeout_seconds, concurrency_limit, generation,
		       cron, timezone
		FROM routines WHERE id = ?
	`, id).Scan(&snapshot.ID, &snapshot.Name, &snapshot.Prompt, &snapshot.Runtime,
		&snapshot.TimeoutSeconds, &snapshot.ConcurrencyLimit, &snapshot.Generation,
		&snapshot.ScheduleCron, &snapshot.ScheduleTimezone)
	if err != nil {
		return snapshot, unavailable(err)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT repository.id, repository.remote_identity
		FROM routine_repositories selected
		JOIN repositories repository ON repository.id = selected.repository_id
		WHERE selected.routine_id = ? ORDER BY selected.position
	`, id)
	if err != nil {
		return snapshot, unavailable(err)
	}
	for rows.Next() {
		var repository protocol.RoutineRepository
		if err := rows.Scan(&repository.ID, &repository.RemoteIdentity); err != nil {
			rows.Close()
			return snapshot, unavailable(err)
		}
		snapshot.Repositories = append(snapshot.Repositories, repository)
	}
	if err := rows.Close(); err != nil {
		return snapshot, unavailable(err)
	}
	return snapshot, nil
}

func loadPendingRoutineSnapshot(ctx context.Context, tx *sql.Tx, id string, encoded []byte) (protocol.RoutineSnapshot, error) {
	var snapshot protocol.RoutineSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return snapshot, unavailable(errors.New("stored Routine pending snapshot is invalid"))
	}
	if snapshot.ID == "" {
		snapshot.ID = id
	}
	if len(snapshot.Repositories) != 0 {
		return snapshot, nil
	}
	var legacy struct {
		RepositoryIDs []string `json:"repository_ids"`
	}
	if err := json.Unmarshal(encoded, &legacy); err != nil {
		return snapshot, unavailable(err)
	}
	for _, repositoryID := range legacy.RepositoryIDs {
		var repository protocol.RoutineRepository
		err := tx.QueryRowContext(ctx, `SELECT id, remote_identity FROM repositories WHERE id = ?`, repositoryID).
			Scan(&repository.ID, &repository.RemoteIdentity)
		if err != nil {
			return snapshot, conflict("routine_repository_missing", "a frozen scheduled repository no longer exists")
		}
		snapshot.Repositories = append(snapshot.Repositories, repository)
	}
	return snapshot, nil
}

func (s *Store) finishRoutineOccurrence(ctx context.Context, id string, due time.Time, admitted bool, admissionErr error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err)
	}
	defer tx.Rollback()
	var cron, timezone string
	var retryCount, enabled, archived int
	err = tx.QueryRowContext(ctx, `
		SELECT cron, timezone, schedule_retry_count, schedule_enabled, archived FROM routines
		WHERE id = ? AND pending_due_at = ?
	`, id, due.UnixMilli()).Scan(&cron, &timezone, &retryCount, &enabled, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return unavailable(err)
	}
	if !admitted && (enabled == 0 || archived != 0) {
		if err := tx.Commit(); err != nil {
			return unavailable(err)
		}
		return nil
	}
	now := s.now()
	if admitted {
		schedule, _, _, err := parseCronSchedule(cron, timezone)
		if err != nil {
			return unavailable(err)
		}
		next, err := schedule.Next(now)
		if err != nil {
			return unavailable(err)
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE routines SET pending_due_at = NULL, pending_snapshot_json = NULL,
			       schedule_retry_at = NULL, schedule_retry_count = 0, next_due_at = ?,
			       schedule_health_status = 'healthy', schedule_health_code = '',
			       schedule_health_message = '', updated_at = ?
			WHERE id = ? AND pending_due_at = ?
		`, next.UnixMilli(), now.UnixMilli(), id, due.UnixMilli())
		if err != nil {
			return unavailable(err)
		}
	} else if permanentRoutineAdmissionError(admissionErr) {
		var service *ServiceError
		code := "schedule_admission_blocked"
		if errors.As(admissionErr, &service) {
			code = service.Code
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE routines SET schedule_retry_at = NULL, schedule_health_status = 'blocked',
			       schedule_health_code = ?, schedule_health_message = ?, updated_at = ?
			WHERE id = ? AND pending_due_at = ?
		`, code, admissionErr.Error(), now.UnixMilli(), id, due.UnixMilli())
		if err != nil {
			return unavailable(err)
		}
	} else {
		retryCount++
		delay := time.Duration(retryCount) * time.Minute
		if delay > 15*time.Minute {
			delay = 15 * time.Minute
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE routines SET schedule_retry_at = ?, schedule_retry_count = ?,
			       schedule_health_status = 'error', schedule_health_code = 'transient_admission_error',
			       schedule_health_message = ?, updated_at = ?
			WHERE id = ? AND pending_due_at = ?
		`, now.Add(delay).UnixMilli(), retryCount, admissionErr.Error(), now.UnixMilli(), id, due.UnixMilli())
		if err != nil {
			return unavailable(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err)
	}
	return nil
}

func permanentRoutineAdmissionError(err error) bool {
	var service *ServiceError
	if !errors.As(err, &service) {
		return false
	}
	switch service.Code {
	case "routine_repository_missing", "routine_repository_unavailable", "routine_has_no_repositories",
		"routine_archived", "agent_prompt_too_large", "invalid_routine_schedule":
		return true
	default:
		return service.Status >= 400 && service.Status < 500 && service.Status != 409
	}
}

func (s *Store) DiscardRoutineOccurrence(
	ctx context.Context,
	id string,
	input protocol.DiscardRoutineOccurrenceRequest,
) (protocol.Routine, error) {
	if input.PendingDueAt.IsZero() {
		return protocol.Routine{}, invalid("pending_due_at_required", "pending_due_at is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Routine{}, unavailable(err)
	}
	defer tx.Rollback()
	var cron, timezone, health string
	var enabled int
	var pending, lastDiscarded sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		SELECT cron, timezone, schedule_enabled, pending_due_at, last_discarded_due_at,
		       schedule_health_status FROM routines
		WHERE id = ? AND migration_only = 0
	`, id).Scan(&cron, &timezone, &enabled, &pending, &lastDiscarded, &health)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Routine{}, ErrNotFound
	}
	if err != nil {
		return protocol.Routine{}, unavailable(err)
	}
	requestedDue := input.PendingDueAt.UTC().UnixMilli()
	if !pending.Valid {
		if lastDiscarded.Valid && lastDiscarded.Int64 == requestedDue {
			if err := tx.Commit(); err != nil {
				return protocol.Routine{}, unavailable(err)
			}
			return s.Routine(ctx, id)
		}
		return protocol.Routine{}, conflict("no_pending_occurrence", "this Routine has no pending occurrence")
	}
	if pending.Int64 != requestedDue {
		return protocol.Routine{}, conflict("pending_occurrence_changed", "the pending occurrence changed; refresh and try again")
	}
	if health != "blocked" && health != "disabled" {
		return protocol.Routine{}, conflict("pending_occurrence_not_discardable", "only a blocked or paused occurrence can be discarded")
	}
	var next any
	if enabled != 0 {
		schedule, _, _, err := parseCronSchedule(cron, timezone)
		if err != nil {
			return protocol.Routine{}, unavailable(err)
		}
		instant, err := schedule.Next(s.now())
		if err != nil {
			return protocol.Routine{}, unavailable(err)
		}
		next = instant.UnixMilli()
	}
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		UPDATE routines SET pending_due_at = NULL, pending_snapshot_json = NULL,
		       schedule_retry_at = NULL, schedule_retry_count = 0, next_due_at = ?,
		       last_discarded_due_at = ?,
		       schedule_health_status = CASE WHEN schedule_enabled = 1 THEN 'healthy' ELSE 'disabled' END,
		       schedule_health_code = '', schedule_health_message = '', updated_at = ?
		WHERE id = ? AND pending_due_at = ?
	`, next, pending.Int64, now, id, pending.Int64); err != nil {
		return protocol.Routine{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.Routine{}, unavailable(err)
	}
	return s.Routine(ctx, id)
}
