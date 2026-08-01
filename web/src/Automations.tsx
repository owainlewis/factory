import {
  Activity,
  ArrowLeft,
  Bot,
  ChevronRight,
  CirclePlay,
  FlaskConical,
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
import { useVisibleInterval } from "./polling";
import type {
  Automation,
  AutomationDetail as AutomationDetailType,
  AutomationOccurrence,
  CreateAutomationInput,
  TestAutomationResult,
} from "./types";
import {
  EmptyState,
  ErrorState,
  InlineError,
  LoadingState,
  PanelHeading,
  StaleBanner,
  ViewHeader,
} from "./ui";

export function AutomationsView({ onAutomation }: { onAutomation: (id: string) => void }) {
  const [createOpen, setCreateOpen] = useState(false);
  const [history, setHistory] = useState<Automation[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>();
  const previousHeadCursor = useRef<string | null | undefined>(undefined);
  const interval = useVisibleInterval(5_000);
  const query = useQuery({
    queryKey: ["automations", "head"],
    queryFn: () => api.automations(),
    refetchInterval: interval,
  });
  const loadMore = useMutation({
    mutationFn: ({ cursor }: { cursor: string; headCursor: string | null }) => api.automations(cursor),
    onSuccess: (page, request) => {
      setHistory((current) => mergeAutomations(current, page.automations));
      if (previousHeadCursor.current === request.headCursor) setNextCursor(page.next_cursor);
    },
  });
  useEffect(() => {
    if (!query.data) return;
    const boundaryChanged = previousHeadCursor.current !== query.data.next_cursor;
    setNextCursor((current) => boundaryChanged ? query.data.next_cursor : current);
    previousHeadCursor.current = query.data.next_cursor;
  }, [query.data]);
  const activeCursor = nextCursor === undefined ? query.data?.next_cursor : nextCursor;
  const items = mergeAutomations(query.data?.automations ?? [], history);

  if (query.isPending) return <LoadingState label="Loading Automations" />;
  if (!query.data) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;

  return (
    <div className="page">
      <ViewHeader
        title="Automations"
        fetching={query.isFetching}
        updatedAt={query.dataUpdatedAt}
        onRefresh={() => void query.refetch()}
      />
      {query.error && <StaleBanner error={query.error} />}
      <div className="view-toolbar">
        <p>Typed GitHub issue triggers evaluated by the local control plane.</p>
        <button className="button button-primary" onClick={() => setCreateOpen(true)}>
          <Plus size={15} /> Create Automation
        </button>
      </div>
      {items.length === 0 ? (
        <EmptyState
          icon={<Bot size={22} />}
          title="No Automations yet"
          description="Create a disabled GitHub issue trigger, preview its matches, then enable it."
          action={<button className="button button-primary" onClick={() => setCreateOpen(true)}>Create Automation</button>}
        />
      ) : (
        <div className="workflow-list automation-list">
          <div className="automation-table-head">
            <span>Name and trigger</span><span>Health</span><span>Counters</span><span>Schedule</span><span>Latest task</span><span />
          </div>
          {items.map((automation) => (
            <button className="automation-row" key={automation.id} onClick={() => onAutomation(automation.id)}>
              <span className="workflow-identity">
                <strong>{automation.name}</strong>
                <small>{automation.repository_identity} · {triggerSummary(automation)}</small>
              </span>
              <span className="automation-list-health"><HealthBadge automation={automation} /><small>{automation.health.message || "No health detail."}</small></span>
              <span className="automation-list-copy"><strong>{automation.matched_count} matched</strong><small>{automation.skipped_count} reused · {automation.dispatched_count} dispatched</small></span>
              <span className="automation-list-copy"><strong>{formatTimestamp(automation.last_checked_at)}</strong><small>Next {formatTimestamp(automation.next_check_at)}</small></span>
              <span className="automation-list-copy"><strong>{automation.latest_task?.title || "No task yet"}</strong><small>{automation.latest_task?.state || "Waiting for a match"}</small></span>
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
            onClick={() => loadMore.mutate({ cursor: activeCursor, headCursor: previousHeadCursor.current ?? null })}
          >
            {loadMore.isPending ? "Loading…" : "Load more Automations"}
          </button>
        </div>
      )}
      {loadMore.error && <InlineError error={loadMore.error} />}
      {createOpen && (
        <AutomationForm
          mode="create"
          onClose={() => setCreateOpen(false)}
          onSaved={(detail) => {
            setCreateOpen(false);
            onAutomation(detail.automation.id);
          }}
        />
      )}
    </div>
  );
}

export function AutomationDetail({
  id,
  onBack,
  onTask,
}: {
  id: string;
  onBack: () => void;
  onTask: (id: string) => void;
}) {
  const queryClient = useQueryClient();
  const interval = useVisibleInterval(3_000);
  const [editing, setEditing] = useState(false);
  const [confirmEnabled, setConfirmEnabled] = useState<boolean>();
  const [pollerConfirmed, setPollerConfirmed] = useState(false);
  const [preview, setPreview] = useState<TestAutomationResult>();
  const [occurrenceHistory, setOccurrenceHistory] = useState<AutomationOccurrence[]>([]);
  const [nextOccurrenceCursor, setNextOccurrenceCursor] = useState<string | null>();
  const previousOccurrenceHeadCursor = useRef<string | null | undefined>(undefined);
  const detail = useQuery({
    queryKey: ["automation", id],
    queryFn: () => api.automation(id),
    refetchInterval: interval,
  });
  const occurrences = useQuery({
    queryKey: ["automations", id, "occurrences", "head"],
    queryFn: () => api.automationOccurrences(id),
    refetchInterval: interval,
  });
  const loadMoreOccurrences = useMutation({
    mutationFn: ({ cursor }: { cursor: string; headCursor: string | null }) => api.automationOccurrences(id, cursor),
    onSuccess: (page, request) => {
      setOccurrenceHistory((current) => mergeOccurrences(current, page.occurrences));
      if (previousOccurrenceHeadCursor.current === request.headCursor) setNextOccurrenceCursor(page.next_cursor);
    },
  });
  useEffect(() => {
    if (!occurrences.data) return;
    const boundaryChanged = previousOccurrenceHeadCursor.current !== occurrences.data.next_cursor;
    setNextOccurrenceCursor((current) => boundaryChanged ? occurrences.data.next_cursor : current);
    previousOccurrenceHeadCursor.current = occurrences.data.next_cursor;
  }, [occurrences.data]);
  const setEnabled = useMutation({
    mutationFn: (enabled: boolean) => api.setAutomationEnabled({ id, enabled }),
    onSuccess: async (next) => {
      queryClient.setQueryData(["automation", id], next);
      setConfirmEnabled(undefined);
      setPollerConfirmed(false);
      await invalidateControlPlane(queryClient);
    },
  });
  const test = useMutation({
    mutationFn: () => api.testAutomation(id),
    onSuccess: setPreview,
  });
  const check = useMutation({
    mutationFn: () => api.checkAutomation(id),
    onSuccess: async (next) => {
      queryClient.setQueryData(["automation", id], next);
      await invalidateControlPlane(queryClient);
    },
  });

  if (detail.isPending) return <LoadingState label="Loading Automation" />;
  if (!detail.data) return <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />;
  const data = detail.data;
  const automation = data.automation;
  const occurrenceItems = mergeOccurrences(occurrences.data?.occurrences ?? data.occurrences, occurrenceHistory);
  const activeOccurrenceCursor = nextOccurrenceCursor === undefined
    ? occurrences.data?.next_cursor
    : nextOccurrenceCursor;

  return (
    <div className="page detail-page">
      <button className="back-button" onClick={onBack}><ArrowLeft size={16} /> All Automations</button>
      <div className="detail-heading">
        <div>
          <HealthBadge automation={automation} />
          <h1>{automation.name}</h1>
          <p>{triggerSummary(automation)} · {automation.repository_identity}</p>
        </div>
        <div className="detail-actions">
          <button className="button button-secondary" onClick={() => test.mutate()} disabled={test.isPending}>
            {test.isPending ? <LoaderCircle size={14} className="spin" /> : <FlaskConical size={14} />} Test trigger
          </button>
          <button className="button button-secondary" onClick={() => check.mutate()} disabled={!automation.enabled || check.isPending}>
            <CirclePlay size={14} /> Run now
          </button>
          <button className="button button-secondary" onClick={() => setEditing(true)} disabled={automation.enabled}>
            <Pencil size={14} /> Edit
          </button>
          <button
            className={automation.enabled ? "button button-danger-secondary" : "button button-primary"}
            onClick={() => setConfirmEnabled(!automation.enabled)}
          >
            {automation.enabled ? <PowerOff size={14} /> : <Power size={14} />}
            {automation.enabled ? "Disable" : "Enable"}
          </button>
        </div>
      </div>
      {detail.error && <StaleBanner error={detail.error} />}
      {setEnabled.error && <InlineError error={setEnabled.error} />}
      {test.error && <InlineError error={test.error} />}
      {check.error && <InlineError error={check.error} />}
      {confirmEnabled !== undefined && (
        <div className="confirm-action automation-confirm" role="alert">
          <div>
            <strong>{confirmEnabled ? "Enable this Automation?" : "Disable this Automation?"}</strong>
            <p>{confirmEnabled
              ? `${automation.workflow_name} · ${automation.repository_identity} · ${triggerSummary(automation)}`
              : "Future checks and pending dispatches stop. Existing tasks continue."}</p>
            {confirmEnabled && (
              <label className="confirmation-check">
                <input
                  type="checkbox"
                  checked={pollerConfirmed}
                  onChange={(event) => setPollerConfirmed(event.target.checked)}
                />
                I confirm factory-poller is stopped for any equivalent queue.
              </label>
            )}
          </div>
          <button
            className={confirmEnabled ? "button button-primary" : "button button-danger"}
            disabled={setEnabled.isPending || (confirmEnabled && !pollerConfirmed)}
            onClick={() => setEnabled.mutate(confirmEnabled)}
          >
            {setEnabled.isPending ? "Saving…" : `Confirm ${confirmEnabled ? "enable" : "disable"}`}
          </button>
          <button className="button button-secondary" onClick={() => setConfirmEnabled(undefined)}>Cancel</button>
        </div>
      )}

      <div className="automation-health-card panel">
        <PanelHeading title="Control-plane health" aside={automation.health.status} />
        <p className={automation.health.status === "error" || automation.health.status === "blocked" ? "health-error" : ""}>
          {automation.health.message || "No health detail yet."}
          {automation.health.code && <span className="mono"> · {automation.health.code}</span>}
        </p>
        <div className="automation-metrics">
          <Metric label="Matched" value={automation.matched_count} />
          <Metric label="Reused" value={automation.skipped_count} />
          <Metric label="Dispatched" value={automation.dispatched_count} />
          <Metric label="Last checked" value={formatTimestamp(automation.last_checked_at)} />
          <Metric label="Next check" value={formatTimestamp(automation.next_check_at)} />
        </div>
        <div className="automation-latest-task">
          <span><strong>Latest task</strong><small>{automation.latest_task ? `${automation.latest_task.title} · ${automation.latest_task.state}` : "No task has been dispatched."}</small></span>
          {automation.latest_task && <button className="button button-secondary" onClick={() => onTask(automation.latest_task!.id)}>Open latest task</button>}
        </div>
      </div>

      {preview && (
        <section className="panel preview-panel" aria-live="polite">
          <PanelHeading title="Test results" aside={`${preview.matches.length} bounded match${preview.matches.length === 1 ? "" : "es"}`} />
          <p className="muted">Testing creates no task or durable occurrence.</p>
          {preview.matches.length === 0 ? <p>No issues matched.</p> : preview.matches.map((match) => (
            <a key={match.number} href={match.url} target="_blank" rel="noreferrer" className="preview-match">
              <strong>#{match.number} {match.title}</strong>
              <span>{match.state} · {match.labels.join(", ") || "no labels"}</span>
            </a>
          ))}
        </section>
      )}

      <div className="detail-grid">
        <section className="panel detail-main">
          <PanelHeading title="Configuration" aside={`Version ${automation.version}`} />
          <dl className="metadata">
            <div><dt>Workflow</dt><dd>{automation.workflow_name} · revision {automation.workflow_revision}</dd></div>
            <div><dt>Repository</dt><dd className="mono">{automation.repository_identity}</dd></div>
            <div><dt>Issue state</dt><dd>{automation.trigger.state}</dd></div>
            <div><dt>Required labels</dt><dd>{automation.trigger.required_labels.join(", ") || "None"}</dd></div>
            <div><dt>Polling</dt><dd>Every {automation.trigger.poll_interval_seconds} seconds</dd></div>
            <div><dt>Timeout</dt><dd>{automation.timeout_seconds} seconds</dd></div>
          </dl>
        </section>
        <section className="panel">
          <PanelHeading title="Trusted context" />
          <div className="long-copy">{automation.context || "No additional context."}</div>
        </section>
      </div>

      <section className="panel">
        <PanelHeading title="Occurrences" aside={`${occurrenceItems.length} loaded`} />
        {occurrenceItems.length === 0 ? (
          <p className="muted">No durable occurrences yet.</p>
        ) : (
          <div className="occurrence-list">
            {occurrenceItems.map((occurrence) => (
              <div className="occurrence-row" key={occurrence.id}>
                <span className={`status-badge status-${occurrence.state}`}><span className="status-dot" />{occurrence.state}</span>
                <span className="occurrence-identity">
                  <strong>#{occurrence.issue_number} {occurrence.issue_title}</strong>
                  <small>{formatTimestamp(occurrence.created_at)}{occurrence.diagnostic ? ` · ${occurrence.diagnostic}` : ""}</small>
                </span>
                {occurrence.task ? (
                  <button className="button button-secondary" onClick={() => onTask(occurrence.task!.id)}>
                    Open task
                  </button>
                ) : occurrence.state === "task_deleted" ? (
                  <span className="muted">Task deleted</span>
                ) : null}
              </div>
            ))}
          </div>
        )}
        {activeOccurrenceCursor && (
          <div className="load-more">
            <button
              className="button button-secondary"
              disabled={loadMoreOccurrences.isPending}
              onClick={() => loadMoreOccurrences.mutate({
                cursor: activeOccurrenceCursor,
                headCursor: previousOccurrenceHeadCursor.current ?? null,
              })}
            >
              {loadMoreOccurrences.isPending ? "Loading…" : "Load more occurrences"}
            </button>
          </div>
        )}
        {(occurrences.error || loadMoreOccurrences.error) && <InlineError error={(occurrences.error || loadMoreOccurrences.error) as Error} />}
      </section>
      {editing && (
        <AutomationForm
          mode="edit"
          detail={data}
          onClose={() => setEditing(false)}
          onSaved={(next) => {
            queryClient.setQueryData(["automation", id], next);
            setEditing(false);
            void invalidateControlPlane(queryClient);
          }}
        />
      )}
    </div>
  );
}

function AutomationForm({
  mode,
  detail,
  onClose,
  onSaved,
}: {
  mode: "create" | "edit";
  detail?: AutomationDetailType;
  onClose: () => void;
  onSaved: (detail: AutomationDetailType) => void;
}) {
  const queryClient = useQueryClient();
  const nameID = useId();
  const workflowID = useId();
  const repositoryID = useId();
  const stateID = useId();
  const labelsID = useId();
  const intervalID = useId();
  const timeoutID = useId();
  const contextID = useId();
  const nameRef = useRef<HTMLInputElement>(null);
  const closeRef = useRef(onClose);
  const requestRef = useRef<{ fingerprint: string; key: string } | undefined>(undefined);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const workflows = useQuery({ queryKey: ["workflows", "automation-form"], queryFn: () => api.workflows() });
  const repositories = useQuery({ queryKey: ["repositories"], queryFn: api.repositories });
  const current = detail?.automation;
  useEffect(() => {
    closeRef.current = onClose;
  }, [onClose]);
  useEffect(() => {
    nameRef.current?.focus();
    const close = (event: KeyboardEvent) => event.key === "Escape" && closeRef.current();
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, []);
  const save = useMutation({
    mutationFn: async (input: CreateAutomationInput) => mode === "create"
      ? api.createAutomation(input)
      : api.updateAutomation({
          id: current!.id,
          input: {
            expected_version: current!.version,
            name: input.name,
            workflow_id: input.workflow_id,
            context: input.context,
            timeout_seconds: input.timeout_seconds,
            trigger: input.trigger,
          },
        }),
    onSuccess: async (next) => {
      await invalidateControlPlane(queryClient);
      onSaved(next);
    },
  });
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const name = String(form.get("name") ?? "").trim();
    const workflow = String(form.get("workflow_id") ?? "");
    const repository = current?.repository_id ?? String(form.get("repository_id") ?? "");
    const context = String(form.get("context") ?? "");
    const timeout = Number(form.get("timeout_seconds"));
    const pollInterval = Number(form.get("poll_interval_seconds"));
    const labels = String(form.get("required_labels") ?? "").split(",").map((label) => label.trim()).filter(Boolean);
    const nextErrors: Record<string, string> = {};
    if (!name) nextErrors.name = "Enter an Automation name.";
    else if (Array.from(name).length > 100) nextErrors.name = "Keep the name to 100 characters.";
    if (!workflow) nextErrors.workflow = "Choose a Workflow.";
    if (!repository) nextErrors.repository = "Choose a repository.";
    if (labels.length > 20 || labels.some((label) => new TextEncoder().encode(label).length > 200)) nextErrors.labels = "Use at most 20 labels of 200 bytes each.";
    if (!Number.isInteger(pollInterval) || pollInterval < 10 || pollInterval > 86_400) nextErrors.interval = "Use 10 to 86,400 seconds.";
    if (!Number.isInteger(timeout) || timeout < 1 || timeout > 28_800) nextErrors.timeout = "Use 1 to 28,800 seconds.";
    if (new TextEncoder().encode(context).length > 8 * 1024) nextErrors.context = "Keep context to 8 KiB.";
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length) return;
    const payload = {
      name,
      workflow_id: workflow,
      repository_id: repository,
      context,
      timeout_seconds: timeout,
      trigger: {
        type: "github_issue" as const,
        state: String(form.get("state")) as "open" | "closed",
        required_labels: labels,
        poll_interval_seconds: pollInterval,
      },
    };
    const fingerprint = JSON.stringify(payload);
    if (requestRef.current?.fingerprint !== fingerprint) {
      requestRef.current = { fingerprint, key: crypto.randomUUID() };
    }
    save.mutate({ request_key: requestRef.current.key, ...payload });
  };
  const workflowItems = workflows.data?.workflows ?? [];
  const repositoryItems = repositories.data ?? [];
  return (
    <div className="modal-layer">
      <button className="modal-scrim" aria-label="Close Automation form" onClick={onClose} />
      <div className="modal automation-modal" role="dialog" aria-modal="true" aria-labelledby="automation-form-heading">
        <div className="modal-header">
          <h2 id="automation-form-heading">{mode === "create" ? "Create Automation" : "Edit Automation"}</h2>
          <button className="icon-button" aria-label="Close" onClick={onClose}><X size={19} /></button>
        </div>
        <form onSubmit={submit} noValidate>
          <div className="modal-body automation-form-grid">
            <Field label="Name" htmlFor={nameID} error={errors.name}>
              <input ref={nameRef} id={nameID} name="name" defaultValue={current?.name ?? ""} aria-invalid={Boolean(errors.name)} />
            </Field>
            <Field label="Workflow" htmlFor={workflowID} error={errors.workflow}>
              <select id={workflowID} name="workflow_id" defaultValue={current?.workflow_id ?? ""} aria-invalid={Boolean(errors.workflow)}>
                <option value="">Choose a Workflow</option>
                {workflowItems.map((workflow) => <option key={workflow.id} value={workflow.id}>{workflow.current_revision.name}{workflow.enabled ? "" : " (disabled)"}</option>)}
              </select>
            </Field>
            <Field label="Managed repository" htmlFor={repositoryID} error={errors.repository} hint={mode === "edit" ? "Repository identity is immutable." : undefined}>
              <select id={repositoryID} name="repository_id" defaultValue={current?.repository_id ?? ""} disabled={mode === "edit"} aria-invalid={Boolean(errors.repository)}>
                <option value="">Choose a repository</option>
                {repositoryItems.map((repository) => <option key={repository.id} value={repository.id}>{repository.remote_identity}{repository.enabled ? "" : " (disabled)"}</option>)}
              </select>
            </Field>
            <Field label="Issue state" htmlFor={stateID}>
              <select id={stateID} name="state" defaultValue={current?.trigger.state ?? "open"}><option value="open">Open</option><option value="closed">Closed</option></select>
            </Field>
            <Field label="Required labels" htmlFor={labelsID} error={errors.labels} hint="Comma separated · up to 20">
              <input id={labelsID} name="required_labels" defaultValue={current?.trigger.required_labels.join(", ") ?? "factory:ready"} aria-invalid={Boolean(errors.labels)} />
            </Field>
            <Field label="Poll interval (seconds)" htmlFor={intervalID} error={errors.interval}>
              <input id={intervalID} name="poll_interval_seconds" type="number" min={10} max={86_400} defaultValue={current?.trigger.poll_interval_seconds ?? 30} aria-invalid={Boolean(errors.interval)} />
            </Field>
            <Field label="Task timeout (seconds)" htmlFor={timeoutID} error={errors.timeout}>
              <input id={timeoutID} name="timeout_seconds" type="number" min={1} max={28_800} defaultValue={current?.timeout_seconds ?? 7200} aria-invalid={Boolean(errors.timeout)} />
            </Field>
            <Field label="Trusted Automation context" htmlFor={contextID} error={errors.context} hint="Optional · 8 KiB">
              <textarea id={contextID} name="context" rows={5} defaultValue={current?.context ?? ""} aria-invalid={Boolean(errors.context)} />
            </Field>
            {(workflows.error || repositories.error || save.error) && <InlineError error={(workflows.error || repositories.error || save.error) as Error} />}
          </div>
          <div className="modal-footer">
            <span className="disabled-first-note"><Activity size={14} /> New Automations are disabled.</span>
            <button type="button" className="button button-secondary" onClick={onClose}>Cancel</button>
            <button type="submit" className="button button-primary" disabled={save.isPending || workflows.isPending || repositories.isPending}>
              {save.isPending ? <><LoaderCircle size={16} className="spin" /> Saving…</> : <><Plus size={16} /> {mode === "create" ? "Create Automation" : "Save changes"}</>}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function Field({ label, htmlFor, error, hint, children }: { label: string; htmlFor: string; error?: string; hint?: string; children: ReactNode }) {
  return <div className="field"><label htmlFor={htmlFor}>{label}</label>{children}{error
    ? <span className="field-error">{error}</span>
    : hint ? <span className="field-hint">{hint}</span> : null}</div>;
}

function HealthBadge({ automation }: { automation: Automation }) {
  const status = automation.enabled ? automation.health.status : "disabled";
  return <span className={`status-badge automation-health status-${status}`}><span className="status-dot" />{status}</span>;
}

function Metric({ label, value }: { label: string; value: string | number }) {
  return <div><span>{label}</span><strong>{value}</strong></div>;
}

function triggerSummary(automation: Automation): string {
  const labels = automation.trigger.required_labels.length
    ? ` · labels ${automation.trigger.required_labels.join(", ")}`
    : "";
  return `GitHub issues · ${automation.trigger.state}${labels}`;
}

function formatTimestamp(value?: string): string {
  return value ? new Date(value).toLocaleString() : "Never";
}

function mergeAutomations(...groups: Automation[][]): Automation[] {
  const merged = new Map<string, Automation>();
  for (const automation of groups.flat()) {
    if (!merged.has(automation.id)) merged.set(automation.id, automation);
  }
  return [...merged.values()].sort((left, right) =>
    right.updated_at.localeCompare(left.updated_at) || right.id.localeCompare(left.id));
}

function mergeOccurrences(...groups: AutomationOccurrence[][]): AutomationOccurrence[] {
  const merged = new Map<string, AutomationOccurrence>();
  for (const occurrence of groups.flat()) {
    if (!merged.has(occurrence.id)) merged.set(occurrence.id, occurrence);
  }
  return [...merged.values()].sort((left, right) =>
    right.created_at.localeCompare(left.created_at) || right.id.localeCompare(left.id));
}
