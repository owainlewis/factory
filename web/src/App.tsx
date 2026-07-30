import { ListChecks, Menu, Plus, Users, X } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api } from "./api";
import { invalidateControlPlane } from "./controlPlaneQueries";
import { DelegateDrawer } from "./DelegateDrawer";
import { TaskDetail } from "./TaskDetail";
import { useVisibleInterval } from "./polling";
import { WorkersView, WorkerDetail } from "./Workers";
import { WorkView } from "./Work";

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
