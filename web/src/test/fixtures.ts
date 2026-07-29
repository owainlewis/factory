import type { Task, Worker } from "../types";
import { vi } from "vitest";

export const worker: Worker = {
  id: "worker-online",
  name: "Build Mac",
  worker_version: "2.0.0",
  codex_version: "0.42.0",
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

export function mockControlPlane(
  options: {
    createFailures?: number;
    eventFailuresAfter?: number;
    taskDetailFailuresAfter?: number;
  } = {},
) {
  let createFailures = options.createFailures ?? 0;
  let eventRequests = 0;
  let taskDetailRequests = 0;
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
      return Response.json({ tasks });
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
      return Response.json({
        task: { ...tasks[1], description: "Cached task detail remains available." },
        execution: {
          id: "execution-running",
          task_id: "task-running",
          assigned_worker_id: worker.id,
          required_runtime: "codex",
          state: "running",
          cancellation_requested: false,
          created_at: tasks[1].created_at,
          updated_at: tasks[1].created_at,
        },
        repository: worker.repositories[0],
        repository_available: true,
        attempts: [
          {
            id: "attempt-running",
            execution_id: "execution-running",
            worker_id: worker.id,
            attempt_number: 1,
            state: "running",
            lease_expires_at: new Date(Date.now() + 30_000).toISOString(),
            started_at: tasks[1].created_at,
            created_at: tasks[1].created_at,
          },
        ],
      });
    }
    if (path === "/api/v1/attempts/attempt-running/events?after=-1") {
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
      return Response.json({
        events: [
          {
            sequence: 0,
            kind: "progress",
            payload: { text: "Cached ordered progress" },
            server_time: new Date().toISOString(),
          },
        ],
      });
    }
    if (path === "/api/v1/workers") return Response.json({ workers: [worker, offlineWorker] });
    throw new Error(`Unhandled request: ${path}`);
  });
}
