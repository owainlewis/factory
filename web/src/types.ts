export type Runtime = "pi" | "codex" | "claude-code";
export type WorkTargetState = "blocked" | "queued" | "preparing" | "running" | "succeeded" | "failed" | "cancelled";
export type WorkState = "blocked" | "queued" | "running" | "succeeded" | "failed" | "partial" | "cancelled";

export interface RoutineRepository {
  id: string;
  remote_identity: string;
}

export interface RoutineSchedule {
  enabled: boolean;
  cron?: string;
  timezone?: string;
  next_due_at?: string;
  pending_due_at?: string;
  health_status: "disabled" | "healthy" | "blocked" | "error";
  health_code?: string;
  health_message?: string;
}

export interface Routine {
  id: string;
  name: string;
  prompt: string;
  prompt_preview?: string;
  runtime: Runtime;
  timeout_seconds: number;
  concurrency_limit: number;
  generation: number;
  archived: boolean;
  read_only: boolean;
  repositories: RoutineRepository[] | null;
  repository_count: number;
  schedule: RoutineSchedule;
  last_work_state?: WorkState;
  created_at: string;
  updated_at: string;
}

export interface SaveRoutineInput {
  name: string;
  prompt: string;
  runtime: Runtime;
  timeout_seconds: number;
  concurrency_limit: number;
  repository_ids: string[];
  schedule: { enabled: boolean; cron?: string; timezone?: string };
  expected_generation?: number;
}

export interface RoutineSnapshot {
  id: string;
  name: string;
  prompt: string;
  runtime: Runtime;
  timeout_seconds: number;
  concurrency_limit: number;
  generation: number;
  repositories: RoutineRepository[] | null;
  cron?: string;
  timezone?: string;
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

export interface WorkTarget {
  id: string;
  work_id: string;
  repository_id: string;
  repository_identity: string;
  resolved_prompt?: string;
  required_runtime: Runtime;
  timeout_seconds: number;
  state: WorkTargetState;
  blocked_reason?: string;
  assigned_worker_id?: string;
  cancellation_requested: boolean;
  retry_may_repeat_effects: boolean;
  admitted_at: string;
  started_at?: string;
  terminal_at?: string;
  result?: string;
  failure_reason?: string;
  attempts?: Attempt[] | null;
}

export interface WorkItem {
  id: string;
  routine_id: string;
  routine: RoutineSnapshot;
  source: "manual" | "schedule" | "provider_history";
  scheduled_at?: string;
  state: WorkState;
  needs_attention: boolean;
  target_count: number;
  succeeded_count: number;
  failed_count: number;
  cancelled_count: number;
  active_count: number;
  admitted_at: string;
  updated_at: string;
  terminal_at?: string;
}

export interface WorkDetailV2 {
  work: WorkItem;
  targets: WorkTarget[] | null;
}

export interface WorkPageV2 {
  work: WorkItem[] | null;
  next_cursor?: string;
}

export interface RoutineOverview {
  active_work: number;
  needs_attention: number;
  completed_last_24h: number;
  workers_online: number;
  workers_total: number;
  run_metrics: {
    window: "24h";
    total_runs: number;
    completed_runs: number;
    completion_rate: number | null;
    average_queue_time_seconds: number | null;
    average_cycle_time_seconds: number | null;
  };
  recent_work: WorkItem[] | null;
  upcoming_routines: Routine[] | null;
  generated_at: string;
}

export interface Capability {
  kind: "tool" | "runtime";
  name: string;
  status: "ready" | "missing" | "unauthenticated" | "unhealthy";
  version?: string;
  message?: string;
}

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
  labels?: Record<string, string>;
  worker_version: string;
  runtime: Runtime;
  runtime_version: string;
  capabilities?: Capability[];
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
  current_work_title?: string;
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
