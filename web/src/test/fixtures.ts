import type { MetricsSummary, Task, Worker } from "../types";
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
  repositories: [
    { id: "repo-factory", key: "factory", remote_identity: "github.com/example/factory", retained_count: 0 },
    { id: "repo-docs", key: "docs", remote_identity: "github.com/example/docs", retained_count: 0 },
  ],
  retained_worktrees: [],
  registered_at: "2026-07-29T10:00:00Z",
  last_heartbeat: new Date().toISOString(),
  current_task_title: "Implement control plane",
};

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

export function mockControlPlane(
  options: {
    createFailures?: number;
    boundedLiveHead?: boolean;
    eventFailuresAfter?: number;
    growingTaskHistory?: boolean;
    incrementalEvents?: boolean;
    paginatedTasks?: boolean;
    shiftingTaskBoundary?: boolean;
    staleHistoryAfterDelete?: boolean;
    switchAttemptAfter?: number;
    taskDetailFailuresAfter?: number;
    terminalEventFailures?: number;
    terminalTaskAfter?: number;
  } = {},
) {
  let createFailures = options.createFailures ?? 0;
  let eventRequests = 0;
  let taskHeadRequests = 0;
  let terminalEventFailures = options.terminalEventFailures ?? 0;
  let taskDetailRequests = 0;
  const deletedTaskIDs = new Set<string>();
  let resolveStaleHistory: (() => void) | undefined;
  let createdTask: { title: string; description: string } = {
    title: "Ship the UI",
    description: "Build and verify the real interface.",
  };
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
    if (path === "/api/v1/tasks") {
      if (init?.method === "POST") {
        if (createFailures > 0) {
          createFailures -= 1;
          throw new Error("connection lost after submit");
        }
        const body = JSON.parse(String(init.body)) as Record<string, unknown>;
        createdTask = {
          title: String(body.title),
          description: String(body.description),
        };
        return Response.json({
          task: { ...tasks[0], id: "created-task", title: body.title, description: body.description },
          execution: { id: "execution-created", state: "queued" },
          repository: worker.repositories[0],
          repository_available: true,
          attempts: [],
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
    if (path === "/api/v1/workers") return Response.json({ workers: [worker, offlineWorker] });
    if (path === `/api/v1/workers/${worker.id}`) return Response.json(worker);
    throw new Error(`Unhandled request: ${path}`);
  });
}
