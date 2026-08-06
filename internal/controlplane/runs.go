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

	"github.com/owainlewis/factory/internal/protocol"
)

type normalizedRunRequest struct {
	RequestKey       string            `json:"request_key"`
	DefinitionID     string            `json:"definition_id"`
	RepositoryIDs    []string          `json:"repository_ids"`
	ConcurrencyLimit int               `json:"concurrency_limit"`
	Parameters       map[string]string `json:"parameters"`
}

type legacyNormalizedRunRequest struct {
	RequestKey   string            `json:"request_key"`
	DefinitionID string            `json:"definition_id"`
	RepositoryID string            `json:"repository_id"`
	Parameters   map[string]string `json:"parameters"`
}

const defaultRunConcurrency = 3

func normalizeRunRequest(input protocol.CreateRunRequest) (normalizedRunRequest, []byte, error) {
	value := normalizedRunRequest{
		RequestKey: strings.TrimSpace(input.RequestKey), DefinitionID: strings.TrimSpace(input.DefinitionID),
		ConcurrencyLimit: input.ConcurrencyLimit, Parameters: input.Parameters,
	}
	if value.RequestKey == "" || len(value.RequestKey) > 200 {
		return value, nil, invalid("invalid_request_key", "request_key is required and limited to 200 bytes")
	}
	if value.DefinitionID == "" {
		return value, nil, invalid("definition_required", "definition_id is required")
	}
	if strings.TrimSpace(input.RepositoryID) != "" && len(input.RepositoryIDs) > 0 {
		return value, nil, invalid("ambiguous_repositories", "use repository_ids or repository_id, not both")
	}
	value.RepositoryIDs = append([]string(nil), input.RepositoryIDs...)
	if len(value.RepositoryIDs) == 0 && strings.TrimSpace(input.RepositoryID) != "" {
		value.RepositoryIDs = []string{input.RepositoryID}
	}
	seen := make(map[string]struct{}, len(value.RepositoryIDs))
	for index, repositoryID := range value.RepositoryIDs {
		repositoryID = strings.TrimSpace(repositoryID)
		if repositoryID == "" {
			return value, nil, invalid("repository_required", "repository_ids cannot contain an empty value")
		}
		if _, duplicate := seen[repositoryID]; duplicate {
			return value, nil, invalid("duplicate_repository", "each repository may be selected only once")
		}
		seen[repositoryID] = struct{}{}
		value.RepositoryIDs[index] = repositoryID
	}
	if len(value.RepositoryIDs) == 0 {
		return value, nil, invalid("repository_required", "at least one repository_id is required")
	}
	if len(value.RepositoryIDs) > 200 {
		return value, nil, invalid("too_many_repositories", "a Run is limited to 200 repositories")
	}
	sort.Strings(value.RepositoryIDs)
	if value.ConcurrencyLimit == 0 {
		value.ConcurrencyLimit = defaultRunConcurrency
	}
	if value.ConcurrencyLimit < 1 || value.ConcurrencyLimit > 100 {
		return value, nil, invalid("invalid_concurrency_limit", "concurrency_limit must be between 1 and 100")
	}
	parameters, err := normalizeDefinitionInputs(value.Parameters)
	if err != nil {
		return value, nil, err
	}
	value.Parameters = parameters
	body, err := json.Marshal(value)
	if err != nil {
		return value, nil, unavailable(err)
	}
	digest := sha256.Sum256(body)
	return value, digest[:], nil
}

func legacyRunRequestDigest(value normalizedRunRequest) ([]byte, error) {
	if len(value.RepositoryIDs) != 1 || value.ConcurrencyLimit != defaultRunConcurrency {
		return nil, nil
	}
	body, err := json.Marshal(legacyNormalizedRunRequest{
		RequestKey: value.RequestKey, DefinitionID: value.DefinitionID,
		RepositoryID: value.RepositoryIDs[0], Parameters: value.Parameters,
	})
	if err != nil {
		return nil, unavailable(err)
	}
	digest := sha256.Sum256(body)
	return digest[:], nil
}

func (s *Store) CreateRun(ctx context.Context, input protocol.CreateRunRequest) (protocol.RunDetail, bool, error) {
	return s.createRun(ctx, input, "manual", nil, "", "")
}

func (s *Store) createScheduledRun(
	ctx context.Context,
	input protocol.CreateRunRequest,
	snapshot protocol.DefinitionSnapshot,
) (protocol.RunDetail, bool, error) {
	return s.createRun(ctx, input, "schedule", &snapshot, "", "")
}

func (s *Store) createWebhookRun(
	ctx context.Context,
	input protocol.CreateRunRequest,
	snapshot protocol.DefinitionSnapshot,
	resolvedPrompt string,
	occurrenceID string,
) (protocol.RunDetail, bool, error) {
	return s.createRun(ctx, input, "webhook", &snapshot, resolvedPrompt, occurrenceID)
}

func (s *Store) createRun(
	ctx context.Context,
	input protocol.CreateRunRequest,
	sourceKind string,
	frozenSnapshot *protocol.DefinitionSnapshot,
	resolvedPrompt string,
	webhookOccurrenceID string,
) (protocol.RunDetail, bool, error) {
	value, digest, err := normalizeRunRequest(input)
	if err != nil {
		return protocol.RunDetail{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	defer tx.Rollback()
	var existingID, existingSourceKind string
	var storedDigest []byte
	err = tx.QueryRowContext(ctx, `SELECT id, request_digest, source_kind FROM runs WHERE request_key = ?`, value.RequestKey).
		Scan(&existingID, &storedDigest, &existingSourceKind)
	if err == nil {
		if existingSourceKind != sourceKind {
			return protocol.RunDetail{}, false, conflict("request_key_conflict", "request_key was already used by a different Run source")
		}
		matches := bytes.Equal(storedDigest, digest)
		if !matches {
			legacyDigest, digestErr := legacyRunRequestDigest(value)
			if digestErr != nil {
				return protocol.RunDetail{}, false, digestErr
			}
			matches = legacyDigest != nil && bytes.Equal(storedDigest, legacyDigest)
		}
		if !matches {
			return protocol.RunDetail{}, false, conflict("request_key_conflict", "request_key was already used with different Run inputs")
		}
		if webhookOccurrenceID != "" {
			result, err := tx.ExecContext(ctx, `UPDATE automation_github_webhook_occurrences SET run_id = ? WHERE occurrence_id = ?`, existingID, webhookOccurrenceID)
			if err != nil {
				return protocol.RunDetail{}, false, unavailable(err)
			}
			if changed, _ := result.RowsAffected(); changed != 1 {
				return protocol.RunDetail{}, false, conflict("webhook_occurrence_missing", "webhook occurrence was not available for Run admission")
			}
		}
		if err := tx.Commit(); err != nil {
			return protocol.RunDetail{}, false, unavailable(err)
		}
		detail, err := s.Run(ctx, existingID)
		return detail, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.RunDetail{}, false, unavailable(err)
	}

	var snapshot protocol.DefinitionSnapshot
	if frozenSnapshot != nil {
		snapshot = *frozenSnapshot
		if snapshot.ID != value.DefinitionID {
			return protocol.RunDetail{}, false, conflict("request_key_conflict", "scheduled Definition snapshot does not match definition_id")
		}
	} else {
		definition, definitionErr := scanDefinition(tx.QueryRowContext(ctx, definitionSelect+` WHERE id = ?`, value.DefinitionID))
		if errors.Is(definitionErr, sql.ErrNoRows) {
			return protocol.RunDetail{}, false, invalid("definition_not_found", "Definition was not found")
		}
		if definitionErr != nil {
			return protocol.RunDetail{}, false, unavailable(definitionErr)
		}
		if definition.Archived {
			return protocol.RunDetail{}, false, conflict("definition_archived", "archived Definitions cannot start new Runs")
		}
		snapshot = definition.Snapshot()
	}
	parameters := make(map[string]string, len(snapshot.Inputs))
	for key, defaultValue := range snapshot.Inputs {
		parameters[key] = defaultValue
	}
	for key, parameter := range value.Parameters {
		if _, declared := snapshot.Inputs[key]; !declared {
			return protocol.RunDetail{}, false, invalid("unknown_run_parameter", "parameters must be declared by the selected Definition")
		}
		parameters[key] = parameter
	}
	if resolvedPrompt == "" {
		resolvedPrompt = snapshot.Prompt
	}

	type runTarget struct {
		id       string
		identity string
	}
	targets := make([]runTarget, 0, len(value.RepositoryIDs))
	for _, repositoryID := range value.RepositoryIDs {
		var repositoryIdentity string
		err = tx.QueryRowContext(ctx, `
			SELECT repository.remote_identity
			FROM repositories repository
			WHERE repository.id = ?
			  AND (
			      repository.enabled = 1
			      OR EXISTS (
			          SELECT 1 FROM worker_repositories available
			          WHERE available.repository_id = repository.id
			            AND available.advertised = 1
			            AND available.dynamic = 0
			      )
			  )
		`, repositoryID).Scan(&repositoryIdentity)
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.RunDetail{}, false, conflict("repository_not_available", "every repository must be configured on a Runner or enabled for managed acquisition")
		}
		if err != nil {
			return protocol.RunDetail{}, false, unavailable(err)
		}
		if !protocol.AgentPromptFits(snapshot.Name, repositoryIdentity, resolvedPrompt) {
			return protocol.RunDetail{}, false, invalid("agent_prompt_too_large", "the complete agent prompt exceeds 72 KiB")
		}
		targets = append(targets, runTarget{id: repositoryID, identity: repositoryIdentity})
	}

	runID, err := newID()
	if err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	parametersJSON, err := json.Marshal(parameters)
	if err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO runs(
			id, request_key, request_digest, source_kind, definition_id,
			definition_snapshot, parameters, concurrency_limit, admitted_at, updated_at, resolved_prompt
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, runID, value.RequestKey, digest, sourceKind, snapshot.ID, snapshotJSON, parametersJSON,
		value.ConcurrencyLimit, now, now, resolvedPrompt); err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	materialized := 0
	for _, target := range targets {
		jobID, err := newID()
		if err != nil {
			return protocol.RunDetail{}, false, unavailable(err)
		}
		jobState := "blocked"
		blockedReason := "Waiting for an available Run concurrency slot."
		var taskID, executionID string
		if materialized < value.ConcurrencyLimit {
			selection, routeErr := s.selectRunRoute(ctx, tx, target.id, target.identity, now, "", snapshot.Runtime, snapshot.AllowedTools)
			blockedReason = "Waiting for a healthy compatible Runner with repository access."
			if routeErr == nil {
				taskID, executionID, err = s.insertRunJobExecution(ctx, tx, runID, jobID, snapshot, snapshotJSON, resolvedPrompt, selection, now)
				if err != nil {
					return protocol.RunDetail{}, false, err
				}
				jobState = "queued"
				blockedReason = ""
				materialized++
			} else if !serviceErrorCode(routeErr, "no_eligible_worker") {
				return protocol.RunDetail{}, false, routeErr
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO jobs(
				id, run_id, repository_id, repository_identity, task_id, execution_id, state,
				blocked_reason, admitted_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, jobID, runID, target.id, target.identity, nullableString(taskID), nullableString(executionID),
			jobState, nullableString(blockedReason), now, now); err != nil {
			return protocol.RunDetail{}, false, unavailable(err)
		}
	}
	if webhookOccurrenceID != "" {
		result, err := tx.ExecContext(ctx, `UPDATE automation_github_webhook_occurrences SET run_id = ? WHERE occurrence_id = ?`, runID, webhookOccurrenceID)
		if err != nil {
			return protocol.RunDetail{}, false, unavailable(err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return protocol.RunDetail{}, false, conflict("webhook_occurrence_missing", "webhook occurrence was not available for Run admission")
		}
	}
	if err := tx.Commit(); err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	detail, err := s.Run(ctx, runID)
	return detail, true, err
}

func (s *Store) RunRepositories(ctx context.Context) ([]protocol.RunRepository, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT repository.id, repository.remote_identity
		FROM repositories repository
		WHERE repository.enabled = 1
		   OR EXISTS (
		       SELECT 1 FROM worker_repositories available
		       WHERE available.repository_id = repository.id
		         AND available.advertised = 1
		         AND available.dynamic = 0
		   )
		ORDER BY repository.remote_identity, repository.id
	`)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	result := make([]protocol.RunRepository, 0)
	for rows.Next() {
		var repository protocol.RunRepository
		if err := rows.Scan(&repository.ID, &repository.RemoteIdentity); err != nil {
			return nil, unavailable(err)
		}
		result = append(result, repository)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	return result, nil
}

func (s *Store) selectRunRoute(
	ctx context.Context,
	tx *sql.Tx,
	repositoryID string,
	repositoryIdentity string,
	now int64,
	workerID string,
	requiredRuntime string,
	requiredTools []string,
) (taskRouteCandidate, error) {
	var enabled int
	var currentIdentity string
	err := tx.QueryRowContext(ctx, `
		SELECT remote_identity, enabled FROM repositories WHERE id = ?
	`, repositoryID).Scan(&currentIdentity, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return taskRouteCandidate{}, conflict("repository_not_available", "repository is not configured on a Runner or enabled for managed acquisition")
	}
	if err != nil {
		return taskRouteCandidate{}, unavailable(err)
	}
	if repositoryIdentity == "" {
		repositoryIdentity = currentIdentity
	}
	route := protocol.TaskRoute{
		RepositoryRemoteIdentity: repositoryIdentity,
		SourceAccess:             protocol.SourceAccess{Provider: "local", Hostname: "localhost"},
	}
	requireSourceAccess := false
	if enabled != 0 && strings.HasPrefix(strings.ToLower(repositoryIdentity), "github.com/") {
		route.SourceAccess = protocol.SourceAccess{Provider: "github", Hostname: "github.com"}
		requireSourceAccess = true
	}
	if err := normalizeTaskRoute(&route); err != nil {
		return taskRouteCandidate{}, err
	}
	return s.selectTaskRouteWithSourceRequirement(
		ctx, tx, route, now, requireSourceAccess, true, repositoryID, workerID, requiredRuntime, requiredTools,
	)
}

func serviceErrorCode(err error, code string) bool {
	var serviceErr *ServiceError
	return errors.As(err, &serviceErr) && serviceErr.Code == code
}

func (s *Store) insertRunJobExecution(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	jobID string,
	snapshot protocol.DefinitionSnapshot,
	snapshotJSON []byte,
	resolvedPrompt string,
	selection taskRouteCandidate,
	now int64,
) (string, string, error) {
	taskID, err := newID()
	if err != nil {
		return "", "", unavailable(err)
	}
	executionID, err := newID()
	if err != nil {
		return "", "", unavailable(err)
	}
	requestKey := "run:" + runID + ":job:" + jobID
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tasks(
			id, request_key, title, description, repository_id, timeout_seconds,
			created_at, context, definition_id, definition_snapshot
		) VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?)
	`, taskID, requestKey, snapshot.Name, resolvedPrompt, selection.repositoryID,
		snapshot.TimeoutSeconds, now, snapshot.ID, snapshotJSON); err != nil {
		return "", "", unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO executions(
			id, task_id, assigned_worker_id, required_runtime, state, created_at, updated_at
		) VALUES (?, ?, ?, ?, 'queued', ?, ?)
	`, executionID, taskID, selection.workerID, snapshot.Runtime, now, now); err != nil {
		return "", "", unavailable(err)
	}
	return taskID, executionID, nil
}

func (s *Store) materializeBlockedJobForWorker(
	ctx context.Context,
	tx *sql.Tx,
	workerID string,
	now int64,
) error {
	const pageSize = 200
	type candidate struct {
		jobID          string
		runID          string
		repositoryID   string
		identity       string
		snapshotJSON   []byte
		resolvedPrompt string
		admittedAt     int64
	}
	var cursorAt int64
	var cursorID string
	for {
		query := `
			SELECT job.id, job.run_id, repository.id, job.repository_identity, run.definition_snapshot,
			       COALESCE(NULLIF(run.resolved_prompt, ''), json_extract(run.definition_snapshot, '$.prompt')),
			       job.admitted_at
			FROM jobs job
			JOIN runs run ON run.id = job.run_id
			JOIN repositories repository ON repository.id = job.repository_id
			WHERE job.state = 'blocked' AND job.task_id IS NULL
			  AND (
			      SELECT COUNT(*)
			      FROM jobs active_job
			      JOIN executions active_execution ON active_execution.id = active_job.execution_id
			      WHERE active_job.run_id = job.run_id
			        AND active_execution.state IN ('queued', 'preparing', 'running')
			  ) < run.concurrency_limit
		`
		args := make([]any, 0, 3)
		if cursorID != "" {
			query += ` AND (job.admitted_at > ? OR (job.admitted_at = ? AND job.id > ?))`
			args = append(args, cursorAt, cursorAt, cursorID)
		}
		query += ` ORDER BY job.admitted_at, job.id LIMIT 200`
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return unavailable(err)
		}
		var candidates []candidate
		for rows.Next() {
			var value candidate
			if err := rows.Scan(
				&value.jobID, &value.runID, &value.repositoryID, &value.identity,
				&value.snapshotJSON, &value.resolvedPrompt, &value.admittedAt,
			); err != nil {
				rows.Close()
				return unavailable(err)
			}
			candidates = append(candidates, value)
		}
		if err := rows.Close(); err != nil {
			return unavailable(err)
		}
		for _, value := range candidates {
			var snapshot protocol.DefinitionSnapshot
			if err := json.Unmarshal(value.snapshotJSON, &snapshot); err != nil {
				return unavailable(errors.New("stored Run Definition snapshot is invalid"))
			}
			selection, err := s.selectRunRoute(
				ctx, tx, value.repositoryID, value.identity, now, workerID, snapshot.Runtime, snapshot.AllowedTools,
			)
			if err != nil {
				if serviceErrorCode(err, "no_eligible_worker") || serviceErrorCode(err, "repository_not_managed") {
					continue
				}
				return err
			}
			taskID, executionID, err := s.insertRunJobExecution(
				ctx, tx, value.runID, value.jobID, snapshot, value.snapshotJSON, value.resolvedPrompt, selection, now,
			)
			if err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, `
				UPDATE jobs
				SET task_id = ?, execution_id = ?, state = 'queued', blocked_reason = NULL, updated_at = ?
				WHERE id = ? AND state = 'blocked' AND task_id IS NULL
			`, taskID, executionID, now, value.jobID)
			if err != nil {
				return unavailable(err)
			}
			changed, _ := result.RowsAffected()
			if changed != 1 {
				return conflict("job_route_conflict", "Job routing state changed before it could be assigned")
			}
			return nil
		}
		if len(candidates) < pageSize {
			return nil
		}
		last := candidates[len(candidates)-1]
		cursorAt, cursorID = last.admittedAt, last.jobID
	}
}

func (s *Store) rerouteQueuedRunJobForWorker(
	ctx context.Context,
	tx *sql.Tx,
	workerID string,
	now int64,
) error {
	const pageSize = 200
	type candidate struct {
		jobID          string
		executionID    string
		repositoryID   string
		identity       string
		assignedWorker string
		snapshotJSON   []byte
		admittedAt     int64
	}
	var cursorAt int64
	var cursorID string
	for {
		query := `
			SELECT job.id, job.execution_id, job.repository_id, job.repository_identity,
			       execution.assigned_worker_id, run.definition_snapshot, job.admitted_at
			FROM jobs job
			JOIN executions execution ON execution.id = job.execution_id
			JOIN runs run ON run.id = job.run_id
			WHERE execution.state = 'queued'
			  AND execution.assigned_worker_id != ?
		`
		args := []any{workerID}
		if cursorID != "" {
			query += ` AND (job.admitted_at > ? OR (job.admitted_at = ? AND job.id > ?))`
			args = append(args, cursorAt, cursorAt, cursorID)
		}
		query += ` ORDER BY job.admitted_at, job.id LIMIT 200`
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return unavailable(err)
		}
		var candidates []candidate
		for rows.Next() {
			var value candidate
			if err := rows.Scan(
				&value.jobID, &value.executionID, &value.repositoryID, &value.identity,
				&value.assignedWorker, &value.snapshotJSON, &value.admittedAt,
			); err != nil {
				rows.Close()
				return unavailable(err)
			}
			candidates = append(candidates, value)
		}
		if err := rows.Close(); err != nil {
			return unavailable(err)
		}
		for _, value := range candidates {
			var snapshot protocol.DefinitionSnapshot
			if err := json.Unmarshal(value.snapshotJSON, &snapshot); err != nil {
				return unavailable(errors.New("stored Run Definition snapshot is invalid"))
			}
			if _, err := s.selectRunRoute(
				ctx, tx, value.repositoryID, value.identity, now, value.assignedWorker, snapshot.Runtime, snapshot.AllowedTools,
			); err == nil {
				continue
			} else if !serviceErrorCode(err, "no_eligible_worker") && !serviceErrorCode(err, "repository_not_managed") {
				return err
			}
			selection, err := s.selectRunRoute(
				ctx, tx, value.repositoryID, value.identity, now, workerID, snapshot.Runtime, snapshot.AllowedTools,
			)
			if err != nil {
				if serviceErrorCode(err, "no_eligible_worker") || serviceErrorCode(err, "repository_not_managed") {
					continue
				}
				return err
			}
			result, err := tx.ExecContext(ctx, `
				UPDATE executions
				SET assigned_worker_id = ?, updated_at = ?
				WHERE id = ? AND state = 'queued' AND assigned_worker_id = ?
			`, selection.workerID, now, value.executionID, value.assignedWorker)
			if err != nil {
				return unavailable(err)
			}
			changed, _ := result.RowsAffected()
			if changed == 1 {
				if _, err := tx.ExecContext(ctx, `UPDATE jobs SET updated_at = ? WHERE id = ?`, now, value.jobID); err != nil {
					return unavailable(err)
				}
				return nil
			}
		}
		if len(candidates) < pageSize {
			return nil
		}
		last := candidates[len(candidates)-1]
		cursorAt, cursorID = last.admittedAt, last.jobID
	}
}

func (s *Store) Runs(ctx context.Context, request protocol.RunPageRequest) (protocol.RunPage, error) {
	if request.Limit < 1 || request.Limit > protocol.MaxRunPageSize {
		return protocol.RunPage{}, invalid("invalid_limit", "limit must be between 1 and 200")
	}
	query := `SELECT id, admitted_at FROM runs`
	args := make([]any, 0, 4)
	if request.Cursor != nil {
		query += ` WHERE (admitted_at < ? OR (admitted_at = ? AND id < ?))`
		args = append(args, request.Cursor.AdmittedAtMillis, request.Cursor.AdmittedAtMillis, request.Cursor.ID)
	}
	query += ` ORDER BY admitted_at DESC, id DESC LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return protocol.RunPage{}, unavailable(err)
	}
	type row struct {
		id         string
		admittedAt int64
	}
	var values []row
	for rows.Next() {
		var value row
		if err := rows.Scan(&value.id, &value.admittedAt); err != nil {
			rows.Close()
			return protocol.RunPage{}, unavailable(err)
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return protocol.RunPage{}, unavailable(err)
	}
	page := protocol.RunPage{Runs: make([]protocol.Run, 0, request.Limit)}
	if len(values) > request.Limit {
		values = values[:request.Limit]
		last := values[len(values)-1]
		page.NextCursor = &protocol.RunCursor{AdmittedAtMillis: last.admittedAt, ID: last.id}
	}
	for _, value := range values {
		detail, err := s.Run(ctx, value.id)
		if err != nil {
			return protocol.RunPage{}, err
		}
		page.Runs = append(page.Runs, detail.Run)
	}
	return page, nil
}

func (s *Store) Run(ctx context.Context, runID string) (protocol.RunDetail, error) {
	var detail protocol.RunDetail
	var snapshotJSON, parametersJSON []byte
	var admittedAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, request_key, source_kind, definition_snapshot, parameters,
		       concurrency_limit, admitted_at, updated_at
		FROM runs WHERE id = ?
	`, strings.TrimSpace(runID)).Scan(
		&detail.Run.ID, &detail.Run.RequestKey, &detail.Run.SourceKind, &snapshotJSON,
		&parametersJSON, &detail.Run.ConcurrencyLimit, &admittedAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return detail, ErrNotFound
	}
	if err != nil {
		return detail, unavailable(err)
	}
	if err := json.Unmarshal(snapshotJSON, &detail.Run.Definition); err != nil {
		return detail, unavailable(errors.New("stored Run Definition snapshot is invalid"))
	}
	if err := json.Unmarshal(parametersJSON, &detail.Parameters); err != nil {
		return detail, unavailable(errors.New("stored Run parameters are invalid"))
	}
	detail.Run.AdmittedAt = fromMillis(admittedAt)
	detail.Run.UpdatedAt = fromMillis(updatedAt)
	if detail.Run.SourceKind == "webhook" {
		err := s.db.QueryRowContext(ctx, `
			SELECT delivery_id, event, action, pull_request_number, pull_request_url, head_commit
			FROM automation_github_webhook_occurrences WHERE run_id = ?
		`, detail.Run.ID).Scan(&detail.Run.DeliveryID, &detail.Run.Event, &detail.Run.Action,
			&detail.Run.PullRequestNumber, &detail.Run.PullRequestURL, &detail.Run.ObservedHeadCommit)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return detail, unavailable(err)
		}
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id FROM jobs WHERE run_id = ? ORDER BY admitted_at, id`, detail.Run.ID)
	if err != nil {
		return detail, unavailable(err)
	}
	var jobIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return detail, unavailable(err)
		}
		jobIDs = append(jobIDs, id)
	}
	if err := rows.Close(); err != nil {
		return detail, unavailable(err)
	}
	for _, jobID := range jobIDs {
		job, err := s.Job(ctx, jobID)
		if err != nil {
			return detail, err
		}
		detail.Jobs = append(detail.Jobs, job)
		detail.Run.RepositoryRemoteIdentities = append(
			detail.Run.RepositoryRemoteIdentities, job.Job.RepositoryRemoteIdentity,
		)
		if job.Job.TerminalAt != nil && job.Job.TerminalAt.After(detail.Run.UpdatedAt) {
			detail.Run.UpdatedAt = *job.Job.TerminalAt
		}
	}
	detail.Run.JobCount = len(detail.Jobs)
	detail.Run.State = aggregateRunState(detail.Jobs)
	return detail, nil
}

func (s *Store) Job(ctx context.Context, jobID string) (protocol.JobDetail, error) {
	var detail protocol.JobDetail
	var taskID, executionID, blockedReason sql.NullString
	var admittedAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT job.id, job.run_id, job.repository_id,
		       CASE WHEN job.repository_identity = '' THEN repository.remote_identity ELSE job.repository_identity END,
		       job.task_id, job.execution_id, job.state, job.blocked_reason,
		       job.admitted_at, job.updated_at
		FROM jobs job
		JOIN repositories repository ON repository.id = job.repository_id
		WHERE job.id = ?
	`, strings.TrimSpace(jobID)).Scan(
		&detail.Job.ID, &detail.Job.RunID, &detail.Job.RepositoryID,
		&detail.Job.RepositoryRemoteIdentity, &taskID, &executionID, &detail.Job.State,
		&blockedReason, &admittedAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return detail, ErrNotFound
	}
	if err != nil {
		return detail, unavailable(err)
	}
	detail.Job.AdmittedAt = fromMillis(admittedAt)
	if detail.Job.State == "cancelled" && !taskID.Valid {
		terminal := fromMillis(updatedAt)
		detail.Job.TerminalAt = &terminal
	}
	if blockedReason.Valid {
		detail.Job.BlockedReason = blockedReason.String
	}
	var snapshotJSON []byte
	var resolvedPrompt string
	if err := s.db.QueryRowContext(ctx, `SELECT definition_snapshot, resolved_prompt FROM runs WHERE id = ?`, detail.Job.RunID).Scan(&snapshotJSON, &resolvedPrompt); err != nil {
		return detail, unavailable(err)
	}
	var snapshot protocol.DefinitionSnapshot
	if err := json.Unmarshal(snapshotJSON, &snapshot); err != nil {
		return detail, unavailable(errors.New("stored Run Definition snapshot is invalid"))
	}
	detail.Job.RequiredRuntime = snapshot.Runtime
	if resolvedPrompt == "" {
		resolvedPrompt = snapshot.Prompt
	}
	detail.ResolvedPrompt = resolvedPrompt
	if !taskID.Valid {
		return detail, nil
	}
	detail.Job.TaskID = taskID.String
	detail.Job.ExecutionID = executionID.String
	task, err := s.Task(ctx, taskID.String)
	if err != nil {
		return detail, err
	}
	detail.Job.AssignedWorkerID = task.Execution.AssignedWorkerID
	detail.Job.State = task.Execution.State
	detail.Attempts = task.Attempts
	for _, attempt := range task.Attempts {
		if attempt.StartedAt != nil {
			detail.Job.RetryMayRepeatEffects = true
			if detail.Job.StartedAt == nil || attempt.StartedAt.Before(*detail.Job.StartedAt) {
				started := *attempt.StartedAt
				detail.Job.StartedAt = &started
			}
		}
	}
	if len(task.Attempts) > 0 {
		latest := task.Attempts[len(task.Attempts)-1]
		detail.Job.Result = latest.Result
		detail.Job.FailureReason = latest.Error
		if isTerminalExecution(task.Execution.State) && latest.CompletedAt != nil {
			completed := *latest.CompletedAt
			detail.Job.TerminalAt = &completed
		}
	}
	if isTerminalExecution(task.Execution.State) && detail.Job.TerminalAt == nil {
		terminal := task.Execution.UpdatedAt
		detail.Job.TerminalAt = &terminal
	}
	return detail, nil
}

func isTerminalExecution(state string) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled"
}

func aggregateRunState(jobs []protocol.JobDetail) string {
	if len(jobs) == 0 {
		return "blocked"
	}
	states := make(map[string]int)
	for _, job := range jobs {
		states[job.Job.State]++
	}
	if states["preparing"] > 0 || states["running"] > 0 {
		return "running"
	}
	if states["queued"] > 0 {
		return "queued"
	}
	if states["blocked"] > 0 {
		return "blocked"
	}
	if states["failed"] > 0 {
		return "failed"
	}
	if states["cancelled"] > 0 {
		return "cancelled"
	}
	return "succeeded"
}

func (s *Store) CancelJob(ctx context.Context, jobID string) (protocol.RunDetail, error) {
	job, err := s.Job(ctx, jobID)
	if err != nil {
		return protocol.RunDetail{}, err
	}
	if job.Job.TaskID == "" {
		result, err := s.db.ExecContext(ctx, `
			UPDATE jobs SET state = 'cancelled', blocked_reason = NULL, updated_at = ?
			WHERE id = ? AND state = 'blocked'
		`, s.now().UnixMilli(), jobID)
		if err != nil {
			return protocol.RunDetail{}, unavailable(err)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return protocol.RunDetail{}, conflict("cancel_not_allowed", "only active Jobs can be cancelled")
		}
	} else if _, err := s.CancelTask(ctx, job.Job.TaskID); err != nil {
		return protocol.RunDetail{}, err
	}
	return s.Run(ctx, job.Job.RunID)
}

func (s *Store) RetryJob(ctx context.Context, jobID string) (protocol.RunDetail, error) {
	job, err := s.Job(ctx, jobID)
	if err != nil {
		return protocol.RunDetail{}, err
	}
	if job.Job.State != "failed" || job.Job.ExecutionID == "" {
		return protocol.RunDetail{}, conflict("retry_not_allowed", "only failed Jobs can be retried")
	}
	if _, err := s.RetryExecution(ctx, job.Job.ExecutionID); err != nil {
		return protocol.RunDetail{}, err
	}
	return s.Run(ctx, job.Job.RunID)
}
