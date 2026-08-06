import {
  AlertCircle,
  ArrowLeft,
  ChevronRight,
  Clock3,
  LoaderCircle,
  Play,
  RefreshCw,
  Square,
  X,
} from "lucide-react";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { api } from "./api";
import { invalidateControlPlane } from "./controlPlaneQueries";
import { duration, eventSummary, runtimeLabel, timeAgo } from "./format";
import { useVisibleInterval } from "./polling";
import type { AttemptEvent, RunDetail as RunDetailType } from "./types";
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

export function RunsView({
  createOpen,
  onCreateOpenChange,
  onRun,
}: {
  createOpen: boolean;
  onCreateOpenChange: (open: boolean) => void;
  onRun: (id: string) => void;
}) {
  const interval = useVisibleInterval(2_000);
  const runs = useInfiniteQuery({
    queryKey: ["runs"],
    queryFn: ({ pageParam }) => api.runs(pageParam),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    refetchInterval: interval,
  });
  if (runs.isPending) return <LoadingState label="Loading Runs" />;
  if (!runs.data) return <ErrorState error={runs.error} onRetry={() => void runs.refetch()} />;
  const items = [
    ...new Map(
      runs.data.pages.flatMap((page) => page.runs).map((run) => [run.id, run]),
    ).values(),
  ];
  return (
    <div className="page">
      <ViewHeader title="Runs" fetching={runs.isFetching} updatedAt={runs.dataUpdatedAt} onRefresh={() => void runs.refetch()} />
      {runs.error && <StaleBanner error={runs.error} />}
      <div className="view-toolbar">
        <p>Run one shared Definition against a configured repository.</p>
        <button className="button button-primary" onClick={() => onCreateOpenChange(true)}>
          <Play size={15} /> Run once
        </button>
      </div>
      {items.length === 0 ? (
        <EmptyState
          icon={<Play size={22} />}
          title="No Runs yet"
          description="Choose a Definition and repository to start the first agent Job."
          action={<button className="button button-primary" onClick={() => onCreateOpenChange(true)}>Run once</button>}
        />
      ) : (
        <div className="workflow-list">
          <div className="run-table-head"><span>Definition</span><span>Repository</span><span>State</span><span>Started</span><span /></div>
          {items.map((run) => (
            <button className="run-row" key={run.id} onClick={() => onRun(run.id)}>
              <span className="workflow-identity"><strong>{run.definition.name}</strong><small>{run.id}</small></span>
              <span className="mono muted break-anywhere">{run.repository_remote_identities.join(", ")}</span>
              <StatusBadge state={run.state} />
              <span className="mono muted">{timeAgo(run.admitted_at)}</span>
              <ChevronRight size={15} className="row-chevron" />
            </button>
          ))}
          {runs.hasNextPage && (
            <button
              className="button button-secondary load-more"
              onClick={() => void runs.fetchNextPage()}
              disabled={runs.isFetchingNextPage}
            >
              {runs.isFetchingNextPage ? "Loading…" : "Load older Runs"}
            </button>
          )}
        </div>
      )}
      {createOpen && <RunOnceDialog onClose={() => onCreateOpenChange(false)} onCreated={(run) => onRun(run.run.id)} />}
    </div>
  );
}

function RunOnceDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (run: RunDetailType) => void }) {
  const titleID = useId();
  const definitionID = useId();
  const repositoryID = useId();
  const firstField = useRef<HTMLSelectElement>(null);
  const definitions = useQuery({ queryKey: ["definitions", "active", "run-once"], queryFn: loadActiveDefinitions });
  const repositories = useQuery({ queryKey: ["run-repositories"], queryFn: api.runRepositories });
  const [definition, setDefinition] = useState("");
  const [repository, setRepository] = useState("");
  useEffect(() => firstField.current?.focus(), []);
  const create = useMutation({
    mutationFn: () => api.createRun({
      request_key: crypto.randomUUID(),
      definition_id: definition,
      repository_id: repository,
    }),
    onSuccess: onCreated,
  });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (definition && repository) create.mutate();
  };
  const availableRepositories = repositories.data ?? [];
  return (
    <div className="modal-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby={titleID}>
        <div className="modal-header">
          <div><h2 id={titleID}>Run once</h2><p>Start one agent Job against one repository.</p></div>
          <button className="icon-button" aria-label="Close" onClick={onClose}><X size={18} /></button>
        </div>
        <form onSubmit={submit}>
          <div className="form-grid">
            <div className="field">
              <label htmlFor={definitionID}>Definition</label>
              <select id={definitionID} ref={firstField} value={definition} onChange={(event) => setDefinition(event.target.value)} required>
                <option value="">Choose a Definition</option>
                {(definitions.data?.definitions ?? []).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
              </select>
            </div>
            <div className="field">
              <label htmlFor={repositoryID}>Repository</label>
              <select id={repositoryID} value={repository} onChange={(event) => setRepository(event.target.value)} required>
                <option value="">Choose a repository</option>
                {availableRepositories.map((item) => <option key={item.id} value={item.id}>{item.remote_identity}</option>)}
              </select>
            </div>
          </div>
          {(definitions.error || repositories.error || create.error) && <InlineError error={definitions.error ?? repositories.error ?? create.error} />}
          {!definitions.isPending && definitions.data?.definitions.length === 0 && <p className="form-help">Create an active Definition before starting a Run.</p>}
          {!repositories.isPending && availableRepositories.length === 0 && <p className="form-help">Configure a repository on a Runner or enable managed acquisition before starting a Run.</p>}
          <div className="modal-actions">
            <button type="button" className="button button-secondary" onClick={onClose}>Cancel</button>
            <button className="button button-primary" disabled={create.isPending || !definition || !repository}>
              {create.isPending ? <LoaderCircle size={15} className="spin" /> : <Play size={15} />}
              {create.isPending ? "Starting…" : "Start Run"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

export function RunDetail({ id, onBack }: { id: string; onBack: () => void }) {
  const interval = useVisibleInterval(2_000);
  const queryClient = useQueryClient();
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [confirmRetry, setConfirmRetry] = useState(false);
  const [terminalCatchupAttemptID, setTerminalCatchupAttemptID] = useState<string | null>(null);
  const detail = useQuery({
    queryKey: ["run", id],
    queryFn: () => api.run(id),
    refetchInterval: (query) => ["blocked", "queued", "running"].includes(query.state.data?.run.state ?? "") ? interval : false,
  });
  const job = detail.data?.jobs[0];
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
  if (detail.isPending) return <LoadingState label="Loading Run" />;
  if (!detail.data || !job) return <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />;
  const data = detail.data;
  const active = ["blocked", "queued", "preparing", "running"].includes(job.job.state);
  const progress = (events.data ?? []).flatMap((event) => {
    const summary = eventSummary(event);
    return summary ? [{ event, summary }] : [];
  });
  return (
    <div className="page detail-page">
      <button className="back-button" onClick={onBack}><ArrowLeft size={16} /> All Runs</button>
      <div className="detail-heading">
        <div><StatusBadge state={data.run.state} /><h1>{data.run.definition.name}</h1><p>Admitted {new Date(data.run.admitted_at).toLocaleString()}</p></div>
        <div className="detail-actions">
          {active && !confirmCancel && <button className="button button-danger-secondary" onClick={() => setConfirmCancel(true)}><Square size={14} /> Cancel Job</button>}
          {job.job.state === "failed" && !confirmRetry && <button className="button button-primary" onClick={() => setConfirmRetry(true)}><RefreshCw size={14} /> Retry Job</button>}
        </div>
      </div>
      {confirmCancel && <div className="confirm-action" role="alert"><span>Cancel this Job?</span><button className="button button-danger" onClick={() => cancel.mutate()} disabled={cancel.isPending}>{cancel.isPending ? "Cancelling…" : "Confirm cancel"}</button><button className="button button-secondary" onClick={() => setConfirmCancel(false)}>Keep running</button></div>}
      {confirmRetry && <div className="warning-banner" role="alert"><AlertCircle size={17} /><span>{job.job.retry_may_repeat_effects ? "The first agent process started and may already have changed GitHub. Retrying can repeat external effects." : "Retry this failed Job?"}</span><button className="button button-primary" onClick={() => retry.mutate()} disabled={retry.isPending}>{retry.isPending ? "Retrying…" : "Confirm retry"}</button><button className="button button-secondary" onClick={() => setConfirmRetry(false)}>Cancel</button></div>}
      {(detail.error || cancel.error || retry.error) && <InlineError error={detail.error ?? cancel.error ?? retry.error} />}
      {job.job.blocked_reason && <div className="warning-banner"><Clock3 size={17} /> {job.job.blocked_reason}</div>}
      <div className="detail-grid">
        <section className="panel detail-main"><PanelHeading title="Definition prompt" /><div className="long-copy">{job.resolved_prompt}</div></section>
        <section className="panel"><PanelHeading title="Job" /><dl className="metadata">
          <div><dt>State</dt><dd><StatusBadge state={job.job.state} /></dd></div>
          <div><dt>Repository</dt><dd className="break-anywhere">{job.job.repository_remote_identity}</dd></div>
          <div><dt>Runner</dt><dd>{job.job.assigned_worker_id ?? "Waiting for a compatible Runner"}</dd></div>
          <div><dt>Runtime</dt><dd>{runtimeLabel(job.job.required_runtime)}</dd></div>
          <div><dt>Duration</dt><dd>{duration(job.job.admitted_at, job.job.terminal_at)}</dd></div>
          <div><dt>Job ID</dt><dd className="mono break-anywhere">{job.job.id}</dd></div>
        </dl></section>
      </div>
      {(job.job.result || job.job.failure_reason) && <section className="panel"><PanelHeading title="Result" />{job.job.result && <div className="attempt-output success-output"><strong>Agent result</strong><pre>{job.job.result}</pre></div>}{job.job.failure_reason && <div className="attempt-output error-output"><strong>Failure reason</strong><pre>{job.job.failure_reason}</pre></div>}</section>}
      <section className="panel progress-panel"><PanelHeading title="Agent output" aside={`${progress.length} updates`} />{events.error && <InlineError error={events.error} />}{latestAttempt && events.isPending ? <div className="loading-line" role="status"><LoaderCircle size={16} className="spin" />Loading output</div> : progress.length === 0 ? <div className="quiet-empty">Output will appear after the Runner starts the agent.</div> : <ol className="event-list">{progress.map(({ event, summary }) => <li key={event.sequence}><span className="event-marker" aria-hidden="true" /><div><span className="event-kind">{summary.label}</span><p>{summary.text}</p><time dateTime={event.server_time}>{new Date(event.server_time).toLocaleTimeString()}</time></div></li>)}</ol>}</section>
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
