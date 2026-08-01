import type {
  APIErrorBody,
  AttemptEventPage,
  CreateTaskInput,
  MetricsSummary,
  MetricsWindow,
  ManagedRepository,
  ManagedRepositoryReadiness,
  TaskPage,
  TaskDetail,
  Worker,
  WorkflowDetail,
  WorkflowPage,
  CreateWorkflowInput,
  CreateWorkflowRevisionInput,
  AutomationDetail,
  AutomationOccurrencePage,
  AutomationPage,
  CreateAutomationInput,
  UpdateAutomationInput,
  TestAutomationResult,
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

async function requestWithStatus<T>(path: string, init?: RequestInit): Promise<{ data: T; status: number }> {
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
    return { data: undefined as T, status: response.status };
  }
  return { data: await response.json() as T, status: response.status };
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  return (await requestWithStatus<T>(path, init)).data;
}

export const api = {
  metrics: (window: MetricsWindow) =>
    request<MetricsSummary>(
      `/api/v1/metrics/summary?${new URLSearchParams({ window })}`,
    ),
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
    ((await request<{ workers: Worker[] | null }>("/api/v1/workers")).workers ?? [])
      .map(normalizeWorker),
  worker: async (id: string) =>
    normalizeWorker(await request<Worker>(`/api/v1/workers/${encodeURIComponent(id)}`)),
  repositories: async () =>
    (await request<{ repositories: ManagedRepository[] | null }>("/api/v1/repositories"))
      .repositories ?? [],
  repository: (id: string) =>
    request<ManagedRepository>(`/api/v1/repositories/${encodeURIComponent(id)}`),
  repositoryReadiness: (id: string) =>
    request<ManagedRepositoryReadiness>(
      `/api/v1/repositories/${encodeURIComponent(id)}/readiness`,
    ),
  createRepository: async (remoteIdentity: string) => {
    const response = await requestWithStatus<ManagedRepository>("/api/v1/repositories", {
      method: "POST",
      body: JSON.stringify({ remote_identity: remoteIdentity }),
    });
    return { repository: response.data, created: response.status === 201 };
  },
  setRepositoryEnabled: (id: string, enabled: boolean) =>
    request<ManagedRepository>(`/api/v1/repositories/${encodeURIComponent(id)}/enabled`, {
      method: "PUT",
      body: JSON.stringify({ enabled }),
    }),
  workflows: async (cursor = "", enabled?: boolean) => {
    const query = new URLSearchParams({ limit: "200" });
    if (cursor) query.set("cursor", cursor);
    if (enabled !== undefined) query.set("enabled", String(enabled));
    const page = await request<{
      workflows: WorkflowPage["workflows"] | null;
      next_cursor: string | null;
    }>(`/api/v1/workflows?${query}`);
    return { workflows: page.workflows ?? [], next_cursor: page.next_cursor };
  },
  allEnabledWorkflows: async () => {
    const workflows: WorkflowPage["workflows"] = [];
    let cursor = "";
    do {
      const page = await api.workflows(cursor, true);
      workflows.push(...page.workflows);
      cursor = page.next_cursor ?? "";
    } while (cursor);
    return workflows;
  },
  workflow: (id: string) => request<WorkflowDetail>(`/api/v1/workflows/${encodeURIComponent(id)}`),
  createWorkflow: (input: CreateWorkflowInput) =>
    request<WorkflowDetail>("/api/v1/workflows", { method: "POST", body: JSON.stringify(input) }),
  reviseWorkflow: ({ id, input }: { id: string; input: CreateWorkflowRevisionInput }) =>
    request<WorkflowDetail>(`/api/v1/workflows/${encodeURIComponent(id)}/revisions`, {
      method: "POST",
      body: JSON.stringify(input),
    }),
  setWorkflowEnabled: ({ id, enabled }: { id: string; enabled: boolean }) =>
    request<WorkflowDetail>(`/api/v1/workflows/${encodeURIComponent(id)}/enabled`, {
      method: "PUT",
      body: JSON.stringify({ enabled }),
    }),
  automations: async (cursor = "") => {
    const query = new URLSearchParams({ limit: "200" });
    if (cursor) query.set("cursor", cursor);
    const page = await request<{
      automations: AutomationPage["automations"] | null;
      next_cursor: string | null;
    }>(`/api/v1/automations?${query}`);
    return { automations: page.automations ?? [], next_cursor: page.next_cursor };
  },
  automation: (id: string) =>
    request<AutomationDetail>(`/api/v1/automations/${encodeURIComponent(id)}`),
  automationOccurrences: async (id: string, cursor = "") => {
    const query = new URLSearchParams({ limit: "50" });
    if (cursor) query.set("cursor", cursor);
    const page = await request<{
      occurrences: AutomationOccurrencePage["occurrences"] | null;
      next_cursor: string | null;
    }>(`/api/v1/automations/${encodeURIComponent(id)}/occurrences?${query}`);
    return { occurrences: page.occurrences ?? [], next_cursor: page.next_cursor };
  },
  createAutomation: (input: CreateAutomationInput) =>
    request<AutomationDetail>("/api/v1/automations", {
      method: "POST",
      body: JSON.stringify(input),
    }),
  updateAutomation: ({ id, input }: { id: string; input: UpdateAutomationInput }) =>
    request<AutomationDetail>(`/api/v1/automations/${encodeURIComponent(id)}`, {
      method: "PUT",
      body: JSON.stringify(input),
    }),
  setAutomationEnabled: ({ id, enabled }: { id: string; enabled: boolean }) =>
    request<AutomationDetail>(`/api/v1/automations/${encodeURIComponent(id)}/enabled`, {
      method: "PUT",
      body: JSON.stringify({
        enabled,
        confirm_legacy_poller_stopped: enabled,
      }),
    }),
  testAutomation: (id: string) =>
    request<TestAutomationResult>(`/api/v1/automations/${encodeURIComponent(id)}/test`, {
      method: "POST",
      body: "{}",
    }),
  checkAutomation: (id: string) =>
    request<AutomationDetail>(`/api/v1/automations/${encodeURIComponent(id)}/check`, {
      method: "POST",
      body: "{}",
    }),
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

function normalizeWorker(worker: Worker): Worker {
  return {
    ...worker,
    repositories: worker.repositories ?? [],
    retained_worktrees: worker.retained_worktrees ?? [],
    source_access: worker.source_access ?? [],
  };
}
