import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { RoutinesView } from "./Routines";
import type { ExecutionProfile, Routine, WorkDetailV2 } from "./types";

const executionProfiles: ExecutionProfile[] = [{
  id: "persistent-auto",
  name: "Persistent auto",
  kind: "persistent",
  version: 1,
  runtime: "",
  provider: "worker",
  model: "worker-default",
  timeout_seconds: 0,
  resource_class: "worker",
  max_concurrent: 100,
  enabled: true,
  healthy: true,
  synthetic_worker_id: "",
}, {
  id: "profile-cloud-1",
  name: "Cloud Run test profile",
  kind: "fake_cloud_run",
  version: 1,
  runtime: "codex",
  provider: "openrouter",
  model: "deepseek/test",
  timeout_seconds: 900,
  resource_class: "standard",
  max_concurrent: 10,
  enabled: true,
  healthy: true,
  synthetic_worker_id: "cloud-run-profile-cloud-1",
}];

const routine: Routine = {
  id: "routine-1",
  name: "Ship ready work",
  prompt: "Find ready work.",
  prompt_preview: "Find ready work.",
  runtime: "codex",
  timeout_seconds: 7200,
  concurrency_limit: 10,
  generation: 2,
  archived: false,
  read_only: false,
  repositories: [],
  repository_count: 0,
  schedule: { enabled: false, health_status: "disabled" },
  created_at: "2026-08-11T12:00:00Z",
  updated_at: "2026-08-11T12:00:00Z",
};

describe("RoutinesView", () => {
  beforeEach(() => {
    vi.spyOn(api, "executionProfiles").mockResolvedValue(executionProfiles);
  });

  it("reuses the Run request key after an ambiguous failure", async () => {
    const runnable = { ...routine, repository_count: 1 };
    vi.spyOn(api, "routines").mockResolvedValue([runnable]);
    const runRoutine = vi.spyOn(api, "runRoutine").mockRejectedValue(new Error("The response was lost."));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><RoutinesView onWork={() => undefined} /></QueryClientProvider>);

    await userEvent.click(await screen.findByRole("button", { name: "Run now" }));
    const dialog = await screen.findByRole("dialog", { name: `Run ${runnable.name}` });
    await userEvent.click(within(dialog).getByRole("button", { name: "Run now" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("The response was lost.");
    await userEvent.click(within(dialog).getByRole("button", { name: "Run now" }));
    expect(runRoutine).toHaveBeenCalledTimes(2);
    expect(runRoutine.mock.calls[1][1]).toBe(runRoutine.mock.calls[0][1]);
    expect(runRoutine).toHaveBeenLastCalledWith(runnable.id, expect.any(String), "persistent-auto");
  });

  it("uses a new Run request key after the Routine generation changes", async () => {
    const runnable = { ...routine, repository_count: 1 };
    vi.spyOn(api, "routines").mockResolvedValue([runnable]);
    const runRoutine = vi.spyOn(api, "runRoutine").mockRejectedValue(new Error("The response was lost."));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><RoutinesView onWork={() => undefined} /></QueryClientProvider>);

    await userEvent.click(await screen.findByRole("button", { name: "Run now" }));
    let dialog = await screen.findByRole("dialog", { name: `Run ${runnable.name}` });
    await userEvent.click(within(dialog).getByRole("button", { name: "Run now" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("The response was lost.");
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    client.setQueryData(["routines", false], [{ ...runnable, name: "Updated Routine", generation: runnable.generation + 1 }]);
    expect(await screen.findByText("Updated Routine")).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "Run now" }));
    dialog = await screen.findByRole("dialog", { name: "Run Updated Routine" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Run now" }));
    expect(runRoutine).toHaveBeenCalledTimes(2);
    expect(runRoutine.mock.calls[1][1]).not.toBe(runRoutine.mock.calls[0][1]);
  });

  it("uses a new Run request key when the execution destination changes", async () => {
    const runnable = { ...routine, repository_count: 1 };
    vi.spyOn(api, "routines").mockResolvedValue([runnable]);
    const runRoutine = vi.spyOn(api, "runRoutine").mockRejectedValue(new Error("The response was lost."));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><RoutinesView onWork={() => undefined} /></QueryClientProvider>);

    await userEvent.click(await screen.findByRole("button", { name: "Run now" }));
    let dialog = await screen.findByRole("dialog", { name: `Run ${runnable.name}` });
    await userEvent.selectOptions(within(dialog).getByLabelText("Run on"), "profile-cloud-1");
    await userEvent.click(within(dialog).getByRole("button", { name: "Run now" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("The response was lost.");
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));

    await userEvent.click(screen.getByRole("button", { name: "Run now" }));
    dialog = await screen.findByRole("dialog", { name: `Run ${runnable.name}` });
    await userEvent.click(within(dialog).getByRole("button", { name: "Run now" }));

    expect(runRoutine.mock.calls[0][2]).toBe("profile-cloud-1");
    expect(runRoutine.mock.calls[1][2]).toBe("persistent-auto");
    expect(runRoutine.mock.calls[1][1]).not.toBe(runRoutine.mock.calls[0][1]);
  });

  it("lets a manual run override the saved execution destination", async () => {
    const runnable = { ...routine, repository_count: 1 };
    vi.spyOn(api, "routines").mockResolvedValue([runnable]);
    const result = { work: { id: "work-cloud-1" }, targets: [] } as unknown as WorkDetailV2;
    const runRoutine = vi.spyOn(api, "runRoutine").mockResolvedValue(result);
    const onWork = vi.fn();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><RoutinesView onWork={onWork} /></QueryClientProvider>);

    await userEvent.click(await screen.findByRole("button", { name: "Run now" }));
    const dialog = await screen.findByRole("dialog", { name: `Run ${runnable.name}` });
    await userEvent.selectOptions(within(dialog).getByLabelText("Run on"), "profile-cloud-1");
    expect(within(dialog).getByText("codex · openrouter / deepseek/test")).toBeVisible();
    await userEvent.click(within(dialog).getByRole("button", { name: "Run now" }));

    expect(runRoutine).toHaveBeenCalledWith(runnable.id, expect.any(String), "profile-cloud-1");
    expect(onWork).toHaveBeenCalledWith("work-cloud-1");
  });

  it("keeps the editor open and shows archive failures", async () => {
    vi.spyOn(api, "routines").mockResolvedValue([routine]);
    vi.spyOn(api, "routine").mockResolvedValue(routine);
    vi.spyOn(api, "repositories").mockResolvedValue([]);
    vi.spyOn(api, "archiveRoutine").mockRejectedValue(new Error("Routine changed; refresh and try again."));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><RoutinesView initialID={routine.id} onWork={() => undefined} /></QueryClientProvider>);

    const dialog = await screen.findByRole("dialog", { name: "Edit Routine" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Archive" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent("Routine changed; refresh and try again.");
    expect(screen.getByRole("dialog", { name: "Edit Routine" })).toBeVisible();
  });

  it("keeps the editor open and shows occurrence discard failures", async () => {
    const blocked: Routine = {
      ...routine,
      schedule: {
        enabled: true,
        cron: "0 9 * * *",
        timezone: "UTC",
        pending_due_at: "2026-08-11T09:00:00Z",
        health_status: "blocked",
        health_message: "Repository unavailable.",
      },
    };
    vi.spyOn(api, "routines").mockResolvedValue([blocked]);
    vi.spyOn(api, "routine").mockResolvedValue(blocked);
    vi.spyOn(api, "repositories").mockResolvedValue([]);
    vi.spyOn(api, "discardRoutineOccurrence").mockRejectedValue(new Error("The pending occurrence changed."));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><RoutinesView initialID={blocked.id} onWork={() => undefined} /></QueryClientProvider>);

    const dialog = await screen.findByRole("dialog", { name: "Edit Routine" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Discard occurrence" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent("The pending occurrence changed.");
    expect(screen.getByRole("dialog", { name: "Edit Routine" })).toBeVisible();
  });

  it("preserves the saved execution profile when editing a Routine", async () => {
    const cloudRoutine: Routine = { ...routine, execution_profile_id: "profile-cloud-1" };
    vi.spyOn(api, "routines").mockResolvedValue([cloudRoutine]);
    vi.spyOn(api, "routine").mockResolvedValue(cloudRoutine);
    vi.spyOn(api, "repositories").mockResolvedValue([]);
    const updateRoutine = vi.spyOn(api, "updateRoutine").mockResolvedValue(cloudRoutine);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><RoutinesView initialID={cloudRoutine.id} onWork={() => undefined} /></QueryClientProvider>);

    const dialog = await screen.findByRole("dialog", { name: "Edit Routine" });
    expect(within(dialog).getByLabelText("Run on")).toHaveValue("profile-cloud-1");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save Routine" }));

    expect(updateRoutine).toHaveBeenCalledWith(cloudRoutine.id, expect.objectContaining({
      execution_profile_id: "profile-cloud-1",
    }));
  });

  it("saves a selected default execution destination on a new Routine", async () => {
    vi.spyOn(api, "routines").mockResolvedValue([]);
    vi.spyOn(api, "repositories").mockResolvedValue([]);
    const createRoutine = vi.spyOn(api, "createRoutine").mockResolvedValue({ ...routine, execution_profile_id: "profile-cloud-1" });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><RoutinesView createOpen onWork={() => undefined} /></QueryClientProvider>);

    const dialog = await screen.findByRole("dialog", { name: "New Routine" });
    await userEvent.type(within(dialog).getByLabelText("Name"), "Cloud review");
    await userEvent.type(within(dialog).getByLabelText("Prompt"), "Review the repository.");
    await userEvent.selectOptions(within(dialog).getByLabelText("Run on"), "profile-cloud-1");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save Routine" }));

    expect(createRoutine).toHaveBeenCalledWith(expect.objectContaining({ execution_profile_id: "profile-cloud-1" }));
  });
});
