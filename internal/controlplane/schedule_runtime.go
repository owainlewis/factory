package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func countScheduledInstants(schedule cronSchedule, after, through time.Time) (int, error) {
	count := 0
	cursor := after
	for {
		next, err := schedule.Next(cursor)
		if err != nil {
			return 0, err
		}
		if next.After(through) {
			return count, nil
		}
		count++
		cursor = next
	}
}

type scheduleSnapshot struct {
	automationID         string
	automationTitle      string
	automationVersion    int
	workflowID           string
	workflowRevisionID   string
	workflowTitle        string
	workflowRevision     int
	workflowInstructions string
	repositoryID         string
	repositoryIdentity   string
	context              string
	timeoutSeconds       int
	cron                 string
	timezone             string
	nextDueAt            sql.NullInt64
	automationEnabled    bool
	workflowEnabled      bool
	repositoryEnabled    bool
	definitionID         string
	repositoryIDs        []string
	parameters           map[string]string
	concurrencyLimit     int
	definitionArchived   bool
}

func loadScheduleSnapshot(ctx context.Context, tx *sql.Tx, automationID string) (scheduleSnapshot, error) {
	var snapshot scheduleSnapshot
	var automationEnabled, workflowEnabled, repositoryEnabled, definitionArchived int
	var workflowID, workflowRevisionID, workflowTitle, workflowInstructions sql.NullString
	var workflowRevision sql.NullInt64
	var definitionID sql.NullString
	var parametersJSON []byte
	err := tx.QueryRowContext(ctx, `
		SELECT automation.id, automation.title, automation.version,
		       workflow.id, revision.id, revision.title, revision.revision_number,
		       revision.instructions, repository.id, repository.remote_identity,
		       automation.context, automation.timeout_seconds,
		       schedule.cron, schedule.timezone, schedule.next_due_at,
		       automation.enabled, COALESCE(workflow.enabled, 0), repository.enabled,
		       schedule.definition_id, schedule.parameters_json, schedule.concurrency_limit,
		       COALESCE(definition.archived, 0)
		FROM automations automation
		JOIN automation_schedule_triggers schedule ON schedule.automation_id = automation.id
		LEFT JOIN workflows workflow ON workflow.id = automation.workflow_id
		LEFT JOIN workflow_revisions revision ON revision.id = workflow.current_revision_id
		LEFT JOIN definitions definition ON definition.id = schedule.definition_id
		JOIN repositories repository ON repository.id = automation.repository_id
		WHERE automation.id = ? AND automation.trigger_type = 'schedule'
	`, strings.TrimSpace(automationID)).Scan(
		&snapshot.automationID, &snapshot.automationTitle, &snapshot.automationVersion,
		&workflowID, &workflowRevisionID, &workflowTitle,
		&workflowRevision, &workflowInstructions,
		&snapshot.repositoryID, &snapshot.repositoryIdentity,
		&snapshot.context, &snapshot.timeoutSeconds, &snapshot.cron, &snapshot.timezone,
		&snapshot.nextDueAt, &automationEnabled, &workflowEnabled, &repositoryEnabled,
		&definitionID, &parametersJSON, &snapshot.concurrencyLimit, &definitionArchived,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, ErrNotFound
	}
	if err != nil {
		return snapshot, unavailable(err)
	}
	snapshot.automationEnabled = automationEnabled != 0
	snapshot.workflowEnabled = workflowEnabled != 0
	snapshot.repositoryEnabled = repositoryEnabled != 0
	snapshot.workflowID = workflowID.String
	snapshot.workflowRevisionID = workflowRevisionID.String
	snapshot.workflowTitle = workflowTitle.String
	snapshot.workflowRevision = int(workflowRevision.Int64)
	snapshot.workflowInstructions = workflowInstructions.String
	snapshot.definitionID = definitionID.String
	snapshot.definitionArchived = definitionArchived != 0
	if snapshot.definitionID != "" {
		if err := json.Unmarshal(parametersJSON, &snapshot.parameters); err != nil {
			return snapshot, unavailable(err)
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT repository_id FROM automation_schedule_repositories
			WHERE automation_id = ? ORDER BY position
		`, snapshot.automationID)
		if err != nil {
			return snapshot, unavailable(err)
		}
		for rows.Next() {
			var repositoryID string
			if err := rows.Scan(&repositoryID); err != nil {
				rows.Close()
				return snapshot, unavailable(err)
			}
			snapshot.repositoryIDs = append(snapshot.repositoryIDs, repositoryID)
		}
		if err := rows.Close(); err != nil {
			return snapshot, unavailable(err)
		}
	}
	return snapshot, nil
}

func (s *Store) processDueSchedules(ctx context.Context, limit int) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT schedule.automation_id
		FROM automation_schedule_triggers schedule
		JOIN automations automation ON automation.id = schedule.automation_id
		WHERE automation.enabled = 1 AND schedule.next_due_at IS NOT NULL
		  AND schedule.next_due_at <= ?
		ORDER BY schedule.next_due_at, schedule.automation_id LIMIT ?
	`, s.now().UnixMilli(), limit)
	if err != nil {
		return unavailable(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return unavailable(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return unavailable(err)
	}
	var result error
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		if err := s.admitDueSchedule(ctx, id); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (s *Store) admitDueSchedule(ctx context.Context, automationID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err)
	}
	defer tx.Rollback()
	snapshot, err := loadScheduleSnapshot(ctx, tx, automationID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	now := s.now()
	if !snapshot.automationEnabled || !snapshot.nextDueAt.Valid || snapshot.nextDueAt.Int64 > now.UnixMilli() {
		return nil
	}
	schedule, _, _, err := parseCronSchedule(snapshot.cron, snapshot.timezone)
	if err != nil {
		_, updateErr := tx.ExecContext(ctx, `
			UPDATE automation_schedule_triggers SET next_due_at = NULL WHERE automation_id = ?
		`, snapshot.automationID)
		if updateErr == nil {
			_, updateErr = tx.ExecContext(ctx, `
				UPDATE automations SET health_status = 'error', health_code = 'stored_schedule_invalid',
				       health_message = ?, updated_at = ? WHERE id = ?
			`, truncateAutomationDiagnostic(err.Error()), now.UnixMilli(), snapshot.automationID)
		}
		if updateErr != nil {
			return unavailable(updateErr)
		}
		return tx.Commit()
	}
	nextDue, err := schedule.Next(now)
	if err != nil {
		return unavailable(err)
	}
	due := fromMillis(snapshot.nextDueAt.Int64)
	var existing int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM automation_schedule_occurrences
		WHERE automation_id = ? AND kind = 'scheduled' AND scheduled_at = ?
	`, snapshot.automationID, due.UnixMilli()).Scan(&existing); err != nil {
		return unavailable(err)
	}
	if existing == 0 {
		var occurrenceCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_occurrences`).Scan(&occurrenceCount); err != nil {
			return unavailable(err)
		}
		if occurrenceCount >= protocol.MaxAutomationOccurrences {
			if _, err := tx.ExecContext(ctx, `
				UPDATE automations SET health_status = 'error', health_code = 'occurrence_limit_reached',
				       health_message = 'The durable Occurrence limit has been reached. Archive history before retrying.'
				WHERE id = ?
			`, snapshot.automationID); err != nil {
				return unavailable(err)
			}
			return tx.Commit()
		}
	}
	skipped := 0
	healthStatus, healthCode := "healthy", ""
	healthMessage := "Scheduled occurrence admitted."
	if existing != 0 {
		skipped = 1
		healthMessage = "Scheduled occurrence was already durable; advanced to the next due instant."
	} else {
		state, diagnostic := "pending", ""
		if snapshot.definitionID != "" {
			if dependencyErr := validateDefinitionScheduleDependencies(
				ctx, tx, snapshot.definitionID, snapshot.repositoryIDs, snapshot.parameters, true,
			); dependencyErr != nil {
				state, diagnostic, skipped = "failed", "schedule_dependency_unavailable", 1
				var serviceErr *ServiceError
				if errors.As(dependencyErr, &serviceErr) {
					diagnostic = serviceErr.Code
				}
				healthStatus, healthCode = "blocked", diagnostic
				healthMessage = "Scheduled occurrence recorded without a Run because its Definition or repository set is unavailable."
			}
		} else if !snapshot.workflowEnabled {
			state, diagnostic, skipped = "failed", "workflow_disabled", 1
			healthStatus, healthCode, healthMessage = "blocked", "workflow_disabled", "Scheduled occurrence recorded without a task because the Workflow is disabled."
		} else if !snapshot.repositoryEnabled {
			state, diagnostic, skipped = "failed", "repository_disabled", 1
			healthStatus, healthCode, healthMessage = "blocked", "repository_disabled", "Scheduled occurrence recorded without a task because the repository is disabled."
		}
		if !now.Before(due.Truncate(time.Minute).Add(time.Minute)) {
			missed, err := countScheduledInstants(schedule, due, now)
			if err != nil {
				return unavailable(err)
			}
			catchUpDiagnostic := "catch_up"
			if missed != 0 {
				catchUpDiagnostic = fmt.Sprintf("catch_up; %d later scheduled instants skipped", missed)
				skipped += missed
			}
			if diagnostic == "" {
				diagnostic = catchUpDiagnostic
			} else {
				diagnostic += "; " + catchUpDiagnostic
			}
		}
		if err := s.insertScheduleOccurrence(ctx, tx, snapshot, "scheduled", due.Format(time.RFC3339), state, diagnostic, now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE automation_schedule_triggers SET next_due_at = ?
		WHERE automation_id = ? AND next_due_at = ?
	`, nextDue.UnixMilli(), snapshot.automationID, snapshot.nextDueAt.Int64); err != nil {
		return unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE automations
		SET last_checked_at = ?, health_status = ?, health_code = ?, health_message = ?,
		    matched_count = matched_count + 1, skipped_count = skipped_count + ?, updated_at = ?
		WHERE id = ? AND enabled = 1
	`, now.UnixMilli(), healthStatus, healthCode, healthMessage, skipped, now.UnixMilli(), snapshot.automationID); err != nil {
		return unavailable(err)
	}
	return tx.Commit()
}

func (s *Store) RunAutomationNow(
	ctx context.Context,
	automationID string,
	input protocol.RunAutomationRequest,
) (protocol.AutomationDetail, error) {
	if input.RequestKey == "" || input.RequestKey != strings.TrimSpace(input.RequestKey) || len([]byte(input.RequestKey)) > 128 {
		return protocol.AutomationDetail{}, invalid("invalid_run_request_key", "request_key must be trimmed, nonblank, and at most 128 bytes")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	defer tx.Rollback()
	var existingOccurrence string
	err = tx.QueryRowContext(ctx, `
		SELECT occurrence_id FROM automation_schedule_occurrences
		WHERE automation_id = ? AND kind = 'run_now' AND run_request_key = ?
	`, strings.TrimSpace(automationID), input.RequestKey).Scan(&existingOccurrence)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return protocol.AutomationDetail{}, unavailable(err)
		}
		return s.Automation(ctx, automationID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	snapshot, err := loadScheduleSnapshot(ctx, tx, automationID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			var triggerType string
			lookupErr := tx.QueryRowContext(ctx, `SELECT trigger_type FROM automations WHERE id = ?`, strings.TrimSpace(automationID)).Scan(&triggerType)
			if lookupErr == nil {
				return protocol.AutomationDetail{}, conflict("automation_not_schedule", "Run now applies only to schedule Automations")
			}
			if !errors.Is(lookupErr, sql.ErrNoRows) {
				return protocol.AutomationDetail{}, unavailable(lookupErr)
			}
		}
		return protocol.AutomationDetail{}, err
	}
	if !snapshot.automationEnabled {
		return protocol.AutomationDetail{}, conflict("automation_disabled", "enable the Automation before using Run now")
	}
	if snapshot.definitionID != "" {
		if err := validateDefinitionScheduleDependencies(
			ctx, tx, snapshot.definitionID, snapshot.repositoryIDs, snapshot.parameters, true,
		); err != nil {
			return protocol.AutomationDetail{}, err
		}
	} else {
		if !snapshot.workflowEnabled {
			return protocol.AutomationDetail{}, conflict("workflow_disabled", "enable the selected Workflow before using Run now")
		}
		if !snapshot.repositoryEnabled {
			return protocol.AutomationDetail{}, conflict("repository_disabled", "enable the selected repository before using Run now")
		}
	}
	var occurrenceCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_occurrences`).Scan(&occurrenceCount); err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	if occurrenceCount >= protocol.MaxAutomationOccurrences {
		return protocol.AutomationDetail{}, conflict("occurrence_limit_reached", "the durable Occurrence limit has been reached")
	}
	now := s.now()
	if err := s.insertScheduleOccurrence(ctx, tx, snapshot, "run_now", input.RequestKey, "pending", "", now); err != nil {
		return protocol.AutomationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE automations
		SET matched_count = matched_count + 1, last_checked_at = ?, health_status = 'healthy',
		    health_code = '', health_message = 'Run now occurrence admitted.', updated_at = ?
		WHERE id = ?
	`, now.UnixMilli(), now.UnixMilli(), snapshot.automationID); err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	return s.Automation(ctx, automationID)
}

func (s *Store) insertScheduleOccurrence(
	ctx context.Context,
	tx *sql.Tx,
	snapshot scheduleSnapshot,
	kind, identity, state, diagnostic string,
	now time.Time,
) error {
	title := snapshot.automationTitle + ": run now"
	requestKey := "automation:" + snapshot.automationID + ":schedule:run:" + identity
	if kind == "scheduled" {
		title = snapshot.automationTitle + ": scheduled " + identity
		requestKey = "automation:" + snapshot.automationID + ":schedule:scheduled:" + identity
	}
	var storedPrompt any
	if snapshot.definitionID == "" {
		prompt, err := protocol.ResolveScheduleAutomationPrompt(
			snapshot.workflowInstructions, snapshot.context, kind, identity, snapshot.cron, snapshot.timezone,
		)
		if err != nil {
			return unavailable(err)
		}
		storedPrompt = prompt
		if state == "pending" && (len([]byte(prompt)) > protocol.MaxResolvedPromptBytes ||
			!protocol.AgentPromptFits(title, snapshot.repositoryIdentity, prompt)) {
			if kind == "run_now" {
				return invalid("resolved_prompt_too_large", "the resolved schedule prompt exceeds the Task limit")
			}
			state, diagnostic, storedPrompt = "failed", "resolved_prompt_too_large", nil
		}
		if state != "pending" {
			storedPrompt = nil
		}
	}
	occurrenceID, err := newID()
	if err != nil {
		return unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO automation_occurrences(
			id, automation_id, automation_version, automation_title,
			workflow_revision_id, repository_id, repository_identity,
			context, timeout_seconds, state, resolved_prompt, task_request_key,
			diagnostic, retry_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, occurrenceID, snapshot.automationID, snapshot.automationVersion, snapshot.automationTitle,
		nullableString(snapshot.workflowRevisionID), snapshot.repositoryID, snapshot.repositoryIdentity,
		snapshot.context, snapshot.timeoutSeconds, state, storedPrompt, requestKey,
		diagnostic, now.UnixMilli(), now.UnixMilli(), now.UnixMilli()); err != nil {
		return unavailable(err)
	}
	var scheduledAt, runRequestKey any
	if kind == "scheduled" {
		value, err := time.Parse(time.RFC3339, identity)
		if err != nil {
			return unavailable(err)
		}
		scheduledAt = value.UnixMilli()
	} else {
		runRequestKey = identity
	}
	var definitionID, definitionSnapshotJSON, repositoryIDsJSON, parametersJSON, concurrencyLimit any
	if snapshot.definitionID != "" {
		definitionID = snapshot.definitionID
		definition, err := scanDefinition(tx.QueryRowContext(ctx, definitionSelect+` WHERE id = ?`, snapshot.definitionID))
		if err != nil {
			return unavailable(err)
		}
		encodedDefinition, err := json.Marshal(definition.Snapshot())
		if err != nil {
			return unavailable(err)
		}
		definitionSnapshotJSON = encodedDefinition
		encodedRepositories, err := json.Marshal(snapshot.repositoryIDs)
		if err != nil {
			return unavailable(err)
		}
		encodedParameters, err := json.Marshal(snapshot.parameters)
		if err != nil {
			return unavailable(err)
		}
		repositoryIDsJSON, parametersJSON, concurrencyLimit = encodedRepositories, encodedParameters, snapshot.concurrencyLimit
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO automation_schedule_occurrences(
			occurrence_id, automation_id, kind, scheduled_at, run_request_key, cron, timezone,
			definition_id, definition_snapshot, repository_ids_json, parameters_json, concurrency_limit
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, occurrenceID, snapshot.automationID, kind, scheduledAt, runRequestKey, snapshot.cron,
		snapshot.timezone, definitionID, definitionSnapshotJSON, repositoryIDsJSON, parametersJSON, concurrencyLimit); err != nil {
		return unavailable(err)
	}
	return nil
}
