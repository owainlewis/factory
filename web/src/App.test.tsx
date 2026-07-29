import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { App } from "./App";
import { mockControlPlane } from "./test/fixtures";

function renderApp() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return {
    ...render(
    <QueryClientProvider client={client}>
      <App />
    </QueryClientProvider>,
    ),
    client,
  };
}

describe("App", () => {
  it("renders every task status in the operational board", async () => {
    mockControlPlane();
    renderApp();

    for (const state of ["Queued", "Running", "Succeeded", "Failed", "Cancelled"]) {
      const column = await screen.findByRole("region", { name: new RegExp(`^${state}`) });
      expect(within(column).getByText(`${state.toLowerCase()} task`)).toBeVisible();
      expect(within(column).getByText(state, { selector: ".status-badge" })).toBeVisible();
    }
  });

  it("restricts repositories to the selected worker and warns for offline work", async () => {
    mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Delegate task" }));
    const dialog = screen.getByRole("dialog", { name: "Delegate task" });
    await user.selectOptions(within(dialog).getByLabelText("Worker"), "worker-online");
    const repository = within(dialog).getByLabelText("Repository");
    expect(within(repository).getByRole("option", { name: /factory/ })).toBeInTheDocument();
    expect(within(repository).queryByRole("option", { name: /archive/ })).not.toBeInTheDocument();

    await user.selectOptions(within(dialog).getByLabelText("Worker"), "worker-offline");
    expect(within(dialog).getByText(/task will queue until it returns/i)).toBeVisible();
    expect(within(repository).getByRole("option", { name: /archive/ })).toBeInTheDocument();
    expect(within(repository).queryByRole("option", { name: /factory/ })).not.toBeInTheDocument();
  });

  it("validates the delegate form and creates a normalized task", async () => {
    const fetch = mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Delegate task" }));
    const dialog = screen.getByRole("dialog", { name: "Delegate task" });
    await user.click(within(dialog).getByRole("button", { name: "Delegate task" }));
    expect(within(dialog).getByText("Enter a task title.")).toBeVisible();
    expect(within(dialog).getByText("Enter a task description.")).toBeVisible();

    await user.type(within(dialog).getByLabelText("Title"), "Ship the UI");
    await user.type(within(dialog).getByLabelText("Description"), "Build and verify the real interface.");
    await user.selectOptions(within(dialog).getByLabelText("Worker"), "worker-online");
    await user.selectOptions(within(dialog).getByLabelText("Repository"), "repo-factory");
    await user.click(within(dialog).getByRole("button", { name: "Delegate task" }));

    expect(await screen.findByRole("heading", { name: "Ship the UI" })).toBeVisible();
    expect(screen.getByText("Progress will appear when the worker starts this task.")).toBeVisible();
    const createCall = fetch.mock.calls.find(([, init]) => init?.method === "POST");
    expect(createCall).toBeDefined();
    expect(JSON.parse(String(createCall?.[1]?.body))).toMatchObject({
      title: "Ship the UI",
      description: "Build and verify the real interface.",
      worker_id: "worker-online",
      repository_id: "repo-factory",
      timeout_seconds: 7200,
    });
  });

  it("closes the keyboard-accessible drawer with Escape", async () => {
    mockControlPlane();
    const user = userEvent.setup();
    renderApp();
    await user.click(await screen.findByRole("button", { name: "Delegate task" }));
    expect(screen.getByRole("dialog")).toBeVisible();
    expect(screen.getByLabelText("Title")).toHaveFocus();
    await user.tab({ shift: true });
    expect(screen.getByRole("button", { name: "Close" })).toHaveFocus();
    await user.tab({ shift: true });
    expect(within(screen.getByRole("dialog")).getByRole("button", { name: "Delegate task" })).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("reuses the request key after an ambiguous create failure and accepts 200 Unicode characters", async () => {
    const fetch = mockControlPlane({ createFailures: 1 });
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Delegate task" }));
    const dialog = screen.getByRole("dialog", { name: "Delegate task" });
    const validUnicodeTitle = "😀".repeat(200);
    fireEvent.change(within(dialog).getByLabelText("Title"), { target: { value: validUnicodeTitle } });
    await user.type(within(dialog).getByLabelText("Description"), "Prove idempotent browser retries.");
    await user.selectOptions(within(dialog).getByLabelText("Worker"), "worker-online");
    await user.selectOptions(within(dialog).getByLabelText("Repository"), "repo-factory");

    await user.click(within(dialog).getByRole("button", { name: "Delegate task" }));
    expect(await within(dialog).findByText(/connection lost after submit/i)).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "Delegate task" }));
    expect(await screen.findByRole("heading", { name: validUnicodeTitle })).toBeVisible();

    const createBodies = fetch.mock.calls
      .filter(([, init]) => init?.method === "POST")
      .map(([, init]) => JSON.parse(String(init?.body)) as { request_key: string; title: string });
    expect(createBodies).toHaveLength(2);
    expect(createBodies[0].request_key).toBe(createBodies[1].request_key);
    expect(createBodies[1].title).toBe(validUnicodeTitle);
  });

  it("rejects a title over the server's 200-code-point limit", async () => {
    mockControlPlane();
    const user = userEvent.setup();
    renderApp();
    await user.click(await screen.findByRole("button", { name: "Delegate task" }));
    const dialog = screen.getByRole("dialog", { name: "Delegate task" });
    fireEvent.change(within(dialog).getByLabelText("Title"), { target: { value: "😀".repeat(201) } });
    await user.type(within(dialog).getByLabelText("Description"), "This should not submit.");
    await user.selectOptions(within(dialog).getByLabelText("Worker"), "worker-online");
    await user.selectOptions(within(dialog).getByLabelText("Repository"), "repo-factory");
    await user.click(within(dialog).getByRole("button", { name: "Delegate task" }));
    expect(within(dialog).getByText("Keep the title to 200 characters.")).toBeVisible();
  });

  it("keeps cached task detail visible after a background refresh fails", async () => {
    window.history.replaceState({}, "", "/tasks/task-running");
    mockControlPlane({ taskDetailFailuresAfter: 1 });
    const { client } = renderApp();
    expect(await screen.findByRole("heading", { name: "running task" })).toBeVisible();

    await client.refetchQueries({ queryKey: ["task", "task-running"] });

    expect(screen.getByRole("heading", { name: "running task" })).toBeVisible();
    expect(await screen.findByText(/Showing the last available data/)).toBeVisible();
    expect(screen.getByText(/temporary read failure/)).toBeVisible();
  });

  it("keeps cached ordered progress visible after an events refresh fails", async () => {
    window.history.replaceState({}, "", "/tasks/task-running");
    mockControlPlane({ eventFailuresAfter: 1 });
    const { client } = renderApp();
    expect(await screen.findByText("Cached ordered progress")).toBeVisible();

    await client.refetchQueries({ queryKey: ["events", "attempt-running"] });

    expect(screen.getByText("Cached ordered progress")).toBeVisible();
    expect(await screen.findByText(/progress refresh failed/)).toBeVisible();
  });
});
