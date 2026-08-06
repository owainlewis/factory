import { BookOpenText, Bot, Boxes, Gauge, GitBranch, ListChecks, Menu, Play, Plus, Workflow as AutomationIcon, X } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { api } from "./api";
import { invalidateControlPlane } from "./controlPlaneQueries";
import { DelegateModal } from "./DelegateModal";
import { Overview } from "./Overview";
import { RepositoriesView, RepositoryDetail } from "./Repositories";
import { TaskDetail } from "./TaskDetail";
import { useVisibleInterval } from "./polling";
import type { Task, TaskPage, Worker } from "./types";
import { WorkersView, WorkerDetail } from "./Workers";
import { WorkView } from "./Work";
import { WorkflowDetail, WorkflowsView } from "./Workflows";
import { AutomationDetail, AutomationsView } from "./Automations";
import { DefinitionDetail, DefinitionsView } from "./Definitions";
import { RunDetail, RunsView } from "./Runs";

type Route =
  | { page: "overview" }
  | { page: "work" }
  | { page: "runs"; create?: boolean }
  | { page: "run"; id: string }
  | { page: "workers" }
  | { page: "repositories" }
  | { page: "task"; id: string }
  | { page: "worker"; id: string }
  | { page: "repository"; id: string }
  | { page: "definitions"; archived?: boolean }
  | { page: "definition"; id: string; archived?: boolean }
  | { page: "workflows" }
  | { page: "workflow"; id: string }
  | { page: "automations" }
  | { page: "automation"; id: string };

function readRoute(): Route {
  const parts = window.location.pathname.split("/").filter(Boolean);
  const archived = new URLSearchParams(window.location.search).get("archived") === "true";
  const createRun = new URLSearchParams(window.location.search).get("new") === "true";
  if (parts[0] === "runs" && parts[1]) return { page: "run", id: parts[1] };
  if (parts[0] === "runs") return { page: "runs", create: createRun };
  if (parts[0] === "tasks" && parts[1]) return { page: "task", id: parts[1] };
  if (parts[0] === "workers" && parts[1]) return { page: "worker", id: parts[1] };
  if (parts[0] === "definitions" && parts[1]) return { page: "definition", id: parts[1], archived };
  if (parts[0] === "definitions") return { page: "definitions", archived };
  if (parts[0] === "workflows" && parts[1]) return { page: "workflow", id: parts[1] };
  if (parts[0] === "workflows") return { page: "workflows" };
  if (parts[0] === "automations" && parts[1]) return { page: "automation", id: parts[1] };
  if (parts[0] === "automations") return { page: "automations" };
  if (parts[0] === "workers") return { page: "workers" };
  if (parts[0] === "repositories" && parts[1]) return { page: "repository", id: parts[1] };
  if (parts[0] === "repositories") return { page: "repositories" };
  if (parts[0] === "work") return { page: "work" };
  return { page: "overview" };
}

function routePath(route: Route): string {
  if (route.page === "task") return `/tasks/${route.id}`;
  if (route.page === "run") return `/runs/${route.id}`;
  if (route.page === "runs") return `/runs${route.create ? "?new=true" : ""}`;
  if (route.page === "worker") return `/workers/${route.id}`;
  if (route.page === "definition") return `/definitions/${route.id}${route.archived ? "?archived=true" : ""}`;
  if (route.page === "definitions") return `/definitions${route.archived ? "?archived=true" : ""}`;
  if (route.page === "workflow") return `/workflows/${route.id}`;
  if (route.page === "workflows") return "/workflows";
  if (route.page === "automation") return `/automations/${route.id}`;
  if (route.page === "automations") return "/automations";
  if (route.page === "workers") return "/workers";
  if (route.page === "repository") return `/repositories/${route.id}`;
  if (route.page === "repositories") return "/repositories";
  return route.page === "work" ? "/work" : "/";
}

export function App() {
  const [route, setRoute] = useState<Route>(readRoute);
  const [delegateRequest, setDelegateRequest] = useState<{ workerID?: string } | null>(null);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [taskHistory, setTaskHistory] = useState<Task[]>([]);
  const [taskHistoryCursor, setTaskHistoryCursor] = useState<string | null>();
  const previousTaskHeadCursor = useRef<string | null | undefined>(undefined);
  const deletedTaskIDs = useRef(new Set<string>());
  const workInterval = useVisibleInterval(5_000);
  const workerInterval = useVisibleInterval(10_000);
  const delegateTrigger = useRef<HTMLElement | null>(null);
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
  const openDelegate = (workerID?: string) => {
    delegateTrigger.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setDelegateRequest({ workerID });
  };
  const closeDelegate = () => {
    const trigger = delegateTrigger.current;
    setDelegateRequest(null);
    window.setTimeout(() => trigger?.focus(), 0);
  };

  const tasks = useQuery({
    queryKey: ["tasks", "head"],
    queryFn: async () => withoutDeletedTasks(await api.tasks(), deletedTaskIDs.current),
    refetchInterval: workInterval,
  });
  const loadTaskHistory = useMutation({
    mutationFn: async ({ cursor }: { cursor: string; headCursor: string | null }) =>
      withoutDeletedTasks(await api.tasks(cursor), deletedTaskIDs.current),
    onSuccess: (page, request) => {
      setTaskHistory((current) => mergeTasks(page.tasks, current));
      if (previousTaskHeadCursor.current === request.headCursor) {
        setTaskHistoryCursor(page.next_cursor);
      }
    },
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

  useEffect(() => {
    if (!tasks.data) return;
    const boundaryChanged = previousTaskHeadCursor.current !== tasks.data.next_cursor;
    setTaskHistoryCursor((current) => boundaryChanged ? tasks.data.next_cursor : current);
    previousTaskHeadCursor.current = tasks.data.next_cursor;
  }, [tasks.data]);

  const taskItems = tasks.data
    ? mergeTasks(tasks.data.tasks, taskHistory)
    : taskHistory.length > 0
      ? taskHistory
      : undefined;
  const detailWorkerState = delegateRequest?.workerID
    ? queryClient.getQueryState<Worker>(["worker", delegateRequest.workerID])
    : undefined;
  const detailWorker = detailWorkerState?.data;
  const fleetWorker = detailWorker
    ? (workers.data ?? []).find((worker) => worker.id === detailWorker.id)
    : undefined;
  const detailWorkerIsFresh = detailWorker && (!fleetWorker || (detailWorkerState?.dataUpdatedAt ?? 0) >= workers.dataUpdatedAt);
  const delegateWorkers = detailWorkerIsFresh
    ? [detailWorker, ...(workers.data ?? []).filter((worker) => worker.id !== detailWorker.id)]
    : workers.data ?? [];

  return (
    <div className="app-shell">
      <aside className={`sidebar ${mobileNavOpen ? "sidebar-open" : ""}`}>
        <div className="brand">
          <div className="brand-mark" aria-hidden="true">
            <Boxes size={18} strokeWidth={2.2} />
          </div>
          <div>
            <span className="brand-name">Factory</span>
            <span className="brand-subtitle">control plane</span>
          </div>
        </div>
        <nav aria-label="Primary navigation">
          <button
            className={`nav-item ${route.page === "overview" ? "active" : ""}`}
            aria-current={route.page === "overview" ? "page" : undefined}
            onClick={() => navigate({ page: "overview" })}
          >
            <Gauge size={17} /> Overview
          </button>
          <button
            className={`nav-item ${route.page === "runs" || route.page === "run" ? "active" : ""}`}
            aria-current={route.page === "runs" ? "page" : undefined}
            onClick={() => navigate({ page: "runs" })}
          >
            <Play size={17} /> Runs
          </button>
          <button
            className={`nav-item ${route.page === "work" || route.page === "task" ? "active" : ""}`}
            aria-current={route.page === "work" ? "page" : undefined}
            onClick={() => navigate({ page: "work" })}
          >
            <ListChecks size={17} /> Work
          </button>
          <button
            className={`nav-item ${route.page === "definitions" || route.page === "definition" ? "active" : ""}`}
            aria-current={route.page === "definitions" ? "page" : undefined}
            onClick={() => navigate({ page: "definitions" })}
          >
            <BookOpenText size={17} /> Definitions
          </button>
          <button
            className={`nav-item ${route.page === "workflows" || route.page === "workflow" ? "active" : ""}`}
            aria-current={route.page === "workflows" ? "page" : undefined}
            onClick={() => navigate({ page: "workflows" })}
          >
            <BookOpenText size={17} /> Runbooks
          </button>
          <button
            className={`nav-item ${route.page === "automations" || route.page === "automation" ? "active" : ""}`}
            aria-current={route.page === "automations" ? "page" : undefined}
            onClick={() => navigate({ page: "automations" })}
          >
            <AutomationIcon size={17} /> Automations
          </button>
          <button
            className={`nav-item ${route.page === "workers" || route.page === "worker" ? "active" : ""}`}
            aria-current={route.page === "workers" ? "page" : undefined}
            onClick={() => navigate({ page: "workers" })}
          >
			<Bot size={17} /> Runners
          </button>
          <button
            className={`nav-item ${route.page === "repositories" || route.page === "repository" ? "active" : ""}`}
            aria-current={route.page === "repositories" ? "page" : undefined}
            onClick={() => navigate({ page: "repositories" })}
          >
            <GitBranch size={17} /> Repositories
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
            {route.page === "overview" && "Overview"}
            {route.page === "work" && "Work"}
			{route.page === "runs" && "Runs"}
			{route.page === "run" && "Run detail"}
			{route.page === "workers" && "Runners"}
            {route.page === "task" && "Task detail"}
			{route.page === "worker" && "Runner detail"}
            {route.page === "repositories" && "Repositories"}
            {route.page === "repository" && "Repository detail"}
            {route.page === "definitions" && "Definitions"}
            {route.page === "definition" && "Definition detail"}
            {route.page === "workflows" && "Runbooks"}
            {route.page === "workflow" && "Runbook detail"}
            {route.page === "automations" && "Automations"}
            {route.page === "automation" && "Automation detail"}
          </div>
          <div className="detail-actions">
            <button className="button button-secondary" onClick={() => openDelegate()}>
              <Plus size={16} /> Delegate task
            </button>
            <button className="button button-primary" onClick={() => navigate({ page: "runs", create: true })}>
              <Play size={16} /> Run once
            </button>
          </div>
        </header>

        <main>
          {route.page === "overview" && <Overview />}
          {route.page === "runs" && (
            <RunsView
              createOpen={Boolean(route.create)}
              onCreateOpenChange={(create) => navigate({ page: "runs", create })}
              onRun={(id) => navigate({ page: "run", id })}
            />
          )}
          {route.page === "run" && <RunDetail id={route.id} onBack={() => navigate({ page: "runs" })} />}
          {route.page === "work" && (
            <WorkView
              tasks={taskItems}
              workers={workers.data}
              pending={tasks.isPending}
              error={tasks.error ?? loadTaskHistory.error}
              fetching={tasks.isFetching}
              updatedAt={tasks.dataUpdatedAt}
              onTask={(id) => navigate({ page: "task", id })}
              onDelegate={() => openDelegate()}
              onRefresh={() => void tasks.refetch()}
              hasMore={Boolean(taskHistoryCursor)}
              loadingMore={loadTaskHistory.isPending}
              onLoadMore={() => {
                if (taskHistoryCursor) {
                  loadTaskHistory.mutate({
                    cursor: taskHistoryCursor,
                    headCursor: previousTaskHeadCursor.current ?? null,
                  });
                }
              }}
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
          {route.page === "repositories" && (
            <RepositoriesView onRepository={(id) => navigate({ page: "repository", id })} />
          )}
          {route.page === "task" && (
            <TaskDetail
              id={route.id}
              workers={workers.data ?? []}
              onBack={() => navigate({ page: "work" })}
              onDeleted={() => {
                deletedTaskIDs.current.add(route.id);
                queryClient.setQueryData<TaskPage>(["tasks", "head"], (current) =>
                  current
                    ? { ...current, tasks: current.tasks.filter((task) => task.id !== route.id) }
                    : current
                );
                setTaskHistory((current) => current.filter((task) => task.id !== route.id));
                navigate({ page: "work" });
              }}
            />
          )}
          {route.page === "worker" && (
            <WorkerDetail
              id={route.id}
              onBack={() => navigate({ page: "workers" })}
              onDelegate={() => openDelegate(route.id)}
            />
          )}
          {route.page === "repository" && (
            <RepositoryDetail
              id={route.id}
              onBack={() => navigate({ page: "repositories" })}
            />
          )}
          {route.page === "definitions" && (
            <DefinitionsView
              key={route.archived ? "archived" : "active"}
              archived={Boolean(route.archived)}
              onArchivedChange={(archived) => navigate({ page: "definitions", archived })}
              onDefinition={(id) => navigate({ page: "definition", id, archived: route.archived })}
            />
          )}
          {route.page === "definition" && (
            <DefinitionDetail
              id={route.id}
              onBack={() => navigate({ page: "definitions", archived: route.archived })}
              onStateChanged={() => navigate({ page: "definitions" })}
            />
          )}
          {route.page === "workflows" && (
            <WorkflowsView onWorkflow={(id) => navigate({ page: "workflow", id })} />
          )}
          {route.page === "workflow" && (
            <WorkflowDetail id={route.id} onBack={() => navigate({ page: "workflows" })} />
          )}
          {route.page === "automations" && (
            <AutomationsView onAutomation={(id) => navigate({ page: "automation", id })} />
          )}
          {route.page === "automation" && (
            <AutomationDetail
              id={route.id}
              onBack={() => navigate({ page: "automations" })}
              onTask={(taskID) => navigate({ page: "task", id: taskID })}
            />
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
      {delegateRequest && (
        <DelegateModal
          workers={delegateWorkers}
          workersPending={workers.isPending && delegateWorkers.length === 0}
          initialWorkerID={delegateRequest.workerID}
          onClose={closeDelegate}
          onCreated={(id) => {
            setDelegateRequest(null);
            navigate({ page: "task", id });
          }}
        />
      )}
    </div>
  );
}

function mergeTasks(...groups: Task[][]): Task[] {
  const unique = new Map<string, Task>();
  for (const group of groups) {
    for (const task of group) {
      if (!unique.has(task.id)) unique.set(task.id, task);
    }
  }
  return [...unique.values()].sort((left, right) => {
    const created = Date.parse(right.created_at) - Date.parse(left.created_at);
    if (created !== 0) return created;
    if (left.id === right.id) return 0;
    return left.id < right.id ? 1 : -1;
  });
}

function withoutDeletedTasks(page: TaskPage, deletedTaskIDs: Set<string>): TaskPage {
  return {
    ...page,
    tasks: page.tasks.filter((task) => !deletedTaskIDs.has(task.id)),
  };
}
