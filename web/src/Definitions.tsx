import {
  Archive,
  ArrowLeft,
  BookOpenText,
  ChevronRight,
  LoaderCircle,
  Pencil,
  Plus,
  RotateCcw,
  X,
} from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from "react";
import { api } from "./api";
import { invalidateControlPlane } from "./controlPlaneQueries";
import type { CreateDefinitionInput, Definition, Runtime } from "./types";
import {
  EmptyState,
  ErrorState,
  InlineError,
  LoadingState,
  PanelHeading,
  StaleBanner,
  ViewHeader,
} from "./ui";

export function DefinitionsView({ onDefinition }: { onDefinition: (id: string) => void }) {
  const [createOpen, setCreateOpen] = useState(false);
  const [archived, setArchived] = useState(false);
  const [history, setHistory] = useState<Definition[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>();
  const previousHeadCursor = useRef<string | null | undefined>(undefined);
  const definitions = useQuery({
    queryKey: ["definitions", archived ? "archived" : "active", "head"],
    queryFn: () => api.definitions("", archived),
  });
  const loadMore = useMutation({
    mutationFn: ({ cursor, archived: archivedPage }: {
      cursor: string;
      headCursor: string | null;
      archived: boolean;
    }) => api.definitions(cursor, archivedPage),
    onSuccess: (page, request) => {
      if (request.archived !== archived) return;
      setHistory((current) => mergeDefinitions(current, page.definitions));
      if (previousHeadCursor.current === request.headCursor) setNextCursor(page.next_cursor);
    },
  });
  useEffect(() => {
    if (!definitions.data) return;
    const boundaryChanged = previousHeadCursor.current !== definitions.data.next_cursor;
    setNextCursor((current) => boundaryChanged ? definitions.data.next_cursor : current);
    previousHeadCursor.current = definitions.data.next_cursor;
  }, [definitions.data]);

  if (definitions.isPending) return <LoadingState label="Loading Definitions" />;
  if (!definitions.data) return <ErrorState error={definitions.error} onRetry={() => void definitions.refetch()} />;
  const activeCursor = nextCursor === undefined ? definitions.data.next_cursor : nextCursor;
  const items = mergeDefinitions(definitions.data.definitions, history);

  return (
    <div className="page">
      <ViewHeader
        title="Definitions"
        fetching={definitions.isFetching}
        updatedAt={definitions.dataUpdatedAt}
        onRefresh={() => {
          setHistory([]);
          setNextCursor(undefined);
          void definitions.refetch();
        }}
      />
      {definitions.error && <StaleBanner error={definitions.error} />}
      <div className="view-toolbar">
        <p>{archived ? "Archived Definitions remain available for inspection and restore." : "Shared agent prompts for repeatable software engineering jobs."}</p>
        <div className="detail-actions">
          <button className="button button-secondary" onClick={() => {
            setArchived((current) => !current);
            setHistory([]);
            setNextCursor(undefined);
            previousHeadCursor.current = undefined;
          }}>
            {archived ? <><ArrowLeft size={15} /> View active</> : <><Archive size={15} /> View archive</>}
          </button>
          {!archived && <button className="button button-primary" onClick={() => setCreateOpen(true)}>
            <Plus size={15} /> Create Definition
          </button>}
        </div>
      </div>
      {items.length === 0 ? (
        <EmptyState
          icon={<BookOpenText size={22} />}
          title={archived ? "No archived Definitions" : "No Definitions yet"}
          description={archived
            ? "Archived Definitions will appear here."
            : "Save a prompt once, then run the same engineering job across your repositories."}
          action={archived ? undefined : <button className="button button-primary" onClick={() => setCreateOpen(true)}>Create Definition</button>}
        />
      ) : (
        <div className="workflow-list">
          <div className="definition-table-head"><span>Name</span><span>Runtime</span><span>Required tools</span><span>Timeout</span><span>Updated</span><span /></div>
          {items.map((definition) => (
            <button className="definition-row" key={definition.id} onClick={() => onDefinition(definition.id)}>
              <span className="workflow-identity">
                <strong>{definition.name}</strong>
                <small>{promptSummary(definition.prompt)}</small>
              </span>
              <span className="mono">{runtimeLabel(definition.runtime)}</span>
              <span className="muted">{definition.allowed_tools.join(", ") || "None"}</span>
              <span className="mono muted">{formatTimeout(definition.timeout_seconds)}</span>
              <span className="mono muted">{new Date(definition.updated_at).toLocaleString()}</span>
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
              archived,
            })}
          >
            {loadMore.isPending ? "Loading…" : "Load more Definitions"}
          </button>
        </div>
      )}
      {loadMore.error && <InlineError error={loadMore.error} />}
      {createOpen && <DefinitionForm onClose={() => setCreateOpen(false)} onSaved={(definition) => {
        setCreateOpen(false);
        onDefinition(definition.id);
      }} />}
    </div>
  );
}

export function DefinitionDetail({ id, onBack }: { id: string; onBack: () => void }) {
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [confirmArchive, setConfirmArchive] = useState(false);
  const detail = useQuery({ queryKey: ["definition", id], queryFn: () => api.definition(id) });
  const archive = useMutation({
    mutationFn: ({ definition, archived }: { definition: Definition; archived: boolean }) => api.setDefinitionArchived({
      id: definition.id,
      archived,
      expectedGeneration: definition.generation,
    }),
    onSuccess: async (next) => {
      queryClient.setQueryData(["definition", next.id], next);
      await invalidateControlPlane(queryClient);
      onBack();
    },
  });
  if (detail.isPending) return <LoadingState label="Loading Definition" />;
  if (!detail.data) return <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />;
  const definition = detail.data;
  const inputs = Object.entries(definition.inputs).sort(([left], [right]) => left.localeCompare(right));

  return (
    <div className="page detail-page">
      <button className="back-button" onClick={onBack}><ArrowLeft size={16} /> All Definitions</button>
      <div className="detail-heading">
        <div>
          <span className={`status-badge ${definition.archived ? "status-cancelled" : "status-succeeded"}`}>
            <span className="status-dot" />{definition.archived ? "Archived" : "Active"}
          </span>
          <h1>{definition.name}</h1>
          <p>Updated {new Date(definition.updated_at).toLocaleString()}</p>
        </div>
        {definition.archived ? (
          <div className="detail-actions">
            <button
              className="button button-secondary"
              disabled={archive.isPending}
              onClick={() => archive.mutate({ definition, archived: false })}
            >
              <RotateCcw size={14} /> {archive.isPending ? "Restoring…" : "Restore Definition"}
            </button>
          </div>
        ) : (
          <div className="detail-actions">
            <button className="button button-secondary" onClick={() => setEditing(true)}>
              <Pencil size={14} /> Edit Definition
            </button>
            <button className="button button-danger-secondary" onClick={() => setConfirmArchive(true)}>
              <Archive size={14} /> Archive
            </button>
          </div>
        )}
      </div>
      {detail.error && <StaleBanner error={detail.error} />}
      {archive.error && <InlineError error={archive.error} />}
      {confirmArchive && (
        <div className="confirm-action workflow-confirm" role="alert">
          <span>Archive this Definition? Past Runs will keep their saved snapshot.</span>
          <button
            className="button button-danger"
            disabled={archive.isPending}
            onClick={() => archive.mutate({ definition, archived: true })}
          >
            {archive.isPending ? "Archiving…" : "Archive Definition"}
          </button>
          <button className="button button-secondary" onClick={() => setConfirmArchive(false)}>Cancel</button>
        </div>
      )}
      <div className="detail-grid">
        <section className="panel detail-main">
          <PanelHeading title="Prompt" />
          <div className="long-copy">{definition.prompt}</div>
        </section>
        <section className="panel">
          <PanelHeading title="Execution" />
          <dl className="metadata">
            <div><dt>Runtime</dt><dd>{runtimeLabel(definition.runtime)}</dd></div>
            <div><dt>Timeout</dt><dd>{formatTimeout(definition.timeout_seconds)}</dd></div>
            <div><dt>Required tools</dt><dd>{definition.allowed_tools.join(", ") || "None"}</dd></div>
            <div><dt>Created</dt><dd>{new Date(definition.created_at).toLocaleString()}</dd></div>
            <div><dt>Identity</dt><dd className="mono break-anywhere">{definition.id}</dd></div>
          </dl>
          <p className="metrics-note">Tool names are Runner requirements. They do not sandbox a trusted local agent.</p>
        </section>
      </div>
      <section className="panel">
        <PanelHeading title="Optional inputs" aside={`${inputs.length} configured`} />
        {inputs.length === 0 ? (
          <p className="muted">No default inputs.</p>
        ) : (
          <dl className="metadata definition-inputs">
            {inputs.map(([key, value]) => <div key={key}><dt className="mono">{key}</dt><dd>{value}</dd></div>)}
          </dl>
        )}
      </section>
      {editing && <DefinitionForm definition={definition} onClose={() => setEditing(false)} onSaved={(next) => {
        queryClient.setQueryData(["definition", id], next);
        setEditing(false);
        void invalidateControlPlane(queryClient);
      }} />}
    </div>
  );
}

function DefinitionForm({
  definition,
  onClose,
  onSaved,
}: {
  definition?: Definition;
  onClose: () => void;
  onSaved: (definition: Definition) => void;
}) {
  const queryClient = useQueryClient();
  const [initialDefinition] = useState(definition);
  const nameID = useId();
  const promptID = useId();
  const runtimeID = useId();
  const toolsID = useId();
  const timeoutID = useId();
  const inputsID = useId();
  const nameRef = useRef<HTMLInputElement>(null);
  const closeRef = useRef(onClose);
  const requestRef = useRef<{ fingerprint: string; key: string } | undefined>(undefined);
  const [errors, setErrors] = useState<Record<string, string>>({});
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
    mutationFn: (input: CreateDefinitionInput) => initialDefinition
      ? api.updateDefinition({
          id: initialDefinition.id,
          input: { ...input, expected_generation: initialDefinition.generation },
        })
      : api.createDefinition(input),
    onSuccess: async (next) => {
      await invalidateControlPlane(queryClient);
      onSaved(next);
    },
  });
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const name = String(form.get("name") ?? "").trim();
    const prompt = String(form.get("prompt") ?? "");
    const runtime = String(form.get("runtime") ?? "") as Runtime;
    const allowedTools = [...new Set(String(form.get("allowed_tools") ?? "")
      .split(",").map((tool) => tool.trim().toLowerCase()).filter(Boolean))].sort();
    const timeoutSeconds = Number(form.get("timeout_seconds"));
    const parsedInputs = parseInputs(String(form.get("inputs") ?? ""));
    const nextErrors: Record<string, string> = {};
    if (!name) nextErrors.name = "Enter a Definition name.";
    else if (Array.from(name).length > 100) nextErrors.name = "Keep the name to 100 characters.";
    if (!prompt.trim()) nextErrors.prompt = "Enter the agent prompt.";
    else if (new TextEncoder().encode(prompt).length > 64 * 1024) nextErrors.prompt = "Keep the prompt to 64 KiB.";
    if (!["pi", "codex", "claude-code"].includes(runtime)) nextErrors.runtime = "Choose an agent runtime.";
    if (allowedTools.some((tool) => tool !== "git" && tool !== "gh")) {
      nextErrors.allowed_tools = "Use supported Runner tools: git and gh.";
    }
    if (!Number.isInteger(timeoutSeconds) || timeoutSeconds < 1 || timeoutSeconds > 28_800) {
      nextErrors.timeout_seconds = "Enter a timeout from 1 to 28,800 seconds.";
    }
    if (parsedInputs.error) nextErrors.inputs = parsedInputs.error;
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length) return;
    const payload = {
      name,
      prompt,
      runtime,
      allowed_tools: allowedTools,
      timeout_seconds: timeoutSeconds,
      inputs: parsedInputs.values,
    };
    const fingerprint = JSON.stringify({ ...payload, expected_generation: initialDefinition?.generation });
    if (requestRef.current?.fingerprint !== fingerprint) {
      requestRef.current = { fingerprint, key: crypto.randomUUID() };
    }
    save.mutate({ request_key: requestRef.current.key, ...payload });
  };

  return (
    <div className="modal-layer">
      <button className="modal-scrim" aria-label="Close Definition form" onClick={onClose} />
      <div className="modal definition-modal" role="dialog" aria-modal="true" aria-labelledby="definition-form-heading">
        <div className="modal-header">
          <h2 id="definition-form-heading">{initialDefinition ? "Edit Definition" : "Create Definition"}</h2>
          <button className="icon-button" aria-label="Close" onClick={onClose}><X size={19} /></button>
        </div>
        <form onSubmit={submit} noValidate>
          <div className="modal-body">
            <DefinitionField label="Name" htmlFor={nameID} error={errors.name}>
              <input ref={nameRef} id={nameID} name="name" autoComplete="off" defaultValue={initialDefinition?.name ?? ""} aria-invalid={Boolean(errors.name)} />
            </DefinitionField>
            <DefinitionField label="Agent prompt" htmlFor={promptID} error={errors.prompt} hint="Required · 64 KiB">
              <textarea id={promptID} name="prompt" rows={10} defaultValue={initialDefinition?.prompt ?? ""} aria-invalid={Boolean(errors.prompt)} />
            </DefinitionField>
            <div className="definition-form-grid">
              <DefinitionField label="Agent runtime" htmlFor={runtimeID} error={errors.runtime}>
                <select id={runtimeID} name="runtime" defaultValue={initialDefinition?.runtime ?? "codex"} aria-invalid={Boolean(errors.runtime)}>
                  <option value="codex">Codex</option>
                  <option value="claude-code">Claude Code</option>
                  <option value="pi">Pi</option>
                </select>
              </DefinitionField>
              <DefinitionField label="Timeout in seconds" htmlFor={timeoutID} error={errors.timeout_seconds}>
                <input id={timeoutID} name="timeout_seconds" type="number" min={1} max={28_800} defaultValue={initialDefinition?.timeout_seconds ?? 3600} aria-invalid={Boolean(errors.timeout_seconds)} />
              </DefinitionField>
            </div>
            <DefinitionField label="Required tools" htmlFor={toolsID} error={errors.allowed_tools} hint="Optional · comma-separated: git, gh">
              <input id={toolsID} name="allowed_tools" autoComplete="off" defaultValue={initialDefinition?.allowed_tools.join(", ") ?? "git, gh"} aria-invalid={Boolean(errors.allowed_tools)} />
            </DefinitionField>
            <DefinitionField label="Optional inputs" htmlFor={inputsID} error={errors.inputs} hint="One NAME=value default per line">
              <textarea id={inputsID} name="inputs" rows={4} defaultValue={formatInputs(initialDefinition?.inputs ?? {})} aria-invalid={Boolean(errors.inputs)} placeholder="severity=high" />
            </DefinitionField>
            {save.error && <InlineError error={save.error} />}
          </div>
          <div className="modal-footer">
            <button type="button" className="button button-secondary" onClick={onClose}>Cancel</button>
            <button type="submit" className="button button-primary" disabled={save.isPending}>
              {save.isPending
                ? <><LoaderCircle size={16} className="spin" /> Saving…</>
                : initialDefinition
                  ? <><Pencil size={15} /> Save changes</>
                  : <><Plus size={15} /> Create Definition</>}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function DefinitionField({ label, htmlFor, error, hint, children }: {
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

function parseInputs(value: string): { values: Record<string, string>; error?: string } {
  const result: Record<string, string> = {};
  const lines = value.split("\n");
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index].endsWith("\r") ? lines[index].slice(0, -1) : lines[index];
    if (!line.trim()) continue;
    const separator = line.indexOf("=");
    if (separator < 1) return { values: {}, error: `Line ${index + 1} must use NAME=value.` };
    const key = line.slice(0, separator).trim();
    const inputValue = line.slice(separator + 1);
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) {
      return { values: {}, error: `Line ${index + 1} has an invalid input name.` };
    }
    if (Object.hasOwn(result, key)) return { values: {}, error: `Input ${key} is repeated.` };
    if (new TextEncoder().encode(inputValue).length > 4 * 1024) {
      return { values: {}, error: `Input ${key} is longer than 4 KiB.` };
    }
    if (/\p{Cc}/u.test(inputValue)) {
      return { values: {}, error: `Input ${key} contains a control character.` };
    }
    result[key] = inputValue;
  }
  if (Object.keys(result).length > 32) return { values: {}, error: "Use at most 32 optional inputs." };
  const totalBytes = Object.entries(result).reduce(
    (total, [key, inputValue]) => total + new TextEncoder().encode(key + inputValue).length,
    0,
  );
  if (totalBytes > 16 * 1024) return { values: {}, error: "Keep optional inputs to 16 KiB in total." };
  return { values: result };
}

function formatInputs(values: Record<string, string>): string {
  return Object.entries(values).sort(([left], [right]) => left.localeCompare(right))
    .map(([key, value]) => `${key}=${value}`).join("\n");
}

function mergeDefinitions(...groups: Definition[][]): Definition[] {
  const unique = new Map<string, Definition>();
  for (const group of groups) {
    for (const definition of group) {
      if (!unique.has(definition.id)) unique.set(definition.id, definition);
    }
  }
  return [...unique.values()].sort((left, right) => Date.parse(right.updated_at) - Date.parse(left.updated_at));
}

function runtimeLabel(runtime: Runtime): string {
  if (runtime === "claude-code") return "Claude Code";
  if (runtime === "pi") return "Pi";
  return "Codex";
}

function formatTimeout(seconds: number): string {
  if (seconds % 3600 === 0) return `${seconds / 3600}h`;
  if (seconds % 60 === 0) return `${seconds / 60}m`;
  return `${seconds}s`;
}

function promptSummary(prompt: string): string {
  const compact = prompt.replace(/\s+/g, " ").trim();
  return compact.length > 120 ? `${compact.slice(0, 117)}…` : compact;
}
