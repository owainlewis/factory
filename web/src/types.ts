export type TaskState = "queued" | "running" | "succeeded" | "failed" | "cancelled";
export type Runtime = "codex" | "claude-code";

export interface Repository {
  id: string;
  key: string;
  remote_identity: string;
  retained_count: number;
}

export interface RetainedWorktree {
  attempt_id: string;
  repository_id: string;
  path: string;
  reason: string;
  cleanup_command: string;
}

export interface Worker {
  id: string;
  name: string;
  worker_version: string;
  runtime: Runtime;
  runtime_version: string;
  capacity: number;
  active_count: number;
  health: "healthy" | "unhealthy";
  online: boolean;
  repositories: Repository[];
  source_access?: Array<{ provider: string; hostname: string }>;
  accepts_managed_repositories?: boolean;
  repository_cache_count?: number;
  retained_worktrees: RetainedWorktree[];
  registered_at: string;
  last_heartbeat: string;
  current_task_title?: string;
}

export interface ManagedRepository {
  id: string;
  remote_identity: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface ManagedRepositoryWorkerReadiness {
  id: string;
  name: string;
  cached: boolean;
  advertised: boolean;
  ready: boolean;
  reason: string;
}

export interface ManagedRepositoryReadiness {
  routing_ready: boolean;
  workers: ManagedRepositoryWorkerReadiness[];
}

export interface Task {
  id: string;
  request_key: string;
  title: string;
  description?: string;
  worker_id: string;
  repository_id: string;
  timeout_seconds: number;
  state: TaskState;
  created_at: string;
}

export interface TaskPage {
  tasks: Task[];
  next_cursor: string | null;
}

export interface Execution {
  id: string;
  task_id: string;
  assigned_worker_id: string;
  required_runtime: Runtime;
  state: "queued" | "preparing" | "running" | "succeeded" | "failed" | "cancelled";
  cancellation_requested: boolean;
  created_at: string;
  updated_at: string;
}

export interface Attempt {
  id: string;
  execution_id: string;
  worker_id: string;
  attempt_number: number;
  state: "preparing" | "running" | "succeeded" | "failed" | "cancelled" | "lost";
  lease_expires_at: string;
  result?: string;
  error?: string;
  started_at?: string;
  completed_at?: string;
  created_at: string;
}

export interface TaskDetail {
  task: Task & { description: string };
  context: string;
  execution: Execution;
  repository: Repository;
  repository_available: boolean;
  attempts: Attempt[] | null;
  workflow?: TaskWorkflowSnapshot;
  resolved_prompt: string;
}

export interface TaskWorkflowSnapshot {
  id: string;
  revision_id: string;
  name: string;
  revision_number: number;
}

export interface WorkflowRevision {
  id: string;
  workflow_id: string;
  revision_number: number;
  name: string;
  summary: string;
  instructions?: string;
  created_at: string;
}

export interface Workflow {
  id: string;
  enabled: boolean;
  current_revision: WorkflowRevision;
  created_at: string;
  updated_at: string;
}

export interface WorkflowDetail {
  workflow: Workflow;
  revisions: WorkflowRevision[];
}

export interface WorkflowPage {
  workflows: Workflow[];
  next_cursor: string | null;
}

export type MetricsWindow = "24h" | "7d" | "30d" | "all";

export interface MetricsSummary {
  window: MetricsWindow;
  generated_at: string;
  executions_created: number;
  executions_completed: number;
  succeeded: number;
  failed: number;
  cancelled: number;
  queued: number;
  running: number;
  success_rate: number | null;
  retry_rate: number | null;
  median_cycle_time_seconds: number | null;
  workers_online: number;
  workers_total: number;
}

export interface AttemptEvent {
  sequence: number;
  kind: string;
  payload: unknown;
  server_time: string;
}

export interface AttemptEventPage {
  events: AttemptEvent[];
  next_after: number;
  has_more: boolean;
}

export interface APIErrorBody {
  error: { code: string; message: string };
}

interface CreateTaskBaseInput {
  request_key: string;
  title: string;
  worker_id: string;
  repository_id: string;
  timeout_seconds: number;
}

export type CreateTaskInput = CreateTaskBaseInput & (
  | { description: string; context?: never; workflow_revision_id?: never }
  | { description?: never; context: string; workflow_revision_id: string }
);

export interface CreateWorkflowInput {
  request_key: string;
  name: string;
  summary: string;
  instructions: string;
}

export interface CreateWorkflowRevisionInput extends CreateWorkflowInput {
  expected_revision_id: string;
}
