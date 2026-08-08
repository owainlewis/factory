package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

type normalizedAutomation struct {
	RequestKey string `json:"request_key,omitempty"`
	// Keep the legacy digest key so an equivalent retry survives the API field rename.
	Title            string                     `json:"name"`
	WorkflowID       string                     `json:"workflow_id"`
	RepositoryID     string                     `json:"repository_id,omitempty"`
	Context          string                     `json:"context"`
	TimeoutSeconds   int                        `json:"timeout_seconds"`
	Trigger          protocol.AutomationTrigger `json:"trigger"`
	DefinitionID     string                     `json:"definition_id,omitempty"`
	RepositoryIDs    []string                   `json:"repository_ids,omitempty"`
	Parameters       map[string]string          `json:"parameters,omitempty"`
	ConcurrencyLimit int                        `json:"concurrency_limit,omitempty"`
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func normalizeAutomation(
	requestKey, title, workflowID, repositoryID, contextValue string,
	timeoutSeconds int,
	trigger protocol.AutomationTrigger,
	definitionID string,
	repositoryIDs []string,
	parameters map[string]string,
	concurrencyLimit int,
	requireRequestKey bool,
	allowLegacySchedule bool,
) (normalizedAutomation, string, error) {
	value := normalizedAutomation{
		RequestKey: strings.TrimSpace(requestKey),
		Title:      strings.TrimSpace(title), WorkflowID: strings.TrimSpace(workflowID),
		RepositoryID: strings.TrimSpace(repositoryID), Context: contextValue,
		TimeoutSeconds: timeoutSeconds, Trigger: trigger,
		DefinitionID:  strings.TrimSpace(definitionID),
		RepositoryIDs: append([]string(nil), repositoryIDs...),
		Parameters:    parameters, ConcurrencyLimit: concurrencyLimit,
	}
	if requireRequestKey && (value.RequestKey == "" || len(value.RequestKey) > 200) {
		return value, "", invalid("invalid_request_key", "request_key is required and limited to 200 bytes")
	}
	if value.Title == "" || utf8.RuneCountInString(value.Title) > 100 {
		return value, "", invalid("invalid_automation_title", "title is required and limited to 100 Unicode characters")
	}
	value.Trigger.Type = strings.TrimSpace(value.Trigger.Type)
	value.Trigger.State = strings.ToLower(strings.TrimSpace(value.Trigger.State))
	if value.Trigger.Type != protocol.AutomationTriggerGitHubIssue &&
		value.Trigger.Type != protocol.AutomationTriggerGitHubPullRequest &&
		value.Trigger.Type != protocol.AutomationTriggerSchedule {
		return value, "", invalid("invalid_trigger_type", "trigger.type must be github_issue, github_pull_request, or schedule")
	}
	if value.Trigger.Type == protocol.AutomationTriggerSchedule {
		if value.DefinitionID == "" {
			if !allowLegacySchedule || value.WorkflowID == "" {
				return value, "", invalid("definition_required", "definition_id is required for a scheduled Automation")
			}
			if len(value.RepositoryIDs) != 0 || len(value.Parameters) != 0 {
				return value, "", invalid("legacy_schedule_shape", "legacy scheduled Automations use their existing repository and runbook")
			}
			value.RepositoryIDs = nil
			value.Parameters = map[string]string{}
			if value.ConcurrencyLimit == 0 {
				value.ConcurrencyLimit = defaultRunConcurrency
			}
			if len([]byte(value.Context)) > protocol.MaxAutomationContextBytes {
				return value, "", invalid("invalid_automation_context", "context is limited to 8 KiB")
			}
			if value.TimeoutSeconds < 1 || value.TimeoutSeconds > int(protocol.MaxTimeout/time.Second) {
				return value, "", invalid("invalid_timeout", "timeout_seconds must be between 1 and 28800")
			}
		} else {
			if value.WorkflowID != "" || value.RepositoryID != "" {
				return value, "", invalid("legacy_schedule_shape", "scheduled Automations use definition_id and repository_ids")
			}
			seenRepositories := make(map[string]struct{}, len(value.RepositoryIDs))
			for index, repositoryID := range value.RepositoryIDs {
				repositoryID = strings.TrimSpace(repositoryID)
				if repositoryID == "" {
					return value, "", invalid("repository_required", "repository_ids cannot contain an empty value")
				}
				if _, exists := seenRepositories[repositoryID]; exists {
					return value, "", invalid("duplicate_repository", "each repository may be selected only once")
				}
				seenRepositories[repositoryID] = struct{}{}
				value.RepositoryIDs[index] = repositoryID
			}
			if len(value.RepositoryIDs) == 0 {
				return value, "", invalid("repository_required", "at least one repository_id is required")
			}
			if len(value.RepositoryIDs) > 200 {
				return value, "", invalid("too_many_repositories", "a scheduled Automation is limited to 200 repositories")
			}
			sort.Strings(value.RepositoryIDs)
			value.RepositoryID = value.RepositoryIDs[0]
			if value.ConcurrencyLimit == 0 {
				value.ConcurrencyLimit = defaultRunConcurrency
			}
			if value.ConcurrencyLimit < 1 || value.ConcurrencyLimit > 100 {
				return value, "", invalid("invalid_concurrency_limit", "concurrency_limit must be between 1 and 100")
			}
			normalizedParameters, inputErr := normalizeDefinitionInputs(value.Parameters)
			if inputErr != nil {
				return value, "", inputErr
			}
			value.Parameters = normalizedParameters
			value.WorkflowID = ""
			value.Context = ""
			value.TimeoutSeconds = 1
		}
		_, cron, timezone, parseErr := parseCronSchedule(value.Trigger.Cron, value.Trigger.Timezone)
		if parseErr != nil {
			if strings.Contains(parseErr.Error(), "timezone") {
				return value, "", invalid("invalid_timezone", parseErr.Error())
			}
			return value, "", invalid("invalid_cron", parseErr.Error())
		}
		value.Trigger = protocol.AutomationTrigger{Type: protocol.AutomationTriggerSchedule, Cron: cron, Timezone: timezone}
		return value, normalizeTitleKey(value.Title), nil
	}
	value.DefinitionID = ""
	value.RepositoryIDs = nil
	value.Parameters = nil
	value.ConcurrencyLimit = 0
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
	value.Trigger.Cron = ""
	value.Trigger.Timezone = ""
	if value.Trigger.Type == protocol.AutomationTriggerGitHubIssue && value.Trigger.State != "open" && value.Trigger.State != "closed" {
		return value, "", invalid("invalid_issue_state", "trigger.state must be open or closed")
	}
	if value.Trigger.Type == protocol.AutomationTriggerGitHubPullRequest && value.Trigger.State != "open" && value.Trigger.State != "closed" && value.Trigger.State != "merged" {
		return value, "", invalid("invalid_pull_request_state", "trigger.state must be open, closed, or merged")
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
	if value.Trigger.Type == protocol.AutomationTriggerGitHubIssue {
		value.Trigger.IncludeDrafts = false
		value.Trigger.BaseBranches = []string{}
		return value, normalizeTitleKey(value.Title), nil
	}
	if len(value.Trigger.BaseBranches) > 20 {
		return value, "", invalid("invalid_base_branches", "base_branches may contain at most 20 branches")
	}
	seenBranches := make(map[string]struct{}, len(value.Trigger.BaseBranches))
	branches := make([]string, 0, len(value.Trigger.BaseBranches))
	for _, branch := range value.Trigger.BaseBranches {
		branch = strings.TrimSpace(branch)
		if branch == "" || len([]byte(branch)) > 255 {
			return value, "", invalid("invalid_base_branches", "base branches must be nonblank and at most 255 bytes")
		}
		if _, exists := seenBranches[branch]; exists {
			return value, "", invalid("invalid_base_branches", "base branches must be unique")
		}
		seenBranches[branch] = struct{}{}
		branches = append(branches, branch)
	}
	sort.Strings(branches)
	value.Trigger.BaseBranches = branches
	return value, normalizeTitleKey(value.Title), nil
}

func automationDigest(value normalizedAutomation) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	return digest[:], nil
}

func legacyAutomationDigest(value normalizedAutomation) ([]byte, error) {
	body, err := json.Marshal(struct {
		RequestKey     string                     `json:"request_key,omitempty"`
		Title          string                     `json:"name"`
		WorkflowID     string                     `json:"workflow_id"`
		RepositoryID   string                     `json:"repository_id,omitempty"`
		Context        string                     `json:"context"`
		TimeoutSeconds int                        `json:"timeout_seconds"`
		Trigger        protocol.AutomationTrigger `json:"trigger"`
	}{
		RequestKey: value.RequestKey, Title: value.Title, WorkflowID: value.WorkflowID,
		RepositoryID: value.RepositoryID, Context: value.Context,
		TimeoutSeconds: value.TimeoutSeconds, Trigger: value.Trigger,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	return digest[:], nil
}

func (s *Store) replayLegacyScheduleAutomation(
	ctx context.Context,
	input protocol.CreateAutomationRequest,
) (protocol.AutomationDetail, bool, error) {
	requestKey := strings.TrimSpace(input.RequestKey)
	var existingID, existingType, existingDefinitionID string
	var existingDigest []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT automation.id, automation.request_digest, automation.trigger_type,
		       COALESCE(schedule.definition_id, '')
		FROM automations automation
		LEFT JOIN automation_schedule_triggers schedule ON schedule.automation_id = automation.id
		WHERE automation.request_key = ?
	`, requestKey).Scan(&existingID, &existingDigest, &existingType, &existingDefinitionID)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.AutomationDetail{}, false, nil
	}
	if err != nil {
		return protocol.AutomationDetail{}, false, unavailable(err)
	}
	if existingType != protocol.AutomationTriggerSchedule || existingDefinitionID != "" {
		return protocol.AutomationDetail{}, false, conflict("request_key_conflict", "request_key was already used for a different Automation")
	}
	value, _, err := normalizeAutomation(
		input.RequestKey, input.Title, input.WorkflowID, input.RepositoryID,
		input.Context, input.TimeoutSeconds, input.Trigger,
		input.DefinitionID, input.RepositoryIDs, input.Parameters, input.ConcurrencyLimit, true, true,
	)
	if err != nil {
		return protocol.AutomationDetail{}, false, err
	}
	digest, err := legacyAutomationDigest(value)
	if err != nil {
		return protocol.AutomationDetail{}, false, unavailable(err)
	}
	if !bytes.Equal(existingDigest, digest) {
		return protocol.AutomationDetail{}, false, conflict("request_key_conflict", "request_key was already used for a different Automation")
	}
	detail, err := s.Automation(ctx, existingID)
	return detail, true, err
}

func (s *Store) CreateAutomation(
	ctx context.Context,
	input protocol.CreateAutomationRequest,
) (protocol.AutomationDetail, bool, error) {
	value, titleKey, err := normalizeAutomation(
		input.RequestKey, input.Title, input.WorkflowID, input.RepositoryID,
		input.Context, input.TimeoutSeconds, input.Trigger,
		input.DefinitionID, input.RepositoryIDs, input.Parameters, input.ConcurrencyLimit, true, false,
	)
	if err != nil {
		if strings.TrimSpace(input.Trigger.Type) == protocol.AutomationTriggerSchedule &&
			strings.TrimSpace(input.DefinitionID) == "" && strings.TrimSpace(input.WorkflowID) != "" {
			if detail, replayed, replayErr := s.replayLegacyScheduleAutomation(ctx, input); replayed || replayErr != nil {
				return detail, false, replayErr
			}
		}
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
	baseBranches, err := json.Marshal(value.Trigger.BaseBranches)
	if err != nil {
		return protocol.AutomationDetail{}, false, unavailable(err)
	}
	parametersJSON, err := json.Marshal(value.Parameters)
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
	if value.Trigger.Type == protocol.AutomationTriggerSchedule {
		if err := validateDefinitionScheduleDependencies(
			ctx, tx, value.DefinitionID, value.RepositoryIDs, value.Parameters, false,
		); err != nil {
			return protocol.AutomationDetail{}, false, err
		}
	} else if err := validateAutomationDependencies(ctx, tx, value.WorkflowID, value.RepositoryID, false); err != nil {
		return protocol.AutomationDetail{}, false, err
	}
	var conflictingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM automations WHERE title_key = ?`, titleKey).Scan(&conflictingID)
	if err == nil {
		return protocol.AutomationDetail{}, false, conflict("automation_title_conflict", "an Automation with this normalized title already exists")
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
			id, request_key, request_digest, title, title_key, workflow_id,
			repository_id, context, timeout_seconds, trigger_type, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, automationID, value.RequestKey, digest, value.Title, titleKey, nullableString(value.WorkflowID),
		value.RepositoryID, value.Context, value.TimeoutSeconds, value.Trigger.Type, now, now); err != nil {
		return protocol.AutomationDetail{}, false, unavailable(err)
	}
	if value.Trigger.Type == protocol.AutomationTriggerGitHubIssue {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO automation_github_issue_triggers(
				automation_id, issue_state, required_labels_json, poll_interval_seconds
			) VALUES (?, ?, ?, ?)
		`, automationID, value.Trigger.State, labels, value.Trigger.PollIntervalSeconds); err != nil {
			return protocol.AutomationDetail{}, false, unavailable(err)
		}
	} else if value.Trigger.Type == protocol.AutomationTriggerGitHubPullRequest {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO automation_github_pull_request_triggers(
				automation_id, pull_request_state, include_drafts, required_labels_json,
				base_branches_json, poll_interval_seconds
			) VALUES (?, ?, ?, ?, ?, ?)
		`, automationID, value.Trigger.State, value.Trigger.IncludeDrafts, labels,
			baseBranches, value.Trigger.PollIntervalSeconds); err != nil {
			return protocol.AutomationDetail{}, false, unavailable(err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO automation_schedule_triggers(
				automation_id, cron, timezone, definition_id, parameters_json, concurrency_limit
			) VALUES (?, ?, ?, ?, ?, ?)
		`, automationID, value.Trigger.Cron, value.Trigger.Timezone, value.DefinitionID,
			parametersJSON, value.ConcurrencyLimit); err != nil {
			return protocol.AutomationDetail{}, false, unavailable(err)
		}
		for position, repositoryID := range value.RepositoryIDs {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO automation_schedule_repositories(automation_id, position, repository_id)
				VALUES (?, ?, ?)
			`, automationID, position, repositoryID); err != nil {
				return protocol.AutomationDetail{}, false, unavailable(err)
			}
		}
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

func validateDefinitionScheduleDependencies(
	ctx context.Context,
	tx *sql.Tx,
	definitionID string,
	repositoryIDs []string,
	parameters map[string]string,
	requireRunnable bool,
) error {
	definition, err := scanDefinition(tx.QueryRowContext(ctx, definitionSelect+` WHERE id = ?`, definitionID))
	if errors.Is(err, sql.ErrNoRows) {
		return invalid("definition_not_found", "Definition was not found")
	}
	if err != nil {
		return unavailable(err)
	}
	if definition.Archived {
		return conflict("definition_archived", "archived Definitions cannot be scheduled")
	}
	for key := range parameters {
		if _, exists := definition.Inputs[key]; !exists {
			return invalid("unknown_run_parameter", "parameters must be declared by the selected Definition")
		}
	}
	resolvedParameters := make(map[string]string, len(definition.Inputs))
	for key, defaultValue := range definition.Inputs {
		resolvedParameters[key] = defaultValue
	}
	for key, value := range parameters {
		resolvedParameters[key] = value
	}
	resolvedPrompt, err := protocol.ResolveDefinitionPrompt(definition.Prompt, resolvedParameters)
	if err != nil {
		return unavailable(err)
	}
	for _, repositoryID := range repositoryIDs {
		var remoteIdentity string
		var enabled, centrallyManaged, advertised int
		err := tx.QueryRowContext(ctx, `
			SELECT repository.remote_identity, repository.enabled, repository.centrally_managed,
			       EXISTS (
			           SELECT 1 FROM worker_repositories available
			           WHERE available.repository_id = repository.id
			             AND available.advertised = 1
			             AND available.dynamic = 0
			       )
			FROM repositories repository
			WHERE repository.id = ?
		`, repositoryID).Scan(&remoteIdentity, &enabled, &centrallyManaged, &advertised)
		if errors.Is(err, sql.ErrNoRows) {
			enabled = 0
		} else if err != nil {
			return unavailable(err)
		}
		if !runRepositoryAvailable(remoteIdentity, enabled, centrallyManaged, advertised) {
			code := "repository_not_available"
			message := "every scheduled repository must be configured on a Runner or enabled for managed acquisition"
			if requireRunnable {
				return conflict(code, message)
			}
			return invalid(code, message)
		}
		if centrallyManaged != 0 {
			if canonical, normalizeErr := normalizeManagedGitHubRemote(remoteIdentity); normalizeErr == nil {
				remoteIdentity = canonical
			}
		}
		if !protocol.AgentPromptFits(definition.Name, remoteIdentity, resolvedPrompt) {
			return invalid("agent_prompt_too_large", "the complete agent prompt exceeds 72 KiB")
		}
	}
	return nil
}

const automationSelect = `
	SELECT automation.id, automation.title, COALESCE(automation.workflow_id, ''),
	       COALESCE(workflow_revision.title, ''), COALESCE(workflow_revision.revision_number, 0),
	       automation.repository_id, repository.remote_identity,
	       automation.context, automation.timeout_seconds, automation.enabled,
	       automation.version, automation.trigger_type,
	       (SELECT COUNT(*) FROM automation_github_issue_triggers typed_issue WHERE typed_issue.automation_id = automation.id),
	       (SELECT COUNT(*) FROM automation_github_pull_request_triggers typed_pull_request WHERE typed_pull_request.automation_id = automation.id),
	       (SELECT COUNT(*) FROM automation_schedule_triggers typed_schedule WHERE typed_schedule.automation_id = automation.id),
	       COALESCE(issue_trigger.issue_state, pull_request_trigger.pull_request_state, ''),
	       COALESCE(pull_request_trigger.include_drafts, 0),
	       COALESCE(issue_trigger.required_labels_json, pull_request_trigger.required_labels_json, '[]'),
	       COALESCE(pull_request_trigger.base_branches_json, '[]'),
	       COALESCE(issue_trigger.poll_interval_seconds, pull_request_trigger.poll_interval_seconds, 0),
	       COALESCE(schedule_trigger.cron, ''), COALESCE(schedule_trigger.timezone, ''),
	       schedule_trigger.next_due_at,
	       automation.health_status,
	       automation.health_code, automation.health_message,
	       automation.last_checked_at, automation.next_check_at,
	       automation.matched_count, automation.skipped_count,
	       automation.dispatched_count, automation.created_at, automation.updated_at
	       , COALESCE(schedule_trigger.definition_id, '')
	       , COALESCE(definition.name, '')
	       , COALESCE(definition.generation, 0)
	       , COALESCE(schedule_trigger.parameters_json, '{}')
	       , COALESCE(schedule_trigger.concurrency_limit, 0)
	       , COALESCE((
	           SELECT json_group_array(json_object(
	               'id', targets.repository_id,
	               'remote_identity', targets.remote_identity
	           ))
	           FROM (
	               SELECT target.repository_id, repository.remote_identity
	               FROM automation_schedule_repositories target
	               JOIN repositories repository ON repository.id = target.repository_id
	               WHERE target.automation_id = automation.id
	               ORDER BY target.position
	           ) targets
	       ), '[]')
	FROM automations automation
	LEFT JOIN workflows workflow ON workflow.id = automation.workflow_id
	LEFT JOIN workflow_revisions workflow_revision ON workflow_revision.id = workflow.current_revision_id
	JOIN repositories repository ON repository.id = automation.repository_id
	LEFT JOIN automation_github_issue_triggers issue_trigger ON issue_trigger.automation_id = automation.id
	LEFT JOIN automation_github_pull_request_triggers pull_request_trigger ON pull_request_trigger.automation_id = automation.id
	LEFT JOIN automation_schedule_triggers schedule_trigger ON schedule_trigger.automation_id = automation.id
	LEFT JOIN definitions definition ON definition.id = schedule_trigger.definition_id
`

const automationOccurrenceSelect = `
	SELECT occurrence.id, occurrence.automation_id, occurrence.automation_version,
	       occurrence.state, automation.trigger_type,
	       issue.issue_number, issue.issue_url, issue.issue_title,
	       issue.observed_state, issue.observed_labels_json,
	       pull_request.pull_request_number, pull_request.pull_request_url,
	       pull_request.pull_request_title, pull_request.observed_state,
	       pull_request.observed_draft, pull_request.observed_base_branch,
	       pull_request.observed_head_commit, pull_request.observed_labels_json,
	       schedule.kind, schedule.scheduled_at, schedule.run_request_key,
	       schedule.cron, schedule.timezone, schedule.run_id,
	       (
	           SELECT CASE
	               WHEN MAX(CASE WHEN COALESCE(run_execution.state, run_job.state) IN ('preparing', 'running') THEN 1 ELSE 0 END) = 1 THEN 'running'
	               WHEN MAX(CASE WHEN COALESCE(run_execution.state, run_job.state) = 'queued' THEN 1 ELSE 0 END) = 1 THEN 'queued'
	               WHEN MAX(CASE WHEN COALESCE(run_execution.state, run_job.state) = 'blocked' THEN 1 ELSE 0 END) = 1 THEN 'blocked'
	               WHEN MAX(CASE WHEN COALESCE(run_execution.state, run_job.state) = 'failed' THEN 1 ELSE 0 END) = 1 THEN 'failed'
	               WHEN MAX(CASE WHEN COALESCE(run_execution.state, run_job.state) = 'cancelled' THEN 1 ELSE 0 END) = 1 THEN 'cancelled'
	               WHEN COUNT(run_job.id) > 0 THEN 'succeeded'
	               ELSE NULL
	           END
	           FROM jobs run_job
	           LEFT JOIN executions run_execution ON run_execution.id = run_job.execution_id
	           WHERE run_job.run_id = schedule.run_id
	       ),
	       occurrence.task_request_key, occurrence.task_id_snapshot,
	       occurrence.diagnostic, occurrence.created_at, occurrence.updated_at,
	       task.id, task.title, execution.state
	FROM automation_occurrences occurrence
	JOIN automations automation ON automation.id = occurrence.automation_id
	LEFT JOIN automation_github_issue_occurrences issue ON issue.occurrence_id = occurrence.id
	LEFT JOIN automation_github_pull_request_occurrences pull_request ON pull_request.occurrence_id = occurrence.id
	LEFT JOIN automation_schedule_occurrences schedule ON schedule.occurrence_id = occurrence.id
	LEFT JOIN tasks task ON task.id = occurrence.task_id
	LEFT JOIN executions execution ON execution.task_id = task.id
`

func scanAutomation(row scanner) (protocol.Automation, error) {
	var automation protocol.Automation
	var enabled, issueTriggerCount, pullRequestTriggerCount, scheduleTriggerCount int
	var includeDrafts int
	var labels, baseBranches []byte
	var parameters, repositories []byte
	var lastChecked, nextCheck, nextDue sql.NullInt64
	var createdAt, updatedAt int64
	err := row.Scan(
		&automation.ID, &automation.Title, &automation.WorkflowID,
		&automation.WorkflowTitle, &automation.WorkflowRevision,
		&automation.RepositoryID, &automation.RepositoryIdentity,
		&automation.Context, &automation.TimeoutSeconds, &enabled,
		&automation.Version, &automation.Trigger.Type, &issueTriggerCount, &pullRequestTriggerCount, &scheduleTriggerCount,
		&automation.Trigger.State,
		&includeDrafts, &labels, &baseBranches,
		&automation.Trigger.PollIntervalSeconds, &automation.Trigger.Cron, &automation.Trigger.Timezone,
		&nextDue, &automation.Health.Status,
		&automation.Health.Code, &automation.Health.Message,
		&lastChecked, &nextCheck, &automation.MatchedCount,
		&automation.SkippedCount, &automation.DispatchedCount,
		&createdAt, &updatedAt,
		&automation.DefinitionID, &automation.DefinitionName, &automation.DefinitionGeneration,
		&parameters, &automation.ConcurrencyLimit, &repositories,
	)
	if err != nil {
		return automation, err
	}
	automation.Enabled = enabled != 0
	if automation.Trigger.Type != protocol.AutomationTriggerGitHubIssue &&
		automation.Trigger.Type != protocol.AutomationTriggerGitHubPullRequest &&
		automation.Trigger.Type != protocol.AutomationTriggerSchedule {
		return automation, errors.New("Automation has an invalid trigger type")
	}
	if (automation.Trigger.Type == protocol.AutomationTriggerGitHubIssue && (issueTriggerCount != 1 || pullRequestTriggerCount != 0)) ||
		(automation.Trigger.Type == protocol.AutomationTriggerGitHubPullRequest && (pullRequestTriggerCount != 1 || issueTriggerCount != 0)) ||
		(automation.Trigger.Type == protocol.AutomationTriggerSchedule && (scheduleTriggerCount != 1 || issueTriggerCount != 0 || pullRequestTriggerCount != 0)) ||
		(automation.Trigger.Type != protocol.AutomationTriggerSchedule && scheduleTriggerCount != 0) {
		return automation, errors.New("Automation typed trigger rows do not match its trigger type")
	}
	automation.Trigger.IncludeDrafts = includeDrafts != 0
	if err := json.Unmarshal(labels, &automation.Trigger.RequiredLabels); err != nil {
		return automation, err
	}
	if err := json.Unmarshal(baseBranches, &automation.Trigger.BaseBranches); err != nil {
		return automation, err
	}
	if err := json.Unmarshal(parameters, &automation.Parameters); err != nil {
		return automation, err
	}
	if err := json.Unmarshal(repositories, &automation.Repositories); err != nil {
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
	if nextDue.Valid {
		value := fromMillis(nextDue.Int64)
		automation.NextDueAt = &value
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
		page.Automations = append(page.Automations, automation)
	}
	if err := rows.Err(); err != nil {
		return protocol.AutomationPage{}, unavailable(err)
	}
	if err := rows.Close(); err != nil {
		return protocol.AutomationPage{}, unavailable(err)
	}
	if len(page.Automations) > limit {
		page.Automations = page.Automations[:limit]
		last := page.Automations[len(page.Automations)-1]
		page.NextCursor = &protocol.AutomationCursor{UpdatedAtMillis: last.UpdatedAt.UnixMilli(), ID: last.ID}
	}
	for index := range page.Automations {
		if err := s.loadLatestAutomationTask(ctx, &page.Automations[index]); err != nil {
			return protocol.AutomationPage{}, err
		}
	}
	if err := s.loadLatestAutomationRuns(ctx, page.Automations); err != nil {
		return protocol.AutomationPage{}, err
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
	if len(occurrences) > 0 {
		automation.LatestRun = &occurrences[0]
	}
	return protocol.AutomationDetail{Automation: automation, Occurrences: occurrences}, nil
}

func (s *Store) loadLatestAutomationRuns(ctx context.Context, automations []protocol.Automation) error {
	if len(automations) == 0 {
		return nil
	}
	placeholders := make([]string, len(automations))
	args := make([]any, len(automations))
	for index := range automations {
		placeholders[index] = "?"
		args[index] = automations[index].ID
	}
	query := automationOccurrenceSelect + `
		WHERE occurrence.automation_id IN (` + strings.Join(placeholders, ",") + `)
		  AND NOT EXISTS (
			SELECT 1 FROM automation_occurrences newer
			WHERE newer.automation_id = occurrence.automation_id
			  AND (newer.created_at > occurrence.created_at OR
			       (newer.created_at = occurrence.created_at AND newer.id > occurrence.id))
		  )
		ORDER BY occurrence.created_at DESC, occurrence.id DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return unavailable(err)
	}
	defer rows.Close()
	occurrences, err := scanAutomationOccurrences(rows, len(automations))
	if err != nil {
		return err
	}
	byAutomation := make(map[string]*protocol.AutomationOccurrence, len(occurrences))
	for index := range occurrences {
		byAutomation[occurrences[index].AutomationID] = &occurrences[index]
	}
	for index := range automations {
		automations[index].LatestRun = byAutomation[automations[index].ID]
	}
	return nil
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
	value, titleKey, err := normalizeAutomation(
		"", input.Title, input.WorkflowID, "", input.Context,
		input.TimeoutSeconds, input.Trigger,
		input.DefinitionID, input.RepositoryIDs, input.Parameters, input.ConcurrencyLimit, false, true,
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
	baseBranches, err := json.Marshal(value.Trigger.BaseBranches)
	if err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	parametersJSON, err := json.Marshal(value.Parameters)
	if err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	defer tx.Rollback()
	var currentVersion, enabled, currentTimeout, currentInterval, currentIncludeDrafts int
	var issueTriggerCount, pullRequestTriggerCount, scheduleTriggerCount int
	var currentTitle, currentTitleKey, currentContext, currentType, currentState string
	var currentWorkflowID sql.NullString
	var currentDefinitionID string
	var currentConcurrency int
	var currentCron, currentTimezone string
	var currentLabels, currentBaseBranches, currentParameters []byte
	err = tx.QueryRowContext(ctx, `
		SELECT automation.version, automation.enabled, automation.title, automation.title_key,
		       automation.workflow_id, automation.context, automation.timeout_seconds,
		       automation.trigger_type,
		       (SELECT COUNT(*) FROM automation_github_issue_triggers typed_issue WHERE typed_issue.automation_id = automation.id),
		       (SELECT COUNT(*) FROM automation_github_pull_request_triggers typed_pull_request WHERE typed_pull_request.automation_id = automation.id),
		       (SELECT COUNT(*) FROM automation_schedule_triggers typed_schedule WHERE typed_schedule.automation_id = automation.id),
		       COALESCE(issue_trigger.issue_state, pull_request_trigger.pull_request_state, ''),
		       COALESCE(pull_request_trigger.include_drafts, 0),
		       COALESCE(issue_trigger.required_labels_json, pull_request_trigger.required_labels_json, '[]'),
		       COALESCE(pull_request_trigger.base_branches_json, '[]'),
		       COALESCE(issue_trigger.poll_interval_seconds, pull_request_trigger.poll_interval_seconds, 0),
		       COALESCE(schedule_trigger.cron, ''), COALESCE(schedule_trigger.timezone, ''),
		       COALESCE(schedule_trigger.definition_id, ''),
		       COALESCE(schedule_trigger.parameters_json, '{}'),
		       COALESCE(schedule_trigger.concurrency_limit, 0)
		FROM automations automation
		LEFT JOIN automation_github_issue_triggers issue_trigger ON issue_trigger.automation_id = automation.id
		LEFT JOIN automation_github_pull_request_triggers pull_request_trigger ON pull_request_trigger.automation_id = automation.id
		LEFT JOIN automation_schedule_triggers schedule_trigger ON schedule_trigger.automation_id = automation.id
		WHERE automation.id = ?
	`, strings.TrimSpace(automationID)).Scan(
		&currentVersion, &enabled, &currentTitle, &currentTitleKey, &currentWorkflowID,
		&currentContext, &currentTimeout, &currentType, &issueTriggerCount, &pullRequestTriggerCount, &scheduleTriggerCount,
		&currentState, &currentIncludeDrafts,
		&currentLabels, &currentBaseBranches, &currentInterval, &currentCron, &currentTimezone,
		&currentDefinitionID, &currentParameters, &currentConcurrency,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.AutomationDetail{}, ErrNotFound
	}
	if err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	if currentType != value.Trigger.Type {
		return protocol.AutomationDetail{}, conflict("automation_trigger_type_immutable", "trigger type is immutable; create a new Automation")
	}
	if currentType == protocol.AutomationTriggerSchedule && currentDefinitionID != "" && value.DefinitionID == "" {
		return protocol.AutomationDetail{}, invalid("definition_required", "definition_id is required for a scheduled Automation")
	}
	if (currentType == protocol.AutomationTriggerGitHubIssue && (issueTriggerCount != 1 || pullRequestTriggerCount != 0)) ||
		(currentType == protocol.AutomationTriggerGitHubPullRequest && (pullRequestTriggerCount != 1 || issueTriggerCount != 0)) ||
		(currentType == protocol.AutomationTriggerSchedule && (scheduleTriggerCount != 1 || issueTriggerCount != 0 || pullRequestTriggerCount != 0)) ||
		(currentType != protocol.AutomationTriggerSchedule && scheduleTriggerCount != 0) {
		return protocol.AutomationDetail{}, unavailable(errors.New("Automation typed trigger rows do not match its trigger type"))
	}
	currentRepositoryIDs := make([]string, 0)
	if currentType == protocol.AutomationTriggerSchedule && currentDefinitionID != "" {
		rows, queryErr := tx.QueryContext(ctx, `
			SELECT repository_id FROM automation_schedule_repositories
			WHERE automation_id = ? ORDER BY position
		`, automationID)
		if queryErr != nil {
			return protocol.AutomationDetail{}, unavailable(queryErr)
		}
		for rows.Next() {
			var repositoryID string
			if err := rows.Scan(&repositoryID); err != nil {
				rows.Close()
				return protocol.AutomationDetail{}, unavailable(err)
			}
			currentRepositoryIDs = append(currentRepositoryIDs, repositoryID)
		}
		if err := rows.Close(); err != nil {
			return protocol.AutomationDetail{}, unavailable(err)
		}
	}
	exactReplay := currentVersion == input.ExpectedVersion+1 &&
		currentTitle == value.Title && currentTitleKey == titleKey && currentWorkflowID.String == value.WorkflowID &&
		currentContext == value.Context && currentTimeout == value.TimeoutSeconds
	if currentType == protocol.AutomationTriggerSchedule {
		exactReplay = exactReplay && currentCron == value.Trigger.Cron && currentTimezone == value.Trigger.Timezone &&
			currentDefinitionID == value.DefinitionID && currentConcurrency == value.ConcurrencyLimit &&
			bytes.Equal(currentParameters, parametersJSON) && slices.Equal(currentRepositoryIDs, value.RepositoryIDs)
	} else {
		exactReplay = exactReplay && currentState == value.Trigger.State &&
			currentIncludeDrafts == boolInt(value.Trigger.IncludeDrafts) &&
			bytes.Equal(currentLabels, labels) && bytes.Equal(currentBaseBranches, baseBranches) &&
			currentInterval == value.Trigger.PollIntervalSeconds
	}
	if exactReplay {
		if err := tx.Commit(); err != nil {
			return protocol.AutomationDetail{}, unavailable(err)
		}
		return s.Automation(ctx, automationID)
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
	if value.Trigger.Type == protocol.AutomationTriggerSchedule {
		if value.DefinitionID == "" {
			if err := validateAutomationDependencies(ctx, tx, value.WorkflowID, repositoryID, false); err != nil {
				return protocol.AutomationDetail{}, err
			}
		} else {
			if err := validateDefinitionScheduleDependencies(
				ctx, tx, value.DefinitionID, value.RepositoryIDs, value.Parameters, false,
			); err != nil {
				return protocol.AutomationDetail{}, err
			}
			repositoryID = value.RepositoryID
		}
	} else if err := validateAutomationDependencies(ctx, tx, value.WorkflowID, repositoryID, false); err != nil {
		return protocol.AutomationDetail{}, err
	}
	var conflictID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM automations WHERE title_key = ? AND id != ?`, titleKey, automationID).Scan(&conflictID)
	if err == nil {
		return protocol.AutomationDetail{}, conflict("automation_title_conflict", "an Automation with this normalized title already exists")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	now := s.now().UnixMilli()
	result, err := tx.ExecContext(ctx, `
		UPDATE automations
		SET title = ?, title_key = ?, workflow_id = ?, repository_id = ?, context = ?, timeout_seconds = ?,
		    version = version + 1, updated_at = ?
		WHERE id = ? AND version = ? AND enabled = 0
	`, value.Title, titleKey, nullableString(value.WorkflowID), repositoryID, value.Context, value.TimeoutSeconds,
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
	if value.Trigger.Type == protocol.AutomationTriggerGitHubIssue {
		if _, err := tx.ExecContext(ctx, `
			UPDATE automation_github_issue_triggers
			SET issue_state = ?, required_labels_json = ?, poll_interval_seconds = ?
			WHERE automation_id = ?
		`, value.Trigger.State, labels, value.Trigger.PollIntervalSeconds, automationID); err != nil {
			return protocol.AutomationDetail{}, unavailable(err)
		}
	} else if value.Trigger.Type == protocol.AutomationTriggerGitHubPullRequest {
		if _, err := tx.ExecContext(ctx, `
			UPDATE automation_github_pull_request_triggers
			SET pull_request_state = ?, include_drafts = ?, required_labels_json = ?,
			    base_branches_json = ?, poll_interval_seconds = ?
			WHERE automation_id = ?
		`, value.Trigger.State, value.Trigger.IncludeDrafts, labels, baseBranches,
			value.Trigger.PollIntervalSeconds, automationID); err != nil {
			return protocol.AutomationDetail{}, unavailable(err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE automation_schedule_triggers
			SET cron = ?, timezone = ?, definition_id = ?, parameters_json = ?, concurrency_limit = ?
			WHERE automation_id = ?
		`, value.Trigger.Cron, value.Trigger.Timezone, nullableString(value.DefinitionID), parametersJSON,
			value.ConcurrencyLimit, automationID); err != nil {
			return protocol.AutomationDetail{}, unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM automation_schedule_repositories WHERE automation_id = ?`, automationID); err != nil {
			return protocol.AutomationDetail{}, unavailable(err)
		}
		for position, repositoryID := range value.RepositoryIDs {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO automation_schedule_repositories(automation_id, position, repository_id)
				VALUES (?, ?, ?)
			`, automationID, position, repositoryID); err != nil {
				return protocol.AutomationDetail{}, unavailable(err)
			}
		}
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
	_ bool,
) (protocol.AutomationDetail, string, error) {
	s.automationDispatchMu.Lock()
	defer s.automationDispatchMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.AutomationDetail{}, "", unavailable(err)
	}
	defer tx.Rollback()
	var workflowID sql.NullString
	var repositoryID, triggerType, cron, timezone, definitionID string
	var parametersJSON []byte
	var currentEnabled int
	var evaluationToken sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT automation.workflow_id, automation.repository_id, automation.enabled,
		       automation.evaluation_token, automation.trigger_type,
		       COALESCE(schedule.cron, ''), COALESCE(schedule.timezone, ''),
		       COALESCE(schedule.definition_id, ''), COALESCE(schedule.parameters_json, '{}')
		FROM automations automation
		LEFT JOIN automation_schedule_triggers schedule ON schedule.automation_id = automation.id
		WHERE automation.id = ?`,
		strings.TrimSpace(automationID),
	).Scan(&workflowID, &repositoryID, &currentEnabled, &evaluationToken, &triggerType,
		&cron, &timezone, &definitionID, &parametersJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.AutomationDetail{}, "", ErrNotFound
	}
	if err != nil {
		return protocol.AutomationDetail{}, "", unavailable(err)
	}
	if enabled == (currentEnabled != 0) {
		if err := tx.Commit(); err != nil {
			return protocol.AutomationDetail{}, "", unavailable(err)
		}
		detail, err := s.Automation(ctx, automationID)
		return detail, "", err
	}
	if enabled {
		var migrationStatus string
		err := tx.QueryRowContext(ctx, `
			SELECT migration.status
			FROM legacy_poller_imports imported
			JOIN legacy_poller_migrations migration ON migration.id = imported.migration_id
			WHERE imported.automation_id = ?
		`, automationID).Scan(&migrationStatus)
		if err == nil && migrationStatus != "finalized" {
			return protocol.AutomationDetail{}, "", conflict(
				"migration_not_finalized",
				"Finalize the locked legacy poller snapshot before enabling this imported Automation",
			)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return protocol.AutomationDetail{}, "", unavailable(err)
		}
		if triggerType == protocol.AutomationTriggerSchedule && definitionID != "" {
			var repositoryIDs []string
			rows, err := tx.QueryContext(ctx, `
				SELECT repository_id FROM automation_schedule_repositories
				WHERE automation_id = ? ORDER BY position
			`, automationID)
			if err != nil {
				return protocol.AutomationDetail{}, "", unavailable(err)
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return protocol.AutomationDetail{}, "", unavailable(err)
				}
				repositoryIDs = append(repositoryIDs, id)
			}
			if err := rows.Close(); err != nil {
				return protocol.AutomationDetail{}, "", unavailable(err)
			}
			var parameters map[string]string
			if err := json.Unmarshal(parametersJSON, &parameters); err != nil {
				return protocol.AutomationDetail{}, "", unavailable(err)
			}
			if err := validateDefinitionScheduleDependencies(
				ctx, tx, definitionID, repositoryIDs, parameters, true,
			); err != nil {
				return protocol.AutomationDetail{}, "", err
			}
		} else if err := validateAutomationDependencies(ctx, tx, workflowID.String, repositoryID, true); err != nil {
			return protocol.AutomationDetail{}, "", err
		}
	}
	nowTime := s.now()
	now := nowTime.UnixMilli()
	status, code, message := "disabled", "", "Automation is disabled."
	var nextCheck any
	var nextDue any
	if enabled {
		if triggerType == protocol.AutomationTriggerSchedule {
			schedule, _, _, parseErr := parseCronSchedule(cron, timezone)
			if parseErr != nil {
				return protocol.AutomationDetail{}, "", unavailable(fmt.Errorf("stored schedule is invalid: %w", parseErr))
			}
			due, nextErr := schedule.Next(nowTime)
			if nextErr != nil {
				return protocol.AutomationDetail{}, "", invalid("invalid_cron", nextErr.Error())
			}
			status, message, nextDue = "pending", "Waiting for the next scheduled occurrence.", due.UnixMilli()
		} else {
			status, message, nextCheck = "pending", "Waiting for the next GitHub check.", now
		}
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
	if triggerType == protocol.AutomationTriggerSchedule {
		if _, err := tx.ExecContext(ctx, `
			UPDATE automation_schedule_triggers SET next_due_at = ? WHERE automation_id = ?
		`, nextDue, automationID); err != nil {
			return protocol.AutomationDetail{}, "", unavailable(err)
		}
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
	var triggerType string
	if err := s.db.QueryRowContext(ctx, `SELECT trigger_type FROM automations WHERE id = ?`, strings.TrimSpace(automationID)).Scan(&triggerType); errors.Is(err, sql.ErrNoRows) {
		return protocol.AutomationDetail{}, ErrNotFound
	} else if err != nil {
		return protocol.AutomationDetail{}, unavailable(err)
	}
	if triggerType == protocol.AutomationTriggerSchedule {
		return protocol.AutomationDetail{}, conflict("automation_not_provider_trigger", "scheduled Automations use Run now instead of a provider check")
	}
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
	return s.automationOccurrencesPage(ctx, automationID, limit, cursor, "")
}

func (s *Store) automationOccurrencesPage(
	ctx context.Context,
	automationID string,
	limit int,
	cursor *protocol.AutomationOccurrenceCursor,
	occurrenceID string,
) (protocol.AutomationOccurrencePage, error) {
	if limit < 1 || limit > protocol.MaxAutomationPageSize {
		return protocol.AutomationOccurrencePage{}, invalid("invalid_limit", "limit must be between 1 and 200")
	}
	query := automationOccurrenceSelect + ` WHERE occurrence.automation_id = ?`
	args := []any{strings.TrimSpace(automationID)}
	if occurrenceID != "" {
		query += ` AND occurrence.id = ?`
		args = append(args, strings.TrimSpace(occurrenceID))
	}
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
	occurrences, err := scanAutomationOccurrences(rows, limit+1)
	if err != nil {
		return protocol.AutomationOccurrencePage{}, err
	}
	page := protocol.AutomationOccurrencePage{Occurrences: occurrences}
	if len(occurrences) > limit {
		page.Occurrences = occurrences[:limit]
		last := page.Occurrences[len(page.Occurrences)-1]
		page.NextCursor = &protocol.AutomationOccurrenceCursor{CreatedAtMillis: last.CreatedAt.UnixMilli(), ID: last.ID}
	}
	return page, nil
}

func scanAutomationOccurrences(rows *sql.Rows, capacity int) ([]protocol.AutomationOccurrence, error) {
	occurrences := make([]protocol.AutomationOccurrence, 0, capacity)
	for rows.Next() {
		var occurrence protocol.AutomationOccurrence
		var triggerType string
		var issueNumber, pullRequestNumber, observedDraft sql.NullInt64
		var issueURL, issueTitle, issueState, pullRequestURL, pullRequestTitle sql.NullString
		var pullRequestState, baseBranch, headCommit sql.NullString
		var scheduleKind, runRequestKey, scheduleCron, scheduleTimezone, runID, runState sql.NullString
		var scheduledAt sql.NullInt64
		var issueLabels, pullRequestLabels []byte
		var taskID, taskTitle, taskState sql.NullString
		var createdAt, updatedAt int64
		if err := rows.Scan(
			&occurrence.ID, &occurrence.AutomationID, &occurrence.AutomationVersion,
			&occurrence.State, &triggerType, &issueNumber, &issueURL,
			&issueTitle, &issueState, &issueLabels, &pullRequestNumber,
			&pullRequestURL, &pullRequestTitle, &pullRequestState, &observedDraft,
			&baseBranch, &headCommit, &pullRequestLabels,
			&scheduleKind, &scheduledAt, &runRequestKey, &scheduleCron, &scheduleTimezone, &runID, &runState,
			&occurrence.TaskRequestKey, &occurrence.TaskIDSnapshot,
			&occurrence.Diagnostic, &createdAt, &updatedAt,
			&taskID, &taskTitle, &taskState,
		); err != nil {
			return nil, unavailable(err)
		}
		switch triggerType {
		case protocol.AutomationTriggerGitHubIssue:
			if !issueNumber.Valid || !issueURL.Valid || !issueTitle.Valid || !issueState.Valid || issueLabels == nil || pullRequestNumber.Valid {
				return nil, unavailable(errors.New("GitHub issue Occurrence is missing typed metadata"))
			}
			occurrence.IssueNumber, occurrence.IssueURL = int(issueNumber.Int64), issueURL.String
			occurrence.IssueTitle, occurrence.ObservedState = issueTitle.String, issueState.String
			if err := json.Unmarshal(issueLabels, &occurrence.ObservedLabels); err != nil {
				return nil, unavailable(err)
			}
		case protocol.AutomationTriggerGitHubPullRequest:
			if !pullRequestNumber.Valid || !pullRequestURL.Valid || !pullRequestTitle.Valid ||
				!pullRequestState.Valid || !observedDraft.Valid || !baseBranch.Valid || !headCommit.Valid || pullRequestLabels == nil || issueNumber.Valid {
				return nil, unavailable(errors.New("GitHub pull request Occurrence is missing typed metadata"))
			}
			occurrence.PullRequestNumber = int(pullRequestNumber.Int64)
			occurrence.PullRequestURL, occurrence.PullRequestTitle = pullRequestURL.String, pullRequestTitle.String
			occurrence.ObservedState = pullRequestState.String
			draft := observedDraft.Int64 != 0
			occurrence.ObservedDraft = &draft
			occurrence.ObservedBaseBranch, occurrence.ObservedHeadCommit = baseBranch.String, headCommit.String
			if err := json.Unmarshal(pullRequestLabels, &occurrence.ObservedLabels); err != nil {
				return nil, unavailable(err)
			}
		case protocol.AutomationTriggerSchedule:
			if !scheduleKind.Valid || !scheduleCron.Valid || !scheduleTimezone.Valid || issueNumber.Valid || pullRequestNumber.Valid {
				return nil, unavailable(errors.New("schedule Occurrence is missing typed metadata"))
			}
			occurrence.Kind, occurrence.Cron, occurrence.Timezone = scheduleKind.String, scheduleCron.String, scheduleTimezone.String
			if occurrence.Kind == "scheduled" {
				if !scheduledAt.Valid || runRequestKey.Valid {
					return nil, unavailable(errors.New("scheduled Occurrence has invalid identity"))
				}
				value := fromMillis(scheduledAt.Int64)
				occurrence.ScheduledAt = &value
			} else if occurrence.Kind == "run_now" {
				if scheduledAt.Valid || !runRequestKey.Valid {
					return nil, unavailable(errors.New("Run now Occurrence has invalid identity"))
				}
				occurrence.RunRequestKey = runRequestKey.String
			} else {
				return nil, unavailable(errors.New("schedule Occurrence has invalid kind"))
			}
			if runID.Valid {
				occurrence.RunID = runID.String
			}
			if runState.Valid {
				occurrence.RunState = runState.String
			}
		default:
			return nil, unavailable(errors.New("Occurrence has an invalid trigger type"))
		}
		if taskID.Valid {
			occurrence.Task = &protocol.AutomationTaskSummary{ID: taskID.String, Title: taskTitle.String, State: taskState.String}
		}
		occurrence.CreatedAt = fromMillis(createdAt)
		occurrence.UpdatedAt = fromMillis(updatedAt)
		occurrences = append(occurrences, occurrence)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	return occurrences, nil
}
