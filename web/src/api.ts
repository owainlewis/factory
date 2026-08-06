import type {
  APIErrorBody,
  AttemptEventPage,
  CreateTaskInput,
  MetricsSummary,
  MetricsFilters,
  MetricsWindow,
  ManagedRepository,
  ManagedRepositoryReadiness,
  WorkerRepositoryOption,
  TaskPage,
  TaskDetail,
  Worker,
  WorkflowDetail,
  WorkflowPage,
  CreateWorkflowInput,
  CreateWorkflowRevisionInput,
  Definition,
  DefinitionPage,
  CreateDefinitionInput,
  UpdateDefinitionInput,
  AutomationDetail,
  AutomationOccurrence,
  AutomationOccurrencePage,
  AutomationPage,
  CreateAutomationInput,
  UpdateAutomationInput,
  TestAutomationResult,
  LegacyPollerMigration,
  LegacyPollerSelection,
  CreateRunInput,
  RunDetail,
  RunPage,
  RunRepository,
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
  metrics: (window: MetricsWindow, filters: MetricsFilters = {}, jobView = "all") => {
    const query = new URLSearchParams({ window });
    if (filters.definition_id) query.set("definition_id", filters.definition_id);
    if (filters.repository_id) query.set("repository_id", filters.repository_id);
    if (filters.runner_id) query.set("runner_id", filters.runner_id);
    if (jobView !== "all") query.set("job_view", jobView);
    return request<MetricsSummary>(`/api/v1/metrics/summary?${query}`);
  },
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
  testWorker: async (id: string) =>
    normalizeWorker(await request<Worker>(`/api/v1/workers/${encodeURIComponent(id)}/test`, {
      method: "POST",
      body: "{}",
    })),
  workerRepositoryOptions: async (id: string) =>
    (await request<{ repositories: WorkerRepositoryOption[] | null }>(
      `/api/v1/workers/${encodeURIComponent(id)}/repository-options`,
    )).repositories ?? [],
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
  definitions: async (cursor = "", archived = false) => {
    const query = new URLSearchParams({ limit: "200", archived: String(archived) });
    if (cursor) query.set("cursor", cursor);
    const page = await request<{
      definitions: DefinitionPage["definitions"] | null;
      next_cursor: string | null;
    }>(`/api/v1/definitions?${query}`);
    return { definitions: page.definitions ?? [], next_cursor: page.next_cursor };
  },
  allDefinitions: async () => {
    const definitions: DefinitionPage["definitions"] = [];
    let cursor = "";
    do {
      const page = await api.definitions(cursor);
      definitions.push(...page.definitions);
      cursor = page.next_cursor ?? "";
    } while (cursor);
    return definitions;
  },
  definition: (id: string) =>
    request<Definition>(`/api/v1/definitions/${encodeURIComponent(id)}`),
  createDefinition: (input: CreateDefinitionInput) =>
    request<Definition>("/api/v1/definitions", { method: "POST", body: JSON.stringify(input) }),
  updateDefinition: ({ id, input }: { id: string; input: UpdateDefinitionInput }) =>
    request<Definition>(`/api/v1/definitions/${encodeURIComponent(id)}`, {
      method: "PUT",
      body: JSON.stringify(input),
    }),
  setDefinitionArchived: ({ id, archived, expectedGeneration }: {
    id: string;
    archived: boolean;
    expectedGeneration: number;
  }) => request<Definition>(`/api/v1/definitions/${encodeURIComponent(id)}/archived`, {
    method: "PUT",
    body: JSON.stringify({ archived, expected_generation: expectedGeneration }),
  }),
  runs: async (cursor = "") => {
    const query = new URLSearchParams({ limit: "50" });
    if (cursor) query.set("cursor", cursor);
    const page = await request<{ runs: RunPage["runs"] | null; next_cursor: string | null }>(
      `/api/v1/runs?${query}`,
    );
    return { runs: page.runs ?? [], next_cursor: page.next_cursor };
  },
  runRepositories: async () =>
    (await request<{ repositories: RunRepository[] | null }>("/api/v1/run-repositories"))
      .repositories ?? [],
  run: (id: string) =>
    request<RunDetail>(`/api/v1/runs/${encodeURIComponent(id)}`),
  createRun: (input: CreateRunInput) =>
    request<RunDetail>("/api/v1/runs", { method: "POST", body: JSON.stringify(input) }),
  cancelJob: (id: string) =>
    request<RunDetail>(`/api/v1/jobs/${encodeURIComponent(id)}/cancel`, {
      method: "POST",
      body: "{}",
    }),
  retryJob: (id: string) =>
    request<RunDetail>(`/api/v1/jobs/${encodeURIComponent(id)}/retry`, {
      method: "POST",
      body: "{}",
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
  allWorkflows: async () => {
    const workflows: WorkflowPage["workflows"] = [];
    let cursor = "";
    do {
      const page = await api.workflows(cursor);
      workflows.push(...page.workflows);
      cursor = page.next_cursor ?? "";
    } while (cursor);
    return workflows;
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
      body: JSON.stringify({ enabled }),
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
  runAutomation: (id: string, requestKey: string) =>
    request<AutomationDetail>(`/api/v1/automations/${encodeURIComponent(id)}/run`, {
      method: "POST",
      body: JSON.stringify({ request_key: requestKey }),
    }),
  previewLegacyPoller: (selection: LegacyPollerSelection) =>
    request<LegacyPollerMigration>("/api/v1/migrations/legacy-poller/preview", {
      method: "POST",
      body: JSON.stringify(selection),
    }),
  activeLegacyPollerMigration: () =>
    request<{ migration: LegacyPollerMigration | null }>("/api/v1/migrations/legacy-poller/active"),
  importLegacyPoller: ({
    migration,
    selection,
    mappings,
  }: {
    migration: LegacyPollerMigration;
    selection: LegacyPollerSelection;
    mappings: Array<{ queue_id: string; workflow_title: string; automation_title: string }>;
  }) => request<LegacyPollerMigration>("/api/v1/migrations/legacy-poller/import", {
    method: "POST",
    body: JSON.stringify({
      ...selection,
      migration_id: migration.id,
      snapshot_digest: migration.snapshot_digest,
      mappings,
    }),
  }),
  legacyPollerMigration: (id: string) =>
    request<LegacyPollerMigration>(`/api/v1/migrations/legacy-poller/${encodeURIComponent(id)}`),
  finalizeLegacyPoller: ({ migration, selection }: { migration: LegacyPollerMigration; selection: LegacyPollerSelection }) =>
    request<LegacyPollerMigration>(`/api/v1/migrations/legacy-poller/${encodeURIComponent(migration.id)}/finalize`, {
      method: "POST",
      body: JSON.stringify({
        ...selection,
        migration_id: migration.id,
        snapshot_digest: migration.snapshot_digest,
      }),
    }),
  resumeLegacyOccurrence: (id: string) =>
    request<AutomationOccurrence>(`/api/v1/occurrences/${encodeURIComponent(id)}/resume`, {
      method: "POST",
      body: "{}",
    }),
  skipLegacyOccurrence: (id: string) =>
    request<AutomationOccurrence>(`/api/v1/occurrences/${encodeURIComponent(id)}/skip`, {
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
	const capabilities = worker.capabilities?.length
		? worker.capabilities
		: [{
			kind: "runtime" as const,
			name: worker.runtime,
			status: worker.health === "healthy" ? "ready" as const : "unhealthy" as const,
			version: worker.runtime_version,
		}];
  return {
    ...worker,
		capabilities,
    repositories: worker.repositories ?? [],
    retained_worktrees: worker.retained_worktrees ?? [],
    source_access: worker.source_access ?? [],
  };
}
