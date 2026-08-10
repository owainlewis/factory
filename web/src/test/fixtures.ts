import type { AutomationDetail, AutomationOccurrence, Definition, LegacyPollerMigration, ManagedRepository, MetricsSummary, ProductUpgrade, Run, RunDetail, Task, Worker, Workflow, WorkflowDetail } from "../types";
import { vi } from "vitest";

export const worker: Worker = {
  id: "worker-online",
  name: "Build Mac",
  worker_version: "2.0.0",
  runtime: "codex",
  runtime_version: "0.42.0",
	capabilities: [
		{ kind: "tool", name: "git", status: "ready", version: "git version 2.50.1" },
		{ kind: "tool", name: "gh", status: "ready", version: "gh version 2.94.0" },
		{ kind: "runtime", name: "pi", status: "ready", version: "0.80.10" },
		{ kind: "runtime", name: "codex", status: "ready", version: "0.42.0" },
		{ kind: "runtime", name: "claude-code", status: "missing", message: "Install Claude Code and make it available on PATH." },
	],
  capacity: 10,
  active_count: 6,
  health: "healthy",
  online: true,
  source_access: [{ provider: "github", hostname: "github.com" }],
  accepts_managed_repositories: true,
  repository_cache_count: 1,
  repositories: [
    { id: "repo-factory", key: "factory", remote_identity: "github.com/example/factory", retained_count: 0 },
    { id: "repo-docs", key: "docs", remote_identity: "github.com/example/docs", retained_count: 0 },
  ],
  retained_worktrees: [],
  registered_at: "2026-07-29T10:00:00Z",
  last_heartbeat: new Date().toISOString(),
  current_task_title: "Implement control plane",
};

export const managedRepositories: ManagedRepository[] = [
  {
    id: "repo-factory",
    remote_identity: "github.com/example/factory",
    enabled: true,
    created_at: "2026-07-29T10:00:00Z",
    updated_at: "2026-07-29T10:00:00Z",
  },
  {
    id: "repo-managed",
    remote_identity: "github.com/example/managed",
    enabled: true,
    created_at: "2026-07-29T10:00:00Z",
    updated_at: "2026-07-29T10:00:00Z",
  },
  {
    id: "repo-disabled",
    remote_identity: "github.com/example/disabled",
    enabled: false,
    created_at: "2026-07-29T10:00:00Z",
    updated_at: "2026-07-29T11:00:00Z",
  },
];

export const offlineWorker: Worker = {
  ...worker,
  id: "worker-offline",
  name: "Offline Mac",
  online: false,
  active_count: 0,
  repositories: [
    { id: "repo-offline", key: "archive", remote_identity: "github.com/example/archive", retained_count: 2 },
  ],
  current_task_title: undefined,
};

export const tasks: Task[] = ["queued", "running", "succeeded", "failed", "cancelled"].map(
  (state, index) => ({
    id: `task-${state}`,
    request_key: `request-${state}`,
    title: `${state} task`,
    worker_id: worker.id,
    repository_id: worker.repositories[0].id,
    required_runtime: index === 0 ? "pi" : "codex",
    timeout_seconds: 7200,
    state: state as Task["state"],
    created_at: new Date(Date.now() - index * 60_000).toISOString(),
  }),
);

export const metrics: MetricsSummary = {
  window: "7d",
  generated_at: new Date().toISOString(),
  executions_created: 53,
  executions_completed: 41,
  succeeded: 34,
  failed: 6,
  cancelled: 1,
  queued: 6,
  running: 4,
  success_rate: 0.85,
  retry_rate: 0.09,
  median_cycle_time_seconds: 14 * 60,
  workers_online: 3,
  workers_total: 4,
  weekly_limit: {
    used_percent: 14,
    resets_at: "2026-08-11T08:51:00Z",
  },
  run_health: {
    total_jobs: 12,
    active: 3,
    blocked: 2,
    succeeded: 5,
    failed: 2,
    cancelled: 0,
    success_rate: 5 / 7,
    average_queue_time_seconds: 90,
    average_cycle_time_seconds: 14 * 60,
    throughput: 7,
    jobs: [
      {
        job_id: "job-health-failed",
        run_id: "run-health-failed",
        definition_id: "definition-health",
        definition_name: "Find confirmed bugs",
        repository_id: "repo-factory",
        repository_remote_identity: "github.com/example/factory",
        worker_id: "worker-online",
        worker_name: "Mac Studio",
        state: "failed",
        admitted_at: "2026-08-06T09:00:00Z",
        started_at: "2026-08-06T09:01:30Z",
        terminal_at: "2026-08-06T09:14:00Z",
      },
      {
        job_id: "job-health-succeeded",
        run_id: "run-health-succeeded",
        definition_id: "definition-health",
        definition_name: "Find confirmed bugs",
        repository_id: "repo-managed",
        repository_remote_identity: "github.com/example/managed",
        worker_id: "worker-online",
        worker_name: "Mac Studio",
        state: "succeeded",
        admitted_at: "2026-08-06T08:00:00Z",
        started_at: "2026-08-06T08:00:30Z",
        terminal_at: "2026-08-06T08:10:00Z",
      },
    ],
    definitions: [{ id: "definition-health", name: "Find confirmed bugs" }],
    repositories: [
      { id: "repo-factory", name: "github.com/example/factory" },
      { id: "repo-managed", name: "github.com/example/managed" },
    ],
    workers: [{ id: "worker-online", name: "Mac Studio" }],
  },
};

const initialWorkflowDetail: WorkflowDetail = {
  workflow: {
    id: "workflow-implement",
    enabled: true,
    current_revision: {
      id: "workflow-revision-1",
      workflow_id: "workflow-implement",
      revision_number: 1,
      title: "Implement",
      summary: "Implement and verify one change.",
      instructions: "Implement the change and run the required checks.",
      created_at: "2026-08-01T08:00:00Z",
    },
    created_at: "2026-08-01T08:00:00Z",
    updated_at: "2026-08-01T08:00:00Z",
  },
  revisions: [],
};
initialWorkflowDetail.revisions = [initialWorkflowDetail.workflow.current_revision];

const historicalWorkflow: Workflow = {
  id: "workflow-history",
  enabled: true,
  current_revision: {
    id: "workflow-history-revision-1",
    workflow_id: "workflow-history",
    revision_number: 1,
    title: "Historical workflow",
    summary: "An older loaded page.",
    instructions: "Use the historical instructions.",
    created_at: "2026-07-31T08:00:00Z",
  },
  created_at: "2026-07-31T08:00:00Z",
  updated_at: "2026-07-31T08:00:00Z",
};

const initialAutomationDetail: AutomationDetail = {
  automation: {
    id: "automation-ready",
    title: "Ready issues",
    workflow_id: "workflow-implement",
    workflow_title: "Implement",
    workflow_revision: 1,
    repository_id: "repo-factory",
    repository_identity: "github.com/example/factory",
    context: "Implement the issue and open a reviewed pull request.",
    timeout_seconds: 7200,
    enabled: false,
    version: 1,
    trigger: {
      type: "github_issue",
      state: "open",
      required_labels: ["needs-agent"],
      poll_interval_seconds: 30,
    },
    health: { status: "disabled", message: "Automation is disabled." },
    matched_count: 0,
    skipped_count: 0,
    dispatched_count: 0,
    created_at: "2026-08-01T08:00:00Z",
    updated_at: "2026-08-01T08:00:00Z",
  },
  occurrences: [],
};

export function mockControlPlane(
  options: {
    createFailures?: number;
    boundedLiveHead?: boolean;
    commandOnlyMigration?: boolean;
    failedLegacyOccurrence?: boolean;
    eventFailuresAfter?: number;
    growingTaskHistory?: boolean;
    incrementalEvents?: boolean;
    definitionHeadRefreshGate?: Promise<void>;
    definitionHistoryGate?: Promise<void>;
    definitionListFailure?: boolean;
    ledgerOnlyMigration?: boolean;
    automationRunWithoutTaskState?: "pending" | "failed" | "skipped" | "task_deleted";
    automationTaskState?: Task["state"];
    activeHistoricalRunCompletesAfterPoll?: boolean;
    multiRepositoryAutomations?: boolean;
    paginatedAutomations?: boolean;
    paginatedAutomationOccurrences?: boolean;
    paginatedAutomationWorkflows?: boolean;
    paginatedDefinitions?: boolean;
    paginatedRuns?: boolean;
    runCreateFailures?: number;
    pendingRunCancellation?: boolean;
    paginatedTasks?: boolean;
    refreshesHistoricalWorkflow?: boolean;
    shiftingWorkflowBoundary?: boolean;
    shiftingTaskBoundary?: boolean;
    staleHistoryAfterDelete?: boolean;
    staleTaskHeadAfterDelete?: boolean;
    switchAttemptAfter?: number;
    taskDetailFailuresAfter?: number;
    terminalEventFailures?: number;
    terminalTaskAfter?: number;
    terminalRunCancellationFlag?: boolean;
    repositoryToggleFailure?: boolean;
    runFailures?: number;
    scheduleDefinition?: boolean;
    workflowHistoryGate?: Promise<void>;
    workerDetailFailuresAfter?: number;
    workerFailure?: boolean;
    workerDetailRuntimeRefresh?: boolean;
    workerOfflineAfterConnectionTest?: boolean;
    workerRuntimeRefresh?: boolean;
	productUpgrade?: "ready" | "draining" | "completed";
	productUpgradeCompletesAfterPoll?: boolean;
	productUpgradeFreezesAfterPoll?: boolean;
  } = {},
) {
  let createFailures = options.createFailures ?? 0;
  let runFailures = options.runFailures ?? 0;
  let runCreateFailures = options.runCreateFailures ?? 0;
  let eventRequests = 0;
  let taskHeadRequests = 0;
  let historicalRunDetailRequests = 0;
  let definitionHeadRequests = 0;
  let workflowHeadRequests = 0;
  let terminalEventFailures = options.terminalEventFailures ?? 0;
  let taskDetailRequests = 0;
  let workerDetailRequests = 0;
  let workerConnectionTests = 0;
  let workerListRequests = 0;
  const deletedTaskIDs = new Set<string>();
  let resolveStaleHistory: (() => void) | undefined;
  let resolveStaleTaskHead: (() => void) | undefined;
  let createdTask: {
    title: string;
    context: string;
    description: string;
    workflow?: { id: string; revision_id: string; title: string; revision_number: number };
    resolvedPrompt?: string;
  } = {
    title: "Ship the UI",
    context: "Build and verify the real interface.",
    description: "Build and verify the real interface.",
  };
  let repositoryItems = managedRepositories.map((repository) => ({ ...repository }));
  let definitionItems: Definition[] = options.paginatedDefinitions ? [
    {
      id: "definition-head",
      name: "Head Definition",
      prompt: "Inspect the current repository.",
      runtime: "codex",
      allowed_tools: ["git"],
      timeout_seconds: 600,
      inputs: {},
      generation: 1,
      archived: false,
      created_at: "2026-08-06T10:00:00Z",
      updated_at: "2026-08-06T10:00:00Z",
    },
    {
      id: "definition-history",
      name: "Historical Definition",
      prompt: "Inspect an older repository.",
      runtime: "codex",
      allowed_tools: ["git"],
      timeout_seconds: 600,
      inputs: {},
      generation: 1,
      archived: false,
      created_at: "2026-08-05T10:00:00Z",
      updated_at: "2026-08-05T10:00:00Z",
    },
  ] : options.scheduleDefinition ? [{
    id: "definition-maintenance",
    name: "Repository maintenance",
    prompt: "Inspect {{scope}} and fix confirmed problems.",
    runtime: "codex",
    allowed_tools: ["git", "gh"],
    timeout_seconds: 1800,
    inputs: { scope: "the repository" },
    generation: 2,
    archived: false,
    created_at: "2026-08-06T10:00:00Z",
    updated_at: "2026-08-06T10:00:00Z",
  }] : [];
  let runDetails: RunDetail[] = [];
	let productUpgrade: ProductUpgrade = options.productUpgrade ? {
		id: "definitions-runs-v1",
		state: options.productUpgrade,
		legacy_read_only: options.productUpgrade !== "ready",
		needed: true,
		counts: {
			legacy_tasks: 5,
			legacy_attempts: 3,
			legacy_workflows: 1,
			legacy_workflow_revisions: 2,
			compatible_schedules: 1,
			github_polling_automations: 1,
			pending_occurrences: 0,
			active_executions: 0,
		},
		schedules: [{
			automation_id: "legacy-schedule",
			title: "Weekly review",
			definition_name: "Weekly review · legacy-schedule",
			repository_id: "repo-factory",
			cron: "0 9 * * 1",
			timezone: "UTC",
			next_due_at: "2026-08-17T09:00:00Z",
			enabled: true,
		}],
		polling_automations: [{
			automation_id: "legacy-poller",
			title: "Issue triage poller",
			trigger_type: "github_issue",
			guidance: "Replace this poller with a scheduled Definition that uses gh, configure a GitHub webhook, or leave it retired.",
		}],
		decisions: ["GitHub polling Automations remain readable and retired."],
	} : {
		id: "definitions-runs-v1",
		state: "ready",
		legacy_read_only: false,
		needed: false,
		counts: {
			legacy_tasks: 0, legacy_attempts: 0, legacy_workflows: 0,
			legacy_workflow_revisions: 0, compatible_schedules: 0,
			github_polling_automations: 0, pending_occurrences: 0, active_executions: 0,
		},
		schedules: [], polling_automations: [], decisions: [],
	};
	let productUpgradeRequests = 0;
  let workflowDetail = structuredClone(initialWorkflowDetail);
  let automationDetail = structuredClone(initialAutomationDetail);
  if (options.automationTaskState) {
    const task = {
      id: "task-automation-run",
      title: "Review pull request #184",
      state: options.automationTaskState,
    };
    automationDetail = {
      automation: {
        ...automationDetail.automation,
        dispatched_count: 1,
        latest_task: task,
        latest_run: undefined,
      },
      occurrences: [{
        id: "automation-run",
        automation_id: automationDetail.automation.id,
        automation_version: 1,
        state: "dispatched",
        issue_number: 184,
        issue_url: "https://github.com/example/factory/issues/184",
        issue_title: "Typed Automation run state",
        observed_state: "open",
        observed_labels: ["needs-agent"],
        task_request_key: "automation:automation-ready:github_issue:184",
        task,
        task_id_snapshot: task.id,
        created_at: "2026-08-01T08:00:00Z",
        updated_at: "2026-08-01T08:00:00Z",
      }],
    };
    automationDetail.automation.latest_run = automationDetail.occurrences[0];
  }
  if (options.automationRunWithoutTaskState) {
    const olderTask = {
      id: "task-older-automation-run",
      title: "Older dispatched task",
      state: "succeeded",
    };
    const latestRun: AutomationOccurrence = {
      id: "automation-run-without-task",
      automation_id: automationDetail.automation.id,
      automation_version: 1,
      state: options.automationRunWithoutTaskState,
      issue_number: 185,
      issue_url: "https://github.com/example/factory/issues/185",
      issue_title: "Newest run without task",
      observed_state: "open",
      observed_labels: ["needs-agent"],
      task_request_key: "automation:automation-ready:github_issue:185",
      task_id_snapshot: options.automationRunWithoutTaskState === "task_deleted" ? "task-deleted" : undefined,
      diagnostic: options.automationRunWithoutTaskState === "failed" ? "No eligible worker." : undefined,
      created_at: "2026-08-02T08:00:00Z",
      updated_at: "2026-08-02T08:00:00Z",
    };
    automationDetail = {
      automation: {
        ...automationDetail.automation,
        latest_task: olderTask,
        latest_run: latestRun,
      },
      occurrences: [latestRun],
    };
  }
  let legacyMigration: LegacyPollerMigration | undefined;
  const automationOccurrence = (id: string, issue: number, createdAt: string): AutomationOccurrence => ({
    id,
    automation_id: automationDetail.automation.id,
    automation_version: 1,
    state: "pending",
    issue_number: issue,
    issue_url: `https://github.com/example/factory/issues/${issue}`,
    issue_title: `Paged issue ${issue}`,
    observed_state: "open",
    observed_labels: ["needs-agent"],
    task_request_key: `automation:${automationDetail.automation.id}:github_issue:${issue}`,
    created_at: createdAt,
    updated_at: createdAt,
  });
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path =
      typeof input === "string"
        ? input
        : input instanceof URL
          ? input.toString()
          : input.url;
    if (path.startsWith("/api/v1/metrics/summary?window=")) {
      const window = new URL(path, "http://factory.test").searchParams.get("window");
      return Response.json({ ...metrics, window });
    }
	if (path === "/api/v1/migrations/product-model") {
		productUpgradeRequests += 1;
		if (options.productUpgradeFreezesAfterPoll && productUpgradeRequests > 1) {
			productUpgrade = { ...productUpgrade, state: "draining", legacy_read_only: true };
		}
		if (options.productUpgradeCompletesAfterPoll && productUpgradeRequests > 1) {
			productUpgrade = { ...productUpgrade, state: "completed", legacy_read_only: true };
		}
		return Response.json(productUpgrade);
	}
	if (path === "/api/v1/migrations/product-model/apply" && init?.method === "POST") {
		productUpgrade = {
			...productUpgrade,
			state: "completed",
			legacy_read_only: true,
			validation: {
				definitions_created: 1,
				schedules_converted: 1,
				polling_automations_retired: 1,
				legacy_tasks_retained: 5,
				legacy_occurrences_retained: 2,
				legacy_attempts_retained: 3,
				synthetic_runs_created: 0,
				validated_at: "2026-08-10T10:00:00Z",
			},
		};
		return Response.json(productUpgrade);
	}
    if (path === "/api/v1/run-repositories") {
      return Response.json({
        repositories: repositoryItems
          .filter((repository) => repository.enabled)
          .map(({ id, remote_identity }) => ({ id, remote_identity })),
      });
    }
    if (path === "/api/v1/repositories") {
      if (init?.method === "POST") {
        const body = JSON.parse(String(init.body)) as { remote_identity: string };
        if (!/^github\.com\/[^/]+\/[^/]+$/.test(body.remote_identity)) {
          return Response.json(
            { error: { code: "invalid_repository", message: "remote_identity must use the canonical github.com/owner/repository form" } },
            { status: 400 },
          );
        }
        if (body.remote_identity === "github.com/example/over-limit") {
          return Response.json(
            { error: { code: "repository_limit_reached", message: "the managed repository limit has been reached" } },
            { status: 409 },
          );
        }
        const existing = repositoryItems.find((item) => item.remote_identity === body.remote_identity);
        if (existing) return Response.json(existing);
        const created: ManagedRepository = {
          id: "repo-created",
          remote_identity: body.remote_identity,
          enabled: true,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        };
        repositoryItems = [...repositoryItems, created];
        return Response.json(created, { status: 201 });
      }
      return Response.json({ repositories: repositoryItems });
    }
    if (path.startsWith("/api/v1/repositories/")) {
      const parts = path.split("/");
      const repositoryID = parts[4];
      const existing = repositoryItems.find((item) => item.id === repositoryID);
      if (!existing) return Response.json({ error: { code: "not_found", message: "not found" } }, { status: 404 });
      if (parts[5] === "readiness") {
        const enabled = existing.enabled;
        return Response.json({
          routing_ready: enabled,
          workers: [
            {
              id: worker.id,
              name: worker.name,
              cached: existing.id === "repo-factory",
              advertised: existing.id === "repo-factory",
              ready: enabled,
              reason: enabled
                ? "Online, healthy, with GitHub access and managed cache headroom."
                : "Repository routing is disabled.",
            },
            {
              id: offlineWorker.id,
              name: offlineWorker.name,
              cached: false,
              advertised: false,
              ready: false,
              reason: enabled ? "Worker is offline." : "Repository routing is disabled.",
            },
          ],
        });
      }
      if (parts[5] === "enabled" && init?.method === "PUT") {
        if (options.repositoryToggleFailure) {
          return Response.json(
            { error: { code: "storage_unavailable", message: "repository update could not be saved" } },
            { status: 503 },
          );
        }
        const body = JSON.parse(String(init.body)) as { enabled: boolean };
        const updated = { ...existing, enabled: body.enabled, updated_at: new Date().toISOString() };
        repositoryItems = repositoryItems.map((item) => item.id === updated.id ? updated : item);
        return Response.json(updated);
      }
      return Response.json(existing);
    }
    if (path.startsWith("/api/v1/definitions?")) {
      if (options.definitionListFailure) {
        return Response.json(
          { error: { code: "storage_unavailable", message: "Definitions could not be loaded" } },
          { status: 503 },
        );
      }
      const query = new URL(path, "http://factory.test").searchParams;
      const archived = query.get("archived") === "true";
      if (options.paginatedDefinitions && !archived) {
        if (query.get("cursor") === "definition-history") {
          const historicalDefinitions = definitionItems.filter((definition) =>
            !definition.archived && definition.id === "definition-history"
          );
          await options.definitionHistoryGate;
          return Response.json({
            definitions: historicalDefinitions,
            next_cursor: null,
          });
        }
        definitionHeadRequests += 1;
        if (definitionHeadRequests > 1) await options.definitionHeadRefreshGate;
        const visible = definitionItems.filter((definition) => !definition.archived);
        const hasHistory = visible.some((definition) => definition.id === "definition-history");
        return Response.json({
          definitions: visible.filter((definition) => definition.id === "definition-head"),
          next_cursor: hasHistory ? "definition-history" : null,
        });
      }
      const visible = definitionItems.filter((definition) => definition.archived === archived);
      return Response.json({
        definitions: visible,
        next_cursor: null,
      });
    }
    if (path === "/api/v1/definitions" && init?.method === "POST") {
      const body = JSON.parse(String(init.body)) as Omit<Definition, "id" | "generation" | "archived" | "created_at" | "updated_at">;
      const now = new Date().toISOString();
      const definition: Definition = {
        ...body,
        id: "definition-created",
        generation: 1,
        archived: false,
        created_at: now,
        updated_at: now,
      };
      definitionItems = [definition, ...definitionItems];
      return Response.json(definition, { status: 201 });
    }
    if (path.startsWith("/api/v1/definitions/")) {
      const parts = path.split("/");
      const definitionID = parts[4];
      const existing = definitionItems.find((definition) => definition.id === definitionID);
      if (!existing) return Response.json({ error: { code: "not_found", message: "not found" } }, { status: 404 });
      if (parts[5] === "archived" && init?.method === "PUT") {
        const body = JSON.parse(String(init.body)) as { archived: boolean; expected_generation: number };
        if (body.archived === existing.archived) return Response.json(existing);
        if (body.expected_generation !== existing.generation) {
          return Response.json(
            { error: { code: "definition_generation_conflict", message: "the Definition was edited by another request" } },
            { status: 409 },
          );
        }
        const updated = {
          ...existing,
          archived: body.archived,
          generation: existing.generation + 1,
          updated_at: new Date().toISOString(),
        };
        definitionItems = definitionItems.map((definition) => definition.id === updated.id ? updated : definition);
        return Response.json(updated);
      }
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body)) as Definition & { expected_generation: number };
        if (body.expected_generation !== existing.generation) {
          return Response.json(
            { error: { code: "definition_generation_conflict", message: "the Definition was edited by another request" } },
            { status: 409 },
          );
        }
        const updated: Definition = {
          ...existing,
          ...body,
          id: existing.id,
          generation: existing.generation + 1,
          archived: existing.archived,
          created_at: existing.created_at,
          updated_at: new Date().toISOString(),
        };
        definitionItems = definitionItems.map((definition) => definition.id === updated.id ? updated : definition);
        return Response.json(updated);
      }
      return Response.json(existing);
    }
    if (path === "/api/v1/runs" && init?.method === "POST") {
      const body = JSON.parse(String(init.body)) as {
        request_key: string;
        definition_id: string;
        repository_ids: string[];
        concurrency_limit: number;
        parameters?: Record<string, string>;
      };
      const replay = runDetails.find((detail) => detail.run.request_key === body.request_key);
      if (replay) return Response.json(replay);
      const definition = definitionItems.find((item) => item.id === body.definition_id);
      const repositories = body.repository_ids.map((id) => repositoryItems.find((item) => item.id === id));
      if (!definition || repositories.length === 0 || repositories.some((repository) => !repository)) {
        return Response.json({ error: { code: "not_found", message: "Run input was not found" } }, { status: 404 });
      }
      const selectedRepositories = repositories as ManagedRepository[];
      const now = new Date().toISOString();
      const detail: RunDetail = {
        run: {
          id: "run-created",
          request_key: body.request_key,
          source_kind: "manual",
          definition: {
            id: definition.id,
            name: definition.name,
            prompt: definition.prompt,
            runtime: definition.runtime,
            allowed_tools: definition.allowed_tools,
            timeout_seconds: definition.timeout_seconds,
            inputs: definition.inputs,
            generation: definition.generation,
          },
          state: options.pendingRunCancellation ? "running" : "queued",
          job_count: selectedRepositories.length,
          concurrency_limit: body.concurrency_limit,
          repository_remote_identities: selectedRepositories.map((repository) => repository.remote_identity),
          admitted_at: now,
          updated_at: now,
        },
        parameters: { ...definition.inputs, ...body.parameters },
        jobs: selectedRepositories.map((repository, index) => ({
          job: {
            id: `job-created-${index + 1}`,
            run_id: "run-created",
            repository_id: repository.id,
            repository_remote_identity: repository.remote_identity,
            task_id: `run-task-created-${index + 1}`,
            execution_id: `run-execution-created-${index + 1}`,
            assigned_worker_id: worker.id,
            required_runtime: definition.runtime,
            state: options.pendingRunCancellation ? "running" : "queued",
            admitted_at: now,
            retry_may_repeat_effects: false,
            cancellation_requested: false,
          },
          attempts: null,
          resolved_prompt: definition.prompt,
        })),
      };
      runDetails = [detail, ...runDetails];
      if (runCreateFailures > 0) {
        runCreateFailures -= 1;
        return Response.json(
          { error: { code: "connection_lost", message: "connection lost after Run commit" } },
          { status: 503 },
        );
      }
      return Response.json(detail, { status: 201 });
    }
    if (path.startsWith("/api/v1/runs?")) {
      if (options.paginatedRuns) {
        const query = new URL(path, "http://factory.test").searchParams;
        const makeRun = (id: string, name: string, admittedAt: string, state: Run["state"] = "succeeded"): Run => ({
          id,
          request_key: `request-${id}`,
          source_kind: "manual",
          definition: {
            id: `definition-${id}`,
            name,
            prompt: "Inspect the configured repository.",
            runtime: "codex",
            allowed_tools: ["git"],
            timeout_seconds: 600,
            inputs: {},
            generation: 1,
          },
          state,
          job_count: 1,
          concurrency_limit: 1,
          repository_remote_identities: ["github.com/example/factory"],
          admitted_at: admittedAt,
          updated_at: admittedAt,
        });
        if (query.get("cursor") === "run-history") {
          return Response.json({
            runs: [
              makeRun(
                "run-history",
                "Older review",
                "2026-08-05T10:00:00Z",
                options.activeHistoricalRunCompletesAfterPoll ? "running" : "succeeded",
              ),
              ...(options.activeHistoricalRunCompletesAfterPoll ? [makeRun(
                "run-history-active",
                "Long-running review",
                "2026-08-04T10:00:00Z",
                "running",
              )] : []),
            ],
            next_cursor: null,
          });
        }
        return Response.json({
          runs: [makeRun("run-head", "Recent review", "2026-08-06T10:00:00Z")],
          next_cursor: "run-history",
        });
      }
      return Response.json({ runs: runDetails.map((detail) => detail.run), next_cursor: null });
    }
    if (path.startsWith("/api/v1/runs/")) {
      const runID = path.split("/")[4];
      if ((runID === "run-history" || runID === "run-history-active") && options.activeHistoricalRunCompletesAfterPoll) {
        if (runID === "run-history") historicalRunDetailRequests += 1;
        const state: Run["state"] = runID === "run-history" && historicalRunDetailRequests > 1 ? "succeeded" : "running";
        const historicalActive = runID === "run-history-active";
        const admittedAt = historicalActive ? "2026-08-04T10:00:00Z" : "2026-08-05T10:00:00Z";
        return Response.json({
          run: {
            id: runID,
            request_key: `request-${runID}`,
            source_kind: "manual",
            definition: {
              id: `definition-${runID}`,
              name: historicalActive ? "Long-running review" : "Older review",
              prompt: "Inspect the configured repository.",
              runtime: "codex",
              allowed_tools: ["git"],
              timeout_seconds: 600,
              inputs: {},
              generation: 1,
            },
            state,
            job_count: 1,
            concurrency_limit: 1,
            repository_remote_identities: ["github.com/example/factory"],
            admitted_at: admittedAt,
            updated_at: admittedAt,
          },
          parameters: {},
          jobs: [],
        });
      }
      const detail = runDetails.find((item) => item.run.id === runID);
      return detail
        ? Response.json(detail)
        : Response.json({ error: { code: "not_found", message: "not found" } }, { status: 404 });
    }
    if (path.startsWith("/api/v1/jobs/") && init?.method === "POST") {
      const parts = path.split("/");
      const jobID = parts[4];
      const detail = runDetails.find((item) => item.jobs.some((job) => job.job.id === jobID));
      if (!detail) return Response.json({ error: { code: "not_found", message: "not found" } }, { status: 404 });
      const cancellationPending = parts[5] === "cancel" && Boolean(options.pendingRunCancellation);
      const jobState = cancellationPending ? detail.jobs[0].job.state : parts[5] === "cancel" ? "cancelled" as const : "queued" as const;
      const runState: Run["state"] = cancellationPending ? detail.run.state : parts[5] === "cancel" ? "cancelled" : "queued";
      const updated: RunDetail = {
        ...detail,
        run: { ...detail.run, state: runState, updated_at: new Date().toISOString() },
        jobs: detail.jobs.map((job) => job.job.id === jobID ? {
          ...job,
          job: {
            ...job.job,
            state: jobState,
            cancellation_requested: cancellationPending || Boolean(options.terminalRunCancellationFlag),
          },
        } : job),
      };
      runDetails = runDetails.map((item) => item.run.id === updated.run.id ? updated : item);
      return Response.json(updated);
    }
    if (path.startsWith("/api/v1/workflows?limit=200")) {
      const query = new URL(path, "http://factory.test").searchParams;
      const enabled = query.get("enabled");
      if (options.paginatedAutomationWorkflows && !enabled) {
        if (query.get("cursor") === "automation-workflow-history") {
          return Response.json({ workflows: [historicalWorkflow], next_cursor: null });
        }
        return Response.json({ workflows: [workflowDetail.workflow], next_cursor: "automation-workflow-history" });
      }
      if (options.shiftingWorkflowBoundary && !enabled) {
        const cursor = query.get("cursor");
        if (cursor === "old-workflow-boundary") {
          await options.workflowHistoryGate;
          return Response.json({ workflows: [historicalWorkflow], next_cursor: "stale-workflow-boundary" });
        }
        if (cursor === "new-workflow-boundary") {
          const shiftedBoundary: Workflow = {
            ...historicalWorkflow,
            id: "workflow-shifted-boundary",
            current_revision: {
              ...historicalWorkflow.current_revision,
              id: "workflow-shifted-boundary-revision-1",
              workflow_id: "workflow-shifted-boundary",
              title: "Shifted boundary workflow",
            },
          };
          return Response.json({ workflows: [shiftedBoundary], next_cursor: null });
        }
        workflowHeadRequests += 1;
        return Response.json({
          workflows: workflowHeadRequests === 1
            ? [workflowDetail.workflow]
            : [{ ...historicalWorkflow, updated_at: "2026-08-01T09:00:00Z" }, workflowDetail.workflow],
          next_cursor: workflowHeadRequests === 1 ? "old-workflow-boundary" : "new-workflow-boundary",
        });
      }
      if (options.refreshesHistoricalWorkflow && !enabled) {
        if (query.get("cursor") === "workflow-history-page") {
          return Response.json({ workflows: [historicalWorkflow], next_cursor: null });
        }
        workflowHeadRequests += 1;
        const refreshedHistorical: Workflow = {
          ...historicalWorkflow,
          enabled: false,
          current_revision: {
            ...historicalWorkflow.current_revision,
            id: "workflow-history-revision-2",
            revision_number: 2,
            title: "Refreshed workflow",
            summary: "Fresh head data wins.",
          },
          updated_at: "2026-08-01T09:00:00Z",
        };
        return Response.json({
          workflows: workflowHeadRequests === 1
            ? [workflowDetail.workflow]
            : [refreshedHistorical, workflowDetail.workflow],
          next_cursor: workflowHeadRequests === 1 ? "workflow-history-page" : null,
        });
      }
      return Response.json({
        workflows: enabled === "true" && !workflowDetail.workflow.enabled ? [] : [workflowDetail.workflow],
        next_cursor: null,
      });
    }
    if (path === "/api/v1/workflows") {
      if (init?.method === "POST") {
        const body = JSON.parse(String(init.body)) as Record<string, unknown>;
        workflowDetail = {
          workflow: {
            id: "workflow-created",
            enabled: true,
            current_revision: {
              id: "workflow-created-revision-1",
              workflow_id: "workflow-created",
              revision_number: 1,
              title: String(body.title),
              summary: String(body.summary),
              instructions: String(body.instructions),
              created_at: new Date().toISOString(),
            },
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
          },
          revisions: [],
        };
        workflowDetail.revisions = [workflowDetail.workflow.current_revision];
        return Response.json(workflowDetail, { status: 201 });
      }
    }
    if (path === `/api/v1/workflows/${workflowDetail.workflow.id}`) {
      return Response.json(workflowDetail);
    }
    if (path === `/api/v1/workflows/${workflowDetail.workflow.id}/revisions` && init?.method === "POST") {
      const body = JSON.parse(String(init.body)) as Record<string, unknown>;
      const revision = {
        id: "workflow-revision-2",
        workflow_id: workflowDetail.workflow.id,
        revision_number: workflowDetail.workflow.current_revision.revision_number + 1,
        title: String(body.title),
        summary: String(body.summary),
        instructions: String(body.instructions),
        created_at: new Date().toISOString(),
      };
      workflowDetail = {
        workflow: { ...workflowDetail.workflow, current_revision: revision, updated_at: revision.created_at },
        revisions: [revision, ...workflowDetail.revisions],
      };
      return Response.json(workflowDetail, { status: 201 });
    }
    if (path === `/api/v1/workflows/${workflowDetail.workflow.id}/enabled` && init?.method === "PUT") {
      const body = JSON.parse(String(init.body)) as { enabled: boolean };
      workflowDetail = { workflow: { ...workflowDetail.workflow, enabled: body.enabled }, revisions: workflowDetail.revisions };
      return Response.json(workflowDetail);
    }
    if (path === "/api/v1/automations?limit=200") {
      const docsAutomation = {
        ...automationDetail.automation,
        id: "automation-docs",
        title: "Docs review",
        repository_id: "repo-disabled",
        repository_identity: "github.com/example/disabled",
        enabled: true,
        health: { status: "healthy" as const, message: "Waiting for the next GitHub check." },
      };
      return Response.json({
        automations: options.multiRepositoryAutomations
          ? [automationDetail.automation, docsAutomation]
          : [automationDetail.automation],
        next_cursor: options.paginatedAutomations ? "automation-history" : null,
      });
    }
    if (path === "/api/v1/migrations/legacy-poller/active") {
      return Response.json({ migration: legacyMigration?.status === "imported" ? legacyMigration : null });
    }
    if (path === "/api/v1/migrations/legacy-poller/preview" && init?.method === "POST") {
      legacyMigration = {
        id: "legacy-migration",
        snapshot_digest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
        status: "previewed",
        config_path: "/tmp/legacy/poller.toml",
        data_home: "/tmp/legacy",
        working_directory: "/tmp/legacy",
        data_directory: "/tmp/legacy/poller",
        ledger_path: "/tmp/legacy/poller/poller.sqlite3",
        archive_root: "/tmp/legacy/archive",
        counts: {
          queues: options.ledgerOnlyMigration ? 2 : 1,
          supported_queues: options.commandOnlyMigration ? 0 : 1,
          unsupported_queues: options.commandOnlyMigration || options.ledgerOnlyMigration ? 1 : 0,
          pending_observations: options.commandOnlyMigration ? 1 : options.ledgerOnlyMigration ? 2 : 1,
          submitted_observations: options.commandOnlyMigration ? 1 : 0,
        },
        queues: [{
          queue_id: "legacy-queue",
          name: "github-ready",
          source: "github",
          project: "example/factory",
          state: "open",
          required_labels: ["needs-agent"],
          poll_interval_seconds: 30,
          timeout_seconds: 3600,
          repository_id: "repo-factory",
          repository_identity: "github.com/example/factory",
          workflow_title: "Legacy github-ready",
          automation_title: "Legacy github-ready issues",
          pending_observations: 1,
          submitted_observations: options.commandOnlyMigration ? 1 : 0,
          supported: !options.commandOnlyMigration,
          blocking: false,
          errors: options.commandOnlyMigration ? ["Only built-in GitHub queues can be imported."] : [],
        }, ...(options.ledgerOnlyMigration ? [{
          queue_id: "removed-queue-ledger-id",
          name: "Removed legacy queue removed-queue-ledger-id",
          source: "ledger-only",
          project: "",
          state: "",
          required_labels: [],
          poll_interval_seconds: 0,
          timeout_seconds: 0,
          workflow_title: "",
          automation_title: "",
          pending_observations: 1,
          submitted_observations: 0,
          supported: false,
          blocking: true,
          errors: ["Ledger observations reference queue ID removed-queue-ledger-id which is missing from poller.toml; restore the matching queue before Import."],
        }] : [])],
        automations: [],
        occurrences: [],
        errors: [],
        created_at: "2026-08-01T08:00:00Z",
        updated_at: "2026-08-01T08:00:00Z",
      };
      return Response.json(legacyMigration);
    }
    if (path === "/api/v1/migrations/legacy-poller/import" && init?.method === "POST") {
      if (!legacyMigration) throw new Error("legacy migration was not previewed");
      const body = JSON.parse(String(init.body)) as { mappings: Array<{ workflow_title: string; automation_title: string }> };
      const importedAutomation = body.mappings.length > 0 ? {
        ...automationDetail.automation,
        id: "automation-imported",
        title: body.mappings[0].automation_title,
        workflow_title: body.mappings[0].workflow_title,
        enabled: false,
      } : undefined;
      legacyMigration = {
        ...legacyMigration,
        status: "imported",
        automations: importedAutomation ? [importedAutomation] : [],
        occurrences: importedAutomation ? [{
          ...automationOccurrence("legacy-occurrence", 187, "2026-08-01T08:00:00Z"),
          automation_id: importedAutomation.id,
          issue_title: "Retire factory-poller",
          task_request_key: "legacy:issue:187",
          state: options.failedLegacyOccurrence ? "failed" : "pending",
          diagnostic: options.failedLegacyOccurrence ? "legacy_pending_invalid_requires_skip" : undefined,
        }] : [],
      };
      return Response.json(legacyMigration);
    }
    if (path === "/api/v1/occurrences/legacy-occurrence/skip" && init?.method === "POST") {
      if (!legacyMigration) throw new Error("legacy migration was not imported");
      legacyMigration = {
        ...legacyMigration,
        counts: { ...legacyMigration.counts, pending_observations: 0 },
        occurrences: legacyMigration.occurrences.map((occurrence) => ({ ...occurrence, state: "skipped" })),
      };
      return Response.json(legacyMigration.occurrences[0]);
    }
    if (path === "/api/v1/migrations/legacy-poller/legacy-migration") {
      if (!legacyMigration) throw new Error("legacy migration does not exist");
      return Response.json(legacyMigration);
    }
    if (path === "/api/v1/migrations/legacy-poller/legacy-migration/finalize" && init?.method === "POST") {
      if (!legacyMigration) throw new Error("legacy migration was not imported");
      legacyMigration = {
        ...legacyMigration,
        status: "finalized",
        archive_path: "/tmp/legacy/archive/legacy-migration",
      };
      return Response.json(legacyMigration);
    }
    if (path === "/api/v1/automations?limit=200&cursor=automation-history") {
      return Response.json({
        automations: [{
          ...automationDetail.automation,
          id: "automation-history",
          title: "Historical Automation",
          updated_at: "2026-07-01T08:00:00Z",
        }],
        next_cursor: null,
      });
    }
    if (path === "/api/v1/automations" && init?.method === "POST") {
      const body = JSON.parse(String(init.body)) as Record<string, unknown>;
      const trigger = body.trigger as AutomationDetail["automation"]["trigger"];
      const definitionTrigger = trigger.type === "schedule" || trigger.type === "github_webhook";
      const definition = definitionItems.find((item) => item.id === body.definition_id);
      const selectedRepositoryIDs = Array.isArray(body.repository_ids) ? body.repository_ids as string[] : [];
      automationDetail = {
        automation: {
          ...automationDetail.automation,
          id: "automation-created",
          title: String(body.title),
          workflow_id: definitionTrigger ? undefined : String(body.workflow_id),
          workflow_title: definitionTrigger ? undefined : automationDetail.automation.workflow_title,
          workflow_revision: definitionTrigger ? undefined : automationDetail.automation.workflow_revision,
          definition_id: definition?.id,
          definition_name: definition?.name,
          definition_generation: definition?.generation,
          repositories: selectedRepositoryIDs.map((id) => ({
            id,
            remote_identity: repositoryItems.find((item) => item.id === id)?.remote_identity ?? id,
          })),
          parameters: body.parameters as Record<string, string> | undefined,
          concurrency_limit: Number(body.concurrency_limit) || undefined,
          repository_id: definitionTrigger ? selectedRepositoryIDs[0] : String(body.repository_id),
          repository_identity: definitionTrigger ? repositoryItems.find((item) => item.id === selectedRepositoryIDs[0])?.remote_identity ?? "" : automationDetail.automation.repository_identity,
          context: definitionTrigger ? "" : String(body.context),
          timeout_seconds: definitionTrigger ? definition?.timeout_seconds ?? 1800 : Number(body.timeout_seconds),
          trigger,
          updated_at: new Date().toISOString(),
        },
        occurrences: [],
      };
      return Response.json(automationDetail, { status: 201 });
    }
    if (path === `/api/v1/automations/${automationDetail.automation.id}`) {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body)) as Record<string, unknown>;
        automationDetail = {
          ...automationDetail,
          automation: {
            ...automationDetail.automation,
            title: String(body.title),
            workflow_id: String(body.workflow_id),
            definition_id: body.definition_id ? String(body.definition_id) : automationDetail.automation.definition_id,
            definition_name: body.definition_id ? definitionItems.find((item) => item.id === body.definition_id)?.name : automationDetail.automation.definition_name,
            repositories: Array.isArray(body.repository_ids) ? (body.repository_ids as string[]).map((id) => ({ id, remote_identity: repositoryItems.find((item) => item.id === id)?.remote_identity ?? id })) : automationDetail.automation.repositories,
            parameters: body.parameters as Record<string, string> | undefined,
            concurrency_limit: Number(body.concurrency_limit) || automationDetail.automation.concurrency_limit,
            context: String(body.context),
            timeout_seconds: Number(body.timeout_seconds),
            trigger: body.trigger as AutomationDetail["automation"]["trigger"],
            version: automationDetail.automation.version + 1,
            updated_at: new Date().toISOString(),
          },
        };
      }
      return Response.json(automationDetail);
    }
    if (path === `/api/v1/automations/${automationDetail.automation.id}/occurrences?limit=50`) {
      return Response.json({
        occurrences: options.paginatedAutomationOccurrences
          ? [automationOccurrence("occurrence-head", 184, "2026-08-01T08:00:00Z")]
          : automationDetail.occurrences,
        next_cursor: options.paginatedAutomationOccurrences ? "occurrence-history" : null,
      });
    }
    if (path === `/api/v1/automations/${automationDetail.automation.id}/occurrences?limit=50&cursor=occurrence-history`) {
      return Response.json({
        occurrences: [automationOccurrence("occurrence-history", 183, "2026-07-01T08:00:00Z")],
        next_cursor: null,
      });
    }
    if (path === `/api/v1/automations/${automationDetail.automation.id}/test` && init?.method === "POST") {
      if (automationDetail.automation.trigger.type === "schedule") {
        return Response.json({ matches: [], next_due_at: "2026-08-03T08:00:00Z" });
      }
      if (automationDetail.automation.trigger.type === "github_pull_request") {
        return Response.json({ matches: [{
          number: 185,
          title: "Typed pull-request Automations",
          url: "https://github.com/example/factory/pull/185",
          state: "open",
          labels: ["needs-agent"],
          is_draft: false,
          base_branch: "main",
          head_commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        }] });
      }
      return Response.json({ matches: [{ number: 184, title: "Typed Automations", url: "https://github.com/example/factory/issues/184", state: "open", labels: ["needs-agent"] }] });
    }
    if (path === `/api/v1/automations/${automationDetail.automation.id}/enabled` && init?.method === "PUT") {
      const body = JSON.parse(String(init.body)) as { enabled: boolean };
      automationDetail = {
        ...automationDetail,
        automation: {
          ...automationDetail.automation,
          enabled: body.enabled,
          health: body.enabled
            ? { status: "pending", message: automationDetail.automation.trigger.type === "schedule" ? "Waiting for the next scheduled occurrence." : "Waiting for the next GitHub check." }
            : { status: "disabled", message: "Automation is disabled." },
          next_check_at: body.enabled && automationDetail.automation.trigger.type !== "schedule" ? new Date().toISOString() : undefined,
          next_due_at: body.enabled && automationDetail.automation.trigger.type === "schedule" ? "2026-08-03T08:00:00Z" : undefined,
        },
      };
      return Response.json(automationDetail);
    }
    if (path === `/api/v1/automations/${automationDetail.automation.id}/check` && init?.method === "POST") {
      automationDetail = {
        ...automationDetail,
        automation: {
          ...automationDetail.automation,
          health: { status: "checking", message: "Checking GitHub now." },
        },
      };
      return Response.json(automationDetail, { status: 202 });
    }
    if (path === `/api/v1/automations/${automationDetail.automation.id}/run` && init?.method === "POST") {
      const body = JSON.parse(String(init.body)) as { request_key: string };
      automationDetail = {
        ...automationDetail,
        occurrences: [{
          id: "schedule-run-now",
          automation_id: automationDetail.automation.id,
          automation_version: automationDetail.automation.version,
          state: "dispatched",
          kind: "run_now",
          run_request_key: body.request_key,
          cron: automationDetail.automation.trigger.type === "schedule" ? automationDetail.automation.trigger.cron : "",
          timezone: automationDetail.automation.trigger.type === "schedule" ? automationDetail.automation.trigger.timezone : "",
          run_id: "run-schedule-run-now",
          run_state: "succeeded",
          task_request_key: `automation:${automationDetail.automation.id}:schedule:run:${body.request_key}`,
          created_at: "2026-08-01T08:00:00Z",
          updated_at: "2026-08-01T08:00:00Z",
        }],
      };
      if (runFailures > 0) {
        runFailures -= 1;
        throw new Error("connection lost after Run now commit");
      }
      return Response.json(automationDetail, { status: 202 });
    }
    if (path === "/api/v1/tasks") {
      if (init?.method === "POST") {
        if (createFailures > 0) {
          createFailures -= 1;
          throw new Error("connection lost after submit");
        }
        const body = JSON.parse(String(init.body)) as Record<string, unknown>;
        const selectedWorkflow = body.workflow_revision_id
          ? {
              id: workflowDetail.workflow.id,
              revision_id: workflowDetail.workflow.current_revision.id,
              title: workflowDetail.workflow.current_revision.title,
              revision_number: workflowDetail.workflow.current_revision.revision_number,
            }
          : undefined;
        const context = selectedWorkflow ? String(body.context) : String(body.description);
        const resolvedPrompt = selectedWorkflow
          ? `Workflow instructions:\n\n${workflowDetail.workflow.current_revision.instructions}\n\nTask context:\n\n${context}`
          : context;
        createdTask = {
          title: String(body.title),
          context,
          description: resolvedPrompt,
          workflow: selectedWorkflow,
          resolvedPrompt,
        };
        return Response.json({
          task: { ...tasks[0], id: "created-task", title: body.title, description: resolvedPrompt },
          context,
          execution: { id: "execution-created", state: "queued" },
          repository: worker.repositories[0],
          repository_available: true,
          attempts: [],
          workflow: selectedWorkflow,
          resolved_prompt: resolvedPrompt,
        }, { status: 201 });
      }
    }
    if (path === "/api/v1/tasks?limit=50") {
      taskHeadRequests += 1;
      if (options.boundedLiveHead) {
        const newHead = {
          ...tasks[0],
          id: "task-new-head",
          request_key: "request-new-head",
          title: "new head task",
          created_at: new Date(Date.now() + 60_000).toISOString(),
        };
        return Response.json({
          tasks: taskHeadRequests === 1 ? tasks.slice(0, 1) : [newHead],
          next_cursor: null,
        });
      }
      if (options.shiftingTaskBoundary) {
        const newHead = {
          ...tasks[0],
          id: "task-new-head",
          request_key: "request-new-head",
          title: "new head task",
          created_at: new Date(Date.now() + 60_000).toISOString(),
        };
        return Response.json(
          taskHeadRequests === 1
            ? { tasks: tasks.slice(0, 1), next_cursor: "old-boundary" }
            : { tasks: [newHead, tasks[0]], next_cursor: "new-boundary" },
        );
      }
      if (options.staleTaskHeadAfterDelete && taskHeadRequests > 1) {
        await new Promise<void>((resolve) => {
          resolveStaleTaskHead = resolve;
        });
        return Response.json({ tasks, next_cursor: null });
      }
      const growingPage =
        options.growingTaskHistory && taskHeadRequests > 1;
      return Response.json({
        tasks: (options.paginatedTasks || options.growingTaskHistory ? tasks.slice(0, 1) : tasks)
          .filter((task) => !deletedTaskIDs.has(task.id)),
        next_cursor: options.staleHistoryAfterDelete
          ? "stale-page"
          : options.paginatedTasks || growingPage
            ? "next-page"
            : null,
      });
    }
    if (path === "/api/v1/tasks?limit=50&cursor=stale-page") {
      await new Promise<void>((resolve) => {
        resolveStaleHistory = resolve;
      });
      return Response.json({ tasks: [tasks[2]], next_cursor: null });
    }
    if (path === "/api/v1/tasks?limit=50&cursor=next-page") {
      return Response.json({ tasks: tasks.slice(1), next_cursor: null });
    }
    if (path === "/api/v1/tasks?limit=50&cursor=old-boundary") {
      return Response.json({ tasks: tasks.slice(1, 2), next_cursor: null });
    }
    if (path === "/api/v1/tasks?limit=50&cursor=new-boundary") {
      return Response.json({
        tasks: [tasks[2], { ...tasks[1], state: "succeeded" }],
        next_cursor: null,
      });
    }
    if (path === "/api/v1/tasks/created-task") {
      return Response.json({
        task: {
          ...tasks[0],
          id: "created-task",
          title: createdTask.title,
          description: createdTask.description,
        },
        execution: {
          id: "execution-created",
          task_id: "created-task",
          assigned_worker_id: worker.id,
          required_runtime: "codex",
          state: "queued",
          cancellation_requested: false,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        },
        repository: worker.repositories[0],
        repository_available: true,
        attempts: [],
        workflow: createdTask.workflow,
        context: createdTask.context,
        resolved_prompt: createdTask.resolvedPrompt ?? createdTask.description,
      });
    }
    if (path.startsWith("/api/v1/attempts/attempt-succeeded/events?")) {
      return Response.json({
        events: [{
          sequence: 0,
          kind: "completed",
          payload: { message: "Terminal event" },
          server_time: tasks[2].created_at,
        }],
        next_after: 0,
        has_more: false,
      });
    }
    if (path === "/api/v1/tasks/task-succeeded") {
      if (init?.method === "DELETE") {
        deletedTaskIDs.add("task-succeeded");
        window.setTimeout(() => {
          resolveStaleHistory?.();
          resolveStaleTaskHead?.();
        }, 0);
        return Response.json({ deleted: true });
      }
      return Response.json({
        task: { ...tasks[2], description: "Terminal history can be deleted explicitly." },
        execution: {
          id: "execution-succeeded",
          task_id: "task-succeeded",
          assigned_worker_id: worker.id,
          required_runtime: "codex",
          state: "succeeded",
          cancellation_requested: false,
          created_at: tasks[2].created_at,
          updated_at: tasks[2].created_at,
        },
        repository: worker.repositories[0],
        repository_available: true,
        context: "Terminal history can be deleted explicitly.",
        attempts: [{
          id: "attempt-succeeded",
          execution_id: "execution-succeeded",
          worker_id: worker.id,
          attempt_number: 1,
          state: "succeeded",
          lease_expires_at: tasks[2].created_at,
          result: "Terminal result",
          started_at: tasks[2].created_at,
          completed_at: tasks[2].created_at,
          created_at: tasks[2].created_at,
        }],
        resolved_prompt: "Terminal history can be deleted explicitly.",
      });
    }
    if (path === "/api/v1/tasks/task-running") {
      taskDetailRequests += 1;
      if (
        options.taskDetailFailuresAfter !== undefined &&
        taskDetailRequests > options.taskDetailFailuresAfter
      ) {
        return Response.json(
          { error: { code: "storage_unavailable", message: "temporary read failure" } },
          { status: 503 },
        );
      }
      const attemptID =
        options.switchAttemptAfter !== undefined &&
        taskDetailRequests > options.switchAttemptAfter
          ? "attempt-next"
          : "attempt-running";
      const terminal =
        options.terminalTaskAfter !== undefined &&
        taskDetailRequests > options.terminalTaskAfter;
      return Response.json({
        task: {
          ...tasks[1],
          state: terminal ? "succeeded" : "running",
          description: "Cached task detail remains available.",
        },
        execution: {
          id: "execution-running",
          task_id: "task-running",
          assigned_worker_id: worker.id,
          required_runtime: "codex",
          state: terminal ? "succeeded" : "running",
          cancellation_requested: false,
          created_at: tasks[1].created_at,
          updated_at: tasks[1].created_at,
        },
        repository: worker.repositories[0],
        repository_available: true,
        context: "Cached task detail remains available.",
        attempts: [
          {
            id: attemptID,
            execution_id: "execution-running",
            worker_id: worker.id,
            attempt_number: 1,
            state: terminal ? "succeeded" : "running",
            lease_expires_at: new Date(Date.now() + 30_000).toISOString(),
            started_at: tasks[1].created_at,
            created_at: tasks[1].created_at,
          },
        ],
        resolved_prompt: "Cached task detail remains available.",
      });
    }
    if (path.startsWith("/api/v1/attempts/attempt-next/events?")) {
      return Response.json({
        events: [{
          sequence: 0,
          kind: "progress",
          payload: { text: "New attempt starts with an empty event cache" },
          server_time: new Date().toISOString(),
        }],
        next_after: 0,
        has_more: false,
      });
    }
    if (path.startsWith("/api/v1/attempts/attempt-running/events?")) {
      eventRequests += 1;
      if (
        options.eventFailuresAfter !== undefined &&
        eventRequests > options.eventFailuresAfter
      ) {
        return Response.json(
          { error: { code: "storage_unavailable", message: "progress refresh failed" } },
          { status: 503 },
        );
      }
      const after = Number(new URLSearchParams(path.split("?")[1]).get("after"));
      if (options.terminalTaskAfter !== undefined) {
        if (after >= 0 && terminalEventFailures > 0) {
          terminalEventFailures -= 1;
          return Response.json(
            { error: { code: "storage_unavailable", message: "terminal catch-up failed" } },
            { status: 503 },
          );
        }
        const sequence = after + 1;
        return Response.json({
          events: sequence <= 1
            ? [{
                sequence,
                kind: "progress",
                payload: {
                  text: sequence === 0 ? "Progress before completion" : "Final terminal progress",
                },
                server_time: new Date().toISOString(),
              }]
            : [],
          next_after: sequence <= 1 ? sequence : after,
          has_more: false,
        });
      }
      if (options.incrementalEvents) {
        const sequence = after + 1;
        return Response.json({
          events: sequence <= 2
            ? [{
                sequence,
                kind: "progress",
                payload: { text: `Incremental event ${sequence}` },
                server_time: new Date().toISOString(),
              }]
            : [],
          next_after: sequence <= 2 ? sequence : after,
          has_more: sequence === 0,
        });
      }
      return Response.json({
        events: [
          {
            sequence: 0,
            kind: "progress",
            payload: { text: "Cached ordered progress" },
            server_time: new Date().toISOString(),
          },
        ],
        next_after: 0,
        has_more: false,
      });
    }
    if (path === "/api/v1/workers") {
      if (options.workerFailure) {
        return Response.json(
          { error: { code: "workers_unavailable", message: "workers could not be loaded" } },
          { status: 503 },
        );
      }
      workerListRequests += 1;
      const onlineWorker = options.workerRuntimeRefresh && workerListRequests > 1
        ? {
            ...worker,
            runtime: "pi",
            capabilities: (worker.capabilities ?? []).map((capability) => capability.kind === "runtime" && capability.name === "codex"
              ? { ...capability, status: "unauthenticated", message: "Authenticate Codex before running Jobs." }
              : capability),
          }
        : worker;
      return Response.json({ workers: [onlineWorker, offlineWorker] });
    }
    const workerRepositoryOptions = path.match(/^\/api\/v1\/workers\/([^/]+)\/repository-options$/);
    if (workerRepositoryOptions) {
      const selectedWorker = [worker, offlineWorker].find((candidate) => candidate.id === workerRepositoryOptions[1]);
      if (!selectedWorker) {
        return Response.json({ error: { code: "not_found", message: "not found" } }, { status: 404 });
      }
      const advertisedByID = new Map(selectedWorker.repositories.map((repository) => [repository.id, repository]));
      const configuredIDs = new Set(repositoryItems.map((repository) => repository.id));
      const configured = repositoryItems.map((repository) => {
        const advertised = advertisedByID.get(repository.id);
        const ready = repository.enabled && selectedWorker.online && selectedWorker.health === "healthy";
        return {
          id: repository.id,
          key: advertised?.key,
          remote_identity: repository.remote_identity,
          enabled: repository.enabled,
          cached: repository.id === "repo-factory",
          advertised: Boolean(advertised),
          ready,
          reason: !repository.enabled
            ? "Repository routing is disabled."
            : !selectedWorker.online
              ? "Worker is offline."
              : ready
                ? advertised
                  ? "Online, healthy, with GitHub access and this repository advertised."
                  : "Online, healthy, with GitHub access and managed cache headroom."
                : "Worker is unhealthy.",
        };
      });
      const advertisedOnly = selectedWorker.repositories
        .filter((repository) => !configuredIDs.has(repository.id))
        .map((repository) => ({
          id: repository.id,
          key: repository.key,
          remote_identity: repository.remote_identity,
          enabled: true,
          cached: false,
          advertised: true,
          ready: selectedWorker.online && selectedWorker.health === "healthy",
          reason: selectedWorker.online
            ? "Online, healthy, with GitHub access and this repository advertised."
            : "Worker is offline.",
        }));
      return Response.json({ repositories: [...configured, ...advertisedOnly] });
    }
    if (path === `/api/v1/workers/${worker.id}`) {
      workerDetailRequests += 1;
      if (options.workerDetailFailuresAfter !== undefined && workerDetailRequests > options.workerDetailFailuresAfter) {
        return Response.json(
          { error: { code: "worker_unavailable", message: "Worker connection failed" } },
          { status: 503 },
        );
      }
      const detailWorker = options.workerDetailRuntimeRefresh
        ? {
            ...worker,
            runtime: "pi",
            capabilities: (worker.capabilities ?? []).map((capability) => capability.kind === "runtime" && capability.name === "codex"
              ? { ...capability, status: "unauthenticated", message: "Authenticate Codex before running Jobs." }
              : capability),
          }
        : worker;
      return Response.json(options.workerOfflineAfterConnectionTest && workerConnectionTests > 0
        ? { ...detailWorker, online: false }
        : detailWorker);
    }
    if (path === `/api/v1/workers/${worker.id}/test` && init?.method === "POST") {
      workerConnectionTests += 1;
      if (workerConnectionTests > 1) {
        return Response.json(
          { error: { code: "worker_connection_timeout", message: "Worker did not send a fresh registration" } },
          { status: 504 },
        );
      }
      return Response.json(worker);
    }
    throw new Error(`Unhandled request: ${path}`);
  });
}
