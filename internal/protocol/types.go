package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

const (
	CapabilityKindTool        = "tool"
	CapabilityKindRuntime     = "runtime"
	CapabilityReady           = "ready"
	CapabilityMissing         = "missing"
	CapabilityUnauthenticated = "unauthenticated"
	CapabilityUnhealthy       = "unhealthy"
	RuntimePi                 = "pi"
	RuntimeCodex              = "codex"
	RuntimeClaudeCode         = "claude-code"
	MaxBodyBytes              = 1 << 20
	MaxDescriptionBytes       = 64 << 10
	MaxEventBatchBytes        = 256 << 10
	MaxEventBytes             = 64 << 10
	MaxEventsPerBatch         = 100
	MaxAttemptEventBytes      = 10 << 20
	MaxResultBytes            = 256 << 10
	MaxErrorBytes             = 64 << 10
	DefaultTimeout            = 2 * time.Hour
	MaxTimeout                = 8 * time.Hour
	LeaseDuration             = 30 * time.Second
	EmptyClaimTTL             = 5 * time.Minute
	WorkerOnlineWindow        = 30 * time.Second
	MaxRetainedPerRepo        = 10
	MaxManagedRepositories    = 1000
	MaxRepositoryCacheEntries = 100
	DefaultTaskPageSize       = 50
	MaxTaskPageSize           = 200
	DefaultEventPageSize      = 100
	MaxEventPageSize          = 500
	DefaultWorkflowPageSize   = 50
	MaxWorkflowPageSize       = 200
	MaxWorkflows              = 500
	MaxWorkflowRevisions      = 100
	DefaultDefinitionPageSize = 50
	MaxDefinitionPageSize     = 200
	DefaultRunPageSize        = 50
	MaxRunPageSize            = 200
	MaxDefinitions            = 500
	MaxDefinitionPromptBytes  = 64 << 10
	MaxDefinitionTools        = 32
	MaxDefinitionInputs       = 32
	DefaultAutomationPageSize = 50
	MaxAutomationPageSize     = 200
	MaxAutomations            = 500
	MaxAutomationOccurrences  = 100000
	MaxAutomationContextBytes = 8 << 10
	MaxAutomationMatches      = 100
	MinWorkerCapacity         = 1
	MaxWorkerCapacity         = 100
)

func SupportedRuntime(value string) bool {
	return value == RuntimePi || value == RuntimeCodex || value == RuntimeClaudeCode
}

func SupportedRuntimes() []string {
	return []string{RuntimePi, RuntimeCodex, RuntimeClaudeCode}
}

type Capability struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	Message string `json:"message,omitempty"`
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

type SourceAccess struct {
	Provider string `json:"provider"`
	Hostname string `json:"hostname"`
}

type WeeklyLimit struct {
	UsedPercent int       `json:"used_percent"`
	ResetsAt    time.Time `json:"resets_at"`
}

type WorkerRegistration struct {
	Name                       string                   `json:"name"`
	WorkerVersion              string                   `json:"worker_version"`
	Runtime                    string                   `json:"runtime"`
	RuntimeVersion             string                   `json:"runtime_version"`
	Capabilities               []Capability             `json:"capabilities,omitempty"`
	Capacity                   int                      `json:"capacity"`
	ActiveCount                int                      `json:"active_count"`
	Health                     string                   `json:"health"`
	Repositories               []RepositoryRegistration `json:"repositories"`
	SourceAccess               []SourceAccess           `json:"source_access,omitempty"`
	AcceptsManagedRepositories bool                     `json:"accepts_managed_repositories,omitempty"`
	ManagedRepositoryIDs       []string                 `json:"managed_repository_ids,omitempty"`
	RetainedWorktrees          []RetainedWorktree       `json:"retained_worktrees"`
	CapacityHandoffVersion     int                      `json:"capacity_handoff_version,omitempty"`
	DisposedAttemptIDs         []string                 `json:"disposed_attempt_ids,omitempty"`
	WeeklyLimit                *WeeklyLimit             `json:"weekly_limit,omitempty"`
}

type Repository struct {
	ID             string `json:"id"`
	Key            string `json:"key"`
	RemoteIdentity string `json:"remote_identity"`
	RetainedCount  int    `json:"retained_count"`
}

type ManagedRepository struct {
	ID             string    `json:"id"`
	RemoteIdentity string    `json:"remote_identity"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ManagedRepositoryReadiness struct {
	RoutingReady bool                               `json:"routing_ready"`
	Workers      []ManagedRepositoryWorkerReadiness `json:"workers"`
}

type ManagedRepositoryWorkerReadiness struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Cached     bool   `json:"cached"`
	Advertised bool   `json:"advertised"`
	Ready      bool   `json:"ready"`
	Reason     string `json:"reason"`
}

type WorkerRepositoryOption struct {
	ID             string `json:"id"`
	Key            string `json:"key,omitempty"`
	RemoteIdentity string `json:"remote_identity"`
	Enabled        bool   `json:"enabled"`
	Cached         bool   `json:"cached"`
	Advertised     bool   `json:"advertised"`
	Ready          bool   `json:"ready"`
	Reason         string `json:"reason"`
}

type CreateManagedRepositoryRequest struct {
	RemoteIdentity string `json:"remote_identity"`
}

type SetManagedRepositoryEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

type Worker struct {
	ID                         string             `json:"id"`
	Name                       string             `json:"name"`
	WorkerVersion              string             `json:"worker_version"`
	Runtime                    string             `json:"runtime"`
	RuntimeVersion             string             `json:"runtime_version"`
	Capabilities               []Capability       `json:"capabilities,omitempty"`
	Capacity                   int                `json:"capacity"`
	ActiveCount                int                `json:"active_count"`
	Health                     string             `json:"health"`
	Online                     bool               `json:"online"`
	Repositories               []Repository       `json:"repositories"`
	SourceAccess               []SourceAccess     `json:"source_access,omitempty"`
	AcceptsManagedRepositories bool               `json:"accepts_managed_repositories,omitempty"`
	RepositoryCacheCount       int                `json:"repository_cache_count,omitempty"`
	RetainedWorktrees          []RetainedWorktree `json:"retained_worktrees"`
	CurrentTaskTitle           string             `json:"current_task_title,omitempty"`
	RegisteredAt               time.Time          `json:"registered_at"`
	LastHeartbeat              time.Time          `json:"last_heartbeat"`
}

type CreateTaskRequest struct {
	RequestKey                 string     `json:"request_key"`
	Title                      string     `json:"title"`
	Description                string     `json:"description,omitempty"`
	Context                    string     `json:"context,omitempty"`
	WorkerID                   string     `json:"worker_id,omitempty"`
	RepositoryID               string     `json:"repository_id,omitempty"`
	Route                      *TaskRoute `json:"route,omitempty"`
	Runtime                    string     `json:"runtime,omitempty"`
	TimeoutSeconds             int        `json:"timeout_seconds"`
	WorkflowRevisionID         string     `json:"workflow_revision_id,omitempty"`
	DefinitionID               string     `json:"definition_id,omitempty"`
	DescriptionProvided        bool       `json:"-"`
	ContextProvided            bool       `json:"-"`
	WorkflowRevisionIDProvided bool       `json:"-"`
	DefinitionIDProvided       bool       `json:"-"`
}

func (request *CreateTaskRequest) UnmarshalJSON(data []byte) error {
	type wireRequest CreateTaskRequest
	var decoded wireRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*request = CreateTaskRequest(decoded)
	_, request.DescriptionProvided = fields["description"]
	_, request.ContextProvided = fields["context"]
	_, request.WorkflowRevisionIDProvided = fields["workflow_revision_id"]
	_, request.DefinitionIDProvided = fields["definition_id"]
	return nil
}

type TaskRoute struct {
	RepositoryRemoteIdentity string       `json:"repository_remote_identity"`
	SourceAccess             SourceAccess `json:"source_access"`
}

type Task struct {
	ID              string    `json:"id"`
	RequestKey      string    `json:"request_key"`
	Title           string    `json:"title"`
	Description     string    `json:"description,omitempty"`
	WorkerID        string    `json:"worker_id"`
	RepositoryID    string    `json:"repository_id"`
	RequiredRuntime string    `json:"required_runtime"`
	TimeoutSeconds  int       `json:"timeout_seconds"`
	State           string    `json:"state"`
	CreatedAt       time.Time `json:"created_at"`
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
	Task                Task                  `json:"task"`
	Context             string                `json:"context"`
	Execution           Execution             `json:"execution"`
	Repository          Repository            `json:"repository"`
	RepositoryAvailable bool                  `json:"repository_available"`
	Attempts            []Attempt             `json:"attempts"`
	Workflow            *TaskWorkflowSnapshot `json:"workflow,omitempty"`
	Definition          *DefinitionSnapshot   `json:"definition,omitempty"`
	ResolvedPrompt      string                `json:"resolved_prompt"`
}

type CreateRunRequest struct {
	RequestKey   string            `json:"request_key"`
	DefinitionID string            `json:"definition_id"`
	RepositoryID string            `json:"repository_id"`
	Parameters   map[string]string `json:"parameters,omitempty"`
}

type RunRepository struct {
	ID             string `json:"id"`
	RemoteIdentity string `json:"remote_identity"`
}

type Run struct {
	ID                         string             `json:"id"`
	RequestKey                 string             `json:"request_key"`
	SourceKind                 string             `json:"source_kind"`
	Definition                 DefinitionSnapshot `json:"definition"`
	State                      string             `json:"state"`
	JobCount                   int                `json:"job_count"`
	RepositoryRemoteIdentities []string           `json:"repository_remote_identities"`
	AdmittedAt                 time.Time          `json:"admitted_at"`
	UpdatedAt                  time.Time          `json:"updated_at"`
}

type Job struct {
	ID                       string     `json:"id"`
	RunID                    string     `json:"run_id"`
	RepositoryID             string     `json:"repository_id"`
	RepositoryRemoteIdentity string     `json:"repository_remote_identity"`
	TaskID                   string     `json:"task_id,omitempty"`
	ExecutionID              string     `json:"execution_id,omitempty"`
	AssignedWorkerID         string     `json:"assigned_worker_id,omitempty"`
	RequiredRuntime          string     `json:"required_runtime"`
	State                    string     `json:"state"`
	BlockedReason            string     `json:"blocked_reason,omitempty"`
	AdmittedAt               time.Time  `json:"admitted_at"`
	StartedAt                *time.Time `json:"started_at,omitempty"`
	TerminalAt               *time.Time `json:"terminal_at,omitempty"`
	Result                   string     `json:"result,omitempty"`
	FailureReason            string     `json:"failure_reason,omitempty"`
	RetryMayRepeatEffects    bool       `json:"retry_may_repeat_effects"`
	CancellationRequested    bool       `json:"cancellation_requested"`
}

type JobDetail struct {
	Job            Job       `json:"job"`
	Attempts       []Attempt `json:"attempts"`
	ResolvedPrompt string    `json:"resolved_prompt"`
}

type RunDetail struct {
	Run        Run               `json:"run"`
	Parameters map[string]string `json:"parameters"`
	Jobs       []JobDetail       `json:"jobs"`
}

type RunCursor struct {
	AdmittedAtMillis int64  `json:"admitted_at"`
	ID               string `json:"id"`
}

type RunPageRequest struct {
	Limit  int
	Cursor *RunCursor
}

type RunPage struct {
	Runs       []Run      `json:"runs"`
	NextCursor *RunCursor `json:"-"`
}

type TaskWorkflowSnapshot struct {
	ID             string `json:"id"`
	RevisionID     string `json:"revision_id"`
	Title          string `json:"title"`
	RevisionNumber int    `json:"revision_number"`
}

type Workflow struct {
	ID              string           `json:"id"`
	Enabled         bool             `json:"enabled"`
	CurrentRevision WorkflowRevision `json:"current_revision"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

type WorkflowRevision struct {
	ID             string    `json:"id"`
	WorkflowID     string    `json:"workflow_id"`
	RevisionNumber int       `json:"revision_number"`
	Title          string    `json:"title"`
	Summary        string    `json:"summary"`
	Instructions   string    `json:"instructions,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type WorkflowDetail struct {
	Workflow  Workflow           `json:"workflow"`
	Revisions []WorkflowRevision `json:"revisions"`
}

type CreateWorkflowRequest struct {
	RequestKey   string `json:"request_key"`
	Title        string `json:"title"`
	Summary      string `json:"summary"`
	Instructions string `json:"instructions"`
}

type CreateWorkflowRevisionRequest struct {
	RequestKey         string `json:"request_key"`
	ExpectedRevisionID string `json:"expected_revision_id"`
	Title              string `json:"title"`
	Summary            string `json:"summary"`
	Instructions       string `json:"instructions"`
}

type SetWorkflowEnabledRequest struct {
	Enabled *bool `json:"enabled"`
}

type WorkflowCursor struct {
	UpdatedAtMillis int64
	ID              string
}

type WorkflowPageRequest struct {
	Limit   int
	Cursor  *WorkflowCursor
	Title   string
	Enabled *bool
}

type WorkflowPage struct {
	Workflows  []Workflow
	NextCursor *WorkflowCursor
}

type Definition struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Prompt         string            `json:"prompt"`
	Runtime        string            `json:"runtime"`
	AllowedTools   []string          `json:"allowed_tools"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Inputs         map[string]string `json:"inputs"`
	Generation     int               `json:"generation"`
	Archived       bool              `json:"archived"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

type DefinitionSnapshot struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Prompt         string            `json:"prompt"`
	Runtime        string            `json:"runtime"`
	AllowedTools   []string          `json:"allowed_tools"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Inputs         map[string]string `json:"inputs"`
	Generation     int               `json:"generation"`
}

func (definition Definition) Snapshot() DefinitionSnapshot {
	return DefinitionSnapshot{
		ID: definition.ID, Name: definition.Name, Prompt: definition.Prompt,
		Runtime: definition.Runtime, AllowedTools: append([]string(nil), definition.AllowedTools...),
		TimeoutSeconds: definition.TimeoutSeconds, Inputs: cloneStringMap(definition.Inputs),
		Generation: definition.Generation,
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

type CreateDefinitionRequest struct {
	RequestKey     string            `json:"request_key"`
	Name           string            `json:"name"`
	Prompt         string            `json:"prompt"`
	Runtime        string            `json:"runtime"`
	AllowedTools   []string          `json:"allowed_tools"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Inputs         map[string]string `json:"inputs"`
}

type UpdateDefinitionRequest struct {
	RequestKey         string            `json:"request_key"`
	ExpectedGeneration int               `json:"expected_generation"`
	Name               string            `json:"name"`
	Prompt             string            `json:"prompt"`
	Runtime            string            `json:"runtime"`
	AllowedTools       []string          `json:"allowed_tools"`
	TimeoutSeconds     int               `json:"timeout_seconds"`
	Inputs             map[string]string `json:"inputs"`
}

type SetDefinitionArchivedRequest struct {
	Archived           *bool `json:"archived"`
	ExpectedGeneration int   `json:"expected_generation"`
}

type DefinitionCursor struct {
	UpdatedAtMillis int64
	ID              string
}

type DefinitionPageRequest struct {
	Limit    int
	Cursor   *DefinitionCursor
	Archived bool
}

type DefinitionPage struct {
	Definitions []Definition
	NextCursor  *DefinitionCursor
}

const (
	AutomationTriggerGitHubIssue       = "github_issue"
	AutomationTriggerGitHubPullRequest = "github_pull_request"
	AutomationTriggerSchedule          = "schedule"
)

type GitHubIssueTrigger struct {
	Type                string   `json:"type"`
	State               string   `json:"state"`
	RequiredLabels      []string `json:"required_labels"`
	PollIntervalSeconds int      `json:"poll_interval_seconds"`
}

type GitHubPullRequestTrigger struct {
	Type                string   `json:"type"`
	State               string   `json:"state"`
	IncludeDrafts       bool     `json:"include_drafts"`
	RequiredLabels      []string `json:"required_labels"`
	BaseBranches        []string `json:"base_branches"`
	PollIntervalSeconds int      `json:"poll_interval_seconds"`
}

type ScheduleTrigger struct {
	Type     string `json:"type"`
	Cron     string `json:"cron"`
	Timezone string `json:"timezone"`
}

// AutomationTrigger is a strict flat tagged union. UnmarshalJSON rejects fields
// that do not belong to the selected concrete trigger type.
type AutomationTrigger struct {
	Type                string
	State               string
	IncludeDrafts       bool
	RequiredLabels      []string
	BaseBranches        []string
	PollIntervalSeconds int
	Cron                string
	Timezone            string
}

func (trigger *AutomationTrigger) UnmarshalJSON(body []byte) error {
	var discriminator struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &discriminator); err != nil {
		return err
	}
	decode := func(target any) error {
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		return decoder.Decode(target)
	}
	switch discriminator.Type {
	case AutomationTriggerGitHubIssue:
		var value GitHubIssueTrigger
		if err := decode(&value); err != nil {
			return err
		}
		*trigger = AutomationTrigger{
			Type: value.Type, State: value.State, RequiredLabels: value.RequiredLabels,
			PollIntervalSeconds: value.PollIntervalSeconds,
		}
		return nil
	case AutomationTriggerGitHubPullRequest:
		var value GitHubPullRequestTrigger
		if err := decode(&value); err != nil {
			return err
		}
		*trigger = AutomationTrigger{
			Type: value.Type, State: value.State, IncludeDrafts: value.IncludeDrafts,
			RequiredLabels: value.RequiredLabels, BaseBranches: value.BaseBranches,
			PollIntervalSeconds: value.PollIntervalSeconds,
		}
		return nil
	case AutomationTriggerSchedule:
		var value ScheduleTrigger
		if err := decode(&value); err != nil {
			return err
		}
		*trigger = AutomationTrigger{Type: value.Type, Cron: value.Cron, Timezone: value.Timezone}
		return nil
	default:
		return fmt.Errorf("unsupported Automation trigger type %q", discriminator.Type)
	}
}

func (trigger AutomationTrigger) MarshalJSON() ([]byte, error) {
	switch trigger.Type {
	case AutomationTriggerGitHubIssue:
		return json.Marshal(GitHubIssueTrigger{
			Type: trigger.Type, State: trigger.State, RequiredLabels: trigger.RequiredLabels,
			PollIntervalSeconds: trigger.PollIntervalSeconds,
		})
	case AutomationTriggerGitHubPullRequest:
		return json.Marshal(GitHubPullRequestTrigger{
			Type: trigger.Type, State: trigger.State, IncludeDrafts: trigger.IncludeDrafts,
			RequiredLabels: trigger.RequiredLabels, BaseBranches: trigger.BaseBranches,
			PollIntervalSeconds: trigger.PollIntervalSeconds,
		})
	case AutomationTriggerSchedule:
		return json.Marshal(ScheduleTrigger{Type: trigger.Type, Cron: trigger.Cron, Timezone: trigger.Timezone})
	default:
		return nil, fmt.Errorf("unsupported Automation trigger type %q", trigger.Type)
	}
}

func (trigger AutomationTrigger) GitHubIssue() GitHubIssueTrigger {
	return GitHubIssueTrigger{
		Type: trigger.Type, State: trigger.State, RequiredLabels: trigger.RequiredLabels,
		PollIntervalSeconds: trigger.PollIntervalSeconds,
	}
}

func (trigger AutomationTrigger) GitHubPullRequest() GitHubPullRequestTrigger {
	return GitHubPullRequestTrigger{
		Type: trigger.Type, State: trigger.State, IncludeDrafts: trigger.IncludeDrafts,
		RequiredLabels: trigger.RequiredLabels, BaseBranches: trigger.BaseBranches,
		PollIntervalSeconds: trigger.PollIntervalSeconds,
	}
}

func (trigger AutomationTrigger) Schedule() ScheduleTrigger {
	return ScheduleTrigger{Type: trigger.Type, Cron: trigger.Cron, Timezone: trigger.Timezone}
}

type AutomationHealth struct {
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type Automation struct {
	ID                 string                 `json:"id"`
	Title              string                 `json:"title"`
	WorkflowID         string                 `json:"workflow_id"`
	WorkflowTitle      string                 `json:"workflow_title"`
	WorkflowRevision   int                    `json:"workflow_revision"`
	RepositoryID       string                 `json:"repository_id"`
	RepositoryIdentity string                 `json:"repository_identity"`
	Context            string                 `json:"context"`
	TimeoutSeconds     int                    `json:"timeout_seconds"`
	Enabled            bool                   `json:"enabled"`
	Version            int                    `json:"version"`
	Trigger            AutomationTrigger      `json:"trigger"`
	Health             AutomationHealth       `json:"health"`
	LastCheckedAt      *time.Time             `json:"last_checked_at,omitempty"`
	NextCheckAt        *time.Time             `json:"next_check_at,omitempty"`
	NextDueAt          *time.Time             `json:"next_due_at,omitempty"`
	MatchedCount       int64                  `json:"matched_count"`
	SkippedCount       int64                  `json:"skipped_count"`
	DispatchedCount    int64                  `json:"dispatched_count"`
	LatestTask         *AutomationTaskSummary `json:"latest_task,omitempty"`
	LatestRun          *AutomationOccurrence  `json:"latest_run,omitempty"`
	CreatedAt          time.Time              `json:"created_at"`
	UpdatedAt          time.Time              `json:"updated_at"`
}

type AutomationTaskSummary struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

type AutomationOccurrence struct {
	ID                 string                 `json:"id"`
	AutomationID       string                 `json:"automation_id"`
	AutomationVersion  int                    `json:"automation_version"`
	State              string                 `json:"state"`
	IssueNumber        int                    `json:"issue_number,omitempty"`
	IssueURL           string                 `json:"issue_url,omitempty"`
	IssueTitle         string                 `json:"issue_title,omitempty"`
	ObservedState      string                 `json:"observed_state,omitempty"`
	ObservedLabels     []string               `json:"observed_labels,omitempty"`
	PullRequestNumber  int                    `json:"pull_request_number,omitempty"`
	PullRequestURL     string                 `json:"pull_request_url,omitempty"`
	PullRequestTitle   string                 `json:"pull_request_title,omitempty"`
	ObservedDraft      *bool                  `json:"observed_draft,omitempty"`
	ObservedBaseBranch string                 `json:"observed_base_branch,omitempty"`
	ObservedHeadCommit string                 `json:"observed_head_commit,omitempty"`
	Kind               string                 `json:"kind,omitempty"`
	ScheduledAt        *time.Time             `json:"scheduled_at,omitempty"`
	RunRequestKey      string                 `json:"run_request_key,omitempty"`
	Cron               string                 `json:"cron,omitempty"`
	Timezone           string                 `json:"timezone,omitempty"`
	TaskRequestKey     string                 `json:"task_request_key"`
	Task               *AutomationTaskSummary `json:"task,omitempty"`
	TaskIDSnapshot     string                 `json:"task_id_snapshot,omitempty"`
	Diagnostic         string                 `json:"diagnostic,omitempty"`
	CreatedAt          time.Time              `json:"created_at"`
	UpdatedAt          time.Time              `json:"updated_at"`
}

type LegacyPollerSelection struct {
	ConfigPath       string `json:"config_path,omitempty"`
	DataHome         string `json:"data_home,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	ConfirmStopped   bool   `json:"confirm_stopped"`
}

type LegacyPollerCounts struct {
	Queues      int `json:"queues"`
	Supported   int `json:"supported_queues"`
	Unsupported int `json:"unsupported_queues"`
	Pending     int `json:"pending_observations"`
	Submitted   int `json:"submitted_observations"`
}

type LegacyPollerQueue struct {
	QueueID               string   `json:"queue_id"`
	Name                  string   `json:"name"`
	Source                string   `json:"source"`
	Project               string   `json:"project"`
	State                 string   `json:"state"`
	RequiredLabels        []string `json:"required_labels"`
	PollIntervalSeconds   int      `json:"poll_interval_seconds"`
	TimeoutSeconds        int      `json:"timeout_seconds"`
	RepositoryID          string   `json:"repository_id,omitempty"`
	RepositoryIdentity    string   `json:"repository_identity,omitempty"`
	WorkflowTitle         string   `json:"workflow_title"`
	AutomationTitle       string   `json:"automation_title"`
	PendingObservations   int      `json:"pending_observations"`
	SubmittedObservations int      `json:"submitted_observations"`
	Supported             bool     `json:"supported"`
	Blocking              bool     `json:"blocking"`
	Errors                []string `json:"errors"`
}

type LegacyPollerMigration struct {
	ID               string                 `json:"id"`
	SnapshotDigest   string                 `json:"snapshot_digest"`
	Status           string                 `json:"status"`
	ConfigPath       string                 `json:"config_path"`
	DataHome         string                 `json:"data_home"`
	WorkingDirectory string                 `json:"working_directory"`
	DataDirectory    string                 `json:"data_directory"`
	LedgerPath       string                 `json:"ledger_path"`
	ArchiveRoot      string                 `json:"archive_root"`
	ArchivePath      string                 `json:"archive_path,omitempty"`
	Counts           LegacyPollerCounts     `json:"counts"`
	Queues           []LegacyPollerQueue    `json:"queues"`
	Automations      []Automation           `json:"automations"`
	Occurrences      []AutomationOccurrence `json:"occurrences"`
	Errors           []string               `json:"errors"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
}

type PreviewLegacyPollerRequest struct {
	LegacyPollerSelection
}

type LegacyPollerQueueMapping struct {
	QueueID         string `json:"queue_id"`
	WorkflowTitle   string `json:"workflow_title"`
	AutomationTitle string `json:"automation_title"`
}

type ImportLegacyPollerRequest struct {
	LegacyPollerSelection
	MigrationID    string                     `json:"migration_id"`
	SnapshotDigest string                     `json:"snapshot_digest"`
	Mappings       []LegacyPollerQueueMapping `json:"mappings"`
}

type FinalizeLegacyPollerRequest struct {
	LegacyPollerSelection
	MigrationID    string `json:"migration_id"`
	SnapshotDigest string `json:"snapshot_digest"`
}

type ActiveLegacyPollerMigrationResponse struct {
	Migration *LegacyPollerMigration `json:"migration"`
}

type AutomationDetail struct {
	Automation  Automation             `json:"automation"`
	Occurrences []AutomationOccurrence `json:"occurrences"`
}

type AutomationPage struct {
	Automations []Automation      `json:"automations"`
	NextCursor  *AutomationCursor `json:"-"`
}

type AutomationCursor struct {
	UpdatedAtMillis int64
	ID              string
}

type AutomationOccurrencePage struct {
	Occurrences []AutomationOccurrence      `json:"occurrences"`
	NextCursor  *AutomationOccurrenceCursor `json:"-"`
}

type AutomationOccurrenceCursor struct {
	CreatedAtMillis int64
	ID              string
}

type CreateAutomationRequest struct {
	RequestKey     string            `json:"request_key"`
	Title          string            `json:"title"`
	WorkflowID     string            `json:"workflow_id"`
	RepositoryID   string            `json:"repository_id"`
	Context        string            `json:"context"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Trigger        AutomationTrigger `json:"trigger"`
}

type UpdateAutomationRequest struct {
	ExpectedVersion int               `json:"expected_version"`
	Title           string            `json:"title"`
	WorkflowID      string            `json:"workflow_id"`
	Context         string            `json:"context"`
	TimeoutSeconds  int               `json:"timeout_seconds"`
	Trigger         AutomationTrigger `json:"trigger"`
}

type SetAutomationEnabledRequest struct {
	Enabled                    *bool `json:"enabled"`
	ConfirmLegacyPollerStopped bool  `json:"confirm_legacy_poller_stopped,omitempty"`
}

type RunAutomationRequest struct {
	RequestKey string `json:"request_key"`
}

type GitHubIssueMatch struct {
	Number int      `json:"number"`
	Title  string   `json:"title"`
	URL    string   `json:"url"`
	State  string   `json:"state"`
	Labels []string `json:"labels"`
}

type GitHubPullRequestMatch struct {
	Number     int      `json:"number"`
	Title      string   `json:"title"`
	URL        string   `json:"url"`
	State      string   `json:"state"`
	IsDraft    bool     `json:"is_draft"`
	BaseBranch string   `json:"base_branch"`
	HeadCommit string   `json:"head_commit"`
	Labels     []string `json:"labels"`
}

type AutomationMatch struct {
	Number     int      `json:"number"`
	Title      string   `json:"title"`
	URL        string   `json:"url"`
	State      string   `json:"state"`
	Labels     []string `json:"labels"`
	IsDraft    *bool    `json:"is_draft,omitempty"`
	BaseBranch string   `json:"base_branch,omitempty"`
	HeadCommit string   `json:"head_commit,omitempty"`
}

type TestAutomationResult struct {
	Matches   []AutomationMatch `json:"matches"`
	NextDueAt *time.Time        `json:"next_due_at,omitempty"`
}

type MetricsSummary struct {
	Window                 string       `json:"window"`
	GeneratedAt            time.Time    `json:"generated_at"`
	ExecutionsCreated      int64        `json:"executions_created"`
	ExecutionsCompleted    int64        `json:"executions_completed"`
	Succeeded              int64        `json:"succeeded"`
	Failed                 int64        `json:"failed"`
	Cancelled              int64        `json:"cancelled"`
	Queued                 int64        `json:"queued"`
	Running                int64        `json:"running"`
	SuccessRate            *float64     `json:"success_rate"`
	RetryRate              *float64     `json:"retry_rate"`
	MedianCycleTimeSeconds *float64     `json:"median_cycle_time_seconds"`
	WorkersOnline          int64        `json:"workers_online"`
	WorkersTotal           int64        `json:"workers_total"`
	WeeklyLimit            *WeeklyLimit `json:"weekly_limit,omitempty"`
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
	RunID      string     `json:"run_id,omitempty"`
	JobID      string     `json:"job_id,omitempty"`
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
