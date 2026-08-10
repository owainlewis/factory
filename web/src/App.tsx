import { BookOpenText, Bot, Boxes, Gauge, GitBranch, ListChecks, Menu, Play, Plus, Workflow as AutomationIcon, X } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
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
import { WorkflowDetail, WorkflowsView } from "./Workflows";
import { AutomationDetail, AutomationsView } from "./Automations";
import { DefinitionDetail, DefinitionsView } from "./Definitions";
import { RunDetail, WorkView, type WorkViewMode } from "./Work";
import { deletedWorkTaskIDsKey } from "./workQueries";

type Route =
  | { page: "overview" }
  | { page: "work"; view?: WorkViewMode; create?: boolean }
  | { page: "run"; id: string; jobID?: string; view?: WorkViewMode }
  | { page: "workers" }
  | { page: "repositories" }
  | { page: "task"; id: string; view?: WorkViewMode }
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
  const search = new URLSearchParams(window.location.search);
  const archived = search.get("archived") === "true";
  const createWork = search.get("new") === "true";
  const view = workViewMode(search.get("view"));
  if (parts[0] === "work" && parts[1]) {
    return { page: "run", id: parts[1], jobID: search.get("job") ?? undefined, view };
  }
  if (parts[0] === "runs" && parts[1]) {
    return { page: "run", id: parts[1], jobID: search.get("job") ?? undefined, view };
  }
  if (parts[0] === "runs" || parts[0] === "work") return { page: "work", view, create: createWork };
  if (parts[0] === "tasks" && parts[1]) return { page: "task", id: parts[1], view };
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
  return { page: "overview" };
}

function routePath(route: Route): string {
  if (route.page === "task") return detailPath(`/tasks/${route.id}`, route.view);
  if (route.page === "run") return detailPath(`/work/${route.id}`, route.view, route.jobID);
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
  if (route.page === "work") {
    const search = new URLSearchParams();
    if (route.view && route.view !== "table") search.set("view", route.view);
    if (route.create) search.set("new", "true");
    const query = search.toString();
    return `/work${query ? `?${query}` : ""}`;
  }
  return "/";
}

function workViewMode(value: string | null): WorkViewMode {
  return value === "list" || value === "kanban" ? value : "table";
}

function detailPath(path: string, view?: WorkViewMode, jobID?: string): string {
  const search = new URLSearchParams();
  if (jobID) search.set("job", jobID);
  if (view && view !== "table") search.set("view", view);
  const query = search.toString();
  return `${path}${query ? `?${query}` : ""}`;
}

export function App() {
  const [route, setRoute] = useState<Route>(readRoute);
  const [delegateRequest, setDelegateRequest] = useState<{ workerID?: string } | null>(null);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const workerInterval = useVisibleInterval(10_000);
  const delegateTrigger = useRef<HTMLElement | null>(null);
  const queryClient = useQueryClient();
	const productUpgrade = useQuery({
		queryKey: ["product-upgrade"],
		queryFn: api.productUpgrade,
		staleTime: 30_000,
		refetchInterval: (query) => query.state.data && query.state.data.state !== "completed" ? 2_000 : false,
	});
	const legacyReadOnly = Boolean(productUpgrade.data?.legacy_read_only);

  useEffect(() => {
    const onPopState = () => setRoute(readRoute());
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  useEffect(() => {
    if (window.location.pathname === "/runs" || window.location.pathname.startsWith("/runs/")) {
      window.history.replaceState({}, "", routePath(route));
    }
  }, [route]);

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
            className={`nav-item ${route.page === "work" || route.page === "run" || route.page === "task" ? "active" : ""}`}
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
			<Bot size={17} /> Workers
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
			{route.page === "run" && "Work detail"}
			{route.page === "workers" && "Workers"}
			{route.page === "task" && "Work detail"}
			{route.page === "worker" && "Worker detail"}
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
			{!legacyReadOnly && <button className="button button-secondary" onClick={() => openDelegate()}>
				<Plus size={16} /> Delegate task
			</button>}
            {route.page !== "work" && <button className="button button-primary" onClick={() => navigate({ page: "work", create: true })}>
              <Play size={16} /> Start work
            </button>}
          </div>
        </header>

        <main>
			{route.page === "overview" && <Overview
				onRun={(id, jobID) => navigate({ page: "run", id, jobID })}
				upgrade={productUpgrade.data}
				upgradeError={productUpgrade.error}
			/>}
          {route.page === "work" && (
            <WorkView
              view={route.view ?? "table"}
              createOpen={Boolean(route.create)}
              onViewChange={(view) => navigate({ page: "work", view })}
              onCreateOpenChange={(create) => navigate({ page: "work", view: route.view, create })}
              onRun={(id) => navigate({ page: "run", id, view: route.view })}
              onTask={(id) => navigate({ page: "task", id, view: route.view })}
              workers={workers.data ?? []}
            />
          )}
          {route.page === "run" && <RunDetail id={route.id} initialJobID={route.jobID} onBack={() => navigate({ page: "work", view: route.view })} />}
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
              legacyReadOnly={legacyReadOnly}
              onBack={() => navigate({ page: "work", view: route.view })}
              onDeleted={() => {
                queryClient.setQueryData<string[]>(deletedWorkTaskIDsKey, (current = []) =>
                  current.includes(route.id) ? current : [...current, route.id]
                );
                queryClient.setQueryData<TaskPage>(["tasks", "head"], (current) => current ? {
                  ...current,
                  tasks: current.tasks.filter((task) => task.id !== route.id),
                } : current);
                queryClient.setQueryData<{ items: Task[]; cursor?: string | null; headCursor?: string | null }>(
                  ["work-history", "tasks"],
                  (current) => current ? {
                    ...current,
                    items: current.items.filter((task) => task.id !== route.id),
                  } : current,
                );
                navigate({ page: "work", view: route.view });
              }}
            />
          )}
          {route.page === "worker" && (
            <WorkerDetail
              id={route.id}
              legacyReadOnly={legacyReadOnly}
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
            <WorkflowsView legacyReadOnly={legacyReadOnly} onWorkflow={(id) => navigate({ page: "workflow", id })} />
          )}
          {route.page === "workflow" && (
            <WorkflowDetail id={route.id} legacyReadOnly={legacyReadOnly} onBack={() => navigate({ page: "workflows" })} />
          )}
          {route.page === "automations" && (
            <AutomationsView legacyReadOnly={legacyReadOnly} onAutomation={(id) => navigate({ page: "automation", id })} />
          )}
          {route.page === "automation" && (
            <AutomationDetail
              id={route.id}
              legacyReadOnly={legacyReadOnly}
              onBack={() => navigate({ page: "automations" })}
              onTask={(taskID) => navigate({ page: "task", id: taskID })}
              onRun={(runID) => navigate({ page: "run", id: runID })}
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
      {delegateRequest && !legacyReadOnly && (
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
