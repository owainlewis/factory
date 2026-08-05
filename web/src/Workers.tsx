import {
  ArrowLeft,
  Bot,
  Check,
  ChevronRight,
  Copy,
  GitBranch,
  HardDrive,
  LoaderCircle,
  Play,
  Plus,
  Server,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useId, useState, type KeyboardEvent, type ReactNode } from "react";
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

const workerTabs = ["overview", "work", "capabilities", "settings"] as const;
type WorkerTab = (typeof workerTabs)[number];

const workerTabLabel: Record<WorkerTab, string> = {
  overview: "Overview",
  work: "Work",
  capabilities: "Capabilities",
  settings: "Settings",
};

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
  const registered = workers ?? [];
  const online = registered.filter((worker) => worker.online).length;
  const availableSlots = registered.reduce(
    (total, worker) =>
      worker.online && worker.health === "healthy"
        ? total + Math.max(worker.capacity - worker.active_count, 0)
        : total,
    0,
  );

  return (
    <div className="page">
      <ViewHeader
        title="Execution capacity"
        fetching={fetching}
        updatedAt={updatedAt}
        onRefresh={onRefresh}
      />
      {error && <StaleBanner error={error} />}
      {registered.length === 0 ? (
        <EmptyState
          icon={<Server size={22} />}
          title="No workers registered"
          description="Start a Factory worker and its registration will appear here automatically."
        />
      ) : (
        <>
          <div className="fleet-summary" aria-label="Fleet summary">
            <div><span>Registered</span><strong>{registered.length}</strong></div>
            <div><span>Online</span><strong>{online}</strong></div>
            <div><span>Available slots</span><strong>{availableSlots}</strong></div>
          </div>
          <div className="workers-list">
            <div className="worker-table-head" aria-hidden="true">
              <span>Worker</span><span>Capacity</span><span>Repositories</span><span>Versions</span><span>Last seen</span><span />
            </div>
            {registered.map((worker) => (
              <button className="worker-row" key={worker.id} onClick={() => onWorker(worker.id)}>
                <span className="worker-identity">
                  <span className="worker-avatar"><Bot size={17} /></span>
                  <span>
                    <span className="worker-name-line">
                      <strong>{worker.name}</strong>
                      <span className={`presence ${worker.online ? "online" : "offline"}`} aria-hidden="true" />
                    </span>
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
        </>
      )}
    </div>
  );
}

export function WorkerDetail({
  id,
  onBack,
  onDelegate,
}: {
  id: string;
  onBack: () => void;
  onDelegate: () => void;
}) {
  const interval = useVisibleInterval(10_000);
  const worker = useQuery({
    queryKey: ["worker", id],
    queryFn: () => api.worker(id),
    refetchInterval: interval,
  });
  const [copied, setCopied] = useState<string>();
  const [activeTab, setActiveTab] = useState<WorkerTab>("overview");
  const tabIDPrefix = useId();

  if (worker.isPending) return <LoadingState label="Loading worker" />;
  if (!worker.data) return <ErrorState error={worker.error} onRetry={() => void worker.refetch()} />;

  const data = worker.data;
  const tabID = (tab: WorkerTab) => `${tabIDPrefix}-tab-${tab}`;
  const tabPanelID = (tab: WorkerTab) => `${tabIDPrefix}-panel-${tab}`;
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
  const selectTabFromKeyboard = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    let nextIndex: number | undefined;
    if (event.key === "ArrowRight") nextIndex = (index + 1) % workerTabs.length;
    if (event.key === "ArrowLeft") nextIndex = (index - 1 + workerTabs.length) % workerTabs.length;
    if (event.key === "Home") nextIndex = 0;
    if (event.key === "End") nextIndex = workerTabs.length - 1;
    if (nextIndex === undefined) return;
    event.preventDefault();
    const nextTab = workerTabs[nextIndex];
    setActiveTab(nextTab);
    document.getElementById(tabID(nextTab))?.focus();
  };
  const tabPanel = (tab: WorkerTab, content: ReactNode) => (
    <div
      className="worker-tab-panel"
      role="tabpanel"
      id={tabPanelID(tab)}
      aria-labelledby={tabID(tab)}
      hidden={activeTab !== tab}
      tabIndex={0}
    >
      {content}
    </div>
  );
  const activeSessions = `${data.active_count} active session${data.active_count === 1 ? "" : "s"}`;
  const latestActiveTask = data.active_count > 1 ? `Latest of ${activeSessions}` : activeSessions;

  return (
    <div className="page detail-page">
      <button className="back-button" onClick={onBack}><ArrowLeft size={16} /> All workers</button>
      <div className="detail-heading worker-detail-heading">
        <div className="worker-detail-identity">
          <span className="worker-avatar worker-avatar-large"><Bot size={25} /></span>
          <div>
            <div className="worker-state-line">
              <span className={`presence ${data.online ? "online" : "offline"}`} aria-hidden="true" />
              <span>{data.online ? "Online" : "Offline"}</span>
              <span>·</span>
              <span className={data.health === "healthy" ? "healthy-text" : "danger-text"}>{stateLabel(data.health)}</span>
            </div>
            <h1>{data.name}</h1>
            <div className="worker-profile-meta">
              <span className={`runtime-badge runtime-${data.runtime}`}>
                <Play size={10} /> {runtimeLabel(data.runtime)}
              </span>
              <span>{data.active_count} / {data.capacity} sessions active</span>
              <span>Last seen {timeAgo(data.last_heartbeat)}</span>
            </div>
          </div>
        </div>
        <div className="detail-actions">
          <button className="button button-primary" onClick={onDelegate}>
            <Plus size={15} /> Assign work
          </button>
        </div>
      </div>
      {worker.error && <StaleBanner error={worker.error} />}

      <div className="worker-tabs" role="tablist" aria-label="Worker profile">
        {workerTabs.map((tab, index) => (
          <button
            type="button"
            role="tab"
            id={tabID(tab)}
            aria-controls={tabPanelID(tab)}
            aria-selected={activeTab === tab}
            tabIndex={activeTab === tab ? 0 : -1}
            key={tab}
            onClick={() => setActiveTab(tab)}
            onKeyDown={(event) => selectTabFromKeyboard(event, index)}
          >
            {workerTabLabel[tab]}
          </button>
        ))}
      </div>

      {tabPanel("overview",
        <>
          <section className="worker-summary-grid" aria-label="Worker summary">
            <div><span>Status</span><strong>{data.online ? "Online" : "Offline"}</strong><small>{stateLabel(data.health)}</small></div>
            <div><span>Sessions</span><strong>{data.active_count} / {data.capacity}</strong><small>active capacity</small></div>
            <div><span>Repositories</span><strong>{data.repositories.length}</strong><small>advertised</small></div>
            <div><span>Worktrees</span><strong>{data.retained_worktrees?.length ?? 0}</strong><small>retained</small></div>
          </section>
          <div className="worker-overview-layout">
            <section className="panel">
              <PanelHeading title="Profile" />
              <dl className="metadata">
                <div><dt>Runtime</dt><dd>{runtimeLabel(data.runtime)}</dd></div>
                <div><dt>Last seen</dt><dd>{timeAgo(data.last_heartbeat)}</dd></div>
                <div><dt>Registered</dt><dd>{new Date(data.registered_at).toLocaleString()}</dd></div>
                <div><dt>Worker ID</dt><dd><span className="worker-id" title={data.id}>{data.id}</span></dd></div>
              </dl>
            </section>
            <section className="panel">
              <PanelHeading title="Latest active task" aside={latestActiveTask} />
              {data.current_task_title ? (
                <div className="current-work"><LoaderCircle size={17} className="spin" /> {data.current_task_title}</div>
              ) : data.active_count > 0 ? (
                <div className="quiet-empty">No active task title is currently reported.</div>
              ) : (
                <div className="quiet-empty">This worker is ready for its next task.</div>
              )}
            </section>
          </div>
        </>,
      )}

      {tabPanel("work",
        <>
          <section className="panel">
            <PanelHeading title="Latest active task" aside={latestActiveTask} />
            {data.current_task_title ? (
              <div className="current-work"><LoaderCircle size={17} className="spin" /> {data.current_task_title}</div>
            ) : data.active_count > 0 ? (
              <div className="quiet-empty">No active task title is currently reported.</div>
            ) : (
              <div className="quiet-empty">This worker is ready for its next task.</div>
            )}
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
        </>,
      )}

      {tabPanel("capabilities",
        <>
          <div className="worker-capabilities-layout">
            <section className="panel">
              <PanelHeading title="Runtime capability" />
              <dl className="metadata">
                <div><dt>Runtime</dt><dd>{runtimeLabel(data.runtime)}</dd></div>
                <div><dt>Runtime version</dt><dd>{data.runtime_version || "Unknown"}</dd></div>
                <div><dt>Worker version</dt><dd>{data.worker_version || "Unknown"}</dd></div>
                <div><dt>Managed repositories</dt><dd>{data.accepts_managed_repositories ? "Accepted" : "Not advertised"}</dd></div>
                <div><dt>Repository cache</dt><dd>{data.repository_cache_count ?? 0} cached</dd></div>
              </dl>
            </section>
            <section className="panel">
              <PanelHeading title="Source access" aside={`${data.source_access?.length ?? 0} advertised`} />
              {(data.source_access ?? []).length === 0 ? (
                <div className="quiet-empty">No source providers are advertised by this worker.</div>
              ) : (
                <div className="capability-list">
                  {(data.source_access ?? []).map((source) => (
                    <div key={`${source.provider}-${source.hostname}`}>
                      <strong>{source.provider}</strong>
                      <span>{source.hostname}</span>
                    </div>
                  ))}
                </div>
              )}
            </section>
          </div>
          <section className="panel">
            <PanelHeading title="Repositories" aside={`${data.repositories.length} advertised`} />
            {data.repositories.length === 0 ? (
              <div className="quiet-empty">No legacy repository checkouts are advertised.</div>
            ) : (
              <div className="repository-rows">
                {data.repositories.map((repo) => (
                  <div className="repository-row" key={repo.id}>
                    <GitBranch size={17} />
                    <span><strong>{repo.key}</strong><small>{repo.remote_identity}</small></span>
                    <span className="retained-count">{repo.retained_count} retained</span>
                  </div>
                ))}
              </div>
            )}
          </section>
        </>,
      )}

      {tabPanel("settings",
        <>
          <section className="panel execution-settings-card">
            <PanelHeading title="Execution" aside="Read only" />
            <div className="execution-settings-grid">
              <div className="execution-setting">
                <span>Runtime</span>
                <strong>{runtimeLabel(data.runtime)}</strong>
                <small>One runtime type per worker identity.</small>
              </div>
              <div className="execution-setting execution-concurrency">
                <span>Concurrency</span>
                <strong>{data.active_count} / {data.capacity}</strong>
                <small>sessions active</small>
                <meter
                  min={0}
                  max={data.capacity}
                  value={Math.min(data.active_count, data.capacity)}
                  aria-label="Worker concurrency"
                />
              </div>
            </div>
            <div className="execution-owner-note">
              <strong>Managed by worker configuration</strong>
              <p>
                Factory reads <code>runtime</code> and <code>max_concurrent</code> from the worker TOML at startup.
                Update the file and restart the worker to apply concurrency changes. Runtime is immutable for an
                existing worker identity; use a separate config and data directory for another runtime.
              </p>
            </div>
          </section>
          <section className="panel">
            <PanelHeading title="Runtime ownership" />
            <p className="settings-copy">
              Model, reasoning effort, speed, and custom runtime arguments stay with the installed Codex or Claude
              Code configuration. Factory reports execution capability but does not edit provider-owned settings.
            </p>
          </section>
        </>,
      )}
    </div>
  );
}
