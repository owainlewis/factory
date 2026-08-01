package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

type normalizedAutomation struct {
	RequestKey     string                      `json:"request_key,omitempty"`
	Name           string                      `json:"name"`
	WorkflowID     string                      `json:"workflow_id"`
	RepositoryID   string                      `json:"repository_id,omitempty"`
	Context        string                      `json:"context"`
	TimeoutSeconds int                         `json:"timeout_seconds"`
	Trigger        protocol.GitHubIssueTrigger `json:"trigger"`
}

func normalizeAutomation(
	requestKey, name, workflowID, repositoryID, contextValue string,
	timeoutSeconds int,
	trigger protocol.GitHubIssueTrigger,
	requireRequestKey bool,
) (normalizedAutomation, string, error) {
	value := normalizedAutomation{
		RequestKey: strings.TrimSpace(requestKey),
		Name:       strings.TrimSpace(name), WorkflowID: strings.TrimSpace(workflowID),
		RepositoryID: strings.TrimSpace(repositoryID), Context: contextValue,
		TimeoutSeconds: timeoutSeconds, Trigger: trigger,
	}
	if requireRequestKey && (value.RequestKey == "" || len(value.RequestKey) > 200) {
		return value, "", invalid("invalid_request_key", "request_key is required and limited to 200 bytes")
	}
	if value.Name == "" || utf8.RuneCountInString(value.Name) > 100 {
		return value, "", invalid("invalid_automation_name", "name is required and limited to 100 Unicode characters")
	}
	if value.WorkflowID == "" {
		return value, "", invalid("invalid_workflow", "workflow_id is required")
	}
	if repositoryID != "" && value.RepositoryID == "" {
		return value, "", invalid("invalid_repository", "repository_id is required")
	}
	if len([]byte(value.Context)) > protocol.MaxAutomationContextBytes {
		return value, "", invalid("invalid_automation_context", "context is limited to 8 KiB")
	}
	if value.TimeoutSeconds < 1 || value.TimeoutSeconds > int(protocol.MaxTimeout/time.Second) {
		return value, "", invalid("invalid_timeout", "timeout_seconds must be between 1 and 28800")
	}
	value.Trigger.Type = strings.TrimSpace(value.Trigger.Type)
	value.Trigger.State = strings.ToLower(strings.TrimSpace(value.Trigger.State))
	if value.Trigger.Type != protocol.AutomationTriggerGitHubIssue {
		return value, "", invalid("invalid_trigger_type", "trigger.type must be github_issue")
	}
	if value.Trigger.State != "open" && value.Trigger.State != "closed" {
		return value, "", invalid("invalid_issue_state", "trigger.state must be open or closed")
	}
	if value.Trigger.PollIntervalSeconds < 10 || value.Trigger.PollIntervalSeconds > 86400 {
		return value, "", invalid("invalid_poll_interval", "poll_interval_seconds must be between 10 and 86400")
	}
	if len(value.Trigger.RequiredLabels) > 20 {
		return value, "", invalid("invalid_required_labels", "required_labels may contain at most 20 labels")
	}
	seen := make(map[string]struct{}, len(value.Trigger.RequiredLabels))
	labels := make([]string, 0, len(value.Trigger.RequiredLabels))
	for _, label := range value.Trigger.RequiredLabels {
		label = strings.TrimSpace(label)
		key := strings.ToLower(label)
		if label == "" || len([]byte(label)) > 200 {
			return value, "", invalid("invalid_required_labels", "required labels must be nonblank and at most 200 bytes")
		}
		if _, exists := seen[key]; exists {
			return value, "", invalid("invalid_required_labels", "required labels must be unique ignoring case")
		}
		seen[key] = struct{}{}
		labels = append(labels, label)
	}
	sort.Slice(labels, func(i, j int) bool {
		left, right := strings.ToLower(labels[i]), strings.ToLower(labels[j])
		if left == right {
			return labels[i] < labels[j]
		}
		return left < right
	})
	value.Trigger.RequiredLabels = labels
	return value, normalizeWorkflowName(value.Name), nil
}

func automationDigest(value normalizedAutomation) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	return digest[:], nil
}

func (s *Store) CreateAutomation(
	ctx context.Context,
	input protocol.CreateAutomationRequest,
) (protocol.AutomationDetail, bool, error) {
	value, nameKey, err := normalizeAutomation(
		input.RequestKey, input.Name, input.WorkflowID, input.RepositoryID,
		input.Context, input.TimeoutSeconds, input.Trigger, true,
	)
	if err != nil {
		return protocol.AutomationDetail{}, false, err
	}
	digest, err := automationDigest(value)
	if err != nil {
		return protocol.AutomationDetail{}, false, unavailable(err)
	}
	labels, err := json.Marshal(value.Trigger.RequiredLabels)
	if err != nil {
		return protocol.AutomationDetail{}, false, unavailable(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.AutomationDetail{}, false, unavailable(err)
	}
	defer tx.Rollback()
	var existingID string
	var existingDigest []byte
	err = tx.QueryRowContext(ctx, `SELECT id, request_digest FROM automations WHERE request_key = ?`, value.RequestKey).
		Scan(&existingID, &existingDigest)
	if err == nil {
		if !bytes.Equal(existingDigest, digest) {
			return protocol.AutomationDetail{}, false, conflict("request_key_conflict", "request_key was already used for a different Automation")
		}
		if err := tx.Commit(); err != nil {
			return protocol.AutomationDetail{}, false, unavailable(err)
		}
		detail, err := s.Automation(ctx, existingID)
		return detail, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.AutomationDetail{}, false, unavailable(err)
	}
	if err := validateAutomationDependencies(ctx, tx, value.WorkflowID, value.RepositoryID, false); err != nil {
		return protocol.AutomationDetail{}, false, err
	}
	var conflictingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM automations WHERE name_key = ?`, nameKey).Scan(&conflictingID)
	if err == nil {
		return protocol.AutomationDetail{}, false, conflict("automation_name_conflict", "an Automation with this normalized name already exists")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.AutomationDetail{}, false, unavailable(err)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM automations`).Scan(&count); err != nil {
		return protocol.AutomationDetail{}, false, unavailable(err)
	}
	if count >= protocol.MaxAutomations {
		return protocol.AutomationDetail{}, false, conflict("automation_limit_reached", "the Automation limit has been reached")
	}
	automationID, err := s.newAutomationID(ctx, tx)
	if err != nil {
		return protocol.AutomationDetail{}, false, err
	}
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO automations(
			id, request_key, request_digest, name, name_key, workflow_id,
			repository_id, context, timeout_seconds, trigger_type, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'github_issue', ?, ?)
	`, automationID, value.RequestKey, digest, value.Name, nameKey, value.WorkflowID,
		value.RepositoryID, value.Context, value.TimeoutSeconds, now, now); err != nil {
		return protocol.AutomationDetail{}, false, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO automation_github_issue_triggers(
			automation_id, issue_state, required_labels_json, poll_interval_seconds
		) VALUES (?, ?, ?, ?)
	`, automationID, value.Trigger.State, labels, value.Trigger.PollIntervalSeconds); err != nil {
		return protocol.AutomationDetail{}, false, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.AutomationDetail{}, false, unavailable(err)
	}
	detail, err := s.Automation(ctx, automationID)
	return detail, true, err
}

func (s *Store) newAutomationID(ctx context.Context, tx *sql.Tx) (string, error) {
	for attempts := 0; attempts < 10; attempts++ {
		id, err := newID()
		if err != nil {
			return "", unavailable(err)
		}
		var occupied int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tasks WHERE request_key LIKE ?`,
			"automation:"+id+":%",
		).Scan(&occupied); err != nil {
			return "", unavailable(err)
		}
		if occupied == 0 {
			return id, nil
		}
	}
	return "", unavailable(errors.New("could not allocate an unreserved Automation identity"))
}

func validateAutomationDependencies(
	ctx context.Context,
	tx *sql.Tx,
	workflowID, repositoryID string,
	requireEnabled bool,
) error {
	var workflowEnabled int
	if err := tx.QueryRowContext(ctx, `SELECT enabled FROM workflows WHERE id = ?`, workflowID).Scan(&workflowEnabled); errors.Is(err, sql.ErrNoRows) {
		return invalid("workflow_not_found", "workflow was not found")
	} else if err != nil {
		return unavailable(err)
	}
	var repositoryEnabled, centrallyManaged int
	if err := tx.QueryRowContext(ctx,
		`SELECT enabled, centrally_managed FROM repositories WHERE id = ?`, repositoryID,
	).Scan(&repositoryEnabled, &centrallyManaged); errors.Is(err, sql.ErrNoRows) {
		return invalid("repository_not_found", "managed repository was not found")
	} else if err != nil {
		return unavailable(err)
	}
	if centrallyManaged == 0 {
		return conflict("repository_not_managed", "repository is not managed by the control plane")
	}
	if requireEnabled && workflowEnabled == 0 {
		return conflict("workflow_disabled", "the selected Workflow is disabled")
	}
	if requireEnabled && repositoryEnabled == 0 {
		return conflict("repository_disabled", "the selected repository is disabled")
	}
	return nil
}

const automationSelect = `
	SELECT automation.id, automation.name, automation.workflow_id,
	       workflow_revision.name, workflow_revision.revision_number,
	       automation.repository_id, repository.remote_identity,
	       automation.context, automation.timeout_seconds, automation.enabled,
	       automation.version, trigger.issue_state, trigger.required_labels_json,
	       trigger.poll_interval_seconds, automation.health_status,
	       automation.health_code, automation.health_message,
	       automation.last_checked_at, automation.next_check_at,
	       automation.matched_count, automation.skipped_count,
	       automation.dispatched_count, automation.created_at, automation.updated_at
	FROM automations automation
	JOIN workflows workflow ON workflow.id = automation.workflow_id
	JOIN workflow_revisions workflow_revision ON workflow_revision.id = workflow.current_revision_id
	JOIN repositories repository ON repository.id = automation.repository_id
	JOIN automation_github_issue_triggers trigger ON trigger.automation_id = automation.id
`

func scanAutomation(row scanner) (protocol.Automation, error) {
	var automation protocol.Automation
	var enabled int
	var labels []byte
	var lastChecked, nextCheck sql.NullInt64
	var createdAt, updatedAt int64
	err := row.Scan(
		&automation.ID, &automation.Name, &automation.WorkflowID,
		&automation.WorkflowName, &automation.WorkflowRevision,
		&automation.RepositoryID, &automation.RepositoryIdentity,
		&automation.Context, &automation.TimeoutSeconds, &enabled,
		&automation.Version, &automation.Trigger.State, &labels,
		&automation.Trigger.PollIntervalSeconds, &automation.Health.Status,
		&automation.Health.Code, &automation.Health.Message,
		&lastChecked, &nextCheck, &automation.MatchedCount,
		&automation.SkippedCount, &automation.DispatchedCount,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return automation, err
	}
	automation.Enabled = enabled != 0
	automation.Trigger.Type = protocol.AutomationTriggerGitHubIssue
	if err := json.Unmarshal(labels, &automation.Trigger.RequiredLabels); err != nil {
		return automation, err
	}
	if lastChecked.Valid {
		value := fromMillis(lastChecked.Int64)
		automation.LastCheckedAt = &value
	}
	if nextCheck.Valid {
		value := fromMillis(nextCheck.Int64)
		automation.NextCheckAt = &value
	}
	automation.CreatedAt = fromMillis(createdAt)
	automation.UpdatedAt = fromMillis(updatedAt)
	return automation, nil
}

func (s *Store) Automations(ctx context.Context, limit int) (protocol.AutomationPage, error) {
	return s.AutomationsPage(ctx, limit, nil)
}

func (s *Store) AutomationsPage(
	ctx context.Context,
	limit int,
	cursor *protocol.AutomationCursor,
) (protocol.AutomationPage, error) {
	if limit < 1 || limit > protocol.MaxAutomationPageSize {
		return protocol.AutomationPage{}, invalid("invalid_limit", "limit must be between 1 and 200")
	}
	query := automationSelect
	args := make([]any, 0, 4)
	if cursor != nil {
		query += ` WHERE (automation.updated_at < ? OR (automation.updated_at = ? AND automation.id < ?))`
		args = append(args, cursor.UpdatedAtMillis, cursor.UpdatedAtMillis, cursor.ID)
	}
	query += ` ORDER BY automation.updated_at DESC, automation.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return protocol.AutomationPage{}, unavailable(err)
	}
	defer rows.Close()
	page := protocol.AutomationPage{Automations: make([]protocol.Automation, 0, limit+1)}
	for rows.Next() {
		automation, err := scanAutomation(rows)
		if err != nil {
			return protocol.AutomationPage{}, unavailable(err)
		}
		if err := s.loadLatestAutomationTask(ctx, &automation); err != nil {
			return protocol.AutomationPage{}, err
		}
		page.Automations = append(page.Automations, automation)
	}
	if err := rows.Err(); err != nil {
		return protocol.AutomationPage{}, unavailable(err)
	}
	if len(page.Automations) > limit {
		page.Automations = page.Automations[:limit]
		last := page.Automations[len(page.Automations)-1]
		page.NextCursor = &protocol.AutomationCursor{UpdatedAtMillis: last.UpdatedAt.UnixMilli(), ID: last.ID}
	}
	return page, nil
}

func (s *Store) Automation(ctx context.Context, automationID string) (protocol.AutomationDetail, error) {
	automation, err := scanAutomation(s.db.QueryRowContext(ctx,
		automationSelect+` WHERE automation.id = ?`, strings.TrimSpace(automationID)))
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.AutomationDetail{}, ErrNotFound
	}
	if err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	if err := s.loadLatestAutomationTask(ctx, &automation); err != nil {
		return protocol.AutomationDetail{}, err
	}
	occurrences, err := s.AutomationOccurrences(ctx, automation.ID, protocol.MaxAutomationPageSize)
	if err != nil {
		return protocol.AutomationDetail{}, err
	}
	return protocol.AutomationDetail{Automation: automation, Occurrences: occurrences}, nil
}

func (s *Store) loadLatestAutomationTask(ctx context.Context, automation *protocol.Automation) error {
	var task protocol.AutomationTaskSummary
	err := s.db.QueryRowContext(ctx, `
		SELECT task.id, task.title, execution.state
		FROM automation_occurrences occurrence
		JOIN tasks task ON task.id = occurrence.task_id
		JOIN executions execution ON execution.task_id = task.id
		WHERE occurrence.automation_id = ?
		ORDER BY occurrence.created_at DESC, occurrence.id DESC LIMIT 1
	`, automation.ID).Scan(&task.ID, &task.Title, &task.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return unavailable(err)
	}
	automation.LatestTask = &task
	return nil
}

func (s *Store) UpdateAutomation(
	ctx context.Context,
	automationID string,
	input protocol.UpdateAutomationRequest,
) (protocol.AutomationDetail, error) {
	value, nameKey, err := normalizeAutomation(
		"", input.Name, input.WorkflowID, "", input.Context,
		input.TimeoutSeconds, input.Trigger, false,
	)
	if err != nil {
		return protocol.AutomationDetail{}, err
	}
	if input.ExpectedVersion < 1 {
		return protocol.AutomationDetail{}, invalid("invalid_expected_version", "expected_version is required")
	}
	labels, err := json.Marshal(value.Trigger.RequiredLabels)
	if err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	defer tx.Rollback()
	var currentVersion, enabled int
	err = tx.QueryRowContext(ctx, `SELECT version, enabled FROM automations WHERE id = ?`, strings.TrimSpace(automationID)).
		Scan(&currentVersion, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.AutomationDetail{}, ErrNotFound
	}
	if err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	if enabled != 0 {
		return protocol.AutomationDetail{}, conflict("automation_enabled", "disable the Automation before editing it")
	}
	if currentVersion != input.ExpectedVersion {
		return protocol.AutomationDetail{}, conflict("automation_version_conflict", "the Automation has a newer configuration version")
	}
	var repositoryID string
	if err := tx.QueryRowContext(ctx, `SELECT repository_id FROM automations WHERE id = ?`, automationID).Scan(&repositoryID); err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	if err := validateAutomationDependencies(ctx, tx, value.WorkflowID, repositoryID, false); err != nil {
		return protocol.AutomationDetail{}, err
	}
	var conflictID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM automations WHERE name_key = ? AND id != ?`, nameKey, automationID).Scan(&conflictID)
	if err == nil {
		return protocol.AutomationDetail{}, conflict("automation_name_conflict", "an Automation with this normalized name already exists")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	now := s.now().UnixMilli()
	result, err := tx.ExecContext(ctx, `
		UPDATE automations
		SET name = ?, name_key = ?, workflow_id = ?, context = ?, timeout_seconds = ?,
		    version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND enabled = 0
	`, value.Name, nameKey, value.WorkflowID, value.Context, value.TimeoutSeconds,
		now, automationID, input.ExpectedVersion)
	if err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	if changed != 1 {
		return protocol.AutomationDetail{}, conflict("automation_version_conflict", "the Automation has a newer configuration version")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE automation_github_issue_triggers
		SET issue_state = ?, required_labels_json = ?, poll_interval_seconds = ?
		WHERE automation_id = ?
	`, value.Trigger.State, labels, value.Trigger.PollIntervalSeconds, automationID); err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	return s.Automation(ctx, automationID)
}

func (s *Store) SetAutomationEnabled(
	ctx context.Context,
	automationID string,
	enabled bool,
	confirmLegacyPollerStopped bool,
) (protocol.AutomationDetail, error) {
	detail, _, err := s.setAutomationEnabled(ctx, automationID, enabled, confirmLegacyPollerStopped)
	return detail, err
}

func (s *Store) setAutomationEnabled(
	ctx context.Context,
	automationID string,
	enabled bool,
	confirmLegacyPollerStopped bool,
) (protocol.AutomationDetail, string, error) {
	if enabled && !confirmLegacyPollerStopped {
		return protocol.AutomationDetail{}, "", invalid(
			"legacy_poller_confirmation_required",
			"confirm that factory-poller is stopped for any equivalent queue before enabling this Automation",
		)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.AutomationDetail{}, "", unavailable(err)
	}
	defer tx.Rollback()
	var workflowID, repositoryID string
	var currentEnabled int
	var evaluationToken sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT workflow_id, repository_id, enabled, evaluation_token FROM automations WHERE id = ?`,
		strings.TrimSpace(automationID),
	).Scan(&workflowID, &repositoryID, &currentEnabled, &evaluationToken)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.AutomationDetail{}, "", ErrNotFound
	}
	if err != nil {
		return protocol.AutomationDetail{}, "", unavailable(err)
	}
	if enabled {
		if err := validateAutomationDependencies(ctx, tx, workflowID, repositoryID, true); err != nil {
			return protocol.AutomationDetail{}, "", err
		}
	}
	now := s.now().UnixMilli()
	status, code, message := "disabled", "", "Automation is disabled."
	var nextCheck any
	if enabled {
		status, message, nextCheck = "pending", "Waiting for the next GitHub check.", now
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE automations
		SET enabled = ?, evaluation_token = NULL, evaluation_started_at = NULL,
		    next_check_at = ?, health_status = ?, health_code = ?, health_message = ?,
		    updated_at = CASE WHEN enabled != ? THEN ? ELSE updated_at END
		WHERE id = ?
	`, enabled, nextCheck, status, code, message, enabled, now, automationID); err != nil {
		return protocol.AutomationDetail{}, "", unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.AutomationDetail{}, "", unavailable(err)
	}
	detail, err := s.Automation(ctx, automationID)
	if err != nil {
		return protocol.AutomationDetail{}, "", err
	}
	invalidatedToken := ""
	if !enabled && evaluationToken.Valid {
		invalidatedToken = evaluationToken.String
	}
	return detail, invalidatedToken, nil
}

func (s *Store) RequestAutomationCheck(ctx context.Context, automationID string) (protocol.AutomationDetail, error) {
	now := s.now().UnixMilli()
	result, err := s.db.ExecContext(ctx, `
		UPDATE automations SET next_check_at = ?, updated_at = ?
		WHERE id = ? AND enabled = 1
	`, now, now, strings.TrimSpace(automationID))
	if err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	if changed == 0 {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automations WHERE id = ?`, automationID).Scan(&exists); err != nil {
			return protocol.AutomationDetail{}, unavailable(err)
		}
		if exists == 0 {
			return protocol.AutomationDetail{}, ErrNotFound
		}
		return protocol.AutomationDetail{}, conflict("automation_disabled", "enable the Automation before running a check")
	}
	return s.Automation(ctx, automationID)
}

func (s *Store) AutomationOccurrences(
	ctx context.Context,
	automationID string,
	limit int,
) ([]protocol.AutomationOccurrence, error) {
	page, err := s.AutomationOccurrencesPage(ctx, automationID, limit, nil)
	return page.Occurrences, err
}

func (s *Store) AutomationOccurrencesPage(
	ctx context.Context,
	automationID string,
	limit int,
	cursor *protocol.AutomationOccurrenceCursor,
) (protocol.AutomationOccurrencePage, error) {
	if limit < 1 || limit > protocol.MaxAutomationPageSize {
		return protocol.AutomationOccurrencePage{}, invalid("invalid_limit", "limit must be between 1 and 200")
	}
	query := `
		SELECT occurrence.id, occurrence.automation_id, occurrence.automation_version,
		       occurrence.state, issue.issue_number, issue.issue_url, issue.issue_title,
		       issue.observed_state, issue.observed_labels_json,
		       occurrence.task_request_key, occurrence.task_id_snapshot,
		       occurrence.diagnostic, occurrence.created_at, occurrence.updated_at,
		       task.id, task.title, execution.state
		FROM automation_occurrences occurrence
		JOIN automation_github_issue_occurrences issue ON issue.occurrence_id = occurrence.id
		LEFT JOIN tasks task ON task.id = occurrence.task_id
		LEFT JOIN executions execution ON execution.task_id = task.id
		WHERE occurrence.automation_id = ?`
	args := []any{strings.TrimSpace(automationID)}
	if cursor != nil {
		query += ` AND (occurrence.created_at < ? OR (occurrence.created_at = ? AND occurrence.id < ?))`
		args = append(args, cursor.CreatedAtMillis, cursor.CreatedAtMillis, cursor.ID)
	}
	query += ` ORDER BY occurrence.created_at DESC, occurrence.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return protocol.AutomationOccurrencePage{}, unavailable(err)
	}
	defer rows.Close()
	occurrences := make([]protocol.AutomationOccurrence, 0, limit+1)
	for rows.Next() {
		var occurrence protocol.AutomationOccurrence
		var labels []byte
		var taskID, taskTitle, taskState sql.NullString
		var createdAt, updatedAt int64
		if err := rows.Scan(
			&occurrence.ID, &occurrence.AutomationID, &occurrence.AutomationVersion,
			&occurrence.State, &occurrence.IssueNumber, &occurrence.IssueURL,
			&occurrence.IssueTitle, &occurrence.ObservedState, &labels,
			&occurrence.TaskRequestKey, &occurrence.TaskIDSnapshot,
			&occurrence.Diagnostic, &createdAt, &updatedAt,
			&taskID, &taskTitle, &taskState,
		); err != nil {
			return protocol.AutomationOccurrencePage{}, unavailable(err)
		}
		if err := json.Unmarshal(labels, &occurrence.ObservedLabels); err != nil {
			return protocol.AutomationOccurrencePage{}, unavailable(err)
		}
		if taskID.Valid {
			occurrence.Task = &protocol.AutomationTaskSummary{ID: taskID.String, Title: taskTitle.String, State: taskState.String}
		}
		occurrence.CreatedAt = fromMillis(createdAt)
		occurrence.UpdatedAt = fromMillis(updatedAt)
		occurrences = append(occurrences, occurrence)
	}
	if err := rows.Err(); err != nil {
		return protocol.AutomationOccurrencePage{}, unavailable(err)
	}
	page := protocol.AutomationOccurrencePage{Occurrences: occurrences}
	if len(occurrences) > limit {
		page.Occurrences = occurrences[:limit]
		last := page.Occurrences[len(page.Occurrences)-1]
		page.NextCursor = &protocol.AutomationOccurrenceCursor{CreatedAtMillis: last.CreatedAt.UnixMilli(), ID: last.ID}
	}
	return page, nil
}
