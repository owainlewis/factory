import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, CalendarClock, Eye, GitBranch, Pencil, Play, Plus, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import { timeAgo } from "./format";
import type { ManagedRepository, Routine, Runtime, SaveRoutineInput } from "./types";
import { EmptyState, ErrorState, InlineError, LoadingState, StatusBadge, ViewHeader } from "./ui";

export function RoutinesView({ initialID, createOpen, onWork }: { initialID?: string; createOpen?: boolean; onWork: (id: string) => void }) {
  const client = useQueryClient();
  const [editing, setEditing] = useState<Routine | "new" | null>(createOpen ? "new" : null);
  const [showArchived, setShowArchived] = useState(false);
  const runRequests = useRef(new Map<string, { generation: number; requestKey: string }>());
  const query = useQuery({ queryKey: ["routines", showArchived], queryFn: () => api.routines(showArchived) });
  const run = useMutation({
    mutationFn: ({ id, requestKey }: { id: string; generation: number; requestKey: string }) => api.runRoutine(id, requestKey),
    onSuccess: (detail, request) => {
      runRequests.current.delete(request.id);
      void client.invalidateQueries({ queryKey: ["overview"] });
      void client.invalidateQueries({ queryKey: ["work"] });
      onWork(detail.work.id);
    },
  });
  const archive = useMutation({
    mutationFn: (routine: Routine) => api.archiveRoutine(routine.id, !routine.archived, routine.generation),
    onSuccess: () => {
      setEditing(null);
      void client.invalidateQueries({ queryKey: ["routines"] });
      void client.invalidateQueries({ queryKey: ["overview"] });
    },
  });
  const resetArchive = archive.reset;
  const openRoutine = useCallback((id: string) => {
    resetArchive();
    void api.routine(id).then(setEditing);
  }, [resetArchive]);
  useEffect(() => {
    if (initialID) openRoutine(initialID);
  }, [initialID, openRoutine]);
  if (query.isPending) return <LoadingState label="Loading Routines" />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  return <div className="page">
    <ViewHeader title="Routines" fetching={query.isFetching} updatedAt={query.dataUpdatedAt} onRefresh={() => void query.refetch()} />
    <div className="view-toolbar routine-toolbar">
      <p>A prompt you can run now or schedule across repositories.</p>
      <div className="work-toolbar-actions">
        <button className={`quiet-toggle ${showArchived ? "active" : ""}`} onClick={() => setShowArchived((value) => !value)}><Archive size={14} /> Archived</button>
        <button className="button button-primary" onClick={() => { resetArchive(); setEditing("new"); }}><Plus size={15} /> New Routine</button>
      </div>
    </div>
    <InlineError error={run.error} />
    {!query.data?.length ? <EmptyState icon={<CalendarClock size={22} />} title="No Routines yet" description="Create one prompt, choose its repositories, then run it now or on a schedule." action={<button className="button button-primary" onClick={() => setEditing("new")}><Plus size={15} /> New Routine</button>} /> :
      <div className="routine-list panel">
        {query.data.map((routine) => <article className="routine-row" key={routine.id}>
          <button className="routine-copy" onClick={() => openRoutine(routine.id)}>
            <span className="routine-title-line"><strong>{routine.name}</strong>{routine.archived && <span className="subtle-pill">Archived</span>}{routine.read_only && <span className="subtle-pill">Read-only</span>}</span>
            <span>{routine.prompt_preview}</span>
            <small><GitBranch size={12} /> {routine.repository_count} repos · {routine.runtime} · up to {routine.concurrency_limit} at once</small>
          </button>
          <div className="routine-schedule">
            <StatusBadge state={routine.schedule.enabled ? routine.schedule.health_status : "disabled"} />
            <small>{routine.schedule.enabled ? `${routine.schedule.cron} · ${routine.schedule.timezone}` : "Manual only"}</small>
          </div>
          <div className="routine-last"><span>{routine.last_work_state ? <StatusBadge state={routine.last_work_state} /> : "No Work yet"}</span><small>Edited {timeAgo(routine.updated_at)}</small></div>
          <div className="routine-actions">
            <button className="icon-button" aria-label={`${routine.read_only ? "View" : "Edit"} ${routine.name}`} onClick={() => openRoutine(routine.id)}>{routine.read_only ? <Eye size={15} /> : <Pencil size={15} />}</button>
            <button className="button button-secondary" title={routine.repository_count === 0 ? "Add a repository before running" : undefined} disabled={routine.read_only || routine.archived || routine.repository_count === 0 || run.isPending} onClick={() => {
              const previous = runRequests.current.get(routine.id);
              const requestKey = previous?.generation === routine.generation ? previous.requestKey : crypto.randomUUID();
              runRequests.current.set(routine.id, { generation: routine.generation, requestKey });
              run.mutate({ id: routine.id, generation: routine.generation, requestKey });
            }}><Play size={14} /> Run now</button>
          </div>
        </article>)}
      </div>}
    {editing && <RoutineComposer routine={editing === "new" ? undefined : editing} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); void client.invalidateQueries({ queryKey: ["routines"] }); void client.invalidateQueries({ queryKey: ["overview"] }); }} onArchive={(routine) => archive.mutate(routine)} archiveError={archive.error} archivePending={archive.isPending} />}
  </div>;
}

function RoutineComposer({ routine, onClose, onSaved, onArchive, archiveError, archivePending }: { routine?: Routine; onClose: () => void; onSaved: () => void; onArchive: (routine: Routine) => void; archiveError: Error | null; archivePending: boolean }) {
  const repositories = useQuery({ queryKey: ["repositories"], queryFn: api.repositories });
  const readOnly = routine?.read_only ?? false;
  const [name, setName] = useState(routine?.name ?? "");
  const [prompt, setPrompt] = useState(routine?.prompt ?? "");
  const [runtime, setRuntime] = useState<Runtime>(routine?.runtime ?? "codex");
  const [timeout, setTimeoutValue] = useState(routine?.timeout_seconds ?? 7200);
  const [concurrency, setConcurrency] = useState(routine?.concurrency_limit ?? 10);
  const [selected, setSelected] = useState<string[]>(routine?.repositories?.map((repository) => repository.id) ?? []);
  const [scheduled, setScheduled] = useState(routine?.schedule.enabled ?? false);
  const [cron, setCron] = useState(routine?.schedule.cron ?? "0 9 * * 1");
  const [timezone, setTimezone] = useState(routine?.schedule.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone);
  const save = useMutation({
    mutationFn: () => {
      const input: SaveRoutineInput = {
        name, prompt, runtime, timeout_seconds: timeout,
        concurrency_limit: concurrency, repository_ids: selected,
        schedule: { enabled: scheduled, cron: scheduled ? cron : undefined, timezone: scheduled ? timezone : undefined },
        expected_generation: routine?.generation,
      };
      return routine ? api.updateRoutine(routine.id, input) : api.createRoutine(input);
    },
    onSuccess: onSaved,
  });
  const discard = useMutation({
    mutationFn: () => api.discardRoutineOccurrence(routine!.id, routine!.schedule.pending_due_at!),
    onSuccess: onSaved,
  });
  const toggleRepository = (id: string) => setSelected((current) => current.includes(id) ? current.filter((value) => value !== id) : [...current, id]);
  return <div className="modal-layer" role="presentation">
    <button className="modal-scrim" aria-label="Close Routine editor" onClick={onClose} />
    <section className="modal routine-modal" role="dialog" aria-modal="true" aria-labelledby="routine-composer-title">
      <header className="modal-header"><div><h2 id="routine-composer-title">{readOnly ? "Routine revision" : routine ? "Edit Routine" : "New Routine"}</h2><p>{readOnly ? "Historical revision preserved as read-only." : "One prompt, one repository set, optional schedule."}</p></div><button className="icon-button" aria-label="Close" onClick={onClose}><X size={17} /></button></header>
      <div className="modal-body routine-form">
        <label className="field"><span>Name</span><input autoFocus={!readOnly} disabled={readOnly} value={name} onChange={(event) => setName(event.target.value)} placeholder="Weekly bug scan" /></label>
        <label className="field"><span>Prompt</span><textarea className="routine-prompt" disabled={readOnly} value={prompt} onChange={(event) => setPrompt(event.target.value)} placeholder="Review the repository and fix..." /></label>
        <div className="routine-settings">
          <div className="field"><span>Runtime</span><div className="choice-control">{[["codex", "Codex"], ["claude-code", "Claude"], ["pi", "Pi"]].map(([value, label]) => <button type="button" key={value} disabled={readOnly} aria-pressed={runtime === value} onClick={() => setRuntime(value as Runtime)}>{label}</button>)}</div></div>
          <div className="field"><span>Timeout</span><div className="choice-control timeout-control">{[[1800, "30m"], [3600, "1h"], [7200, "2h"], [14400, "4h"], [28800, "8h"]].map(([value, label]) => <button type="button" key={value} disabled={readOnly} aria-pressed={timeout === value} onClick={() => setTimeoutValue(Number(value))}>{label}</button>)}</div></div>
          <label className="field"><span>Parallel targets</span><input type="number" min={1} max={100} disabled={readOnly} value={concurrency} onChange={(event) => setConcurrency(Number(event.target.value))} /></label>
        </div>
        <div className="field"><span>Repositories</span><div className="repository-picker">{(repositories.data ?? []).map((repository: ManagedRepository) => <button type="button" key={repository.id} disabled={readOnly} className={selected.includes(repository.id) ? "selected" : ""} onClick={() => toggleRepository(repository.id)}><span className="check-mark">{selected.includes(repository.id) ? "✓" : ""}</span><GitBranch size={14} />{repository.remote_identity}</button>)}</div></div>
        <div className="schedule-card">
          <label className="switch-line"><span><strong>Schedule</strong><small>Run automatically using a five-field cron schedule.</small></span><input type="checkbox" disabled={readOnly} checked={scheduled} onChange={(event) => setScheduled(event.target.checked)} /></label>
          {scheduled && <div className="routine-settings schedule-fields"><label className="field"><span>Cron</span><input className="mono" disabled={readOnly} value={cron} onChange={(event) => setCron(event.target.value)} /></label><label className="field"><span>Timezone</span><input disabled={readOnly} value={timezone} onChange={(event) => setTimezone(event.target.value)} /></label></div>}
          {routine?.schedule.pending_due_at && <div className="pending-occurrence"><span><strong>{routine.schedule.health_status === "disabled" ? "Occurrence paused" : "Occurrence blocked"}</strong><small>{routine.schedule.health_message}</small></span>{!readOnly && (routine.schedule.health_status === "blocked" || routine.schedule.health_status === "disabled") && <button className="button button-danger-secondary" disabled={discard.isPending} onClick={() => discard.mutate()}>{discard.isPending ? "Discarding…" : "Discard occurrence"}</button>}</div>}
        </div>
        <InlineError error={save.error ?? archiveError ?? discard.error ?? repositories.error} />
      </div>
      <footer className="modal-footer">{routine && !readOnly && <button className="button button-danger-secondary" disabled={archivePending} onClick={() => onArchive(routine)}><Archive size={14} /> {archivePending ? (routine.archived ? "Restoring…" : "Archiving…") : (routine.archived ? "Restore" : "Archive")}</button>}<span /><button className="button button-secondary" onClick={onClose}>{readOnly ? "Close" : "Cancel"}</button>{!readOnly && <button className="button button-primary" disabled={save.isPending || !name.trim() || !prompt.trim() || (scheduled && selected.length === 0)} onClick={() => save.mutate()}>{save.isPending ? "Saving…" : "Save Routine"}</button>}</footer>
    </section>
  </div>;
}
