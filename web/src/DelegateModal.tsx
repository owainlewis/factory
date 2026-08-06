import { AlertCircle, LoaderCircle, Plus, X } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  useEffect,
  useId,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import { api } from "./api";
import { invalidateControlPlane } from "./controlPlaneQueries";
import { runtimeLabel } from "./format";
import type { CreateTaskInput, Runtime, Worker } from "./types";
import { InlineError } from "./ui";

function preferredRuntime(worker?: Worker): Runtime | "" {
  const ready = (worker?.capabilities ?? []).filter(
    (capability) => capability.kind === "runtime" && capability.status === "ready",
  );
  return (ready.find((capability) => capability.name === worker?.runtime)?.name
    ?? ready[0]?.name
    ?? "") as Runtime | "";
}

export function DelegateModal({
  workers,
  workersPending,
  initialWorkerID,
  onClose,
  onCreated,
}: {
  workers: Worker[];
  workersPending: boolean;
  initialWorkerID?: string;
  onClose: () => void;
  onCreated: (id: string) => void;
}) {
  const queryClient = useQueryClient();
  const titleID = useId();
  const descriptionID = useId();
  const workflowID = useId();
  const runtimeID = useId();
  const titleRef = useRef<HTMLInputElement>(null);
  const modalRef = useRef<HTMLDivElement>(null);
  const requestRef = useRef<{ fingerprint: string; key: string } | undefined>(undefined);
  const [workerID, setWorkerID] = useState(initialWorkerID ?? "");
  const [repositoryID, setRepositoryID] = useState("");
  const [runtime, setRuntime] = useState<Runtime | "">(() =>
    preferredRuntime(workers.find((worker) => worker.id === initialWorkerID)),
  );
  const [timeout, setTimeout] = useState("7200");
  const [workflowRevisionID, setWorkflowRevisionID] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const selectedWorker = workers.find((worker) => worker.id === workerID);
  const availableRuntimes = (selectedWorker?.capabilities ?? [])
    .filter((capability) => capability.kind === "runtime" && capability.status === "ready")
    .map((capability) => capability.name as Runtime);
  const repositoryOptions = useQuery({
    queryKey: ["workers", workerID, "repository-options"],
    queryFn: () => api.workerRepositoryOptions(workerID),
    enabled: Boolean(workerID),
  });
  const workflows = useQuery({
    queryKey: ["workflows", "enabled"],
    queryFn: api.allEnabledWorkflows,
  });

  useEffect(() => {
    titleRef.current?.focus();
  }, []);

  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
      if (event.key !== "Tab") return;
      const focusable = [...(modalRef.current?.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
      ) ?? [])];
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable.at(-1)!;
      if (event.shiftKey && (document.activeElement === first || !modalRef.current?.contains(document.activeElement))) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, [onClose]);

  const create = useMutation({
    mutationFn: api.createTask,
    onSuccess: async (detail) => {
      await invalidateControlPlane(queryClient);
      onCreated(detail.task.id);
    },
  });

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const title = String(form.get("title") ?? "").trim();
    const context = String(form.get("description") ?? "");
    const repository = repositoryOptions.data?.find((option) => option.id === repositoryID);
    const nextErrors: Record<string, string> = {};
    if (!title) nextErrors.title = "Enter a task title.";
    else if (Array.from(title).length > 200) nextErrors.title = "Keep the title to 200 characters.";
    if (!context.trim()) nextErrors.description = "Enter task context.";
    if (!workerID) nextErrors.worker = "Choose a Runner.";
    if (!runtime) nextErrors.runtime = "Choose a ready coding agent.";
    if (!repositoryID) nextErrors.repository = "Choose a repository.";
    else if (!repository) nextErrors.repository = "Choose an available repository.";
    else if (!repository.advertised && !repository.ready) nextErrors.repository = `This repository is unavailable: ${repository.reason}`;
    const timeoutSeconds = Number(timeout);
    if (!Number.isInteger(timeoutSeconds) || timeoutSeconds < 1 || timeoutSeconds > 28_800) {
      nextErrors.timeout = "Choose a timeout from one minute to eight hours.";
    }
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length) return;
    const assignment = repository?.advertised ? {
      worker_id: workerID,
      repository_id: repository.id,
    } : repository ? {
      worker_id: workerID,
      route: {
        repository_remote_identity: repository.remote_identity,
        source_access: { provider: "github", hostname: "github.com" },
      },
    } : undefined;
    if (!assignment) return;
    const payload = {
      title,
      runtime,
      timeout_seconds: timeoutSeconds,
      ...assignment,
      ...(workflowRevisionID
        ? { context, workflow_revision_id: workflowRevisionID }
        : { description: context }),
    };
    const fingerprint = JSON.stringify(payload);
    if (requestRef.current?.fingerprint !== fingerprint) {
      requestRef.current = { fingerprint, key: crypto.randomUUID() };
    }
    const input = {
      request_key: requestRef.current.key,
      ...payload,
    } as CreateTaskInput;
    create.mutate(input);
  };

  return (
    <div className="modal-layer">
      <button className="modal-scrim" aria-label="Close delegate task" onClick={onClose} />
      <div ref={modalRef} className="modal" role="dialog" aria-modal="true" aria-labelledby="delegate-heading">
        <div className="modal-header">
          <div>
            <h2 id="delegate-heading">Delegate task</h2>
          </div>
          <button className="icon-button" aria-label="Close" onClick={onClose}><X size={19} /></button>
        </div>
        <form onSubmit={submit} noValidate>
          <div className="modal-body">
            <Field label="Title" htmlFor={titleID} error={errors.title}>
              <input ref={titleRef} id={titleID} name="title" aria-invalid={Boolean(errors.title)} placeholder="Review this repository" />
            </Field>
            <Field
              label="Runbook"
              htmlFor={workflowID}
              hint="Blank task uses the context as the complete prompt."
            >
              <select
                id={workflowID}
                value={workflowRevisionID}
                onChange={(event) => setWorkflowRevisionID(event.target.value)}
                disabled={workflows.isPending}
              >
                <option value="">Blank task</option>
                {(workflows.data ?? []).map((workflow) => (
                  <option key={workflow.id} value={workflow.current_revision.id}>
                    {workflow.current_revision.title} · revision {workflow.current_revision.revision_number}
                  </option>
                ))}
              </select>
            </Field>
            {workflows.error && <InlineError error={workflows.error} />}
            <Field
              label="Context"
              htmlFor={descriptionID}
              error={errors.description}
              hint={selectedWorker
                ? workflowRevisionID
                  ? `Factory combines this with the selected runbook for ${runtime ? runtimeLabel(runtime) : "the selected coding agent"}.`
                  : `This becomes the ${runtime ? runtimeLabel(runtime) : "selected coding agent"} prompt.`
                : workflowRevisionID
                  ? "Factory combines this with the selected runbook."
                  : "This becomes the selected Runner prompt."}
            >
              <textarea id={descriptionID} name="description" rows={6} aria-invalid={Boolean(errors.description)} placeholder="Describe the outcome, constraints, and checks…" />
            </Field>
            <Field label="Runner" htmlFor="delegate-worker" error={errors.worker}>
              <select
                id="delegate-worker"
                value={workerID}
                onChange={(event) => {
                  setWorkerID(event.target.value);
                  setRepositoryID("");
                  const nextWorker = workers.find((worker) => worker.id === event.target.value);
                  setRuntime(preferredRuntime(nextWorker));
                }}
                disabled={workersPending || workers.length === 0}
              >
                <option value="">{workersPending ? "Loading Runners…" : workers.length ? "Choose a Runner" : "No Runners registered"}</option>
                {workers.map((worker) => (
                  <option key={worker.id} value={worker.id}>
                    {worker.name} · {(worker.capabilities ?? [])
                      .filter((capability) => capability.kind === "runtime" && capability.status === "ready")
                      .map((capability) => runtimeLabel(capability.name)).join(", ") || "no ready agent"} · {worker.online ? "online" : "offline"}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Coding agent" htmlFor={runtimeID} error={errors.runtime}>
              <select
                id={runtimeID}
                value={runtime}
                onChange={(event) => setRuntime(event.target.value as Runtime)}
                disabled={!selectedWorker || availableRuntimes.length === 0}
              >
                <option value="">{selectedWorker ? "Choose a coding agent" : "Choose a Runner first"}</option>
                {availableRuntimes.map((value) => <option key={value} value={value}>{runtimeLabel(value)}</option>)}
              </select>
            </Field>
            {selectedWorker && !selectedWorker.online && (
              <div className="warning-banner compact"><AlertCircle size={16} /> This Runner is offline. The task will queue until it returns.</div>
            )}
            {selectedWorker?.health === "unhealthy" && (
              <div className="warning-banner compact"><AlertCircle size={16} /> This Runner is unhealthy and will not claim work until it recovers.</div>
            )}
            <Field label="Repository" htmlFor="delegate-repository" error={errors.repository}>
              <select id="delegate-repository" value={repositoryID} onChange={(event) => setRepositoryID(event.target.value)} disabled={!workerID || repositoryOptions.isPending}>
                <option value="">{workerID ? repositoryOptions.isPending ? "Loading repositories…" : repositoryOptions.data?.length ? "Choose a repository" : "No repositories configured" : "Choose a Runner first"}</option>
                {(repositoryOptions.data ?? []).map((repository) => (
                  <option
                    key={repository.id}
                    value={repository.id}
                    disabled={!repository.advertised && !repository.ready}
                  >
                    {repository.advertised
                      ? `${repository.key ?? repository.remote_identity} · ${repository.remote_identity}`
                      : `${repository.remote_identity} · ${repository.ready ? "acquired on demand" : repository.reason}`}
                  </option>
                ))}
              </select>
            </Field>
            {repositoryOptions.error && <InlineError error={repositoryOptions.error} />}
            <Field label="Timeout" htmlFor="delegate-timeout" error={errors.timeout}>
              <select id="delegate-timeout" value={timeout} onChange={(event) => setTimeout(event.target.value)}>
                <option value="1800">30 minutes</option>
                <option value="3600">1 hour</option>
                <option value="7200">2 hours</option>
                <option value="14400">4 hours</option>
                <option value="28800">8 hours</option>
              </select>
            </Field>
            {create.error && <InlineError error={create.error} />}
          </div>
          <div className="modal-footer">
            <button type="button" className="button button-secondary" onClick={onClose}>Cancel</button>
            <button type="submit" className="button button-primary" disabled={create.isPending || workers.length === 0}>
              {create.isPending ? <><LoaderCircle size={16} className="spin" /> Delegating…</> : <><Plus size={16} /> Delegate task</>}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function Field({
  label,
  htmlFor,
  error,
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  error?: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <div className="field">
      <label htmlFor={htmlFor}>{label}</label>
      {children}
      {error ? <span className="field-error">{error}</span> : hint ? <span className="field-hint">{hint}</span> : null}
    </div>
  );
}
