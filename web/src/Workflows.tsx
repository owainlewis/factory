import {
  ArrowLeft,
  BookOpenText,
  ChevronRight,
  LoaderCircle,
  Pencil,
  Plus,
  Power,
  PowerOff,
  X,
} from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from "react";
import { api } from "./api";
import { invalidateControlPlane } from "./controlPlaneQueries";
import type { CreateWorkflowInput, Workflow, WorkflowDetail as WorkflowDetailType } from "./types";
import {
  EmptyState,
  ErrorState,
  InlineError,
  LoadingState,
  PanelHeading,
  StaleBanner,
  ViewHeader,
} from "./ui";

export function WorkflowsView({ onWorkflow }: { onWorkflow: (id: string) => void }) {
  const [createOpen, setCreateOpen] = useState(false);
  const [history, setHistory] = useState<Workflow[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>();
  const previousHeadCursor = useRef<string | null | undefined>(undefined);
  const workflows = useQuery({
    queryKey: ["workflows", "head"],
    queryFn: () => api.workflows(),
  });
  const loadMore = useMutation({
    mutationFn: ({ cursor }: { cursor: string; headCursor: string | null }) => api.workflows(cursor),
    onSuccess: (page, request) => {
      setHistory((current) => mergeWorkflows(current, page.workflows));
      if (previousHeadCursor.current === request.headCursor) {
        setNextCursor(page.next_cursor);
      }
    },
  });
  useEffect(() => {
    if (!workflows.data) return;
    const boundaryChanged = previousHeadCursor.current !== workflows.data.next_cursor;
    setNextCursor((current) => boundaryChanged ? workflows.data.next_cursor : current);
    previousHeadCursor.current = workflows.data.next_cursor;
  }, [workflows.data]);
  const activeCursor = nextCursor === undefined ? workflows.data?.next_cursor : nextCursor;
  const items = mergeWorkflows(workflows.data?.workflows ?? [], history);

  if (workflows.isPending) return <LoadingState label="Loading runbooks" />;
  if (!workflows.data) return <ErrorState error={workflows.error} onRetry={() => void workflows.refetch()} />;

  return (
    <div className="page">
      <ViewHeader
        title="Runbooks"
        fetching={workflows.isFetching}
        updatedAt={workflows.dataUpdatedAt}
        onRefresh={() => {
          setHistory([]);
          setNextCursor(undefined);
          void workflows.refetch();
        }}
      />
      {workflows.error && <StaleBanner error={workflows.error} />}
      <div className="view-toolbar">
        <p>Reusable Markdown instructions for tasks and Automations, with immutable numbered revisions.</p>
        <button className="button button-primary" onClick={() => setCreateOpen(true)}>
          <Plus size={15} /> Create runbook
        </button>
      </div>
      {items.length === 0 ? (
        <EmptyState
          icon={<BookOpenText size={22} />}
          title="No runbooks yet"
          description="Create trusted instructions once, then pin them when delegating tasks."
          action={<button className="button button-primary" onClick={() => setCreateOpen(true)}>Create runbook</button>}
        />
      ) : (
        <div className="workflow-list">
          <div className="workflow-table-head"><span>Title</span><span>Revision</span><span>State</span><span>Updated</span><span /></div>
          {items.map((workflow) => (
            <button className="workflow-row" key={workflow.id} onClick={() => onWorkflow(workflow.id)}>
              <span className="workflow-identity">
                <strong>{workflow.current_revision.title}</strong>
                <small>{workflow.current_revision.summary || "No summary"}</small>
              </span>
              <span className="mono">#{workflow.current_revision.revision_number}</span>
              <span className={`status-badge ${workflow.enabled ? "status-succeeded" : "status-cancelled"}`}>
                <span className="status-dot" />{workflow.enabled ? "Enabled" : "Disabled"}
              </span>
              <span className="mono muted">{new Date(workflow.updated_at).toLocaleString()}</span>
              <ChevronRight size={15} className="row-chevron" />
            </button>
          ))}
        </div>
      )}
      {activeCursor && (
        <div className="load-more">
          <button
            className="button button-secondary"
            disabled={loadMore.isPending}
            onClick={() => loadMore.mutate({
              cursor: activeCursor,
              headCursor: previousHeadCursor.current ?? null,
            })}
          >
            {loadMore.isPending ? "Loading…" : "Load more runbooks"}
          </button>
        </div>
      )}
      {loadMore.error && <InlineError error={loadMore.error} />}
      {createOpen && <WorkflowForm mode="create" onClose={() => setCreateOpen(false)} onSaved={(detail) => {
        setCreateOpen(false);
        onWorkflow(detail.workflow.id);
      }} />}
    </div>
  );
}

export function WorkflowDetail({ id, onBack }: { id: string; onBack: () => void }) {
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [confirmEnabled, setConfirmEnabled] = useState<boolean>();
  const detail = useQuery({ queryKey: ["workflow", id], queryFn: () => api.workflow(id) });
  const enabled = useMutation({
    mutationFn: (next: boolean) => api.setWorkflowEnabled({ id, enabled: next }),
    onSuccess: async (next) => {
      queryClient.setQueryData(["workflow", id], next);
      setConfirmEnabled(undefined);
      await invalidateControlPlane(queryClient);
    },
  });
  if (detail.isPending) return <LoadingState label="Loading runbook" />;
  if (!detail.data) return <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />;
  const data = detail.data;
  const current = data.workflow.current_revision;

  return (
    <div className="page detail-page">
      <button className="back-button" onClick={onBack}><ArrowLeft size={16} /> All runbooks</button>
      <div className="detail-heading">
        <div>
          <span className={`status-badge ${data.workflow.enabled ? "status-succeeded" : "status-cancelled"}`}>
            <span className="status-dot" />{data.workflow.enabled ? "Enabled" : "Disabled"}
          </span>
          <h1>{current.title}</h1>
          <p>Revision {current.revision_number} · updated {new Date(data.workflow.updated_at).toLocaleString()}</p>
        </div>
        <div className="detail-actions">
          <button className="button button-secondary" onClick={() => setEditing(true)}>
            <Pencil size={14} /> New revision
          </button>
          <button
            className={data.workflow.enabled ? "button button-danger-secondary" : "button button-primary"}
            onClick={() => setConfirmEnabled(!data.workflow.enabled)}
          >
            {data.workflow.enabled ? <PowerOff size={14} /> : <Power size={14} />}
            {data.workflow.enabled ? "Disable" : "Enable"}
          </button>
        </div>
      </div>
      {detail.error && <StaleBanner error={detail.error} />}
      {enabled.error && <InlineError error={enabled.error} />}
      {confirmEnabled !== undefined && (
        <div className="confirm-action workflow-confirm" role="alert">
          <span>{confirmEnabled ? "Enable this runbook for new tasks?" : "Disable it for new tasks? Existing tasks and retries keep their snapshot."}</span>
          <button
            className={confirmEnabled ? "button button-primary" : "button button-danger"}
            disabled={enabled.isPending}
            onClick={() => enabled.mutate(confirmEnabled)}
          >
            {enabled.isPending ? "Saving…" : `Confirm ${confirmEnabled ? "enable" : "disable"}`}
          </button>
          <button className="button button-secondary" onClick={() => setConfirmEnabled(undefined)}>Cancel</button>
        </div>
      )}
      <div className="detail-grid">
        <section className="panel detail-main">
          <PanelHeading title="Current instructions" aside={`Revision ${current.revision_number}`} />
          <div className="long-copy">{current.instructions}</div>
        </section>
        <section className="panel">
          <PanelHeading title="Runbook" />
          <dl className="metadata">
            <div><dt>Summary</dt><dd>{current.summary || "No summary"}</dd></div>
            <div><dt>Created</dt><dd>{new Date(data.workflow.created_at).toLocaleString()}</dd></div>
            <div><dt>Revisions</dt><dd>{data.revisions.length}</dd></div>
            <div><dt>Identity</dt><dd className="mono break-anywhere">{data.workflow.id}</dd></div>
          </dl>
        </section>
      </div>
      <section className="panel">
        <PanelHeading title="Revision history" aside={`${data.revisions.length} immutable revisions`} />
        <div className="revision-list">
          {data.revisions.map((revision) => (
            <details key={revision.id} open={revision.id === current.id}>
              <summary>
                <span><strong>Revision {revision.revision_number}</strong><small>{revision.title} · {new Date(revision.created_at).toLocaleString()}</small></span>
                <span className="mono">{revision.id.slice(0, 8)}</span>
              </summary>
              {revision.summary && <p>{revision.summary}</p>}
              <pre>{revision.instructions}</pre>
            </details>
          ))}
        </div>
      </section>
      {editing && <WorkflowForm mode="revise" detail={data} onClose={() => setEditing(false)} onSaved={(next) => {
        queryClient.setQueryData(["workflow", id], next);
        setEditing(false);
        void invalidateControlPlane(queryClient);
      }} />}
    </div>
  );
}

function WorkflowForm({
  mode,
  detail,
  onClose,
  onSaved,
}: {
  mode: "create" | "revise";
  detail?: WorkflowDetailType;
  onClose: () => void;
  onSaved: (detail: WorkflowDetailType) => void;
}) {
  const queryClient = useQueryClient();
  const titleID = useId();
  const summaryID = useId();
  const instructionsID = useId();
  const titleRef = useRef<HTMLInputElement>(null);
  const closeRef = useRef(onClose);
  const requestRef = useRef<{ fingerprint: string; key: string } | undefined>(undefined);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const current = detail?.workflow.current_revision;
  useEffect(() => {
    closeRef.current = onClose;
  }, [onClose]);
  useEffect(() => {
    titleRef.current?.focus();
    const close = (event: KeyboardEvent) => event.key === "Escape" && closeRef.current();
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, []);
  const save = useMutation({
    mutationFn: async (input: CreateWorkflowInput) => mode === "create"
      ? api.createWorkflow(input)
      : api.reviseWorkflow({
          id: detail!.workflow.id,
          input: { ...input, expected_revision_id: current!.id },
        }),
    onSuccess: async (next) => {
      await invalidateControlPlane(queryClient);
      onSaved(next);
    },
  });
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const title = String(form.get("title") ?? "").trim();
    const summary = String(form.get("summary") ?? "").trim();
    const instructions = String(form.get("instructions") ?? "");
    const nextErrors: Record<string, string> = {};
    if (!title) nextErrors.title = "Enter a runbook title.";
    else if (Array.from(title).length > 100) nextErrors.title = "Keep the title to 100 characters.";
    if (Array.from(summary).length > 500) nextErrors.summary = "Keep the summary to 500 characters.";
    if (!instructions.trim()) nextErrors.instructions = "Enter runbook instructions.";
    else if (new TextEncoder().encode(instructions).length > 48 * 1024) nextErrors.instructions = "Keep instructions to 48 KiB.";
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length) return;
    const payload = { title, summary, instructions };
    const fingerprint = JSON.stringify(payload);
    if (requestRef.current?.fingerprint !== fingerprint) {
      requestRef.current = { fingerprint, key: crypto.randomUUID() };
    }
    save.mutate({ request_key: requestRef.current.key, ...payload });
  };
  return (
    <div className="modal-layer">
      <button className="modal-scrim" aria-label="Close runbook form" onClick={onClose} />
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="workflow-form-heading">
        <div className="modal-header">
          <h2 id="workflow-form-heading">{mode === "create" ? "Create runbook" : "Create revision"}</h2>
          <button className="icon-button" aria-label="Close" onClick={onClose}><X size={19} /></button>
        </div>
        <form onSubmit={submit} noValidate>
          <div className="modal-body">
            <WorkflowField label="Title" htmlFor={titleID} error={errors.title}>
              <input ref={titleRef} id={titleID} name="title" autoComplete="off" defaultValue={current?.title ?? ""} aria-invalid={Boolean(errors.title)} />
            </WorkflowField>
            <WorkflowField label="Summary" htmlFor={summaryID} error={errors.summary} hint="Optional · 500 characters">
              <textarea id={summaryID} name="summary" rows={2} defaultValue={current?.summary ?? ""} aria-invalid={Boolean(errors.summary)} />
            </WorkflowField>
            <WorkflowField label="Markdown instructions" htmlFor={instructionsID} error={errors.instructions} hint="Required · 48 KiB">
              <textarea id={instructionsID} name="instructions" rows={12} defaultValue={current?.instructions ?? ""} aria-invalid={Boolean(errors.instructions)} />
            </WorkflowField>
            {save.error && <InlineError error={save.error} />}
          </div>
          <div className="modal-footer">
            <button type="button" className="button button-secondary" onClick={onClose}>Cancel</button>
            <button type="submit" className="button button-primary" disabled={save.isPending}>
              {save.isPending ? <><LoaderCircle size={16} className="spin" /> Saving…</> : <><Plus size={16} /> {mode === "create" ? "Create runbook" : "Create revision"}</>}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function WorkflowField({ label, htmlFor, error, hint, children }: {
  label: string;
  htmlFor: string;
  error?: string;
  hint?: string;
  children: ReactNode;
}) {
  return <div className="field"><label htmlFor={htmlFor}>{label}</label>{children}{error
    ? <span className="field-error">{error}</span>
    : hint ? <span className="field-hint">{hint}</span> : null}</div>;
}

function mergeWorkflows(...groups: Workflow[][]): Workflow[] {
  const unique = new Map<string, Workflow>();
  for (const group of groups) {
    for (const workflow of group) {
      if (!unique.has(workflow.id)) unique.set(workflow.id, workflow);
    }
  }
  return [...unique.values()].sort((left, right) => Date.parse(right.updated_at) - Date.parse(left.updated_at));
}
