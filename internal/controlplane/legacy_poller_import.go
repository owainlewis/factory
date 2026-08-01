package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

var maxLegacyImportOccurrences = protocol.MaxAutomationOccurrences

func (s *Store) ImportLegacyPoller(
	ctx context.Context,
	input protocol.ImportLegacyPollerRequest,
) (protocol.LegacyPollerMigration, error) {
	if !input.ConfirmStopped {
		return protocol.LegacyPollerMigration{}, invalid(
			"legacy_poller_confirmation_required",
			"stop factory-poller and confirm it is stopped before Import",
		)
	}
	input.MigrationID = strings.TrimSpace(input.MigrationID)
	input.SnapshotDigest = strings.TrimSpace(input.SnapshotDigest)
	if input.MigrationID == "" || input.SnapshotDigest == "" {
		return protocol.LegacyPollerMigration{}, invalid("invalid_migration", "migration_id and snapshot_digest are required")
	}
	source, err := openLegacyPollerSource(ctx, input.LegacyPollerSelection)
	if err != nil {
		return protocol.LegacyPollerMigration{}, err
	}
	defer source.close()
	bound, err := s.verifyLegacyMigrationBinding(ctx, input.MigrationID, input.SnapshotDigest, source.snapshot)
	if err != nil {
		return protocol.LegacyPollerMigration{}, err
	}
	if bound.Status == "imported" || bound.Status == "finalized" {
		return s.LegacyPollerMigration(ctx, input.MigrationID)
	}
	preview, err := s.legacyPollerMigrationFromSnapshot(ctx, input.MigrationID, "previewed", source.snapshot)
	if err != nil {
		return protocol.LegacyPollerMigration{}, err
	}
	mappings := make(map[string]protocol.LegacyPollerQueueMapping, len(input.Mappings))
	for _, mapping := range input.Mappings {
		mapping.QueueID = strings.TrimSpace(mapping.QueueID)
		if mapping.QueueID == "" || mappings[mapping.QueueID].QueueID != "" {
			return protocol.LegacyPollerMigration{}, invalid("invalid_migration_mapping", "queue mappings must have unique queue_id values")
		}
		mappings[mapping.QueueID] = mapping
	}
	for _, queue := range preview.Queues {
		if queue.Blocking {
			return protocol.LegacyPollerMigration{}, conflict(
				"legacy_ledger_queue_missing",
				"legacy ledger contains observations for queue "+queue.QueueID+" which is missing from poller.toml; restore the matching queue and run Preview again",
			)
		}
		_, mapped := mappings[queue.QueueID]
		if queue.Supported && !mapped {
			return protocol.LegacyPollerMigration{}, invalid("invalid_migration_mapping", "every supported queue requires reviewed Workflow and Automation names")
		}
		if !queue.Supported && mapped {
			return protocol.LegacyPollerMigration{}, invalid("invalid_migration_mapping", "unsupported queue "+queue.Name+" cannot be imported")
		}
	}
	if len(mappings) != preview.Counts.Supported {
		return protocol.LegacyPollerMigration{}, invalid("invalid_migration_mapping", "mapping set does not match the reviewed supported queues")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.LegacyPollerMigration{}, unavailable(err)
	}
	defer tx.Rollback()
	var status, storedDigest string
	if err := tx.QueryRowContext(ctx, `
		SELECT status, snapshot_digest FROM legacy_poller_migrations WHERE id = ?
	`, input.MigrationID).Scan(&status, &storedDigest); errors.Is(err, sql.ErrNoRows) {
		return protocol.LegacyPollerMigration{}, ErrNotFound
	} else if err != nil {
		return protocol.LegacyPollerMigration{}, unavailable(err)
	}
	if storedDigest != source.snapshot.Digest || storedDigest != input.SnapshotDigest {
		return protocol.LegacyPollerMigration{}, conflict("migration_source_changed", "legacy source no longer matches Preview; stop factory-poller and run Preview again")
	}
	if status != "previewed" {
		if err := tx.Commit(); err != nil {
			return protocol.LegacyPollerMigration{}, unavailable(err)
		}
		return s.LegacyPollerMigration(ctx, input.MigrationID)
	}
	var workflowCount, automationCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows`).Scan(&workflowCount); err != nil {
		return protocol.LegacyPollerMigration{}, unavailable(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM automations`).Scan(&automationCount); err != nil {
		return protocol.LegacyPollerMigration{}, unavailable(err)
	}
	if workflowCount+len(mappings) > protocol.MaxWorkflows {
		return protocol.LegacyPollerMigration{}, conflict("workflow_limit_reached", "import would exceed the Workflow limit")
	}
	if automationCount+len(mappings) > protocol.MaxAutomations {
		return protocol.LegacyPollerMigration{}, conflict("automation_limit_reached", "import would exceed the Automation limit")
	}
	additionalOccurrences := 0
	for _, observation := range source.snapshot.Observations {
		if _, supported := mappings[observation.QueueID]; supported {
			additionalOccurrences++
		}
	}
	var occurrenceCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_occurrences`).Scan(&occurrenceCount); err != nil {
		return protocol.LegacyPollerMigration{}, unavailable(err)
	}
	if occurrenceCount+additionalOccurrences > maxLegacyImportOccurrences {
		return protocol.LegacyPollerMigration{}, conflict("occurrence_limit_reached", "import would exceed the durable Occurrence limit")
	}

	queueByID := make(map[string]legacyPollerQueue, len(source.snapshot.Config.Queues))
	previewByID := make(map[string]protocol.LegacyPollerQueue, len(preview.Queues))
	for index, queue := range source.snapshot.Config.Queues {
		queueByID[legacyQueueID(queue)] = source.snapshot.Config.Queues[index]
	}
	for _, queue := range preview.Queues {
		previewByID[queue.QueueID] = queue
	}
	type importedQueue struct {
		queue        legacyPollerQueue
		preview      protocol.LegacyPollerQueue
		workflowID   string
		automationID string
		name         string
	}
	imports := make(map[string]importedQueue, len(mappings))
	workflowNames := make(map[string]bool, len(mappings))
	automationNames := make(map[string]bool, len(mappings))
	now := s.now().UnixMilli()
	for queueID, mapping := range mappings {
		queue := queueByID[queueID]
		queuePreview := previewByID[queueID]
		workflowValue, workflowNameKey, normalizeErr := normalizeWorkflowRevision(
			"legacy-poller:"+input.MigrationID+":workflow:"+queueID,
			"", "", mapping.WorkflowName, "Imported from legacy poller queue "+queue.Name+".", queue.Prompt,
		)
		if normalizeErr != nil {
			return protocol.LegacyPollerMigration{}, normalizeErr
		}
		if workflowNames[workflowNameKey] {
			return protocol.LegacyPollerMigration{}, conflict("workflow_name_conflict", "imported Workflow names must be unique")
		}
		workflowNames[workflowNameKey] = true
		var conflictID string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM workflows WHERE current_name_key = ?`, workflowNameKey).Scan(&conflictID); err == nil {
			return protocol.LegacyPollerMigration{}, conflict("workflow_name_conflict", "a Workflow named "+workflowValue.Name+" already exists; choose an explicit rename")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return protocol.LegacyPollerMigration{}, unavailable(err)
		}
		workflowID, idErr := newID()
		if idErr != nil {
			return protocol.LegacyPollerMigration{}, unavailable(idErr)
		}
		revisionID, idErr := newID()
		if idErr != nil {
			return protocol.LegacyPollerMigration{}, unavailable(idErr)
		}
		workflowDigest, digestErr := workflowMutationDigest(workflowValue)
		if digestErr != nil {
			return protocol.LegacyPollerMigration{}, unavailable(digestErr)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workflows(id, enabled, current_revision_id, current_name_key, created_at, updated_at)
			VALUES (?, 1, ?, ?, ?, ?)
		`, workflowID, revisionID, workflowNameKey, now, now); err != nil {
			return protocol.LegacyPollerMigration{}, unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workflow_revisions(
				id, workflow_id, revision_number, request_key, request_digest,
				name, summary, instructions, created_at
			) VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?)
		`, revisionID, workflowID, workflowValue.RequestKey, workflowDigest,
			workflowValue.Name, workflowValue.Summary, workflowValue.Instructions, now); err != nil {
			return protocol.LegacyPollerMigration{}, unavailable(err)
		}

		automationValue, automationNameKey, normalizeErr := normalizeAutomation(
			"legacy-poller:"+input.MigrationID+":automation:"+queueID,
			mapping.AutomationName, workflowID, queuePreview.RepositoryID, "",
			queue.TimeoutSeconds,
			protocol.AutomationTrigger{
				Type: protocol.AutomationTriggerGitHubIssue, State: queue.Status,
				RequiredLabels:      append([]string(nil), queue.Labels...),
				PollIntervalSeconds: queuePreview.PollIntervalSeconds,
			}, true,
		)
		if normalizeErr != nil {
			return protocol.LegacyPollerMigration{}, normalizeErr
		}
		if automationNames[automationNameKey] {
			return protocol.LegacyPollerMigration{}, conflict("automation_name_conflict", "imported Automation names must be unique")
		}
		automationNames[automationNameKey] = true
		if err := tx.QueryRowContext(ctx, `SELECT id FROM automations WHERE name_key = ?`, automationNameKey).Scan(&conflictID); err == nil {
			return protocol.LegacyPollerMigration{}, conflict("automation_name_conflict", "an Automation named "+automationValue.Name+" already exists; choose an explicit rename")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return protocol.LegacyPollerMigration{}, unavailable(err)
		}
		automationID, idErr := s.newAutomationID(ctx, tx)
		if idErr != nil {
			return protocol.LegacyPollerMigration{}, idErr
		}
		automationDigest, digestErr := automationDigest(automationValue)
		if digestErr != nil {
			return protocol.LegacyPollerMigration{}, unavailable(digestErr)
		}
		labels, _ := json.Marshal(automationValue.Trigger.RequiredLabels)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO automations(
				id, request_key, request_digest, name, name_key, workflow_id,
				repository_id, context, timeout_seconds, trigger_type, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, 'github_issue', ?, ?)
		`, automationID, automationValue.RequestKey, automationDigest, automationValue.Name,
			automationNameKey, workflowID, queuePreview.RepositoryID,
			automationValue.TimeoutSeconds, now, now); err != nil {
			return protocol.LegacyPollerMigration{}, unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO automation_github_issue_triggers(
				automation_id, issue_state, required_labels_json, poll_interval_seconds
			) VALUES (?, ?, ?, ?)
		`, automationID, automationValue.Trigger.State, labels,
			automationValue.Trigger.PollIntervalSeconds); err != nil {
			return protocol.LegacyPollerMigration{}, unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO legacy_poller_imports(
				migration_id, queue_id, queue_name, workflow_id, automation_id
			) VALUES (?, ?, ?, ?, ?)
		`, input.MigrationID, queueID, queue.Name, workflowID, automationID); err != nil {
			return protocol.LegacyPollerMigration{}, unavailable(err)
		}
		imports[queueID] = importedQueue{
			queue: queue, preview: queuePreview, workflowID: workflowID,
			automationID: automationID, name: automationValue.Name,
		}
	}

	dispatchedByAutomation := make(map[string]int64)
	matchedByAutomation := make(map[string]int64)
	for _, observation := range source.snapshot.Observations {
		imported, supported := imports[observation.QueueID]
		if !supported {
			continue
		}
		if err := s.importLegacyObservation(ctx, tx, input.MigrationID, imported, observation, now); err != nil {
			return protocol.LegacyPollerMigration{}, err
		}
		matchedByAutomation[imported.automationID]++
		var linked int
		if err := tx.QueryRowContext(ctx, `
			SELECT task_id IS NOT NULL FROM automation_occurrences WHERE task_request_key = ?
		`, observation.RequestKey).Scan(&linked); err != nil {
			return protocol.LegacyPollerMigration{}, unavailable(err)
		}
		if linked != 0 {
			dispatchedByAutomation[imported.automationID]++
		}
	}
	for automationID, matched := range matchedByAutomation {
		if _, err := tx.ExecContext(ctx, `
			UPDATE automations SET matched_count = ?, dispatched_count = ? WHERE id = ?
		`, matched, dispatchedByAutomation[automationID], automationID); err != nil {
			return protocol.LegacyPollerMigration{}, unavailable(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE legacy_poller_migrations SET status = 'imported', updated_at = ?
		WHERE id = ? AND status = 'previewed'
	`, now, input.MigrationID); err != nil {
		return protocol.LegacyPollerMigration{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.LegacyPollerMigration{}, unavailable(err)
	}
	return s.LegacyPollerMigration(ctx, input.MigrationID)
}

func (s *Store) importLegacyObservation(
	ctx context.Context,
	tx *sql.Tx,
	migrationID string,
	imported struct {
		queue        legacyPollerQueue
		preview      protocol.LegacyPollerQueue
		workflowID   string
		automationID string
		name         string
	},
	observation legacyPollerObservation,
	now int64,
) error {
	issueNumber, err := legacyIssueNumber(observation.IssueKey)
	if err != nil {
		return invalid("invalid_legacy_observation", err.Error())
	}
	occurrenceID, err := newID()
	if err != nil {
		return unavailable(err)
	}
	state, diagnostic := "pending", "legacy_pending_requires_resume_or_skip"
	var taskID, taskIDSnapshot, title, description string
	var timeout any
	var requestJSON any
	if observation.State == "pending" {
		request, decodeErr := validateLegacyPendingRequest(observation, imported.preview.RepositoryIdentity)
		requestJSON = observation.Request
		if decodeErr != nil {
			state, diagnostic = "failed", "legacy_pending_invalid_requires_skip"
			title = "Legacy GitHub issue " + observation.IssueKey
		} else {
			title = request.Title
		}
	} else {
		state, diagnostic = "task_deleted", "legacy_task_deleted"
		var found bool
		taskID, title, description, timeout, found, err = lookupLegacySubmittedTask(
			ctx, tx, observation, imported.preview.RepositoryID,
		)
		if err != nil {
			return err
		}
		if found {
			state, diagnostic, taskIDSnapshot = "dispatched", "legacy_task_reused", taskID
		} else {
			taskIDSnapshot = observation.TaskID
			taskID = ""
			title = "Legacy GitHub issue " + observation.IssueKey
			timeout = imported.queue.TimeoutSeconds
		}
	}
	createdAt := observation.CreatedAt
	if createdAt <= 0 {
		createdAt = now
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO automation_occurrences(
			id, automation_id, automation_version, automation_name,
			workflow_revision_id, repository_id, repository_identity, context,
			timeout_seconds, state, resolved_prompt, task_request_key, task_id,
			task_id_snapshot, diagnostic, legacy_task_request_json, created_at, updated_at
		) VALUES (?, ?, 1, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, occurrenceID, imported.automationID, imported.name,
		imported.preview.RepositoryID, imported.preview.RepositoryIdentity,
		nullableImportedString(description), timeout, state, nullableImportedString(description),
		observation.RequestKey, nullableImportedString(taskID), taskIDSnapshot, diagnostic,
		requestJSON, createdAt, now); err != nil {
		return unavailable(err)
	}
	labels, _ := json.Marshal(imported.queue.Labels)
	issueURL := "https://github.com/" + strings.TrimSuffix(imported.queue.Project, ".git") + "/issues/" + fmt.Sprint(issueNumber)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO automation_github_issue_occurrences(
			occurrence_id, automation_id, issue_number, issue_url, issue_title,
			observed_state, observed_labels_json, configured_state, required_labels_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, occurrenceID, imported.automationID, issueNumber, issueURL, title,
		imported.queue.Status, labels, imported.queue.Status, labels); err != nil {
		return unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO legacy_poller_observations(
			occurrence_id, migration_id, queue_id, issue_key, source_state
		) VALUES (?, ?, ?, ?, ?)
	`, occurrenceID, migrationID, observation.QueueID, observation.IssueKey, observation.State); err != nil {
		return unavailable(err)
	}
	return nil
}

func validateLegacyPendingRequest(
	observation legacyPollerObservation,
	repositoryIdentity string,
) (protocol.CreateTaskRequest, error) {
	var request protocol.CreateTaskRequest
	if len(observation.Request) == 0 || len(observation.Request) > protocol.MaxBodyBytes {
		return request, invalid("invalid_legacy_pending_request", "pending observation "+observation.RequestKey+" has an empty or oversized request")
	}
	if err := decodeAutomationJSON(observation.Request, &request); err != nil {
		return request, invalid("invalid_legacy_pending_request", "pending observation "+observation.RequestKey+" has invalid request JSON: "+err.Error())
	}
	if strings.TrimSpace(request.RequestKey) != observation.RequestKey || strings.TrimSpace(request.Title) == "" ||
		strings.TrimSpace(request.Description) == "" || request.TimeoutSeconds < 1 ||
		request.TimeoutSeconds > int(protocol.MaxTimeout.Seconds()) {
		return request, invalid("invalid_legacy_pending_request", "pending observation "+observation.RequestKey+" has invalid immutable task fields")
	}
	if request.Route == nil || request.WorkerID != "" || request.RepositoryID != "" ||
		request.WorkflowRevisionID != "" || request.Context != "" {
		return request, invalid("invalid_legacy_pending_request", "pending observation "+observation.RequestKey+" is not a legacy GitHub routed task")
	}
	if err := normalizeTaskRoute(request.Route); err != nil {
		return request, invalid("invalid_legacy_pending_request", "pending observation "+observation.RequestKey+" has an invalid route")
	}
	if !strings.EqualFold(request.Route.RepositoryRemoteIdentity, repositoryIdentity) ||
		request.Route.SourceAccess.Provider != "github" || request.Route.SourceAccess.Hostname != "github.com" {
		return request, invalid("invalid_legacy_pending_request", "pending observation "+observation.RequestKey+" routes to a different repository or provider")
	}
	return request, nil
}

func lookupLegacySubmittedTask(
	ctx context.Context,
	tx *sql.Tx,
	observation legacyPollerObservation,
	repositoryID string,
) (taskID, title, description string, timeout any, found bool, resultErr error) {
	query := `SELECT id, request_key, title, description, repository_id, timeout_seconds FROM tasks WHERE id = ?`
	lookup := observation.TaskID
	if lookup == "" {
		query = `SELECT id, request_key, title, description, repository_id, timeout_seconds FROM tasks WHERE request_key = ?`
		lookup = observation.RequestKey
	}
	var requestKey, taskRepositoryID string
	var timeoutSeconds int
	err := tx.QueryRowContext(ctx, query, lookup).Scan(
		&taskID, &requestKey, &title, &description, &taskRepositoryID, &timeoutSeconds,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", nil, false, nil
	}
	if err != nil {
		return "", "", "", nil, false, unavailable(err)
	}
	if requestKey != observation.RequestKey || taskRepositoryID != repositoryID {
		return "", "", "", nil, false, conflict(
			"legacy_task_conflict",
			"submitted observation "+observation.RequestKey+" points to a Task with conflicting request or repository identity",
		)
	}
	return taskID, title, description, timeoutSeconds, true, nil
}

func nullableImportedString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) verifyLegacyMigrationBinding(
	ctx context.Context,
	migrationID string,
	snapshotDigest string,
	snapshot legacyPollerSnapshot,
) (protocol.LegacyPollerMigration, error) {
	migration, err := s.LegacyPollerMigration(ctx, migrationID)
	if err != nil {
		return migration, err
	}
	if migration.SnapshotDigest != snapshotDigest || migration.SnapshotDigest != snapshot.Digest ||
		migration.ConfigPath != snapshot.ConfigPath || migration.DataHome != snapshot.DataHome ||
		migration.WorkingDirectory != snapshot.WorkingDirectory ||
		migration.DataDirectory != snapshot.DataDirectory || migration.LedgerPath != snapshot.LedgerPath ||
		migration.ArchiveRoot != snapshot.ArchiveRoot {
		return migration, conflict("migration_source_changed", "legacy source or selection no longer matches Preview; stop factory-poller and run Preview again")
	}
	return migration, nil
}

func (s *Store) LegacyPollerMigration(
	ctx context.Context,
	migrationID string,
) (protocol.LegacyPollerMigration, error) {
	result := protocol.LegacyPollerMigration{
		Queues: []protocol.LegacyPollerQueue{}, Automations: []protocol.Automation{},
		Occurrences: []protocol.AutomationOccurrence{}, Errors: []string{},
	}
	var createdAt, updatedAt int64
	var unimportedPending, unimportedSubmitted int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, snapshot_digest, status, config_path, data_home, working_directory,
		       data_directory, ledger_path, archive_root, archive_path, queue_count,
		       supported_queue_count, unsupported_queue_count,
		       unimported_pending_observation_count, unimported_submitted_observation_count,
		       created_at, updated_at
		FROM legacy_poller_migrations WHERE id = ?
	`, strings.TrimSpace(migrationID)).Scan(
		&result.ID, &result.SnapshotDigest, &result.Status, &result.ConfigPath,
		&result.DataHome, &result.WorkingDirectory, &result.DataDirectory,
		&result.LedgerPath, &result.ArchiveRoot, &result.ArchivePath,
		&result.Counts.Queues, &result.Counts.Supported, &result.Counts.Unsupported,
		&unimportedPending, &unimportedSubmitted,
		&createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, unavailable(err)
	}
	result.CreatedAt, result.UpdatedAt = fromMillis(createdAt), fromMillis(updatedAt)
	rows, err := s.db.QueryContext(ctx, `
		SELECT automation_id FROM legacy_poller_imports
		WHERE migration_id = ? ORDER BY queue_id
	`, result.ID)
	if err != nil {
		return result, unavailable(err)
	}
	var automationIDs []string
	for rows.Next() {
		var automationID string
		if err := rows.Scan(&automationID); err != nil {
			rows.Close()
			return result, unavailable(err)
		}
		automationIDs = append(automationIDs, automationID)
	}
	if err := rows.Close(); err != nil {
		return result, unavailable(err)
	}
	for _, automationID := range automationIDs {
		detail, err := s.Automation(ctx, automationID)
		if err != nil {
			return result, err
		}
		result.Automations = append(result.Automations, detail.Automation)
		occurrences, err := s.allAutomationOccurrences(ctx, automationID)
		if err != nil {
			return result, err
		}
		result.Occurrences = append(result.Occurrences, occurrences...)
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN occurrence.state IN ('pending', 'dispatching', 'failed')
		                         OR occurrence.legacy_task_request_json IS NOT NULL THEN 1 ELSE 0 END), 0)
		FROM automation_occurrences occurrence
		JOIN legacy_poller_observations legacy ON legacy.occurrence_id = occurrence.id
		WHERE legacy.migration_id = ?
	`, result.ID).Scan(&result.Counts.Submitted, &result.Counts.Pending); err != nil {
		return result, unavailable(err)
	}
	result.Counts.Submitted -= result.Counts.Pending
	result.Counts.Pending += unimportedPending
	result.Counts.Submitted += unimportedSubmitted
	return result, nil
}

func (s *Store) allAutomationOccurrences(
	ctx context.Context,
	automationID string,
) ([]protocol.AutomationOccurrence, error) {
	result := []protocol.AutomationOccurrence{}
	var cursor *protocol.AutomationOccurrenceCursor
	for {
		page, err := s.AutomationOccurrencesPage(ctx, automationID, protocol.MaxAutomationPageSize, cursor)
		if err != nil {
			return nil, err
		}
		result = append(result, page.Occurrences...)
		if page.NextCursor == nil {
			return result, nil
		}
		cursor = page.NextCursor
	}
}

func (s *Store) ActiveLegacyPollerMigration(
	ctx context.Context,
) (*protocol.LegacyPollerMigration, error) {
	var migrationID string
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM legacy_poller_migrations
		WHERE status = 'imported'
		ORDER BY created_at DESC, id DESC LIMIT 1
	`).Scan(&migrationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, unavailable(err)
	}
	migration, err := s.LegacyPollerMigration(ctx, migrationID)
	if err != nil {
		return nil, err
	}
	return &migration, nil
}

func equalLegacyTask(detail protocol.TaskDetail, request protocol.CreateTaskRequest, repositoryID string) bool {
	return detail.Task.RequestKey == strings.TrimSpace(request.RequestKey) &&
		detail.Task.Title == strings.TrimSpace(request.Title) &&
		detail.Task.Description == request.Description &&
		detail.Task.RepositoryID == repositoryID &&
		detail.Task.TimeoutSeconds == request.TimeoutSeconds
}
