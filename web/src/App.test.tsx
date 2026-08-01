import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
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
  beforeEach(() => {
    window.history.replaceState({}, "", "/work");
  });

  it("renders truthful retained metrics on the default overview", async () => {
    window.history.replaceState({}, "", "/");
    const fetch = mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    expect(await screen.findByRole("heading", { name: "Factory overview" })).toBeVisible();
    expect(screen.getByText("53")).toBeVisible();
    expect(screen.getByText("41", { selector: ".metric-card strong" })).toBeVisible();
    expect(screen.getByText("85%")).toBeVisible();
    expect(screen.getByText("14m 0s")).toBeVisible();
    expect(screen.getByText("Rates exclude cancellations.", { exact: false })).toBeVisible();
    expect(screen.getByRole("button", { name: /^Overview$/ })).toHaveAttribute(
      "aria-current",
      "page",
    );

    await user.click(screen.getByRole("button", { name: "30 days" }));
    await vi.waitFor(() => {
      expect(fetch.mock.calls.some(([input]) => {
        const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
        return path === "/api/v1/metrics/summary?window=30d";
      })).toBe(true);
    });
  });

  it("marks only exact navigation destinations as the current page", async () => {
    mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    const work = await screen.findByRole("button", { name: /^Work$/ });
    const workers = screen.getByRole("button", { name: /^Workers$/ });
    expect(screen.getByRole("button", { name: /^Overview$/ })).not.toHaveAttribute("aria-current");
    expect(work).toHaveAttribute("aria-current", "page");
    expect(workers).not.toHaveAttribute("aria-current");

    await user.click(workers);

    expect(work).not.toHaveAttribute("aria-current");
    expect(workers).toHaveAttribute("aria-current", "page");
  });

  it("highlights the Work section without marking task detail as current", async () => {
    window.history.replaceState({}, "", "/tasks/task-running");
    mockControlPlane();
    renderApp();

    await screen.findByRole("heading", { name: "running task" });
    const work = screen.getByRole("button", { name: /^Work$/ });
    expect(work).toHaveClass("active");
    expect(work).not.toHaveAttribute("aria-current");
    expect(screen.getByRole("button", { name: /^Workers$/ })).not.toHaveAttribute("aria-current");
  });

  it("highlights the Workers section without marking worker detail as current", async () => {
    window.history.replaceState({}, "", "/workers/worker-online");
    mockControlPlane();
    renderApp();

    await screen.findByRole("heading", { name: "Build Mac" });
    const workers = screen.getByRole("button", { name: /^Workers$/ });
    expect(workers).toHaveClass("active");
    expect(workers).not.toHaveAttribute("aria-current");
    expect(screen.getByRole("button", { name: /^Work$/ })).not.toHaveAttribute("aria-current");
  });

  it("lists managed repositories and shows current acquisition readiness", async () => {
    window.history.replaceState({}, "", "/repositories/repo-factory");
    mockControlPlane();
    renderApp();

    expect(await screen.findByRole("heading", { name: "github.com/example/factory" })).toBeVisible();
    expect(screen.getByText("1 worker is ready to acquire routed work")).toBeVisible();
    const workerRow = screen.getByText("Build Mac").closest(".repository-worker-row");
    expect(workerRow).not.toBeNull();
    expect(within(workerRow as HTMLElement).getByText("Cached")).toBeVisible();
    expect(within(workerRow as HTMLElement).getByText("Advertised")).toBeVisible();
    expect(within(workerRow as HTMLElement).getByText("Ready")).toBeVisible();
    const navigation = screen.getByRole("button", { name: /^Repositories$/ });
    expect(navigation).toHaveClass("active");
    expect(navigation).not.toHaveAttribute("aria-current");
  });

  it("adds, disables, and enables a managed repository", async () => {
    window.history.replaceState({}, "", "/repositories");
    mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    expect(await screen.findByText("github.com/example/disabled")).toBeVisible();
    await user.type(screen.getByLabelText("Canonical identity"), "github.com/example/new-repository");
    await user.click(screen.getByRole("button", { name: "Add repository" }));

    expect(await screen.findByRole("heading", { name: "github.com/example/new-repository" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Disable repository" }));
    expect(screen.getByText(/Disabling rejects new routed work/)).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Disable routing" }));
    expect(await screen.findByText("Routing disabled")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Enable repository" }));
    expect(await screen.findByRole("button", { name: "Disable repository" })).toBeVisible();
  });

  it("shows actionable repository validation errors", async () => {
    window.history.replaceState({}, "", "/repositories");
    mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    const input = await screen.findByLabelText("Canonical identity");
    await user.type(input, "example.com/not-github");
    await user.click(screen.getByRole("button", { name: "Add repository" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "remote_identity must use the canonical github.com/owner/repository form (invalid_repository)",
    );
    expect(input).toHaveAttribute("aria-invalid", "true");
  });

  it("identifies duplicate repositories and opens the existing entry", async () => {
    window.history.replaceState({}, "", "/repositories");
    mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    const input = await screen.findByLabelText("Canonical identity");
    await user.type(input, "github.com/example/factory");
    await user.click(screen.getByRole("button", { name: "Add repository" }));

    expect(await screen.findByRole("status")).toHaveTextContent("is already managed");
    expect(screen.queryByRole("heading", { name: "github.com/example/factory" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Open existing repository" }));
    expect(await screen.findByRole("heading", { name: "github.com/example/factory" })).toBeVisible();
  });

  it("shows repository limit and failed mutation errors", async () => {
    window.history.replaceState({}, "", "/repositories");
    mockControlPlane({ repositoryToggleFailure: true });
    const user = userEvent.setup();
    renderApp();

    const input = await screen.findByLabelText("Canonical identity");
    await user.type(input, "github.com/example/over-limit");
    await user.click(screen.getByRole("button", { name: "Add repository" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "the managed repository limit has been reached (repository_limit_reached)",
    );

    await user.click(screen.getByText("github.com/example/disabled"));
    await user.click(await screen.findByRole("button", { name: "Enable repository" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "repository update could not be saved (storage_unavailable)",
    );
  });

  it("shows server readiness even when the worker list fails", async () => {
    window.history.replaceState({}, "", "/repositories/repo-factory");
    mockControlPlane({ workerFailure: true });
    renderApp();

    expect(await screen.findByRole("heading", { name: "github.com/example/factory" })).toBeVisible();
    expect(screen.getByText("1 worker is ready to acquire routed work")).toBeVisible();
    expect(screen.getByText("Worker readiness")).toBeVisible();
  });

  it("preserves repository form focus and input during background polling", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      window.history.replaceState({}, "", "/repositories");
      mockControlPlane();
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      renderApp();

      const input = await screen.findByLabelText("Canonical identity");
      await user.type(input, "github.com/example/in-progress");
      expect(input).toHaveFocus();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_000);
      });

      expect(input).toHaveValue("github.com/example/in-progress");
      expect(input).toHaveFocus();
    } finally {
      vi.useRealTimers();
    }
  });

  it("creates, revises, and disables a Workflow through its bounded views", async () => {
    const fetch = mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: /^Workflows$/ }));
    expect(await screen.findByRole("heading", { name: "Workflows" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Create workflow" }));
    const createDialog = screen.getByRole("dialog", { name: "Create workflow" });
    await user.type(within(createDialog).getByLabelText("Name"), "Security review");
    await user.type(within(createDialog).getByLabelText("Summary"), "Review trust boundaries.");
    await user.type(within(createDialog).getByLabelText("Markdown instructions"), "Inspect inputs and permissions.");
    await user.click(within(createDialog).getByRole("button", { name: "Create workflow" }));

    expect(await screen.findByRole("heading", { name: "Security review" })).toBeVisible();
    expect(screen.getByText("Inspect inputs and permissions.", { selector: ".long-copy" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "New revision" }));
    const revisionDialog = screen.getByRole("dialog", { name: "Create revision" });
    const instructions = within(revisionDialog).getByLabelText("Markdown instructions");
    await user.clear(instructions);
    await user.type(instructions, "Inspect inputs, permissions, and recovery.");
    await user.click(within(revisionDialog).getByRole("button", { name: "Create revision" }));
    expect(await screen.findByText("Revision 2", { selector: ".panel-heading span" })).toBeVisible();

    await user.click(screen.getByRole("button", { name: "Disable" }));
    await user.click(screen.getByRole("button", { name: "Confirm disable" }));
    expect(await screen.findByRole("button", { name: "Enable" })).toBeVisible();
    expect(fetch.mock.calls.some(([input, init]) => {
      const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      return path.endsWith("/enabled") && init?.method === "PUT";
    })).toBe(true);
  });

  it("keeps a refreshed Workflow head entry over its stale loaded history copy", async () => {
    window.history.replaceState({}, "", "/workflows");
    mockControlPlane({ refreshesHistoricalWorkflow: true });
    const user = userEvent.setup();
    const { client } = renderApp();

    await user.click(await screen.findByRole("button", { name: "Load more workflows" }));
    expect(await screen.findByText("Historical workflow")).toBeVisible();

    await client.refetchQueries({ queryKey: ["workflows", "head"] });

    expect(await screen.findByText("Refreshed workflow")).toBeVisible();
    expect(screen.queryByText("Historical workflow")).not.toBeInTheDocument();
    const refreshedRow = screen.getByText("Refreshed workflow").closest(".workflow-row");
    expect(refreshedRow).not.toBeNull();
    expect(within(refreshedRow as HTMLElement).getByText("#2")).toBeVisible();
    expect(within(refreshedRow as HTMLElement).getByText("Disabled")).toBeVisible();
  });

  it("pins an enabled Workflow revision while preserving free-text context", async () => {
    const fetch = mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Delegate task" }));
    const dialog = screen.getByRole("dialog", { name: "Delegate task" });
    await user.type(within(dialog).getByLabelText("Title"), "Implement #183");
    await user.selectOptions(within(dialog).getByLabelText("Workflow"), "workflow-revision-1");
    await user.type(within(dialog).getByLabelText("Context"), "Issue #183 remains ordinary text.");
    await user.selectOptions(within(dialog).getByLabelText("Worker"), "worker-online");
    await user.selectOptions(within(dialog).getByLabelText("Repository"), "repo-factory");
    await user.click(within(dialog).getByRole("button", { name: "Delegate task" }));

    expect(await screen.findByRole("heading", { name: "Implement #183" })).toBeVisible();
    expect(screen.getAllByText("Implement · revision 1")).toHaveLength(2);
    expect(screen.getByText("Issue #183 remains ordinary text.", { selector: ".long-copy" })).toBeVisible();
    expect(screen.getByText(/Workflow instructions:/)).toBeVisible();
    const taskCreate = fetch.mock.calls.find(([input, init]) => input === "/api/v1/tasks" && init?.method === "POST");
    expect(JSON.parse(String(taskCreate?.[1]?.body))).toMatchObject({
      context: "Issue #183 remains ordinary text.",
      workflow_revision_id: "workflow-revision-1",
    });
  });

  it("renders every task status in the operational board", async () => {
    mockControlPlane();
    renderApp();

    for (const state of ["Queued", "Running", "Succeeded", "Failed", "Cancelled"]) {
      const column = await screen.findByRole("region", { name: new RegExp(`^${state}`) });
      expect(within(column).getByText(`${state.toLowerCase()} task`)).toBeVisible();
      expect(within(column).getByText(state, { selector: ".status-badge" })).toBeVisible();
    }
  });

  it("counts available capacity only from online healthy workers", async () => {
    mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: /^Workers$/ }));
    const summary = screen.getByLabelText("Fleet summary");
    expect(within(summary).getByText("Available slots").closest("div")).toHaveTextContent("1");
  });

  it("loads another bounded task page without duplicating existing work", async () => {
    mockControlPlane({ paginatedTasks: true });
    const user = userEvent.setup();
    renderApp();

    expect(await screen.findByText("queued task")).toBeVisible();
    expect(screen.queryByText("running task")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Load more work" }));

    expect(await screen.findByText("running task")).toBeVisible();
    expect(screen.getAllByText("queued task")).toHaveLength(1);
    expect(screen.queryByRole("button", { name: "Load more work" })).not.toBeInTheDocument();
  });

  it("polls only the live head page after older work is loaded", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const fetch = mockControlPlane({ paginatedTasks: true });
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      renderApp();
      await user.click(await screen.findByRole("button", { name: "Load more work" }));
      expect(await screen.findByText("running task")).toBeVisible();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });

      const taskPaths = fetch.mock.calls
        .map(([input]) => typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url)
        .filter((path) => path.startsWith("/api/v1/tasks?"));
      expect(taskPaths.filter((path) => path === "/api/v1/tasks?limit=50")).toHaveLength(2);
      expect(
        taskPaths.filter((path) => path === "/api/v1/tasks?limit=50&cursor=next-page"),
      ).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not retain tasks shifted out of the live head without loading history", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      mockControlPlane({ boundedLiveHead: true });
      renderApp();
      expect(await screen.findByText("queued task")).toBeVisible();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });

      expect(screen.getByText("new head task")).toBeVisible();
      expect(screen.queryByText("queued task")).not.toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("exposes a new history cursor when the live head grows beyond one page", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      mockControlPlane({ growingTaskHistory: true });
      renderApp();
      expect(await screen.findByText("queued task")).toBeVisible();
      expect(screen.queryByRole("button", { name: "Load more work" })).not.toBeInTheDocument();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });

      expect(screen.getByRole("button", { name: "Load more work" })).toBeVisible();
    } finally {
      vi.useRealTimers();
    }
  });

  it("reopens exhausted history from a changed live-head boundary without duplicates", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const fetch = mockControlPlane({ shiftingTaskBoundary: true });
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      renderApp();

      await user.click(await screen.findByRole("button", { name: "Load more work" }));
      expect(await screen.findByText("running task")).toBeVisible();
      expect(screen.queryByText("succeeded task")).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Load more work" })).not.toBeInTheDocument();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(screen.getByText("new head task")).toBeVisible();
      await user.click(screen.getByRole("button", { name: "Load more work" }));

      expect(await screen.findByText("succeeded task")).toBeVisible();
      expect(screen.getAllByText("running task")).toHaveLength(1);
      const refreshedTask = screen.getByText("running task").closest("button");
      expect(refreshedTask).not.toBeNull();
      expect(
        within(refreshedTask!).getByText("Succeeded", { selector: ".status-badge" }),
      ).toBeVisible();
      const taskPaths = fetch.mock.calls
        .map(([input]) => typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url)
        .filter((path) => path.startsWith("/api/v1/tasks?"));
      expect(taskPaths).toEqual([
        "/api/v1/tasks?limit=50",
        "/api/v1/tasks?limit=50&cursor=old-boundary",
        "/api/v1/tasks?limit=50",
        "/api/v1/tasks?limit=50&cursor=new-boundary",
      ]);
    } finally {
      vi.useRealTimers();
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

  it("confirms permanent deletion only for terminal task history", async () => {
    window.history.replaceState({}, "", "/tasks/task-succeeded");
    const fetch = mockControlPlane();
    const user = userEvent.setup();
    const { client } = renderApp();

    expect(await screen.findByRole("heading", { name: "succeeded task" })).toBeVisible();
    expect(await screen.findByText("Terminal event")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Delete history" }));
    expect(screen.getByText(/Permanently delete this task, prompt, attempts, and events/)).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Keep history" }));
    expect(screen.queryByRole("button", { name: "Confirm delete" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Delete history" }));
    await user.click(screen.getByRole("button", { name: "Confirm delete" }));

    expect(await screen.findByText("queued task")).toBeVisible();
    expect(screen.queryByText("succeeded task")).not.toBeInTheDocument();
    const deleteCall = fetch.mock.calls.find(([, init]) => init?.method === "DELETE");
    expect(deleteCall?.[0]).toBe("/api/v1/tasks/task-succeeded");
    expect(deleteCall?.[1]?.body).toBe("{}");
    expect(client.getQueryData(["task", "task-succeeded"])).toBeUndefined();
    expect(client.getQueryData(["events", "attempt-succeeded"])).toBeUndefined();
    expect(
      client
        .getQueryData<{ tasks: Array<{ id: string }> }>(["tasks", "head"])
        ?.tasks.some((task) => task.id === "task-succeeded"),
    ).toBe(false);
  });

  it("does not offer history deletion for active work", async () => {
    window.history.replaceState({}, "", "/tasks/task-running");
    mockControlPlane();
    renderApp();
    expect(await screen.findByRole("heading", { name: "running task" })).toBeVisible();
    expect(screen.queryByRole("button", { name: "Delete history" })).not.toBeInTheDocument();
  });

  it("does not restore deleted work when an older history request finishes late", async () => {
    const fetch = mockControlPlane({ staleHistoryAfterDelete: true });
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Load more work" }));
    await user.click(screen.getByText("succeeded task"));
    expect(await screen.findByRole("heading", { name: "succeeded task" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Delete history" }));
    await user.click(screen.getByRole("button", { name: "Confirm delete" }));

    expect(await screen.findByText("queued task")).toBeVisible();
    await vi.waitFor(() => {
      expect(screen.queryByRole("button", { name: "Load more work" })).not.toBeInTheDocument();
    });
    expect(screen.queryByText("succeeded task")).not.toBeInTheDocument();
    expect(fetch.mock.calls.some(([input]) => {
      const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      return path === "/api/v1/tasks?limit=50&cursor=stale-page";
    })).toBe(true);
  });

  it("validates the delegate form and creates a normalized task", async () => {
    const fetch = mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Delegate task" }));
    const dialog = screen.getByRole("dialog", { name: "Delegate task" });
    await user.click(within(dialog).getByRole("button", { name: "Delegate task" }));
    expect(within(dialog).getByText("Enter a task title.")).toBeVisible();
    expect(within(dialog).getByText("Enter task context.")).toBeVisible();

    await user.type(within(dialog).getByLabelText("Title"), "Ship the UI");
    await user.type(within(dialog).getByLabelText("Context"), "Build and verify the real interface.");
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
    const trigger = await screen.findByRole("button", { name: "Delegate task" });
    await user.click(trigger);
    expect(screen.getByRole("dialog")).toBeVisible();
    expect(screen.getByLabelText("Title")).toHaveFocus();
    await user.tab({ shift: true });
    expect(screen.getByRole("button", { name: "Close" })).toHaveFocus();
    await user.tab({ shift: true });
    expect(within(screen.getByRole("dialog")).getByRole("button", { name: "Delegate task" })).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await vi.waitFor(() => expect(trigger).toHaveFocus());
  });

  it("preselects the worker when assigning from worker detail", async () => {
    window.history.replaceState({}, "", "/workers/worker-online");
    mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Assign work" }));

    expect(screen.getByRole("dialog", { name: "Delegate task" })).toBeVisible();
    expect(screen.getByLabelText("Worker")).toHaveValue("worker-online");
    expect(screen.getByLabelText("Repository")).toBeEnabled();
  });

  it("keeps the active delegate field focused while worker data refreshes", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      mockControlPlane();
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      renderApp();

      await user.click(await screen.findByRole("button", { name: "Delegate task" }));
      const description = screen.getByLabelText("Context");
      await user.type(description, "Keep typing here.");
      expect(description).toHaveFocus();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_000);
      });

      expect(description).toHaveFocus();
      expect(description).toHaveValue("Keep typing here.");
    } finally {
      vi.useRealTimers();
    }
  });

  it("reuses the request key after an ambiguous create failure and accepts 200 Unicode characters", async () => {
    const fetch = mockControlPlane({ createFailures: 1 });
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Delegate task" }));
    const dialog = screen.getByRole("dialog", { name: "Delegate task" });
    const validUnicodeTitle = "😀".repeat(200);
    fireEvent.change(within(dialog).getByLabelText("Title"), { target: { value: validUnicodeTitle } });
    await user.type(within(dialog).getByLabelText("Context"), "Prove idempotent browser retries.");
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
    await user.type(within(dialog).getByLabelText("Context"), "This should not submit.");
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

  it("drains bounded event pages and later polls after the last cached sequence", async () => {
    window.history.replaceState({}, "", "/tasks/task-running");
    const fetch = mockControlPlane({ incrementalEvents: true });
    const { client } = renderApp();

    expect(await screen.findByText("Incremental event 0")).toBeVisible();
    expect(await screen.findByText("Incremental event 1")).toBeVisible();
    await client.refetchQueries({ queryKey: ["events", "attempt-running"] });
    expect(await screen.findByText("Incremental event 2")).toBeVisible();

    const eventPaths = fetch.mock.calls
      .map(([input]) => typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url)
      .filter((path) => path.includes("/events?"));
    expect(eventPaths).toEqual([
      "/api/v1/attempts/attempt-running/events?after=-1&limit=100",
      "/api/v1/attempts/attempt-running/events?after=0&limit=100",
      "/api/v1/attempts/attempt-running/events?after=1&limit=100",
    ]);
    expect(screen.getAllByText(/Incremental event/)).toHaveLength(3);
  });

  it("starts a distinct empty event cache when the latest attempt changes", async () => {
    window.history.replaceState({}, "", "/tasks/task-running");
    const fetch = mockControlPlane({ switchAttemptAfter: 1 });
    const { client } = renderApp();
    expect(await screen.findByText("Cached ordered progress")).toBeVisible();

    await client.refetchQueries({ queryKey: ["task", "task-running"] });

    expect(await screen.findByText("New attempt starts with an empty event cache")).toBeVisible();
    expect(screen.queryByText("Cached ordered progress")).not.toBeInTheDocument();
    expect(fetch.mock.calls.some(([input]) => {
      const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      return path === "/api/v1/attempts/attempt-next/events?after=-1&limit=100";
    })).toBe(true);
  });

  it("performs one final incremental event fetch when active work becomes terminal", async () => {
    window.history.replaceState({}, "", "/tasks/task-running");
    const fetch = mockControlPlane({ terminalTaskAfter: 1 });
    const { client } = renderApp();
    expect(await screen.findByText("Progress before completion")).toBeVisible();

    await client.refetchQueries({ queryKey: ["task", "task-running"] });

    expect(await screen.findByText("Final terminal progress")).toBeVisible();
    const eventPaths = fetch.mock.calls
      .map(([input]) => typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url)
      .filter((path) => path.includes("/events?"));
    expect(eventPaths).toEqual([
      "/api/v1/attempts/attempt-running/events?after=-1&limit=100",
      "/api/v1/attempts/attempt-running/events?after=0&limit=100",
    ]);
  });

  it("does not add a catch-up request when task detail is initially terminal", async () => {
    window.history.replaceState({}, "", "/tasks/task-running");
    const fetch = mockControlPlane({ terminalTaskAfter: 0 });
    renderApp();
    expect(await screen.findByText("Progress before completion")).toBeVisible();

    const eventPaths = fetch.mock.calls
      .map(([input]) => typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url)
      .filter((path) => path.includes("/events?"));
    expect(eventPaths).toEqual([
      "/api/v1/attempts/attempt-running/events?after=-1&limit=100",
    ]);
  });

  it("retries a failed terminal catch-up until one read succeeds, then stops", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      window.history.replaceState({}, "", "/tasks/task-running");
      const fetch = mockControlPlane({ terminalTaskAfter: 1, terminalEventFailures: 1 });
      const { client } = renderApp();
      expect(await screen.findByText("Progress before completion")).toBeVisible();

      await client.refetchQueries({ queryKey: ["task", "task-running"] });
      await vi.waitFor(() => {
        const eventCalls = fetch.mock.calls.filter(([input]) => {
          const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
          return path.includes("/events?");
        });
        expect(eventCalls).toHaveLength(2);
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2_000);
      });
      expect(screen.getByText("Final terminal progress")).toBeVisible();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(6_000);
      });
      const eventPaths = fetch.mock.calls
        .map(([input]) => typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url)
        .filter((path) => path.includes("/events?"));
      expect(eventPaths).toEqual([
        "/api/v1/attempts/attempt-running/events?after=-1&limit=100",
        "/api/v1/attempts/attempt-running/events?after=0&limit=100",
        "/api/v1/attempts/attempt-running/events?after=0&limit=100",
      ]);
    } finally {
      vi.useRealTimers();
    }
  });
});
