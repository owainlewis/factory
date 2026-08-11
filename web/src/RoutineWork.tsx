import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ArrowLeft, Columns3, List, RotateCcw, Rows3, StopCircle } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "./api";
import { duration, eventSummary, timeAgo } from "./format";
import type { Attempt, AttemptEvent, WorkItem, WorkState, WorkTarget } from "./types";
import { EmptyState, ErrorState, InlineError, LoadingState, StaleBanner, StatusBadge, ViewHeader } from "./ui";

export type WorkViewMode = "table" | "list" | "kanban";

interface WorkHistory {
  items: WorkItem[];
  cursor: string | null;
  headCursor: string | null;
}

const workHistoryKey = ["work-history"] as const;

export function RoutineWorkView({ mode, onMode, onWork }: { mode: WorkViewMode; onMode: (mode: WorkViewMode) => void; onWork: (id: string) => void }) {
  const client = useQueryClient();
  const cachedHistory = client.getQueryData<WorkHistory>(workHistoryKey);
  const [history, setHistory] = useState<WorkItem[]>(cachedHistory?.items ?? []);
  const [historyCursor, setHistoryCursor] = useState<string | null>(cachedHistory?.cursor ?? null);
  const previousHeadCursor = useRef<string | null>(cachedHistory?.headCursor ?? null);
  const query = useQuery({ queryKey: ["work", "head"], queryFn: () => api.workItems(), refetchInterval: 5_000 });
  const headIDs = new Set(query.data?.work.map((work) => work.id) ?? []);
  const activeHistoricalIDs = history
    .filter((work) => !headIDs.has(work.id) && activeWorkState(work.state))
    .map((work) => work.id)
    .sort();
  const activeHistory = useQuery({
    queryKey: ["work-history", "active", activeHistoricalIDs],
    queryFn: async () => Promise.all(activeHistoricalIDs.map(async (id) => (await api.workItem(id)).work)),
    enabled: activeHistoricalIDs.length > 0,
    refetchInterval: (activeQuery) => activeQuery.state.data?.every((work) => !activeWorkState(work.state)) ? false : 5_000,
  });
  const refreshedHistory = useMemo(
    () => updateWorkItems(history, activeHistory.data ?? []),
    [activeHistory.data, history],
  );
  const loadHistory = useMutation({
    mutationFn: ({ cursor }: { cursor: string; headCursor: string | null }) => api.workItems(cursor),
    onSuccess: (page, request) => {
      setHistory((current) => mergeWorkItems(page.work, updateWorkItems(current, activeHistory.data ?? [])));
      if (previousHeadCursor.current === request.headCursor) setHistoryCursor(page.next_cursor);
    },
  });
  useEffect(() => {
    if (!query.data) return;
    if (previousHeadCursor.current !== query.data.next_cursor) setHistoryCursor(query.data.next_cursor);
    previousHeadCursor.current = query.data.next_cursor;
  }, [query.data]);
  useEffect(() => {
    client.setQueryData<WorkHistory>(workHistoryKey, {
      items: refreshedHistory,
      cursor: historyCursor,
      headCursor: previousHeadCursor.current,
    });
  }, [client, historyCursor, query.data, refreshedHistory]);
  if (query.isPending) return <LoadingState label="Loading Work" />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  const items = mergeWorkItems(query.data?.work ?? [], refreshedHistory);
  const error = loadHistory.error ?? activeHistory.error;
  return <div className="page page-work">
    <ViewHeader title="Work" fetching={query.isFetching || activeHistory.isFetching || loadHistory.isPending} updatedAt={Math.max(query.dataUpdatedAt, activeHistory.dataUpdatedAt)} onRefresh={() => void query.refetch()} />
    {error && <StaleBanner error={error} />}
    <div className="view-toolbar"><p>Every Routine invocation, grouped across its repository targets.</p><ViewSwitch mode={mode} onMode={onMode} /></div>
    {!items.length ? <EmptyState icon={<Rows3 size={22} />} title="No Work yet" description="Run a Routine now or wait for its next schedule." /> : mode === "table" ? <WorkTable items={items} onWork={onWork} /> : mode === "list" ? <WorkList items={items} onWork={onWork} /> : <WorkBoard items={items} onWork={onWork} />}
    {historyCursor && <div className="load-more-row"><button className="button button-secondary" disabled={loadHistory.isPending} onClick={() => loadHistory.mutate({ cursor: historyCursor, headCursor: previousHeadCursor.current })}>{loadHistory.isPending ? "Loading…" : "Load more Work"}</button></div>}
  </div>;
}

function activeWorkState(state: WorkState): boolean {
  return state === "blocked" || state === "queued" || state === "running";
}

function mergeWorkItems(primary: WorkItem[], secondary: WorkItem[]): WorkItem[] {
  const primaryIDs = new Set(primary.map((work) => work.id));
  return [...primary, ...secondary.filter((work) => !primaryIDs.has(work.id))];
}

function updateWorkItems(current: WorkItem[], updates: WorkItem[]): WorkItem[] {
  const byID = new Map(updates.map((work) => [work.id, work]));
  return current.map((work) => byID.get(work.id) ?? work);
}

function ViewSwitch({ mode, onMode }: { mode: WorkViewMode; onMode: (mode: WorkViewMode) => void }) {
  return <div className="work-view-switcher" aria-label="Work view"><button aria-pressed={mode === "table"} onClick={() => onMode("table")}><Rows3 size={14} /> Table</button><button aria-pressed={mode === "list"} onClick={() => onMode("list")}><List size={14} /> List</button><button aria-pressed={mode === "kanban"} onClick={() => onMode("kanban")}><Columns3 size={14} /> Board</button></div>;
}

function WorkTable({ items, onWork }: { items: WorkItem[]; onWork: (id: string) => void }) {
  return <div className="work-table panel"><div className="work-table-row work-table-head"><span>Routine</span><span>Progress</span><span>Source</span><span>State</span><span>Started</span><span>Duration</span></div>{items.map((work) => <button className="work-table-row" key={work.id} onClick={() => onWork(work.id)}><span className="work-name"><strong>{work.routine.name}</strong><small>{work.id.slice(0, 8)}</small></span><span><Progress work={work} /></span><span className="capitalize">{work.source.replace("_", " ")}</span><span><StatusBadge state={work.state} /></span><span>{timeAgo(work.admitted_at)}</span><span>{work.terminal_at ? duration(work.admitted_at, work.terminal_at) : "In progress"}</span></button>)}</div>;
}

function WorkList({ items, onWork }: { items: WorkItem[]; onWork: (id: string) => void }) {
  return <div className="work-clean-list">{items.map((work) => <button className="work-clean-card" key={work.id} onClick={() => onWork(work.id)}><span className="work-card-state"><StatusBadge state={work.state} /></span><span className="work-card-copy"><strong>{work.routine.name}</strong><small>{work.source.replace("_", " ")} · started {timeAgo(work.admitted_at)}</small></span><Progress work={work} /><span className="work-card-duration">{work.terminal_at ? duration(work.admitted_at, work.terminal_at) : "Active"}</span></button>)}</div>;
}

const boardColumns: Array<{ key: string; label: string; states: WorkState[] }> = [
  { key: "queued", label: "Queued", states: ["queued"] },
  { key: "active", label: "Active", states: ["running"] },
  { key: "attention", label: "Attention", states: ["blocked", "failed", "partial"] },
  { key: "done", label: "Done", states: ["succeeded", "cancelled"] },
];

function WorkBoard({ items, onWork }: { items: WorkItem[]; onWork: (id: string) => void }) {
  return <div className="routine-board">{boardColumns.map((column) => { const values = items.filter((item) => column.states.includes(item.state)); return <section className="routine-board-column" key={column.key}><header><strong>{column.label}</strong><span>{values.length}</span></header><div>{values.map((work) => <button className="routine-board-card" key={work.id} onClick={() => onWork(work.id)}><strong>{work.routine.name}</strong><small>{work.source.replace("_", " ")} · {timeAgo(work.admitted_at)}</small><Progress work={work} /></button>)}{!values.length && <p>Nothing here</p>}</div></section>; })}</div>;
}

function Progress({ work }: { work: WorkItem }) {
  const complete = work.succeeded_count + work.failed_count + work.cancelled_count;
  const percent = work.target_count ? Math.round((complete / work.target_count) * 100) : 0;
  return <span className="target-progress"><span><i style={{ width: `${percent}%` }} /></span><small>{complete}/{work.target_count}</small></span>;
}

export function RoutineWorkDetail({ id, onBack }: { id: string; onBack: () => void }) {
  const client = useQueryClient();
  const query = useQuery({ queryKey: ["work", id], queryFn: () => api.workItem(id), refetchInterval: 3_000 });
  const cancel = useMutation({ mutationFn: () => api.cancelWork(id), onSuccess: () => { void query.refetch(); void client.invalidateQueries({ queryKey: ["work"] }); } });
  const retry = useMutation({ mutationFn: (targetId: string) => api.retryWorkTarget(id, targetId), onSuccess: () => { void query.refetch(); void client.invalidateQueries({ queryKey: ["work"] }); } });
  const cancelTarget = useMutation({ mutationFn: (targetId: string) => api.cancelWorkTarget(id, targetId), onSuccess: () => { void query.refetch(); void client.invalidateQueries({ queryKey: ["work"] }); } });
  if (query.isPending) return <LoadingState label="Loading Work" />;
  if (query.isError || !query.data) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  const { work, targets } = query.data;
  return <div className="page work-detail-clean">
    <button className="back-link" onClick={onBack}><ArrowLeft size={14} /> Work</button>
    <div className="detail-heading work-detail-heading"><div><span className="eyebrow">{work.source.replace("_", " ")} · {work.id.slice(0, 8)}</span><h1>{work.routine.name}</h1><p>{work.target_count} repository target{work.target_count === 1 ? "" : "s"} · started {timeAgo(work.admitted_at)}</p></div><div className="detail-actions"><StatusBadge state={work.state} />{work.active_count > 0 && <button className="button button-danger-secondary" disabled={cancel.isPending} onClick={() => cancel.mutate()}><StopCircle size={14} /> Cancel</button>}</div></div>
    <InlineError error={cancel.error ?? retry.error ?? cancelTarget.error} />
    <section className="work-summary-strip"><div><span>Progress</span><strong>{work.succeeded_count + work.failed_count + work.cancelled_count}/{work.target_count}</strong></div><div><span>Succeeded</span><strong>{work.succeeded_count}</strong></div><div><span>Failed</span><strong>{work.failed_count}</strong></div><div><span>Duration</span><strong>{work.terminal_at ? duration(work.admitted_at, work.terminal_at) : "Active"}</strong></div></section>
    <section className="panel target-panel"><div className="panel-heading"><h2>Targets</h2><span>{targets?.length ?? 0}</span></div>{(targets ?? []).map((target) => <TargetRow key={target.id} target={target} onRetry={() => retry.mutate(target.id)} onCancel={() => cancelTarget.mutate(target.id)} />)}</section>
    <details className="prompt-panel"><summary>Routine snapshot</summary><pre>{work.routine.prompt}</pre></details>
  </div>;
}

function TargetRow({ target, onRetry, onCancel }: { target: WorkTarget; onRetry: () => void; onCancel: () => void }) {
  const active = ["blocked", "queued", "preparing", "running"].includes(target.state);
  const [open, setOpen] = useState(false);
  return <details className="target-row" onToggle={(event) => setOpen(event.currentTarget.open)}><summary><span className="target-repo"><strong>{target.repository_identity}</strong><small>{target.assigned_worker_id ? `Worker ${target.assigned_worker_id}` : target.blocked_reason ?? "Waiting for a Worker"}</small></span><StatusBadge state={target.state} /><span className="target-time">{target.terminal_at && target.started_at ? duration(target.started_at, target.terminal_at) : target.started_at ? "Active" : "Waiting"}</span></summary>{open && <div className="target-detail-body">{target.failure_reason && <p className="target-failure"><AlertTriangle size={14} /> {target.failure_reason}</p>}{target.result && <pre>{target.result}</pre>}{(target.attempts ?? []).map((attempt) => <AttemptStream key={attempt.id} attempt={attempt} />)}<div className="target-detail-actions"><span>{target.attempts?.length ?? 0} attempt{target.attempts?.length === 1 ? "" : "s"}</span>{active && <button className="button button-danger-secondary" onClick={onCancel}><StopCircle size={14} /> Cancel target</button>}{(target.state === "failed" || target.state === "cancelled") && <button className="button button-secondary" onClick={onRetry}><RotateCcw size={14} /> Retry target</button>}</div></div>}</details>;
}

function AttemptStream({ attempt }: { attempt: Attempt }) {
  const active = attempt.state === "preparing" || attempt.state === "running";
  const client = useQueryClient();
  const eventKey = ["attempt-events", attempt.id] as const;
  const query = useQuery({
    queryKey: eventKey,
    queryFn: () => loadAttemptEvents(attempt.id, client.getQueryData<AttemptEvent[]>(eventKey) ?? []),
    refetchInterval: active ? 2_000 : false,
  });
  const wasActive = useRef(active);
  const refetch = query.refetch;
  useEffect(() => {
    const becameTerminal = wasActive.current && !active;
    wasActive.current = active;
    if (becameTerminal) void refetch();
  }, [active, refetch]);
  const events = (query.data ?? []).flatMap((event) => {
    const summary = eventSummary(event);
    return summary ? [{ event, summary }] : [];
  });
  return <section className="attempt-stream"><header><strong>Attempt {attempt.attempt_number}</strong><StatusBadge state={attempt.state} /></header>{query.error && <InlineError error={query.error} />}{query.isPending ? <p className="quiet-empty">Loading attempt output…</p> : events.length === 0 ? <p className="quiet-empty">No runtime output recorded.</p> : <ol className="attempt-events">{events.map(({ event, summary }) => <li key={event.sequence}><span><strong>{summary.label}</strong><time dateTime={event.server_time}>{new Date(event.server_time).toLocaleTimeString()}</time></span><p>{summary.text}</p></li>)}</ol>}</section>;
}

async function loadAttemptEvents(attemptID: string, current: AttemptEvent[]): Promise<AttemptEvent[]> {
  const events = [...current];
  let after = events.at(-1)?.sequence ?? -1;
  for (;;) {
    const requestedAfter = after;
    const page = await api.events(attemptID, after);
    for (const event of page.events) {
      if (event.sequence > after) {
        events.push(event);
        after = event.sequence;
      }
    }
    if (!page.has_more) return events;
    if (page.next_after <= requestedAfter) throw new Error("Attempt event pagination did not advance.");
    after = page.next_after;
  }
}
