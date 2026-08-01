package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

type normalizedWorkflowRevision struct {
	RequestKey         string `json:"request_key"`
	WorkflowID         string `json:"workflow_id,omitempty"`
	ExpectedRevisionID string `json:"expected_revision_id,omitempty"`
	Name               string `json:"name"`
	Summary            string `json:"summary"`
	Instructions       string `json:"instructions"`
}

func normalizeWorkflowRevision(
	requestKey, workflowID, expectedRevisionID, name, summary, instructions string,
) (normalizedWorkflowRevision, string, error) {
	value := normalizedWorkflowRevision{
		RequestKey: strings.TrimSpace(requestKey), WorkflowID: strings.TrimSpace(workflowID),
		ExpectedRevisionID: strings.TrimSpace(expectedRevisionID), Name: strings.TrimSpace(name),
		Summary: strings.TrimSpace(summary), Instructions: instructions,
	}
	if value.RequestKey == "" || len(value.RequestKey) > 200 {
		return value, "", invalid("invalid_request_key", "request_key is required and limited to 200 bytes")
	}
	if value.Name == "" || utf8.RuneCountInString(value.Name) > 100 {
		return value, "", invalid("invalid_workflow_name", "name is required and limited to 100 Unicode characters")
	}
	if utf8.RuneCountInString(value.Summary) > 500 {
		return value, "", invalid("invalid_workflow_summary", "summary is limited to 500 Unicode characters")
	}
	if strings.TrimSpace(value.Instructions) == "" || len([]byte(value.Instructions)) > protocol.MaxWorkflowInstructionsBytes {
		return value, "", invalid("invalid_workflow_instructions", "instructions are required and limited to 48 KiB")
	}
	return value, normalizeWorkflowName(value.Name), nil
}

func normalizeWorkflowName(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	builder.Grow(len(value))
	for _, character := range value {
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		builder.WriteRune(character)
	}
	return builder.String()
}

func workflowMutationDigest(value normalizedWorkflowRevision) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	return digest[:], nil
}

func (s *Store) CreateWorkflow(
	ctx context.Context,
	input protocol.CreateWorkflowRequest,
) (protocol.WorkflowDetail, bool, error) {
	value, nameKey, err := normalizeWorkflowRevision(
		input.RequestKey, "", "", input.Name, input.Summary, input.Instructions,
	)
	if err != nil {
		return protocol.WorkflowDetail{}, false, err
	}
	digest, err := workflowMutationDigest(value)
	if err != nil {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	defer tx.Rollback()
	workflowID, replay, err := workflowMutationReplay(ctx, tx, value.RequestKey, digest)
	if err != nil {
		return protocol.WorkflowDetail{}, false, err
	}
	if replay {
		if err := tx.Commit(); err != nil {
			return protocol.WorkflowDetail{}, false, unavailable(err)
		}
		detail, err := s.Workflow(ctx, workflowID)
		return detail, false, err
	}
	var conflictingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM workflows WHERE current_name_key = ?`, nameKey).Scan(&conflictingID)
	if err == nil {
		return protocol.WorkflowDetail{}, false, conflict("workflow_name_conflict", "a workflow with this normalized name already exists")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	var workflowCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows`).Scan(&workflowCount); err != nil {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	if workflowCount >= protocol.MaxWorkflows {
		return protocol.WorkflowDetail{}, false, conflict(
			"workflow_limit_reached",
			"the Workflow limit has been reached",
		)
	}
	workflowID, err = newID()
	if err != nil {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	revisionID, err := newID()
	if err != nil {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workflows(id, enabled, current_revision_id, current_name_key, created_at, updated_at)
		VALUES (?, 1, ?, ?, ?, ?)
	`, workflowID, revisionID, nameKey, now, now); err != nil {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workflow_revisions(
			id, workflow_id, revision_number, request_key, request_digest,
			name, summary, instructions, created_at
		) VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?)
	`, revisionID, workflowID, value.RequestKey, digest, value.Name, value.Summary, value.Instructions, now); err != nil {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	detail, err := s.Workflow(ctx, workflowID)
	return detail, true, err
}

func (s *Store) CreateWorkflowRevision(
	ctx context.Context,
	workflowID string,
	input protocol.CreateWorkflowRevisionRequest,
) (protocol.WorkflowDetail, bool, error) {
	value, nameKey, err := normalizeWorkflowRevision(
		input.RequestKey, workflowID, input.ExpectedRevisionID,
		input.Name, input.Summary, input.Instructions,
	)
	if err != nil {
		return protocol.WorkflowDetail{}, false, err
	}
	if value.WorkflowID == "" {
		return protocol.WorkflowDetail{}, false, ErrNotFound
	}
	if value.ExpectedRevisionID == "" {
		return protocol.WorkflowDetail{}, false, invalid("invalid_expected_revision", "expected_revision_id is required")
	}
	digest, err := workflowMutationDigest(value)
	if err != nil {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	defer tx.Rollback()
	replayWorkflowID, replay, err := workflowMutationReplay(ctx, tx, value.RequestKey, digest)
	if err != nil {
		return protocol.WorkflowDetail{}, false, err
	}
	if replay {
		if err := tx.Commit(); err != nil {
			return protocol.WorkflowDetail{}, false, unavailable(err)
		}
		detail, err := s.Workflow(ctx, replayWorkflowID)
		return detail, false, err
	}
	var currentRevisionID string
	var currentRevisionNumber int
	err = tx.QueryRowContext(ctx, `
		SELECT workflow.current_revision_id, revision.revision_number
		FROM workflows workflow
		JOIN workflow_revisions revision ON revision.id = workflow.current_revision_id
		WHERE workflow.id = ?
	`, value.WorkflowID).Scan(&currentRevisionID, &currentRevisionNumber)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkflowDetail{}, false, ErrNotFound
	}
	if err != nil {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	if currentRevisionID != value.ExpectedRevisionID {
		return protocol.WorkflowDetail{}, false, conflict("workflow_revision_conflict", "the workflow has a newer current revision")
	}
	if currentRevisionNumber >= protocol.MaxWorkflowRevisions {
		return protocol.WorkflowDetail{}, false, conflict("workflow_revision_limit", "the workflow revision limit has been reached")
	}
	var conflictingID string
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM workflows WHERE current_name_key = ? AND id != ?
	`, nameKey, value.WorkflowID).Scan(&conflictingID)
	if err == nil {
		return protocol.WorkflowDetail{}, false, conflict("workflow_name_conflict", "a workflow with this normalized name already exists")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	revisionID, err := newID()
	if err != nil {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workflow_revisions(
			id, workflow_id, revision_number, request_key, request_digest,
			name, summary, instructions, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, revisionID, value.WorkflowID, currentRevisionNumber+1, value.RequestKey, digest,
		value.Name, value.Summary, value.Instructions, now); err != nil {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE workflows
		SET current_revision_id = ?, current_name_key = ?, updated_at = ?
		WHERE id = ? AND current_revision_id = ?
	`, revisionID, nameKey, now, value.WorkflowID, value.ExpectedRevisionID)
	if err != nil {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	if changed != 1 {
		return protocol.WorkflowDetail{}, false, conflict("workflow_revision_conflict", "the workflow has a newer current revision")
	}
	if err := tx.Commit(); err != nil {
		return protocol.WorkflowDetail{}, false, unavailable(err)
	}
	detail, err := s.Workflow(ctx, value.WorkflowID)
	return detail, true, err
}

func workflowMutationReplay(
	ctx context.Context,
	tx *sql.Tx,
	requestKey string,
	digest []byte,
) (workflowID string, replay bool, resultErr error) {
	var storedDigest []byte
	err := tx.QueryRowContext(ctx, `
		SELECT workflow_id, request_digest FROM workflow_revisions WHERE request_key = ?
	`, requestKey).Scan(&workflowID, &storedDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, unavailable(err)
	}
	if !bytes.Equal(storedDigest, digest) {
		return "", false, conflict("request_key_conflict", "request_key was already used for a different workflow mutation")
	}
	return workflowID, true, nil
}

func (s *Store) Workflow(ctx context.Context, workflowID string) (protocol.WorkflowDetail, error) {
	var detail protocol.WorkflowDetail
	workflow, err := scanWorkflow(s.db.QueryRowContext(ctx, workflowSelect+` WHERE workflow.id = ?`, strings.TrimSpace(workflowID)))
	if errors.Is(err, sql.ErrNoRows) {
		return detail, ErrNotFound
	}
	if err != nil {
		return detail, unavailable(err)
	}
	detail.Workflow = workflow
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, workflow_id, revision_number, name, summary, instructions, created_at
		FROM workflow_revisions
		WHERE workflow_id = ?
		ORDER BY revision_number DESC
		LIMIT ?
	`, workflow.ID, protocol.MaxWorkflowRevisions)
	if err != nil {
		return detail, unavailable(err)
	}
	defer rows.Close()
	for rows.Next() {
		revision, err := scanWorkflowRevision(rows, true)
		if err != nil {
			return detail, unavailable(err)
		}
		detail.Revisions = append(detail.Revisions, revision)
		if revision.ID == detail.Workflow.CurrentRevision.ID {
			detail.Workflow.CurrentRevision = revision
		}
	}
	return detail, rows.Err()
}

const workflowSelect = `
	SELECT workflow.id, workflow.enabled, workflow.created_at, workflow.updated_at,
	       revision.id, revision.workflow_id, revision.revision_number,
	       revision.name, revision.summary, revision.created_at
	FROM workflows workflow
	JOIN workflow_revisions revision ON revision.id = workflow.current_revision_id
`

func (s *Store) Workflows(ctx context.Context, request protocol.WorkflowPageRequest) (protocol.WorkflowPage, error) {
	if request.Limit < 1 || request.Limit > protocol.MaxWorkflowPageSize {
		return protocol.WorkflowPage{}, invalid("invalid_limit", "limit must be between 1 and 200")
	}
	query := workflowSelect
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 6)
	if strings.TrimSpace(request.Name) != "" {
		conditions = append(conditions, "workflow.current_name_key = ?")
		args = append(args, normalizeWorkflowName(request.Name))
	}
	if request.Enabled != nil {
		conditions = append(conditions, "workflow.enabled = ?")
		args = append(args, *request.Enabled)
	}
	if request.Cursor != nil {
		conditions = append(conditions, "(workflow.updated_at < ? OR (workflow.updated_at = ? AND workflow.id < ?))")
		args = append(args, request.Cursor.UpdatedAtMillis, request.Cursor.UpdatedAtMillis, request.Cursor.ID)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY workflow.updated_at DESC, workflow.id DESC LIMIT ?"
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return protocol.WorkflowPage{}, unavailable(err)
	}
	defer rows.Close()
	workflows := make([]protocol.Workflow, 0, request.Limit+1)
	for rows.Next() {
		workflow, err := scanWorkflow(rows)
		if err != nil {
			return protocol.WorkflowPage{}, unavailable(err)
		}
		workflows = append(workflows, workflow)
	}
	if err := rows.Err(); err != nil {
		return protocol.WorkflowPage{}, unavailable(err)
	}
	page := protocol.WorkflowPage{Workflows: workflows}
	if len(workflows) > request.Limit {
		page.Workflows = workflows[:request.Limit]
		last := page.Workflows[len(page.Workflows)-1]
		page.NextCursor = &protocol.WorkflowCursor{UpdatedAtMillis: last.UpdatedAt.UnixMilli(), ID: last.ID}
	}
	return page, nil
}

func (s *Store) SetWorkflowEnabled(ctx context.Context, workflowID string, enabled bool) (protocol.WorkflowDetail, error) {
	now := s.now().UnixMilli()
	result, err := s.db.ExecContext(ctx, `
		UPDATE workflows
		SET enabled = ?, updated_at = CASE WHEN enabled != ? THEN ? ELSE updated_at END
		WHERE id = ?
	`, enabled, enabled, now, strings.TrimSpace(workflowID))
	if err != nil {
		return protocol.WorkflowDetail{}, unavailable(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return protocol.WorkflowDetail{}, unavailable(err)
	}
	if changed == 0 {
		return protocol.WorkflowDetail{}, ErrNotFound
	}
	return s.Workflow(ctx, workflowID)
}

type workflowScanner interface {
	Scan(dest ...any) error
}

func scanWorkflow(row workflowScanner) (protocol.Workflow, error) {
	var workflow protocol.Workflow
	var enabled int
	var createdAt, updatedAt, revisionCreatedAt int64
	err := row.Scan(
		&workflow.ID, &enabled, &createdAt, &updatedAt,
		&workflow.CurrentRevision.ID, &workflow.CurrentRevision.WorkflowID,
		&workflow.CurrentRevision.RevisionNumber, &workflow.CurrentRevision.Name,
		&workflow.CurrentRevision.Summary, &revisionCreatedAt,
	)
	workflow.Enabled = enabled != 0
	workflow.CreatedAt = fromMillis(createdAt)
	workflow.UpdatedAt = fromMillis(updatedAt)
	workflow.CurrentRevision.CreatedAt = fromMillis(revisionCreatedAt)
	return workflow, err
}

func scanWorkflowRevision(row workflowScanner, instructions bool) (protocol.WorkflowRevision, error) {
	var revision protocol.WorkflowRevision
	var createdAt int64
	var err error
	if instructions {
		err = row.Scan(&revision.ID, &revision.WorkflowID, &revision.RevisionNumber,
			&revision.Name, &revision.Summary, &revision.Instructions, &createdAt)
	} else {
		err = row.Scan(&revision.ID, &revision.WorkflowID, &revision.RevisionNumber,
			&revision.Name, &revision.Summary, &createdAt)
	}
	revision.CreatedAt = fromMillis(createdAt)
	return revision, err
}
