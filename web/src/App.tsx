import {
  AlertCircle,
  ArrowLeft,
  Check,
  ChevronRight,
  CircleDot,
  Clock3,
  Copy,
  GitBranch,
  HardDrive,
  ListChecks,
  LoaderCircle,
  Menu,
  Play,
  Plus,
  RefreshCw,
  Server,
  Square,
  Users,
  X,
  XCircle,
} from "lucide-react";
import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import {
  useEffect,
  useId,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import { api, APIError } from "./api";
import { duration, eventText, stateLabel, taskStates, timeAgo } from "./format";
import { useVisibleInterval } from "./polling";
import type {
  Attempt,
  AttemptEvent,
  CreateTaskInput,
  Task,
  TaskDetail as TaskDetailType,
  TaskState,
  Worker,
} from "./types";

type Route =
  | { page: "work" }
  | { page: "workers" }
  | { page: "task"; id: string }
  | { page: "worker"; id: string };

function readRoute(): Route {
  const parts = window.location.pathname.split("/").filter(Boolean);
  if (parts[0] === "tasks" && parts[1]) return { page: "task", id: parts[1] };
  if (parts[0] === "workers" && parts[1]) return { page: "worker", id: parts[1] };
  if (parts[0] === "workers") return { page: "workers" };
  return { page: "work" };
}

function routePath(route: Route): string {
  if (route.page === "task") return `/tasks/${route.id}`;
  if (route.page === "worker") return `/workers/${route.id}`;
  return route.page === "workers" ? "/workers" : "/";
}

function invalidateControlPlane(queryClient: QueryClient) {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: ["tasks"] }),
    queryClient.invalidateQueries({ queryKey: ["workers"] }),
  ]);
}

export function App() {
  const [route, setRoute] = useState<Route>(readRoute);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const workInterval = useVisibleInterval(5_000);
  const workerInterval = useVisibleInterval(10_000);
  const queryClient = useQueryClient();

  useEffect(() => {
    const onPopState = () => setRoute(readRoute());
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  const navigate = (next: Route) => {
    window.history.pushState({}, "", routePath(next));
    setRoute(next);
    setMobileNavOpen(false);
    window.scrollTo({ top: 0, behavior: "instant" });
  };

  const tasks = useQuery({
    queryKey: ["tasks"],
    queryFn: api.tasks,
    refetchInterval: workInterval,
  });
  const workers = useQuery({
    queryKey: ["workers"],
    queryFn: api.workers,
    refetchInterval: workerInterval,
  });

  useEffect(() => {
    const refresh = () => {
      if (document.visibilityState === "visible") {
        void invalidateControlPlane(queryClient);
      }
    };
    document.addEventListener("visibilitychange", refresh);
    return () => document.removeEventListener("visibilitychange", refresh);
  }, [queryClient]);

  return (
    <div className="app-shell">
      <aside className={`sidebar ${mobileNavOpen ? "sidebar-open" : ""}`}>
        <div className="brand">
          <div className="brand-mark" aria-hidden="true">
            F
          </div>
          <div>
            <span className="brand-name">Factory</span>
            <span className="brand-subtitle">Control plane</span>
          </div>
        </div>
        <nav aria-label="Primary navigation">
          <button
            className={`nav-item ${route.page === "work" || route.page === "task" ? "active" : ""}`}
            onClick={() => navigate({ page: "work" })}
          >
            <ListChecks size={17} /> Work
          </button>
          <button
            className={`nav-item ${route.page === "workers" || route.page === "worker" ? "active" : ""}`}
            onClick={() => navigate({ page: "workers" })}
          >
            <Users size={17} /> Workers
          </button>
        </nav>
        <div className="sidebar-foot">
          <span className="local-dot" aria-hidden="true" />
          Local control plane
        </div>
      </aside>

      <div className="main-shell">
        <header className="topbar">
          <button
            className="icon-button mobile-menu"
            aria-label="Toggle navigation"
            aria-expanded={mobileNavOpen}
            onClick={() => setMobileNavOpen((open) => !open)}
          >
            {mobileNavOpen ? <X size={19} /> : <Menu size={19} />}
          </button>
          <div className="topbar-title">
            {route.page === "work" && "Work"}
            {route.page === "workers" && "Workers"}
            {route.page === "task" && "Task detail"}
            {route.page === "worker" && "Worker detail"}
          </div>
          <button className="button button-primary" onClick={() => setDrawerOpen(true)}>
            <Plus size={16} /> Delegate task
          </button>
        </header>

        <main>
          {route.page === "work" && (
            <WorkView
              tasks={tasks.data}
              workers={workers.data}
              pending={tasks.isPending}
              error={tasks.error}
              fetching={tasks.isFetching}
              updatedAt={tasks.dataUpdatedAt}
              onTask={(id) => navigate({ page: "task", id })}
              onDelegate={() => setDrawerOpen(true)}
              onRefresh={() => void tasks.refetch()}
            />
          )}
          {route.page === "workers" && (
            <WorkersView
              workers={workers.data}
              pending={workers.isPending}
              error={workers.error}
              fetching={workers.isFetching}
              updatedAt={workers.dataUpdatedAt}
              onWorker={(id) => navigate({ page: "worker", id })}
              onRefresh={() => void workers.refetch()}
            />
          )}
          {route.page === "task" && (
            <TaskDetail
              id={route.id}
              workers={workers.data ?? []}
              onBack={() => navigate({ page: "work" })}
            />
          )}
          {route.page === "worker" && (
            <WorkerDetail id={route.id} onBack={() => navigate({ page: "workers" })} />
          )}
        </main>
      </div>

      {mobileNavOpen && (
        <button
          className="nav-scrim"
          aria-label="Close navigation"
          onClick={() => setMobileNavOpen(false)}
        />
      )}
      {drawerOpen && (
        <DelegateDrawer
          workers={workers.data ?? []}
          workersPending={workers.isPending}
          onClose={() => setDrawerOpen(false)}
          onCreated={(id) => {
            setDrawerOpen(false);
            navigate({ page: "task", id });
          }}
        />
      )}
    </div>
  );
}

interface ViewStateProps {
  fetching: boolean;
  updatedAt: number;
  onRefresh: () => void;
}

function ViewHeader({
  eyebrow,
  title,
  description,
  fetching,
  updatedAt,
  onRefresh,
}: ViewStateProps & { eyebrow: string; title: string; description: string }) {
  return (
    <div className="view-header">
      <div>
        <span className="eyebrow">{eyebrow}</span>
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      <div className="refresh-state" aria-live="polite">
        <span>{updatedAt ? `Updated ${timeAgo(new Date(updatedAt).toISOString())}` : "Waiting for data"}</span>
        <button className="icon-button" aria-label="Refresh" onClick={onRefresh} disabled={fetching}>
          <RefreshCw size={16} className={fetching ? "spin" : ""} />
        </button>
      </div>
    </div>
  );
}

function WorkView({
  tasks,
  workers,
  pending,
  error,
  fetching,
  updatedAt,
  onTask,
  onDelegate,
  onRefresh,
}: ViewStateProps & {
  tasks?: Task[];
  workers?: Worker[];
  pending: boolean;
  error: Error | null;
  onTask: (id: string) => void;
  onDelegate: () => void;
}) {
  if (pending) return <LoadingState label="Loading work" />;
  if (error && !tasks) return <ErrorState error={error} onRetry={onRefresh} />;

  const grouped = Object.fromEntries(
    taskStates.map((state) => [state, (tasks ?? []).filter((task) => task.state === state)]),
  ) as Record<TaskState, Task[]>;
  const workerMap = new Map((workers ?? []).map((worker) => [worker.id, worker]));

  return (
    <div className="page page-work">
      <ViewHeader
        eyebrow="Live queue"
        title="Agent work"
        description="Delegate tasks and follow their progress across the local worker fleet."
        fetching={fetching}
        updatedAt={updatedAt}
        onRefresh={onRefresh}
      />
      {error && <StaleBanner error={error} />}
      {(tasks ?? []).length === 0 ? (
        <EmptyState
          icon={<ListChecks size={22} />}
          title="No work yet"
          description="Delegate the first task to a registered worker. It will stay here through restarts."
          action={<button className="button button-primary" onClick={onDelegate}><Plus size={16} /> Delegate task</button>}
        />
      ) : (
        <div className="work-board" data-testid="work-board">
          {taskStates.map((state) => (
            <section className="work-column" key={state} aria-labelledby={`heading-${state}`}>
              <div className="column-heading">
                <span className={`status-dot status-${state}`} aria-hidden="true" />
                <h2 id={`heading-${state}`}>{stateLabel(state)}</h2>
                <span className="count">{grouped[state].length}</span>
              </div>
              <div className="task-stack">
                {grouped[state].length === 0 ? (
                  <div className="column-empty">No {state} work</div>
                ) : (
                  grouped[state].map((task) => (
                    <TaskCard
                      key={task.id}
                      task={task}
                      worker={workerMap.get(task.worker_id)}
                      onClick={() => onTask(task.id)}
                    />
                  ))
                )}
              </div>
            </section>
          ))}
        </div>
      )}
    </div>
  );
}

function TaskCard({ task, worker, onClick }: { task: Task; worker?: Worker; onClick: () => void }) {
  return (
    <button className="task-card" onClick={onClick}>
      <div className="task-card-top">
        <StatusBadge state={task.state} />
        <ChevronRight size={15} aria-hidden="true" />
      </div>
      <span className="task-title">{task.title}</span>
      <div className="task-meta">
        <span>{worker?.name ?? "Unknown worker"}</span>
        <span aria-hidden="true">·</span>
        <span>{timeAgo(task.created_at)}</span>
      </div>
    </button>
  );
}

function WorkersView({
  workers,
  pending,
  error,
  fetching,
  updatedAt,
  onWorker,
  onRefresh,
}: ViewStateProps & {
  workers?: Worker[];
  pending: boolean;
  error: Error | null;
  onWorker: (id: string) => void;
}) {
  if (pending) return <LoadingState label="Loading workers" />;
  if (error && !workers) return <ErrorState error={error} onRetry={onRefresh} />;

  return (
    <div className="page">
      <ViewHeader
        eyebrow="Worker fleet"
        title="Execution capacity"
        description="Health, capacity, repositories, and retained worktrees reported by every registration."
        fetching={fetching}
        updatedAt={updatedAt}
        onRefresh={onRefresh}
      />
      {error && <StaleBanner error={error} />}
      {(workers ?? []).length === 0 ? (
        <EmptyState
          icon={<Server size={22} />}
          title="No workers registered"
          description="Start a Factory worker and its registration will appear here automatically."
        />
      ) : (
        <div className="workers-list">
          <div className="worker-table-head" aria-hidden="true">
            <span>Worker</span><span>Capacity</span><span>Repositories</span><span>Versions</span><span>Last seen</span><span />
          </div>
          {(workers ?? []).map((worker) => (
            <button className="worker-row" key={worker.id} onClick={() => onWorker(worker.id)}>
              <span className="worker-identity">
                <span className={`presence ${worker.online ? "online" : "offline"}`} aria-hidden="true" />
                <span>
                  <strong>{worker.name}</strong>
                  <small>
                    {worker.online ? "Online" : "Offline"} ·{" "}
                    <span className={worker.health === "healthy" ? "healthy-text" : "danger-text"}>
                      {stateLabel(worker.health)}
                    </span>
                  </small>
                  {worker.current_task_title && <em>{worker.current_task_title}</em>}
                </span>
              </span>
              <span className="capacity-cell">
                <strong>{worker.active_count}/{worker.capacity}</strong>
                <span className="capacity-bar" aria-label={`${worker.active_count} of ${worker.capacity} slots active`}>
                  <span style={{ width: `${(worker.active_count / worker.capacity) * 100}%` }} />
                </span>
              </span>
              <span className="repo-list">
                {worker.repositories.map((repo) => <span className="tag" key={repo.id}>{repo.key}</span>)}
              </span>
              <span className="versions">
                <small>Codex {worker.codex_version || "unknown"}</small>
                <small>Worker {worker.worker_version || "unknown"}</small>
              </span>
              <span className="last-seen">{timeAgo(worker.last_heartbeat)}</span>
              <ChevronRight size={16} className="row-chevron" aria-hidden="true" />
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function TaskDetail({ id, workers, onBack }: { id: string; workers: Worker[]; onBack: () => void }) {
  const interval = useVisibleInterval(2_000);
  const queryClient = useQueryClient();
  const [confirmCancel, setConfirmCancel] = useState(false);
  const detail = useQuery({
    queryKey: ["task", id],
    queryFn: () => api.task(id),
    refetchInterval: (query) => {
      const state = query.state.data?.task.state;
      return state === "queued" || state === "running" ? interval : false;
    },
  });
  const latestAttempt = detail.data?.attempts?.at(-1);
  const events = useQuery({
    queryKey: ["events", latestAttempt?.id],
    queryFn: () => api.events(latestAttempt!.id),
    enabled: Boolean(latestAttempt),
    refetchInterval: detail.data?.task.state === "running" ? interval : false,
  });
  const cancel = useMutation({
    mutationFn: () => api.cancelTask(id),
    onSuccess: async (next) => {
      queryClient.setQueryData(["task", id], next);
      setConfirmCancel(false);
      await invalidateControlPlane(queryClient);
    },
  });
  const retry = useMutation({
    mutationFn: () => api.retryExecution(detail.data!.execution.id),
    onSuccess: async (next) => {
      queryClient.setQueryData(["task", id], next);
      await invalidateControlPlane(queryClient);
    },
  });

  if (detail.isPending) return <LoadingState label="Loading task" />;
  if (!detail.data) return <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />;

  const data = detail.data;
  const worker = workers.find((item) => item.id === data.execution.assigned_worker_id);
  const active = data.task.state === "queued" || data.task.state === "running";
  const retryable = data.task.state === "failed" || data.task.state === "cancelled";

  return (
    <div className="page detail-page">
      <button className="back-button" onClick={onBack}><ArrowLeft size={16} /> All work</button>
      <div className="detail-heading">
        <div>
          <StatusBadge state={data.task.state} />
          <h1>{data.task.title}</h1>
          <p>Created {new Date(data.task.created_at).toLocaleString()}</p>
        </div>
        <div className="detail-actions">
          {active && !confirmCancel && (
            <button className="button button-danger-secondary" onClick={() => setConfirmCancel(true)}>
              <Square size={14} /> Cancel
            </button>
          )}
          {confirmCancel && (
            <div className="confirm-action" role="alert">
              <span>Cancel this task?</span>
              <button className="button button-danger" onClick={() => cancel.mutate()} disabled={cancel.isPending}>
                {cancel.isPending ? "Cancelling…" : "Confirm cancel"}
              </button>
              <button className="button button-secondary" onClick={() => setConfirmCancel(false)}>Keep running</button>
            </div>
          )}
          {retryable && (
            <button className="button button-primary" onClick={() => retry.mutate()} disabled={retry.isPending}>
              <RefreshCw size={15} className={retry.isPending ? "spin" : ""} />
              {retry.isPending ? "Retrying…" : "Retry task"}
            </button>
          )}
        </div>
      </div>
      {detail.error && <StaleBanner error={detail.error} />}
      {(cancel.error || retry.error) && <InlineError error={cancel.error ?? retry.error} />}
      {!data.repository_available && (
        <div className="warning-banner"><AlertCircle size={17} /> Repository unavailable on the assigned worker. Queued work will wait.</div>
      )}
      {data.execution.cancellation_requested && data.task.state === "running" && (
        <div className="warning-banner"><Clock3 size={17} /> Cancellation requested. The worker will stop this task on its next heartbeat.</div>
      )}

      <div className="detail-grid">
        <section className="panel detail-main">
          <PanelHeading title="Description" />
          <div className="long-copy">{data.task.description}</div>
        </section>
        <section className="panel">
          <PanelHeading title="Assignment" />
          <dl className="metadata">
            <div><dt>Worker</dt><dd>{worker?.name ?? data.execution.assigned_worker_id}</dd></div>
            <div><dt>Repository</dt><dd>{data.repository.key}</dd></div>
            <div><dt>Remote</dt><dd className="break-anywhere">{data.repository.remote_identity}</dd></div>
            <div><dt>Timeout</dt><dd>{formatTimeout(data.task.timeout_seconds)}</dd></div>
            <div><dt>Elapsed</dt><dd>{taskElapsed(data)}</dd></div>
          </dl>
        </section>
      </div>

      <section className="panel progress-panel">
        <PanelHeading title="Progress" aside={`${events.data?.length ?? 0} events`} />
        {events.error && <InlineError error={events.error} />}
        {!latestAttempt ? (
          <div className="quiet-empty">Progress will appear when the worker starts this task.</div>
        ) : events.isPending && events.data === undefined ? (
          <LoadingLine label="Loading progress" />
        ) : events.data === undefined ? null : events.data.length === 0 ? (
          <div className="quiet-empty">Progress will appear when the worker starts this task.</div>
        ) : (
          <ol className="event-list">
            {(events.data ?? []).map((event) => <ProgressEvent key={event.sequence} event={event} />)}
          </ol>
        )}
      </section>

      {(data.attempts ?? []).length > 0 && (
        <section className="panel attempts-panel">
          <PanelHeading title="Attempts" aside={`${data.attempts?.length ?? 0} total`} />
          {[...(data.attempts ?? [])].reverse().map((attempt, index) => (
            <AttemptRow key={attempt.id} attempt={attempt} current={index === 0} />
          ))}
        </section>
      )}
    </div>
  );
}

function WorkerDetail({ id, onBack }: { id: string; onBack: () => void }) {
  const interval = useVisibleInterval(10_000);
  const worker = useQuery({
    queryKey: ["worker", id],
    queryFn: () => api.worker(id),
    refetchInterval: interval,
  });
  const [copied, setCopied] = useState<string>();

  if (worker.isPending) return <LoadingState label="Loading worker" />;
  if (!worker.data) return <ErrorState error={worker.error} onRetry={() => void worker.refetch()} />;

  const data = worker.data;
  const grouped = (data.retained_worktrees ?? []).reduce((groups, worktree) => {
    const current = groups.get(worktree.repository_id) ?? [];
    current.push(worktree);
    groups.set(worktree.repository_id, current);
    return groups;
  }, new Map<string, Worker["retained_worktrees"]>());
  const copy = async (attemptID: string, command: string) => {
    await navigator.clipboard.writeText(command);
    setCopied(attemptID);
    window.setTimeout(() => setCopied(undefined), 1_500);
  };

  return (
    <div className="page detail-page">
      <button className="back-button" onClick={onBack}><ArrowLeft size={16} /> All workers</button>
      <div className="detail-heading worker-detail-heading">
        <div>
          <div className="worker-state-line">
            <span className={`presence ${data.online ? "online" : "offline"}`} aria-hidden="true" />
            <span>{data.online ? "Online" : "Offline"}</span>
            <span>·</span>
            <span className={data.health === "healthy" ? "healthy-text" : "danger-text"}>{stateLabel(data.health)}</span>
          </div>
          <h1>{data.name}</h1>
          <p>Registered {new Date(data.registered_at).toLocaleString()}</p>
        </div>
        <span className="worker-id" title={data.id}>{data.id}</span>
      </div>
      {worker.error && <StaleBanner error={worker.error} />}

      <div className="worker-summary">
        <SummaryItem label="Active capacity" value={`${data.active_count} / ${data.capacity}`} icon={<CircleDot size={17} />} />
        <SummaryItem label="Codex version" value={data.codex_version || "Unknown"} icon={<Play size={17} />} />
        <SummaryItem label="Worker version" value={data.worker_version || "Unknown"} icon={<Server size={17} />} />
        <SummaryItem label="Last seen" value={timeAgo(data.last_heartbeat)} icon={<Clock3 size={17} />} />
      </div>

      {data.current_task_title && (
        <section className="panel">
          <PanelHeading title="Current work" />
          <div className="current-work"><LoaderCircle size={17} className="spin" /> {data.current_task_title}</div>
        </section>
      )}

      <section className="panel">
        <PanelHeading title="Repositories" aside={`${data.repositories.length} advertised`} />
        <div className="repository-rows">
          {data.repositories.map((repo) => (
            <div className="repository-row" key={repo.id}>
              <GitBranch size={17} />
              <span><strong>{repo.key}</strong><small>{repo.remote_identity}</small></span>
              <span className="retained-count">{repo.retained_count} retained</span>
            </div>
          ))}
        </div>
      </section>

      <section className="panel">
        <PanelHeading title="Retained worktrees" aside={`${data.retained_worktrees?.length ?? 0} retained`} />
        {(data.retained_worktrees ?? []).length === 0 ? (
          <div className="quiet-empty">No worktrees need local inspection or cleanup.</div>
        ) : (
          [...grouped.entries()].map(([repositoryID, worktrees]) => {
            const repo = data.repositories.find((candidate) => candidate.id === repositoryID);
            return (
              <div className="worktree-group" key={repositoryID}>
                <h3>{repo?.key ?? `Repository ${repositoryID}`}</h3>
                {worktrees.map((worktree) => (
                  <div className="worktree-card" key={worktree.attempt_id}>
                    <div className="worktree-title">
                      <HardDrive size={16} />
                      <span><strong>Attempt {worktree.attempt_id}</strong><small>{worktree.reason}</small></span>
                    </div>
                    <div className="worktree-path">{worktree.path}</div>
                    <div className="command-row">
                      <code>{worktree.cleanup_command}</code>
                      <button className="icon-button" aria-label={`Copy cleanup command for ${worktree.attempt_id}`} onClick={() => void copy(worktree.attempt_id, worktree.cleanup_command)}>
                        {copied === worktree.attempt_id ? <Check size={16} /> : <Copy size={16} />}
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            );
          })
        )}
      </section>
    </div>
  );
}

function DelegateDrawer({
  workers,
  workersPending,
  onClose,
  onCreated,
}: {
  workers: Worker[];
  workersPending: boolean;
  onClose: () => void;
  onCreated: (id: string) => void;
}) {
  const queryClient = useQueryClient();
  const titleID = useId();
  const descriptionID = useId();
  const titleRef = useRef<HTMLInputElement>(null);
  const drawerRef = useRef<HTMLElement>(null);
  const requestRef = useRef<{ fingerprint: string; key: string } | undefined>(undefined);
  const [workerID, setWorkerID] = useState("");
  const [repositoryID, setRepositoryID] = useState("");
  const [timeout, setTimeout] = useState("7200");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const selectedWorker = workers.find((worker) => worker.id === workerID);
  const repositories = selectedWorker?.repositories ?? [];

  useEffect(() => {
    titleRef.current?.focus();
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
      if (event.key !== "Tab") return;
      const focusable = [...(drawerRef.current?.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
      ) ?? [])];
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable.at(-1)!;
      if (event.shiftKey && (document.activeElement === first || !drawerRef.current?.contains(document.activeElement))) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, [onClose]);

  const create = useMutation({
    mutationFn: api.createTask,
    onSuccess: async (detail) => {
      await invalidateControlPlane(queryClient);
      onCreated(detail.task.id);
    },
  });

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const title = String(form.get("title") ?? "").trim();
    const description = String(form.get("description") ?? "");
    const nextErrors: Record<string, string> = {};
    if (!title) nextErrors.title = "Enter a task title.";
    else if (Array.from(title).length > 200) nextErrors.title = "Keep the title to 200 characters.";
    if (!description.trim()) nextErrors.description = "Enter a task description.";
    if (!workerID) nextErrors.worker = "Choose a worker.";
    if (!repositoryID) nextErrors.repository = "Choose a repository.";
    const timeoutSeconds = Number(timeout);
    if (!Number.isInteger(timeoutSeconds) || timeoutSeconds < 1 || timeoutSeconds > 28_800) {
      nextErrors.timeout = "Choose a timeout from one minute to eight hours.";
    }
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length) return;
    const payload = {
      title,
      description,
      worker_id: workerID,
      repository_id: repositoryID,
      timeout_seconds: timeoutSeconds,
    };
    const fingerprint = JSON.stringify(payload);
    if (requestRef.current?.fingerprint !== fingerprint) {
      requestRef.current = { fingerprint, key: crypto.randomUUID() };
    }
    const input: CreateTaskInput = {
      request_key: requestRef.current.key,
      ...payload,
    };
    create.mutate(input);
  };

  return (
    <div className="drawer-layer">
      <button className="drawer-scrim" aria-label="Close delegate task" onClick={onClose} />
      <aside ref={drawerRef} className="drawer" role="dialog" aria-modal="true" aria-labelledby="delegate-heading">
        <div className="drawer-header">
          <div>
            <span className="eyebrow">New work</span>
            <h2 id="delegate-heading">Delegate task</h2>
          </div>
          <button className="icon-button" aria-label="Close" onClick={onClose}><X size={19} /></button>
        </div>
        <form onSubmit={submit} noValidate>
          <div className="drawer-body">
            <Field label="Title" htmlFor={titleID} error={errors.title}>
              <input ref={titleRef} id={titleID} name="title" aria-invalid={Boolean(errors.title)} placeholder="Fix stale worker status" />
            </Field>
            <Field label="Description" htmlFor={descriptionID} error={errors.description} hint="This becomes the Codex prompt.">
              <textarea id={descriptionID} name="description" rows={9} aria-invalid={Boolean(errors.description)} placeholder="Describe the outcome, constraints, and checks…" />
            </Field>
            <Field label="Worker" htmlFor="delegate-worker" error={errors.worker}>
              <select
                id="delegate-worker"
                value={workerID}
                onChange={(event) => {
                  setWorkerID(event.target.value);
                  setRepositoryID("");
                }}
                disabled={workersPending || workers.length === 0}
              >
                <option value="">{workersPending ? "Loading workers…" : workers.length ? "Choose a worker" : "No workers registered"}</option>
                {workers.map((worker) => (
                  <option key={worker.id} value={worker.id}>{worker.name} · {worker.online ? "online" : "offline"}</option>
                ))}
              </select>
            </Field>
            {selectedWorker && !selectedWorker.online && (
              <div className="warning-banner compact"><AlertCircle size={16} /> This worker is offline. The task will queue until it returns.</div>
            )}
            {selectedWorker?.health === "unhealthy" && (
              <div className="warning-banner compact"><AlertCircle size={16} /> This worker is unhealthy and will not claim work until it recovers.</div>
            )}
            <Field label="Repository" htmlFor="delegate-repository" error={errors.repository}>
              <select id="delegate-repository" value={repositoryID} onChange={(event) => setRepositoryID(event.target.value)} disabled={!workerID}>
                <option value="">{workerID ? (repositories.length ? "Choose a repository" : "No repositories advertised") : "Choose a worker first"}</option>
                {repositories.map((repo) => <option key={repo.id} value={repo.id}>{repo.key} · {repo.remote_identity}</option>)}
              </select>
            </Field>
            <Field label="Timeout" htmlFor="delegate-timeout" error={errors.timeout}>
              <select id="delegate-timeout" value={timeout} onChange={(event) => setTimeout(event.target.value)}>
                <option value="1800">30 minutes</option>
                <option value="3600">1 hour</option>
                <option value="7200">2 hours</option>
                <option value="14400">4 hours</option>
                <option value="28800">8 hours</option>
              </select>
            </Field>
            {create.error && <InlineError error={create.error} />}
          </div>
          <div className="drawer-footer">
            <button type="button" className="button button-secondary" onClick={onClose}>Cancel</button>
            <button type="submit" className="button button-primary" disabled={create.isPending || workers.length === 0}>
              {create.isPending ? <><LoaderCircle size={16} className="spin" /> Delegating…</> : <><Plus size={16} /> Delegate task</>}
            </button>
          </div>
        </form>
      </aside>
    </div>
  );
}

function Field({
  label,
  htmlFor,
  error,
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  error?: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <div className="field">
      <label htmlFor={htmlFor}>{label}</label>
      {children}
      {error ? <span className="field-error">{error}</span> : hint ? <span className="field-hint">{hint}</span> : null}
    </div>
  );
}

function StatusBadge({ state }: { state: string }) {
  return <span className={`status-badge status-${state}`}><span className="status-dot" />{stateLabel(state)}</span>;
}

function PanelHeading({ title, aside }: { title: string; aside?: string }) {
  return <div className="panel-heading"><h2>{title}</h2>{aside && <span>{aside}</span>}</div>;
}

function ProgressEvent({ event }: { event: AttemptEvent }) {
  return (
    <li>
      <span className="event-marker" aria-hidden="true" />
      <div>
        <span className="event-kind">{stateLabel(event.kind)}</span>
        <p>{eventText(event)}</p>
        <time dateTime={event.server_time}>{new Date(event.server_time).toLocaleTimeString()}</time>
      </div>
    </li>
  );
}

function AttemptRow({ attempt, current }: { attempt: Attempt; current: boolean }) {
  const [open, setOpen] = useState(current);
  return (
    <div className="attempt-row">
      <button onClick={() => setOpen((value) => !value)} aria-expanded={open}>
        <span><strong>Attempt {attempt.attempt_number}</strong><small>{attempt.id}</small></span>
        <span className="attempt-state"><StatusBadge state={attempt.state} /><ChevronRight size={16} className={open ? "rotate" : ""} /></span>
      </button>
      {open && (
        <div className="attempt-content">
          <dl className="metadata compact-metadata">
            <div><dt>Started</dt><dd>{attempt.started_at ? new Date(attempt.started_at).toLocaleString() : "Not started"}</dd></div>
            <div><dt>Duration</dt><dd>{duration(attempt.started_at ?? attempt.created_at, attempt.completed_at)}</dd></div>
          </dl>
          {attempt.result && <div className="attempt-output success-output"><strong>Result</strong><pre>{attempt.result}</pre></div>}
          {attempt.error && <div className="attempt-output error-output"><strong>Error</strong><pre>{attempt.error}</pre></div>}
          {!attempt.result && !attempt.error && <div className="quiet-empty">No result or error recorded yet.</div>}
        </div>
      )}
    </div>
  );
}

function SummaryItem({ label, value, icon }: { label: string; value: string; icon: ReactNode }) {
  return <div className="summary-item"><span className="summary-icon">{icon}</span><span><small>{label}</small><strong>{value}</strong></span></div>;
}

function EmptyState({ icon, title, description, action }: { icon: ReactNode; title: string; description: string; action?: ReactNode }) {
  return <div className="empty-state"><span className="empty-icon">{icon}</span><h2>{title}</h2><p>{description}</p>{action}</div>;
}

function LoadingState({ label }: { label: string }) {
  return <div className="page centered-state" role="status"><LoaderCircle size={24} className="spin" /><span>{label}</span></div>;
}

function LoadingLine({ label }: { label: string }) {
  return <div className="loading-line" role="status"><LoaderCircle size={16} className="spin" />{label}</div>;
}

function ErrorState({ error, onRetry }: { error: Error | null; onRetry: () => void }) {
  return (
    <div className="page centered-state error-state" role="alert">
      <span className="empty-icon danger"><XCircle size={22} /></span>
      <h1>Couldn’t load this view</h1>
      <p>{errorMessage(error)}</p>
      <button className="button button-secondary" onClick={onRetry}><RefreshCw size={15} /> Try again</button>
    </div>
  );
}

function StaleBanner({ error }: { error: Error }) {
  return <div className="stale-banner" role="status"><AlertCircle size={16} /> Showing the last available data. Refresh failed: {errorMessage(error)}</div>;
}

function InlineError({ error }: { error: Error | null }) {
  if (!error) return null;
  return <div className="inline-error" role="alert"><AlertCircle size={16} /> {errorMessage(error)}</div>;
}

function errorMessage(error: Error | null): string {
  if (!error) return "The server returned no data.";
  if (error instanceof APIError) return `${error.message} (${error.code})`;
  return error.message || "The request failed.";
}

function formatTimeout(seconds: number): string {
  if (seconds % 3600 === 0) return `${seconds / 3600}h`;
  if (seconds % 60 === 0) return `${seconds / 60}m`;
  return `${seconds}s`;
}

function taskElapsed(detail: TaskDetailType): string {
  const latest = detail.attempts?.at(-1);
  if (!latest) return duration(detail.task.created_at);
  return duration(latest.started_at ?? latest.created_at, latest.completed_at);
}
