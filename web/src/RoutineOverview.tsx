import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, Clock3, Play, Users } from "lucide-react";
import type { ReactNode } from "react";
import { api } from "./api";
import { duration, timeAgo, timeUntil } from "./format";
import { ErrorState, LoadingState, StatusBadge, ViewHeader } from "./ui";

export function RoutineOverview({ onWork, onRoutine }: { onWork: (id: string) => void; onRoutine: (id: string) => void }) {
  const query = useQuery({ queryKey: ["overview"], queryFn: api.overview, refetchInterval: 10_000 });
  if (query.isPending) return <LoadingState label="Loading overview" />;
  if (query.isError || !query.data) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  const overview = query.data;
  return <div className="page routine-overview">
    <ViewHeader title="Overview" fetching={query.isFetching} updatedAt={query.dataUpdatedAt} onRefresh={() => void query.refetch()} />
    <section className="overview-facts" aria-label="Factory status">
      <Fact icon={<Play size={15} />} label="Active work" value={overview.active_work} />
      <Fact icon={<AlertTriangle size={15} />} label="Needs attention" value={overview.needs_attention} tone={overview.needs_attention ? "danger" : undefined} />
      <Fact icon={<CheckCircle2 size={15} />} label="Completed · 24h" value={overview.completed_last_24h} />
      <Fact icon={<Users size={15} />} label="Workers online" value={`${overview.workers_online}/${overview.workers_total}`} />
    </section>
    <div className="overview-columns">
      <section className="panel clean-panel">
        <div className="panel-heading"><div><span className="eyebrow">RECENT</span><h2>Work</h2></div><span>{overview.recent_work?.length ?? 0}</span></div>
        <div className="overview-list">
          {(overview.recent_work ?? []).map((work) => <button key={work.id} className="overview-row" onClick={() => onWork(work.id)}>
            <span className="overview-row-main"><strong>{work.routine.name}</strong><small>{work.source} · {work.target_count} target{work.target_count === 1 ? "" : "s"}</small></span>
            <span className="overview-row-meta"><StatusBadge state={work.state} /><small>{work.terminal_at ? duration(work.admitted_at, work.terminal_at) : timeAgo(work.admitted_at)}</small></span>
          </button>)}
          {!overview.recent_work?.length && <p className="quiet-empty">Run a Routine to see Work here.</p>}
        </div>
      </section>
      <section className="panel clean-panel">
        <div className="panel-heading"><div><span className="eyebrow">NEXT</span><h2>Scheduled routines</h2></div><Clock3 size={15} /></div>
        <div className="overview-list">
          {(overview.upcoming_routines ?? []).map((routine) => <button key={routine.id} className="overview-row" onClick={() => onRoutine(routine.id)}>
            <span className="overview-row-main"><strong>{routine.name}</strong><small>{routine.schedule.cron} · {routine.schedule.timezone}</small></span>
            <span className="overview-row-meta"><StatusBadge state={routine.schedule.health_status} /><small>{routine.schedule.next_due_at ? timeUntil(routine.schedule.next_due_at) : "Not scheduled"}</small></span>
          </button>)}
          {!overview.upcoming_routines?.length && <p className="quiet-empty">No scheduled Routines.</p>}
        </div>
      </section>
    </div>
  </div>;
}

function Fact({ icon, label, value, tone }: { icon: ReactNode; label: string; value: string | number; tone?: string }) {
  return <div className={`overview-fact ${tone ?? ""}`}><span>{icon}{label}</span><strong>{value}</strong></div>;
}
