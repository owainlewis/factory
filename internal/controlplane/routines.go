package controlplane

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

const (
	defaultRoutineConcurrency       = 10
	defaultRoutineTimeout           = 2 * time.Hour
	defaultRoutinePageSize          = 50
	maxRoutinePageSize              = 200
	routineConcurrencyBlockedReason = "Waiting for an available Routine concurrency slot."
)

func normalizeTitleKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeMigratedRoutineTitleKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM routines WHERE migration_only = 0`)
	if err != nil {
		return err
	}
	type routineName struct {
		id   string
		name string
	}
	var routines []routineName
	for rows.Next() {
		var routine routineName
		if err := rows.Scan(&routine.id, &routine.name); err != nil {
			rows.Close()
			return err
		}
		routines = append(routines, routine)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, routine := range routines {
		if _, err := tx.ExecContext(ctx, `UPDATE routines SET name_key = ? WHERE id = ?`,
			normalizeTitleKey(routine.name), routine.id); err != nil {
			return err
		}
	}
	return nil
}

type normalizedRoutine struct {
	name               string
	nameKey            string
	prompt             string
	runtime            string
	executionProfileID string
	timeoutSeconds     int
	concurrencyLimit   int
	repositoryIDs      []string
	scheduleEnabled    bool
	cron               string
	timezone           string
	nextDueAt          *time.Time
}

func normalizeRoutine(input protocol.SaveRoutineRequest, now time.Time) (normalizedRoutine, error) {
	value := normalizedRoutine{
		name:               strings.TrimSpace(input.Name),
		prompt:             strings.TrimSpace(input.Prompt),
		runtime:            strings.ToLower(strings.TrimSpace(input.Runtime)),
		executionProfileID: strings.TrimSpace(input.ExecutionProfileID),
		timeoutSeconds:     input.TimeoutSeconds,
		concurrencyLimit:   input.ConcurrencyLimit,
		repositoryIDs:      append([]string(nil), input.RepositoryIDs...),
		scheduleEnabled:    input.Schedule.Enabled,
		cron:               strings.TrimSpace(input.Schedule.Cron),
		timezone:           strings.TrimSpace(input.Schedule.Timezone),
	}
	value.nameKey = normalizeTitleKey(value.name)
	if value.executionProfileID == protocol.PersistentAutoProfileID {
		value.executionProfileID = ""
	}
	if value.name == "" || utf8.RuneCountInString(value.name) > 200 {
		return value, invalid("invalid_routine_name", "name is required and limited to 200 characters")
	}
	if value.prompt == "" || len([]byte(value.prompt)) > protocol.MaxRoutinePromptBytes {
		return value, invalid("invalid_routine_prompt", "prompt is required and limited to 64 KiB")
	}
	if value.runtime == "" {
		value.runtime = protocol.RuntimeCodex
	}
	if !protocol.SupportedRuntime(value.runtime) {
		return value, invalid("invalid_routine_runtime", "runtime is not supported")
	}
	if value.timeoutSeconds == 0 {
		value.timeoutSeconds = int(defaultRoutineTimeout.Seconds())
	}
	if value.timeoutSeconds < 1 || value.timeoutSeconds > int(protocol.MaxTimeout.Seconds()) {
		return value, invalid("invalid_routine_timeout", "timeout_seconds must be between 1 and 28800")
	}
	if value.concurrencyLimit == 0 {
		value.concurrencyLimit = defaultRoutineConcurrency
	}
	if value.concurrencyLimit < 1 || value.concurrencyLimit > 100 {
		return value, invalid("invalid_routine_concurrency", "concurrency_limit must be between 1 and 100")
	}
	if len(value.repositoryIDs) == 0 && value.scheduleEnabled {
		return value, invalid("routine_repository_required", "select at least one repository before enabling a schedule")
	}
	if len(value.repositoryIDs) > protocol.MaxRoutineRepositories {
		return value, invalid("too_many_routine_repositories", "a Routine is limited to 100 repositories")
	}
	repositorySet := make(map[string]struct{}, len(value.repositoryIDs))
	for index, repositoryID := range value.repositoryIDs {
		repositoryID = strings.TrimSpace(repositoryID)
		if repositoryID == "" {
			return value, invalid("routine_repository_required", "repository_ids cannot contain an empty value")
		}
		if _, exists := repositorySet[repositoryID]; exists {
			return value, invalid("duplicate_routine_repository", "each repository may be selected once")
		}
		repositorySet[repositoryID] = struct{}{}
		value.repositoryIDs[index] = repositoryID
	}
	if value.scheduleEnabled || value.cron != "" || value.timezone != "" {
		schedule, cron, timezone, err := parseCronSchedule(value.cron, value.timezone)
		if err != nil {
			return value, invalid("invalid_routine_schedule", err.Error())
		}
		next, err := schedule.Next(now)
		if err != nil {
			return value, invalid("invalid_routine_schedule", err.Error())
		}
		value.cron, value.timezone, value.nextDueAt = cron, timezone, &next
		if value.scheduleEnabled {
			resolved, err := protocol.ResolveRoutineSchedulePrompt(value.prompt, next, cron, timezone)
			if err != nil {
				return value, invalid("invalid_routine_schedule", err.Error())
			}
			if len([]byte(resolved)) > protocol.MaxResolvedPromptBytes {
				return value, invalid("routine_schedule_prompt_too_large", "shorten the prompt before enabling its schedule")
			}
		}
	} else {
		value.cron, value.timezone = "", ""
	}
	return value, nil
}

func (s *Store) CreateRoutine(ctx context.Context, input protocol.SaveRoutineRequest) (protocol.Routine, error) {
	value, err := normalizeRoutine(input, s.now())
	if err != nil {
		return protocol.Routine{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Routine{}, unavailable(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM routines WHERE migration_only = 0 AND read_only = 0`).Scan(&count); err != nil {
		return protocol.Routine{}, unavailable(err)
	}
	if count >= protocol.MaxRoutines {
		return protocol.Routine{}, conflict("routine_limit_reached", "Factory is limited to 500 Routines")
	}
	if err := validateRoutineRepositories(ctx, tx, value.repositoryIDs); err != nil {
		return protocol.Routine{}, err
	}
	if err := validateRoutineExecutionProfile(ctx, tx, value.executionProfileID); err != nil {
		return protocol.Routine{}, err
	}
	id, err := newID()
	if err != nil {
		return protocol.Routine{}, unavailable(err)
	}
	now := s.now().UnixMilli()
	var next any
	if value.scheduleEnabled && value.nextDueAt != nil {
		next = value.nextDueAt.UnixMilli()
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO routines(
			id, name, name_key, prompt, runtime, timeout_seconds,
			concurrency_limit, execution_profile_id, generation, archived, migration_only, schedule_enabled,
			cron, timezone, next_due_at, schedule_health_status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 0, 0, ?, ?, ?, ?, ?, ?, ?)
	`, id, value.name, value.nameKey, value.prompt, value.runtime, value.timeoutSeconds,
		value.concurrencyLimit, nullableString(value.executionProfileID), value.scheduleEnabled, nullableString(value.cron), nullableString(value.timezone),
		next, routineScheduleHealth(value.scheduleEnabled), now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return protocol.Routine{}, conflict("routine_name_conflict", "a Routine with this name already exists")
		}
		return protocol.Routine{}, unavailable(err)
	}
	if err := replaceRoutineRepositories(ctx, tx, id, value.repositoryIDs); err != nil {
		return protocol.Routine{}, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.Routine{}, unavailable(err)
	}
	return s.Routine(ctx, id)
}

func (s *Store) UpdateRoutine(ctx context.Context, id string, input protocol.SaveRoutineRequest) (protocol.Routine, error) {
	value, err := normalizeRoutine(input, s.now())
	if err != nil {
		return protocol.Routine{}, err
	}
	if input.ExpectedGeneration < 1 {
		return protocol.Routine{}, invalid("routine_generation_required", "expected_generation is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Routine{}, unavailable(err)
	}
	defer tx.Rollback()
	if err := validateRoutineRepositories(ctx, tx, value.repositoryIDs); err != nil {
		return protocol.Routine{}, err
	}
	if err := validateRoutineExecutionProfile(ctx, tx, value.executionProfileID); err != nil {
		return protocol.Routine{}, err
	}
	var archived, migrationOnly, readOnly int
	var pendingDue, scheduleRetry sql.NullInt64
	var scheduleHealthStatus, scheduleHealthCode string
	err = tx.QueryRowContext(ctx, `
		SELECT archived, migration_only, read_only, pending_due_at, schedule_retry_at,
		       schedule_health_status, schedule_health_code
		FROM routines WHERE id = ?
	`, id).Scan(&archived, &migrationOnly, &readOnly, &pendingDue, &scheduleRetry,
		&scheduleHealthStatus, &scheduleHealthCode)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Routine{}, ErrNotFound
	}
	if err != nil {
		return protocol.Routine{}, unavailable(err)
	}
	if migrationOnly != 0 {
		return protocol.Routine{}, conflict("routine_read_only", "migration history Routines are read-only")
	}
	if readOnly != 0 {
		return protocol.Routine{}, conflict("routine_read_only", "historical Routine revisions are read-only")
	}
	if archived != 0 && value.scheduleEnabled {
		return protocol.Routine{}, conflict("routine_archived", "an archived Routine cannot be scheduled")
	}
	now := s.now().UnixMilli()
	var next any
	if value.scheduleEnabled && value.nextDueAt != nil && !pendingDue.Valid {
		next = value.nextDueAt.UnixMilli()
	}
	preserveBlockedOccurrence := pendingDue.Valid && !scheduleRetry.Valid &&
		(scheduleHealthStatus == "blocked" || scheduleHealthCode != "")
	result, err := tx.ExecContext(ctx, `
		UPDATE routines SET
			name = ?, name_key = ?, prompt = ?, runtime = ?, timeout_seconds = ?,
			concurrency_limit = ?, execution_profile_id = ?, generation = generation + 1, schedule_enabled = ?,
			cron = CASE WHEN pending_due_at IS NOT NULL AND ? = 0 THEN cron ELSE ? END,
			timezone = CASE WHEN pending_due_at IS NOT NULL AND ? = 0 THEN timezone ELSE ? END,
			next_due_at = CASE WHEN pending_due_at IS NULL THEN ? ELSE next_due_at END,
			schedule_health_status = CASE
				WHEN ? = 1 AND ? = 1 THEN 'blocked'
				WHEN ? = 1 THEN 'healthy' ELSE 'disabled' END,
			schedule_health_code = CASE
				WHEN ? = 1 THEN schedule_health_code
				ELSE '' END,
			schedule_health_message = CASE
				WHEN ? = 1 THEN schedule_health_message
				ELSE '' END,
			updated_at = ?
		WHERE id = ? AND generation = ?
	`, value.name, value.nameKey, value.prompt, value.runtime, value.timeoutSeconds,
		value.concurrencyLimit, nullableString(value.executionProfileID), value.scheduleEnabled, value.scheduleEnabled, nullableString(value.cron),
		value.scheduleEnabled, nullableString(value.timezone),
		next, value.scheduleEnabled, preserveBlockedOccurrence, value.scheduleEnabled,
		preserveBlockedOccurrence, preserveBlockedOccurrence, now, id, input.ExpectedGeneration)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return protocol.Routine{}, conflict("routine_name_conflict", "a Routine with this name already exists")
		}
		return protocol.Routine{}, unavailable(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return protocol.Routine{}, conflict("routine_generation_conflict", "the Routine changed; refresh and try again")
	}
	if err := replaceRoutineRepositories(ctx, tx, id, value.repositoryIDs); err != nil {
		return protocol.Routine{}, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.Routine{}, unavailable(err)
	}
	return s.Routine(ctx, id)
}

func routineScheduleHealth(enabled bool) string {
	if enabled {
		return "healthy"
	}
	return "disabled"
}

func validateRoutineRepositories(ctx context.Context, tx *sql.Tx, ids []string) error {
	for _, id := range ids {
		var enabled, centrallyManaged, advertised int
		err := tx.QueryRowContext(ctx, `
			SELECT repository.enabled, repository.centrally_managed,
			       EXISTS (SELECT 1 FROM worker_repositories available
			               WHERE available.repository_id = repository.id AND available.advertised = 1)
			FROM repositories repository WHERE repository.id = ?
		`, id).Scan(&enabled, &centrallyManaged, &advertised)
		if errors.Is(err, sql.ErrNoRows) {
			return invalid("routine_repository_not_found", "one selected repository does not exist")
		}
		if err != nil {
			return unavailable(err)
		}
		if centrallyManaged != 0 && enabled == 0 || centrallyManaged == 0 && advertised == 0 {
			return conflict("routine_repository_unavailable", "every selected repository must be enabled or advertised by a Worker")
		}
	}
	return nil
}

func validateRoutineExecutionProfile(ctx context.Context, tx *sql.Tx, id string) error {
	if id == "" {
		return nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_profiles WHERE id = ?`, id).Scan(&exists); err != nil {
		return unavailable(err)
	}
	if exists == 0 {
		return invalid("execution_profile_not_found", "the selected execution profile does not exist")
	}
	return nil
}

func replaceRoutineRepositories(ctx context.Context, tx *sql.Tx, routineID string, ids []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM routine_repositories WHERE routine_id = ?`, routineID); err != nil {
		return unavailable(err)
	}
	for position, id := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO routine_repositories(routine_id, position, repository_id) VALUES (?, ?, ?)`, routineID, position, id); err != nil {
			return unavailable(err)
		}
	}
	return nil
}

func (s *Store) SetRoutineArchived(ctx context.Context, id string, input protocol.SetRoutineArchivedRequest) (protocol.Routine, error) {
	if input.Archived == nil {
		return protocol.Routine{}, invalid("routine_archived_required", "archived is required")
	}
	if input.ExpectedGeneration < 1 {
		return protocol.Routine{}, invalid("routine_generation_required", "expected_generation is required")
	}
	archived := *input.Archived
	now := s.now().UnixMilli()
	result, err := s.db.ExecContext(ctx, `
		UPDATE routines SET archived = ?, generation = generation + 1,
			schedule_enabled = CASE WHEN ? = 1 THEN 0 ELSE schedule_enabled END,
			next_due_at = CASE WHEN ? = 1 THEN NULL ELSE next_due_at END,
			schedule_health_status = CASE WHEN ? = 1 THEN 'disabled' ELSE schedule_health_status END,
			updated_at = ?
		WHERE id = ? AND generation = ? AND migration_only = 0 AND read_only = 0
	`, archived, archived, archived, archived, now, id, input.ExpectedGeneration)
	if err != nil {
		return protocol.Routine{}, unavailable(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		var exists, readOnly int
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(read_only), 0) FROM routines WHERE id = ?`, id).Scan(&exists, &readOnly)
		if exists == 0 {
			return protocol.Routine{}, ErrNotFound
		}
		if readOnly != 0 {
			return protocol.Routine{}, conflict("routine_read_only", "historical Routine revisions are read-only")
		}
		return protocol.Routine{}, conflict("routine_generation_conflict", "the Routine changed; refresh and try again")
	}
	return s.Routine(ctx, id)
}

func (s *Store) Routines(ctx context.Context, includeArchived bool, limit int, cursor string) (protocol.RoutinePage, error) {
	if limit == 0 {
		limit = defaultRoutinePageSize
	}
	if limit < 1 || limit > maxRoutinePageSize {
		return protocol.RoutinePage{}, invalid("invalid_limit", "limit must be between 1 and 200")
	}
	updated, cursorID, err := decodeWorkCursor(cursor)
	if err != nil {
		return protocol.RoutinePage{}, err
	}
	query := `SELECT id, updated_at FROM routines WHERE migration_only = 0`
	args := make([]any, 0, 4)
	if !includeArchived {
		query += ` AND archived = 0`
	}
	if updated != 0 {
		query += ` AND (updated_at < ? OR (updated_at = ? AND id < ?))`
		args = append(args, updated, updated, cursorID)
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return protocol.RoutinePage{}, unavailable(err)
	}
	type routineKey struct {
		id      string
		updated int64
	}
	var keys []routineKey
	for rows.Next() {
		var key routineKey
		if err := rows.Scan(&key.id, &key.updated); err != nil {
			rows.Close()
			return protocol.RoutinePage{}, unavailable(err)
		}
		keys = append(keys, key)
	}
	if err := rows.Close(); err != nil {
		return protocol.RoutinePage{}, unavailable(err)
	}
	hasMore := len(keys) > limit
	if hasMore {
		keys = keys[:limit]
	}
	page := protocol.RoutinePage{Routines: make([]protocol.Routine, 0, len(keys))}
	for _, key := range keys {
		routine, err := s.Routine(ctx, key.id)
		if err != nil {
			return protocol.RoutinePage{}, err
		}
		page.Routines = append(page.Routines, routineSummary(routine))
	}
	if hasMore {
		last := keys[len(keys)-1]
		page.NextCursor = encodeWorkCursor(last.updated, last.id)
	}
	return page, nil
}

func routineSummary(routine protocol.Routine) protocol.Routine {
	routine.PromptPreview = routine.Prompt
	preview := []rune(routine.PromptPreview)
	if len(preview) > 180 {
		routine.PromptPreview = string(preview[:180]) + "…"
	}
	routine.Prompt = ""
	routine.RepositoryCount = len(routine.Repositories)
	routine.Repositories = nil
	return routine
}

func (s *Store) Routine(ctx context.Context, id string) (protocol.Routine, error) {
	var routine protocol.Routine
	var archived, readOnly, scheduleEnabled int
	var cron, timezone sql.NullString
	var nextDue, pendingDue sql.NullInt64
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, prompt, runtime, COALESCE(execution_profile_id, ''), timeout_seconds, concurrency_limit,
		       generation, archived, read_only, schedule_enabled, cron, timezone, next_due_at, pending_due_at,
		       schedule_health_status, schedule_health_code, schedule_health_message, created_at, updated_at
		FROM routines WHERE id = ? AND migration_only = 0
	`, id).Scan(&routine.ID, &routine.Name, &routine.Prompt, &routine.Runtime, &routine.ExecutionProfileID,
		&routine.TimeoutSeconds, &routine.ConcurrencyLimit, &routine.Generation, &archived, &readOnly,
		&scheduleEnabled, &cron, &timezone, &nextDue, &pendingDue, &routine.Schedule.HealthStatus,
		&routine.Schedule.HealthCode, &routine.Schedule.HealthMessage, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return routine, ErrNotFound
	}
	if err != nil {
		return routine, unavailable(err)
	}
	routine.Archived = archived != 0
	routine.ReadOnly = readOnly != 0
	routine.Schedule.Enabled = scheduleEnabled != 0
	routine.Schedule.Cron, routine.Schedule.Timezone = cron.String, timezone.String
	if nextDue.Valid {
		value := fromMillis(nextDue.Int64)
		routine.Schedule.NextDueAt = &value
	}
	if pendingDue.Valid {
		value := fromMillis(pendingDue.Int64)
		routine.Schedule.PendingDueAt = &value
	}
	routine.CreatedAt, routine.UpdatedAt = fromMillis(created), fromMillis(updated)
	rows, err := s.db.QueryContext(ctx, `
		SELECT repository.id, repository.remote_identity
		FROM routine_repositories selected
		JOIN repositories repository ON repository.id = selected.repository_id
		WHERE selected.routine_id = ? ORDER BY selected.position
	`, id)
	if err != nil {
		return routine, unavailable(err)
	}
	for rows.Next() {
		var repository protocol.RoutineRepository
		if err := rows.Scan(&repository.ID, &repository.RemoteIdentity); err != nil {
			rows.Close()
			return routine, unavailable(err)
		}
		routine.Repositories = append(routine.Repositories, repository)
	}
	if err := rows.Close(); err != nil {
		return routine, unavailable(err)
	}
	routine.RepositoryCount = len(routine.Repositories)
	_ = s.db.QueryRowContext(ctx, `
		SELECT COALESCE((SELECT CASE
			WHEN SUM(target.state IN ('blocked','queued','preparing','running')) = 0 THEN CASE
				WHEN SUM(target.state = 'succeeded') = COUNT(*) THEN 'succeeded'
				WHEN SUM(target.state = 'cancelled') = COUNT(*) THEN 'cancelled'
				WHEN SUM(target.state = 'succeeded') = 0 AND SUM(target.state = 'failed') > 0 THEN 'failed'
				ELSE 'partial' END
			WHEN SUM(target.state IN ('preparing','running')) > 0
			  OR SUM(target.state IN ('succeeded','failed','cancelled')) > 0 THEN 'running'
			WHEN SUM(target.state = 'blocked') = SUM(target.state IN ('blocked','queued','preparing','running')) THEN 'blocked'
			ELSE 'queued' END
		FROM work recent JOIN work_targets target ON target.work_id = recent.id
		WHERE recent.routine_id = ? GROUP BY recent.id ORDER BY recent.admitted_at DESC LIMIT 1), '')
	`, id).Scan(&routine.LastWorkState)
	return routine, nil
}

func (s *Store) RunRoutine(ctx context.Context, id string, input protocol.RunRoutineRequest) (protocol.WorkDetail, bool, error) {
	input.RequestKey = strings.TrimSpace(input.RequestKey)
	input.ExecutionProfileID = strings.TrimSpace(input.ExecutionProfileID)
	if input.RequestKey == "" || len(input.RequestKey) > 200 {
		return protocol.WorkDetail{}, false, invalid("invalid_request_key", "request_key is required and limited to 200 bytes")
	}
	if strings.HasPrefix(input.RequestKey, "schedule:") {
		return protocol.WorkDetail{}, false, invalid("reserved_request_key", "request_key uses a reserved internal prefix")
	}
	return s.admitRoutine(ctx, id, "manual", input.RequestKey, nil, nil, input.ExecutionProfileID)
}

func (s *Store) admitRoutine(
	ctx context.Context,
	routineID, source, requestKey string,
	scheduledAt *time.Time,
	frozen *protocol.RoutineSnapshot,
	requestedProfileID string,
) (protocol.WorkDetail, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.WorkDetail{}, false, unavailable(err)
	}
	defer tx.Rollback()
	var existingID, existingRoutineID, existingSource string
	var existingScheduledAt sql.NullInt64
	var existingRequestedProfile sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT id, routine_id, source, scheduled_at, requested_execution_profile_id FROM work WHERE request_key = ?
	`, requestKey).Scan(&existingID, &existingRoutineID, &existingSource, &existingScheduledAt, &existingRequestedProfile)
	if err == nil {
		sameSchedule := scheduledAt == nil && !existingScheduledAt.Valid
		if scheduledAt != nil && existingScheduledAt.Valid {
			sameSchedule = scheduledAt.UTC().UnixMilli() == existingScheduledAt.Int64
		}
		if existingRoutineID != routineID || existingSource != source || !sameSchedule || existingRequestedProfile.String != requestedProfileID {
			return protocol.WorkDetail{}, false, conflict("request_key_conflict", "request_key was already used with different Work inputs")
		}
		if err := tx.Commit(); err != nil {
			return protocol.WorkDetail{}, false, unavailable(err)
		}
		detail, err := s.Work(ctx, existingID)
		return detail, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkDetail{}, false, unavailable(err)
	}
	snapshot := protocol.RoutineSnapshot{}
	if frozen != nil {
		snapshot = *frozen
		var archived, migrationOnly, readOnly, scheduleEnabled int
		var pendingDue sql.NullInt64
		err := tx.QueryRowContext(ctx, `
			SELECT archived, migration_only, read_only, schedule_enabled, pending_due_at
			FROM routines WHERE id = ?
		`, routineID).Scan(&archived, &migrationOnly, &readOnly, &scheduleEnabled, &pendingDue)
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.WorkDetail{}, false, ErrNotFound
		}
		if err != nil {
			return protocol.WorkDetail{}, false, unavailable(err)
		}
		if readOnly != 0 {
			return protocol.WorkDetail{}, false, conflict("routine_read_only", "historical Routine revisions cannot start Work")
		}
		if archived != 0 || migrationOnly != 0 {
			return protocol.WorkDetail{}, false, conflict("routine_archived", "archived Routines cannot start Work")
		}
		if scheduleEnabled == 0 {
			return protocol.WorkDetail{}, false, conflict("routine_schedule_disabled", "disabled Routine schedules cannot start Work")
		}
		if scheduledAt == nil || !pendingDue.Valid || pendingDue.Int64 != scheduledAt.UTC().UnixMilli() {
			return protocol.WorkDetail{}, false, conflict("routine_occurrence_changed", "the scheduled Routine occurrence changed")
		}
	} else {
		var archived, migrationOnly, readOnly int
		err := tx.QueryRowContext(ctx, `
			SELECT id, name, prompt, runtime, COALESCE(execution_profile_id, ''), timeout_seconds, concurrency_limit,
			       generation, archived, migration_only, read_only
			FROM routines WHERE id = ?
		`, routineID).Scan(&snapshot.ID, &snapshot.Name, &snapshot.Prompt, &snapshot.Runtime,
			&snapshot.ExecutionProfileID, &snapshot.TimeoutSeconds, &snapshot.ConcurrencyLimit, &snapshot.Generation, &archived, &migrationOnly, &readOnly)
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.WorkDetail{}, false, ErrNotFound
		}
		if err != nil {
			return protocol.WorkDetail{}, false, unavailable(err)
		}
		if readOnly != 0 {
			return protocol.WorkDetail{}, false, conflict("routine_read_only", "historical Routine revisions cannot start Work")
		}
		if archived != 0 || migrationOnly != 0 {
			return protocol.WorkDetail{}, false, conflict("routine_archived", "archived Routines cannot start Work")
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT repository.id, repository.remote_identity
			FROM routine_repositories selected
			JOIN repositories repository ON repository.id = selected.repository_id
			WHERE selected.routine_id = ? ORDER BY selected.position
		`, routineID)
		if err != nil {
			return protocol.WorkDetail{}, false, unavailable(err)
		}
		for rows.Next() {
			var repository protocol.RoutineRepository
			if err := rows.Scan(&repository.ID, &repository.RemoteIdentity); err != nil {
				rows.Close()
				return protocol.WorkDetail{}, false, unavailable(err)
			}
			snapshot.Repositories = append(snapshot.Repositories, repository)
		}
		if err := rows.Close(); err != nil {
			return protocol.WorkDetail{}, false, unavailable(err)
		}
	}
	if len(snapshot.Repositories) == 0 {
		return protocol.WorkDetail{}, false, conflict("routine_has_no_repositories", "select at least one repository before running this Routine")
	}
	execution, profileReady, profileBlockedReason, err := loadExecutionSnapshot(ctx, tx, snapshot, requestedProfileID)
	if err != nil {
		return protocol.WorkDetail{}, false, err
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return protocol.WorkDetail{}, false, unavailable(err)
	}
	digestBody, _ := json.Marshal(struct {
		RequestKey  string                     `json:"request_key"`
		RoutineID   string                     `json:"routine_id"`
		Source      string                     `json:"source"`
		ScheduledAt *time.Time                 `json:"scheduled_at,omitempty"`
		Snapshot    protocol.RoutineSnapshot   `json:"snapshot"`
		Execution   protocol.ExecutionSnapshot `json:"execution"`
	}{requestKey, routineID, source, scheduledAt, snapshot, execution})
	digestArray := sha256.Sum256(digestBody)
	digest := digestArray[:]
	if err := validateFrozenRepositories(ctx, tx, snapshot.Repositories); err != nil {
		return protocol.WorkDetail{}, false, err
	}
	workID, err := newID()
	if err != nil {
		return protocol.WorkDetail{}, false, unavailable(err)
	}
	now := s.now().UnixMilli()
	var scheduled any
	if scheduledAt != nil {
		scheduled = scheduledAt.UTC().UnixMilli()
	}
	executionJSON, err := json.Marshal(execution)
	if err != nil {
		return protocol.WorkDetail{}, false, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO work(id, request_key, request_digest, routine_id, routine_snapshot, source,
		                 scheduled_at, requested_execution_profile_id, execution_snapshot, admitted_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, workID, requestKey, digest, routineID, snapshotJSON, source, scheduled,
		nullableString(requestedProfileID), executionJSON, now, now); err != nil {
		return protocol.WorkDetail{}, false, unavailable(err)
	}
	resolvedPrompt := snapshot.Prompt
	if source == "schedule" && scheduledAt != nil {
		cron, timezone := snapshot.ScheduleCron, snapshot.ScheduleTimezone
		if cron == "" || timezone == "" {
			if err := tx.QueryRowContext(ctx, `SELECT cron, timezone FROM routines WHERE id = ?`, routineID).Scan(&cron, &timezone); err != nil {
				return protocol.WorkDetail{}, false, unavailable(err)
			}
		}
		resolvedPrompt, err = protocol.ResolveRoutineSchedulePrompt(snapshot.Prompt, *scheduledAt, cron, timezone)
		if err != nil {
			return protocol.WorkDetail{}, false, unavailable(err)
		}
	}
	if len([]byte(resolvedPrompt)) > protocol.MaxResolvedPromptBytes {
		return protocol.WorkDetail{}, false, conflict("resolved_prompt_too_large", "the frozen Routine prompt exceeds 64 KiB")
	}
	materialized := 0
	for _, repository := range snapshot.Repositories {
		if !protocol.AgentPromptFits(snapshot.Name, repository.RemoteIdentity, resolvedPrompt) {
			return protocol.WorkDetail{}, false, conflict("agent_prompt_too_large", "the frozen Routine prompt cannot fit the Worker request")
		}
		targetID, err := newID()
		if err != nil {
			return protocol.WorkDetail{}, false, unavailable(err)
		}
		state, blockedReason := "blocked", routineConcurrencyBlockedReason
		var assigned any
		var selection workRouteCandidate
		if !profileReady {
			blockedReason = profileBlockedReason
		} else if materialized < snapshot.ConcurrencyLimit {
			if execution.Backend == protocol.BackendPersistent {
				selection, err = s.selectWorkTargetRoute(ctx, tx, repository.ID, repository.RemoteIdentity, now, "", snapshot.Runtime)
				blockedReason = "Waiting for a healthy compatible Worker with repository access."
				if err == nil {
					state, blockedReason, assigned = "queued", "", selection.workerID
					materialized++
				} else if !serviceErrorCode(err, "no_eligible_worker") {
					return protocol.WorkDetail{}, false, err
				}
			} else {
				state, blockedReason, assigned = "queued", "", syntheticWorkerID(execution.ProfileID)
				selection.workerID = assigned.(string)
				materialized++
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO work_targets(
				id, work_id, repository_id, repository_identity, resolved_prompt, required_runtime,
				timeout_seconds, state, blocked_reason, assigned_worker_id, admitted_at,
				execution_profile_id, execution_profile_version, execution_backend, execution_provider,
				execution_model, resource_class, commit_resolution_policy
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, targetID, workID, repository.ID, repository.RemoteIdentity, resolvedPrompt, snapshot.Runtime,
			execution.TimeoutSeconds, state, nullableString(blockedReason), assigned, now,
			execution.ProfileID, execution.ProfileVersion, execution.Backend, execution.Provider,
			execution.Model, execution.ResourceClass, execution.CommitResolutionPolicy); err != nil {
			return protocol.WorkDetail{}, false, unavailable(err)
		}
		if state == "queued" {
			executionID, err := newID()
			if err != nil {
				return protocol.WorkDetail{}, false, unavailable(err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO executions(id, work_target_id, assigned_worker_id, required_runtime, state, created_at, updated_at)
				VALUES (?, ?, ?, ?, 'queued', ?, ?)
			`, executionID, targetID, selection.workerID, snapshot.Runtime, now, now); err != nil {
				return protocol.WorkDetail{}, false, unavailable(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return protocol.WorkDetail{}, false, unavailable(err)
	}
	detail, err := s.Work(ctx, workID)
	return detail, true, err
}

func loadExecutionSnapshot(
	ctx context.Context,
	tx *sql.Tx,
	routine protocol.RoutineSnapshot,
	requestedProfileID string,
) (protocol.ExecutionSnapshot, bool, string, error) {
	profileID := requestedProfileID
	if profileID == "" {
		profileID = routine.ExecutionProfileID
	}
	if profileID == "" || profileID == protocol.PersistentAutoProfileID {
		return protocol.ExecutionSnapshot{
			ProfileID: protocol.PersistentAutoProfileID, ProfileVersion: 1,
			Backend: protocol.BackendPersistent, Runtime: routine.Runtime,
			Provider: "worker", Model: "worker-default", TimeoutSeconds: routine.TimeoutSeconds,
			ResourceClass: "worker", CommitResolutionPolicy: protocol.CommitResolvePerAttempt,
		}, true, "", nil
	}
	var snapshot protocol.ExecutionSnapshot
	var enabled, healthy int
	var reason string
	err := tx.QueryRowContext(ctx, `
		SELECT p.id, p.current_version, v.kind, v.runtime, v.provider, v.model,
		       v.timeout_seconds, v.resource_class, v.commit_resolution_policy,
		       p.enabled, p.healthy, p.health_reason
		FROM execution_profiles p
		JOIN execution_profile_versions v ON v.profile_id = p.id AND v.version = p.current_version
		WHERE p.id = ?
	`, profileID).Scan(&snapshot.ProfileID, &snapshot.ProfileVersion, &snapshot.Backend,
		&snapshot.Runtime, &snapshot.Provider, &snapshot.Model, &snapshot.TimeoutSeconds,
		&snapshot.ResourceClass, &snapshot.CommitResolutionPolicy, &enabled, &healthy, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, false, "", invalid("execution_profile_not_found", "the selected execution profile does not exist")
	}
	if err != nil {
		return snapshot, false, "", unavailable(err)
	}
	if snapshot.Runtime != routine.Runtime {
		return snapshot, false, fmt.Sprintf("Execution profile %s does not support runtime %s.", profileID, routine.Runtime), nil
	}
	if enabled == 0 {
		return snapshot, false, fmt.Sprintf("Execution profile %s is disabled.", profileID), nil
	}
	if healthy == 0 {
		if reason == "" {
			reason = "health validation has not passed"
		}
		return snapshot, false, fmt.Sprintf("Execution profile %s is unhealthy: %s", profileID, reason), nil
	}
	return snapshot, true, "", nil
}

func validateFrozenRepositories(ctx context.Context, tx *sql.Tx, repositories []protocol.RoutineRepository) error {
	for _, repository := range repositories {
		var currentIdentity string
		var enabled, centrallyManaged, advertised int
		err := tx.QueryRowContext(ctx, `
			SELECT current.remote_identity, current.enabled, current.centrally_managed,
			       EXISTS (SELECT 1 FROM worker_repositories available
			               WHERE available.repository_id = current.id AND available.advertised = 1)
			FROM repositories current WHERE current.id = ?
		`, repository.ID).Scan(&currentIdentity, &enabled, &centrallyManaged, &advertised)
		if errors.Is(err, sql.ErrNoRows) {
			return conflict("routine_repository_missing", fmt.Sprintf("repository %s no longer exists", repository.RemoteIdentity))
		}
		if err != nil {
			return unavailable(err)
		}
		if centrallyManaged != 0 && enabled == 0 || centrallyManaged == 0 && advertised == 0 {
			return conflict("routine_repository_unavailable", fmt.Sprintf("repository %s is disabled or unavailable", repository.RemoteIdentity))
		}
	}
	return nil
}

func encodeWorkCursor(admittedAt int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%s", admittedAt, id)))
}

func decodeWorkCursor(value string) (int64, string, error) {
	if value == "" {
		return 0, "", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, "", invalid("invalid_cursor", "cursor is invalid")
	}
	var admitted int64
	var id string
	if _, err := fmt.Sscanf(string(decoded), "%d:%s", &admitted, &id); err != nil || id == "" {
		return 0, "", invalid("invalid_cursor", "cursor is invalid")
	}
	return admitted, id, nil
}

func (s *Store) WorkPage(ctx context.Context, limit int, cursor string) (protocol.WorkPage, error) {
	if limit == 0 {
		limit = defaultRoutinePageSize
	}
	if limit < 1 || limit > maxRoutinePageSize {
		return protocol.WorkPage{}, invalid("invalid_limit", "limit must be between 1 and 200")
	}
	admitted, cursorID, err := decodeWorkCursor(cursor)
	if err != nil {
		return protocol.WorkPage{}, err
	}
	query := `SELECT id, admitted_at FROM work WHERE 1 = 1`
	args := make([]any, 0, 5)
	if admitted != 0 {
		query += ` AND (admitted_at < ? OR (admitted_at = ? AND id < ?))`
		args = append(args, admitted, admitted, cursorID)
	}
	query += ` ORDER BY admitted_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return protocol.WorkPage{}, unavailable(err)
	}
	type key struct {
		id       string
		admitted int64
	}
	var keys []key
	for rows.Next() {
		var value key
		if err := rows.Scan(&value.id, &value.admitted); err != nil {
			rows.Close()
			return protocol.WorkPage{}, unavailable(err)
		}
		keys = append(keys, value)
	}
	if err := rows.Close(); err != nil {
		return protocol.WorkPage{}, unavailable(err)
	}
	hasMore := len(keys) > limit
	if hasMore {
		keys = keys[:limit]
	}
	page := protocol.WorkPage{Work: make([]protocol.Work, 0, len(keys))}
	for _, key := range keys {
		detail, err := s.Work(ctx, key.id)
		if err != nil {
			return protocol.WorkPage{}, err
		}
		detail.Work.Routine.Prompt = ""
		detail.Work.Routine.TimeoutSeconds = 0
		detail.Work.Routine.ConcurrencyLimit = 0
		detail.Work.Routine.Repositories = nil
		page.Work = append(page.Work, detail.Work)
	}
	if hasMore {
		last := keys[len(keys)-1]
		page.NextCursor = encodeWorkCursor(last.admitted, last.id)
	}
	return page, nil
}

func (s *Store) Work(ctx context.Context, id string) (protocol.WorkDetail, error) {
	var detail protocol.WorkDetail
	var snapshot, executionSnapshot []byte
	var scheduledAt, terminalAt sql.NullInt64
	var providerSnapshot sql.NullString
	var admittedAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, routine_id, routine_snapshot, execution_snapshot, source, scheduled_at, provider_snapshot,
		       admitted_at, updated_at, terminal_at
		FROM work WHERE id = ?
	`, id).Scan(&detail.Work.ID, &detail.Work.RoutineID, &snapshot, &executionSnapshot, &detail.Work.Source,
		&scheduledAt, &providerSnapshot, &admittedAt, &updatedAt, &terminalAt)
	if errors.Is(err, sql.ErrNoRows) {
		return detail, ErrNotFound
	}
	if err != nil {
		return detail, unavailable(err)
	}
	if err := json.Unmarshal(snapshot, &detail.Work.Routine); err != nil {
		return detail, unavailable(err)
	}
	if err := json.Unmarshal(executionSnapshot, &detail.Work.Execution); err != nil {
		return detail, unavailable(err)
	}
	if providerSnapshot.Valid {
		detail.ProviderSnapshot = json.RawMessage(providerSnapshot.String)
	}
	detail.Work.AdmittedAt, detail.Work.UpdatedAt = fromMillis(admittedAt), fromMillis(updatedAt)
	if scheduledAt.Valid {
		value := fromMillis(scheduledAt.Int64)
		detail.Work.ScheduledAt = &value
	}
	if terminalAt.Valid {
		value := fromMillis(terminalAt.Int64)
		detail.Work.TerminalAt = &value
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT target.id, target.work_id, target.repository_id, target.repository_identity,
		       target.resolved_prompt, target.required_runtime, target.timeout_seconds,
		       target.execution_profile_id, target.execution_profile_version, target.execution_backend,
		       target.execution_provider, target.execution_model, target.resource_class, target.commit_resolution_policy,
		       target.state, target.blocked_reason, target.assigned_worker_id,
		       target.cancellation_requested, target.retry_may_repeat_effects,
		       target.admitted_at, target.started_at, target.terminal_at, target.result, target.failure_reason
		FROM work_targets target WHERE target.work_id = ? ORDER BY target.admitted_at, target.id
	`, id)
	if err != nil {
		return detail, unavailable(err)
	}
	for rows.Next() {
		var target protocol.WorkTarget
		var blockedReason, workerID, result, failure sql.NullString
		var cancellation, retry int
		var admitted int64
		var started, terminal sql.NullInt64
		if err := rows.Scan(&target.ID, &target.WorkID, &target.RepositoryID, &target.RepositoryIdentity,
			&target.ResolvedPrompt, &target.RequiredRuntime, &target.TimeoutSeconds,
			&target.Execution.ProfileID, &target.Execution.ProfileVersion, &target.Execution.Backend,
			&target.Execution.Provider, &target.Execution.Model, &target.Execution.ResourceClass,
			&target.Execution.CommitResolutionPolicy, &target.State,
			&blockedReason, &workerID, &cancellation, &retry, &admitted, &started, &terminal, &result, &failure); err != nil {
			rows.Close()
			return detail, unavailable(err)
		}
		target.BlockedReason, target.AssignedWorkerID = blockedReason.String, workerID.String
		target.Execution.Runtime, target.Execution.TimeoutSeconds = target.RequiredRuntime, target.TimeoutSeconds
		target.CancellationRequested, target.RetryMayRepeatEffects = cancellation != 0, retry != 0
		target.AdmittedAt, target.Result, target.FailureReason = fromMillis(admitted), result.String, failure.String
		if started.Valid {
			value := fromMillis(started.Int64)
			target.StartedAt = &value
		}
		if terminal.Valid {
			value := fromMillis(terminal.Int64)
			target.TerminalAt = &value
		}
		detail.Targets = append(detail.Targets, target)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return detail, unavailable(err)
	}
	if err := rows.Close(); err != nil {
		return detail, unavailable(err)
	}
	for index := range detail.Targets {
		target := &detail.Targets[index]
		attemptRows, err := s.db.QueryContext(ctx, `
			SELECT attempt.id, attempt.execution_id, attempt.worker_id, attempt.attempt_number, attempt.state,
			       attempt.lease_expires_at, attempt.supervisor_pid, attempt.process_identity, attempt.process_group_id,
			       attempt.result, attempt.error, attempt.started_at, attempt.completed_at, attempt.created_at
			FROM attempts attempt JOIN executions execution ON execution.id = attempt.execution_id
			WHERE execution.work_target_id = ? ORDER BY attempt.attempt_number
		`, target.ID)
		if err != nil {
			return detail, unavailable(err)
		}
		for attemptRows.Next() {
			attempt, err := scanAttempt(attemptRows)
			if err != nil {
				attemptRows.Close()
				return detail, unavailable(err)
			}
			target.Attempts = append(target.Attempts, attempt)
		}
		if err := attemptRows.Err(); err != nil {
			attemptRows.Close()
			return detail, unavailable(err)
		}
		if err := attemptRows.Close(); err != nil {
			return detail, unavailable(err)
		}
	}
	applyWorkAggregate(&detail.Work, detail.Targets, s.now())
	return detail, nil
}

func applyWorkAggregate(work *protocol.Work, targets []protocol.WorkTarget, now time.Time) {
	work.TargetCount = len(targets)
	work.SucceededCount, work.FailedCount, work.CancelledCount, work.ActiveCount = 0, 0, 0, 0
	blocked, actionableBlocked, queued, running := 0, 0, 0, 0
	for _, target := range targets {
		switch target.State {
		case protocol.WorkTargetBlocked:
			blocked++
			if target.BlockedReason != routineConcurrencyBlockedReason {
				actionableBlocked++
			}
		case protocol.WorkTargetQueued:
			queued++
		case protocol.WorkTargetPreparing, protocol.WorkTargetRunning:
			running++
		case protocol.WorkTargetSucceeded:
			work.SucceededCount++
		case protocol.WorkTargetFailed:
			work.FailedCount++
		case protocol.WorkTargetCancelled:
			work.CancelledCount++
		}
	}
	work.ActiveCount = blocked + queued + running
	if work.ActiveCount > 0 {
		switch {
		case running > 0 || work.SucceededCount+work.FailedCount+work.CancelledCount > 0:
			work.State = protocol.WorkRunning
		case blocked == work.ActiveCount:
			work.State = protocol.WorkBlocked
		default:
			work.State = protocol.WorkQueued
		}
	} else {
		switch {
		case work.SucceededCount == work.TargetCount:
			work.State = protocol.WorkSucceeded
		case work.SucceededCount == 0 && work.FailedCount > 0:
			work.State = protocol.WorkFailed
		case work.CancelledCount == work.TargetCount:
			work.State = protocol.WorkCancelled
		default:
			work.State = protocol.WorkPartial
		}
	}
	work.NeedsAttention = actionableBlocked > 0 || ((work.State == protocol.WorkFailed || work.State == protocol.WorkPartial) &&
		work.TerminalAt != nil && work.TerminalAt.After(now.Add(-24*time.Hour)))
}

func (s *Store) CancelWork(ctx context.Context, workID string) (protocol.WorkDetail, error) {
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM work WHERE id = ?`, workID).Scan(&exists); err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	if exists == 0 {
		return protocol.WorkDetail{}, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE work_targets SET
			state = CASE WHEN state IN ('blocked','queued') THEN 'cancelled' ELSE state END,
			cancellation_requested = CASE WHEN state IN ('preparing','running') THEN 1 ELSE cancellation_requested END,
			terminal_at = CASE WHEN state IN ('blocked','queued') THEN ? ELSE terminal_at END
		WHERE work_id = ? AND state IN ('blocked','queued','preparing','running')
	`, now, workID); err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE executions SET
			state = CASE WHEN state = 'queued' THEN 'cancelled' ELSE state END,
			cancellation_requested = CASE WHEN state IN ('preparing','running') THEN 1 ELSE cancellation_requested END,
			updated_at = ?
		WHERE work_target_id IN (SELECT id FROM work_targets WHERE work_id = ?)
		  AND state IN ('queued','preparing','running')
	`, now, workID); err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE work SET updated_at = ?, terminal_at = CASE
			WHEN NOT EXISTS (
				SELECT 1 FROM work_targets target WHERE target.work_id = work.id
				  AND target.state IN ('blocked','queued','preparing','running')
			) THEN ? ELSE NULL END
		WHERE id = ?
	`, now, now, workID); err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	return s.Work(ctx, workID)
}

func (s *Store) CancelWorkTarget(ctx context.Context, workID, targetID string) (protocol.WorkDetail, error) {
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRowContext(ctx, `
		SELECT state FROM work_targets WHERE id = ? AND work_id = ?
	`, targetID, workID).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkDetail{}, ErrNotFound
	} else if err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	if state != "blocked" && state != "queued" && state != "preparing" && state != "running" {
		return protocol.WorkDetail{}, conflict("target_cancel_not_allowed", "only active Targets can be cancelled")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE work_targets SET
			state = CASE WHEN state IN ('blocked','queued') THEN 'cancelled' ELSE state END,
			cancellation_requested = CASE WHEN state IN ('preparing','running') THEN 1 ELSE cancellation_requested END,
			terminal_at = CASE WHEN state IN ('blocked','queued') THEN ? ELSE terminal_at END
		WHERE id = ? AND work_id = ?
	`, now, targetID, workID); err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE executions SET
			state = CASE WHEN state = 'queued' THEN 'cancelled' ELSE state END,
			cancellation_requested = CASE WHEN state IN ('preparing','running') THEN 1 ELSE cancellation_requested END,
			updated_at = ?
		WHERE work_target_id = ? AND state IN ('queued','preparing','running')
	`, now, targetID); err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE work SET updated_at = ?, terminal_at = CASE
			WHEN NOT EXISTS (
				SELECT 1 FROM work_targets target WHERE target.work_id = work.id
				  AND target.state IN ('blocked','queued','preparing','running')
			) THEN ? ELSE NULL END
		WHERE id = ?
	`, now, now, workID); err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	return s.Work(ctx, workID)
}

func (s *Store) RetryWorkTarget(ctx context.Context, expectedWorkID, targetID string) (protocol.WorkDetail, error) {
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	defer tx.Rollback()
	var workID, state, repositoryID, identity, runtime, backend, profileID string
	var profileVersion int
	err = tx.QueryRowContext(ctx, `
		SELECT work_id, state, repository_id, repository_identity, required_runtime,
		       execution_backend, execution_profile_id, execution_profile_version
		FROM work_targets WHERE id = ? AND work_id = ?
	`, targetID, expectedWorkID).Scan(&workID, &state, &repositoryID, &identity, &runtime,
		&backend, &profileID, &profileVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkDetail{}, ErrNotFound
	}
	if err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	if state != "failed" && state != "cancelled" {
		return protocol.WorkDetail{}, conflict("target_retry_not_allowed", "only failed or cancelled Targets can be retried")
	}
	var activeTargets, concurrencyLimit int
	if err := tx.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM work_targets active
			 WHERE active.work_id = work.id AND active.state IN ('queued','preparing','running')),
			json_extract(work.routine_snapshot, '$.concurrency_limit')
		FROM work WHERE id = ?
	`, workID).Scan(&activeTargets, &concurrencyLimit); err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	if activeTargets >= concurrencyLimit {
		return protocol.WorkDetail{}, conflict("routine_concurrency_full", "retry this Target after another active Target finishes")
	}
	assignedWorkerID := ""
	if backend == protocol.BackendPersistent {
		selection, err := s.selectWorkTargetRoute(ctx, tx, repositoryID, identity, now, "", runtime)
		if err != nil {
			return protocol.WorkDetail{}, err
		}
		assignedWorkerID = selection.workerID
	} else {
		var available int
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM execution_profile_versions version
				JOIN workers worker ON worker.id = ? AND worker.synthetic = 1
				WHERE version.profile_id = ? AND version.version = ?
			)
		`, syntheticWorkerID(profileID), profileID, profileVersion).Scan(&available); err != nil {
			return protocol.WorkDetail{}, unavailable(err)
		}
		if available == 0 {
			return protocol.WorkDetail{}, conflict("execution_profile_version_unavailable", "the frozen execution profile version is unavailable")
		}
		assignedWorkerID = syntheticWorkerID(profileID)
	}
	var executionID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM executions WHERE work_target_id = ?`, targetID).Scan(&executionID)
	if errors.Is(err, sql.ErrNoRows) {
		executionID, err = newID()
		if err != nil {
			return protocol.WorkDetail{}, unavailable(err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO executions(id, work_target_id, assigned_worker_id, required_runtime, state,
			                       cancellation_requested, created_at, updated_at, retry_count)
			VALUES (?, ?, ?, ?, 'queued', 0, ?, ?, 1)
		`, executionID, targetID, assignedWorkerID, runtime, now, now)
	} else if err == nil {
		_, err = tx.ExecContext(ctx, `
			UPDATE executions SET assigned_worker_id = ?, state = 'queued', cancellation_requested = 0,
			       retry_count = retry_count + 1, updated_at = ? WHERE id = ?
		`, assignedWorkerID, now, executionID)
	}
	if err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE work_targets SET state = 'queued', blocked_reason = NULL, assigned_worker_id = ?,
		       cancellation_requested = 0, retry_may_repeat_effects = 1,
		       started_at = NULL, terminal_at = NULL, result = NULL, failure_reason = NULL
		WHERE id = ?
	`, assignedWorkerID, targetID); err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work SET updated_at = ?, terminal_at = NULL WHERE id = ?`, now, workID); err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	return s.Work(ctx, workID)
}

func (s *Store) Overview(ctx context.Context) (protocol.Overview, error) {
	page, err := s.WorkPage(ctx, 10, "")
	if err != nil {
		return protocol.Overview{}, err
	}
	result := protocol.Overview{
		GeneratedAt: s.now().UTC(),
		RunMetrics:  protocol.OverviewRunMetrics{Window: "24h"},
	}
	result.RecentWork = page.Work
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(EXISTS (
				SELECT 1 FROM work_targets target
				WHERE target.work_id = work.id AND target.state IN ('blocked','queued','preparing','running')
			)), 0),
			COALESCE(SUM(
				(terminal_at IS NULL AND EXISTS (
					SELECT 1 FROM work_targets target
					WHERE target.work_id = work.id AND target.state = 'blocked'
					  AND COALESCE(target.blocked_reason, '') != ?
				)) OR
				(terminal_at >= ? AND EXISTS (
					SELECT 1 FROM work_targets target
					WHERE target.work_id = work.id AND target.state = 'failed'
				))
			), 0),
			COALESCE(SUM(terminal_at >= ?), 0)
		FROM work
	`, routineConcurrencyBlockedReason, result.GeneratedAt.Add(-24*time.Hour).UnixMilli(),
		result.GeneratedAt.Add(-24*time.Hour).UnixMilli()).Scan(
		&result.ActiveWork, &result.NeedsAttention, &result.CompletedLast24H); err != nil {
		return result, unavailable(err)
	}
	var averageQueueMillis, averageCycleMillis sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `
		WITH recent_work AS (
			SELECT work.id, work.admitted_at, work.terminal_at,
			       (SELECT MIN(attempt.started_at)
			        FROM work_targets target
			        JOIN executions execution ON execution.work_target_id = target.id
			        JOIN attempts attempt ON attempt.execution_id = execution.id
			        WHERE target.work_id = work.id AND attempt.started_at IS NOT NULL) AS first_started_at
			FROM work
			WHERE work.admitted_at >= ? AND work.admitted_at <= ?
		)
		SELECT COUNT(*),
		       COALESCE(SUM(terminal_at IS NOT NULL), 0),
		       AVG(CASE WHEN first_started_at IS NOT NULL
		                THEN MAX(0, first_started_at - admitted_at) END),
		       AVG(CASE WHEN terminal_at IS NOT NULL
		                THEN MAX(0, terminal_at - admitted_at) END)
		FROM recent_work
	`, result.GeneratedAt.Add(-24*time.Hour).UnixMilli(), result.GeneratedAt.UnixMilli()).Scan(
		&result.RunMetrics.TotalRuns, &result.RunMetrics.CompletedRuns,
		&averageQueueMillis, &averageCycleMillis); err != nil {
		return result, unavailable(err)
	}
	if result.RunMetrics.TotalRuns > 0 {
		completionRate := float64(result.RunMetrics.CompletedRuns) / float64(result.RunMetrics.TotalRuns)
		result.RunMetrics.CompletionRate = &completionRate
	}
	if averageQueueMillis.Valid {
		averageQueueSeconds := averageQueueMillis.Float64 / float64(time.Second/time.Millisecond)
		result.RunMetrics.AverageQueueTimeSeconds = &averageQueueSeconds
	}
	if averageCycleMillis.Valid {
		averageCycleSeconds := averageCycleMillis.Float64 / float64(time.Second/time.Millisecond)
		result.RunMetrics.AverageCycleTimeSeconds = &averageCycleSeconds
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(
			(synthetic = 1 AND health = 'healthy') OR
			(synthetic = 0 AND last_heartbeat >= ?)
		), 0), COUNT(*) FROM workers
	`, result.GeneratedAt.Add(-protocol.WorkerOnlineWindow).UnixMilli()).Scan(&result.WorkersOnline, &result.WorkersTotal); err != nil {
		return result, unavailable(err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM routines
		WHERE migration_only = 0 AND archived = 0 AND schedule_enabled = 1 AND next_due_at IS NOT NULL
		ORDER BY next_due_at, id LIMIT 5
	`)
	if err != nil {
		return result, unavailable(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return result, unavailable(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return result, unavailable(err)
	}
	for _, id := range ids {
		routine, err := s.Routine(ctx, id)
		if err != nil {
			return result, err
		}
		result.UpcomingRoutines = append(result.UpcomingRoutines, routineSummary(routine))
	}
	return result, nil
}
