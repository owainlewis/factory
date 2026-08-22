import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { RunDetailView, RunsView } from "./Runs";
import type { AttemptEvent, RunDetail, Run } from "./types";

const headRun = run("run-head", "Current Run", "running");
const olderRun = run("run-older", "Older Run", "succeeded");

describe("Runs", () => {
  it("shows actionable software work on the board and opens a Run", async () => {
    const blocked = {
      ...run("run-blocked", "Fix release build", "blocked"),
      needs_attention: true,
      task: {
        ...run("run-blocked", "Fix release build", "blocked").task,
        repositories: [{ id: "repository-1", remote_identity: "github.com/example/factory.git" }],
      },
    };
    const running = run("run-running", "Review pull request", "running");
    const queued = run("run-queued", "Plan migration", "queued");
    const succeeded = run("run-succeeded", "Ship documentation", "succeeded");
    const capacityBlocked = run("run-capacity", "Wait for capacity", "blocked");
    const historicalFailure = run("run-historical-failure", "Old failed run", "failed");
    vi.spyOn(api, "runs").mockResolvedValue({ runs: [blocked, running, queued, succeeded, capacityBlocked, historicalFailure], next_cursor: null });
    const onRun = vi.fn();
    const client = testClient();

    render(<QueryClientProvider client={client}><RunsView mode="kanban" onMode={() => undefined} onRun={onRun} /></QueryClientProvider>);

    expect(await screen.findByRole("heading", { name: "Work" })).toBeVisible();
    expect(screen.getByRole("region", { name: "Work summary" })).toHaveTextContent("Needs attention1Running1Queued2Done2");
    expect(screen.getByRole("region", { name: "Needs attention" })).toContainElement(screen.getByRole("button", { name: /Fix release build, blocked/ }));
    expect(screen.getByRole("region", { name: "Queued" })).toContainElement(screen.getByRole("button", { name: /Wait for capacity, blocked/ }));
    expect(screen.getByRole("region", { name: "Done" })).toContainElement(screen.getByRole("button", { name: /Old failed run, failed/ }));
    expect(screen.getByText("factory", { exact: true })).toBeVisible();
    expect(screen.getAllByText("codex", { exact: true }).length).toBeGreaterThan(0);

    await userEvent.click(screen.getByRole("button", { name: /Fix release build, blocked/ }));
    expect(onRun).toHaveBeenCalledWith("run-blocked");
  });

  it("refreshes terminal history until it no longer needs attention", async () => {
    const attention = { ...run("run-attention", "Recent failed run", "failed"), needs_attention: true };
    const settled = { ...attention, needs_attention: false };
    vi.spyOn(api, "runs").mockResolvedValue({ runs: [], next_cursor: null });
    const runDetail = vi.spyOn(api, "run").mockResolvedValue({ run: settled, sessions: [] });
    const client = testClient();
    client.setQueryData(["run-history"], { items: [attention], cursor: null, headCursor: null });

    render(<QueryClientProvider client={client}><RunsView mode="kanban" onMode={() => undefined} onRun={() => undefined} /></QueryClientProvider>);

    expect(await screen.findByRole("region", { name: "Done" })).toContainElement(await screen.findByRole("button", { name: /Recent failed run, failed/ }));
    expect(runDetail).toHaveBeenCalledWith("run-attention");
  });

  it("loads Run history one cursor-bounded page at a time", async () => {
    const runs = vi.spyOn(api, "runs").mockImplementation(async (cursor = "") => cursor ? {
      runs: [olderRun],
      next_cursor: null,
    } : {
      runs: [headRun],
      next_cursor: "older-cursor",
    });
    const client = testClient();
    render(<QueryClientProvider client={client}><RunsView mode="table" onMode={() => undefined} onRun={() => undefined} /></QueryClientProvider>);

    expect(await screen.findByText("Current Run")).toBeVisible();
    expect(screen.queryByText("Older Run")).not.toBeInTheDocument();
    expect(runs).toHaveBeenCalledTimes(1);

    await userEvent.click(await screen.findByRole("button", { name: "Load more Runs" }));

    expect(await screen.findByText("Older Run")).toBeVisible();
    expect(runs).toHaveBeenNthCalledWith(2, "older-cursor");
    expect(screen.queryByRole("button", { name: "Load more Runs" })).not.toBeInTheDocument();
  });

  it("polls active attempt output after the last cached event", async () => {
    const eventZero: AttemptEvent = {
      sequence: 0,
      kind: "progress",
      payload: "First update",
      server_time: "2026-08-11T12:00:00Z",
    };
    const eventOne: AttemptEvent = {
      sequence: 1,
      kind: "progress",
      payload: "Second update",
      server_time: "2026-08-11T12:00:01Z",
    };
    vi.spyOn(api, "run").mockResolvedValue(runDetail());
    const events = vi.spyOn(api, "events").mockImplementation(async (_attemptID, after) => after < 0 ? {
      events: [eventZero], next_after: 0, has_more: false,
    } : {
      events: after === 0 ? [eventOne] : [], next_after: Math.max(after, 1), has_more: false,
    });
    const client = testClient();
    render(<QueryClientProvider client={client}><RunDetailView id={headRun.id} onBack={() => undefined} /></QueryClientProvider>);

    await userEvent.click(await screen.findByText("github.com/example/factory"));
    expect(await screen.findByText("First update")).toBeVisible();
    expect(events).toHaveBeenLastCalledWith("attempt-1", -1);

    await client.refetchQueries({ queryKey: ["attempt-events", "attempt-1"] });

    expect(await screen.findByText("Second update")).toBeVisible();
    expect(events).toHaveBeenLastCalledWith("attempt-1", 0);
  });

  it("fetches final attempt output once when an attempt becomes terminal", async () => {
    const eventZero: AttemptEvent = {
      sequence: 0,
      kind: "progress",
      payload: "Still working",
      server_time: "2026-08-11T12:00:00Z",
    };
    const finalEvent: AttemptEvent = {
      sequence: 1,
      kind: "result",
      payload: "Finished",
      server_time: "2026-08-11T12:00:01Z",
    };
    let attemptState: "running" | "succeeded" = "running";
    vi.spyOn(api, "run").mockImplementation(async () => runDetail(attemptState));
    const events = vi.spyOn(api, "events").mockImplementation(async (_attemptID, after) => after < 0 ? {
      events: [eventZero], next_after: 0, has_more: false,
    } : {
      events: after === 0 ? [finalEvent] : [], next_after: Math.max(after, 1), has_more: false,
    });
    const client = testClient();
    render(<QueryClientProvider client={client}><RunDetailView id={headRun.id} onBack={() => undefined} /></QueryClientProvider>);

    await userEvent.click(await screen.findByText("github.com/example/factory"));
    expect(await screen.findByText("Still working")).toBeVisible();

    attemptState = "succeeded";
    await client.refetchQueries({ queryKey: ["runs", headRun.id] });

    expect(await screen.findByText("Finished")).toBeVisible();
    expect(events).toHaveBeenCalledTimes(2);
    expect(events).toHaveBeenLastCalledWith("attempt-1", 0);
  });

  it("shows the frozen execution destination in Run detail", async () => {
    const cloud = runDetail();
    cloud.run = {
      ...cloud.run,
      execution: {
        profile_id: "profile-cloud-1",
        profile_version: 1,
        backend: "fake_cloud_run",
        runtime: "codex",
        provider: "openrouter",
        model: "deepseek/test",
        timeout_seconds: 7200,
        resource_class: "standard",
        commit_resolution_policy: "frozen_commit",
      },
    };
    vi.spyOn(api, "run").mockResolvedValue(cloud);
    const client = testClient();
    render(<QueryClientProvider client={client}><RunDetailView id={headRun.id} onBack={() => undefined} /></QueryClientProvider>);

    expect(await screen.findByText(/Cloud Run · openrouter \/ deepseek\/test/)).toBeVisible();
  });
});

function testClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
}

function run(id: string, name: string, state: Run["state"]): Run {
  return {
    id,
    task_id: "task-1",
    task: {
      id: "task-1",
      name,
      prompt: "Review.",
      runtime: "codex",
      timeout_seconds: 7200,
      concurrency_limit: 10,
      generation: 1,
      repositories: [],
    },
    execution: {
      profile_id: "persistent-auto",
      profile_version: 1,
      backend: "persistent",
      runtime: "codex",
      provider: "worker",
      model: "worker-default",
      timeout_seconds: 7200,
      resource_class: "worker",
      commit_resolution_policy: "resolve_per_attempt",
    },
    source: "manual",
    state,
    needs_attention: false,
    session_count: 1,
    succeeded_count: state === "succeeded" ? 1 : 0,
    failed_count: 0,
    cancelled_count: 0,
    active_count: state === "succeeded" ? 0 : 1,
    admitted_at: "2026-08-11T12:00:00Z",
    updated_at: "2026-08-11T12:00:00Z",
  };
}

function runDetail(attemptState: "running" | "succeeded" = "running"): RunDetail {
  return {
    run: headRun,
    sessions: [{
      id: "session-1",
      run_id: headRun.id,
      repository_id: "repository-1",
      repository_identity: "github.com/example/factory",
      required_runtime: "codex",
      execution: headRun.execution,
      timeout_seconds: 7200,
      state: "running",
      assigned_worker_id: "worker-1",
      cancellation_requested: false,
      retry_may_repeat_effects: false,
      admitted_at: "2026-08-11T12:00:00Z",
      started_at: "2026-08-11T12:00:00Z",
      attempts: [{
        id: "attempt-1",
        execution_id: "execution-1",
        worker_id: "worker-1",
        attempt_number: 1,
        state: attemptState,
        lease_expires_at: "2026-08-11T12:01:00Z",
        started_at: "2026-08-11T12:00:00Z",
        created_at: "2026-08-11T12:00:00Z",
      }],
    }],
  };
}
