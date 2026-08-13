package protocol

import (
	"encoding/json"
	"time"
)

const (
	MaxRoutines            = 500
	MaxRoutineRepositories = 100
	MaxRoutinePromptBytes  = 64 * 1024
)

type RoutineSchedule struct {
	Enabled       bool       `json:"enabled"`
	Cron          string     `json:"cron,omitempty"`
	Timezone      string     `json:"timezone,omitempty"`
	NextDueAt     *time.Time `json:"next_due_at,omitempty"`
	PendingDueAt  *time.Time `json:"pending_due_at,omitempty"`
	HealthStatus  string     `json:"health_status"`
	HealthCode    string     `json:"health_code,omitempty"`
	HealthMessage string     `json:"health_message,omitempty"`
}

type RoutineRepository struct {
	ID             string `json:"id"`
	RemoteIdentity string `json:"remote_identity"`
}

type Routine struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	Prompt           string              `json:"prompt,omitempty"`
	PromptPreview    string              `json:"prompt_preview,omitempty"`
	Runtime          string              `json:"runtime"`
	TimeoutSeconds   int                 `json:"timeout_seconds"`
	ConcurrencyLimit int                 `json:"concurrency_limit"`
	Generation       int                 `json:"generation"`
	Archived         bool                `json:"archived"`
	ReadOnly         bool                `json:"read_only"`
	Repositories     []RoutineRepository `json:"repositories"`
	RepositoryCount  int                 `json:"repository_count"`
	Schedule         RoutineSchedule     `json:"schedule"`
	LastWorkState    string              `json:"last_work_state,omitempty"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
}

// WorkExecution is the worker-facing execution record for one Work target.
type WorkExecution struct {
	ID                    string    `json:"id"`
	WorkTargetID          string    `json:"work_target_id"`
	AssignedWorkerID      string    `json:"assigned_worker_id"`
	RequiredRuntime       string    `json:"required_runtime"`
	State                 string    `json:"state"`
	CancellationRequested bool      `json:"cancellation_requested"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// ClaimedWorkTarget contains the immutable input a Worker needs to execute a
// single repository target. It intentionally uses only the Routines model.
type ClaimedWorkTarget struct {
	ID              string    `json:"id"`
	WorkID          string    `json:"work_id"`
	RoutineName     string    `json:"routine_name"`
	Prompt          string    `json:"prompt"`
	WorkerID        string    `json:"worker_id"`
	RepositoryID    string    `json:"repository_id"`
	RequiredRuntime string    `json:"required_runtime"`
	TimeoutSeconds  int       `json:"timeout_seconds"`
	State           string    `json:"state"`
	AdmittedAt      time.Time `json:"admitted_at"`
}

type RoutinePage struct {
	Routines   []Routine `json:"routines"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

type SaveRoutineRequest struct {
	RequestKey         string          `json:"request_key,omitempty"`
	Name               string          `json:"name"`
	Prompt             string          `json:"prompt"`
	Runtime            string          `json:"runtime"`
	TimeoutSeconds     int             `json:"timeout_seconds"`
	ConcurrencyLimit   int             `json:"concurrency_limit"`
	RepositoryIDs      []string        `json:"repository_ids"`
	Schedule           RoutineSchedule `json:"schedule"`
	ExpectedGeneration int             `json:"expected_generation,omitempty"`
}

type SetRoutineArchivedRequest struct {
	Archived           *bool `json:"archived"`
	ExpectedGeneration int   `json:"expected_generation"`
}

type RunRoutineRequest struct {
	RequestKey string `json:"request_key"`
}

type DiscardRoutineOccurrenceRequest struct {
	PendingDueAt time.Time `json:"pending_due_at"`
}

type WorkTargetState string

const (
	WorkTargetBlocked   WorkTargetState = "blocked"
	WorkTargetQueued    WorkTargetState = "queued"
	WorkTargetPreparing WorkTargetState = "preparing"
	WorkTargetRunning   WorkTargetState = "running"
	WorkTargetSucceeded WorkTargetState = "succeeded"
	WorkTargetFailed    WorkTargetState = "failed"
	WorkTargetCancelled WorkTargetState = "cancelled"
)

type WorkState string

const (
	WorkQueued    WorkState = "queued"
	WorkBlocked   WorkState = "blocked"
	WorkRunning   WorkState = "running"
	WorkSucceeded WorkState = "succeeded"
	WorkFailed    WorkState = "failed"
	WorkPartial   WorkState = "partial"
	WorkCancelled WorkState = "cancelled"
)

type RoutineSnapshot struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	Prompt           string              `json:"prompt,omitempty"`
	Runtime          string              `json:"runtime"`
	TimeoutSeconds   int                 `json:"timeout_seconds,omitempty"`
	ConcurrencyLimit int                 `json:"concurrency_limit,omitempty"`
	Generation       int                 `json:"generation"`
	Repositories     []RoutineRepository `json:"repositories,omitempty"`
	ScheduleCron     string              `json:"cron,omitempty"`
	ScheduleTimezone string              `json:"timezone,omitempty"`
}

type WorkTarget struct {
	ID                    string          `json:"id"`
	WorkID                string          `json:"work_id"`
	RepositoryID          string          `json:"repository_id"`
	RepositoryIdentity    string          `json:"repository_identity"`
	ResolvedPrompt        string          `json:"resolved_prompt,omitempty"`
	RequiredRuntime       string          `json:"required_runtime"`
	TimeoutSeconds        int             `json:"timeout_seconds"`
	State                 WorkTargetState `json:"state"`
	BlockedReason         string          `json:"blocked_reason,omitempty"`
	AssignedWorkerID      string          `json:"assigned_worker_id,omitempty"`
	CancellationRequested bool            `json:"cancellation_requested"`
	RetryMayRepeatEffects bool            `json:"retry_may_repeat_effects"`
	AdmittedAt            time.Time       `json:"admitted_at"`
	StartedAt             *time.Time      `json:"started_at,omitempty"`
	TerminalAt            *time.Time      `json:"terminal_at,omitempty"`
	Result                string          `json:"result,omitempty"`
	FailureReason         string          `json:"failure_reason,omitempty"`
	Attempts              []Attempt       `json:"attempts,omitempty"`
}

type Work struct {
	ID             string          `json:"id"`
	RoutineID      string          `json:"routine_id"`
	Routine        RoutineSnapshot `json:"routine"`
	Source         string          `json:"source"`
	ScheduledAt    *time.Time      `json:"scheduled_at,omitempty"`
	State          WorkState       `json:"state"`
	NeedsAttention bool            `json:"needs_attention"`
	TargetCount    int             `json:"target_count"`
	SucceededCount int             `json:"succeeded_count"`
	FailedCount    int             `json:"failed_count"`
	CancelledCount int             `json:"cancelled_count"`
	ActiveCount    int             `json:"active_count"`
	AdmittedAt     time.Time       `json:"admitted_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	TerminalAt     *time.Time      `json:"terminal_at,omitempty"`
}

type WorkDetail struct {
	Work             Work            `json:"work"`
	ProviderSnapshot json.RawMessage `json:"provider_snapshot,omitempty"`
	Targets          []WorkTarget    `json:"targets"`
}

type WorkPage struct {
	Work       []Work `json:"work"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type OverviewRunMetrics struct {
	Window                  string   `json:"window"`
	TotalRuns               int      `json:"total_runs"`
	CompletedRuns           int      `json:"completed_runs"`
	CompletionRate          *float64 `json:"completion_rate"`
	AverageQueueTimeSeconds *float64 `json:"average_queue_time_seconds"`
	AverageCycleTimeSeconds *float64 `json:"average_cycle_time_seconds"`
}

type Overview struct {
	ActiveWork       int                `json:"active_work"`
	NeedsAttention   int                `json:"needs_attention"`
	CompletedLast24H int                `json:"completed_last_24h"`
	WorkersOnline    int                `json:"workers_online"`
	WorkersTotal     int                `json:"workers_total"`
	RunMetrics       OverviewRunMetrics `json:"run_metrics"`
	RecentWork       []Work             `json:"recent_work"`
	UpcomingRoutines []Routine          `json:"upcoming_routines"`
	GeneratedAt      time.Time          `json:"generated_at"`
}
