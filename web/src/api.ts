import type {
  APIErrorBody,
  AttemptEvent,
  CreateTaskInput,
  Task,
  TaskDetail,
  Worker,
} from "./types";

export class APIError extends Error {
  constructor(
    public code: string,
    message: string,
    public status: number,
  ) {
    super(message);
    this.name = "APIError";
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: init?.body
      ? { "Content-Type": "application/json", ...init.headers }
      : init?.headers,
  });
  if (!response.ok) {
    let body: APIErrorBody | undefined;
    try {
      body = (await response.json()) as APIErrorBody;
    } catch {
      // The status still gives the operator a useful failure when a proxy fails.
    }
    throw new APIError(
      body?.error.code ?? "request_failed",
      body?.error.message ?? `Request failed with status ${response.status}`,
      response.status,
    );
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return response.json() as Promise<T>;
}

export const api = {
  tasks: async () => (await request<{ tasks: Task[] | null }>("/api/v1/tasks")).tasks ?? [],
  task: (id: string) => request<TaskDetail>(`/api/v1/tasks/${encodeURIComponent(id)}`),
  workers: async () =>
    (await request<{ workers: Worker[] | null }>("/api/v1/workers")).workers ?? [],
  worker: (id: string) => request<Worker>(`/api/v1/workers/${encodeURIComponent(id)}`),
  events: async (attemptID: string) =>
    (
      await request<{ events: AttemptEvent[] | null }>(
        `/api/v1/attempts/${encodeURIComponent(attemptID)}/events?after=-1`,
      )
    ).events ?? [],
  createTask: (input: CreateTaskInput) =>
    request<TaskDetail>("/api/v1/tasks", { method: "POST", body: JSON.stringify(input) }),
  cancelTask: (id: string) =>
    request<TaskDetail>(`/api/v1/tasks/${encodeURIComponent(id)}/cancel`, {
      method: "POST",
      body: "{}",
    }),
  retryExecution: (id: string) =>
    request<TaskDetail>(`/api/v1/executions/${encodeURIComponent(id)}/retry`, {
      method: "POST",
      body: "{}",
    }),
};
