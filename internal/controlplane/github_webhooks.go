package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

type GitHubPullRequestWebhook struct {
	DeliveryID         string
	Action             string
	RepositoryIdentity string
	PullRequest        protocol.GitHubPullRequestMatch
}

type webhookOccurrenceAdmission struct {
	ID           string
	AutomationID string
	DefinitionID string
	RepositoryID string
	Parameters   map[string]string
	Snapshot     protocol.DefinitionSnapshot
	Prompt       string
}

func (s *Store) AcceptGitHubPullRequestWebhook(
	ctx context.Context,
	delivery GitHubPullRequestWebhook,
	payload []byte,
) (int, error) {
	s.automationDispatchMu.Lock()
	defer s.automationDispatchMu.Unlock()
	delivery.DeliveryID = strings.TrimSpace(delivery.DeliveryID)
	delivery.Action = strings.ToLower(strings.TrimSpace(delivery.Action))
	delivery.RepositoryIdentity = strings.ToLower(strings.Trim(strings.TrimSpace(delivery.RepositoryIdentity), "/"))
	if delivery.DeliveryID == "" || len(delivery.DeliveryID) > 200 {
		return 0, invalid("invalid_delivery_id", "X-GitHub-Delivery is required and limited to 200 bytes")
	}
	if delivery.Action != "opened" && delivery.Action != "synchronize" {
		return 0, invalid("unsupported_webhook_action", "only pull_request opened and synchronize actions are supported")
	}
	if !strings.HasPrefix(delivery.RepositoryIdentity, "github.com/") || delivery.PullRequest.Number < 1 ||
		delivery.PullRequest.URL == "" || delivery.PullRequest.BaseBranch == "" || delivery.PullRequest.HeadCommit == "" {
		return 0, invalid("invalid_webhook_payload", "the pull_request webhook payload is missing required repository or revision fields")
	}
	payloadDigest := sha256.Sum256(payload)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, unavailable(err)
	}
	defer tx.Rollback()
	var storedDigest []byte
	err = tx.QueryRowContext(ctx, `SELECT payload_digest FROM github_webhook_deliveries WHERE delivery_id = ?`, delivery.DeliveryID).Scan(&storedDigest)
	if err == nil && !bytes.Equal(storedDigest, payloadDigest[:]) {
		return 0, conflict("delivery_id_conflict", "X-GitHub-Delivery was already used with a different payload")
	}
	firstDelivery := errors.Is(err, sql.ErrNoRows)
	if err != nil && !firstDelivery {
		return 0, unavailable(err)
	}
	now := s.now().UnixMilli()
	if firstDelivery {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO github_webhook_deliveries(
				delivery_id, payload_digest, event, action, repository_identity,
				pull_request_number, pull_request_url, pull_request_title,
				base_branch, head_commit, state, created_at, updated_at
			) VALUES (?, ?, 'pull_request', ?, ?, ?, ?, ?, ?, ?, 'accepted', ?, ?)
		`, delivery.DeliveryID, payloadDigest[:], delivery.Action, delivery.RepositoryIdentity,
			delivery.PullRequest.Number, delivery.PullRequest.URL, delivery.PullRequest.Title,
			delivery.PullRequest.BaseBranch, delivery.PullRequest.HeadCommit, now, now); err != nil {
			return 0, unavailable(err)
		}
		type matchingAutomation struct {
			id, title, repositoryID, definitionID string
			version                               int
			parametersJSON                        []byte
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT automation.id, automation.title, automation.version, automation.repository_id,
			       webhook.definition_id, webhook.parameters_json
			FROM automations automation
			JOIN repositories repository ON repository.id = automation.repository_id
			JOIN automation_github_webhook_triggers webhook ON webhook.automation_id = automation.id
			JOIN definitions definition ON definition.id = webhook.definition_id
			WHERE automation.enabled = 1
			  AND repository.enabled = 1
			  AND definition.archived = 0
			  AND lower(repository.remote_identity) = ?
			  AND EXISTS (SELECT 1 FROM json_each(webhook.actions_json) WHERE value = ?)
			ORDER BY automation.id
		`, delivery.RepositoryIdentity, delivery.Action)
		if err != nil {
			return 0, unavailable(err)
		}
		var matches []matchingAutomation
		for rows.Next() {
			var match matchingAutomation
			if err := rows.Scan(&match.id, &match.title, &match.version, &match.repositoryID, &match.definitionID, &match.parametersJSON); err != nil {
				rows.Close()
				return 0, unavailable(err)
			}
			matches = append(matches, match)
		}
		if err := rows.Close(); err != nil {
			return 0, unavailable(err)
		}
		for _, match := range matches {
			definition, err := scanDefinition(tx.QueryRowContext(ctx, definitionSelect+` WHERE id = ?`, match.definitionID))
			if err != nil {
				return 0, unavailable(err)
			}
			snapshot := definition.Snapshot()
			snapshotJSON, err := json.Marshal(snapshot)
			if err != nil {
				return 0, unavailable(err)
			}
			prompt, err := protocol.ResolveGitHubWebhookPrompt(snapshot.Prompt, delivery.DeliveryID,
				delivery.Action, delivery.RepositoryIdentity, delivery.PullRequest)
			if err != nil {
				return 0, unavailable(err)
			}
			occurrenceID, err := newID()
			if err != nil {
				return 0, unavailable(err)
			}
			runRequestKey := "automation:" + match.id + ":webhook:" + delivery.DeliveryID
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO automation_occurrences(
					id, automation_id, automation_version, automation_title, workflow_revision_id,
					repository_id, repository_identity, context, timeout_seconds, state,
					resolved_prompt, task_request_key, created_at, updated_at
				) VALUES (?, ?, ?, ?, NULL, ?, ?, NULL, NULL, 'pending', ?, ?, ?, ?)
			`, occurrenceID, match.id, match.version, match.title, match.repositoryID,
				delivery.RepositoryIdentity, prompt, runRequestKey, now, now); err != nil {
				return 0, unavailable(err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO automation_github_webhook_occurrences(
					occurrence_id, automation_id, delivery_id, event, action,
					pull_request_number, pull_request_url, pull_request_title, base_branch,
					head_commit, definition_id, definition_snapshot, parameters_json
				) VALUES (?, ?, ?, 'pull_request', ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, occurrenceID, match.id, delivery.DeliveryID, delivery.Action,
				delivery.PullRequest.Number, delivery.PullRequest.URL, delivery.PullRequest.Title,
				delivery.PullRequest.BaseBranch, delivery.PullRequest.HeadCommit,
				match.definitionID, snapshotJSON, match.parametersJSON); err != nil {
				return 0, unavailable(err)
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE automations SET matched_count = matched_count + 1, last_checked_at = ?,
				    health_status = 'pending', health_code = '', health_message = 'Webhook accepted; starting Run.', updated_at = ?
				WHERE id = ?
			`, now, now, match.id); err != nil {
				return 0, unavailable(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, unavailable(err)
	}
	return s.dispatchGitHubWebhookOccurrences(ctx, delivery.DeliveryID)
}

func (s *Store) dispatchGitHubWebhookOccurrences(ctx context.Context, deliveryID string) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT occurrence.id, occurrence.automation_id, occurrence.repository_id,
		       occurrence.resolved_prompt, occurrence.task_request_key,
		       webhook.definition_snapshot, webhook.parameters_json
		FROM automation_occurrences occurrence
		JOIN automation_github_webhook_occurrences webhook ON webhook.occurrence_id = occurrence.id
		WHERE webhook.delivery_id = ? AND occurrence.state IN ('pending', 'failed')
		ORDER BY occurrence.id
	`, deliveryID)
	if err != nil {
		return 0, unavailable(err)
	}
	var admissions []webhookOccurrenceAdmission
	for rows.Next() {
		var value webhookOccurrenceAdmission
		var snapshotJSON, parametersJSON []byte
		var requestKey string
		if err := rows.Scan(&value.ID, &value.AutomationID, &value.RepositoryID, &value.Prompt,
			&requestKey, &snapshotJSON, &parametersJSON); err != nil {
			rows.Close()
			return 0, unavailable(err)
		}
		if err := json.Unmarshal(snapshotJSON, &value.Snapshot); err != nil {
			rows.Close()
			return 0, unavailable(err)
		}
		value.DefinitionID = value.Snapshot.ID
		if err := json.Unmarshal(parametersJSON, &value.Parameters); err != nil {
			rows.Close()
			return 0, unavailable(err)
		}
		admissions = append(admissions, value)
	}
	if err := rows.Close(); err != nil {
		return 0, unavailable(err)
	}
	dispatched := 0
	var firstDispatchError error
	for _, admission := range admissions {
		_, _, err := s.createWebhookRun(ctx, protocol.CreateRunRequest{
			RequestKey:   "automation:" + admission.AutomationID + ":webhook:" + deliveryID,
			DefinitionID: admission.DefinitionID, RepositoryIDs: []string{admission.RepositoryID},
			ConcurrencyLimit: 1, Parameters: admission.Parameters,
		}, admission.Snapshot, admission.Prompt, admission.ID)
		now := s.now().UnixMilli()
		if err != nil {
			diagnostic := truncateAutomationDiagnostic(err.Error())
			_, _ = s.db.ExecContext(ctx, `
				UPDATE automation_occurrences SET state = 'failed', diagnostic = ?, updated_at = ? WHERE id = ?;
			`, diagnostic, now, admission.ID)
			_, _ = s.db.ExecContext(ctx, `
				UPDATE automations SET health_status = 'error', health_code = 'webhook_dispatch_failed',
				    health_message = ?, updated_at = ? WHERE id = ?
			`, diagnostic, now, admission.AutomationID)
			_, _ = s.db.ExecContext(ctx, `UPDATE github_webhook_deliveries SET state = 'failed', diagnostic = ?, updated_at = ? WHERE delivery_id = ?`, diagnostic, now, deliveryID)
			if firstDispatchError == nil {
				firstDispatchError = err
			}
			continue
		}
		result, err := s.db.ExecContext(ctx, `
			UPDATE automation_occurrences SET state = 'dispatched', diagnostic = '', updated_at = ?
			WHERE id = ? AND state IN ('pending', 'failed')
		`, now, admission.ID)
		if err != nil {
			return dispatched, unavailable(err)
		}
		changed, _ := result.RowsAffected()
		if changed == 1 {
			dispatched++
			_, _ = s.db.ExecContext(ctx, `UPDATE automations SET dispatched_count = dispatched_count + 1,
				health_status = 'healthy', health_code = '', health_message = 'Latest webhook started a Run.', updated_at = ? WHERE id = ?`, now, admission.AutomationID)
		}
	}
	now := s.now().UnixMilli()
	var failed int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM automation_occurrences occurrence
		JOIN automation_github_webhook_occurrences webhook ON webhook.occurrence_id = occurrence.id
		WHERE webhook.delivery_id = ? AND occurrence.state = 'failed'
	`, deliveryID).Scan(&failed); err != nil {
		return dispatched, unavailable(err)
	}
	state := "completed"
	diagnostic := ""
	if failed > 0 {
		state = "failed"
		diagnostic = "one or more matching Automations could not start a Run"
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE github_webhook_deliveries SET state = ?, diagnostic = ?, updated_at = ? WHERE delivery_id = ?`, state, diagnostic, now, deliveryID); err != nil {
		return dispatched, unavailable(err)
	}
	if firstDispatchError != nil {
		return dispatched, firstDispatchError
	}
	return dispatched, nil
}
