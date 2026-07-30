import {
  AlertCircle,
  ArrowLeft,
  ChevronRight,
  Clock3,
  LoaderCircle,
  RefreshCw,
  Square,
} from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "./api";
import { invalidateControlPlane } from "./controlPlaneQueries";
import { duration, eventText, stateLabel } from "./format";
import { useVisibleInterval } from "./polling";
import type {
  Attempt,
  AttemptEvent,
  TaskDetail as TaskDetailType,
  Worker,
} from "./types";
import {
  ErrorState,
  InlineError,
  LoadingState,
  PanelHeading,
  StaleBanner,
  StatusBadge,
} from "./ui";

export function TaskDetail({ id, workers, onBack }: { id: string; workers: Worker[]; onBack: () => void }) {
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

function LoadingLine({ label }: { label: string }) {
  return <div className="loading-line" role="status"><LoaderCircle size={16} className="spin" />{label}</div>;
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
