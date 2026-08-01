package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

const legacyResumeCleanupTimeout = 5 * time.Second

func (s *Store) ResumeLegacyPollerOccurrence(
	ctx context.Context,
	occurrenceID string,
) (protocol.AutomationOccurrence, error) {
	occurrenceID = strings.TrimSpace(occurrenceID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.AutomationOccurrence{}, unavailable(err)
	}
	defer tx.Rollback()
	var requestJSON []byte
	var repositoryID, repositoryIdentity, state string
	err = tx.QueryRowContext(ctx, `
		SELECT occurrence.legacy_task_request_json, occurrence.repository_id,
		       occurrence.repository_identity, occurrence.state
		FROM automation_occurrences occurrence
		JOIN legacy_poller_observations legacy ON legacy.occurrence_id = occurrence.id
		WHERE occurrence.id = ?
	`, occurrenceID).Scan(&requestJSON, &repositoryID, &repositoryIdentity, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.AutomationOccurrence{}, ErrNotFound
	}
	if err != nil {
		return protocol.AutomationOccurrence{}, unavailable(err)
	}
	if state == "dispatching" {
		return protocol.AutomationOccurrence{}, conflict("occurrence_dispatching", "the pending legacy observation is already being resumed")
	}
	if state != "pending" && state != "failed" {
		return protocol.AutomationOccurrence{}, conflict("occurrence_not_pending", "only an unresolved imported pending observation can be resumed")
	}
	observation := legacyPollerObservation{Request: requestJSON}
	var request protocol.CreateTaskRequest
	if err := decodeAutomationJSON(requestJSON, &request); err != nil {
		return protocol.AutomationOccurrence{}, unavailable(errors.New("stored legacy task request is invalid: " + err.Error()))
	}
	observation.RequestKey = strings.TrimSpace(request.RequestKey)
	if _, err := validateLegacyPendingRequest(observation, repositoryIdentity); err != nil {
		return protocol.AutomationOccurrence{}, err
	}
	now := s.now().UnixMilli()
	result, err := tx.ExecContext(ctx, `
		UPDATE automation_occurrences
		SET state = 'dispatching', diagnostic = 'legacy_resume_in_progress', updated_at = ?
		WHERE id = ? AND state IN ('pending', 'failed') AND legacy_task_request_json IS NOT NULL
	`, now, occurrenceID)
	if err != nil {
		return protocol.AutomationOccurrence{}, unavailable(err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return protocol.AutomationOccurrence{}, conflict("occurrence_not_pending", "the imported pending observation changed before Resume")
	}
	if err := tx.Commit(); err != nil {
		return protocol.AutomationOccurrence{}, unavailable(err)
	}

	detail, _, createErr := s.CreateTask(ctx, request)
	if createErr != nil {
		s.resetLegacyResume(ctx, occurrenceID, truncateAutomationDiagnostic(createErr.Error()))
		return protocol.AutomationOccurrence{}, createErr
	}
	linked := false
	cleanupDiagnostic := "legacy_resume_link_failed"
	defer func() {
		if !linked {
			s.resetLegacyResume(ctx, occurrenceID, cleanupDiagnostic)
		}
	}()
	if !equalLegacyTask(detail, request, repositoryID) {
		cleanupDiagnostic = "legacy_task_conflict"
		return protocol.AutomationOccurrence{}, conflict(
			"legacy_task_conflict",
			"the legacy request key belongs to a Task with a different repository, title, description, or timeout; Skip the observation or repair the conflicting Task",
		)
	}

	beginLink := s.beginLegacyResumeLink
	if beginLink == nil {
		beginLink = func(ctx context.Context) (*sql.Tx, error) { return s.db.BeginTx(ctx, nil) }
	}
	link, err := beginLink(ctx)
	if err != nil {
		cleanupDiagnostic = truncateAutomationDiagnostic(err.Error())
		return protocol.AutomationOccurrence{}, unavailable(err)
	}
	defer link.Rollback()
	var automationID string
	if err := link.QueryRowContext(ctx, `SELECT automation_id FROM automation_occurrences WHERE id = ?`, occurrenceID).Scan(&automationID); err != nil {
		return protocol.AutomationOccurrence{}, unavailable(err)
	}
	now = s.now().UnixMilli()
	result, err = link.ExecContext(ctx, `
		UPDATE automation_occurrences
		SET state = 'dispatched', context = ?, timeout_seconds = ?, resolved_prompt = ?,
		    task_id = ?, task_id_snapshot = ?, legacy_task_request_json = NULL,
		    diagnostic = 'legacy_task_resumed', retry_at = NULL, updated_at = ?
		WHERE id = ? AND state = 'dispatching' AND legacy_task_request_json = ?
	`, request.Description, request.TimeoutSeconds, request.Description,
		detail.Task.ID, detail.Task.ID, now, occurrenceID, requestJSON)
	if err != nil {
		return protocol.AutomationOccurrence{}, unavailable(err)
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return protocol.AutomationOccurrence{}, conflict("occurrence_changed", "the imported pending observation changed before the Task could be linked")
	}
	if _, err := link.ExecContext(ctx, `
		UPDATE automations SET dispatched_count = dispatched_count + 1 WHERE id = ?
	`, automationID); err != nil {
		return protocol.AutomationOccurrence{}, unavailable(err)
	}
	if err := link.Commit(); err != nil {
		cleanupDiagnostic = truncateAutomationDiagnostic(err.Error())
		return protocol.AutomationOccurrence{}, unavailable(err)
	}
	linked = true
	return s.legacyOccurrence(ctx, occurrenceID)
}

func (s *Store) resetLegacyResume(ctx context.Context, occurrenceID, diagnostic string) {
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), legacyResumeCleanupTimeout)
	defer cancel()
	_, _ = s.db.ExecContext(cleanupContext, `
		UPDATE automation_occurrences
		SET state = 'pending', diagnostic = ?, updated_at = ?
		WHERE id = ? AND state = 'dispatching' AND legacy_task_request_json IS NOT NULL
	`, diagnostic, s.now().UnixMilli(), occurrenceID)
}

func (s *Store) SkipLegacyPollerOccurrence(
	ctx context.Context,
	occurrenceID string,
) (protocol.AutomationOccurrence, error) {
	occurrenceID = strings.TrimSpace(occurrenceID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.AutomationOccurrence{}, unavailable(err)
	}
	defer tx.Rollback()
	var state string
	err = tx.QueryRowContext(ctx, `
		SELECT occurrence.state
		FROM automation_occurrences occurrence
		JOIN legacy_poller_observations legacy ON legacy.occurrence_id = occurrence.id
		WHERE occurrence.id = ?
	`, occurrenceID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.AutomationOccurrence{}, ErrNotFound
	}
	if err != nil {
		return protocol.AutomationOccurrence{}, unavailable(err)
	}
	if state == "dispatching" {
		return protocol.AutomationOccurrence{}, conflict("occurrence_dispatching", "Resume is currently creating or recovering the legacy Task")
	}
	if state != "pending" && state != "failed" {
		return protocol.AutomationOccurrence{}, conflict("occurrence_not_pending", "only an unresolved imported pending observation can be skipped")
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE automation_occurrences
		SET state = 'skipped', context = NULL, timeout_seconds = NULL,
		    resolved_prompt = NULL, legacy_task_request_json = NULL,
		    diagnostic = 'legacy_pending_skipped', retry_at = NULL, updated_at = ?
		WHERE id = ? AND state IN ('pending', 'failed') AND legacy_task_request_json IS NOT NULL
	`, s.now().UnixMilli(), occurrenceID)
	if err != nil {
		return protocol.AutomationOccurrence{}, unavailable(err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return protocol.AutomationOccurrence{}, conflict("occurrence_not_pending", "the imported pending observation changed before Skip")
	}
	if err := tx.Commit(); err != nil {
		return protocol.AutomationOccurrence{}, unavailable(err)
	}
	return s.legacyOccurrence(ctx, occurrenceID)
}

func (s *Store) legacyOccurrence(ctx context.Context, occurrenceID string) (protocol.AutomationOccurrence, error) {
	var automationID string
	if err := s.db.QueryRowContext(ctx, `
		SELECT occurrence.automation_id
		FROM automation_occurrences occurrence
		JOIN legacy_poller_observations legacy ON legacy.occurrence_id = occurrence.id
		WHERE occurrence.id = ?
	`, occurrenceID).Scan(&automationID); errors.Is(err, sql.ErrNoRows) {
		return protocol.AutomationOccurrence{}, ErrNotFound
	} else if err != nil {
		return protocol.AutomationOccurrence{}, unavailable(err)
	}
	page, err := s.automationOccurrencesPage(ctx, automationID, 1, nil, occurrenceID)
	if err != nil {
		return protocol.AutomationOccurrence{}, err
	}
	if len(page.Occurrences) == 1 {
		return page.Occurrences[0], nil
	}
	return protocol.AutomationOccurrence{}, ErrNotFound
}
