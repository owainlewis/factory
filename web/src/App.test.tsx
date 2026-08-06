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

function workflowRequestPaths(fetch: ReturnType<typeof mockControlPlane>) {
  return fetch.mock.calls
    .map(([input]) => typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url)
    .filter((path) => path.startsWith("/api/v1/workflows?limit=200"));
}

describe("App", () => {
  beforeEach(() => {
    window.history.replaceState({}, "", "/work");
  });

  it("filters Run health metrics and drills from a failed Job into its Run", async () => {
    window.history.replaceState({}, "", "/");
    const fetch = mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    expect(await screen.findByRole("heading", { name: "Factory overview" })).toBeVisible();
    expect(within(screen.getByRole("button", { name: /Active Jobs/ })).getByText("3")).toBeVisible();
    expect(within(screen.getByRole("button", { name: /Blocked Jobs/ })).getByText("2")).toBeVisible();
    expect(screen.getByText("71.4%")).toBeVisible();
    expect(screen.getByText("1m 30s")).toBeVisible();
    expect(within(screen.getByRole("button", { name: /Average cycle time/ })).getByText("14m 0s")).toBeVisible();
    expect(screen.getByText("Every metric uses the same cohort", { exact: false })).not.toBeVisible();
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
    await user.click(screen.getByRole("button", { name: /Failed Jobs/ }));
    expect(screen.getByRole("heading", { name: "Failed Jobs" })).toBeVisible();
    await user.selectOptions(screen.getByLabelText("Definition filter"), "definition-health");
    await vi.waitFor(() => expect(fetch.mock.calls.some(([input]) =>
      String(input).includes("definition_id=definition-health"))).toBe(true));
    const callsBeforeClearing = fetch.mock.calls.length;
    await user.selectOptions(screen.getByLabelText("Definition filter"), "");
    await vi.waitFor(() => {
      const paths = fetch.mock.calls.slice(callsBeforeClearing).map(([input]) => String(input));
      expect(paths).toContain("/api/v1/metrics/summary?window=30d");
      expect(paths.some((path) => path.includes("undefined"))).toBe(false);
    });
    await user.click(screen.getByRole("button", { name: /github.com\/example\/factory/ }));
    expect(window.location.pathname).toBe("/runs/run-health-failed");
  });

  it("marks only exact navigation destinations as the current page", async () => {
    mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    const work = await screen.findByRole("button", { name: /^Work$/ });
    const workers = screen.getByRole("button", { name: /^Runners$/ });
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
    expect(screen.getByRole("button", { name: /^Runners$/ })).not.toHaveAttribute("aria-current");
  });

  it("highlights the Runners section without marking Runner detail as current", async () => {
    window.history.replaceState({}, "", "/workers/worker-online");
    mockControlPlane();
    renderApp();

    await screen.findByRole("heading", { name: "Build Mac" });
    const workers = screen.getByRole("button", { name: /^Runners$/ });
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

    await user.click(await screen.findByRole("button", { name: /^Runbooks$/ }));
    expect(await screen.findByRole("heading", { name: "Runbooks" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Create runbook" }));
    const createDialog = screen.getByRole("dialog", { name: "Create runbook" });
    const workflowTitle = within(createDialog).getByLabelText("Title");
    expect(workflowTitle).toHaveAttribute("name", "title");
    expect(workflowTitle).toHaveAttribute("autocomplete", "off");
    expect(createDialog.querySelector('[name="name"]')).not.toBeInTheDocument();
    await user.type(workflowTitle, "Security review");
    await user.type(within(createDialog).getByLabelText("Summary"), "Review trust boundaries.");
    await user.type(within(createDialog).getByLabelText("Markdown instructions"), "Inspect inputs and permissions.");
    await user.click(within(createDialog).getByRole("button", { name: "Create runbook" }));

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

  it("creates, edits, and archives a shared Definition without revision controls", async () => {
    const fetch = mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: /^Definitions$/ }));
    expect(await screen.findByRole("heading", { name: "Definitions" })).toBeVisible();
    await user.click(screen.getAllByRole("button", { name: "Create Definition" })[0]);
    const create = screen.getByRole("dialog", { name: "Create Definition" });
    await user.type(within(create).getByLabelText("Name"), "Find important bugs");
    await user.type(within(create).getByLabelText("Agent prompt"), "Inspect the repository and report confirmed bugs.");
    await user.selectOptions(within(create).getByLabelText("Agent runtime"), "pi");
    await user.clear(within(create).getByLabelText("Required tools"));
    await user.type(within(create).getByLabelText("Required tools"), "git, gh");
    await user.type(within(create).getByLabelText("Optional inputs"), "severity=high");
    expect(within(create).queryByText(/revision/i)).not.toBeInTheDocument();
    await user.click(within(create).getByRole("button", { name: "Create Definition" }));

    expect(await screen.findByRole("heading", { name: "Find important bugs" })).toBeVisible();
    expect(screen.getByText("Inspect the repository and report confirmed bugs.", { selector: ".long-copy" })).toBeVisible();
    expect(screen.getByText("Pi", { selector: "dd" })).toBeVisible();
    expect(screen.getByText("severity", { selector: "dt" })).toBeVisible();

    await user.click(screen.getByRole("button", { name: "Edit Definition" }));
    const edit = screen.getByRole("dialog", { name: "Edit Definition" });
    const prompt = within(edit).getByLabelText("Agent prompt");
    await user.clear(prompt);
    await user.type(prompt, "Inspect the repository and open issues for confirmed bugs.");
    await user.click(within(edit).getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Inspect the repository and open issues for confirmed bugs.", { selector: ".long-copy" })).toBeVisible();

    await user.click(screen.getByRole("button", { name: "Archive" }));
    await user.click(screen.getByRole("button", { name: "Archive Definition" }));
    expect(await screen.findByText("No Definitions yet")).toBeVisible();
    expect(fetch.mock.calls.some(([input, init]) => {
      const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      return path.endsWith("/archived") && init?.method === "PUT";
    })).toBe(true);

    await user.click(screen.getByRole("button", { name: "View archive" }));
    expect(window.location.href).toContain("/definitions?archived=true");
    await user.click(await screen.findByRole("button", { name: /Find important bugs/ }));
    expect(window.location.href).toContain("/definitions/definition-created?archived=true");
    expect(await screen.findByText("Archived", { selector: ".status-badge" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "All Definitions" }));
    expect(window.location.href).toContain("/definitions?archived=true");
    await user.click(await screen.findByRole("button", { name: /Find important bugs/ }));
    await user.click(screen.getByRole("button", { name: "Restore Definition" }));
    expect(window.location.pathname).toBe("/definitions");
    expect(window.location.search).toBe("");
    expect(await screen.findByRole("heading", { name: "Definitions" })).toBeVisible();
    expect(await screen.findByRole("button", { name: /Find important bugs/ })).toBeVisible();
  });

  it("keeps the edit generation frozen across a background refetch", async () => {
    const fetch = mockControlPlane();
    await globalThis.fetch("/api/v1/definitions", {
      method: "POST",
      body: JSON.stringify({
        request_key: "create-concurrency-definition",
        name: "Concurrent Definition",
        prompt: "Original prompt.",
        runtime: "codex",
        allowed_tools: ["git"],
        timeout_seconds: 600,
        inputs: {},
      }),
    });
    window.history.replaceState({}, "", "/definitions/definition-created");
    const { client } = renderApp();
    const user = userEvent.setup();
    expect(await screen.findByRole("heading", { name: "Concurrent Definition" })).toBeVisible();

    await user.click(screen.getByRole("button", { name: "Edit Definition" }));
    const edit = screen.getByRole("dialog", { name: "Edit Definition" });
    const prompt = within(edit).getByLabelText("Agent prompt");
    await user.clear(prompt);
    await user.type(prompt, "Stale local prompt.");

    await globalThis.fetch("/api/v1/definitions/definition-created", {
      method: "PUT",
      body: JSON.stringify({
        request_key: "external-definition-edit",
        expected_generation: 1,
        name: "Concurrent Definition",
        prompt: "Newer external prompt.",
        runtime: "codex",
        allowed_tools: ["git"],
        timeout_seconds: 600,
        inputs: {},
      }),
    });
    await act(async () => {
      await client.invalidateQueries({ queryKey: ["definition", "definition-created"] });
    });
    await user.click(within(edit).getByRole("button", { name: "Save changes" }));
    expect(await within(edit).findByText(/the Definition was edited by another request/)).toBeVisible();

    const updates = fetch.mock.calls.filter(([input, init]) => {
      const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      return path === "/api/v1/definitions/definition-created" && init?.method === "PUT";
    });
    const submitted = JSON.parse(String(updates.at(-1)?.[1]?.body)) as { expected_generation: number };
    expect(submitted.expected_generation).toBe(1);
  });

  it("drops loaded history when a head refresh changes archive membership", async () => {
    window.history.replaceState({}, "", "/definitions");
    mockControlPlane({ paginatedDefinitions: true });
    const user = userEvent.setup();
    const { client } = renderApp();

    expect(await screen.findByText("Head Definition")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Load more Definitions" }));
    expect(await screen.findByText("Historical Definition")).toBeVisible();

    await globalThis.fetch("/api/v1/definitions/definition-history/archived", {
      method: "PUT",
      body: JSON.stringify({ archived: true, expected_generation: 1 }),
    });
    await act(async () => {
      await client.invalidateQueries({ queryKey: ["definitions", "active", "head"] });
    });

    await vi.waitFor(() => {
      expect(screen.queryByText("Historical Definition")).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Load more Definitions" })).not.toBeInTheDocument();
    });
  });

  it("rejects a stale page response after a cached Definitions remount", async () => {
    window.history.replaceState({}, "", "/definitions");
    let releaseHeadRefresh!: () => void;
    let releaseHistory!: () => void;
    const definitionHeadRefreshGate = new Promise<void>((resolve) => {
      releaseHeadRefresh = resolve;
    });
    const definitionHistoryGate = new Promise<void>((resolve) => {
      releaseHistory = resolve;
    });
    const fetch = mockControlPlane({
      paginatedDefinitions: true,
      definitionHeadRefreshGate,
      definitionHistoryGate,
    });
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: /Head Definition/ }));
    await user.click(await screen.findByRole("button", { name: "All Definitions" }));
    await vi.waitFor(() => {
      const heads = fetch.mock.calls.filter(([input]) => {
        const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
        return path === "/api/v1/definitions?limit=200&archived=false";
      });
      expect(heads).toHaveLength(2);
    });
    await user.click(screen.getByRole("button", { name: "Load more Definitions" }));
    await vi.waitFor(() => expect(fetch.mock.calls.some(([input]) => {
      const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      return path.includes("cursor=definition-history");
    })).toBe(true));

    await globalThis.fetch("/api/v1/definitions/definition-history/archived", {
      method: "PUT",
      body: JSON.stringify({ archived: true, expected_generation: 1 }),
    });
    releaseHeadRefresh();
    await vi.waitFor(() => {
      expect(screen.queryByRole("button", { name: "Load more Definitions" })).not.toBeInTheDocument();
    });
    releaseHistory();

    await vi.waitFor(() => {
      expect(screen.queryByText("Historical Definition")).not.toBeInTheDocument();
      expect(screen.getByText("Head Definition")).toBeVisible();
    });
  });

  it("previews and runs one shared Definition across independently tracked repositories", async () => {
    mockControlPlane();
    await globalThis.fetch("/api/v1/definitions", {
      method: "POST",
      body: JSON.stringify({
        request_key: "create-run-once-definition",
        name: "Review repository",
        prompt: "Review this repository and report confirmed bugs.",
        runtime: "codex",
        allowed_tools: ["git", "gh"],
        timeout_seconds: 600,
        inputs: {},
      }),
    });
    window.history.replaceState({}, "", "/runs?new=true");
    const user = userEvent.setup();
    renderApp();

    const dialog = await screen.findByRole("dialog", { name: "Run once" });
    await user.selectOptions(within(dialog).getByLabelText("Definition"), "definition-created");
    await user.selectOptions(within(dialog).getByLabelText("Repositories"), ["repo-factory", "repo-managed"]);
    expect(within(dialog).getByRole("region", { name: "Run preview" })).toHaveTextContent("2 Jobs · concurrency 3");
    await user.click(within(dialog).getByRole("button", { name: "Start Run" }));

    expect(await screen.findByRole("heading", { name: "Review repository" })).toBeVisible();
    expect(screen.getByText("0 of 2 Jobs complete · concurrency 3", { exact: false })).toBeVisible();
    expect(screen.getByRole("button", { name: "View github.com/example/factory Job" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "View github.com/example/managed Job" }));
    expect(screen.getByRole("button", { name: "View github.com/example/managed Job" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getAllByText("Queued").length).toBeGreaterThan(0);
    expect(screen.getByText("Review this repository and report confirmed bugs.", { selector: ".long-copy" })).toBeVisible();

    const createCall = vi.mocked(globalThis.fetch).mock.calls.find(([input, init]) =>
      String(input) === "/api/v1/runs" && init?.method === "POST");
    expect(JSON.parse(String(createCall?.[1]?.body))).toMatchObject({
      repository_ids: ["repo-factory", "repo-managed"],
      concurrency_limit: 3,
    });
    await user.click(screen.getByRole("button", { name: "Cancel Job" }));
    await user.click(screen.getByRole("button", { name: "Confirm cancel" }));
    expect((await screen.findAllByText("Cancelled", { selector: ".status-badge" })).length).toBeGreaterThan(0);
  });

  it("loads every active Definition page in the Run once selector", async () => {
    const fetch = mockControlPlane({ paginatedDefinitions: true });
    window.history.replaceState({}, "", "/runs?new=true");
    renderApp();

    const dialog = await screen.findByRole("dialog", { name: "Run once" });
    expect(await within(dialog).findByRole("option", { name: "Historical Definition" })).toBeVisible();
    expect(fetch.mock.calls.some(([input]) =>
      String(input).includes("cursor=definition-history")
    )).toBe(true);
  });

  it("loads older Runs through stable cursor pagination", async () => {
    mockControlPlane({ paginatedRuns: true });
    window.history.replaceState({}, "", "/runs");
    const user = userEvent.setup();
    renderApp();

    expect(await screen.findByText("Recent review")).toBeVisible();
    expect(screen.queryByText("Older review")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Load older Runs" }));

    expect(await screen.findByText("Older review")).toBeVisible();
    expect(screen.getByText("Recent review")).toBeVisible();
    expect(vi.mocked(globalThis.fetch).mock.calls.some(([input]) =>
      String(input).includes("cursor=run-history")
    )).toBe(true);
  });

  it("keeps runbook editor focus during background polling", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      window.history.replaceState({}, "", "/workflows");
      mockControlPlane();
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      renderApp();

      await user.click(await screen.findByRole("button", { name: "Create runbook" }));
      const dialog = screen.getByRole("dialog", { name: "Create runbook" });
      const instructions = within(dialog).getByLabelText("Markdown instructions");
      await user.type(instructions, "Keep this cursor here.");
      expect(instructions).toHaveFocus();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_000);
      });

      expect(instructions).toHaveValue("Keep this cursor here.");
      expect(instructions).toHaveFocus();
    } finally {
      vi.useRealTimers();
    }
  });

  it("creates, tests, enables, runs, and disables a typed GitHub issue Automation", async () => {
    window.history.replaceState({}, "", "/automations");
    const fetch = mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    expect(await screen.findByRole("heading", { name: "Automations" })).toBeVisible();
    const existingRow = screen.getByRole("button", { name: /Ready issues/ });
    expect(existingRow).toHaveTextContent("Automation is disabled.");
    expect(existingRow).toHaveTextContent("0 matched");
    expect(existingRow).toHaveTextContent("Next check");
    expect(existingRow).toHaveTextContent("No run yet");
    await user.click(screen.getByRole("button", { name: "Create Automation" }));
    const dialog = screen.getByRole("dialog", { name: "Create Automation" });
    const automationTitle = within(dialog).getByLabelText("Title");
    expect(automationTitle).toHaveAttribute("name", "title");
    expect(automationTitle).toHaveAttribute("autocomplete", "off");
    expect(dialog.querySelector('[name="name"]')).not.toBeInTheDocument();
    await user.type(automationTitle, "Factory ready issues");
    await user.selectOptions(within(dialog).getByLabelText("Runbook"), "workflow-implement");
    await user.selectOptions(within(dialog).getByLabelText("Repository"), "repo-factory");
    await user.type(within(dialog).getByLabelText("Context for this Automation"), "Fetch and revalidate live state.");
    await user.click(within(dialog).getByRole("button", { name: "Create Automation" }));

    expect(await screen.findByRole("heading", { name: "Factory ready issues" })).toBeVisible();
    expect(screen.getByText("Automation is disabled.")).toBeVisible();
    expect(screen.getByText("No durable run yet.")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    const editDialog = screen.getByRole("dialog", { name: "Edit Automation" });
    const editName = within(editDialog).getByLabelText("Title");
    await user.clear(editName);
    await user.type(editName, "Edited ready issues");
    await user.click(within(editDialog).getByRole("button", { name: "Save changes" }));
    expect(await screen.findByRole("heading", { name: "Edited ready issues" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Test trigger" }));
    expect(await screen.findByText("#184 Typed Automations")).toBeVisible();
    expect(screen.getByText("Testing creates no task or durable run.")).toBeVisible();

    await user.click(screen.getByRole("button", { name: "Enable" }));
    expect(screen.queryByRole("checkbox", { name: /factory-poller is stopped/ })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Confirm enable" }));
    expect(await screen.findByRole("button", { name: "Disable" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Check now" }));
    expect(await screen.findByText("Checking GitHub now.")).toBeVisible();
    expect(fetch.mock.calls.some(([input, init]) => {
      const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      return path.endsWith("/check") && init?.method === "POST";
    })).toBe(true);
  });

  it("filters a multi-repository Automation workspace by repository and status", async () => {
    window.history.replaceState({}, "", "/automations");
    mockControlPlane({ multiRepositoryAutomations: true });
    const user = userEvent.setup();
    renderApp();

    expect(await screen.findByText("Docs review")).toBeVisible();
    await user.selectOptions(screen.getByLabelText("Repository"), "github.com/example/disabled");
    expect(screen.getByText("Docs review")).toBeVisible();
    expect(screen.queryByText("Ready issues")).not.toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText("Status"), "disabled");
    expect(await screen.findByRole("heading", { name: "No Automations match" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(await screen.findByText("Ready issues")).toBeVisible();
    expect(screen.getByText("Docs review")).toBeVisible();
  });

  it("shows the newest durable Run instead of an older dispatched task", async () => {
    window.history.replaceState({}, "", "/automations");
    mockControlPlane({ automationRunWithoutTaskState: "failed" });
    renderApp();

    const row = await screen.findByRole("button", { name: /Ready issues/ });
    expect(row).toHaveTextContent("#185 Newest run without task");
    expect(row).toHaveTextContent("Failed");
    expect(row).not.toHaveTextContent("Older dispatched task");
  });

  it.each([
    ["queued", "Queued"],
    ["running", "Running"],
    ["succeeded", "Succeeded"],
    ["failed", "Failed"],
    ["cancelled", "Cancelled"],
  ] as const)("shows a linked %s task as the Automation Run state", async (taskState, label) => {
    window.history.replaceState({}, "", "/automations/automation-ready");
    mockControlPlane({ automationTaskState: taskState });
    renderApp();

    const identity = await screen.findByText("#184 Typed Automation run state", { selector: ".occurrence-identity strong" });
    const row = identity.closest(".occurrence-row");
    expect(row).not.toBeNull();
    expect(within(row as HTMLElement).getByText(label, { selector: ".status-badge" })).toBeVisible();
    expect(within(row as HTMLElement).getByRole("link", { name: "GitHub source" })).toHaveAttribute(
      "href",
      "https://github.com/example/factory/issues/184",
    );
    expect(await screen.findByText("Implement the change and run the required checks.", { selector: ".runbook-copy" })).toBeVisible();
  });

  it("previews, imports, resolves, and finalizes a legacy poller migration", async () => {
    window.history.replaceState({}, "", "/automations");
    const fetch = mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Migrate legacy poller" }));
    const dialog = screen.getByRole("dialog", { name: "Migrate legacy poller" });
    expect(within(dialog).getByRole("button", { name: "Preview locked snapshot" })).toBeDisabled();
    await user.type(within(dialog).getByLabelText("Legacy poller.toml"), "/tmp/legacy/poller.toml");
    await user.click(within(dialog).getByRole("checkbox", { name: /I stopped every factory-poller process/ }));
    await user.click(within(dialog).getByRole("button", { name: "Preview locked snapshot" }));

    expect(await within(dialog).findByText("1 supported · 0 unsupported")).toBeVisible();
    expect(within(dialog).getAllByText("/tmp/legacy", { exact: true })).toHaveLength(2);
    expect(within(dialog).getByText("/tmp/legacy/poller", { exact: true })).toBeVisible();
    expect(within(dialog).getByText(/Repository mapping:/)).toHaveTextContent("github.com/example/factory");
    expect(within(dialog).getByText(/Repository mapping:/)).toHaveTextContent("repo-factory");
    expect(within(dialog).getByText(/0 submitted · 1 pending · every 30s/)).toBeVisible();
    const workflowTitle = within(dialog).getByLabelText("Runbook title");
    const automationTitle = within(dialog).getByLabelText("Automation title");
    await user.clear(workflowTitle);
    await user.type(workflowTitle, "Imported implementation workflow");
    await user.clear(automationTitle);
    await user.type(automationTitle, "Imported ready issues");
    await user.click(within(dialog).getByRole("button", { name: "Import disabled Automations" }));

    expect(await within(dialog).findByText("1 unresolved")).toBeVisible();
    expect(within(dialog).getByRole("button", { name: "Finalize and archive" })).toBeDisabled();
    await user.click(within(dialog).getAllByRole("button", { name: "Close" })[1]);
    await user.click(screen.getByRole("button", { name: "Migrate legacy poller" }));
    const resumedDialog = screen.getByRole("dialog", { name: "Migrate legacy poller" });
    expect(await within(resumedDialog).findByText("1 unresolved")).toBeVisible();
    const reconfirm = within(resumedDialog).getByRole("checkbox", { name: /I reconfirmed every factory-poller process/ });
    expect(reconfirm).not.toBeChecked();
    expect(within(resumedDialog).getByRole("button", { name: "Skip" })).toBeDisabled();
    await user.click(reconfirm);
    await user.click(within(resumedDialog).getByRole("button", { name: "Skip" }));
    await vi.waitFor(() => expect(within(resumedDialog).getByText("0 unresolved")).toBeVisible());
    await user.click(within(resumedDialog).getByRole("button", { name: "Finalize and archive" }));

    expect(await within(resumedDialog).findByText("Migration finalized")).toBeVisible();
    expect(within(resumedDialog).getByText("/tmp/legacy/archive/legacy-migration")).toBeVisible();
    expect(within(resumedDialog).getByRole("button", { name: "Review Imported ready issues" })).toBeVisible();
    const importCall = fetch.mock.calls.find(([input, init]) => {
      const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      return path === "/api/v1/migrations/legacy-poller/import" && init?.method === "POST";
    });
    expect(importCall).toBeDefined();
    expect(JSON.parse(String(importCall?.[1]?.body))).toMatchObject({
      migration_id: "legacy-migration",
      mappings: [{
        queue_id: "legacy-queue",
        workflow_title: "Imported implementation workflow",
        automation_title: "Imported ready issues",
      }],
    });
  });

  it("requires Skip for an imported invalid pending observation", async () => {
    window.history.replaceState({}, "", "/automations");
    mockControlPlane({ failedLegacyOccurrence: true });
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Migrate legacy poller" }));
    const dialog = screen.getByRole("dialog", { name: "Migrate legacy poller" });
    await user.click(within(dialog).getByRole("checkbox", { name: /I stopped every factory-poller process/ }));
    await user.click(within(dialog).getByRole("button", { name: "Preview locked snapshot" }));
    await user.click(await within(dialog).findByRole("button", { name: "Import disabled Automations" }));

    expect(await within(dialog).findByText("legacy_pending_invalid_requires_skip")).toBeVisible();
    expect(within(dialog).queryByRole("button", { name: "Resume" })).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Skip" })).toBeEnabled();
  });

  it("archives a command-only legacy migration without creating Automations", async () => {
    window.history.replaceState({}, "", "/automations");
    mockControlPlane({ commandOnlyMigration: true });
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Migrate legacy poller" }));
    const dialog = screen.getByRole("dialog", { name: "Migrate legacy poller" });
    await user.click(within(dialog).getByRole("checkbox", { name: /I stopped every factory-poller process/ }));
    await user.click(within(dialog).getByRole("button", { name: "Preview locked snapshot" }));
    expect(await within(dialog).findByText("0 supported · 1 unsupported")).toBeVisible();
    expect(within(dialog).getByText("1 submitted · 1 pending", { exact: true })).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "Continue to archive" }));
    expect(await within(dialog).findByRole("button", { name: "Finalize and archive" })).toBeEnabled();
    expect(within(dialog).getByText("0 supported · 1 unsupported")).toBeVisible();
    await user.click(within(dialog).getAllByRole("button", { name: "Close" })[1]);
    await user.click(screen.getByRole("button", { name: "Migrate legacy poller" }));
    const resumedDialog = screen.getByRole("dialog", { name: "Migrate legacy poller" });
    expect(await within(resumedDialog).findByText("0 supported · 1 unsupported")).toBeVisible();
    expect(within(resumedDialog).getByText("1 submitted · 1 pending", { exact: true })).toBeVisible();
    await user.click(within(resumedDialog).getByRole("checkbox", { name: /I reconfirmed every factory-poller process/ }));
    await user.click(within(resumedDialog).getByRole("button", { name: "Finalize and archive" }));
    expect(await within(resumedDialog).findByText("Migration finalized")).toBeVisible();
  });

  it("blocks Import when the ledger contains a removed queue", async () => {
    window.history.replaceState({}, "", "/automations");
    const fetch = mockControlPlane({ ledgerOnlyMigration: true });
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Migrate legacy poller" }));
    const dialog = screen.getByRole("dialog", { name: "Migrate legacy poller" });
    await user.click(within(dialog).getByRole("checkbox", { name: /I stopped every factory-poller process/ }));
    await user.click(within(dialog).getByRole("button", { name: "Preview locked snapshot" }));

    expect(await within(dialog).findByText("1 supported · 1 unsupported")).toBeVisible();
    expect(within(dialog).getByText(/restore the matching queue before Import/)).toBeVisible();
    expect(within(dialog).getByRole("button", { name: "Restore missing queue before Import" })).toBeDisabled();
    expect(fetch.mock.calls.some(([input]) => {
      const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      return path === "/api/v1/migrations/legacy-poller/import";
    })).toBe(false);
  });

  it("creates, previews, enables, and runs a typed schedule Automation", async () => {
    window.history.replaceState({}, "", "/automations");
    const fetch = mockControlPlane({ runFailures: 1 });
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Create Automation" }));
    const dialog = screen.getByRole("dialog", { name: "Create Automation" });
    await user.type(within(dialog).getByLabelText("Title"), "Daily Factory maintenance");
    await user.selectOptions(within(dialog).getByLabelText("Runbook"), "workflow-implement");
    await user.selectOptions(within(dialog).getByLabelText("Repository"), "repo-factory");
    await user.selectOptions(within(dialog).getByLabelText("Trigger"), "schedule");
    await user.selectOptions(within(dialog).getByLabelText("Frequency"), "custom");
    await user.clear(within(dialog).getByLabelText("Cron (five fields)"));
    await user.type(within(dialog).getByLabelText("Cron (five fields)"), "0 9 * * 1");
    await user.clear(within(dialog).getByLabelText("Timezone"));
    await user.type(within(dialog).getByLabelText("Timezone"), "Europe/London");
    await user.click(within(dialog).getByRole("button", { name: "Create Automation" }));

    expect(await screen.findByRole("heading", { name: "Daily Factory maintenance" })).toBeVisible();
    expect(screen.getByText("0 9 * * 1")).toBeVisible();
    expect(screen.getByText("Europe/London")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Test trigger" }));
    expect(await screen.findByText(/next matching UTC instant/i)).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Enable" }));
    expect(screen.queryByRole("checkbox", { name: /factory-poller is stopped/ })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Confirm enable" }));
    await user.click(await screen.findByRole("button", { name: "Run now" }));
    expect(await screen.findByText(/connection lost after Run now commit/i)).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Run now" }));
    expect(await screen.findByText("Run now", { selector: ".occurrence-identity strong" })).toBeVisible();
    expect(screen.getAllByText("Run now", { selector: ".occurrence-identity strong" })).toHaveLength(1);
    expect(screen.getAllByRole("button", { name: "Open task" })).toHaveLength(1);
    const runBodies = fetch.mock.calls
      .filter(([input, init]) => {
        const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
        return path.endsWith("/run") && init?.method === "POST";
      })
      .map(([, init]) => JSON.parse(String(init?.body)) as { request_key: string });
    expect(runBodies).toHaveLength(2);
    expect(runBodies[0].request_key).toBe(runBodies[1].request_key);
  });

  it("preserves Automation form focus and typed input during background refresh", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      window.history.replaceState({}, "", "/automations");
      mockControlPlane();
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      renderApp();

      await user.click(await screen.findByRole("button", { name: "Create Automation" }));
      const input = screen.getByLabelText("Context for this Automation");
      await user.type(input, "In progress Automation context");
      expect(input).toHaveFocus();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });

      expect(input).toHaveValue("In progress Automation context");
      expect(input).toHaveFocus();
    } finally {
      vi.useRealTimers();
    }
  });

  it("creates and previews a typed GitHub pull-request Automation", async () => {
    window.history.replaceState({}, "", "/automations");
    mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Create Automation" }));
    const dialog = screen.getByRole("dialog", { name: "Create Automation" });
    await user.type(within(dialog).getByLabelText("Title"), "Factory pull request reviews");
    await user.selectOptions(within(dialog).getByLabelText("Runbook"), "workflow-implement");
    await user.selectOptions(within(dialog).getByLabelText("Repository"), "repo-factory");
    await user.selectOptions(within(dialog).getByLabelText("Trigger"), "github_pull_request");
    await user.selectOptions(within(dialog).getByLabelText("Pull request state"), "open");
    await user.click(within(dialog).getByLabelText("Include drafts"));
    await user.clear(within(dialog).getByLabelText("Required labels"));
    await user.type(within(dialog).getByLabelText("Required labels"), "factory:review");
    await user.type(within(dialog).getByLabelText("Base branches"), "main, release");
    await user.click(within(dialog).getByRole("button", { name: "Create Automation" }));

    expect(await screen.findByRole("heading", { name: "Factory pull request reviews" })).toBeVisible();
    expect(screen.getByText("Included")).toBeVisible();
    expect(screen.getByText("main, release")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Test trigger" }));
    expect(await screen.findByText("#185 Typed pull-request Automations")).toBeVisible();
    expect(screen.getByText(/base main/)).toBeVisible();
  });

  it("loads every Workflow page in the Automation form", async () => {
    window.history.replaceState({}, "", "/automations");
    mockControlPlane({ paginatedAutomationWorkflows: true });
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Create Automation" }));
    const workflow = screen.getByLabelText("Runbook");
    expect(await within(workflow).findByRole("option", { name: "Historical workflow" })).toHaveValue("workflow-history");
  });

  it("keeps direct-detail Automation selections while form options load", async () => {
    window.history.replaceState({}, "", "/automations/automation-ready");
    const fetch = mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const dialog = screen.getByRole("dialog", { name: "Edit Automation" });
    expect(within(dialog).getByLabelText("Runbook")).toHaveValue("workflow-implement");
    expect(within(dialog).getByLabelText("Repository")).toHaveValue("repo-factory");
    await user.type(within(dialog).getByLabelText("Title"), " updated");
    await user.click(within(dialog).getByRole("button", { name: "Save changes" }));
    expect(fetch.mock.calls.some(([input, init]) => {
      const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (!path.endsWith("/api/v1/automations/automation-ready") || init?.method !== "PUT") return false;
      const body = JSON.parse(String(init.body)) as { workflow_id: string };
      return body.workflow_id === "workflow-implement";
    })).toBe(true);
  });

  it("loads additional Automation and Occurrence pages", async () => {
    window.history.replaceState({}, "", "/automations");
    mockControlPlane({ paginatedAutomations: true, paginatedAutomationOccurrences: true });
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Load more Automations" }));
    expect(await screen.findByText("Historical Automation")).toBeVisible();

    await user.click(screen.getByRole("button", { name: /Ready issues/ }));
    expect(await screen.findByText("#184 Paged issue 184", { selector: ".occurrence-identity strong" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Load more runs" }));
    expect(await screen.findByText("#183 Paged issue 183", { selector: ".occurrence-identity strong" })).toBeVisible();
  });

  it("keeps a refreshed Workflow head entry over its stale loaded history copy", async () => {
    window.history.replaceState({}, "", "/workflows");
    mockControlPlane({ refreshesHistoricalWorkflow: true });
    const user = userEvent.setup();
    const { client } = renderApp();

    await user.click(await screen.findByRole("button", { name: "Load more runbooks" }));
    expect(await screen.findByText("Historical workflow")).toBeVisible();

    await client.refetchQueries({ queryKey: ["workflows", "head"] });

    expect(await screen.findByText("Refreshed workflow")).toBeVisible();
    expect(screen.queryByText("Historical workflow")).not.toBeInTheDocument();
    const refreshedRow = screen.getByText("Refreshed workflow").closest(".workflow-row");
    expect(refreshedRow).not.toBeNull();
    expect(within(refreshedRow as HTMLElement).getByText("#2")).toBeVisible();
    expect(within(refreshedRow as HTMLElement).getByText("Disabled")).toBeVisible();
  });

  it("restarts Workflow history from a changed head boundary", async () => {
    window.history.replaceState({}, "", "/workflows");
    let releaseWorkflowHistory!: () => void;
    const workflowHistoryGate = new Promise<void>((resolve) => {
      releaseWorkflowHistory = resolve;
    });
    const fetch = mockControlPlane({ shiftingWorkflowBoundary: true, workflowHistoryGate });
    const user = userEvent.setup();
    const { client } = renderApp();

    await user.click(await screen.findByRole("button", { name: "Load more runbooks" }));
    await vi.waitFor(() => expect(workflowRequestPaths(fetch)).toContain(
      "/api/v1/workflows?limit=200&cursor=old-workflow-boundary",
    ));

    await client.refetchQueries({ queryKey: ["workflows", "head"] });
    releaseWorkflowHistory();
    expect(await screen.findByText("Historical workflow")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Load more runbooks" }));

    expect(await screen.findByText("Shifted boundary workflow")).toBeVisible();
    expect(workflowRequestPaths(fetch)).toEqual([
      "/api/v1/workflows?limit=200",
      "/api/v1/workflows?limit=200&cursor=old-workflow-boundary",
      "/api/v1/workflows?limit=200",
      "/api/v1/workflows?limit=200&cursor=new-workflow-boundary",
    ]);
  });

  it("pins an enabled Workflow revision while preserving free-text context", async () => {
    const fetch = mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Delegate task" }));
    const dialog = screen.getByRole("dialog", { name: "Delegate task" });
    await user.type(within(dialog).getByLabelText("Title"), "Implement #183");
    await user.selectOptions(within(dialog).getByLabelText("Runbook"), "workflow-revision-1");
    await user.type(within(dialog).getByLabelText("Context"), "Issue #183 remains ordinary text.");
    await user.selectOptions(within(dialog).getByLabelText("Runner"), "worker-online");
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
    const queuedCard = screen.getByText("queued task").closest(".task-card");
    expect(queuedCard).not.toBeNull();
    expect(within(queuedCard as HTMLElement).getByText("Pi")).toBeVisible();
  });

  it("counts available capacity only from online healthy workers", async () => {
    mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: /^Runners$/ }));
    const summary = screen.getByLabelText("Fleet summary");
    expect(within(summary).getByText("Available slots").closest("div")).toHaveTextContent("4");
    expect(screen.getByLabelText("6 of 10 slots active")).toBeVisible();
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
    await user.selectOptions(within(dialog).getByLabelText("Runner"), "worker-online");
    const repository = within(dialog).getByLabelText("Repository");
    expect(within(repository).getByRole("option", { name: /factory/ })).toBeInTheDocument();
    expect(within(repository).getByRole("option", { name: /github.com\/example\/managed/ })).toBeEnabled();
    expect(within(repository).getByRole("option", { name: /github.com\/example\/disabled/ })).toBeDisabled();
    expect(within(repository).queryByRole("option", { name: /archive/ })).not.toBeInTheDocument();

    await user.selectOptions(within(dialog).getByLabelText("Runner"), "worker-offline");
    expect(within(dialog).getByText(/task will queue until it returns/i)).toBeVisible();
    expect(within(repository).getByRole("option", { name: /archive/ })).toBeInTheDocument();
    expect(within(repository).getByRole("option", { name: /github.com\/example\/managed/ })).toBeDisabled();
    expect(within(repository).getByRole("option", { name: /github.com\/example\/factory/ })).toBeDisabled();
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
    await user.selectOptions(within(dialog).getByLabelText("Runner"), "worker-online");
    expect(within(dialog).getByLabelText("Coding agent")).toHaveValue("codex");
    await user.selectOptions(within(dialog).getByLabelText("Coding agent"), "pi");
    await user.selectOptions(within(dialog).getByLabelText("Repository"), "repo-factory");
    await user.click(within(dialog).getByRole("button", { name: "Delegate task" }));

    expect(await screen.findByRole("heading", { name: "Ship the UI" })).toBeVisible();
    expect(screen.getByText("Progress will appear when the Runner starts this task.")).toBeVisible();
    const createCall = fetch.mock.calls.find(([, init]) => init?.method === "POST");
    expect(createCall).toBeDefined();
    expect(JSON.parse(String(createCall?.[1]?.body))).toMatchObject({
      title: "Ship the UI",
      description: "Build and verify the real interface.",
      worker_id: "worker-online",
      runtime: "pi",
      repository_id: "repo-factory",
      timeout_seconds: 7200,
    });
  });

  it("delegates a configured managed repository to the selected worker", async () => {
    const fetch = mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Delegate task" }));
    const dialog = screen.getByRole("dialog", { name: "Delegate task" });
    await user.type(within(dialog).getByLabelText("Title"), "Work in managed repository");
    await user.type(within(dialog).getByLabelText("Context"), "Acquire the configured repository on demand.");
    await user.selectOptions(within(dialog).getByLabelText("Runner"), "worker-online");
    const repositoryPicker = within(dialog).getByLabelText("Repository");
    expect(await within(repositoryPicker).findByRole("option", { name: /github\.com\/example\/managed · acquired on demand/ })).toBeEnabled();
    expect(within(repositoryPicker).getByRole("option", { name: /github\.com\/example\/disabled · Repository routing is disabled\./ })).toBeDisabled();
    expect(within(repositoryPicker).getByRole("option", { name: /docs · github\.com\/example\/docs/ })).toBeEnabled();
    await user.selectOptions(repositoryPicker, "repo-managed");
    await user.click(within(dialog).getByRole("button", { name: "Delegate task" }));

    expect(await screen.findByRole("heading", { name: "Work in managed repository" })).toBeVisible();
    const createCall = fetch.mock.calls.find(([input, init]) => input === "/api/v1/tasks" && init?.method === "POST");
    expect(createCall).toBeDefined();
    const createBody = JSON.parse(String(createCall?.[1]?.body));
    expect(createBody).toMatchObject({
      title: "Work in managed repository",
      worker_id: "worker-online",
      route: {
        repository_remote_identity: "github.com/example/managed",
        source_access: { provider: "github", hostname: "github.com" },
      },
    });
    expect(createBody).not.toHaveProperty("repository_id");
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

  it("preselects the Runner when assigning from Runner detail", async () => {
    window.history.replaceState({}, "", "/workers/worker-online");
    mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Assign work" }));

    expect(screen.getByRole("dialog", { name: "Delegate task" })).toBeVisible();
    expect(screen.getByLabelText("Runner")).toHaveValue("worker-online");
    expect(screen.getByLabelText("Repository")).toBeEnabled();
  });

  it("uses loaded Runner detail when the fleet request fails", async () => {
    window.history.replaceState({}, "", "/workers/worker-online");
    mockControlPlane({ workerFailure: true });
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Assign work" }));

    expect(screen.getByRole("dialog", { name: "Delegate task" })).toBeVisible();
    expect(screen.getByLabelText("Runner")).toHaveValue("worker-online");
    expect(screen.getByLabelText("Coding agent")).toBeEnabled();
    expect(screen.getByLabelText("Coding agent")).toHaveValue("codex");
    expect(screen.getByLabelText("Repository")).toBeEnabled();
  });

  it("uses newer Runner detail instead of stale fleet capabilities", async () => {
    window.history.replaceState({}, "", "/workers/worker-online");
    mockControlPlane({ workerDetailRuntimeRefresh: true });
    const user = userEvent.setup();
    const { client } = renderApp();

    await screen.findByRole("heading", { name: "Build Mac" });
    await client.refetchQueries({ queryKey: ["worker", "worker-online"] });

    await user.click(screen.getByRole("button", { name: "Assign work" }));

    const dialog = screen.getByRole("dialog", { name: "Delegate task" });
    expect(within(dialog).getByLabelText("Runner")).toHaveValue("worker-online");
    expect(within(dialog).getByLabelText("Coding agent")).toHaveValue("pi");
    expect(within(dialog).queryByRole("option", { name: "Codex" })).not.toBeInTheDocument();
  });

  it("uses newer fleet capabilities instead of stale Runner detail", async () => {
    window.history.replaceState({}, "", "/workers/worker-online");
    mockControlPlane({ workerRuntimeRefresh: true });
    const user = userEvent.setup();
    const { client } = renderApp();

    await screen.findByRole("heading", { name: "Build Mac" });
    await client.refetchQueries({ queryKey: ["workers"] });
    await user.click(screen.getByRole("button", { name: "Assign work" }));

    const dialog = screen.getByRole("dialog", { name: "Delegate task" });
    expect(within(dialog).getByLabelText("Runner")).toHaveValue("worker-online");
    expect(within(dialog).getByLabelText("Coding agent")).toHaveValue("pi");
    expect(within(dialog).queryByRole("option", { name: "Codex" })).not.toBeInTheDocument();
  });

  it("replaces a selected coding agent that becomes unavailable", async () => {
    const fetch = mockControlPlane({ workerRuntimeRefresh: true });
    const user = userEvent.setup();
    const { client } = renderApp();

    await user.click(await screen.findByRole("button", { name: "Delegate task" }));
    const dialog = screen.getByRole("dialog", { name: "Delegate task" });
    await user.selectOptions(within(dialog).getByLabelText("Runner"), "worker-online");
    expect(within(dialog).getByLabelText("Coding agent")).toHaveValue("codex");

    await client.refetchQueries({ queryKey: ["workers"] });

    await vi.waitFor(() => expect(within(dialog).getByLabelText("Coding agent")).toHaveValue("pi"));
    expect(fetch.mock.calls.filter(([input]) => input === "/api/v1/workers")).toHaveLength(2);
  });

  it("presents worker facts in accessible profile tabs with read-only execution settings", async () => {
    window.history.replaceState({}, "", "/workers/worker-online");
    mockControlPlane();
    const user = userEvent.setup();
    renderApp();

    expect(await screen.findByRole("heading", { name: "Build Mac" })).toBeVisible();
    const tabs = screen.getByRole("tablist", { name: "Runner profile" });
    const overview = within(tabs).getByRole("tab", { name: "Overview" });
    const work = within(tabs).getByRole("tab", { name: "Work" });
    const capabilities = within(tabs).getByRole("tab", { name: "Capabilities" });
    const settings = within(tabs).getByRole("tab", { name: "Settings" });
    for (const tab of [overview, work, capabilities, settings]) {
      expect(document.getElementById(tab.getAttribute("aria-controls") ?? "")).not.toBeNull();
    }
    expect(overview).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("region", { name: "Runner summary" })).toHaveTextContent("6 / 10");

    overview.focus();
    await user.keyboard("{ArrowRight}");
    expect(work).toHaveFocus();
    expect(work).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tabpanel")).toHaveTextContent("Retained worktrees");
    expect(screen.getByRole("tabpanel")).toHaveTextContent("Latest of 6 active sessions");
    expect(screen.getByRole("tabpanel")).toHaveTextContent("Latest active task");

    await user.keyboard("{End}");
    expect(settings).toHaveFocus();
    const settingsPanel = screen.getByRole("tabpanel");
    expect(within(settingsPanel).getByRole("heading", { name: "Execution" })).toBeVisible();
    expect(within(settingsPanel).getByText("Read only")).toBeVisible();
    expect(within(settingsPanel).getByText("6 / 10")).toBeVisible();
    expect(within(settingsPanel).getByRole("meter", { name: "Runner concurrency" })).toHaveAttribute("max", "10");
    expect(settingsPanel).toHaveTextContent("max_concurrent");
    expect(settingsPanel).toHaveTextContent("restart the Runner");
    expect(within(settingsPanel).queryByRole("textbox")).not.toBeInTheDocument();
    expect(within(settingsPanel).queryByRole("spinbutton")).not.toBeInTheDocument();
    expect(within(settingsPanel).queryByRole("combobox")).not.toBeInTheDocument();

    await user.keyboard("{Home}");
    expect(overview).toHaveFocus();
    await user.click(capabilities);
    const capabilitiesPanel = screen.getByRole("tabpanel");
    expect(capabilitiesPanel).toHaveTextContent("Pi");
    expect(capabilitiesPanel).toHaveTextContent("Codex");
    expect(capabilitiesPanel).toHaveTextContent("Claude Code");
    expect(capabilitiesPanel).toHaveTextContent("Missing");
    expect(capabilitiesPanel).toHaveTextContent("Install Claude Code and make it available on PATH.");
    expect(capabilitiesPanel).toHaveTextContent("github.com");
    expect(capabilitiesPanel).toHaveTextContent("github.com/example/factory");

    await user.click(screen.getByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("Runner is online with at least one ready coding agent.")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("Runner connection failed. Check its status and capability guidance below.")).toBeVisible();
  });

  it("clears a connection result when newer Runner status is loaded", async () => {
    window.history.replaceState({}, "", "/workers/worker-online");
    mockControlPlane({ workerOfflineAfterConnectionTest: true });
    const user = userEvent.setup();
    const { client } = renderApp();

    await user.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("Runner is online with at least one ready coding agent.")).toBeVisible();

    await client.refetchQueries({ queryKey: ["worker", "worker-online"] });

    expect(await screen.findByText("Offline", { selector: ".worker-state-line span" })).toBeVisible();
    expect(screen.queryByText("Runner is online with at least one ready coding agent.")).not.toBeInTheDocument();
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
    await user.selectOptions(within(dialog).getByLabelText("Runner"), "worker-online");
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
    await user.selectOptions(within(dialog).getByLabelText("Runner"), "worker-online");
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
