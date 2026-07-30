package protocol

import (
	"encoding/json"
	"time"
)

const (
	RuntimeCodex         = "codex"
	RuntimeClaudeCode    = "claude-code"
	MaxBodyBytes         = 1 << 20
	MaxDescriptionBytes  = 64 << 10
	MaxEventBatchBytes   = 256 << 10
	MaxEventBytes        = 64 << 10
	MaxEventsPerBatch    = 100
	MaxAttemptEventBytes = 10 << 20
	MaxResultBytes       = 256 << 10
	MaxErrorBytes        = 64 << 10
	DefaultTimeout       = 2 * time.Hour
	MaxTimeout           = 8 * time.Hour
	LeaseDuration        = 30 * time.Second
	EmptyClaimTTL        = 5 * time.Minute
	WorkerOnlineWindow   = 30 * time.Second
	MaxRetainedPerRepo   = 10
	DefaultTaskPageSize  = 50
	MaxTaskPageSize      = 200
	DefaultEventPageSize = 100
	MaxEventPageSize     = 500
)

func SupportedRuntime(value string) bool {
	return value == RuntimeCodex || value == RuntimeClaudeCode
}

type RepositoryRegistration struct {
	Key            string `json:"key"`
	RemoteIdentity string `json:"remote_identity"`
	RetainedCount  int    `json:"retained_count"`
}

type RetainedWorktree struct {
	AttemptID      string `json:"attempt_id"`
	RepositoryID   string `json:"repository_id"`
	Path           string `json:"path"`
	Reason         string `json:"reason"`
	CleanupCommand string `json:"cleanup_command"`
}

type WorkerRegistration struct {
	Name                   string                   `json:"name"`
	WorkerVersion          string                   `json:"worker_version"`
	Runtime                string                   `json:"runtime"`
	RuntimeVersion         string                   `json:"runtime_version"`
	Capacity               int                      `json:"capacity"`
	ActiveCount            int                      `json:"active_count"`
	Health                 string                   `json:"health"`
	Repositories           []RepositoryRegistration `json:"repositories"`
	RetainedWorktrees      []RetainedWorktree       `json:"retained_worktrees"`
	CapacityHandoffVersion int                      `json:"capacity_handoff_version,omitempty"`
	DisposedAttemptIDs     []string                 `json:"disposed_attempt_ids,omitempty"`
}

type Repository struct {
	ID             string `json:"id"`
	Key            string `json:"key"`
	RemoteIdentity string `json:"remote_identity"`
	RetainedCount  int    `json:"retained_count"`
}

type Worker struct {
	ID                string             `json:"id"`
	Name              string             `json:"name"`
	WorkerVersion     string             `json:"worker_version"`
	Runtime           string             `json:"runtime"`
	RuntimeVersion    string             `json:"runtime_version"`
	Capacity          int                `json:"capacity"`
	ActiveCount       int                `json:"active_count"`
	Health            string             `json:"health"`
	Online            bool               `json:"online"`
	Repositories      []Repository       `json:"repositories"`
	RetainedWorktrees []RetainedWorktree `json:"retained_worktrees"`
	CurrentTaskTitle  string             `json:"current_task_title,omitempty"`
	RegisteredAt      time.Time          `json:"registered_at"`
	LastHeartbeat     time.Time          `json:"last_heartbeat"`
}

type CreateTaskRequest struct {
	RequestKey     string `json:"request_key"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	WorkerID       string `json:"worker_id"`
	RepositoryID   string `json:"repository_id"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type Task struct {
	ID             string    `json:"id"`
	RequestKey     string    `json:"request_key"`
	Title          string    `json:"title"`
	Description    string    `json:"description,omitempty"`
	WorkerID       string    `json:"worker_id"`
	RepositoryID   string    `json:"repository_id"`
	TimeoutSeconds int       `json:"timeout_seconds"`
	State          string    `json:"state"`
	CreatedAt      time.Time `json:"created_at"`
}

type TaskCursor struct {
	CreatedAtMillis int64
	ID              string
}

type TaskPageRequest struct {
	Limit  int
	Cursor *TaskCursor
}

type TaskPage struct {
	Tasks      []Task
	NextCursor *TaskCursor
}

type Execution struct {
	ID                    string    `json:"id"`
	TaskID                string    `json:"task_id"`
	AssignedWorkerID      string    `json:"assigned_worker_id"`
	RequiredRuntime       string    `json:"required_runtime"`
	State                 string    `json:"state"`
	CancellationRequested bool      `json:"cancellation_requested"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type Attempt struct {
	ID              string     `json:"id"`
	ExecutionID     string     `json:"execution_id"`
	WorkerID        string     `json:"worker_id"`
	AttemptNumber   int        `json:"attempt_number"`
	State           string     `json:"state"`
	LeaseExpiresAt  time.Time  `json:"lease_expires_at"`
	SupervisorPID   *int64     `json:"supervisor_pid,omitempty"`
	ProcessIdentity string     `json:"process_identity,omitempty"`
	ProcessGroupID  *int64     `json:"process_group_id,omitempty"`
	Result          string     `json:"result,omitempty"`
	Error           string     `json:"error,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

type TaskDetail struct {
	Task                Task       `json:"task"`
	Execution           Execution  `json:"execution"`
	Repository          Repository `json:"repository"`
	RepositoryAvailable bool       `json:"repository_available"`
	Attempts            []Attempt  `json:"attempts"`
}

type ClaimRequest struct {
	RequestID  string `json:"request_id"`
	LeaseToken string `json:"lease_token"`
}

type Claim struct {
	Attempt    Attempt    `json:"attempt"`
	Execution  Execution  `json:"execution"`
	Task       Task       `json:"task"`
	Repository Repository `json:"repository"`
}

type LeaseRequest struct {
	LeaseToken string `json:"lease_token"`
}

type StartAttemptRequest struct {
	LeaseToken      string `json:"lease_token"`
	SupervisorPID   *int64 `json:"supervisor_pid,omitempty"`
	ProcessIdentity string `json:"process_identity,omitempty"`
	ProcessGroupID  *int64 `json:"process_group_id,omitempty"`
}

type HeartbeatResponse struct {
	LeaseExpiresAt        time.Time `json:"lease_expires_at"`
	CancellationRequested bool      `json:"cancellation_requested"`
}

type AttemptEvent struct {
	Sequence   int64           `json:"sequence"`
	Kind       string          `json:"kind"`
	Payload    json.RawMessage `json:"payload"`
	ServerTime time.Time       `json:"server_time,omitempty"`
}

type AttemptEventPage struct {
	Events    []AttemptEvent
	NextAfter int64
	HasMore   bool
}

type EventBatchRequest struct {
	LeaseToken string         `json:"lease_token"`
	Events     []AttemptEvent `json:"events"`
}

type CompleteAttemptRequest struct {
	LeaseToken string `json:"lease_token"`
	State      string `json:"state"`
	Result     string `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
}

type ErrorBody struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
