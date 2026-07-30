import {
  ArrowLeft,
  Check,
  ChevronRight,
  CircleDot,
  Clock3,
  Copy,
  GitBranch,
  HardDrive,
  LoaderCircle,
  Play,
  Server,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { api } from "./api";
import { runtimeLabel, stateLabel, timeAgo } from "./format";
import { useVisibleInterval } from "./polling";
import type { Worker } from "./types";
import {
  EmptyState,
  ErrorState,
  LoadingState,
  PanelHeading,
  StaleBanner,
  ViewHeader,
  type ViewStateProps,
} from "./ui";

export function WorkersView({
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
                  <span className={`runtime-badge runtime-${worker.runtime}`}>
                    <Play size={10} /> {runtimeLabel(worker.runtime)}
                  </span>
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
                <small>{runtimeLabel(worker.runtime)} {worker.runtime_version || "unknown"}</small>
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

export function WorkerDetail({ id, onBack }: { id: string; onBack: () => void }) {
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
          <span className={`runtime-badge runtime-${data.runtime}`}>
            <Play size={10} /> {runtimeLabel(data.runtime)}
          </span>
          <p>Registered {new Date(data.registered_at).toLocaleString()}</p>
        </div>
        <span className="worker-id" title={data.id}>{data.id}</span>
      </div>
      {worker.error && <StaleBanner error={worker.error} />}

      <div className="worker-summary">
        <SummaryItem label="Active capacity" value={`${data.active_count} / ${data.capacity}`} icon={<CircleDot size={17} />} />
        <SummaryItem label={`${runtimeLabel(data.runtime)} version`} value={data.runtime_version || "Unknown"} icon={<Play size={17} />} />
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

function SummaryItem({ label, value, icon }: { label: string; value: string; icon: ReactNode }) {
  return <div className="summary-item"><span className="summary-icon">{icon}</span><span><small>{label}</small><strong>{value}</strong></span></div>;
}
