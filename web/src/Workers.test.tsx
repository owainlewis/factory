import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { WorkerDetail, WorkersView } from "./Workers";
import type { Worker } from "./types";

const worker: Worker = {
  id: "worker-1",
  name: "local-claude",
  labels: {},
  worker_version: "1.0.0",
  runtime: "claude-code",
  runtime_version: "2.0.0",
  capabilities: [{ kind: "runtime", name: "claude-code", status: "ready", version: "2.0.0" }],
  capacity: 1,
  active_count: 0,
  health: "healthy",
  online: true,
  repositories: [],
  source_access: [],
  retained_worktrees: [],
  registered_at: "2026-08-18T06:00:00Z",
  last_heartbeat: "2026-08-18T06:00:10Z",
};

const successBanner = "Worker is online with at least one ready coding agent.";
const failureBanner = "Worker connection failed. Check its status and capability guidance below.";

function renderDetail(client: QueryClient) {
  return render(
    <QueryClientProvider client={client}>
      <WorkerDetail id={worker.id} legacyReadOnly={false} onBack={() => undefined} onDelegate={() => undefined} />
    </QueryClientProvider>,
  );
}

function newClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
}

describe("WorkerDetail connection test", () => {
  it("keeps the passing verdict when a later poll only advances the heartbeat", async () => {
    vi.spyOn(api, "worker").mockResolvedValue(worker);
    vi.spyOn(api, "testWorker").mockResolvedValue({ ...worker, last_heartbeat: "2026-08-18T06:00:20Z" });
    const client = newClient();
    renderDetail(client);

    await userEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText(successBanner)).toBeVisible();

    client.setQueryData(["worker", worker.id], { ...worker, last_heartbeat: "2026-08-18T06:00:30Z" });

    expect(await screen.findByText(successBanner)).toBeVisible();
  });

  it("retires the passing verdict once the Worker goes offline", async () => {
    vi.spyOn(api, "worker").mockResolvedValue(worker);
    vi.spyOn(api, "testWorker").mockResolvedValue({ ...worker, last_heartbeat: "2026-08-18T06:00:20Z" });
    const client = newClient();
    renderDetail(client);

    await userEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText(successBanner)).toBeVisible();

    client.setQueryData(["worker", worker.id], { ...worker, online: false });

    await vi.waitFor(() => expect(screen.queryByText(successBanner)).toBeNull());
  });

  it("retires the passing verdict once health changes", async () => {
    vi.spyOn(api, "worker").mockResolvedValue(worker);
    vi.spyOn(api, "testWorker").mockResolvedValue({ ...worker, last_heartbeat: "2026-08-18T06:00:20Z" });
    const client = newClient();
    renderDetail(client);

    await userEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText(successBanner)).toBeVisible();

    client.setQueryData(["worker", worker.id], { ...worker, health: "unhealthy" });

    await vi.waitFor(() => expect(screen.queryByText(successBanner)).toBeNull());
  });

  it("retires the passing verdict once a runtime capability stops being ready", async () => {
    vi.spyOn(api, "worker").mockResolvedValue(worker);
    vi.spyOn(api, "testWorker").mockResolvedValue({ ...worker, last_heartbeat: "2026-08-18T06:00:20Z" });
    const client = newClient();
    renderDetail(client);

    await userEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText(successBanner)).toBeVisible();

    client.setQueryData(["worker", worker.id], {
      ...worker,
      capabilities: [{ kind: "runtime", name: "claude-code", status: "missing" }],
    });

    await vi.waitFor(() => expect(screen.queryByText(successBanner)).toBeNull());
  });

  it("keeps the failing verdict when the request fails", async () => {
    const offline = { ...worker, online: false };
    vi.spyOn(api, "worker").mockResolvedValue(offline);
    vi.spyOn(api, "testWorker").mockRejectedValue(new Error("The Worker did not report a heartbeat."));
    const client = newClient();
    renderDetail(client);

    await userEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText(failureBanner)).toBeVisible();

    client.setQueryData(["worker", offline.id], { ...offline, last_heartbeat: "2026-08-18T06:00:30Z" });

    expect(await screen.findByText(failureBanner)).toBeVisible();
  });
});

describe("stale health for an offline Worker", () => {
  const offline: Worker = { ...worker, online: false, last_heartbeat: "2026-08-06T06:00:00Z" };

  it("does not present the list row health as a current healthy reading", async () => {
    const client = newClient();
    render(
      <QueryClientProvider client={client}>
        <WorkersView
          workers={[offline]}
          pending={false}
          error={null}
          fetching={false}
          updatedAt={Date.parse("2026-08-18T06:00:30Z")}
          onWorker={() => undefined}
          onRefresh={() => undefined}
        />
      </QueryClientProvider>,
    );

    const row = screen.getByRole("button", { name: /local-claude/ });
    expect(within(row).queryByText("Healthy")).toBeNull();
    const health = within(row).getByText("Unknown");
    expect(health).not.toHaveClass("healthy-text");
    expect(health).toHaveClass("stale-text");
  });

  async function findStateLine(container: HTMLElement) {
    await screen.findByRole("heading", { name: worker.name });
    return container.querySelector(".worker-state-line") as HTMLElement;
  }

  it("does not present the detail heading health as a current healthy reading", async () => {
    vi.spyOn(api, "worker").mockResolvedValue(offline);
    const { container } = renderDetail(newClient());

    const stateLine = await findStateLine(container);
    const health = within(stateLine).getByText("Unknown");
    expect(health).not.toHaveClass("healthy-text");
    expect(health).toHaveClass("stale-text");
    expect(within(stateLine).queryByText("Healthy")).toBeNull();
  });

  it("keeps the healthy presentation for an online Worker", async () => {
    vi.spyOn(api, "worker").mockResolvedValue(worker);
    const { container } = renderDetail(newClient());

    const health = within(await findStateLine(container)).getByText("Healthy");
    expect(health).toHaveClass("healthy-text");
  });

  it("keeps the danger presentation for an online unhealthy Worker", async () => {
    vi.spyOn(api, "worker").mockResolvedValue({ ...worker, health: "unhealthy" });
    const { container } = renderDetail(newClient());

    const health = within(await findStateLine(container)).getByText("Unhealthy");
    expect(health).toHaveClass("danger-text");
  });
});
