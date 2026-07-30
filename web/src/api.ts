import type {
  APIErrorBody,
  AttemptEventPage,
  CreateTaskInput,
  TaskPage,
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
  tasks: async (cursor = "") => {
    const query = new URLSearchParams({ limit: "50" });
    if (cursor) query.set("cursor", cursor);
    const page = await request<{ tasks: TaskPage["tasks"] | null; next_cursor: string | null }>(
      `/api/v1/tasks?${query}`,
    );
    return { tasks: page.tasks ?? [], next_cursor: page.next_cursor };
  },
  task: (id: string) => request<TaskDetail>(`/api/v1/tasks/${encodeURIComponent(id)}`),
  workers: async () =>
    (await request<{ workers: Worker[] | null }>("/api/v1/workers")).workers ?? [],
  worker: (id: string) => request<Worker>(`/api/v1/workers/${encodeURIComponent(id)}`),
  events: async (attemptID: string, after: number): Promise<AttemptEventPage> => {
    const query = new URLSearchParams({ after: String(after), limit: "100" });
    const page = await request<{
      events: AttemptEventPage["events"] | null;
      next_after: number;
      has_more: boolean;
    }>(`/api/v1/attempts/${encodeURIComponent(attemptID)}/events?${query}`);
    return { ...page, events: page.events ?? [] };
  },
  createTask: (input: CreateTaskInput) =>
    request<TaskDetail>("/api/v1/tasks", { method: "POST", body: JSON.stringify(input) }),
  cancelTask: (id: string) =>
    request<TaskDetail>(`/api/v1/tasks/${encodeURIComponent(id)}/cancel`, {
      method: "POST",
      body: "{}",
    }),
  deleteTask: (id: string) =>
    request<{ deleted: boolean }>(`/api/v1/tasks/${encodeURIComponent(id)}`, {
      method: "DELETE",
      body: "{}",
    }),
  retryExecution: (id: string) =>
    request<TaskDetail>(`/api/v1/executions/${encodeURIComponent(id)}/retry`, {
      method: "POST",
      body: "{}",
    }),
};
