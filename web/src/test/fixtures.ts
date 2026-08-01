import type { AutomationDetail, AutomationOccurrence, ManagedRepository, MetricsSummary, Task, Worker, Workflow, WorkflowDetail } from "../types";
import { vi } from "vitest";

export const worker: Worker = {
  id: "worker-online",
  name: "Build Mac",
  worker_version: "2.0.0",
  runtime: "codex",
  runtime_version: "0.42.0",
  capacity: 2,
  active_count: 1,
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
};

const initialWorkflowDetail: WorkflowDetail = {
  workflow: {
    id: "workflow-implement",
    enabled: true,
    current_revision: {
      id: "workflow-revision-1",
      workflow_id: "workflow-implement",
      revision_number: 1,
      name: "Implement",
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
    name: "Historical workflow",
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
    name: "Ready issues",
    workflow_id: "workflow-implement",
    workflow_name: "Implement",
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
      required_labels: ["factory:ready"],
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
    eventFailuresAfter?: number;
    growingTaskHistory?: boolean;
    incrementalEvents?: boolean;
    paginatedAutomations?: boolean;
    paginatedAutomationOccurrences?: boolean;
    paginatedAutomationWorkflows?: boolean;
    paginatedTasks?: boolean;
    refreshesHistoricalWorkflow?: boolean;
    shiftingWorkflowBoundary?: boolean;
    shiftingTaskBoundary?: boolean;
    staleHistoryAfterDelete?: boolean;
    switchAttemptAfter?: number;
    taskDetailFailuresAfter?: number;
    terminalEventFailures?: number;
    terminalTaskAfter?: number;
    repositoryToggleFailure?: boolean;
    workflowHistoryGate?: Promise<void>;
    workerFailure?: boolean;
  } = {},
) {
  let createFailures = options.createFailures ?? 0;
  let eventRequests = 0;
  let taskHeadRequests = 0;
  let workflowHeadRequests = 0;
  let terminalEventFailures = options.terminalEventFailures ?? 0;
  let taskDetailRequests = 0;
  const deletedTaskIDs = new Set<string>();
  let resolveStaleHistory: (() => void) | undefined;
  let createdTask: {
    title: string;
    context: string;
    description: string;
    workflow?: { id: string; revision_id: string; name: string; revision_number: number };
    resolvedPrompt?: string;
  } = {
    title: "Ship the UI",
    context: "Build and verify the real interface.",
    description: "Build and verify the real interface.",
  };
  let repositoryItems = managedRepositories.map((repository) => ({ ...repository }));
  let workflowDetail = structuredClone(initialWorkflowDetail);
  let automationDetail = structuredClone(initialAutomationDetail);
  const automationOccurrence = (id: string, issue: number, createdAt: string): AutomationOccurrence => ({
    id,
    automation_id: automationDetail.automation.id,
    automation_version: 1,
    state: "pending",
    issue_number: issue,
    issue_url: `https://github.com/example/factory/issues/${issue}`,
    issue_title: `Paged issue ${issue}`,
    observed_state: "open",
    observed_labels: ["factory:ready"],
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
              name: "Shifted boundary workflow",
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
            name: "Refreshed workflow",
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
              name: String(body.name),
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
        name: String(body.name),
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
      return Response.json({
        automations: [automationDetail.automation],
        next_cursor: options.paginatedAutomations ? "automation-history" : null,
      });
    }
    if (path === "/api/v1/automations?limit=200&cursor=automation-history") {
      return Response.json({
        automations: [{
          ...automationDetail.automation,
          id: "automation-history",
          name: "Historical Automation",
          updated_at: "2026-07-01T08:00:00Z",
        }],
        next_cursor: null,
      });
    }
    if (path === "/api/v1/automations" && init?.method === "POST") {
      const body = JSON.parse(String(init.body)) as Record<string, unknown>;
      const trigger = body.trigger as AutomationDetail["automation"]["trigger"];
      automationDetail = {
        automation: {
          ...automationDetail.automation,
          id: "automation-created",
          name: String(body.name),
          workflow_id: String(body.workflow_id),
          repository_id: String(body.repository_id),
          context: String(body.context),
          timeout_seconds: Number(body.timeout_seconds),
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
            name: String(body.name),
            workflow_id: String(body.workflow_id),
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
      return Response.json({ matches: [{ number: 184, title: "Typed Automations", url: "https://github.com/example/factory/issues/184", state: "open", labels: ["factory:ready"] }] });
    }
    if (path === `/api/v1/automations/${automationDetail.automation.id}/enabled` && init?.method === "PUT") {
      const body = JSON.parse(String(init.body)) as { enabled: boolean };
      automationDetail = {
        ...automationDetail,
        automation: {
          ...automationDetail.automation,
          enabled: body.enabled,
          health: body.enabled
            ? { status: "pending", message: "Waiting for the next GitHub check." }
            : { status: "disabled", message: "Automation is disabled." },
          next_check_at: body.enabled ? new Date().toISOString() : undefined,
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
              name: workflowDetail.workflow.current_revision.name,
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
        window.setTimeout(() => resolveStaleHistory?.(), 0);
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
      return Response.json({ workers: [worker, offlineWorker] });
    }
    if (path === `/api/v1/workers/${worker.id}`) return Response.json(worker);
    throw new Error(`Unhandled request: ${path}`);
  });
}
