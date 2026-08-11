import { Bot, Boxes, Gauge, GitBranch, ListChecks, Menu, Plus, Repeat2, X } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { api } from "./api";
import { RepositoriesView, RepositoryDetail } from "./Repositories";
import { RoutineOverview } from "./RoutineOverview";
import { RoutinesView } from "./Routines";
import { RoutineWorkDetail, RoutineWorkView, type WorkViewMode } from "./RoutineWork";
import { WorkersView, WorkerDetail } from "./Workers";
import { useVisibleInterval } from "./polling";

type Route =
  | { page: "overview" }
  | { page: "routines"; id?: string; create?: boolean }
  | { page: "work"; mode: WorkViewMode }
  | { page: "work-detail"; id: string; mode: WorkViewMode }
  | { page: "workers" }
  | { page: "worker"; id: string }
  | { page: "repositories" }
  | { page: "repository"; id: string };

function readRoute(): Route {
  const parts = window.location.pathname.split("/").filter(Boolean);
  const search = new URLSearchParams(window.location.search);
  const mode = workMode(search.get("view"));
  if (parts[0] === "routines") return { page: "routines", id: parts[1], create: search.get("new") === "true" };
  if (parts[0] === "work" && parts[1]) return { page: "work-detail", id: parts[1], mode };
  if (parts[0] === "work") return { page: "work", mode };
  if (parts[0] === "workers" && parts[1]) return { page: "worker", id: parts[1] };
  if (parts[0] === "workers") return { page: "workers" };
  if (parts[0] === "repositories" && parts[1]) return { page: "repository", id: parts[1] };
  if (parts[0] === "repositories") return { page: "repositories" };
  return { page: "overview" };
}

function workMode(value: string | null): WorkViewMode {
  return value === "list" || value === "kanban" ? value : "table";
}

function routePath(route: Route): string {
  switch (route.page) {
    case "routines": return `/routines${route.id ? `/${route.id}` : ""}${route.create ? "?new=true" : ""}`;
    case "work": return `/work${route.mode === "table" ? "" : `?view=${route.mode}`}`;
    case "work-detail": return `/work/${route.id}${route.mode === "table" ? "" : `?view=${route.mode}`}`;
    case "workers": return "/workers";
    case "worker": return `/workers/${route.id}`;
    case "repositories": return "/repositories";
    case "repository": return `/repositories/${route.id}`;
    default: return "/";
  }
}

export function App() {
  const [route, setRoute] = useState<Route>(readRoute);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const workerInterval = useVisibleInterval(10_000);
  const workers = useQuery({ queryKey: ["workers"], queryFn: api.workers, refetchInterval: workerInterval });
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
  const activeWorkMode = route.page === "work" || route.page === "work-detail" ? route.mode : "table";
  return <div className="app-shell">
    <aside className={`sidebar ${mobileNavOpen ? "sidebar-open" : ""}`}>
      <div className="brand"><div className="brand-mark" aria-hidden="true"><Boxes size={18} strokeWidth={2.2} /></div><div><span className="brand-name">Factory</span><span className="brand-subtitle">control plane</span></div></div>
      <nav aria-label="Primary navigation">
        <div className="nav-items">
          <Nav active={route.page === "overview"} icon={<Gauge size={17} />} label="Overview" onClick={() => navigate({ page: "overview" })} />
          <Nav active={route.page === "routines"} icon={<Repeat2 size={17} />} label="Routines" onClick={() => navigate({ page: "routines" })} />
          <Nav active={route.page === "work" || route.page === "work-detail"} icon={<ListChecks size={17} />} label="Work" onClick={() => navigate({ page: "work", mode: activeWorkMode })} />
        </div>
        <div className="nav-section" role="group" aria-labelledby="infrastructure-nav-label">
          <div className="nav-section-label" id="infrastructure-nav-label">Infrastructure</div>
          <div className="nav-items">
            <Nav active={route.page === "workers" || route.page === "worker"} icon={<Bot size={17} />} label="Workers" onClick={() => navigate({ page: "workers" })} />
            <Nav active={route.page === "repositories" || route.page === "repository"} icon={<GitBranch size={17} />} label="Repositories" onClick={() => navigate({ page: "repositories" })} />
          </div>
        </div>
      </nav>
      <div className="sidebar-foot"><span className="local-dot" aria-hidden="true" /> Local control plane</div>
    </aside>
    <div className="main-shell">
      <header className="topbar"><button className="icon-button mobile-menu" aria-label="Toggle navigation" aria-expanded={mobileNavOpen} onClick={() => setMobileNavOpen((open) => !open)}>{mobileNavOpen ? <X size={19} /> : <Menu size={19} />}</button><div className="topbar-title">{pageTitle(route)}</div><button className="button button-primary" onClick={() => navigate({ page: "routines", create: true })}><Plus size={15} /> New Routine</button></header>
      <main>
        {route.page === "overview" && <RoutineOverview onWork={(id) => navigate({ page: "work-detail", id, mode: "table" })} onRoutine={(id) => navigate({ page: "routines", id })} />}
        {route.page === "routines" && <RoutinesView key={`${route.id ?? "list"}:${route.create ?? false}`} initialID={route.id} createOpen={route.create} onWork={(id) => navigate({ page: "work-detail", id, mode: "table" })} />}
        {route.page === "work" && <RoutineWorkView mode={route.mode} onMode={(mode) => navigate({ page: "work", mode })} onWork={(id) => navigate({ page: "work-detail", id, mode: route.mode })} />}
        {route.page === "work-detail" && <RoutineWorkDetail id={route.id} onBack={() => navigate({ page: "work", mode: route.mode })} />}
        {route.page === "workers" && <WorkersView workers={workers.data} pending={workers.isPending} error={workers.error} fetching={workers.isFetching} updatedAt={workers.dataUpdatedAt} onWorker={(id) => navigate({ page: "worker", id })} onRefresh={() => void workers.refetch()} />}
        {route.page === "worker" && <WorkerDetail id={route.id} legacyReadOnly onBack={() => navigate({ page: "workers" })} onDelegate={() => {}} />}
        {route.page === "repositories" && <RepositoriesView onRepository={(id) => navigate({ page: "repository", id })} />}
        {route.page === "repository" && <RepositoryDetail id={route.id} onBack={() => navigate({ page: "repositories" })} />}
      </main>
    </div>
    {mobileNavOpen && <button className="nav-scrim" aria-label="Close navigation" onClick={() => setMobileNavOpen(false)} />}
  </div>;
}

function Nav({ active, icon, label, onClick }: { active: boolean; icon: ReactNode; label: string; onClick: () => void }) {
  return <button className={`nav-item ${active ? "active" : ""}`} aria-current={active ? "page" : undefined} onClick={onClick}>{icon}{label}</button>;
}

function pageTitle(route: Route): string {
  if (route.page === "work-detail") return "Work detail";
  if (route.page === "worker") return "Worker detail";
  if (route.page === "repository") return "Repository detail";
  return route.page[0].toUpperCase() + route.page.slice(1);
}
