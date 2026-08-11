package controlplane

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Store) materializeBlockedTargetForWorker(
	ctx context.Context,
	tx *sql.Tx,
	workerID string,
	now int64,
) error {
	type candidate struct {
		id, repositoryID, identity, runtime string
		admittedAt                          int64
	}
	var cursorAdmitted int64
	var cursorID string
	for {
		rows, err := tx.QueryContext(ctx, `
			SELECT target.id, target.repository_id, target.repository_identity,
			       target.required_runtime, target.admitted_at
			FROM work_targets target
			JOIN work ON work.id = target.work_id
			JOIN repositories repository ON repository.id = target.repository_id
			WHERE target.state = 'blocked'
			  AND (repository.centrally_managed = 0 OR repository.enabled = 1)
			  AND (? = '' OR target.admitted_at > ? OR (target.admitted_at = ? AND target.id > ?))
			  AND (
			      SELECT COUNT(*) FROM work_targets active
			      WHERE active.work_id = target.work_id AND active.state IN ('queued', 'preparing', 'running')
			  ) < json_extract(work.routine_snapshot, '$.concurrency_limit')
			ORDER BY target.admitted_at, target.id LIMIT 50
		`, cursorID, cursorAdmitted, cursorAdmitted, cursorID)
		if err != nil {
			return unavailable(err)
		}
		var candidates []candidate
		for rows.Next() {
			var value candidate
			if err := rows.Scan(&value.id, &value.repositoryID, &value.identity, &value.runtime, &value.admittedAt); err != nil {
				rows.Close()
				return unavailable(err)
			}
			candidates = append(candidates, value)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return unavailable(err)
		}
		if err := rows.Close(); err != nil {
			return unavailable(err)
		}
		for _, value := range candidates {
			selection, err := s.selectWorkTargetRoute(
				ctx, tx, value.repositoryID, value.identity, now, workerID, value.runtime,
			)
			if err != nil {
				if serviceErrorCode(err, "no_eligible_worker") || serviceErrorCode(err, "repository_not_managed") {
					continue
				}
				return err
			}
			executionID, err := newID()
			if err != nil {
				return unavailable(err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO executions(id, work_target_id, assigned_worker_id, required_runtime, state, created_at, updated_at)
				VALUES (?, ?, ?, ?, 'queued', ?, ?)
			`, executionID, value.id, selection.workerID, value.runtime, now, now); err != nil {
				return unavailable(err)
			}
			result, err := tx.ExecContext(ctx, `
				UPDATE work_targets SET state = 'queued', blocked_reason = NULL, assigned_worker_id = ?
				WHERE id = ? AND state = 'blocked'
			`, selection.workerID, value.id)
			if err != nil {
				return unavailable(err)
			}
			if changed, _ := result.RowsAffected(); changed != 1 {
				return conflict("target_route_conflict", "Target routing state changed before assignment")
			}
			return nil
		}
		if len(candidates) < 50 {
			return nil
		}
		last := candidates[len(candidates)-1]
		cursorAdmitted, cursorID = last.admittedAt, last.id
	}
}

func (s *Store) rerouteQueuedTargetForWorker(
	ctx context.Context,
	tx *sql.Tx,
	workerID string,
	now int64,
) error {
	type candidate struct {
		targetID, executionID, repositoryID, identity, runtime, assigned string
		admittedAt                                                       int64
	}
	var cursorAdmitted int64
	var cursorID string
	for {
		rows, err := tx.QueryContext(ctx, `
			SELECT target.id, execution.id, target.repository_id, target.repository_identity,
			       target.required_runtime, execution.assigned_worker_id, target.admitted_at
			FROM work_targets target
			JOIN executions execution ON execution.work_target_id = target.id
			WHERE target.state = 'queued' AND execution.state = 'queued'
			  AND execution.assigned_worker_id != ?
			  AND (? = '' OR target.admitted_at > ? OR (target.admitted_at = ? AND target.id > ?))
			ORDER BY target.admitted_at, target.id LIMIT 50
		`, workerID, cursorID, cursorAdmitted, cursorAdmitted, cursorID)
		if err != nil {
			return unavailable(err)
		}
		var candidates []candidate
		for rows.Next() {
			var value candidate
			if err := rows.Scan(&value.targetID, &value.executionID, &value.repositoryID, &value.identity,
				&value.runtime, &value.assigned, &value.admittedAt); err != nil {
				rows.Close()
				return unavailable(err)
			}
			candidates = append(candidates, value)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return unavailable(err)
		}
		if err := rows.Close(); err != nil {
			return unavailable(err)
		}
		for _, value := range candidates {
			if _, err := s.selectWorkTargetRoute(ctx, tx, value.repositoryID, value.identity, now, value.assigned, value.runtime); err == nil {
				continue
			} else if !serviceErrorCode(err, "no_eligible_worker") && !serviceErrorCode(err, "repository_not_managed") {
				return err
			}
			selection, err := s.selectWorkTargetRoute(ctx, tx, value.repositoryID, value.identity, now, workerID, value.runtime)
			if err != nil {
				var service *ServiceError
				if errors.As(err, &service) && (service.Code == "no_eligible_worker" || service.Code == "repository_not_managed") {
					continue
				}
				return err
			}
			result, err := tx.ExecContext(ctx, `
				UPDATE executions SET assigned_worker_id = ?, updated_at = ?
				WHERE id = ? AND state = 'queued' AND assigned_worker_id = ?
			`, selection.workerID, now, value.executionID, value.assigned)
			if err != nil {
				return unavailable(err)
			}
			if changed, _ := result.RowsAffected(); changed == 1 {
				if _, err := tx.ExecContext(ctx, `UPDATE work_targets SET assigned_worker_id = ? WHERE id = ?`, selection.workerID, value.targetID); err != nil {
					return unavailable(err)
				}
				return nil
			}
		}
		if len(candidates) < 50 {
			return nil
		}
		last := candidates[len(candidates)-1]
		cursorAdmitted, cursorID = last.admittedAt, last.targetID
	}
}

func updateWorkLifecycle(ctx context.Context, tx *sql.Tx, executionID string, now int64) error {
	var workID string
	err := tx.QueryRowContext(ctx, `
		SELECT target.work_id
		FROM executions execution
		JOIN work_targets target ON target.id = execution.work_target_id
		WHERE execution.id = ?
	`, executionID).Scan(&workID)
	if err != nil {
		return unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE work SET updated_at = ?, terminal_at = CASE
			WHEN NOT EXISTS (
				SELECT 1 FROM work_targets target
				WHERE target.work_id = work.id
				  AND target.state IN ('blocked','queued','preparing','running')
			) THEN ? ELSE NULL END
		WHERE id = ?
	`, now, now, workID); err != nil {
		return unavailable(err)
	}
	return nil
}
