# HTTP API contract

This file is generated from the route definitions used by the server. Run `just api-contract` after changing a route. CI rejects drift.

All request and response bodies are JSON unless the response is `204`. Named shapes correspond to JSON-tagged types in `internal/protocol/types.go`. Unknown request fields are rejected. JSON responses use `Cache-Control: no-store`.

Every error body has this shape:

```json
{"error":{"code":"stable_machine_code","message":"human-readable message"}}
```

## Health

| Method and path | Operation | Request | Success response | Pagination | Errors |
| --- | --- | --- | --- | --- | --- |
| `GET /healthz` | health: Check SQLite availability. | none | 200 HealthResponse JSON | none | ErrorBody: {error: {code: string, message: string}} |

## Workers

| Method and path | Operation | Request | Success response | Pagination | Errors |
| --- | --- | --- | --- | --- | --- |
| `PUT /api/v1/workers/{worker_id}` | registerWorker: Register or heartbeat a worker. | WorkerRegistrationRequest JSON; legacy codex_version is accepted only without runtime fields | 200 Worker JSON; legacy requests receive LegacyWorkerResponse JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/workers/{worker_id}/claims` | claim: Claim the next eligible execution. | ClaimRequest JSON | 200 Claim JSON or 204 with no body | none | ErrorBody: {error: {code: string, message: string}} |
| `GET /api/v1/workers` | listWorkers: List workers. | none | 200 ListWorkersResponse JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `GET /api/v1/workers/{worker_id}` | getWorker: Get one worker. | none | 200 Worker JSON | none | ErrorBody: {error: {code: string, message: string}} |

## Repositories

| Method and path | Operation | Request | Success response | Pagination | Errors |
| --- | --- | --- | --- | --- | --- |
| `GET /api/v1/repositories` | listManagedRepositories: List managed repositories. | none | 200 ListManagedRepositoriesResponse JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/repositories` | createManagedRepository: Create or replay a managed repository. | CreateManagedRepositoryRequest JSON | 200 or 201 ManagedRepository JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `GET /api/v1/repositories/{repository_id}` | getManagedRepository: Get one managed repository. | none | 200 ManagedRepository JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `GET /api/v1/repositories/{repository_id}/readiness` | getManagedRepositoryReadiness: Inspect worker readiness for a repository. | none | 200 ManagedRepositoryReadiness JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `PUT /api/v1/repositories/{repository_id}/enabled` | setManagedRepositoryEnabled: Enable or disable a repository. | SetManagedRepositoryEnabledRequest JSON | 200 ManagedRepository JSON | none | ErrorBody: {error: {code: string, message: string}} |

## Workflows

| Method and path | Operation | Request | Success response | Pagination | Errors |
| --- | --- | --- | --- | --- | --- |
| `GET /api/v1/workflows` | listWorkflows: List and filter workflows. | none | 200 ListWorkflowsResponse JSON | Query: title, enabled, limit 1..200, opaque cursor; stable updated-at/ID ordering | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/workflows` | createWorkflow: Create or replay a workflow. | CreateWorkflowRequest JSON | 200 or 201 WorkflowDetail JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `GET /api/v1/workflows/{workflow_id}` | getWorkflow: Get workflow revisions and current state. | none | 200 WorkflowDetail JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/workflows/{workflow_id}/revisions` | createWorkflowRevision: Create or replay a workflow revision. | CreateWorkflowRevisionRequest JSON | 200 or 201 WorkflowDetail JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `PUT /api/v1/workflows/{workflow_id}/enabled` | setWorkflowEnabled: Enable or disable a workflow. | SetWorkflowEnabledRequest JSON | 200 WorkflowDetail JSON | none | ErrorBody: {error: {code: string, message: string}} |

## Automations

| Method and path | Operation | Request | Success response | Pagination | Errors |
| --- | --- | --- | --- | --- | --- |
| `GET /api/v1/automations` | listAutomations: List automations. | none | 200 ListAutomationsResponse JSON | Query: limit 1..200 and opaque cursor; stable updated-at/ID ordering | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/automations` | createAutomation: Create or replay an automation. | CreateAutomationRequest JSON | 200 or 201 AutomationDetail JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `GET /api/v1/automations/{automation_id}` | getAutomation: Get one automation. | none | 200 AutomationDetail JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `PUT /api/v1/automations/{automation_id}` | updateAutomation: Update an automation with optimistic versioning. | UpdateAutomationRequest JSON | 200 AutomationDetail JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `PUT /api/v1/automations/{automation_id}/enabled` | setAutomationEnabled: Enable or disable an automation. | SetAutomationEnabledRequest JSON | 200 AutomationDetail JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/automations/{automation_id}/test` | testAutomation: Test an automation without dispatch. | empty JSON object | 200 TestAutomationResult JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/automations/{automation_id}/check` | checkAutomation: Request an immediate provider check. | empty JSON object | 202 AutomationDetail JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/automations/{automation_id}/run` | runAutomation: Run a schedule automation now. | RunAutomationRequest JSON | 202 AutomationDetail JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `GET /api/v1/automations/{automation_id}/occurrences` | listAutomationOccurrences: List retained automation occurrences. | none | 200 ListAutomationOccurrencesResponse JSON | Query: limit 1..200 and opaque cursor; stable created-at/ID ordering | ErrorBody: {error: {code: string, message: string}} |

## Legacy migration

| Method and path | Operation | Request | Success response | Pagination | Errors |
| --- | --- | --- | --- | --- | --- |
| `POST /api/v1/migrations/legacy-poller/preview` | previewLegacyPoller: Preview a locked legacy poller snapshot. | PreviewLegacyPollerRequest JSON | 201 LegacyPollerMigration JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/migrations/legacy-poller/import` | importLegacyPoller: Import a reviewed legacy poller snapshot. | ImportLegacyPollerRequest JSON | 200 LegacyPollerMigration JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `GET /api/v1/migrations/legacy-poller/active` | activeLegacyPollerMigration: Get the active legacy migration. | none | 200 ActiveLegacyPollerMigrationResponse JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `GET /api/v1/migrations/legacy-poller/{migration_id}` | getLegacyPollerMigration: Get one legacy migration. | none | 200 LegacyPollerMigration JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/migrations/legacy-poller/{migration_id}/finalize` | finalizeLegacyPoller: Finalize and archive a legacy migration. | FinalizeLegacyPollerRequest JSON | 200 LegacyPollerMigration JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/occurrences/{occurrence_id}/resume` | resumeLegacyPollerOccurrence: Resume one pending legacy occurrence. | empty JSON object | 200 AutomationOccurrence JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/occurrences/{occurrence_id}/skip` | skipLegacyPollerOccurrence: Skip one pending legacy occurrence. | empty JSON object | 200 AutomationOccurrence JSON | none | ErrorBody: {error: {code: string, message: string}} |

## Metrics

| Method and path | Operation | Request | Success response | Pagination | Errors |
| --- | --- | --- | --- | --- | --- |
| `GET /api/v1/metrics/summary` | getMetrics: Get bounded execution metrics. | none | 200 MetricsSummary JSON | Query: window is 24h, 7d, 30d, or all; default 7d | ErrorBody: {error: {code: string, message: string}} |

## Tasks

| Method and path | Operation | Request | Success response | Pagination | Errors |
| --- | --- | --- | --- | --- | --- |
| `GET /api/v1/tasks` | listTasks: List retained task summaries. | none | 200 ListTasksResponse JSON | Query: limit 1..200 and opaque cursor; stable created-at/ID ordering | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/tasks` | createTask: Create or replay a task. | CreateTaskRequest JSON | 200 or 201 TaskDetail JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `GET /api/v1/tasks/{task_id}` | getTask: Get task, execution, attempts, and resolved prompt. | none | 200 TaskDetail JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `DELETE /api/v1/tasks/{task_id}` | deleteTask: Delete eligible terminal task history. | empty JSON object or empty body | 200 DeleteTaskResponse JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/tasks/{task_id}/cancel` | cancelTask: Request task cancellation. | empty JSON object or empty body | 200 TaskDetail JSON | none | ErrorBody: {error: {code: string, message: string}} |

## Executions

| Method and path | Operation | Request | Success response | Pagination | Errors |
| --- | --- | --- | --- | --- | --- |
| `POST /api/v1/executions/{execution_id}/retry` | retryExecution: Retry a terminal execution. | empty JSON object or empty body | 200 TaskDetail JSON | none | ErrorBody: {error: {code: string, message: string}} |

## Attempts and events

| Method and path | Operation | Request | Success response | Pagination | Errors |
| --- | --- | --- | --- | --- | --- |
| `GET /api/v1/attempts/{attempt_id}` | getAttempt: Get one attempt. | none | 200 Attempt JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/attempts/{attempt_id}/start` | startAttempt: Record attempt startup and worktree metadata. | StartAttemptRequest JSON | 200 Attempt JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `PUT /api/v1/attempts/{attempt_id}/heartbeat` | heartbeat: Renew an attempt lease. | LeaseRequest JSON | 200 HeartbeatResponse JSON | none | ErrorBody: {error: {code: string, message: string}} |
| `GET /api/v1/attempts/{attempt_id}/events` | getEvents: Read an attempt event page. | none | 200 ListAttemptEventsResponse JSON | Query: after >= -1 and limit 1..500; next page starts after next_after | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/attempts/{attempt_id}/events` | appendEvents: Append an ordered event batch. | EventBatchRequest JSON | 204 with no body | none | ErrorBody: {error: {code: string, message: string}} |
| `POST /api/v1/attempts/{attempt_id}/complete` | completeAttempt: Complete an attempt. | CompleteAttemptRequest JSON | 200 Attempt JSON | none | ErrorBody: {error: {code: string, message: string}} |

## JSON shapes

These field inventories are generated from the JSON-tagged Go types. A field marked optional may be omitted; `null` is stated separately.

### APIError

```text
{
  code: string
  message: string
}
```

### ActiveLegacyPollerMigrationResponse

```text
{
  migration: LegacyPollerMigration | null
}
```

### Attempt

```text
{
  id: string
  execution_id: string
  worker_id: string
  attempt_number: integer
  state: string
  lease_expires_at: string (RFC3339 timestamp)
  supervisor_pid: integer | null (optional)
  process_identity: string (optional)
  process_group_id: integer | null (optional)
  result: string (optional)
  error: string (optional)
  started_at: string (RFC3339 timestamp) | null (optional)
  completed_at: string (RFC3339 timestamp) | null (optional)
  created_at: string (RFC3339 timestamp)
}
```

### AttemptEvent

```text
{
  sequence: integer
  kind: string
  payload: any JSON value
  server_time: string (RFC3339 timestamp) (optional)
}
```

### Automation

```text
{
  id: string
  title: string
  workflow_id: string
  workflow_title: string
  workflow_revision: integer
  repository_id: string
  repository_identity: string
  context: string
  timeout_seconds: integer
  enabled: boolean
  version: integer
  trigger: GitHubIssueTrigger | GitHubPullRequestTrigger | ScheduleTrigger
  health: AutomationHealth
  last_checked_at: string (RFC3339 timestamp) | null (optional)
  next_check_at: string (RFC3339 timestamp) | null (optional)
  next_due_at: string (RFC3339 timestamp) | null (optional)
  matched_count: integer
  skipped_count: integer
  dispatched_count: integer
  latest_task: AutomationTaskSummary | null (optional)
  created_at: string (RFC3339 timestamp)
  updated_at: string (RFC3339 timestamp)
}
```

### AutomationDetail

```text
{
  automation: Automation
  occurrences: AutomationOccurrence[]
}
```

### AutomationHealth

```text
{
  status: string
  code: string (optional)
  message: string (optional)
}
```

### AutomationMatch

```text
{
  number: integer
  title: string
  url: string
  state: string
  labels: string[]
  is_draft: boolean | null (optional)
  base_branch: string (optional)
  head_commit: string (optional)
}
```

### AutomationOccurrence

```text
{
  id: string
  automation_id: string
  automation_version: integer
  state: string
  issue_number: integer (optional)
  issue_url: string (optional)
  issue_title: string (optional)
  observed_state: string (optional)
  observed_labels: string[] (optional)
  pull_request_number: integer (optional)
  pull_request_url: string (optional)
  pull_request_title: string (optional)
  observed_draft: boolean | null (optional)
  observed_base_branch: string (optional)
  observed_head_commit: string (optional)
  kind: string (optional)
  scheduled_at: string (RFC3339 timestamp) | null (optional)
  run_request_key: string (optional)
  cron: string (optional)
  timezone: string (optional)
  task_request_key: string
  task: AutomationTaskSummary | null (optional)
  task_id_snapshot: string (optional)
  diagnostic: string (optional)
  created_at: string (RFC3339 timestamp)
  updated_at: string (RFC3339 timestamp)
}
```

### AutomationTaskSummary

```text
{
  id: string
  title: string
  state: string
}
```

### AutomationTrigger

```text
GitHubIssueTrigger | GitHubPullRequestTrigger | ScheduleTrigger
```

### Claim

```text
{
  attempt: Attempt
  execution: Execution
  task: Task
  repository: Repository
}
```

### ClaimRequest

```text
{
  request_id: string
  lease_token: string
}
```

### CompleteAttemptRequest

```text
{
  lease_token: string
  state: string
  result: string (optional)
  error: string (optional)
}
```

### CreateAutomationRequest

```text
{
  request_key: string
  title: string
  workflow_id: string
  repository_id: string
  context: string
  timeout_seconds: integer
  trigger: GitHubIssueTrigger | GitHubPullRequestTrigger | ScheduleTrigger
}
```

### CreateManagedRepositoryRequest

```text
{
  remote_identity: string
}
```

### CreateTaskRequest

```text
{
  request_key: string
  title: string
  description: string (optional)
  context: string (optional)
  worker_id: string (optional)
  repository_id: string (optional)
  route: TaskRoute | null (optional)
  timeout_seconds: integer
  workflow_revision_id: string (optional)
}
```

### CreateWorkflowRequest

```text
{
  request_key: string
  title: string
  summary: string
  instructions: string
}
```

### CreateWorkflowRevisionRequest

```text
{
  request_key: string
  expected_revision_id: string
  title: string
  summary: string
  instructions: string
}
```

### DeleteTaskResponse

```text
{
  deleted: boolean
}
```

### ErrorBody

```text
{
  error: APIError
}
```

### EventBatchRequest

```text
{
  lease_token: string
  events: AttemptEvent[]
}
```

### Execution

```text
{
  id: string
  task_id: string
  assigned_worker_id: string
  required_runtime: string
  state: string
  cancellation_requested: boolean
  created_at: string (RFC3339 timestamp)
  updated_at: string (RFC3339 timestamp)
}
```

### FinalizeLegacyPollerRequest

```text
{
  config_path: string (optional)
  data_home: string (optional)
  working_directory: string (optional)
  confirm_stopped: boolean
  migration_id: string
  snapshot_digest: string
}
```

### GitHubIssueTrigger

```text
{
  type: string
  state: string
  required_labels: string[]
  poll_interval_seconds: integer
}
```

### GitHubPullRequestTrigger

```text
{
  type: string
  state: string
  include_drafts: boolean
  required_labels: string[]
  base_branches: string[]
  poll_interval_seconds: integer
}
```

### HealthResponse

```text
{
  status: string
}
```

### HeartbeatResponse

```text
{
  lease_expires_at: string (RFC3339 timestamp)
  cancellation_requested: boolean
}
```

### ImportLegacyPollerRequest

```text
{
  config_path: string (optional)
  data_home: string (optional)
  working_directory: string (optional)
  confirm_stopped: boolean
  migration_id: string
  snapshot_digest: string
  mappings: LegacyPollerQueueMapping[]
}
```

### LeaseRequest

```text
{
  lease_token: string
}
```

### LegacyPollerCounts

```text
{
  queues: integer
  supported_queues: integer
  unsupported_queues: integer
  pending_observations: integer
  submitted_observations: integer
}
```

### LegacyPollerMigration

```text
{
  id: string
  snapshot_digest: string
  status: string
  config_path: string
  data_home: string
  working_directory: string
  data_directory: string
  ledger_path: string
  archive_root: string
  archive_path: string (optional)
  counts: LegacyPollerCounts
  queues: LegacyPollerQueue[]
  automations: Automation[]
  occurrences: AutomationOccurrence[]
  errors: string[]
  created_at: string (RFC3339 timestamp)
  updated_at: string (RFC3339 timestamp)
}
```

### LegacyPollerQueue

```text
{
  queue_id: string
  name: string
  source: string
  project: string
  state: string
  required_labels: string[]
  poll_interval_seconds: integer
  timeout_seconds: integer
  repository_id: string (optional)
  repository_identity: string (optional)
  workflow_title: string
  automation_title: string
  pending_observations: integer
  submitted_observations: integer
  supported: boolean
  blocking: boolean
  errors: string[]
}
```

### LegacyPollerQueueMapping

```text
{
  queue_id: string
  workflow_title: string
  automation_title: string
}
```

### LegacyPollerSelection

```text
{
  config_path: string (optional)
  data_home: string (optional)
  working_directory: string (optional)
  confirm_stopped: boolean
}
```

### LegacyWorkerResponse

```text
{
  id: string
  name: string
  worker_version: string
  codex_version: string
  capacity: integer
  active_count: integer
  health: string
  online: boolean
  repositories: Repository[]
  retained_worktrees: RetainedWorktree[]
  current_task_title: string (optional)
  registered_at: string (RFC3339 timestamp)
  last_heartbeat: string (RFC3339 timestamp)
}
```

### ListAttemptEventsResponse

```text
{
  events: AttemptEvent[]
  next_after: integer
  has_more: boolean
}
```

### ListAutomationOccurrencesResponse

```text
{
  occurrences: AutomationOccurrence[]
  next_cursor: string | null
}
```

### ListAutomationsResponse

```text
{
  automations: Automation[]
  next_cursor: string | null
}
```

### ListManagedRepositoriesResponse

```text
{
  repositories: ManagedRepository[]
}
```

### ListTasksResponse

```text
{
  tasks: Task[]
  next_cursor: string | null
}
```

### ListWorkersResponse

```text
{
  workers: Worker[]
}
```

### ListWorkflowsResponse

```text
{
  workflows: Workflow[]
  next_cursor: string | null
}
```

### ManagedRepository

```text
{
  id: string
  remote_identity: string
  enabled: boolean
  created_at: string (RFC3339 timestamp)
  updated_at: string (RFC3339 timestamp)
}
```

### ManagedRepositoryReadiness

```text
{
  routing_ready: boolean
  workers: ManagedRepositoryWorkerReadiness[]
}
```

### ManagedRepositoryWorkerReadiness

```text
{
  id: string
  name: string
  cached: boolean
  advertised: boolean
  ready: boolean
  reason: string
}
```

### MetricsSummary

```text
{
  window: string
  generated_at: string (RFC3339 timestamp)
  executions_created: integer
  executions_completed: integer
  succeeded: integer
  failed: integer
  cancelled: integer
  queued: integer
  running: integer
  success_rate: number | null
  retry_rate: number | null
  median_cycle_time_seconds: number | null
  workers_online: integer
  workers_total: integer
  weekly_limit: WeeklyLimit | null (optional)
}
```

### PreviewLegacyPollerRequest

```text
{
  config_path: string (optional)
  data_home: string (optional)
  working_directory: string (optional)
  confirm_stopped: boolean
}
```

### Repository

```text
{
  id: string
  key: string
  remote_identity: string
  retained_count: integer
}
```

### RepositoryRegistration

```text
{
  key: string
  remote_identity: string
  retained_count: integer
}
```

### RetainedWorktree

```text
{
  attempt_id: string
  repository_id: string
  path: string
  reason: string
  cleanup_command: string
}
```

### RunAutomationRequest

```text
{
  request_key: string
}
```

### ScheduleTrigger

```text
{
  type: string
  cron: string
  timezone: string
}
```

### SetAutomationEnabledRequest

```text
{
  enabled: boolean | null
  confirm_legacy_poller_stopped: boolean (optional)
}
```

### SetManagedRepositoryEnabledRequest

```text
{
  enabled: boolean | null
}
```

### SetWorkflowEnabledRequest

```text
{
  enabled: boolean | null
}
```

### SourceAccess

```text
{
  provider: string
  hostname: string
}
```

### StartAttemptRequest

```text
{
  lease_token: string
  supervisor_pid: integer | null (optional)
  process_identity: string (optional)
  process_group_id: integer | null (optional)
}
```

### Task

```text
{
  id: string
  request_key: string
  title: string
  description: string (optional)
  worker_id: string
  repository_id: string
  timeout_seconds: integer
  state: string
  created_at: string (RFC3339 timestamp)
}
```

### TaskDetail

```text
{
  task: Task
  context: string
  execution: Execution
  repository: Repository
  repository_available: boolean
  attempts: Attempt[]
  workflow: TaskWorkflowSnapshot | null (optional)
  resolved_prompt: string
}
```

### TaskRoute

```text
{
  repository_remote_identity: string
  source_access: SourceAccess
}
```

### TaskWorkflowSnapshot

```text
{
  id: string
  revision_id: string
  title: string
  revision_number: integer
}
```

### TestAutomationResult

```text
{
  matches: AutomationMatch[]
  next_due_at: string (RFC3339 timestamp) | null (optional)
}
```

### UpdateAutomationRequest

```text
{
  expected_version: integer
  title: string
  workflow_id: string
  context: string
  timeout_seconds: integer
  trigger: GitHubIssueTrigger | GitHubPullRequestTrigger | ScheduleTrigger
}
```

### WeeklyLimit

```text
{
  used_percent: integer
  resets_at: string (RFC3339 timestamp)
}
```

### Worker

```text
{
  id: string
  name: string
  worker_version: string
  runtime: string
  runtime_version: string
  capacity: integer
  active_count: integer
  health: string
  online: boolean
  repositories: Repository[]
  source_access: SourceAccess[] (optional)
  accepts_managed_repositories: boolean (optional)
  repository_cache_count: integer (optional)
  retained_worktrees: RetainedWorktree[]
  current_task_title: string (optional)
  registered_at: string (RFC3339 timestamp)
  last_heartbeat: string (RFC3339 timestamp)
}
```

### WorkerRegistrationRequest

```text
{
  name: string
  worker_version: string
  runtime: string | null (optional)
  runtime_version: string | null (optional)
  codex_version: string | null (optional)
  capacity: integer
  active_count: integer
  health: string
  repositories: RepositoryRegistration[]
  source_access: SourceAccess[] (optional)
  accepts_managed_repositories: boolean (optional)
  managed_repository_ids: string[] (optional)
  retained_worktrees: RetainedWorktree[]
  capacity_handoff_version: integer (optional)
  disposed_attempt_ids: string[] (optional)
  weekly_limit: WeeklyLimit | null (optional)
}
```

### Workflow

```text
{
  id: string
  enabled: boolean
  current_revision: WorkflowRevision
  created_at: string (RFC3339 timestamp)
  updated_at: string (RFC3339 timestamp)
}
```

### WorkflowDetail

```text
{
  workflow: Workflow
  revisions: WorkflowRevision[]
}
```

### WorkflowRevision

```text
{
  id: string
  workflow_id: string
  revision_number: integer
  title: string
  summary: string
  instructions: string (optional)
  created_at: string (RFC3339 timestamp)
}
```
