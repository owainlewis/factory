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
	"unicode"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

const (
	maxDefinitionNameRunes  = 100
	maxDefinitionToolRunes  = 64
	maxDefinitionInputRunes = 64
	maxDefinitionInputBytes = 4 << 10
	maxDefinitionInputsSize = 16 << 10
)

type normalizedDefinitionMutation struct {
	Operation          string            `json:"operation"`
	RequestKey         string            `json:"request_key"`
	DefinitionID       string            `json:"definition_id,omitempty"`
	ExpectedGeneration int               `json:"expected_generation,omitempty"`
	Name               string            `json:"name"`
	Prompt             string            `json:"prompt"`
	Runtime            string            `json:"runtime"`
	AllowedTools       []string          `json:"allowed_tools"`
	TimeoutSeconds     int               `json:"timeout_seconds"`
	Inputs             map[string]string `json:"inputs"`
}

func normalizeDefinitionMutation(value normalizedDefinitionMutation) (normalizedDefinitionMutation, string, error) {
	value.RequestKey = strings.TrimSpace(value.RequestKey)
	value.DefinitionID = strings.TrimSpace(value.DefinitionID)
	value.Name = strings.TrimSpace(value.Name)
	value.Runtime = strings.ToLower(strings.TrimSpace(value.Runtime))
	if value.RequestKey == "" || len(value.RequestKey) > 200 {
		return value, "", invalid("invalid_request_key", "request_key is required and limited to 200 bytes")
	}
	if value.Name == "" || utf8.RuneCountInString(value.Name) > maxDefinitionNameRunes {
		return value, "", invalid("invalid_definition_name", "name is required and limited to 100 Unicode characters")
	}
	if strings.TrimSpace(value.Prompt) == "" || len([]byte(value.Prompt)) > protocol.MaxDefinitionPromptBytes {
		return value, "", invalid("invalid_definition_prompt", "prompt is required and limited to 64 KiB")
	}
	if !protocol.SupportedRuntime(value.Runtime) {
		return value, "", invalid("invalid_definition_runtime", "runtime must be pi, codex, or claude-code")
	}
	if value.TimeoutSeconds < 1 || value.TimeoutSeconds > int(protocol.MaxTimeout/time.Second) {
		return value, "", invalid("invalid_definition_timeout", "timeout_seconds must be between 1 and 28800")
	}
	tools, err := normalizeDefinitionTools(value.AllowedTools)
	if err != nil {
		return value, "", err
	}
	inputs, err := normalizeDefinitionInputs(value.Inputs)
	if err != nil {
		return value, "", err
	}
	value.AllowedTools = tools
	value.Inputs = inputs
	return value, normalizeTitleKey(value.Name), nil
}

func normalizeDefinitionTools(values []string) ([]string, error) {
	if len(values) > protocol.MaxDefinitionTools {
		return nil, invalid("invalid_definition_tools", "allowed_tools is limited to 32 entries")
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value != "git" && value != "gh" {
			return nil, invalid("invalid_definition_tools", "allowed_tools may contain git and gh")
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func normalizeDefinitionInputs(values map[string]string) (map[string]string, error) {
	if len(values) > protocol.MaxDefinitionInputs {
		return nil, invalid("invalid_definition_inputs", "inputs is limited to 32 entries")
	}
	result := make(map[string]string, len(values))
	total := 0
	for rawKey, value := range values {
		key := strings.TrimSpace(rawKey)
		if !validDefinitionInputName(key) {
			return nil, invalid("invalid_definition_inputs", "input names must use letters, numbers, and underscores and start with a letter or underscore")
		}
		if len([]byte(value)) > maxDefinitionInputBytes || containsControl(value) {
			return nil, invalid("invalid_definition_inputs", "input values must be printable and limited to 4 KiB")
		}
		if _, exists := result[key]; exists {
			return nil, invalid("invalid_definition_inputs", "input names must be unique after trimming")
		}
		result[key] = value
		total += len([]byte(key)) + len([]byte(value))
	}
	if total > maxDefinitionInputsSize {
		return nil, invalid("invalid_definition_inputs", "inputs are limited to 16 KiB in total")
	}
	return result, nil
}

func validDefinitionInputName(value string) bool {
	if value == "" || len(value) > maxDefinitionInputRunes {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || character == '_' ||
			(index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func definitionMutationDigest(value normalizedDefinitionMutation) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	return digest[:], nil
}

func (s *Store) CreateDefinition(
	ctx context.Context,
	input protocol.CreateDefinitionRequest,
) (protocol.Definition, bool, error) {
	value, nameKey, err := normalizeDefinitionMutation(normalizedDefinitionMutation{
		Operation: "create", RequestKey: input.RequestKey, Name: input.Name,
		Prompt: input.Prompt, Runtime: input.Runtime, AllowedTools: input.AllowedTools,
		TimeoutSeconds: input.TimeoutSeconds, Inputs: input.Inputs,
	})
	if err != nil {
		return protocol.Definition{}, false, err
	}
	digest, err := definitionMutationDigest(value)
	if err != nil {
		return protocol.Definition{}, false, unavailable(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Definition{}, false, unavailable(err)
	}
	defer tx.Rollback()
	definitionID, replay, err := definitionMutationReplay(ctx, tx, value.RequestKey, digest)
	if err != nil {
		return protocol.Definition{}, false, err
	}
	if replay {
		if err := tx.Commit(); err != nil {
			return protocol.Definition{}, false, unavailable(err)
		}
		definition, err := s.Definition(ctx, definitionID)
		return definition, false, err
	}
	if err := definitionNameAvailable(ctx, tx, nameKey, ""); err != nil {
		return protocol.Definition{}, false, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM definitions`).Scan(&count); err != nil {
		return protocol.Definition{}, false, unavailable(err)
	}
	if count >= protocol.MaxDefinitions {
		return protocol.Definition{}, false, conflict("definition_limit_reached", "the Definition limit has been reached")
	}
	definitionID, err = newID()
	if err != nil {
		return protocol.Definition{}, false, unavailable(err)
	}
	tools, err := json.Marshal(value.AllowedTools)
	if err != nil {
		return protocol.Definition{}, false, unavailable(err)
	}
	inputs, err := json.Marshal(value.Inputs)
	if err != nil {
		return protocol.Definition{}, false, unavailable(err)
	}
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO definitions(
			id, name, name_key, prompt, runtime, allowed_tools, timeout_seconds,
			inputs, generation, archived, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 0, ?, ?)
	`, definitionID, value.Name, nameKey, value.Prompt, value.Runtime, tools,
		value.TimeoutSeconds, inputs, now, now); err != nil {
		return protocol.Definition{}, false, unavailable(err)
	}
	if err := insertDefinitionMutation(ctx, tx, value.RequestKey, digest, definitionID, now); err != nil {
		return protocol.Definition{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.Definition{}, false, unavailable(err)
	}
	definition, err := s.Definition(ctx, definitionID)
	return definition, true, err
}

func (s *Store) UpdateDefinition(
	ctx context.Context,
	definitionID string,
	input protocol.UpdateDefinitionRequest,
) (protocol.Definition, bool, error) {
	definitionID = strings.TrimSpace(definitionID)
	if definitionID == "" {
		return protocol.Definition{}, false, ErrNotFound
	}
	if input.ExpectedGeneration < 1 {
		return protocol.Definition{}, false, invalid("invalid_expected_generation", "expected_generation must be at least 1")
	}
	value, nameKey, err := normalizeDefinitionMutation(normalizedDefinitionMutation{
		Operation: "update", RequestKey: input.RequestKey, DefinitionID: definitionID,
		ExpectedGeneration: input.ExpectedGeneration, Name: input.Name, Prompt: input.Prompt,
		Runtime: input.Runtime, AllowedTools: input.AllowedTools,
		TimeoutSeconds: input.TimeoutSeconds, Inputs: input.Inputs,
	})
	if err != nil {
		return protocol.Definition{}, false, err
	}
	digest, err := definitionMutationDigest(value)
	if err != nil {
		return protocol.Definition{}, false, unavailable(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Definition{}, false, unavailable(err)
	}
	defer tx.Rollback()
	replayID, replay, err := definitionMutationReplay(ctx, tx, value.RequestKey, digest)
	if err != nil {
		return protocol.Definition{}, false, err
	}
	if replay {
		if replayID != definitionID {
			return protocol.Definition{}, false, conflict("request_key_conflict", "request_key belongs to a different Definition")
		}
		if err := tx.Commit(); err != nil {
			return protocol.Definition{}, false, unavailable(err)
		}
		definition, err := s.Definition(ctx, definitionID)
		return definition, false, err
	}
	if err := definitionNameAvailable(ctx, tx, nameKey, definitionID); err != nil {
		return protocol.Definition{}, false, err
	}
	tools, err := json.Marshal(value.AllowedTools)
	if err != nil {
		return protocol.Definition{}, false, unavailable(err)
	}
	inputs, err := json.Marshal(value.Inputs)
	if err != nil {
		return protocol.Definition{}, false, unavailable(err)
	}
	now := s.now().UnixMilli()
	result, err := tx.ExecContext(ctx, `
		UPDATE definitions
		SET name = ?, name_key = ?, prompt = ?, runtime = ?, allowed_tools = ?,
		    timeout_seconds = ?, inputs = ?, generation = generation + 1, updated_at = ?
		WHERE id = ? AND generation = ? AND archived = 0
	`, value.Name, nameKey, value.Prompt, value.Runtime, tools, value.TimeoutSeconds,
		inputs, now, definitionID, value.ExpectedGeneration)
	if err != nil {
		return protocol.Definition{}, false, unavailable(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return protocol.Definition{}, false, unavailable(err)
	}
	if changed != 1 {
		var currentGeneration int
		var archived bool
		err := tx.QueryRowContext(ctx, `SELECT generation, archived FROM definitions WHERE id = ?`, definitionID).
			Scan(&currentGeneration, &archived)
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.Definition{}, false, ErrNotFound
		}
		if err != nil {
			return protocol.Definition{}, false, unavailable(err)
		}
		if archived {
			return protocol.Definition{}, false, conflict("definition_archived", "archived Definitions cannot be edited")
		}
		return protocol.Definition{}, false, conflict("definition_generation_conflict", "the Definition was edited by another request")
	}
	if err := insertDefinitionMutation(ctx, tx, value.RequestKey, digest, definitionID, now); err != nil {
		return protocol.Definition{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.Definition{}, false, unavailable(err)
	}
	definition, err := s.Definition(ctx, definitionID)
	return definition, true, err
}

func (s *Store) SetDefinitionArchived(
	ctx context.Context,
	definitionID string,
	archived bool,
	expectedGeneration int,
) (protocol.Definition, error) {
	definitionID = strings.TrimSpace(definitionID)
	if definitionID == "" {
		return protocol.Definition{}, ErrNotFound
	}
	if expectedGeneration < 1 {
		return protocol.Definition{}, invalid("invalid_expected_generation", "expected_generation must be at least 1")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Definition{}, unavailable(err)
	}
	defer tx.Rollback()
	var currentGeneration int
	var currentArchived bool
	err = tx.QueryRowContext(ctx, `SELECT generation, archived FROM definitions WHERE id = ?`, definitionID).
		Scan(&currentGeneration, &currentArchived)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Definition{}, ErrNotFound
	}
	if err != nil {
		return protocol.Definition{}, unavailable(err)
	}
	if currentArchived == archived {
		if err := tx.Commit(); err != nil {
			return protocol.Definition{}, unavailable(err)
		}
		return s.Definition(ctx, definitionID)
	}
	if currentGeneration != expectedGeneration {
		return protocol.Definition{}, conflict("definition_generation_conflict", "the Definition was edited by another request")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE definitions SET archived = ?, generation = generation + 1, updated_at = ? WHERE id = ?
	`, archived, s.now().UnixMilli(), definitionID); err != nil {
		return protocol.Definition{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.Definition{}, unavailable(err)
	}
	return s.Definition(ctx, definitionID)
}

func (s *Store) Definition(ctx context.Context, definitionID string) (protocol.Definition, error) {
	definition, err := scanDefinition(s.db.QueryRowContext(ctx,
		definitionSelect+` WHERE id = ?`, strings.TrimSpace(definitionID)))
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Definition{}, ErrNotFound
	}
	if err != nil {
		return protocol.Definition{}, unavailable(err)
	}
	return definition, nil
}

func (s *Store) DefinitionSnapshot(ctx context.Context, definitionID string) (protocol.DefinitionSnapshot, error) {
	definition, err := s.Definition(ctx, definitionID)
	if err != nil {
		return protocol.DefinitionSnapshot{}, err
	}
	return definition.Snapshot(), nil
}

const definitionSelect = `
	SELECT id, name, prompt, runtime, allowed_tools, timeout_seconds, inputs,
	       generation, archived, created_at, updated_at
	FROM definitions
`

func (s *Store) Definitions(ctx context.Context, request protocol.DefinitionPageRequest) (protocol.DefinitionPage, error) {
	if request.Limit < 1 || request.Limit > protocol.MaxDefinitionPageSize {
		return protocol.DefinitionPage{}, invalid("invalid_limit", "limit must be between 1 and 200")
	}
	query := definitionSelect + ` WHERE archived = ?`
	args := []any{request.Archived}
	if request.Cursor != nil {
		query += ` AND (updated_at < ? OR (updated_at = ? AND id < ?))`
		args = append(args, request.Cursor.UpdatedAtMillis, request.Cursor.UpdatedAtMillis, request.Cursor.ID)
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return protocol.DefinitionPage{}, unavailable(err)
	}
	defer rows.Close()
	definitions := make([]protocol.Definition, 0, request.Limit+1)
	for rows.Next() {
		definition, err := scanDefinition(rows)
		if err != nil {
			return protocol.DefinitionPage{}, unavailable(err)
		}
		definitions = append(definitions, definition)
	}
	if err := rows.Err(); err != nil {
		return protocol.DefinitionPage{}, unavailable(err)
	}
	page := protocol.DefinitionPage{Definitions: definitions}
	if len(definitions) > request.Limit {
		page.Definitions = definitions[:request.Limit]
		last := page.Definitions[len(page.Definitions)-1]
		page.NextCursor = &protocol.DefinitionCursor{UpdatedAtMillis: last.UpdatedAt.UnixMilli(), ID: last.ID}
	}
	return page, nil
}

func definitionNameAvailable(ctx context.Context, tx *sql.Tx, nameKey, excludingID string) error {
	query := `SELECT id FROM definitions WHERE name_key = ?`
	args := []any{nameKey}
	if excludingID != "" {
		query += ` AND id != ?`
		args = append(args, excludingID)
	}
	var conflictingID string
	err := tx.QueryRowContext(ctx, query, args...).Scan(&conflictingID)
	if err == nil {
		return conflict("definition_name_conflict", "a Definition with this normalized name already exists")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return unavailable(err)
	}
	return nil
}

func definitionMutationReplay(
	ctx context.Context,
	tx *sql.Tx,
	requestKey string,
	digest []byte,
) (definitionID string, replay bool, resultErr error) {
	var storedDigest []byte
	err := tx.QueryRowContext(ctx, `
		SELECT definition_id, request_digest FROM definition_mutations WHERE request_key = ?
	`, requestKey).Scan(&definitionID, &storedDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, unavailable(err)
	}
	if !bytes.Equal(storedDigest, digest) {
		return "", false, conflict("request_key_conflict", "request_key was already used for a different Definition mutation")
	}
	return definitionID, true, nil
}

func insertDefinitionMutation(
	ctx context.Context,
	tx *sql.Tx,
	requestKey string,
	digest []byte,
	definitionID string,
	createdAt int64,
) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO definition_mutations(request_key, request_digest, definition_id, created_at)
		VALUES (?, ?, ?, ?)
	`, requestKey, digest, definitionID, createdAt); err != nil {
		return unavailable(err)
	}
	return nil
}

type definitionScanner interface {
	Scan(dest ...any) error
}

func scanDefinition(row definitionScanner) (protocol.Definition, error) {
	var definition protocol.Definition
	var toolsJSON, inputsJSON []byte
	var archived int
	var createdAt, updatedAt int64
	if err := row.Scan(
		&definition.ID, &definition.Name, &definition.Prompt, &definition.Runtime,
		&toolsJSON, &definition.TimeoutSeconds, &inputsJSON, &definition.Generation,
		&archived, &createdAt, &updatedAt,
	); err != nil {
		return definition, err
	}
	if err := json.Unmarshal(toolsJSON, &definition.AllowedTools); err != nil {
		return definition, err
	}
	if err := json.Unmarshal(inputsJSON, &definition.Inputs); err != nil {
		return definition, err
	}
	if definition.AllowedTools == nil {
		definition.AllowedTools = []string{}
	}
	if definition.Inputs == nil {
		definition.Inputs = map[string]string{}
	}
	definition.Archived = archived != 0
	definition.CreatedAt = fromMillis(createdAt)
	definition.UpdatedAt = fromMillis(updatedAt)
	return definition, nil
}
