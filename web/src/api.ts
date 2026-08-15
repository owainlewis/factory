import type {
  APIErrorBody,
  AttemptEventPage,
  ExecutionProfile,
  ManagedRepository,
  ManagedRepositoryReadiness,
  Routine,
  RoutineOverview,
  SaveRoutineInput,
  WorkDetailV2,
  WorkPageV2,
  Worker,
} from "./types";

export class APIError extends Error {
  constructor(public code: string, message: string, public status: number) {
    super(message);
    this.name = "APIError";
  }
}

async function requestWithStatus<T>(path: string, init?: RequestInit): Promise<{ data: T; status: number }> {
  const response = await fetch(path, {
    ...init,
    headers: init?.body ? { "Content-Type": "application/json", ...init.headers } : init?.headers,
  });
  if (!response.ok) {
    let body: APIErrorBody | undefined;
    try {
      body = await response.json() as APIErrorBody;
    } catch {
      // A proxy response may not be JSON. The HTTP status remains useful.
    }
    throw new APIError(
      body?.error.code ?? "request_failed",
      body?.error.message ?? `Request failed with status ${response.status}`,
      response.status,
    );
  }
  if (response.status === 204) return { data: undefined as T, status: response.status };
  return { data: await response.json() as T, status: response.status };
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  return (await requestWithStatus<T>(path, init)).data;
}

export const api = {
  overview: () => request<RoutineOverview>("/api/v1/overview"),
  executionProfiles: async () => (await request<{ profiles: ExecutionProfile[] | null }>("/api/v1/execution-profiles")).profiles ?? [],
  routines: async (includeArchived = false) => {
    const routines: Routine[] = [];
    let cursor = "";
    do {
      const query = new URLSearchParams({ limit: "200", include_archived: String(includeArchived) });
      if (cursor) query.set("cursor", cursor);
      const page = await request<{ routines: Routine[] | null; next_cursor?: string }>(`/api/v1/routines?${query}`);
      routines.push(...(page.routines ?? []));
      cursor = page.next_cursor ?? "";
    } while (cursor);
    return routines;
  },
  routine: (id: string) => request<Routine>(`/api/v1/routines/${encodeURIComponent(id)}`),
  createRoutine: (input: SaveRoutineInput) => request<Routine>("/api/v1/routines", {
    method: "POST", body: JSON.stringify(input),
  }),
  updateRoutine: (id: string, input: SaveRoutineInput) => request<Routine>(`/api/v1/routines/${encodeURIComponent(id)}`, {
    method: "PUT", body: JSON.stringify(input),
  }),
  archiveRoutine: (id: string, archived: boolean, expectedGeneration: number) => request<Routine>(`/api/v1/routines/${encodeURIComponent(id)}/archived`, {
    method: "PUT", body: JSON.stringify({ archived, expected_generation: expectedGeneration }),
  }),
  runRoutine: (id: string, requestKey: string, executionProfileID?: string) => request<WorkDetailV2>(`/api/v1/routines/${encodeURIComponent(id)}/run`, {
    method: "POST", body: JSON.stringify({
      request_key: requestKey,
      execution_profile_id: executionProfileID === "persistent-auto" ? undefined : executionProfileID,
    }),
  }),
  discardRoutineOccurrence: (id: string, pendingDueAt: string) => request<Routine>(`/api/v1/routines/${encodeURIComponent(id)}/discard-occurrence`, {
    method: "POST", body: JSON.stringify({ pending_due_at: pendingDueAt }),
  }),
  workItems: async (cursor = "") => {
    const query = new URLSearchParams({ limit: "50" });
    if (cursor) query.set("cursor", cursor);
    const page = await request<WorkPageV2>(`/api/v1/work?${query}`);
    return { work: page.work ?? [], next_cursor: page.next_cursor ?? null };
  },
  workItem: (id: string) => request<WorkDetailV2>(`/api/v1/work/${encodeURIComponent(id)}`),
  cancelWork: (id: string) => request<WorkDetailV2>(`/api/v1/work/${encodeURIComponent(id)}/cancel`, {
    method: "POST", body: "{}",
  }),
  retryWorkTarget: (workId: string, targetId: string) => request<WorkDetailV2>(`/api/v1/work/${encodeURIComponent(workId)}/targets/${encodeURIComponent(targetId)}/retry`, {
    method: "POST", body: "{}",
  }),
  events: async (attemptID: string, after: number): Promise<AttemptEventPage> => {
    const query = new URLSearchParams({ after: String(after), limit: "100" });
    const page = await request<{ events: AttemptEventPage["events"] | null; next_after: number; has_more: boolean }>(
      `/api/v1/attempts/${encodeURIComponent(attemptID)}/events?${query}`,
    );
    return { ...page, events: page.events ?? [] };
  },
  cancelWorkTarget: (workId: string, targetId: string) => request<WorkDetailV2>(`/api/v1/work/${encodeURIComponent(workId)}/targets/${encodeURIComponent(targetId)}/cancel`, {
    method: "POST", body: "{}",
  }),
  workers: async () => ((await request<{ workers: Worker[] | null }>("/api/v1/workers")).workers ?? []).map(normalizeWorker),
  worker: async (id: string) => normalizeWorker(await request<Worker>(`/api/v1/workers/${encodeURIComponent(id)}`)),
  testWorker: async (id: string) => normalizeWorker(await request<Worker>(`/api/v1/workers/${encodeURIComponent(id)}/test`, {
    method: "POST", body: "{}",
  })),
  repositories: async () => (await request<{ repositories: ManagedRepository[] | null }>("/api/v1/repositories")).repositories ?? [],
  repository: (id: string) => request<ManagedRepository>(`/api/v1/repositories/${encodeURIComponent(id)}`),
  repositoryReadiness: (id: string) => request<ManagedRepositoryReadiness>(`/api/v1/repositories/${encodeURIComponent(id)}/readiness`),
  createRepository: async (remoteIdentity: string) => {
    const response = await requestWithStatus<ManagedRepository>("/api/v1/repositories", {
      method: "POST", body: JSON.stringify({ remote_identity: remoteIdentity }),
    });
    return { repository: response.data, created: response.status === 201 };
  },
  setRepositoryEnabled: (id: string, enabled: boolean) => request<ManagedRepository>(`/api/v1/repositories/${encodeURIComponent(id)}/enabled`, {
    method: "PUT", body: JSON.stringify({ enabled }),
  }),
};

function normalizeWorker(worker: Worker): Worker {
  const capabilities = worker.capabilities?.length ? worker.capabilities : [{
    kind: "runtime" as const,
    name: worker.runtime,
    status: worker.health === "healthy" ? "ready" as const : "unhealthy" as const,
    version: worker.runtime_version,
  }];
  return {
    ...worker,
    labels: worker.labels ?? {},
    capabilities,
    repositories: worker.repositories ?? [],
    retained_worktrees: worker.retained_worktrees ?? [],
    source_access: worker.source_access ?? [],
  };
}
