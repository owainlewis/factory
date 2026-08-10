import {
  AlertCircle,
  ArrowLeft,
  ChevronRight,
  Clock3,
  Columns3,
  LayoutList,
  LoaderCircle,
  Play,
  RefreshCw,
  Square,
  Table2,
  X,
} from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { api } from "./api";
import { invalidateControlPlane } from "./controlPlaneQueries";
import { duration, eventSummary, runtimeLabel, stateLabel, timeAgo } from "./format";
import { useVisibleInterval } from "./polling";
import type { AttemptEvent, Run, RunDetail as RunDetailType, RunState, Task, TaskState, Worker } from "./types";
import { deletedWorkTaskIDsKey } from "./workQueries";
import {
  EmptyState,
  ErrorState,
  InlineError,
  LoadingState,
  PanelHeading,
  StaleBanner,
  StatusBadge,
  ViewHeader,
} from "./ui";

export type WorkViewMode = "table" | "list" | "kanban";

const workStates: RunState[] = ["blocked", "queued", "running", "succeeded", "failed", "cancelled"];
type WorkState = RunState | TaskState;

interface WorkItem {
  id: string;
  kind: "run" | "task";
  title: string;
  description: string;
  repository: string;
  state: WorkState;
  startedAt: string;
  meta: string;
}

interface WorkHistory<T> {
  items: T[];
  cursor?: string | null;
  headCursor?: string | null;
}

const runHistoryKey = ["work-history", "runs"] as const;
const taskHistoryKey = ["work-history", "tasks"] as const;

export function WorkView({
  view,
  createOpen,
  onViewChange,
  onCreateOpenChange,
  onRun,
  onTask,
  workers,
}: {
  view: WorkViewMode;
  createOpen: boolean;
  onViewChange: (view: WorkViewMode) => void;
  onCreateOpenChange: (open: boolean) => void;
  onRun: (id: string) => void;
  onTask: (id: string) => void;
  workers: Worker[];
}) {
  const interval = useVisibleInterval(2_000);
  const queryClient = useQueryClient();
  const cachedRunHistory = queryClient.getQueryData<WorkHistory<Run>>(runHistoryKey);
  const cachedTaskHistory = queryClient.getQueryData<WorkHistory<Task>>(taskHistoryKey);
  const [runHistory, setRunHistory] = useState<Run[]>(cachedRunHistory?.items ?? []);
  const [taskHistory, setTaskHistory] = useState<Task[]>(cachedTaskHistory?.items ?? []);
  const [runHistoryCursor, setRunHistoryCursor] = useState<string | null | undefined>(cachedRunHistory?.cursor);
  const [taskHistoryCursor, setTaskHistoryCursor] = useState<string | null | undefined>(cachedTaskHistory?.cursor);
  const previousRunHeadCursor = useRef<string | null | undefined>(cachedRunHistory?.headCursor);
  const previousTaskHeadCursor = useRef<string | null | undefined>(cachedTaskHistory?.headCursor);
  const runs = useQuery({
    queryKey: ["runs", "head"],
    queryFn: () => api.runs(),
    refetchInterval: interval,
  });
  const tasks = useQuery({
    queryKey: ["tasks", "head"],
    queryFn: async () => filterDeletedTasks(
      await api.tasks(),
      queryClient.getQueryData<string[]>(deletedWorkTaskIDsKey),
    ),
    refetchInterval: interval,
  });
  const loadRunHistory = useMutation({
    mutationFn: ({ cursor }: { cursor: string; headCursor: string | null }) => api.runs(cursor),
    onSuccess: (page, request) => {
      setRunHistory((current) => mergeByID(page.runs, current));
      if (previousRunHeadCursor.current === request.headCursor) setRunHistoryCursor(page.next_cursor);
    },
  });
  const loadTaskHistory = useMutation({
    mutationFn: async ({ cursor }: { cursor: string; headCursor: string | null }) => filterDeletedTasks(
      await api.tasks(cursor),
      queryClient.getQueryData<string[]>(deletedWorkTaskIDsKey),
    ),
    onSuccess: (page, request) => {
      setTaskHistory((current) => mergeByID(page.tasks, current));
      if (previousTaskHeadCursor.current === request.headCursor) setTaskHistoryCursor(page.next_cursor);
    },
  });
  useEffect(() => {
    if (!runs.data) return;
    if (previousRunHeadCursor.current !== runs.data.next_cursor) setRunHistoryCursor(runs.data.next_cursor);
    previousRunHeadCursor.current = runs.data.next_cursor;
  }, [runs.data]);
  useEffect(() => {
    if (!tasks.data) return;
    if (previousTaskHeadCursor.current !== tasks.data.next_cursor) setTaskHistoryCursor(tasks.data.next_cursor);
    previousTaskHeadCursor.current = tasks.data.next_cursor;
  }, [tasks.data]);
  useEffect(() => {
    queryClient.setQueryData<WorkHistory<Run>>(runHistoryKey, {
      items: runHistory,
      cursor: runHistoryCursor,
      headCursor: previousRunHeadCursor.current,
    });
  }, [queryClient, runHistory, runHistoryCursor, runs.data]);
  useEffect(() => {
    queryClient.setQueryData<WorkHistory<Task>>(taskHistoryKey, {
      items: taskHistory,
      cursor: taskHistoryCursor,
      headCursor: previousTaskHeadCursor.current,
    });
  }, [queryClient, taskHistory, taskHistoryCursor, tasks.data]);
  if (runs.isPending || tasks.isPending) return <LoadingState label="Loading work" />;
  if (!runs.data && !tasks.data) return <ErrorState error={runs.error ?? tasks.error} onRetry={() => { void runs.refetch(); void tasks.refetch(); }} />;
  const runItems = mergeByID(runs.data?.runs ?? [], runHistory).map(workItemFromRun);
  const workerMap = new Map(workers.map((worker) => [worker.id, worker]));
  const taskItems = mergeByID(tasks.data?.tasks ?? [], taskHistory).map((task) => workItemFromTask(task, workerMap.get(task.worker_id)));
  const items = [...runItems, ...taskItems].sort((left, right) => right.startedAt.localeCompare(left.startedAt));
  const openWork = (item: WorkItem) => item.kind === "run" ? onRun(item.id) : onTask(item.id);
  const error = runs.error ?? tasks.error ?? loadRunHistory.error ?? loadTaskHistory.error;
  const fetching = runs.isFetching || tasks.isFetching;
  const updatedAt = Math.max(runs.dataUpdatedAt, tasks.dataUpdatedAt);
  return (
    <div className="page page-work">
      <ViewHeader title="Work" fetching={fetching} updatedAt={updatedAt} onRefresh={() => { void runs.refetch(); void tasks.refetch(); }} />
      {error && <StaleBanner error={error} />}
      <div className="view-toolbar">
        <p>Track every unit of work from admission through completion.</p>
        <div className="work-toolbar-actions">
          <div className="work-view-switcher" role="group" aria-label="Work view">
            <button aria-pressed={view === "table"} onClick={() => onViewChange("table")}><Table2 size={14} /> Table</button>
            <button aria-pressed={view === "list"} onClick={() => onViewChange("list")}><LayoutList size={14} /> List</button>
            <button aria-pressed={view === "kanban"} onClick={() => onViewChange("kanban")}><Columns3 size={14} /> Kanban</button>
          </div>
          <button className="button button-primary" onClick={() => onCreateOpenChange(true)}>
            <Play size={15} /> Start work
          </button>
        </div>
      </div>
      {items.length === 0 ? (
        <EmptyState
          icon={<Play size={22} />}
          title="No work yet"
          description="Choose a Definition and repositories to start independently tracked agent Jobs."
          action={<button className="button button-primary" onClick={() => onCreateOpenChange(true)}>Start work</button>}
        />
      ) : view === "table" ? (
        <div className="workflow-list" data-testid="work-table">
          <div className="run-table-head"><span>Work</span><span>Repository</span><span>State</span><span>Started</span><span /></div>
          {items.map((item) => (
            <button className="run-row" key={`${item.kind}-${item.id}`} onClick={() => openWork(item)}>
              <span className="workflow-identity"><strong>{item.title}</strong><small>{item.meta}</small></span>
              <span className="mono muted break-anywhere">{item.repository}</span>
              <StatusBadge state={item.state} />
              <span className="mono muted">{timeAgo(item.startedAt)}</span>
              <ChevronRight size={15} className="row-chevron" />
            </button>
          ))}
        </div>
      ) : view === "list" ? (
        <div className="work-list" data-testid="work-list">
          {items.map((item) => <WorkListItem key={`${item.kind}-${item.id}`} item={item} onClick={() => openWork(item)} />)}
        </div>
      ) : (
        <div className="work-board work-run-board" data-testid="work-kanban">
          {workStates.map((state) => {
            const grouped = items.filter((item) => item.state === state);
            return <section className="work-column" key={state} aria-labelledby={`heading-${state}`}>
              <div className="column-heading">
                <span className={`status-dot status-${state}`} aria-hidden="true" />
                <h2 id={`heading-${state}`}>{stateLabel(state)}</h2>
                <span className="count">{grouped.length}</span>
              </div>
              <div className="task-stack">
                {grouped.length === 0
                  ? <div className="column-empty">No work</div>
                  : grouped.map((item) => <WorkCard key={`${item.kind}-${item.id}`} item={item} onClick={() => openWork(item)} />)}
              </div>
            </section>;
          })}
        </div>
      )}
      {(runHistoryCursor || taskHistoryCursor) && (
        <div className="load-more"><button
          className="button button-secondary"
          onClick={() => {
            if (runHistoryCursor) loadRunHistory.mutate({ cursor: runHistoryCursor, headCursor: previousRunHeadCursor.current ?? null });
            if (taskHistoryCursor) loadTaskHistory.mutate({ cursor: taskHistoryCursor, headCursor: previousTaskHeadCursor.current ?? null });
          }}
          disabled={loadRunHistory.isPending || loadTaskHistory.isPending}
        >
          {loadRunHistory.isPending || loadTaskHistory.isPending ? "Loading…" : "Load older work"}
        </button></div>
      )}
      {createOpen && <RunOnceDialog onClose={() => onCreateOpenChange(false)} onCreated={(run) => onRun(run.run.id)} />}
    </div>
  );
}

function WorkListItem({ item, onClick }: { item: WorkItem; onClick: () => void }) {
  return <button className="work-list-item" onClick={onClick}>
    <span className="work-list-main">
      <span><strong>{item.title}</strong><StatusBadge state={item.state} /></span>
      <small className="break-anywhere">{item.repository}</small>
    </span>
    <span className="work-list-meta">{item.meta} · {timeAgo(item.startedAt)}</span>
    <ChevronRight size={15} className="row-chevron" />
  </button>;
}

function WorkCard({ item, onClick }: { item: WorkItem; onClick: () => void }) {
  return <button className="task-card work-card" onClick={onClick}>
    <div className="task-card-top"><StatusBadge state={item.state} /><ChevronRight size={14} aria-hidden="true" /></div>
    <span className="task-title">{item.title}</span>
    <span className="task-description">{item.description || item.repository}</span>
    <span className="task-meta">{item.meta}<span aria-hidden="true">·</span>{timeAgo(item.startedAt)}</span>
  </button>;
}

function workItemFromRun(run: Run): WorkItem {
  return {
    id: run.id,
    kind: "run",
    title: run.definition.name,
    description: run.repository_remote_identities.join(", "),
    repository: run.repository_remote_identities.join(", "),
    state: run.state,
    startedAt: run.admitted_at,
    meta: `${run.job_count} ${run.job_count === 1 ? "Job" : "Jobs"}`,
  };
}

function filterDeletedTasks<T extends { tasks: Task[] }>(page: T, deletedIDs: string[] = []): T {
  if (deletedIDs.length === 0) return page;
  const deleted = new Set(deletedIDs);
  return { ...page, tasks: page.tasks.filter((task) => !deleted.has(task.id)) };
}

function workItemFromTask(task: Task, worker?: Worker): WorkItem {
  const repository = worker?.repositories.find((item) => item.id === task.repository_id)?.remote_identity ?? task.repository_id;
  return {
    id: task.id,
    kind: "task",
    title: task.title,
    description: task.description ?? repository,
    repository,
    state: task.state,
    startedAt: task.created_at,
    meta: worker?.name ?? runtimeLabel(task.required_runtime),
  };
}

function mergeByID<T extends { id: string }>(...groups: T[][]): T[] {
  const unique = new Map<string, T>();
  for (const group of groups) {
    for (const item of group) if (!unique.has(item.id)) unique.set(item.id, item);
  }
  return [...unique.values()];
}

function RunOnceDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (run: RunDetailType) => void }) {
  const titleID = useId();
  const definitionID = useId();
  const repositoryID = useId();
  const concurrencyID = useId();
  const firstField = useRef<HTMLSelectElement>(null);
  const definitions = useQuery({ queryKey: ["definitions", "active", "run-once"], queryFn: loadActiveDefinitions });
  const repositories = useQuery({ queryKey: ["run-repositories"], queryFn: api.runRepositories });
  const [definition, setDefinition] = useState("");
  const [selectedRepositories, setSelectedRepositories] = useState<string[]>([]);
  const [concurrencyLimit, setConcurrencyLimit] = useState(3);
  const [parameters, setParameters] = useState<Record<string, string>>({});
  const requestKey = useRef({ selection: "", value: "" });
  useEffect(() => firstField.current?.focus(), []);
  const selectedDefinition = definitions.data?.definitions.find((item) => item.id === definition);
  const create = useMutation({
    mutationFn: () => {
      const selection = JSON.stringify({ definition, selectedRepositories, concurrencyLimit, parameters });
      if (requestKey.current.selection !== selection) {
        requestKey.current = { selection, value: crypto.randomUUID() };
      }
      return api.createRun({
        request_key: requestKey.current.value,
        definition_id: definition,
        repository_ids: selectedRepositories,
        concurrency_limit: concurrencyLimit,
        parameters,
      });
    },
    onSuccess: onCreated,
  });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (definition && selectedRepositories.length > 0) create.mutate();
  };
  const availableRepositories = repositories.data ?? [];
  const selectedRepositoryItems = availableRepositories.filter((item) => selectedRepositories.includes(item.id));
  return (
    <div className="modal-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby={titleID}>
        <div className="modal-header">
          <div><h2 id={titleID}>Start work</h2><p>Start one independently tracked agent Job per repository.</p></div>
          <button className="icon-button" aria-label="Close" onClick={onClose}><X size={18} /></button>
        </div>
        <form onSubmit={submit}>
          <div className="form-grid">
            <div className="field">
              <label htmlFor={definitionID}>Definition</label>
              <select
                id={definitionID}
                ref={firstField}
                value={definition}
                onChange={(event) => {
                  const value = event.target.value;
                  setDefinition(value);
                  const inputs = definitions.data?.definitions.find((item) => item.id === value)?.inputs ?? {};
                  setParameters({ ...inputs });
                }}
                required
              >
                <option value="">Choose a Definition</option>
                {(definitions.data?.definitions ?? []).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
              </select>
            </div>
            <div className="field">
              <label htmlFor={repositoryID}>Repositories</label>
              <select
                id={repositoryID}
                multiple
                size={Math.min(7, Math.max(3, availableRepositories.length))}
                value={selectedRepositories}
                onChange={(event) => setSelectedRepositories(Array.from(event.target.selectedOptions, (option) => option.value))}
                required
              >
                {availableRepositories.map((item) => <option key={item.id} value={item.id}>{item.remote_identity}</option>)}
              </select>
              <small className="form-help">Select one or more repositories. Each becomes a separate Job.</small>
            </div>
            <div className="field">
              <label htmlFor={concurrencyID}>Work concurrency</label>
              <input
                id={concurrencyID}
                type="number"
                min={1}
                max={100}
                value={concurrencyLimit}
                onChange={(event) => setConcurrencyLimit(Number(event.target.value))}
                required
              />
              <small className="form-help">Maximum Jobs from this work that Factory can keep active.</small>
            </div>
          </div>
          {selectedDefinition && Object.keys(selectedDefinition.inputs).length > 0 && (
            <div className="form-section">
              <PanelHeading title="Inputs" />
              <p className="form-help">Override this Definition&apos;s defaults for this work.</p>
              <div className="form-grid">
                {Object.keys(selectedDefinition.inputs).sort().map((name) => (
                  <div className="field" key={name}>
                    <label htmlFor={`${definitionID}-input-${name}`}>{name}</label>
                    <input
                      id={`${definitionID}-input-${name}`}
                      value={parameters[name] ?? selectedDefinition.inputs[name]}
                      onChange={(event) => setParameters((current) => ({ ...current, [name]: event.target.value }))}
                    />
                  </div>
                ))}
              </div>
            </div>
          )}
          {(selectedDefinition || selectedRepositoryItems.length > 0) && <section className="run-preview" aria-label="Work preview">
            <strong>Preview</strong>
            <p>{selectedDefinition ? selectedDefinition.name : "Choose a Definition"}</p>
            {selectedDefinition && <small>{selectedDefinition.prompt}</small>}
            <ul>{selectedRepositoryItems.map((item) => <li key={item.id} className="mono">{item.remote_identity}</li>)}</ul>
            <small>{selectedRepositoryItems.length} {selectedRepositoryItems.length === 1 ? "Job" : "Jobs"} · concurrency {concurrencyLimit || 0}</small>
          </section>}
          {(definitions.error || repositories.error || create.error) && <InlineError error={definitions.error ?? repositories.error ?? create.error} />}
          {!definitions.isPending && definitions.data?.definitions.length === 0 && <p className="form-help">Create an active Definition before starting work.</p>}
          {!repositories.isPending && availableRepositories.length === 0 && <p className="form-help">Configure a repository on a Worker or enable managed acquisition before starting work.</p>}
          <div className="modal-actions">
            <button type="button" className="button button-secondary" onClick={onClose}>Cancel</button>
            <button className="button button-primary" disabled={create.isPending || !definition || selectedRepositories.length === 0 || concurrencyLimit < 1 || concurrencyLimit > 100}>
              {create.isPending ? <LoaderCircle size={15} className="spin" /> : <Play size={15} />}
              {create.isPending ? "Starting…" : "Start work"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

export function RunDetail({ id, initialJobID = "", onBack }: { id: string; initialJobID?: string; onBack: () => void }) {
  const interval = useVisibleInterval(2_000);
  const queryClient = useQueryClient();
  const [selectedJobID, setSelectedJobID] = useState(initialJobID);
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [confirmRetry, setConfirmRetry] = useState(false);
  const [terminalCatchupAttemptID, setTerminalCatchupAttemptID] = useState<string | null>(null);
  const detail = useQuery({
    queryKey: ["run", id],
    queryFn: () => api.run(id),
    refetchInterval: (query) => ["blocked", "queued", "running"].includes(query.state.data?.run.state ?? "") ? interval : false,
  });
  const job = detail.data?.jobs.find((item) => item.job.id === selectedJobID) ?? detail.data?.jobs[0];
  const latestAttempt = job?.attempts?.at(-1);
  const jobIsActive = Boolean(job && ["preparing", "running"].includes(job.job.state));
  const eventKey = ["events", latestAttempt?.id] as const;
  const events = useQuery({
    queryKey: eventKey,
    queryFn: async () => {
      const cached = queryClient.getQueryData<AttemptEvent[]>(eventKey) ?? [];
      const appended = await loadJobEvents(latestAttempt!.id, cached.at(-1)?.sequence ?? -1);
      const unique = new Map(cached.map((event) => [event.sequence, event]));
      for (const event of appended) unique.set(event.sequence, event);
      return [...unique.values()].sort((left, right) => left.sequence - right.sequence);
    },
    enabled: Boolean(latestAttempt),
    refetchInterval: jobIsActive ? interval : false,
  });
  const previousJob = useRef<{ id: string; active: boolean } | undefined>(undefined);
  useEffect(() => {
    if (!job) return;
    let stateTimer: number | undefined;
    if (previousJob.current?.id === job.job.id && previousJob.current.active && !jobIsActive && latestAttempt) {
      stateTimer = window.setTimeout(() => setTerminalCatchupAttemptID(latestAttempt.id), 0);
    } else if (jobIsActive) {
      stateTimer = window.setTimeout(() => setTerminalCatchupAttemptID(null), 0);
    }
    previousJob.current = { id: job.job.id, active: jobIsActive };
    return () => {
      if (stateTimer !== undefined) window.clearTimeout(stateTimer);
    };
  }, [job, jobIsActive, latestAttempt]);
  const refetchEvents = events.refetch;
  useEffect(() => {
    if (!terminalCatchupAttemptID || terminalCatchupAttemptID !== latestAttempt?.id) return;
    let cancelled = false;
    let retryTimer: number | undefined;
    const catchUp = async () => {
      const result = await refetchEvents();
      if (cancelled) return;
      if (result.isSuccess) {
        setTerminalCatchupAttemptID((current) => current === terminalCatchupAttemptID ? null : current);
      } else if (interval !== false) {
        retryTimer = window.setTimeout(() => void catchUp(), interval);
      }
    };
    void catchUp();
    return () => {
      cancelled = true;
      if (retryTimer !== undefined) window.clearTimeout(retryTimer);
    };
  }, [interval, latestAttempt?.id, refetchEvents, terminalCatchupAttemptID]);
  const cancel = useMutation({
    mutationFn: () => api.cancelJob(job!.job.id),
    onSuccess: async (next) => {
      queryClient.setQueryData(["run", id], next);
      setConfirmCancel(false);
      await invalidateControlPlane(queryClient);
    },
  });
  const retry = useMutation({
    mutationFn: () => api.retryJob(job!.job.id),
    onSuccess: async (next) => {
      queryClient.setQueryData(["run", id], next);
      setConfirmRetry(false);
      await invalidateControlPlane(queryClient);
    },
  });
  if (detail.isPending) return <LoadingState label="Loading work" />;
  if (!detail.data || !job) return <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />;
  const data = detail.data;
  const terminalJobs = data.jobs.filter((item) => ["succeeded", "failed", "cancelled"].includes(item.job.state)).length;
  const active = ["blocked", "queued", "preparing", "running"].includes(job.job.state) && !job.job.cancellation_requested;
  const progress = (events.data ?? []).flatMap((event) => {
    const summary = eventSummary(event);
    return summary ? [{ event, summary }] : [];
  });
  return (
    <div className="page detail-page">
      <button className="back-button" onClick={onBack}><ArrowLeft size={16} /> All work</button>
      <div className="detail-heading">
        <div><StatusBadge state={data.run.state} /><h1>{data.run.definition.name}</h1><p>{terminalJobs} of {data.run.job_count} Jobs complete · concurrency {data.run.concurrency_limit} · admitted {new Date(data.run.admitted_at).toLocaleString()}</p></div>
        <div className="detail-actions">
          {active && !confirmCancel && <button className="button button-danger-secondary" onClick={() => setConfirmCancel(true)}><Square size={14} /> Cancel Job</button>}
          {job.job.state === "failed" && !confirmRetry && <button className="button button-primary" onClick={() => setConfirmRetry(true)}><RefreshCw size={14} /> Retry Job</button>}
        </div>
      </div>
      {confirmCancel && <div className="confirm-action" role="alert"><span>Cancel this Job?</span><button className="button button-danger" onClick={() => cancel.mutate()} disabled={cancel.isPending}>{cancel.isPending ? "Cancelling…" : "Confirm cancel"}</button><button className="button button-secondary" onClick={() => setConfirmCancel(false)}>Keep running</button></div>}
      {confirmRetry && <div className="warning-banner" role="alert"><AlertCircle size={17} /><span>{job.job.retry_may_repeat_effects ? "The first agent process started and may already have changed GitHub. Retrying can repeat external effects." : "Retry this failed Job?"}</span><button className="button button-primary" onClick={() => retry.mutate()} disabled={retry.isPending}>{retry.isPending ? "Retrying…" : "Confirm retry"}</button><button className="button button-secondary" onClick={() => setConfirmRetry(false)}>Cancel</button></div>}
      {(detail.error || cancel.error || retry.error) && <InlineError error={detail.error ?? cancel.error ?? retry.error} />}
      {jobIsActive && job.job.cancellation_requested && <div className="warning-banner"><Clock3 size={17} /> Cancellation requested. The Worker will stop this Job on its next heartbeat.</div>}
      {job.job.blocked_reason && <div className="warning-banner"><Clock3 size={17} /> {job.job.blocked_reason}</div>}
      {data.run.source_kind === "webhook" && <section className="panel">
        <PanelHeading title="GitHub webhook" aside={`${data.run.event} · ${data.run.action}`} />
        <dl className="metadata">
          <div><dt>Delivery</dt><dd className="mono break-anywhere">{data.run.delivery_id}</dd></div>
          <div><dt>Pull request</dt><dd>{data.run.pull_request_url ? <a href={data.run.pull_request_url} target="_blank" rel="noreferrer">#{data.run.pull_request_number}</a> : `#${data.run.pull_request_number}`}</dd></div>
          <div><dt>Revision</dt><dd className="mono break-anywhere">{data.run.observed_head_commit}</dd></div>
        </dl>
      </section>}
      <section className="panel run-jobs-panel">
        <PanelHeading title="Repositories" aside={`${terminalJobs}/${data.run.job_count} complete`} />
        <div className="run-job-list">
          {data.jobs.map((item) => <button
            type="button"
            key={item.job.id}
            className={item.job.id === job.job.id ? "run-job-card selected" : "run-job-card"}
            aria-label={`View ${item.job.repository_remote_identity} Job`}
            aria-pressed={item.job.id === job.job.id}
            onClick={() => { setSelectedJobID(item.job.id); setConfirmCancel(false); setConfirmRetry(false); }}
          >
            <span><strong>{item.job.repository_remote_identity}</strong><small className="mono">{item.job.id}</small></span>
            <StatusBadge state={item.job.state} />
          </button>)}
        </div>
      </section>
      <div className="detail-grid">
        <section className="panel detail-main"><PanelHeading title="Definition prompt" /><div className="long-copy">{job.resolved_prompt}</div></section>
        <section className="panel"><PanelHeading title="Job" /><dl className="metadata">
          <div><dt>State</dt><dd><StatusBadge state={job.job.state} /></dd></div>
          <div><dt>Repository</dt><dd className="break-anywhere">{job.job.repository_remote_identity}</dd></div>
          <div><dt>Worker</dt><dd>{job.job.assigned_worker_id ?? "Waiting for a compatible Worker"}</dd></div>
          <div><dt>Runtime</dt><dd>{runtimeLabel(job.job.required_runtime)}</dd></div>
          <div><dt>Duration</dt><dd>{duration(job.job.admitted_at, job.job.terminal_at)}</dd></div>
          <div><dt>Job ID</dt><dd className="mono break-anywhere">{job.job.id}</dd></div>
        </dl></section>
      </div>
      {(job.job.result || job.job.failure_reason) && <section className="panel"><PanelHeading title="Result" />{job.job.result && <div className="attempt-output success-output"><strong>Agent result</strong><pre>{job.job.result}</pre></div>}{job.job.failure_reason && <div className="attempt-output error-output"><strong>Failure reason</strong><pre>{job.job.failure_reason}</pre></div>}</section>}
      <section className="panel progress-panel"><PanelHeading title="Agent output" aside={`${progress.length} updates`} />{events.error && <InlineError error={events.error} />}{latestAttempt && events.isPending ? <div className="loading-line" role="status"><LoaderCircle size={16} className="spin" />Loading output</div> : progress.length === 0 ? <div className="quiet-empty">Output will appear after the Worker starts the agent.</div> : <ol className="event-list">{progress.map(({ event, summary }) => <li key={event.sequence}><span className="event-marker" aria-hidden="true" /><div><span className="event-kind">{summary.label}</span><p>{summary.text}</p><time dateTime={event.server_time}>{new Date(event.server_time).toLocaleTimeString()}</time></div></li>)}</ol>}</section>
      {(job.attempts ?? []).length > 0 && <section className="panel attempts-panel"><PanelHeading title="Attempts" aside={`${job.attempts?.length ?? 0} total`} />{[...(job.attempts ?? [])].reverse().map((attempt) => <div className="attempt-row run-attempt" key={attempt.id}><span><strong>Attempt {attempt.attempt_number}</strong><small>{attempt.id}</small></span><StatusBadge state={attempt.state} /><span className="mono muted">{duration(attempt.started_at ?? attempt.created_at, attempt.completed_at)}</span></div>)}</section>}
    </div>
  );
}

async function loadActiveDefinitions() {
  const definitions = [];
  let cursor = "";
  do {
    const page = await api.definitions(cursor);
    definitions.push(...page.definitions);
    cursor = page.next_cursor ?? "";
  } while (cursor);
  return { definitions, next_cursor: null };
}

async function loadJobEvents(attemptID: string, initialAfter: number): Promise<AttemptEvent[]> {
  const events: AttemptEvent[] = [];
  let after = initialAfter;
  for (;;) {
    const previous = after;
    const page = await api.events(attemptID, after);
    for (const event of page.events) if (event.sequence > after) { events.push(event); after = event.sequence; }
    if (!page.has_more) return events;
    if (page.next_after <= previous) throw new Error("Event pagination did not advance.");
    after = page.next_after;
  }
}
