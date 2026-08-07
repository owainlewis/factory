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
	value, digest, err := normalizeRunRequest(input)
	if err != nil {
		return protocol.RunDetail{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	defer tx.Rollback()
	var existingID string
	var storedDigest []byte
	err = tx.QueryRowContext(ctx, `SELECT id, request_digest FROM runs WHERE request_key = ?`, value.RequestKey).
		Scan(&existingID, &storedDigest)
	if err == nil {
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
		if err := tx.Commit(); err != nil {
			return protocol.RunDetail{}, false, unavailable(err)
		}
		detail, err := s.Run(ctx, existingID)
		return detail, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.RunDetail{}, false, unavailable(err)
	}

	definition, err := scanDefinition(tx.QueryRowContext(ctx, definitionSelect+` WHERE id = ?`, value.DefinitionID))
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.RunDetail{}, false, invalid("definition_not_found", "Definition was not found")
	}
	if err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	if definition.Archived {
		return protocol.RunDetail{}, false, conflict("definition_archived", "archived Definitions cannot start new Runs")
	}
	snapshot := definition.Snapshot()
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
	resolvedPrompt, err := protocol.ResolveDefinitionPrompt(snapshot.Prompt, parameters)
	if err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}

	type runTarget struct {
		id       string
		identity string
	}
	targets := make([]runTarget, 0, len(value.RepositoryIDs))
	for _, repositoryID := range value.RepositoryIDs {
		var repositoryIdentity string
		var repositoryEnabled, repositoryCentrallyManaged, repositoryAdvertised int
		err = tx.QueryRowContext(ctx, `
			SELECT repository.remote_identity, repository.enabled, repository.centrally_managed,
			       EXISTS (
			           SELECT 1 FROM worker_repositories available
			           WHERE available.repository_id = repository.id
			             AND available.advertised = 1
			             AND available.dynamic = 0
			       )
			FROM repositories repository
			WHERE repository.id = ?
		`, repositoryID).Scan(
			&repositoryIdentity, &repositoryEnabled, &repositoryCentrallyManaged, &repositoryAdvertised,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.RunDetail{}, false, conflict("repository_not_available", "every repository must be configured on a Runner or enabled for managed acquisition")
		}
		if err != nil {
			return protocol.RunDetail{}, false, unavailable(err)
		}
		if !runRepositoryAvailable(repositoryIdentity, repositoryEnabled, repositoryCentrallyManaged, repositoryAdvertised) {
			return protocol.RunDetail{}, false, conflict("repository_not_available", "every repository must be configured on a Runner or enabled for managed acquisition")
		}
		if repositoryCentrallyManaged != 0 {
			if canonical, normalizeErr := normalizeManagedGitHubRemote(repositoryIdentity); normalizeErr == nil {
				repositoryIdentity = canonical
			}
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
			definition_snapshot, parameters, concurrency_limit, admitted_at, updated_at
		) VALUES (?, ?, ?, 'manual', ?, ?, ?, ?, ?, ?)
	`, runID, value.RequestKey, digest, snapshot.ID, snapshotJSON, parametersJSON,
		value.ConcurrencyLimit, now, now); err != nil {
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
				taskID, executionID, err = s.insertRunJobExecution(
					ctx, tx, runID, jobID, snapshot, snapshotJSON, resolvedPrompt, selection, now,
				)
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
	if err := tx.Commit(); err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	detail, err := s.Run(ctx, runID)
	return detail, true, err
}

func (s *Store) RunRepositories(ctx context.Context) ([]protocol.RunRepository, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT repository.id, repository.remote_identity, repository.enabled, repository.centrally_managed,
		       EXISTS (
		           SELECT 1 FROM worker_repositories available
		           WHERE available.repository_id = repository.id
		             AND available.advertised = 1
		             AND available.dynamic = 0
		       )
		FROM repositories repository
		ORDER BY repository.remote_identity, repository.id
	`)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	result := make([]protocol.RunRepository, 0)
	for rows.Next() {
		var repository protocol.RunRepository
		var enabled, centrallyManaged, advertised int
		if err := rows.Scan(&repository.ID, &repository.RemoteIdentity, &enabled, &centrallyManaged, &advertised); err != nil {
			return nil, unavailable(err)
		}
		if !runRepositoryAvailable(repository.RemoteIdentity, enabled, centrallyManaged, advertised) {
			continue
		}
		result = append(result, repository)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	return result, nil
}

func runRepositoryAvailable(remoteIdentity string, enabled, centrallyManaged, advertised int) bool {
	if centrallyManaged != 0 && enabled == 0 {
		return false
	}
	_, githubErr := normalizeManagedGitHubRemote(remoteIdentity)
	if centrallyManaged != 0 && githubErr == nil {
		return true
	}
	return advertised != 0
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
	var currentIdentity string
	var enabled, centrallyManaged int
	err := tx.QueryRowContext(ctx, `
		SELECT remote_identity, enabled, centrally_managed FROM repositories WHERE id = ?
	`, repositoryID).Scan(&currentIdentity, &enabled, &centrallyManaged)
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
	if centrallyManaged != 0 && enabled == 0 {
		return taskRouteCandidate{}, conflict(
			"repository_not_managed", "repository is not enabled in the control-plane managed repository catalog",
		)
	}
	requireSourceAccess := false
	if _, githubErr := normalizeManagedGitHubRemote(repositoryIdentity); centrallyManaged != 0 && githubErr == nil {
		route.SourceAccess = protocol.SourceAccess{Provider: "github", Hostname: "github.com"}
		requireSourceAccess = true
	}
	if err := normalizeTaskRoute(&route); err != nil {
		return taskRouteCandidate{}, err
	}
	return s.selectTaskRouteWithSourceRequirement(
		ctx, tx, route, repositoryID, now, requireSourceAccess, true, workerID, requiredRuntime, requiredTools,
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
	const scanLimit = 50
	type candidate struct {
		jobID          string
		runID          string
		repositoryID   string
		identity       string
		snapshotJSON   []byte
		parametersJSON []byte
		admittedAt     int64
	}
	loadCandidates := func(cursor runJobScanCursor) ([]candidate, error) {
		query := `
			SELECT job.id, job.run_id, repository.id, job.repository_identity,
			       run.definition_snapshot, run.parameters, job.admitted_at
			FROM jobs job
			JOIN runs run ON run.id = job.run_id
			JOIN repositories repository ON repository.id = job.repository_id
			WHERE job.state = 'blocked' AND job.task_id IS NULL
			  AND (
			      SELECT COUNT(*)
			      FROM jobs active_job
			      JOIN executions active_execution ON active_execution.id = active_job.execution_id
			      WHERE active_job.run_id = job.run_id
			        AND active_execution.state IN ('preparing', 'running')
			  ) < run.concurrency_limit
		`
		args := make([]any, 0, 4)
		if cursor.jobID != "" {
			query += ` AND (job.admitted_at > ? OR (job.admitted_at = ? AND job.id > ?))`
			args = append(args, cursor.admittedAt, cursor.admittedAt, cursor.jobID)
		}
		query += ` ORDER BY job.admitted_at, job.id LIMIT ?`
		args = append(args, scanLimit)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, unavailable(err)
		}
		var candidates []candidate
		for rows.Next() {
			var value candidate
			if err := rows.Scan(
				&value.jobID, &value.runID, &value.repositoryID, &value.identity,
				&value.snapshotJSON, &value.parametersJSON, &value.admittedAt,
			); err != nil {
				rows.Close()
				return nil, unavailable(err)
			}
			candidates = append(candidates, value)
		}
		if err := rows.Close(); err != nil {
			return nil, unavailable(err)
		}
		return candidates, nil
	}
	cursor := s.runJobScanCursor(blockedRunJobScan, workerID)
	candidates, err := loadCandidates(cursor)
	if err != nil {
		return err
	}
	if len(candidates) == 0 && cursor.jobID != "" {
		cursor = runJobScanCursor{}
		s.setRunJobScanCursor(blockedRunJobScan, workerID, cursor)
		candidates, err = loadCandidates(cursor)
		if err != nil {
			return err
		}
	}
	for _, value := range candidates {
		s.setRunJobScanCursor(blockedRunJobScan, workerID, runJobScanCursor{
			admittedAt: value.admittedAt,
			jobID:      value.jobID,
		})
		var snapshot protocol.DefinitionSnapshot
		if err := json.Unmarshal(value.snapshotJSON, &snapshot); err != nil {
			return unavailable(errors.New("stored Run Definition snapshot is invalid"))
		}
		var parameters map[string]string
		if err := json.Unmarshal(value.parametersJSON, &parameters); err != nil {
			return unavailable(errors.New("stored Run parameters are invalid"))
		}
		resolvedPrompt, err := protocol.ResolveDefinitionPrompt(snapshot.Prompt, parameters)
		if err != nil {
			return unavailable(err)
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
			ctx, tx, value.runID, value.jobID, snapshot, value.snapshotJSON, resolvedPrompt, selection, now,
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
	if len(candidates) < scanLimit {
		s.setRunJobScanCursor(blockedRunJobScan, workerID, runJobScanCursor{})
	}
	return nil
}

func (s *Store) rerouteQueuedRunJobForWorker(
	ctx context.Context,
	tx *sql.Tx,
	workerID string,
	now int64,
) error {
	const scanLimit = 50
	type candidate struct {
		jobID          string
		executionID    string
		repositoryID   string
		identity       string
		assignedWorker string
		snapshotJSON   []byte
		admittedAt     int64
	}
	loadCandidates := func(cursor runJobScanCursor) ([]candidate, error) {
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
		if cursor.jobID != "" {
			query += ` AND (job.admitted_at > ? OR (job.admitted_at = ? AND job.id > ?))`
			args = append(args, cursor.admittedAt, cursor.admittedAt, cursor.jobID)
		}
		query += ` ORDER BY job.admitted_at, job.id LIMIT ?`
		args = append(args, scanLimit)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, unavailable(err)
		}
		var candidates []candidate
		for rows.Next() {
			var value candidate
			if err := rows.Scan(
				&value.jobID, &value.executionID, &value.repositoryID, &value.identity,
				&value.assignedWorker, &value.snapshotJSON, &value.admittedAt,
			); err != nil {
				rows.Close()
				return nil, unavailable(err)
			}
			candidates = append(candidates, value)
		}
		if err := rows.Close(); err != nil {
			return nil, unavailable(err)
		}
		return candidates, nil
	}
	cursor := s.runJobScanCursor(queuedRunJobScan, workerID)
	candidates, err := loadCandidates(cursor)
	if err != nil {
		return err
	}
	if len(candidates) == 0 && cursor.jobID != "" {
		cursor = runJobScanCursor{}
		s.setRunJobScanCursor(queuedRunJobScan, workerID, cursor)
		candidates, err = loadCandidates(cursor)
		if err != nil {
			return err
		}
	}
	for _, value := range candidates {
		s.setRunJobScanCursor(queuedRunJobScan, workerID, runJobScanCursor{
			admittedAt: value.admittedAt,
			jobID:      value.jobID,
		})
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
	if len(candidates) < scanLimit {
		s.setRunJobScanCursor(queuedRunJobScan, workerID, runJobScanCursor{})
	}
	return nil
}

type runJobScanKind string

const (
	blockedRunJobScan runJobScanKind = "blocked"
	queuedRunJobScan  runJobScanKind = "queued"
)

type runJobScanCursorKey struct {
	kind     runJobScanKind
	workerID string
}

type runJobScanCursor struct {
	admittedAt int64
	jobID      string
}

func (s *Store) runJobScanCursor(kind runJobScanKind, workerID string) runJobScanCursor {
	s.runJobScanCursorMu.Lock()
	defer s.runJobScanCursorMu.Unlock()
	return s.runJobScanCursors[runJobScanCursorKey{kind: kind, workerID: workerID}]
}

func (s *Store) setRunJobScanCursor(kind runJobScanKind, workerID string, cursor runJobScanCursor) {
	s.runJobScanCursorMu.Lock()
	defer s.runJobScanCursorMu.Unlock()
	key := runJobScanCursorKey{kind: kind, workerID: workerID}
	if cursor.jobID == "" {
		delete(s.runJobScanCursors, key)
		return
	}
	if s.runJobScanCursors == nil {
		s.runJobScanCursors = make(map[runJobScanCursorKey]runJobScanCursor)
	}
	s.runJobScanCursors[key] = cursor
}

func (s *Store) Runs(ctx context.Context, request protocol.RunPageRequest) (protocol.RunPage, error) {
	if request.Limit < 1 || request.Limit > protocol.MaxRunPageSize {
		return protocol.RunPage{}, invalid("invalid_limit", "limit must be between 1 and 200")
	}
	query := `
		WITH page AS (
			SELECT id, request_key, source_kind, definition_snapshot, concurrency_limit, admitted_at, updated_at
			FROM runs`
	args := make([]any, 0, 4)
	if request.Cursor != nil {
		query += ` WHERE (admitted_at < ? OR (admitted_at = ? AND id < ?))`
		args = append(args, request.Cursor.AdmittedAtMillis, request.Cursor.AdmittedAtMillis, request.Cursor.ID)
	}
	query += `
			ORDER BY admitted_at DESC, id DESC
			LIMIT ?
		)
		SELECT page.id, page.request_key, page.source_kind, page.definition_snapshot,
		       page.concurrency_limit, page.admitted_at, page.updated_at, job.id,
		       CASE WHEN job.repository_identity = '' THEN repository.remote_identity ELSE job.repository_identity END,
		       CASE WHEN execution.id IS NULL THEN job.state ELSE execution.state END,
		       CASE
		           WHEN execution.id IS NULL AND job.state = 'cancelled' THEN job.updated_at
		           WHEN execution.state IN ('succeeded', 'failed', 'cancelled') THEN execution.updated_at
		       END
		FROM page
		LEFT JOIN jobs job ON job.run_id = page.id
		LEFT JOIN repositories repository ON repository.id = job.repository_id
		LEFT JOIN executions execution ON execution.id = job.execution_id
		ORDER BY page.admitted_at DESC, page.id DESC, job.admitted_at, job.id`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return protocol.RunPage{}, unavailable(err)
	}
	defer rows.Close()
	page := protocol.RunPage{Runs: make([]protocol.Run, 0, request.Limit+1)}
	indexes := make(map[string]int, request.Limit+1)
	states := make(map[string][]protocol.JobDetail, request.Limit+1)
	for rows.Next() {
		var run protocol.Run
		var snapshotJSON []byte
		var admittedAt, updatedAt int64
		var jobID, repositoryIdentity, state sql.NullString
		var terminalAt sql.NullInt64
		if err := rows.Scan(
			&run.ID, &run.RequestKey, &run.SourceKind, &snapshotJSON, &run.ConcurrencyLimit,
			&admittedAt, &updatedAt, &jobID, &repositoryIdentity, &state, &terminalAt,
		); err != nil {
			return protocol.RunPage{}, unavailable(err)
		}
		index, exists := indexes[run.ID]
		if !exists {
			if err := json.Unmarshal(snapshotJSON, &run.Definition); err != nil {
				return protocol.RunPage{}, unavailable(errors.New("stored Run Definition snapshot is invalid"))
			}
			run.AdmittedAt = fromMillis(admittedAt)
			run.UpdatedAt = fromMillis(updatedAt)
			index = len(page.Runs)
			indexes[run.ID] = index
			page.Runs = append(page.Runs, run)
		}
		if jobID.Valid {
			page.Runs[index].JobCount++
			page.Runs[index].RepositoryRemoteIdentities = append(
				page.Runs[index].RepositoryRemoteIdentities, repositoryIdentity.String,
			)
			states[run.ID] = append(states[run.ID], protocol.JobDetail{Job: protocol.Job{State: state.String}})
			if terminalAt.Valid {
				terminal := fromMillis(terminalAt.Int64)
				if terminal.After(page.Runs[index].UpdatedAt) {
					page.Runs[index].UpdatedAt = terminal
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return protocol.RunPage{}, unavailable(err)
	}
	for index := range page.Runs {
		page.Runs[index].State = aggregateRunState(states[page.Runs[index].ID])
	}
	if len(page.Runs) > request.Limit {
		last := page.Runs[request.Limit-1]
		page.NextCursor = &protocol.RunCursor{AdmittedAtMillis: last.AdmittedAt.UnixMilli(), ID: last.ID}
		page.Runs = page.Runs[:request.Limit]
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

	resolvedPrompt, err := protocol.ResolveDefinitionPrompt(detail.Run.Definition.Prompt, detail.Parameters)
	if err != nil {
		return detail, unavailable(err)
	}
	detail.Jobs, err = s.runJobs(ctx, detail.Run.ID, detail.Run.Definition.Runtime, resolvedPrompt)
	if err != nil {
		return detail, err
	}
	for _, job := range detail.Jobs {
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

func (s *Store) runJobs(
	ctx context.Context,
	runID string,
	requiredRuntime string,
	resolvedPrompt string,
) ([]protocol.JobDetail, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT job.id, job.run_id, job.repository_id,
		       CASE WHEN job.repository_identity = '' THEN repository.remote_identity ELSE job.repository_identity END,
		       job.task_id, job.execution_id, job.state, job.blocked_reason,
		       job.admitted_at, job.updated_at, task.description,
		       execution.assigned_worker_id, execution.required_runtime, execution.state,
		       execution.cancellation_requested, execution.updated_at
		FROM jobs job
		JOIN repositories repository ON repository.id = job.repository_id
		LEFT JOIN tasks task ON task.id = job.task_id
		LEFT JOIN executions execution ON execution.id = job.execution_id
		WHERE job.run_id = ?
		ORDER BY job.admitted_at, job.id
	`, runID)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()

	details := make([]protocol.JobDetail, 0)
	executionIndexes := make(map[string]int)
	executionUpdatedAt := make(map[string]int64)
	for rows.Next() {
		var detail protocol.JobDetail
		var taskID, executionID, blockedReason, taskPrompt sql.NullString
		var assignedWorkerID, executionRuntime, executionState sql.NullString
		var admittedAt, jobUpdatedAt int64
		var cancellationRequested, executionUpdated sql.NullInt64
		if err := rows.Scan(
			&detail.Job.ID, &detail.Job.RunID, &detail.Job.RepositoryID,
			&detail.Job.RepositoryRemoteIdentity, &taskID, &executionID, &detail.Job.State,
			&blockedReason, &admittedAt, &jobUpdatedAt, &taskPrompt, &assignedWorkerID,
			&executionRuntime, &executionState, &cancellationRequested, &executionUpdated,
		); err != nil {
			return nil, unavailable(err)
		}
		detail.Job.RequiredRuntime = requiredRuntime
		detail.Job.AdmittedAt = fromMillis(admittedAt)
		detail.ResolvedPrompt = resolvedPrompt
		if blockedReason.Valid {
			detail.Job.BlockedReason = blockedReason.String
		}
		if !taskID.Valid {
			if detail.Job.State == "cancelled" {
				terminal := fromMillis(jobUpdatedAt)
				detail.Job.TerminalAt = &terminal
			}
			details = append(details, detail)
			continue
		}
		if !executionID.Valid || !assignedWorkerID.Valid || !executionRuntime.Valid ||
			!executionState.Valid || !cancellationRequested.Valid || !executionUpdated.Valid || !taskPrompt.Valid {
			return nil, unavailable(errors.New("stored Run Job execution is incomplete"))
		}
		detail.Job.TaskID = taskID.String
		detail.Job.ExecutionID = executionID.String
		detail.Job.AssignedWorkerID = assignedWorkerID.String
		detail.Job.RequiredRuntime = executionRuntime.String
		detail.Job.State = executionState.String
		detail.Job.CancellationRequested = cancellationRequested.Int64 != 0
		detail.ResolvedPrompt = taskPrompt.String
		executionIndexes[executionID.String] = len(details)
		executionUpdatedAt[executionID.String] = executionUpdated.Int64
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	if err := rows.Close(); err != nil {
		return nil, unavailable(err)
	}

	attemptRows, err := s.db.QueryContext(ctx, `
		SELECT attempt.id, attempt.execution_id, attempt.worker_id, attempt.attempt_number,
		       attempt.state, attempt.lease_expires_at, attempt.supervisor_pid,
		       attempt.process_identity, attempt.process_group_id, attempt.result, attempt.error,
		       attempt.started_at, attempt.completed_at, attempt.created_at
		FROM attempts attempt
		JOIN executions execution ON execution.id = attempt.execution_id
		JOIN jobs job ON job.execution_id = execution.id
		WHERE job.run_id = ?
		ORDER BY job.admitted_at, job.id, attempt.attempt_number
	`, runID)
	if err != nil {
		return nil, unavailable(err)
	}
	defer attemptRows.Close()
	for attemptRows.Next() {
		attempt, err := scanAttempt(attemptRows)
		if err != nil {
			return nil, unavailable(err)
		}
		index, exists := executionIndexes[attempt.ExecutionID]
		if !exists {
			return nil, unavailable(errors.New("stored Run attempt has no Job execution"))
		}
		details[index].Attempts = append(details[index].Attempts, attempt)
	}
	if err := attemptRows.Err(); err != nil {
		return nil, unavailable(err)
	}

	for index := range details {
		detail := &details[index]
		for _, attempt := range detail.Attempts {
			if attempt.StartedAt != nil {
				detail.Job.RetryMayRepeatEffects = true
				if detail.Job.StartedAt == nil || attempt.StartedAt.Before(*detail.Job.StartedAt) {
					started := *attempt.StartedAt
					detail.Job.StartedAt = &started
				}
			}
		}
		if !isTerminalExecution(detail.Job.State) {
			continue
		}
		executionUpdated := fromMillis(executionUpdatedAt[detail.Job.ExecutionID])
		if len(detail.Attempts) > 0 {
			latest := detail.Attempts[len(detail.Attempts)-1]
			if latest.CompletedAt != nil && latest.CompletedAt.Equal(executionUpdated) &&
				attemptProducedExecutionState(latest.State, detail.Job.State) {
				detail.Job.Result = latest.Result
				detail.Job.FailureReason = latest.Error
				completed := *latest.CompletedAt
				detail.Job.TerminalAt = &completed
			}
		}
		if detail.Job.TerminalAt == nil {
			detail.Job.TerminalAt = &executionUpdated
		}
	}
	return details, nil
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
	var snapshotJSON, parametersJSON []byte
	if err := s.db.QueryRowContext(ctx, `
		SELECT definition_snapshot, parameters FROM runs WHERE id = ?
	`, detail.Job.RunID).Scan(&snapshotJSON, &parametersJSON); err != nil {
		return detail, unavailable(err)
	}
	var snapshot protocol.DefinitionSnapshot
	if err := json.Unmarshal(snapshotJSON, &snapshot); err != nil {
		return detail, unavailable(errors.New("stored Run Definition snapshot is invalid"))
	}
	var parameters map[string]string
	if err := json.Unmarshal(parametersJSON, &parameters); err != nil {
		return detail, unavailable(errors.New("stored Run parameters are invalid"))
	}
	detail.Job.RequiredRuntime = snapshot.Runtime
	detail.ResolvedPrompt, err = protocol.ResolveDefinitionPrompt(snapshot.Prompt, parameters)
	if err != nil {
		return detail, unavailable(err)
	}
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
	detail.Job.CancellationRequested = task.Execution.CancellationRequested
	detail.ResolvedPrompt = task.ResolvedPrompt
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
		if isTerminalExecution(task.Execution.State) && latest.CompletedAt != nil &&
			latest.CompletedAt.Equal(task.Execution.UpdatedAt) &&
			attemptProducedExecutionState(latest.State, task.Execution.State) {
			detail.Job.Result = latest.Result
			detail.Job.FailureReason = latest.Error
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

func attemptProducedExecutionState(attemptState, executionState string) bool {
	return attemptState == executionState || (attemptState == "lost" && executionState == "failed")
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	var executionID, runID string
	err = tx.QueryRowContext(ctx, `
		UPDATE executions
		SET state = CASE WHEN state = 'queued' THEN 'cancelled' ELSE state END,
		    cancellation_requested = CASE
		        WHEN state IN ('preparing', 'running') THEN 1
		        ELSE cancellation_requested
		    END,
		    updated_at = ?
		WHERE id = (SELECT execution_id FROM jobs WHERE id = ?)
		  AND (
		      state = 'queued'
		      OR (state IN ('preparing', 'running') AND cancellation_requested = 0)
		  )
		RETURNING id
	`, now, jobID).Scan(&executionID)
	if err == nil {
		if err := tx.QueryRowContext(ctx, `SELECT run_id FROM jobs WHERE execution_id = ?`, executionID).Scan(&runID); err != nil {
			return protocol.RunDetail{}, unavailable(err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return protocol.RunDetail{}, unavailable(err)
	} else {
		err = tx.QueryRowContext(ctx, `
			UPDATE jobs
			SET state = 'cancelled', blocked_reason = NULL, updated_at = ?
			WHERE id = ? AND state = 'blocked' AND task_id IS NULL
			RETURNING run_id
		`, now, jobID).Scan(&runID)
		if errors.Is(err, sql.ErrNoRows) {
			var exists int
			if lookupErr := tx.QueryRowContext(ctx, `SELECT 1 FROM jobs WHERE id = ?`, jobID).Scan(&exists); errors.Is(lookupErr, sql.ErrNoRows) {
				return protocol.RunDetail{}, ErrNotFound
			} else if lookupErr != nil {
				return protocol.RunDetail{}, unavailable(lookupErr)
			}
			return protocol.RunDetail{}, conflict("cancel_not_allowed", "only active Jobs can be cancelled")
		}
		if err != nil {
			return protocol.RunDetail{}, unavailable(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	return s.Run(ctx, runID)
}

func (s *Store) RetryJob(ctx context.Context, jobID string) (protocol.RunDetail, error) {
	job, err := s.Job(ctx, jobID)
	if err != nil {
		return protocol.RunDetail{}, err
	}
	if job.Job.State != "failed" || job.Job.ExecutionID == "" {
		return protocol.RunDetail{}, conflict("retry_not_allowed", "only failed Jobs can be retried")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	defer tx.Rollback()
	var state, assignedWorkerID string
	var concurrencyLimit int
	var snapshotJSON []byte
	err = tx.QueryRowContext(ctx, `
		SELECT execution.state, execution.assigned_worker_id, run.definition_snapshot, run.concurrency_limit
		FROM jobs job
		JOIN executions execution ON execution.id = job.execution_id
		JOIN runs run ON run.id = job.run_id
		WHERE job.id = ? AND execution.id = ?
	`, job.Job.ID, job.Job.ExecutionID).Scan(&state, &assignedWorkerID, &snapshotJSON, &concurrencyLimit)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.RunDetail{}, ErrNotFound
	}
	if err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if state != "failed" {
		return protocol.RunDetail{}, conflict("retry_not_allowed", "only failed Jobs can be retried")
	}
	var activeJobs int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM jobs job
		JOIN executions execution ON execution.id = job.execution_id
		WHERE job.run_id = ?
		  AND execution.state IN ('preparing', 'running')
	`, job.Job.RunID).Scan(&activeJobs); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if activeJobs >= concurrencyLimit {
		return protocol.RunDetail{}, conflict(
			"run_concurrency_full",
			"retry this Job after another active Job in the Run reaches a terminal state",
		)
	}
	var snapshot protocol.DefinitionSnapshot
	if err := json.Unmarshal(snapshotJSON, &snapshot); err != nil {
		return protocol.RunDetail{}, unavailable(errors.New("stored Run Definition snapshot is invalid"))
	}
	now := s.now().UnixMilli()
	selection, routeErr := s.selectRunRoute(
		ctx, tx, job.Job.RepositoryID, job.Job.RepositoryRemoteIdentity, now, "", snapshot.Runtime, snapshot.AllowedTools,
	)
	if routeErr == nil {
		assignedWorkerID = selection.workerID
	} else if !serviceErrorCode(routeErr, "no_eligible_worker") {
		return protocol.RunDetail{}, routeErr
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE executions
		SET assigned_worker_id = ?, state = 'queued', cancellation_requested = 0,
		    retry_count = retry_count + 1, updated_at = ?
		WHERE id = ? AND state = 'failed'
	`, assignedWorkerID, now, job.Job.ExecutionID)
	if err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return protocol.RunDetail{}, conflict("retry_conflict", "Job state changed before it could be retried")
	}
	if err := tx.Commit(); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	return s.Run(ctx, job.Job.RunID)
}
