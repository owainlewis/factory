import {
  Activity,
  ArrowLeft,
  Bot,
  ChevronRight,
  CirclePlay,
  DatabaseBackup,
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
  AutomationTrigger,
  CreateAutomationInput,
  LegacyPollerMigration,
  LegacyPollerSelection,
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
  const [migrationOpen, setMigrationOpen] = useState(false);
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
        <p>Typed GitHub issue, pull-request, and schedule triggers evaluated by the local control plane.</p>
        <div className="detail-actions">
          <button className="button button-secondary" onClick={() => setMigrationOpen(true)}>
            <DatabaseBackup size={15} /> Migrate legacy poller
          </button>
          <button className="button button-primary" onClick={() => setCreateOpen(true)}>
            <Plus size={15} /> Create Automation
          </button>
        </div>
      </div>
      {items.length === 0 ? (
        <EmptyState
          icon={<Bot size={22} />}
          title="No Automations yet"
          description="Create a disabled typed trigger, preview it, then enable it."
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
              <span className="automation-list-copy"><strong>{formatTimestamp(automation.last_checked_at)}</strong><small>{automation.trigger.type === "schedule" ? "Next due" : "Next check"} {formatTimestamp(automation.next_due_at ?? automation.next_check_at)}</small></span>
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
      {migrationOpen && (
        <LegacyPollerMigrationDialog
          onClose={() => setMigrationOpen(false)}
          onAutomation={onAutomation}
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
  const [preview, setPreview] = useState<TestAutomationResult>();
  const [occurrenceHistory, setOccurrenceHistory] = useState<AutomationOccurrence[]>([]);
  const [nextOccurrenceCursor, setNextOccurrenceCursor] = useState<string | null>();
  const previousOccurrenceHeadCursor = useRef<string | null | undefined>(undefined);
  const runRequest = useRef<{ automationID: string; key: string } | undefined>(undefined);
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
  const run = useMutation({
    mutationFn: () => {
      if (!runRequest.current || runRequest.current.automationID !== id) {
        runRequest.current = { automationID: id, key: crypto.randomUUID() };
      }
      return api.runAutomation(id, runRequest.current.key);
    },
    onSuccess: async (next) => {
      runRequest.current = undefined;
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
          {automation.trigger.type === "schedule" ? (
            <button className="button button-secondary" onClick={() => run.mutate()} disabled={!automation.enabled || run.isPending}>
              <CirclePlay size={14} /> Run now
            </button>
          ) : (
            <button className="button button-secondary" onClick={() => check.mutate()} disabled={!automation.enabled || check.isPending}>
              <CirclePlay size={14} /> Check now
            </button>
          )}
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
      {run.error && <InlineError error={run.error} />}
      {confirmEnabled !== undefined && (
        <div className="confirm-action automation-confirm" role="alert">
          <div>
            <strong>{confirmEnabled ? "Enable this Automation?" : "Disable this Automation?"}</strong>
            <p>{confirmEnabled
              ? `${automation.workflow_name} · ${automation.repository_identity} · ${triggerSummary(automation)}`
              : "Future checks and pending dispatches stop. Existing tasks continue."}</p>
          </div>
          <button
            className={confirmEnabled ? "button button-primary" : "button button-danger"}
            disabled={setEnabled.isPending}
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
          <Metric label={automation.trigger.type === "schedule" ? "Last occurrence" : "Last checked"} value={formatTimestamp(automation.last_checked_at)} />
          <Metric label={automation.trigger.type === "schedule" ? "Next due" : "Next check"} value={formatTimestamp(automation.next_due_at ?? automation.next_check_at)} />
        </div>
        <div className="automation-latest-task">
          <span><strong>Latest task</strong><small>{automation.latest_task ? `${automation.latest_task.title} · ${automation.latest_task.state}` : "No task has been dispatched."}</small></span>
          {automation.latest_task && <button className="button button-secondary" onClick={() => onTask(automation.latest_task!.id)}>Open latest task</button>}
        </div>
      </div>

      {preview && (
        <section className="panel preview-panel" aria-live="polite">
          <PanelHeading title="Test results" aside={preview.next_due_at ? `Next due ${formatTimestamp(preview.next_due_at)}` : `${preview.matches.length} bounded match${preview.matches.length === 1 ? "" : "es"}`} />
          <p className="muted">Testing creates no task or durable occurrence.</p>
          {preview.next_due_at ? <p>The next matching UTC instant is <strong>{new Date(preview.next_due_at).toISOString()}</strong>.</p> : preview.matches.length === 0 ? <p>No GitHub items matched.</p> : preview.matches.map((match) => (
            <a key={match.number} href={match.url} target="_blank" rel="noreferrer" className="preview-match">
              <strong>#{match.number} {match.title}</strong>
              <span>{match.state}{match.base_branch ? ` · base ${match.base_branch}` : ""}{match.is_draft ? " · draft" : ""} · {match.labels.join(", ") || "no labels"}</span>
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
            {automation.trigger.type === "schedule" ? <>
              <div><dt>Cron</dt><dd className="mono">{automation.trigger.cron}</dd></div>
              <div><dt>Timezone</dt><dd>{automation.trigger.timezone}</dd></div>
              <div><dt>Next due UTC</dt><dd>{automation.next_due_at ? new Date(automation.next_due_at).toISOString() : "Disabled"}</dd></div>
            </> : <>
              <div><dt>{automation.trigger.type === "github_pull_request" ? "Pull request state" : "Issue state"}</dt><dd>{automation.trigger.state}</dd></div>
            {automation.trigger.type === "github_pull_request" && <>
              <div><dt>Drafts</dt><dd>{automation.trigger.include_drafts ? "Included" : "Excluded"}</dd></div>
              <div><dt>Base branches</dt><dd>{automation.trigger.base_branches.join(", ") || "Any"}</dd></div>
            </>}
            <div><dt>Required labels</dt><dd>{automation.trigger.required_labels.join(", ") || "None"}</dd></div>
            <div><dt>Polling</dt><dd>Every {automation.trigger.poll_interval_seconds} seconds</dd></div>
            </>}
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
                  <strong>{occurrenceIdentity(occurrence)}</strong>
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

function LegacyPollerMigrationDialog({
  onClose,
  onAutomation,
}: {
  onClose: () => void;
  onAutomation: (id: string) => void;
}) {
  const queryClient = useQueryClient();
  const [selection, setSelection] = useState<LegacyPollerSelection>({ confirm_stopped: false });
  const [migrationState, setMigration] = useState<LegacyPollerMigration>();
  const [reviewQueues, setReviewQueues] = useState<LegacyPollerMigration["queues"]>([]);
  const activeMigration = useQuery({
    queryKey: ["legacy-poller-migration", "active"],
    queryFn: api.activeLegacyPollerMigration,
  });
  const migration = migrationState ?? activeMigration.data?.migration ?? undefined;
  const actionSelection = migration?.status === "imported" ? {
    config_path: migration.config_path,
    data_home: migration.data_home,
    working_directory: migration.working_directory,
    confirm_stopped: selection.confirm_stopped,
  } : selection;
  const preview = useMutation({
    mutationFn: () => api.previewLegacyPoller(selection),
    onSuccess: (result) => {
      setMigration(result);
      setReviewQueues(result.queues);
    },
  });
  const importMigration = useMutation({
    mutationFn: (mappings: Array<{ queue_id: string; workflow_name: string; automation_name: string }>) =>
      api.importLegacyPoller({ migration: migration!, selection, mappings }),
    onSuccess: async (result) => {
      setMigration(result);
      await invalidateControlPlane(queryClient);
    },
  });
  const resolveOccurrence = useMutation({
    mutationFn: ({ id, action }: { id: string; action: "resume" | "skip" }) =>
      action === "resume" ? api.resumeLegacyOccurrence(id) : api.skipLegacyOccurrence(id),
    onSuccess: async () => {
      setMigration(await api.legacyPollerMigration(migration!.id));
      await invalidateControlPlane(queryClient);
    },
  });
  const finalize = useMutation({
    mutationFn: () => api.finalizeLegacyPoller({ migration: migration!, selection: actionSelection }),
    onSuccess: async (result) => {
      setMigration(result);
      await invalidateControlPlane(queryClient);
    },
  });
  const unresolved = migration?.occurrences.filter((occurrence) =>
    occurrence.state === "pending" || occurrence.state === "dispatching" || occurrence.state === "failed") ?? [];
  const operationError = activeMigration.error || preview.error || importMigration.error || resolveOccurrence.error || finalize.error;
  const setPath = (field: "config_path" | "data_home" | "working_directory", value: string) =>
    setSelection((current) => ({ ...current, [field]: value || undefined }));
  const submitImport = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const mappings = reviewQueues.filter((queue) => queue.supported).map((queue) => ({
      queue_id: queue.queue_id,
      workflow_name: String(form.get(`workflow-${queue.queue_id}`) ?? "").trim(),
      automation_name: String(form.get(`automation-${queue.queue_id}`) ?? "").trim(),
    }));
    importMigration.mutate(mappings);
  };

  return (
    <div className="modal-layer">
      <button className="modal-scrim" aria-label="Close legacy poller migration" onClick={onClose} />
      <div className="modal migration-modal" role="dialog" aria-modal="true" aria-labelledby="legacy-migration-heading">
        <div className="modal-header">
          <div>
            <h2 id="legacy-migration-heading">Migrate legacy poller</h2>
            <p className="muted">Stop poller → Preview → Import disabled → Resume or Skip pending → Finalize → Enable</p>
          </div>
          <button className="icon-button" aria-label="Close" onClick={onClose}><X size={19} /></button>
        </div>
        <div className="modal-body migration-body">
          {!migration && activeMigration.isPending && <LoadingState label="Checking for an active migration" />}
          {!migration && !activeMigration.isPending && (
            <div className="migration-grid">
              <Field label="Legacy poller.toml" htmlFor="migration-config" hint="Optional absolute override. Default: ~/.factory/poller.toml">
                <input id="migration-config" value={selection.config_path ?? ""} onChange={(event) => setPath("config_path", event.target.value)} placeholder="/absolute/path/poller.toml" />
              </Field>
              <Field label="Legacy data home" htmlFor="migration-home" hint="Optional absolute override. Default: FACTORY_DATA_HOME or ~/.factory">
                <input id="migration-home" value={selection.data_home ?? ""} onChange={(event) => setPath("data_home", event.target.value)} placeholder="/absolute/path/.factory" />
              </Field>
              <Field label="Original working directory" htmlFor="migration-cwd" hint="Needed only when the old environment selected a relative path.">
                <input id="migration-cwd" value={selection.working_directory ?? ""} onChange={(event) => setPath("working_directory", event.target.value)} placeholder="/absolute/path" />
              </Field>
              <label className="confirmation-check migration-confirm">
                <input
                  type="checkbox"
                  checked={selection.confirm_stopped}
                  onChange={(event) => setSelection((current) => ({ ...current, confirm_stopped: event.target.checked }))}
                />
                I stopped every factory-poller process. Factory may hold an exclusive lock during each migration action.
              </label>
              <button className="button button-primary" disabled={!selection.confirm_stopped || preview.isPending} onClick={() => preview.mutate()}>
                {preview.isPending ? <LoaderCircle size={15} className="spin" /> : <DatabaseBackup size={15} />} Preview locked snapshot
              </button>
            </div>
          )}

          {migration && (
            <>
              <section className="migration-summary">
                <div><span>Status</span><strong>{migration.status}</strong></div>
                <div><span>Queues</span><strong>{migration.counts.supported_queues} supported · {migration.counts.unsupported_queues} unsupported</strong></div>
                <div><span>Observations</span><strong>{migration.counts.submitted_observations} submitted · {migration.counts.pending_observations} pending</strong></div>
                <div><span>Snapshot</span><strong className="mono">{migration.snapshot_digest.slice(0, 16)}…</strong></div>
              </section>
              <dl className="migration-paths">
                <div><dt>Config</dt><dd className="mono">{migration.config_path}</dd></div>
                <div><dt>Data home</dt><dd className="mono">{migration.data_home}</dd></div>
                <div><dt>Original working directory</dt><dd className="mono">{migration.working_directory}</dd></div>
                <div><dt>Legacy data directory</dt><dd className="mono">{migration.data_directory}</dd></div>
                <div><dt>Ledger</dt><dd className="mono">{migration.ledger_path}</dd></div>
                <div><dt>Archive root</dt><dd className="mono">{migration.archive_root}</dd></div>
              </dl>
            </>
          )}

          {migration?.status === "previewed" && (
            <form onSubmit={submitImport}>
              <div className="migration-queue-list">
                {reviewQueues.map((queue) => (
                  <section className="panel migration-queue" key={queue.queue_id}>
                    <PanelHeading title={queue.name} aside={queue.supported ? "GitHub issue" : "Not imported"} />
                    <p>{queue.project} · {queue.state} · labels {queue.required_labels.join(", ") || "none"}</p>
                    {queue.supported && <p className="muted">Repository mapping: <span className="mono">{queue.repository_identity}</span> · <span className="mono">{queue.repository_id}</span></p>}
                    <p className="muted">{queue.submitted_observations} submitted · {queue.pending_observations} pending · every {queue.poll_interval_seconds}s</p>
                    {queue.errors.map((message) => <p className="field-error" key={message}>{message}</p>)}
                    {queue.supported && (
                      <div className="migration-name-grid">
                        <Field label="Workflow name" htmlFor={`workflow-${queue.queue_id}`}>
                          <input id={`workflow-${queue.queue_id}`} name={`workflow-${queue.queue_id}`} defaultValue={queue.workflow_name} required />
                        </Field>
                        <Field label="Automation name" htmlFor={`automation-${queue.queue_id}`}>
                          <input id={`automation-${queue.queue_id}`} name={`automation-${queue.queue_id}`} defaultValue={queue.automation_name} required />
                        </Field>
                      </div>
                    )}
                  </section>
                ))}
              </div>
              <div className="migration-actions">
                <button type="button" className="button button-secondary" onClick={() => { setMigration(undefined); setReviewQueues([]); }}>Run a new Preview</button>
                <button type="submit" className="button button-primary" disabled={importMigration.isPending}>
                  {importMigration.isPending ? "Importing…" : migration.counts.supported_queues === 0 ? "Continue to archive" : "Import disabled Automations"}
                </button>
              </div>
            </form>
          )}

          {migration && migration.status !== "previewed" && (
            <>
              {migration.status === "imported" && (
                <label className="confirmation-check migration-confirm">
                  <input
                    type="checkbox"
                    checked={selection.confirm_stopped}
                    onChange={(event) => setSelection((current) => ({ ...current, confirm_stopped: event.target.checked }))}
                  />
                  I reconfirmed every factory-poller process is stopped before continuing.
                </label>
              )}
              {migration.occurrences.length > 0 && (
                <section className="panel">
                  <PanelHeading title="Imported observations" aside={`${unresolved.length} unresolved`} />
                  <div className="occurrence-list">
                    {migration.occurrences.map((occurrence) => (
                      <div className="occurrence-row" key={occurrence.id}>
                        <span className={`status-badge status-${occurrence.state}`}><span className="status-dot" />{occurrence.state}</span>
                        <span className="occurrence-identity">
                          <strong>{occurrenceIdentity(occurrence)}</strong>
                          <small>{occurrence.diagnostic || occurrence.task_request_key}</small>
                        </span>
                        {(occurrence.state === "pending" || occurrence.state === "failed") && (
                          <div className="detail-actions">
                            {occurrence.state === "pending" && <button className="button button-secondary" disabled={!selection.confirm_stopped || resolveOccurrence.isPending} onClick={() => resolveOccurrence.mutate({ id: occurrence.id, action: "resume" })}>Resume</button>}
                            <button className="button button-danger-secondary" disabled={!selection.confirm_stopped || resolveOccurrence.isPending} onClick={() => resolveOccurrence.mutate({ id: occurrence.id, action: "skip" })}>Skip</button>
                          </div>
                        )}
                      </div>
                    ))}
                  </div>
                </section>
              )}
              {migration.status === "imported" && (
                <div className="migration-actions">
                  <p className="muted">Finalize verifies the same locked snapshot and archives copies. It never deletes the source files.</p>
                  <button className="button button-primary" disabled={!selection.confirm_stopped || unresolved.length > 0 || finalize.isPending} onClick={() => finalize.mutate()}>
                    {finalize.isPending ? "Finalizing…" : "Finalize and archive"}
                  </button>
                </div>
              )}
              {migration.status === "finalized" && (
                <section className="panel migration-complete">
                  <PanelHeading title="Migration finalized" aside="Ready for review" />
                  <p>Archive: <span className="mono">{migration.archive_path}</span></p>
                  <p className="muted">Review each imported Automation, test its trigger, then enable it. The retired poller must remain stopped.</p>
                  <div className="detail-actions">
                    {migration.automations.map((automation) => (
                      <button className="button button-secondary" key={automation.id} onClick={() => onAutomation(automation.id)}>
                        Review {automation.name}
                      </button>
                    ))}
                  </div>
                </section>
              )}
            </>
          )}
          {operationError && <InlineError error={operationError as Error} />}
        </div>
        <div className="modal-footer">
          <span className="disabled-first-note"><Activity size={14} /> Imported Automations stay disabled until Finalize.</span>
          <button className="button button-secondary" onClick={onClose}>Close</button>
        </div>
      </div>
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
  const triggerTypeID = useId();
  const stateID = useId();
  const labelsID = useId();
  const draftsID = useId();
  const branchesID = useId();
  const intervalID = useId();
  const cronID = useId();
  const timezoneID = useId();
  const timeoutID = useId();
  const contextID = useId();
  const nameRef = useRef<HTMLInputElement>(null);
  const closeRef = useRef(onClose);
  const requestRef = useRef<{ fingerprint: string; key: string } | undefined>(undefined);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const workflows = useQuery({ queryKey: ["workflows", "automation-form"], queryFn: api.allWorkflows });
  const repositories = useQuery({ queryKey: ["repositories"], queryFn: api.repositories });
  const current = detail?.automation;
  const [triggerType, setTriggerType] = useState<AutomationTrigger["type"]>(current?.trigger.type ?? "github_issue");
  const isPullRequest = triggerType === "github_pull_request";
  const isSchedule = triggerType === "schedule";
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
    const cron = String(form.get("cron") ?? "").trim().replace(/\s+/g, " ");
    const timezone = String(form.get("timezone") ?? "").trim();
    const labels = String(form.get("required_labels") ?? "").split(",").map((label) => label.trim()).filter(Boolean);
    const baseBranches = String(form.get("base_branches") ?? "").split(",").map((branch) => branch.trim()).filter(Boolean);
    const nextErrors: Record<string, string> = {};
    if (!name) nextErrors.name = "Enter an Automation name.";
    else if (Array.from(name).length > 100) nextErrors.name = "Keep the name to 100 characters.";
    if (!workflow) nextErrors.workflow = "Choose a Workflow.";
    if (!repository) nextErrors.repository = "Choose a repository.";
    if (!isSchedule && (labels.length > 20 || labels.some((label) => new TextEncoder().encode(label).length > 200))) nextErrors.labels = "Use at most 20 labels of 200 bytes each.";
    if (isPullRequest && (baseBranches.length > 20 || baseBranches.some((branch) => new TextEncoder().encode(branch).length > 255))) nextErrors.branches = "Use at most 20 base branches of 255 bytes each.";
    if (!isSchedule && (!Number.isInteger(pollInterval) || pollInterval < 10 || pollInterval > 86_400)) nextErrors.interval = "Use 10 to 86,400 seconds.";
    if (isSchedule && cron.split(" ").length !== 5) nextErrors.cron = "Enter exactly five cron fields, with no seconds field.";
    if (isSchedule && !timezone) nextErrors.timezone = "Enter an IANA timezone, such as Europe/London.";
    if (!Number.isInteger(timeout) || timeout < 1 || timeout > 28_800) nextErrors.timeout = "Use 1 to 28,800 seconds.";
    if (new TextEncoder().encode(context).length > 8 * 1024) nextErrors.context = "Keep context to 8 KiB.";
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length) return;
    const trigger: AutomationTrigger = isSchedule ? {
      type: "schedule",
      cron,
      timezone,
    } : isPullRequest ? {
      type: "github_pull_request",
      state: String(form.get("state")) as "open" | "closed" | "merged",
      include_drafts: form.get("include_drafts") === "on",
      required_labels: labels,
      base_branches: baseBranches,
      poll_interval_seconds: pollInterval,
    } : {
      type: "github_issue",
      state: String(form.get("state")) as "open" | "closed",
      required_labels: labels,
      poll_interval_seconds: pollInterval,
    };
    const payload = {
      name,
      workflow_id: workflow,
      repository_id: repository,
      context,
      timeout_seconds: timeout,
      trigger,
    };
    const fingerprint = JSON.stringify(payload);
    if (requestRef.current?.fingerprint !== fingerprint) {
      requestRef.current = { fingerprint, key: crypto.randomUUID() };
    }
    save.mutate({ request_key: requestRef.current.key, ...payload });
  };
  const workflowItems = workflows.data ?? [];
  const repositoryItems = repositories.data ?? [];
  const selectedWorkflow = current
    ? workflowItems.find((workflow) => workflow.id === current.workflow_id)
    : undefined;
  const selectedRepository = current
    ? repositoryItems.find((repository) => repository.id === current.repository_id)
    : undefined;
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
                {current && <option key={current.workflow_id} value={current.workflow_id}>
                  {selectedWorkflow?.current_revision.name ?? current.workflow_name}{selectedWorkflow?.enabled === false ? " (disabled)" : ""}
                </option>}
                {workflowItems.filter((workflow) => workflow.id !== current?.workflow_id).map((workflow) => <option key={workflow.id} value={workflow.id}>{workflow.current_revision.name}{workflow.enabled ? "" : " (disabled)"}</option>)}
              </select>
            </Field>
            <Field label="Managed repository" htmlFor={repositoryID} error={errors.repository} hint={mode === "edit" ? "Repository identity is immutable." : undefined}>
              <select id={repositoryID} name="repository_id" defaultValue={current?.repository_id ?? ""} disabled={mode === "edit"} aria-invalid={Boolean(errors.repository)}>
                <option value="">Choose a repository</option>
                {current && <option key={current.repository_id} value={current.repository_id}>
                  {selectedRepository?.remote_identity ?? current.repository_identity}{selectedRepository?.enabled === false ? " (disabled)" : ""}
                </option>}
                {repositoryItems.filter((repository) => repository.id !== current?.repository_id).map((repository) => <option key={repository.id} value={repository.id}>{repository.remote_identity}{repository.enabled ? "" : " (disabled)"}</option>)}
              </select>
            </Field>
            <Field label="Trigger type" htmlFor={triggerTypeID} hint={mode === "edit" ? "Trigger type is immutable." : undefined}>
              <select
                id={triggerTypeID}
                name="trigger_type"
                value={triggerType}
                disabled={mode === "edit"}
                onChange={(event) => setTriggerType(event.target.value as AutomationTrigger["type"])}
              >
                <option value="github_issue">GitHub issue</option>
                <option value="github_pull_request">GitHub pull request</option>
                <option value="schedule">Schedule</option>
              </select>
            </Field>
            {!isSchedule && <Field label={isPullRequest ? "Pull request state" : "Issue state"} htmlFor={stateID}>
              <select id={stateID} name="state" defaultValue={current?.trigger.type === "github_issue" || current?.trigger.type === "github_pull_request" ? current.trigger.state : "open"}>
                <option value="open">Open</option><option value="closed">Closed</option>
                {isPullRequest && <option value="merged">Merged</option>}
              </select>
            </Field>}
            {isPullRequest && <>
              <Field label="Draft pull requests" htmlFor={draftsID} hint="Include drafts that match every other condition.">
                <label className="confirmation-check" htmlFor={draftsID}>
                  <input id={draftsID} name="include_drafts" type="checkbox" defaultChecked={current?.trigger.type === "github_pull_request" && current.trigger.include_drafts} />
                  Include drafts
                </label>
              </Field>
              <Field label="Base branches" htmlFor={branchesID} error={errors.branches} hint="Comma separated · optional · up to 20">
                <input id={branchesID} name="base_branches" defaultValue={current?.trigger.type === "github_pull_request" ? current.trigger.base_branches.join(", ") : ""} aria-invalid={Boolean(errors.branches)} />
              </Field>
            </>}
            {isSchedule ? <>
              <Field label="Cron (five fields)" htmlFor={cronID} error={errors.cron} hint="Minute hour day-of-month month day-of-week · no seconds">
                <input id={cronID} name="cron" className="mono" defaultValue={current?.trigger.type === "schedule" ? current.trigger.cron : "0 9 * * 1"} aria-invalid={Boolean(errors.cron)} />
              </Field>
              <Field label="IANA timezone" htmlFor={timezoneID} error={errors.timezone} hint="For example Europe/London">
                <input id={timezoneID} name="timezone" defaultValue={current?.trigger.type === "schedule" ? current.trigger.timezone : Intl.DateTimeFormat().resolvedOptions().timeZone} aria-invalid={Boolean(errors.timezone)} />
              </Field>
            </> : <>
              <Field label="Required labels" htmlFor={labelsID} error={errors.labels} hint="Comma separated · up to 20">
                <input id={labelsID} name="required_labels" defaultValue={current?.trigger.type === "github_issue" || current?.trigger.type === "github_pull_request" ? current.trigger.required_labels.join(", ") : "factory:ready"} aria-invalid={Boolean(errors.labels)} />
              </Field>
              <Field label="Poll interval (seconds)" htmlFor={intervalID} error={errors.interval}>
                <input id={intervalID} name="poll_interval_seconds" type="number" min={10} max={86_400} defaultValue={current?.trigger.type === "github_issue" || current?.trigger.type === "github_pull_request" ? current.trigger.poll_interval_seconds : 30} aria-invalid={Boolean(errors.interval)} />
              </Field>
            </>}
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
  if (automation.trigger.type === "schedule") {
    return `Schedule · ${automation.trigger.cron} · ${automation.trigger.timezone}`;
  }
  const labels = automation.trigger.required_labels.length
    ? ` · labels ${automation.trigger.required_labels.join(", ")}`
    : "";
  if (automation.trigger.type === "github_pull_request") {
    const drafts = automation.trigger.include_drafts ? " · including drafts" : " · excluding drafts";
    const bases = automation.trigger.base_branches.length
      ? ` · bases ${automation.trigger.base_branches.join(", ")}`
      : "";
    return `GitHub pull requests · ${automation.trigger.state}${drafts}${labels}${bases}`;
  }
  return `GitHub issues · ${automation.trigger.state}${labels}`;
}

function occurrenceIdentity(occurrence: AutomationOccurrence): string {
  if (occurrence.kind === "scheduled") return `Scheduled ${occurrence.scheduled_at ? new Date(occurrence.scheduled_at).toISOString() : "instant"}`;
  if (occurrence.kind === "run_now") return "Run now";
  const number = occurrence.pull_request_number ?? occurrence.issue_number ?? 0;
  const title = occurrence.pull_request_title ?? occurrence.issue_title ?? "Unknown GitHub item";
  return `#${number} ${title}`;
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
