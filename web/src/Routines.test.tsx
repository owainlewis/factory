import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { RoutinesView } from "./Routines";
import type { Routine } from "./types";

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
  it("reuses the Run request key after an ambiguous failure", async () => {
    const runnable = { ...routine, repository_count: 1 };
    vi.spyOn(api, "routines").mockResolvedValue([runnable]);
    const runRoutine = vi.spyOn(api, "runRoutine").mockRejectedValue(new Error("The response was lost."));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><RoutinesView onWork={() => undefined} /></QueryClientProvider>);

    await userEvent.click(await screen.findByRole("button", { name: "Run now" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("The response was lost.");
    await userEvent.click(screen.getByRole("button", { name: "Run now" }));
    expect(runRoutine).toHaveBeenCalledTimes(2);
    expect(runRoutine.mock.calls[1][1]).toBe(runRoutine.mock.calls[0][1]);
  });

  it("uses a new Run request key after the Routine generation changes", async () => {
    const runnable = { ...routine, repository_count: 1 };
    vi.spyOn(api, "routines").mockResolvedValue([runnable]);
    const runRoutine = vi.spyOn(api, "runRoutine").mockRejectedValue(new Error("The response was lost."));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={client}><RoutinesView onWork={() => undefined} /></QueryClientProvider>);

    await userEvent.click(await screen.findByRole("button", { name: "Run now" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("The response was lost.");
    client.setQueryData(["routines", false], [{ ...runnable, name: "Updated Routine", generation: runnable.generation + 1 }]);
    expect(await screen.findByText("Updated Routine")).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "Run now" }));
    expect(runRoutine).toHaveBeenCalledTimes(2);
    expect(runRoutine.mock.calls[1][1]).not.toBe(runRoutine.mock.calls[0][1]);
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
});
