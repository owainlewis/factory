import { AlertCircle, LoaderCircle, Plus, X } from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
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
import type { CreateTaskInput, Worker } from "./types";
import { InlineError } from "./ui";

export function DelegateDrawer({
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
  const titleRef = useRef<HTMLInputElement>(null);
  const drawerRef = useRef<HTMLElement>(null);
  const requestRef = useRef<{ fingerprint: string; key: string } | undefined>(undefined);
  const [workerID, setWorkerID] = useState(initialWorkerID ?? "");
  const [repositoryID, setRepositoryID] = useState("");
  const [timeout, setTimeout] = useState("7200");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const selectedWorker = workers.find((worker) => worker.id === workerID);
  const repositories = selectedWorker?.repositories ?? [];

  useEffect(() => {
    titleRef.current?.focus();
  }, []);

  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
      if (event.key !== "Tab") return;
      const focusable = [...(drawerRef.current?.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
      ) ?? [])];
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable.at(-1)!;
      if (event.shiftKey && (document.activeElement === first || !drawerRef.current?.contains(document.activeElement))) {
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
    const description = String(form.get("description") ?? "");
    const nextErrors: Record<string, string> = {};
    if (!title) nextErrors.title = "Enter a task title.";
    else if (Array.from(title).length > 200) nextErrors.title = "Keep the title to 200 characters.";
    if (!description.trim()) nextErrors.description = "Enter a task description.";
    if (!workerID) nextErrors.worker = "Choose a worker.";
    if (!repositoryID) nextErrors.repository = "Choose a repository.";
    const timeoutSeconds = Number(timeout);
    if (!Number.isInteger(timeoutSeconds) || timeoutSeconds < 1 || timeoutSeconds > 28_800) {
      nextErrors.timeout = "Choose a timeout from one minute to eight hours.";
    }
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length) return;
    const payload = {
      title,
      description,
      worker_id: workerID,
      repository_id: repositoryID,
      timeout_seconds: timeoutSeconds,
    };
    const fingerprint = JSON.stringify(payload);
    if (requestRef.current?.fingerprint !== fingerprint) {
      requestRef.current = { fingerprint, key: crypto.randomUUID() };
    }
    const input: CreateTaskInput = {
      request_key: requestRef.current.key,
      ...payload,
    };
    create.mutate(input);
  };

  return (
    <div className="drawer-layer">
      <button className="drawer-scrim" aria-label="Close delegate task" onClick={onClose} />
      <aside ref={drawerRef} className="drawer" role="dialog" aria-modal="true" aria-labelledby="delegate-heading">
        <div className="drawer-header">
          <div>
            <span className="eyebrow">New work</span>
            <h2 id="delegate-heading">Delegate task</h2>
          </div>
          <button className="icon-button" aria-label="Close" onClick={onClose}><X size={19} /></button>
        </div>
        <form onSubmit={submit} noValidate>
          <div className="drawer-body">
            <Field label="Title" htmlFor={titleID} error={errors.title}>
              <input ref={titleRef} id={titleID} name="title" aria-invalid={Boolean(errors.title)} placeholder="Fix stale worker status" />
            </Field>
            <Field
              label="Description"
              htmlFor={descriptionID}
              error={errors.description}
              hint={selectedWorker
                ? `This becomes the ${runtimeLabel(selectedWorker.runtime)} prompt.`
                : "This becomes the selected worker runtime prompt."}
            >
              <textarea id={descriptionID} name="description" rows={9} aria-invalid={Boolean(errors.description)} placeholder="Describe the outcome, constraints, and checks…" />
            </Field>
            <Field label="Worker" htmlFor="delegate-worker" error={errors.worker}>
              <select
                id="delegate-worker"
                value={workerID}
                onChange={(event) => {
                  setWorkerID(event.target.value);
                  setRepositoryID("");
                }}
                disabled={workersPending || workers.length === 0}
              >
                <option value="">{workersPending ? "Loading workers…" : workers.length ? "Choose a worker" : "No workers registered"}</option>
                {workers.map((worker) => (
                  <option key={worker.id} value={worker.id}>
                    {worker.name} · {runtimeLabel(worker.runtime)} · {worker.online ? "online" : "offline"}
                  </option>
                ))}
              </select>
            </Field>
            {selectedWorker && !selectedWorker.online && (
              <div className="warning-banner compact"><AlertCircle size={16} /> This worker is offline. The task will queue until it returns.</div>
            )}
            {selectedWorker?.health === "unhealthy" && (
              <div className="warning-banner compact"><AlertCircle size={16} /> This worker is unhealthy and will not claim work until it recovers.</div>
            )}
            <Field label="Repository" htmlFor="delegate-repository" error={errors.repository}>
              <select id="delegate-repository" value={repositoryID} onChange={(event) => setRepositoryID(event.target.value)} disabled={!workerID}>
                <option value="">{workerID ? (repositories.length ? "Choose a repository" : "No repositories advertised") : "Choose a worker first"}</option>
                {repositories.map((repo) => <option key={repo.id} value={repo.id}>{repo.key} · {repo.remote_identity}</option>)}
              </select>
            </Field>
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
          <div className="drawer-footer">
            <button type="button" className="button button-secondary" onClick={onClose}>Cancel</button>
            <button type="submit" className="button button-primary" disabled={create.isPending || workers.length === 0}>
              {create.isPending ? <><LoaderCircle size={16} className="spin" /> Delegating…</> : <><Plus size={16} /> Delegate task</>}
            </button>
          </div>
        </form>
      </aside>
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
