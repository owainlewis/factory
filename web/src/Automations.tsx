import {
  Activity,
  ArrowLeft,
  BookOpenText,
  Bot,
  ChevronRight,
  CirclePlay,
  DatabaseBackup,
  ExternalLink,
  FlaskConical,
  LoaderCircle,
  Pencil,
  Plus,
  Power,
  PowerOff,
  SlidersHorizontal,
  X,
} from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
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
  const [repositoryFilter, setRepositoryFilter] = useState("all");
  const [statusFilter, setStatusFilter] = useState<"all" | "enabled" | "disabled">("all");
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
  const repositoryOptions = useMemo(() => Array.from(new Set(
    items.flatMap(automationRepositoryIdentities),
  )).sort(), [items]);
  const visibleItems = items.filter((automation) => {
    if (repositoryFilter !== "all" && !automationRepositoryIdentities(automation).includes(repositoryFilter)) return false;
    if (statusFilter === "enabled" && !automation.enabled) return false;
    if (statusFilter === "disabled" && automation.enabled) return false;
    return true;
  });

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
        <p>Run shared agent Definitions on demand or on a schedule across your repositories.</p>
        <div className="detail-actions">
          <button className="button button-secondary" onClick={() => setMigrationOpen(true)}>
            <DatabaseBackup size={15} /> Migrate legacy poller
          </button>
          <button className="button button-primary" onClick={() => setCreateOpen(true)}>
            <Plus size={15} /> Create Automation
          </button>
        </div>
      </div>
      {items.length > 0 && (
        <div className="automation-filterbar" aria-label="Filter Automations">
          <SlidersHorizontal size={14} aria-hidden="true" />
          <label>
            <span>Repository</span>
            <select value={repositoryFilter} onChange={(event) => setRepositoryFilter(event.target.value)}>
              <option value="all">All repositories</option>
              {repositoryOptions.map((identity) => <option key={identity} value={identity}>{identity}</option>)}
            </select>
          </label>
          <label>
            <span>Status</span>
            <select value={statusFilter} onChange={(event) => setStatusFilter(event.target.value as typeof statusFilter)}>
              <option value="all">Any status</option>
              <option value="enabled">Enabled</option>
              <option value="disabled">Disabled</option>
            </select>
          </label>
          <span className="automation-filter-count">{visibleItems.length} of {items.length}</span>
        </div>
      )}
      {items.length === 0 ? (
        <EmptyState
          icon={<Bot size={22} />}
          title="No Automations yet"
          description="Create a disabled typed trigger, preview it, then enable it."
          action={<button className="button button-primary" onClick={() => setCreateOpen(true)}>Create Automation</button>}
        />
      ) : visibleItems.length === 0 ? (
        <EmptyState
          icon={<SlidersHorizontal size={22} />}
          title="No Automations match"
          description="Change the repository or status filter to see more Automations."
          action={<button className="button button-secondary" onClick={() => { setRepositoryFilter("all"); setStatusFilter("all"); }}>Clear filters</button>}
        />
      ) : (
        <div className="workflow-list automation-list">
          <div className="automation-table-head">
            <span>Automation</span><span>Status</span><span>Last run</span><span>Activity</span><span>Next action</span><span />
          </div>
          {visibleItems.map((automation) => {
            const latestRun = automation.latest_run;
            const latestRunState = latestRun ? automationRunState(latestRun) : undefined;
            return (
              <button className="automation-row" key={automation.id} onClick={() => onAutomation(automation.id)}>
                <span className="workflow-identity">
                  <strong>{automation.title}</strong>
                  <small>{automationRepositorySummary(automation)} · {triggerSummary(automation)}</small>
                </span>
                <span className="automation-list-health"><HealthBadge automation={automation} /><small>{automation.health.message || "No health detail."}</small></span>
                <span className="automation-list-copy">
                  <strong>{latestRun ? occurrenceIdentity(latestRun) : automation.latest_task?.title || "No run yet"}</strong>
                  <small>{latestRun && latestRunState
                    ? `${latestRunState.label} · ${formatTimestamp(latestRun.created_at)}`
                    : automation.latest_task ? formatRunState(automation.latest_task.state) : "Waiting for the first run"}</small>
                </span>
                <span className="automation-list-copy"><strong>{automation.dispatched_count} dispatched</strong><small>{automation.matched_count} matched · {automation.skipped_count} reused</small></span>
                <span className="automation-list-copy"><strong>{formatTimestamp(automation.next_due_at ?? automation.next_check_at)}</strong><small>{automation.trigger.type === "schedule" ? "Next run" : "Next check"} · last checked {formatTimestamp(automation.last_checked_at)}</small></span>
                <ChevronRight size={15} className="row-chevron" />
              </button>
            );
          })}
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
  onRun,
}: {
  id: string;
  onBack: () => void;
  onTask: (id: string) => void;
  onRun: (id: string) => void;
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
  const workflowID = detail.data?.automation.workflow_id ?? "";
  const runbook = useQuery({
    queryKey: ["workflow", workflowID, "automation-detail"],
    queryFn: () => api.workflow(workflowID),
    enabled: Boolean(workflowID),
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
  const latestRun = automation.latest_run ?? occurrenceItems[0];
  const latestRunState = latestRun ? automationRunState(latestRun) : undefined;
  const activeOccurrenceCursor = nextOccurrenceCursor === undefined
    ? occurrences.data?.next_cursor
    : nextOccurrenceCursor;

  return (
    <div className="page detail-page">
      <button className="back-button" onClick={onBack}><ArrowLeft size={16} /> All Automations</button>
      <div className="detail-heading">
        <div>
          <HealthBadge automation={automation} />
          <h1>{automation.title}</h1>
          <p>{triggerSummary(automation)} · {automationRepositorySummary(automation)}</p>
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
              ? `${automation.definition_name ?? automation.workflow_title} · ${automationRepositorySummary(automation)} · ${triggerSummary(automation)}`
              : "Future checks and pending dispatches stop. Existing runs continue."}</p>
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
        <PanelHeading title="Automation health" aside={automation.health.status} />
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
          <span><strong>Latest run</strong><small>{latestRun ? occurrenceIdentity(latestRun) : "No durable run yet."}</small></span>
          {latestRun && latestRunState && <>
            <span className={`status-badge status-${latestRunState.style}`}><span className="status-dot" />{latestRunState.label}</span>
            {latestRun.run_id && <button className="button button-secondary" onClick={() => onRun(latestRun.run_id!)}>Open latest Run</button>}
            {latestRun.task && <button className="button button-secondary" onClick={() => onTask(latestRun.task!.id)}>Open latest task</button>}
          </>}
        </div>
      </div>

      {preview && (
        <section className="panel preview-panel" aria-live="polite">
          <PanelHeading title="Test results" aside={preview.next_due_at ? `Next due ${formatTimestamp(preview.next_due_at)}` : `${preview.matches.length} bounded match${preview.matches.length === 1 ? "" : "es"}`} />
          <p className="muted">Testing creates no task or durable run.</p>
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
            {automation.definition_id ? <>
              <div><dt>Definition</dt><dd>{automation.definition_name} · generation {automation.definition_generation}</dd></div>
              <div><dt>Repositories</dt><dd className="mono">{automationRepositoryIdentities(automation).join(", ")}</dd></div>
              <div><dt>Concurrency</dt><dd>{automation.concurrency_limit}</dd></div>
              <div><dt>Cron</dt><dd className="mono">{automation.trigger.cron}</dd></div>
              <div><dt>Timezone</dt><dd>{automation.trigger.timezone}</dd></div>
              <div><dt>Next due UTC</dt><dd>{automation.next_due_at ? new Date(automation.next_due_at).toISOString() : "Disabled"}</dd></div>
            </> : <>
              <div><dt>Runbook</dt><dd>{automation.workflow_title} · revision {automation.workflow_revision}</dd></div>
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
            </>}
            {!automation.definition_id && <div><dt>Timeout</dt><dd>{automation.timeout_seconds} seconds</dd></div>}
          </dl>
        </section>
        <section className="panel">
          <PanelHeading title={automation.definition_id ? "Definition" : "Runbook"} aside={automation.definition_id ? `Generation ${automation.definition_generation}` : runbook.data ? `Revision ${runbook.data.workflow.current_revision.revision_number}` : undefined} />
          {automation.definition_id ? (
            <><p><strong>{automation.definition_name}</strong></p><p className="muted">Parameters: {Object.keys(automation.parameters ?? {}).length ? Object.entries(automation.parameters ?? {}).map(([key, value]) => `${key}=${value}`).join(", ") : "defaults"}</p></>
          ) : runbook.isPending ? (
            <p className="muted">Loading Markdown instructions…</p>
          ) : runbook.data ? (
            <pre className="runbook-copy">{runbook.data.workflow.current_revision.instructions}</pre>
          ) : (
            <InlineError error={runbook.error as Error} />
          )}
        </section>
      </div>

      {!automation.definition_id && <section className="panel automation-context-panel">
        <PanelHeading title="Context for this Automation" />
        <div className="long-copy">{automation.context || "No additional context."}</div>
      </section>}

      <section className="panel">
        <PanelHeading title="Runs" aside={`${occurrenceItems.length} loaded`} />
        {occurrenceItems.length === 0 ? (
          <p className="muted">No runs yet.</p>
        ) : (
          <div className="occurrence-list">
            {occurrenceItems.map((occurrence) => {
              const state = automationRunState(occurrence);
              const sourceURL = occurrence.pull_request_url ?? occurrence.issue_url;
              return (
                <div className="occurrence-row" key={occurrence.id}>
                  <span className={`status-badge status-${state.style}`}><span className="status-dot" />{state.label}</span>
                  <span className="occurrence-identity">
                    <strong>{occurrenceIdentity(occurrence)}</strong>
                    <small>{formatTimestamp(occurrence.created_at)}{occurrence.task ? ` · ${occurrence.task.title}` : ""}{occurrence.diagnostic ? ` · ${occurrence.diagnostic}` : ""}</small>
                  </span>
                  <span className="occurrence-actions">
                    {sourceURL && (
                      <a className="button button-secondary" href={sourceURL} target="_blank" rel="noreferrer">
                        <ExternalLink size={13} /> GitHub source
                      </a>
                    )}
                    {occurrence.run_id ? (
                      <button className="button button-secondary" onClick={() => onRun(occurrence.run_id!)}>
                        Open Run
                      </button>
                    ) : occurrence.task ? (
                      <button className="button button-secondary" onClick={() => onTask(occurrence.task!.id)}>
                        Open task
                      </button>
                    ) : occurrence.state === "task_deleted" ? (
                      <span className="muted">Task deleted</span>
                    ) : null}
                  </span>
                </div>
              );
            })}
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
              {loadMoreOccurrences.isPending ? "Loading…" : "Load more runs"}
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
    mutationFn: (mappings: Array<{ queue_id: string; workflow_title: string; automation_title: string }>) =>
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
  const hasMissingLedgerQueue = reviewQueues.some((queue) => queue.blocking);
  const operationError = activeMigration.error || preview.error || importMigration.error || resolveOccurrence.error || finalize.error;
  const setPath = (field: "config_path" | "data_home" | "working_directory", value: string) =>
    setSelection((current) => ({ ...current, [field]: value || undefined }));
  const submitImport = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const mappings = reviewQueues.filter((queue) => queue.supported).map((queue) => ({
      queue_id: queue.queue_id,
      workflow_title: String(form.get(`workflow-${queue.queue_id}`) ?? "").trim(),
      automation_title: String(form.get(`automation-${queue.queue_id}`) ?? "").trim(),
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
                      <div className="migration-title-grid">
                        <Field label="Runbook title" htmlFor={`workflow-${queue.queue_id}`}>
                          <input id={`workflow-${queue.queue_id}`} name={`workflow-${queue.queue_id}`} autoComplete="off" defaultValue={queue.workflow_title} required />
                        </Field>
                        <Field label="Automation title" htmlFor={`automation-${queue.queue_id}`}>
                          <input id={`automation-${queue.queue_id}`} name={`automation-${queue.queue_id}`} autoComplete="off" defaultValue={queue.automation_title} required />
                        </Field>
                      </div>
                    )}
                  </section>
                ))}
              </div>
              <div className="migration-actions">
                <button type="button" className="button button-secondary" onClick={() => { setMigration(undefined); setReviewQueues([]); }}>Run a new Preview</button>
                <button type="submit" className="button button-primary" disabled={hasMissingLedgerQueue || importMigration.isPending}>
                  {importMigration.isPending ? "Importing…" : hasMissingLedgerQueue ? "Restore missing queue before Import" : migration.counts.supported_queues === 0 ? "Continue to archive" : "Import disabled Automations"}
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
                        Review {automation.title}
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
  const titleID = useId();
  const workflowID = useId();
  const definitionID = useId();
  const repositoryID = useId();
  const triggerTypeID = useId();
  const stateID = useId();
  const labelsID = useId();
  const draftsID = useId();
  const branchesID = useId();
  const intervalID = useId();
  const schedulePresetID = useId();
  const scheduleTimeID = useId();
  const cronID = useId();
  const timezoneID = useId();
  const timeoutID = useId();
  const concurrencyID = useId();
  const contextID = useId();
  const titleRef = useRef<HTMLInputElement>(null);
  const advancedRef = useRef<HTMLDetailsElement>(null);
  const closeRef = useRef(onClose);
  const requestRef = useRef<{ fingerprint: string; key: string } | undefined>(undefined);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const workflows = useQuery({ queryKey: ["workflows", "automation-form"], queryFn: api.allWorkflows });
  const repositories = useQuery({ queryKey: ["repositories"], queryFn: api.repositories });
  const definitions = useQuery({ queryKey: ["definitions", "automation-form"], queryFn: api.allDefinitions });
  const runRepositories = useQuery({ queryKey: ["run-repositories", "automation-form"], queryFn: api.runRepositories });
  const current = detail?.automation;
  const [triggerType, setTriggerType] = useState<AutomationTrigger["type"]>(current?.trigger.type ?? "github_issue");
  const [workflowSelection, setWorkflowSelection] = useState(current?.workflow_id ?? "");
  const [definitionSelection, setDefinitionSelection] = useState(current?.definition_id ?? "");
  const [parameterOverrides, setParameterOverrides] = useState<Record<string, string>>(current?.parameters ?? {});
  const [repositorySelections, setRepositorySelections] = useState(() => new Set(
    current?.repositories?.map((repository) => repository.id) ?? [],
  ));
  const initialSchedule = scheduleEditorState(current?.trigger);
  const [schedulePreset, setSchedulePreset] = useState<SchedulePreset>(initialSchedule.preset);
  const [scheduleTime, setScheduleTime] = useState(initialSchedule.time);
  const [customCron, setCustomCron] = useState(initialSchedule.cron);
  const isPullRequest = triggerType === "github_pull_request";
  const isSchedule = triggerType === "schedule";
  const usesDefinitionSchedule = isSchedule && (mode === "create" || Boolean(current?.definition_id));
  const selectedRunbook = useQuery({
    queryKey: ["workflow", workflowSelection, "automation-form"],
    queryFn: () => api.workflow(workflowSelection),
    enabled: Boolean(workflowSelection),
  });
  const selectedDefinition = definitions.data?.find((definition) => definition.id === definitionSelection);
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
    mutationFn: async (input: CreateAutomationInput) => mode === "create"
      ? api.createAutomation(input)
      : api.updateAutomation({
          id: current!.id,
          input: {
            expected_version: current!.version,
            title: input.title,
            workflow_id: input.workflow_id,
            definition_id: input.definition_id,
            repository_ids: input.repository_ids,
            parameters: input.parameters,
            concurrency_limit: input.concurrency_limit,
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
    const title = String(form.get("title") ?? "").trim();
    const workflow = workflowSelection;
    const repository = current?.repository_id ?? String(form.get("repository_id") ?? "");
    const repositoryIDs = Array.from(repositorySelections);
    const definition = definitionSelection;
    const parameters = Object.fromEntries(Object.entries(parameterOverrides).filter(([key]) =>
      Object.hasOwn(selectedDefinition?.inputs ?? {}, key),
    ));
    const concurrency = Number(form.get("concurrency_limit") || current?.concurrency_limit || 3);
    const context = String(form.get("context") ?? "");
    const timeout = Number(form.get("timeout_seconds"));
    const pollInterval = Number(form.get("poll_interval_seconds"));
    const cron = (schedulePreset === "custom" ? customCron : cronForSchedule(schedulePreset, scheduleTime))
      .trim().replace(/\s+/g, " ");
    const timezone = String(form.get("timezone") ?? "").trim();
    const labels = String(form.get("required_labels") ?? "").split(",").map((label) => label.trim()).filter(Boolean);
    const baseBranches = String(form.get("base_branches") ?? "").split(",").map((branch) => branch.trim()).filter(Boolean);
    const nextErrors: Record<string, string> = {};
    if (!title) nextErrors.title = "Enter an Automation title.";
    else if (Array.from(title).length > 100) nextErrors.title = "Keep the title to 100 characters.";
    if (usesDefinitionSchedule && !definition) nextErrors.definition = "Choose a Definition.";
    if (!usesDefinitionSchedule && !workflow) nextErrors.workflow = "Choose a runbook.";
    if (usesDefinitionSchedule && repositoryIDs.length === 0) nextErrors.repository = "Choose at least one repository.";
    if (!usesDefinitionSchedule && !repository) nextErrors.repository = "Choose a repository.";
    if (!isSchedule && (labels.length > 20 || labels.some((label) => new TextEncoder().encode(label).length > 200))) nextErrors.labels = "Use at most 20 labels of 200 bytes each.";
    if (isPullRequest && (baseBranches.length > 20 || baseBranches.some((branch) => new TextEncoder().encode(branch).length > 255))) nextErrors.branches = "Use at most 20 base branches of 255 bytes each.";
    if (!isSchedule && (!Number.isInteger(pollInterval) || pollInterval < 10 || pollInterval > 86_400)) nextErrors.interval = "Use 10 to 86,400 seconds.";
    if (isSchedule && schedulePreset !== "custom" && !/^\d{2}:\d{2}$/.test(scheduleTime)) nextErrors.schedule = "Choose a schedule time.";
    if (isSchedule && cron.split(" ").length !== 5) nextErrors.cron = "Enter exactly five cron fields, with no seconds field.";
    if (isSchedule && !timezone) nextErrors.timezone = "Enter an IANA timezone, such as Europe/London.";
    if (usesDefinitionSchedule && (!Number.isInteger(concurrency) || concurrency < 1 || concurrency > 100)) nextErrors.concurrency = "Use 1 to 100 concurrent Jobs.";
    if (!usesDefinitionSchedule && (!Number.isInteger(timeout) || timeout < 1 || timeout > 28_800)) nextErrors.timeout = "Use 1 to 28,800 seconds.";
    if (!usesDefinitionSchedule && new TextEncoder().encode(context).length > 8 * 1024) nextErrors.context = "Keep context to 8 KiB.";
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length) {
      if (nextErrors.interval || nextErrors.cron || nextErrors.timeout || nextErrors.concurrency) advancedRef.current?.setAttribute("open", "");
      return;
    }
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
    const payload: Omit<CreateAutomationInput, "request_key"> = usesDefinitionSchedule ? {
      title,
      definition_id: definition,
      repository_ids: repositoryIDs,
      parameters,
      concurrency_limit: concurrency,
      trigger,
    } : {
      title,
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
  const definitionItems = definitions.data ?? [];
  const runRepositoryItems = runRepositories.data ?? [];
  const selectedWorkflow = workflowItems.find((workflow) => workflow.id === workflowSelection);
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
          <div className="modal-body automation-composer">
            <section className="automation-form-section">
              <div className="automation-section-heading">
                <span>1</span>
                <div><strong>What should run?</strong><small>{usesDefinitionSchedule ? "Choose the shared Definition every agent will run." : "Choose the legacy runbook the agent will follow."}</small></div>
              </div>
              <div className="automation-form-grid">
                <Field label="Title" htmlFor={titleID} error={errors.title}>
                  <input ref={titleRef} id={titleID} name="title" autoComplete="off" defaultValue={current?.title ?? ""} aria-invalid={Boolean(errors.title)} />
                </Field>
                {usesDefinitionSchedule ? <Field label="Definition" htmlFor={definitionID} error={errors.definition} hint="Saved prompt, runtime, tools, and inputs.">
                  <select
                    id={definitionID}
                    value={definitionSelection}
                    onChange={(event) => {
                      setDefinitionSelection(event.target.value);
                      setParameterOverrides({});
                    }}
                    aria-invalid={Boolean(errors.definition)}
                  >
                    <option value="">Choose a Definition</option>
                    {current?.definition_id && !definitionItems.some((definition) => definition.id === current.definition_id) && (
                      <option value={current.definition_id}>{current.definition_name}</option>
                    )}
                    {definitionItems.map((definition) => <option key={definition.id} value={definition.id}>{definition.name}</option>)}
                  </select>
                </Field> : <Field label="Runbook" htmlFor={workflowID} error={errors.workflow} hint="Legacy Markdown workflow.">
                  <select
                    id={workflowID}
                    name="workflow_id"
                    value={workflowSelection}
                    onChange={(event) => setWorkflowSelection(event.target.value)}
                    aria-invalid={Boolean(errors.workflow)}
                  >
                    <option value="">Choose a runbook</option>
                    {current && !workflowItems.some((workflow) => workflow.id === current.workflow_id) && (
                      <option value={current.workflow_id}>{current.workflow_title}</option>
                    )}
                    {workflowItems.map((workflow) => <option key={workflow.id} value={workflow.id}>{workflow.current_revision.title}{workflow.enabled ? "" : " (disabled)"}</option>)}
                  </select>
                </Field>}
              </div>
              {usesDefinitionSchedule && selectedDefinition && (
                <div className="runbook-preview" aria-live="polite">
                  <div><BookOpenText size={14} /><strong>{selectedDefinition.name}</strong><span>Generation {selectedDefinition.generation} · {selectedDefinition.runtime}</span></div>
                  <pre>{selectedDefinition.prompt}</pre>
                </div>
              )}
              {!usesDefinitionSchedule && workflowSelection && (
                <div className="runbook-preview" aria-live="polite">
                  <div><BookOpenText size={14} /><strong>{selectedRunbook.data?.workflow.current_revision.title ?? selectedWorkflow?.current_revision.title ?? current?.workflow_title ?? "Runbook"}</strong><span>{selectedRunbook.data ? `Revision ${selectedRunbook.data.workflow.current_revision.revision_number}` : "Loading…"}</span></div>
                  {selectedRunbook.data ? <pre>{selectedRunbook.data.workflow.current_revision.instructions}</pre> : selectedRunbook.error ? <InlineError error={selectedRunbook.error as Error} /> : null}
                </div>
              )}
            </section>

            <section className="automation-form-section">
              <div className="automation-section-heading">
                <span>2</span>
                <div><strong>Where should it run?</strong><small>{usesDefinitionSchedule ? "A Run creates one isolated Job per selected repository." : "Each task uses one managed Git repository."}</small></div>
              </div>
              {usesDefinitionSchedule ? <Field label="Repositories" htmlFor={repositoryID} error={errors.repository} hint={`${repositorySelections.size} selected · local and remote Runner routes are supported`}>
                <select
                  id={repositoryID}
                  multiple
                  size={Math.min(6, Math.max(3, runRepositoryItems.length))}
                  value={Array.from(repositorySelections)}
                  onChange={(event) => setRepositorySelections(new Set(Array.from(event.target.selectedOptions, (option) => option.value)))}
                  aria-invalid={Boolean(errors.repository)}
                >
                  {current?.repositories?.filter((repository) => !runRepositoryItems.some((item) => item.id === repository.id)).map((repository) => (
                    <option key={repository.id} value={repository.id}>{repository.remote_identity}</option>
                  ))}
                  {runRepositoryItems.map((repository) => <option key={repository.id} value={repository.id}>{repository.remote_identity}</option>)}
                </select>
              </Field> : <Field label="Repository" htmlFor={repositoryID} error={errors.repository} hint={mode === "edit" ? "Repository identity is immutable." : "Choose one target now."}>
                <select id={repositoryID} name="repository_id" defaultValue={current?.repository_id ?? ""} disabled={mode === "edit"} aria-invalid={Boolean(errors.repository)}>
                  <option value="">Choose a repository</option>
                  {current && <option key={current.repository_id} value={current.repository_id}>
                    {selectedRepository?.remote_identity ?? current.repository_identity}{selectedRepository?.enabled === false ? " (disabled)" : ""}
                  </option>}
                  {repositoryItems.filter((repository) => repository.id !== current?.repository_id).map((repository) => <option key={repository.id} value={repository.id}>{repository.remote_identity}{repository.enabled ? "" : " (disabled)"}</option>)}
                </select>
              </Field>}
              {usesDefinitionSchedule && selectedDefinition && Object.keys(selectedDefinition.inputs).length > 0 && (
                <div className="automation-form-grid">
                  {Object.entries(selectedDefinition.inputs).map(([key, defaultValue]) => (
                    <Field key={`${selectedDefinition.id}:${key}`} label={key} htmlFor={`automation-parameter-${key}`} hint={`Default: ${defaultValue || "empty"}`}>
                      <input
                        id={`automation-parameter-${key}`}
                        name={`parameter:${key}`}
                        value={parameterOverrides[key] ?? ""}
                        onChange={(event) => setParameterOverrides((values) => ({ ...values, [key]: event.target.value }))}
                      />
                      {Object.hasOwn(parameterOverrides, key) && <button type="button" className="button button-secondary" onClick={() => setParameterOverrides((values) => {
                        const next = { ...values };
                        delete next[key];
                        return next;
                      })}>Use default</button>}
                    </Field>
                  ))}
                </div>
              )}
              {!usesDefinitionSchedule && <Field label="Context for this Automation" htmlFor={contextID} error={errors.context} hint="Optional repository-specific context · 8 KiB">
                <textarea id={contextID} name="context" rows={4} defaultValue={current?.context ?? ""} aria-invalid={Boolean(errors.context)} />
              </Field>}
            </section>

            <section className="automation-form-section">
              <div className="automation-section-heading">
                <span>3</span>
                <div><strong>When should it run?</strong><small>React to GitHub state or run on a local control-plane schedule.</small></div>
              </div>
              <div className="automation-form-grid">
                <Field label="Trigger" htmlFor={triggerTypeID} hint={mode === "edit" ? "Trigger type is immutable." : undefined}>
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
                {!isSchedule && <Field key="provider-state" label={isPullRequest ? "Pull request state" : "Issue state"} htmlFor={stateID}>
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
                  <Field key="schedule-frequency" label="Frequency" htmlFor={schedulePresetID}>
                    <select id={schedulePresetID} value={schedulePreset} onChange={(event) => setSchedulePreset(event.target.value as SchedulePreset)}>
                      <option value="weekdays">Every weekday</option>
                      <option value="daily">Every day</option>
                      <option value="weekly">Every Monday</option>
                      <option value="custom">Custom cron</option>
                    </select>
                  </Field>
                  {schedulePreset !== "custom" && <Field key="schedule-time" label="Time" htmlFor={scheduleTimeID} error={errors.schedule}>
                    <input id={scheduleTimeID} type="time" value={scheduleTime} onChange={(event) => setScheduleTime(event.target.value)} aria-invalid={Boolean(errors.schedule)} />
                  </Field>}
                  <Field key="schedule-timezone" label="Timezone" htmlFor={timezoneID} error={errors.timezone} hint="IANA name, such as Europe/London">
                    <input id={timezoneID} name="timezone" defaultValue={current?.trigger.type === "schedule" ? current.trigger.timezone : Intl.DateTimeFormat().resolvedOptions().timeZone} aria-invalid={Boolean(errors.timezone)} />
                  </Field>
                </> : <Field key="provider-labels" label="Required labels" htmlFor={labelsID} error={errors.labels} hint="Comma separated · up to 20">
                  <input id={labelsID} name="required_labels" defaultValue={current?.trigger.type === "github_issue" || current?.trigger.type === "github_pull_request" ? current.trigger.required_labels.join(", ") : "factory:ready"} aria-invalid={Boolean(errors.labels)} />
                </Field>}
              </div>
            </section>

            <details ref={advancedRef} className="automation-advanced">
              <summary><span><SlidersHorizontal size={14} /> Expert settings</span><small>{isSchedule ? `Cron ${schedulePreset === "custom" ? customCron : cronForSchedule(schedulePreset, scheduleTime)} · ${usesDefinitionSchedule ? `concurrency ${current?.concurrency_limit ?? 3}` : `timeout ${current?.timeout_seconds ?? 7200}s`}` : `Poll every ${current?.trigger.type === "github_issue" || current?.trigger.type === "github_pull_request" ? current.trigger.poll_interval_seconds : 30}s · timeout ${current?.timeout_seconds ?? 7200}s`}</small></summary>
              <div className="automation-form-grid">
                {isSchedule ? <Field key="schedule-cron" label="Cron (five fields)" htmlFor={cronID} error={errors.cron} hint="Choose Custom cron above to edit the raw expression.">
                  <input
                    id={cronID}
                    name="cron"
                    className="mono"
                    value={schedulePreset === "custom" ? customCron : cronForSchedule(schedulePreset, scheduleTime)}
                    readOnly={schedulePreset !== "custom"}
                    onChange={(event) => setCustomCron(event.target.value)}
                    aria-invalid={Boolean(errors.cron)}
                  />
                </Field> : <Field key="provider-poll" label="Poll interval (seconds)" htmlFor={intervalID} error={errors.interval}>
                  <input id={intervalID} name="poll_interval_seconds" type="number" min={10} max={86_400} defaultValue={current?.trigger.type === "github_issue" || current?.trigger.type === "github_pull_request" ? current.trigger.poll_interval_seconds : 30} aria-invalid={Boolean(errors.interval)} />
                </Field>}
                {usesDefinitionSchedule ? <Field key="schedule-concurrency" label="Concurrent Jobs" htmlFor={concurrencyID} error={errors.concurrency} hint="Maximum repositories running at once · 1 to 100">
                  <input id={concurrencyID} name="concurrency_limit" type="number" min={1} max={100} defaultValue={current?.concurrency_limit ?? 3} aria-invalid={Boolean(errors.concurrency)} />
                </Field> : <Field key="provider-timeout" label="Task timeout (seconds)" htmlFor={timeoutID} error={errors.timeout}>
                  <input id={timeoutID} name="timeout_seconds" type="number" min={1} max={28_800} defaultValue={current?.timeout_seconds ?? 7200} aria-invalid={Boolean(errors.timeout)} />
                </Field>}
              </div>
            </details>
            {(usesDefinitionSchedule ? definitions.error || runRepositories.error || save.error : workflows.error || repositories.error || save.error) && <InlineError error={(usesDefinitionSchedule ? definitions.error || runRepositories.error || save.error : workflows.error || repositories.error || save.error) as Error} />}
          </div>
          <div className="modal-footer">
            <span className="disabled-first-note"><Activity size={14} /> New Automations are disabled.</span>
            <button type="button" className="button button-secondary" onClick={onClose}>Cancel</button>
            <button type="submit" className="button button-primary" disabled={save.isPending || (usesDefinitionSchedule ? definitions.isPending || runRepositories.isPending : workflows.isPending || repositories.isPending)}>
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

function automationRepositoryIdentities(automation: Automation): string[] {
  return automation.repositories?.length
    ? automation.repositories.map((repository) => repository.remote_identity)
    : [automation.repository_identity];
}

function automationRepositorySummary(automation: Automation): string {
  const identities = automationRepositoryIdentities(automation);
  return identities.length === 1 ? identities[0] : `${identities.length} repositories`;
}

function Metric({ label, value }: { label: string; value: string | number }) {
  return <div><span>{label}</span><strong>{value}</strong></div>;
}

type SchedulePreset = "weekdays" | "daily" | "weekly" | "custom";

function scheduleEditorState(trigger?: AutomationTrigger): { preset: SchedulePreset; time: string; cron: string } {
  const cron = trigger?.type === "schedule" ? trigger.cron : "0 9 * * 1-5";
  const match = /^(\d{1,2}) (\d{1,2}) \* \* (\*|1-5|1)$/.exec(cron);
  if (!match) return { preset: "custom", time: "09:00", cron };
  const [, minute, hour, weekday] = match;
  const preset: SchedulePreset = weekday === "*" ? "daily" : weekday === "1" ? "weekly" : "weekdays";
  return { preset, time: `${hour.padStart(2, "0")}:${minute.padStart(2, "0")}`, cron };
}

function cronForSchedule(preset: SchedulePreset, time: string): string {
  const [hour = "9", minute = "0"] = time.split(":");
  const weekday = preset === "daily" ? "*" : preset === "weekly" ? "1" : "1-5";
  return `${Number(minute)} ${Number(hour)} * * ${weekday}`;
}

function formatRunState(state: string): string {
  const labels: Record<string, string> = {
    task_deleted: "Task deleted",
    dispatching: "Preparing",
    dispatched: "Dispatched",
    preparing: "Preparing",
    queued: "Queued",
    running: "Running",
    succeeded: "Succeeded",
    failed: "Failed",
    cancelled: "Cancelled",
    lost: "Lost",
    pending: "Pending",
    skipped: "Skipped",
  };
  return labels[state] ?? state.replaceAll("_", " ").replace(/^./, (character) => character.toUpperCase());
}

function automationRunState(occurrence: AutomationOccurrence): { style: string; label: string } {
  if (occurrence.task) {
    const state = occurrence.task.state;
    const known = new Set(["queued", "preparing", "running", "succeeded", "failed", "cancelled", "lost"]);
    return { style: known.has(state) ? state : "cancelled", label: formatRunState(state) };
  }
  if (occurrence.state === "pending" || occurrence.state === "dispatching") {
    return { style: "preparing", label: "Preparing" };
  }
  if (occurrence.state === "task_deleted") return { style: "cancelled", label: "Task deleted" };
  if (occurrence.state === "skipped") return { style: "skipped", label: "Skipped" };
  return { style: occurrence.state, label: formatRunState(occurrence.state) };
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
