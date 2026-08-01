import {
  expect,
  request,
  test,
  type APIRequestContext,
  type Page,
} from "@playwright/test";

test.describe.configure({ mode: "serial" });
test.setTimeout(120_000);

const workerOnline = "worker-online-e2e";
const workerOffline = "worker-offline-e2e";
const managedWorker = "worker-managed-e2e";
const automationWorker = "worker-automation-e2e";
const realWorker = "11111111-1111-4111-8111-111111111111";
const onlineRepositories = [
  { key: "factory", remote_identity: "github.com/example/factory", retained_count: 1 },
  { key: "handbook", remote_identity: "github.com/example/handbook", retained_count: 0 },
];
const offlineRepositories = [
  { key: "archive", remote_identity: "github.com/example/archive", retained_count: 0 },
];
const identifiers: Record<string, string> = {};

interface TaskDetail {
  task: { id: string; title: string; description?: string; state?: string };
  context?: string;
  execution: { id: string };
  repository: { id: string };
  attempts: Array<{ id: string; state?: string; result?: string; error?: string }>;
  workflow?: { id: string; revision_id: string; name: string; revision_number: number };
  resolved_prompt?: string;
}

async function json<T>(response: Awaited<ReturnType<APIRequestContext["get"]>>): Promise<T> {
  if (!response.ok()) {
    throw new Error(`API ${response.status()}: ${await response.text()}`);
  }
  return response.json() as Promise<T>;
}

async function registerWorker(
  api: APIRequestContext,
  id: string,
  name: string,
  repositories: typeof onlineRepositories,
  activeCount = 0,
  disposedAttemptIDs: string[] = [],
  runtime: "codex" | "claude-code" = "codex",
  sourceAccess: Array<{ provider: "github"; hostname: string }> = [],
) {
  return json<{
    repositories: Array<{ id: string; key: string }>;
  }>(
    await api.put(`/api/v1/workers/${id}`, {
      data: {
        name,
        worker_version: "2.0.0-test",
        runtime,
        runtime_version: runtime === "claude-code" ? "2.1.220-test" : "0.42.0-test",
        capacity: 2,
        active_count: activeCount,
        health: "healthy",
        source_access: sourceAccess,
        capacity_handoff_version: 1,
        disposed_attempt_ids: disposedAttemptIDs,
        repositories,
        retained_worktrees:
          id === workerOnline
            ? [
                {
                  attempt_id: "attempt-retained-001",
                  repository_id: identifiers.factoryRepository ?? "",
                  path: "/tmp/factory-e2e/worktrees/attempt-retained-001",
                  reason: "failed with local changes",
                  cleanup_command: "factory-worker cleanup attempt-retained-001 --confirm",
                },
              ]
            : [],
      },
    }),
  );
}

async function createTask(
  api: APIRequestContext,
  key: string,
  title: string,
  workerID: string,
  repositoryID: string,
  description = "A representative task created through the real control-plane API.",
) {
  return json<TaskDetail>(
    await api.post("/api/v1/tasks", {
      data: {
        request_key: key,
        title,
        description,
        worker_id: workerID,
        repository_id: repositoryID,
        timeout_seconds: 7200,
      },
    }),
  );
}

async function claimAndStart(api: APIRequestContext, requestID: string) {
  const token = `lease-token-${requestID}-0123456789abcdef0123456789`;
  const claim = await json<{
    attempt: { id: string };
    execution: { id: string };
    task: { id: string };
  }>(
    await api.post(`/api/v1/workers/${workerOnline}/claims`, {
      data: { request_id: requestID, lease_token: token },
    }),
  );
  await json(
    await api.post(`/api/v1/attempts/${claim.attempt.id}/start`, {
      data: { lease_token: token, process_identity: `e2e-${requestID}` },
    }),
  );
  return { ...claim, token };
}

async function complete(
  api: APIRequestContext,
  requestID: string,
  state: "succeeded" | "failed",
  result: string,
) {
  const claim = await claimAndStart(api, requestID);
  await json(
    await api.post(`/api/v1/attempts/${claim.attempt.id}/complete`, {
      data: {
        lease_token: claim.token,
        state,
        ...(state === "succeeded" ? { result } : { error: result }),
      },
    }),
  );
  return claim;
}

async function waitForRealWorker(api: APIRequestContext) {
  for (let attempt = 0; attempt < 60; attempt += 1) {
    const response = await api.get("/api/v1/workers");
    if (response.ok()) {
      const body = (await response.json()) as {
        workers: Array<{
          id: string;
          health: string;
          online: boolean;
          repositories: Array<{ id: string; key: string }>;
        }> | null;
      };
      const worker = body.workers?.find((candidate) => candidate.id === realWorker);
      if (worker?.online && worker.health === "healthy") return worker;
    }
    await new Promise((resolveWait) => setTimeout(resolveWait, 500));
  }
  throw new Error("real Factory worker did not register as healthy within 30 seconds");
}

function observeBrowser(page: Page) {
  const problems: string[] = [];
  const realtime: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error") problems.push(`console: ${message.text()}`);
  });
  page.on("requestfailed", (failed) => {
    problems.push(`request: ${failed.url()} ${failed.failure()?.errorText ?? "failed"}`);
  });
  page.on("websocket", (socket) => realtime.push(`websocket: ${socket.url()}`));
  page.on("response", (response) => {
    if (response.headers()["content-type"]?.includes("text/event-stream")) {
      realtime.push(`event-stream: ${response.url()}`);
    }
  });
  return {
    assertClean() {
      expect(problems, "browser console and network failures").toEqual([]);
      expect(realtime, "the UI must use HTTP polling only").toEqual([]);
    },
  };
}

test.beforeAll(async () => {
  const api = await request.newContext({ baseURL: "http://127.0.0.1:17437" });
  const real = await waitForRealWorker(api);
  identifiers.realFactoryRepository = real.repositories.find(
    (repository) => repository.key === "factory-demo",
  )!.id;
  identifiers.realHandbookRepository = real.repositories.find(
    (repository) => repository.key === "handbook-demo",
  )!.id;

  const offline = await registerWorker(
    api,
    workerOffline,
    "Archive Mac",
    offlineRepositories,
    0,
    [],
    "claude-code",
  );
  identifiers.offlineRepository = offline.repositories[0].id;

  // Registrations become offline after the server's documented 30 second window.
  await new Promise((resolveWait) => setTimeout(resolveWait, 31_000));

  const online = await registerWorker(api, workerOnline, "Build Mac", onlineRepositories);
  identifiers.factoryRepository = online.repositories.find((repo) => repo.key === "factory")!.id;
  identifiers.handbookRepository = online.repositories.find((repo) => repo.key === "handbook")!.id;
  const automation = await registerWorker(
    api,
    automationWorker,
    "Automation fixture",
    [
      {
        key: "automation-fixture",
        remote_identity: "github.com/example/automation-fixture",
        retained_count: 0,
      },
    ],
    0,
    [],
    "codex",
    [{ provider: "github", hostname: "github.com" }],
  );
  const managedAutomationRepository = await json<{ id: string }>(
    await api.post("/api/v1/repositories", {
      data: { remote_identity: "github.com/example/automation-fixture" },
    }),
  );
  expect(managedAutomationRepository.id).toBe(automation.repositories[0].id);
  identifiers.automationRepository = managedAutomationRepository.id;

  const queued = await createTask(
    api,
    "e2e-queued",
    "Queued for the offline archive worker",
    workerOffline,
    identifiers.offlineRepository,
  );
  identifiers.queuedTask = queued.task.id;

  const cancelled = await createTask(
    api,
    "e2e-cancelled",
    "Cancelled queue cleanup",
    workerOffline,
    identifiers.offlineRepository,
  );
  await json(await api.post(`/api/v1/tasks/${cancelled.task.id}/cancel`, { data: {} }));

  const succeeded = await createTask(
    api,
    "e2e-succeeded",
    "Ship the stable API client",
    workerOnline,
    identifiers.factoryRepository,
  );
  const succeededAttempt = await complete(
    api,
    "claim-succeeded",
    "succeeded",
    "API client shipped with all checks passing.",
  );
  identifiers.succeededTask = succeeded.task.id;

  const failed = await createTask(
    api,
    "e2e-failed",
    "Repair a failed release check",
    workerOnline,
    identifiers.factoryRepository,
  );
  const failedAttempt = await complete(
    api,
    "claim-failed",
    "failed",
    "The release check found a deterministic failure.",
  );
  identifiers.failedTask = failed.task.id;

  const longTitle = `Long operational title ${"with bounded content ".repeat(8)}`.slice(0, 200);
  const longTask = await createTask(
    api,
    "e2e-long",
    longTitle,
    workerOffline,
    identifiers.offlineRepository,
    `${"Long descriptions remain readable and do not escape their surface. ".repeat(80)}\nEnd of description.`,
  );
  identifiers.longTask = longTask.task.id;

  const running = await createTask(
    api,
    "e2e-running",
    "Implement the modern control-plane UI",
    workerOnline,
    identifiers.factoryRepository,
    "Build the complete browser interface, verify it against the real server, and preserve unrelated state.",
  );
  const active = await claimAndStart(api, "claim-running");
  await api.post(`/api/v1/attempts/${active.attempt.id}/events`, {
    data: {
      lease_token: active.token,
      events: [
        {
          sequence: 0,
          kind: "codex",
          payload: {
            type: "item.completed",
            item: { type: "agent_message", text: "Inspected the control-plane contract." },
          },
        },
        {
          sequence: 1,
          kind: "codex",
          payload: {
            type: "item.completed",
            item: {
              type: "command_execution",
              command: "npm test",
              aggregated_output: "RAW_COMMAND_OUTPUT_SHOULD_NOT_RENDER",
              exit_code: 0,
            },
          },
        },
        { sequence: 2, kind: "codex", payload: { type: "thread.started", thread_id: "thread-e2e" } },
        { sequence: 3, kind: "check", payload: { summary: "Running browser verification." } },
      ],
    },
  });
  identifiers.runningTask = running.task.id;
  identifiers.runningAttempt = active.attempt.id;

  await registerWorker(
    api,
    workerOnline,
    "Build Mac",
    onlineRepositories,
    1,
    [succeededAttempt.attempt.id, failedAttempt.attempt.id],
  );
  await api.dispose();
});

test("shows retained Factory metrics and saves the overview", async ({ page }) => {
  const browser = observeBrowser(page);
  const api = await request.newContext({ baseURL: "http://127.0.0.1:17437" });
  const summary = await json<{
    executions_created: number;
    executions_completed: number;
    queued: number;
    running: number;
  }>(await api.get("/api/v1/metrics/summary?window=7d"));
  await api.dispose();

  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Factory overview" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Overview", exact: true })).toHaveAttribute(
    "aria-current",
    "page",
  );
  await expect(
    page.locator(".metric-card").filter({ hasText: "Executions created" }).locator("strong"),
  ).toHaveText(String(summary.executions_created));
  await expect(
    page.locator(".metric-card").filter({ hasText: "Executions completed" }).locator("strong"),
  ).toHaveText(String(summary.executions_completed));
  await expect(page.locator(".health-metrics").getByText(String(summary.queued), { exact: true })).toBeVisible();
  await expect(page.locator(".health-metrics").getByText(String(summary.running), { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "30 days" }).click();
  await expect(page.getByRole("button", { name: "30 days" })).toHaveAttribute("aria-pressed", "true");
  await page.screenshot({ path: "test-results/screenshots/overview-desktop.png", fullPage: true });
  browser.assertClean();
});

test("creates, pins, revises, and disables a reusable Workflow", async ({ page }) => {
  const browser = observeBrowser(page);
  await page.goto("/workflows");
  await expect(page.getByRole("heading", { name: "Workflows", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Create workflow" }).first().click();
  const create = page.getByRole("dialog", { name: "Create workflow" });
  await create.getByLabel("Name").fill("E2E pinned review");
  await create.getByLabel("Summary").fill("Prove immutable prompt snapshots.");
  await create.getByLabel("Markdown instructions").fill("Use revision one instructions exactly.");
  await create.getByRole("button", { name: "Create workflow" }).click();
  await expect(page.getByRole("heading", { name: "E2E pinned review" })).toBeVisible();
  const workflowURL = page.url();

  await page.getByRole("button", { name: "Delegate task" }).click();
  const delegate = page.getByRole("dialog", { name: "Delegate task" });
  await delegate.getByLabel("Workflow").selectOption({ label: "E2E pinned review · revision 1" });
  await delegate.getByLabel("Title").fill("Pinned Workflow browser task");
  await delegate.getByLabel("Context").fill("JIRA-183 stays free text.");
  await delegate.getByLabel("Worker").selectOption(workerOffline);
  await delegate.getByLabel("Repository").selectOption(identifiers.offlineRepository);
  await delegate.getByRole("button", { name: "Delegate task" }).click();
  await expect(page.getByRole("heading", { name: "Pinned Workflow browser task" })).toBeVisible();
  await expect(page.getByText("JIRA-183 stays free text.", { exact: true })).toBeVisible();
  await expect(page.getByText(/Use revision one instructions exactly/)).toBeVisible();
  const taskID = new URL(page.url()).pathname.split("/").at(-1)!;

  await page.goto(workflowURL);
  await page.getByRole("button", { name: "New revision" }).click();
  const revise = page.getByRole("dialog", { name: "Create revision" });
  await revise.getByLabel("Markdown instructions").fill("Use revision two instructions instead.");
  await revise.getByRole("button", { name: "Create revision" }).click();
  await expect(page.getByText("Revision 2", { exact: true }).first()).toBeVisible();

  const api = await request.newContext({ baseURL: "http://127.0.0.1:17437" });
  const pinned = await json<TaskDetail>(await api.get(`/api/v1/tasks/${taskID}`));
  expect(pinned.context).toBe("JIRA-183 stays free text.");
  expect(pinned.task.description).toBe(pinned.resolved_prompt);
  expect(pinned.workflow?.revision_number).toBe(1);
  expect(pinned.resolved_prompt).toContain("Use revision one instructions exactly.");
  expect(pinned.resolved_prompt).not.toContain("Use revision two instructions instead.");
  await api.dispose();

  await page.getByRole("button", { name: "Disable" }).click();
  await page.getByRole("button", { name: "Confirm disable" }).click();
  await expect(page.getByRole("button", { name: "Enable" })).toBeVisible();
  await page.getByRole("button", { name: "Delegate task" }).click();
  await expect(page.getByRole("dialog").getByLabel("Workflow").getByRole("option", { name: /E2E pinned review/ })).toHaveCount(0);
  await page.keyboard.press("Escape");
  browser.assertClean();
});

test("runs the complete UI to real-worker and Git-worktree workflow", async ({ page }) => {
  const browser = observeBrowser(page);
  await page.goto("/");
  await page.getByRole("button", { name: "Delegate task" }).first().click();
  const dialog = page.getByRole("dialog", { name: "Delegate task" });
  await dialog.getByLabel("Worker").selectOption(realWorker);
  await expect(
    dialog.getByLabel("Repository").getByRole("option", { name: /factory-demo/ }),
  ).toHaveCount(1);
  await expect(
    dialog.getByLabel("Repository").getByRole("option", { name: /handbook-demo/ }),
  ).toHaveCount(1);
  await dialog.getByLabel("Title").fill("Prove the complete local workflow");
  await dialog
    .getByLabel("Context")
    .fill("Create deterministic evidence in the assigned real Git worktree.");
  await dialog.getByLabel("Repository").selectOption(identifiers.realFactoryRepository);
  await page.screenshot({
    path: "test-results/screenshots/delegate-desktop.png",
    fullPage: true,
  });
  await dialog.getByRole("button", { name: "Delegate task" }).click();

  await expect(page.getByRole("heading", { name: "Prove the complete local workflow" })).toBeVisible();
  await expect(page.getByText("Succeeded", { exact: true }).first()).toBeVisible({
    timeout: 30_000,
  });
  await expect(page.getByText("Created deterministic worktree evidence.")).toBeVisible();
  await expect(page.getByText("Completed by deterministic fake Codex.", { exact: false })).toBeVisible();
  await expect(page.getByText(/Branch: factory\//)).toBeVisible();
  await expect(page.getByText(/Worktree: .*factory-ui-e2e-.*\/worker\/worktrees\//)).toBeVisible();
  await page.screenshot({
    path: "test-results/screenshots/task-detail-desktop.png",
    fullPage: true,
  });

  const taskID = new URL(page.url()).pathname.split("/").at(-1)!;
  await page.reload();
  await expect(page.getByRole("heading", { name: "Prove the complete local workflow" })).toBeVisible();

  const api = await request.newContext({ baseURL: "http://127.0.0.1:17437" });
  const detail = await json<TaskDetail>(await api.get(`/api/v1/tasks/${taskID}`));
  expect(detail.task.state).toBe("succeeded");
  expect(detail.attempts).toHaveLength(1);
  expect(detail.attempts[0].result).toContain("Branch: factory/");
  const worker = await json<{
    retained_worktrees: Array<{ attempt_id: string; path: string; cleanup_command: string }>;
  }>(await api.get(`/api/v1/workers/${realWorker}`));
  const retained = worker.retained_worktrees.find(
    (worktree) => worktree.attempt_id === detail.attempts[0].id,
  );
  expect(retained?.path).toContain("/worker/worktrees/");
  expect(retained?.cleanup_command).toContain(`factory-worker cleanup ${detail.attempts[0].id}`);
  await api.dispose();
  browser.assertClean();
});

test("cancels active work running in the real worker", async ({ page }) => {
  const browser = observeBrowser(page);
  await page.goto("/");
  await page.getByRole("button", { name: "Delegate task" }).first().click();
  const dialog = page.getByRole("dialog", { name: "Delegate task" });
  await dialog.getByLabel("Worker").selectOption(realWorker);
  await dialog.getByLabel("Title").fill("Cancel a real active Codex process");
  await dialog
    .getByLabel("Context")
    .fill("FACTORY_E2E_WAIT until the operator cancels this task.");
  await dialog.getByLabel("Repository").selectOption(identifiers.realHandbookRepository);
  await dialog.getByRole("button", { name: "Delegate task" }).click();

  await expect(page.getByText("Running", { exact: true }).first()).toBeVisible({
    timeout: 30_000,
  });
  await expect(page.getByText("Waiting for operator cancellation.")).toBeVisible();
  await page.getByRole("button", { name: "Cancel" }).click();
  await page.getByRole("button", { name: "Confirm cancel" }).click();
  await expect(page.getByText("Cancelled", { exact: true }).first()).toBeVisible({
    timeout: 20_000,
  });
  await expect(page.getByText("attempt cancelled", { exact: false })).toBeVisible();
  browser.assertClean();
});

test("renders every state and saves the desktop Work view", async ({ page }) => {
  const browser = observeBrowser(page);
  await page.goto("/work");
  await expect(page.getByRole("heading", { name: "Agent work" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Work", exact: true })).toHaveAttribute(
    "aria-current",
    "page",
  );
  for (const state of ["Queued", "Running", "Succeeded", "Failed", "Cancelled"]) {
    await expect(page.getByRole("region", { name: new RegExp(`^${state}`) })).toBeVisible();
  }
  await expect(page.getByText("Long operational title", { exact: false })).toBeVisible();
  await page.screenshot({ path: "test-results/screenshots/work-desktop.png", fullPage: true });
  browser.assertClean();
});

test("confirms and deletes terminal task history", async ({ page }) => {
  const browser = observeBrowser(page);
  await page.goto(`/tasks/${identifiers.succeededTask}`);
  await expect(page.getByRole("heading", { name: "Ship the stable API client" })).toBeVisible();
  await page.waitForLoadState("networkidle");
  await page.getByRole("button", { name: "Delete history" }).click();
  await expect(page.getByText(/Permanently delete this task, prompt, attempts, and events/)).toBeVisible();
  await page.getByRole("button", { name: "Confirm delete" }).click();
  await expect(page).toHaveURL("/work");
  await expect(page.getByText("Ship the stable API client")).toHaveCount(0);
  browser.assertClean();

  const api = await request.newContext({ baseURL: "http://127.0.0.1:17437" });
  const response = await api.get(`/api/v1/tasks/${identifiers.succeededTask}`);
  expect(response.status()).toBe(404);
  await api.dispose();
});

test("shows worker capacity, current work, retained cleanup, and saves Workers", async ({ page }) => {
  const browser = observeBrowser(page);
  await page.goto("/workers");
  await expect(page.getByRole("heading", { name: "Execution capacity" })).toBeVisible();
  const workersNavigation = page.getByRole("button", { name: "Workers", exact: true });
  await expect(workersNavigation).toHaveAttribute("aria-current", "page");
  await expect(page.getByText("Implement the modern control-plane UI")).toBeVisible();
  const offlineRow = page.getByRole("button", { name: /Archive Mac/ });
  await expect(offlineRow).toBeVisible();
  await expect(offlineRow).toContainText("Offline");
  await expect(offlineRow).toContainText("Claude Code");
  await page.screenshot({ path: "test-results/screenshots/workers-desktop.png", fullPage: true });

  await page.getByRole("button", { name: /Build Mac/ }).click();
  await expect(page.getByRole("heading", { name: "Build Mac" })).toBeVisible();
  await expect(workersNavigation).toHaveClass(/active/);
  await expect(workersNavigation).not.toHaveAttribute("aria-current");
  await expect(page.getByText("factory-worker cleanup attempt-retained-001 --confirm")).toBeVisible();
  const assign = page.getByRole("button", { name: "Assign work" });
  await assign.click();
  await expect(page.getByRole("dialog").getByLabel("Worker")).toHaveValue(workerOnline);
  await page.keyboard.press("Escape");
  await expect(assign).toBeFocused();
  browser.assertClean();
});

test("delegates with worker-specific repositories and preserves the task on refresh", async ({ page }) => {
  const browser = observeBrowser(page);
  await page.goto("/");
  await page.getByRole("button", { name: "Delegate task" }).first().click();
  const dialog = page.getByRole("dialog", { name: "Delegate task" });
  await dialog.getByLabel("Worker").selectOption(workerOffline);
  await expect(dialog.getByText(/task will queue until it returns/i)).toBeVisible();
  await expect(dialog.getByText("This becomes the Claude Code prompt.")).toBeVisible();
  await expect(dialog.getByLabel("Repository").getByRole("option", { name: /archive/ })).toHaveCount(1);
  await expect(dialog.getByLabel("Repository").getByRole("option", { name: /factory/ })).toHaveCount(0);
  await dialog.getByLabel("Title").fill("Durable delegated browser task");
  await dialog.getByLabel("Context").fill("Created in the real UI and stored by the real Go server.");
  await dialog.getByLabel("Repository").selectOption(identifiers.offlineRepository);
  await dialog.getByRole("button", { name: "Delegate task" }).click();
  await expect(page.getByRole("heading", { name: "Durable delegated browser task" })).toBeVisible();
  await expect(page.getByText("Claude Code", { exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: "Durable delegated browser task" })).toBeVisible();
  browser.assertClean();
});

test("confirms queued cancellation and explicitly retries a failure", async ({ page }) => {
  const browser = observeBrowser(page);
  await page.goto(`/tasks/${identifiers.queuedTask}`);
  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByText("Cancel this task?")).toBeVisible();
  await page.getByRole("button", { name: "Confirm cancel" }).click();
  await expect(page.getByText("Cancelled", { exact: true }).first()).toBeVisible();

  await page.goto(`/tasks/${identifiers.failedTask}`);
  await page.getByRole("button", { name: "Retry task" }).click();
  await expect(page.getByText("Queued", { exact: true }).first()).toBeVisible();
  browser.assertClean();
});

test("shows ordered progress and long task detail", async ({ page }) => {
  const browser = observeBrowser(page);
  const eventAfters: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === `/api/v1/attempts/${identifiers.runningAttempt}/events`) {
      eventAfters.push(url.searchParams.get("after") ?? "");
    }
  });
  await page.goto(`/tasks/${identifiers.runningTask}`);
  const workNavigation = page.getByRole("button", { name: "Work", exact: true });
  await expect(workNavigation).toHaveClass(/active/);
  await expect(workNavigation).not.toHaveAttribute("aria-current");
  const events = page.locator(".event-list li");
  await expect(events).toHaveCount(3);
  await expect(events.nth(0)).toContainText("Inspected the control-plane contract.");
  await expect(events.nth(1)).toContainText("Succeeded: npm test");
  await expect(events.nth(2)).toContainText("Running browser verification.");
  await expect(page.getByText("RAW_COMMAND_OUTPUT_SHOULD_NOT_RENDER")).toHaveCount(0);
  await expect(page.getByText("3 updates")).toBeVisible();
  await expect.poll(() => eventAfters).toContain("-1");
  await expect.poll(() => eventAfters, { timeout: 8_000 }).toContain("3");

  await page.goto(`/tasks/${identifiers.longTask}`);
  const contextPanel = page.locator("section.panel").filter({
    has: page.getByRole("heading", { name: "Context", exact: true }),
  });
  await expect(contextPanel.getByText("End of description.")).toBeVisible();
  browser.assertClean();
});

test("supports narrow grouped layouts and saves narrow screenshots", async ({ page }) => {
  const browser = observeBrowser(page);
  await page.setViewportSize({ width: 800, height: 900 });
  await page.goto("/workers");
  const workerList = page.locator(".workers-list");
  const tabletListBounds = await workerList.boundingBox();
  const tabletRowBounds = await page.locator(".worker-row").first().boundingBox();
  expect(tabletRowBounds?.width ?? Infinity).toBeLessThanOrEqual(
    tabletListBounds?.width ?? 0,
  );

  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Factory overview" })).toBeVisible();
  await page.screenshot({ path: "test-results/screenshots/overview-narrow.png", fullPage: true });

  await page.goto("/work");
  const columns = page.locator(".work-column");
  await expect(columns).toHaveCount(5);
  const first = await columns.nth(0).boundingBox();
  const second = await columns.nth(1).boundingBox();
  expect(Math.abs((first?.x ?? 0) - (second?.x ?? 1))).toBeLessThan(2);
  await page.screenshot({ path: "test-results/screenshots/work-narrow.png", fullPage: true });

  await page.goto("/workers");
  await expect(page.getByRole("heading", { name: "Execution capacity" })).toBeVisible();
  await page.screenshot({ path: "test-results/screenshots/workers-narrow.png", fullPage: true });

  await page.getByRole("button", { name: "Delegate task" }).click();
  const dialog = page.getByRole("dialog", { name: "Delegate task" });
  await dialog.getByLabel("Worker").selectOption(realWorker);
  await dialog.getByLabel("Title").fill("Narrow viewport delegation");
  await dialog.getByLabel("Context").fill("Review the complete narrow task form.");
  await dialog.getByLabel("Repository").selectOption(identifiers.realFactoryRepository);
  await page.screenshot({ path: "test-results/screenshots/delegate-narrow.png", fullPage: true });
  await page.keyboard.press("Escape");

  await page.goto(`/tasks/${identifiers.runningTask}`);
  await expect(page.getByRole("heading", { name: "Implement the modern control-plane UI" })).toBeVisible();
  await page.screenshot({
    path: "test-results/screenshots/task-detail-narrow.png",
    fullPage: true,
  });
  browser.assertClean();
});

test("opens and closes delegation from the keyboard", async ({ page }) => {
  const browser = observeBrowser(page);
  await page.goto("/");
  const delegate = page.getByRole("button", { name: "Delegate task" }).first();
  await delegate.focus();
  await page.keyboard.press("Enter");
  await expect(page.getByLabel("Title")).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(delegate).toBeFocused();
  browser.assertClean();
});

test("manages repository routing end to end and preserves add input while polling", async ({ page }) => {
  const browser = observeBrowser(page);
  const api = await request.newContext({ baseURL: "http://127.0.0.1:17437" });
  await json(
    await api.put(`/api/v1/workers/${managedWorker}`, {
      data: {
        name: "Managed repository worker",
        worker_version: "2.0.0-test",
        runtime: "codex",
        runtime_version: "0.42.0-test",
        capacity: 1,
        active_count: 0,
        health: "healthy",
        source_access: [{ provider: "github", hostname: "github.com" }],
        accepts_managed_repositories: true,
        managed_repository_ids: [],
        repositories: [],
        retained_worktrees: [],
      },
    }),
  );

  await page.goto("/repositories");
  const repositoriesNavigation = page.getByRole("button", { name: "Repositories", exact: true });
  await expect(repositoriesNavigation).toHaveAttribute("aria-current", "page");
  const input = page.getByLabel("Canonical identity");
  await input.fill("github.com/example/browser-managed");
  await expect(input).toBeFocused();
  await page.waitForTimeout(10_500);
  await expect(input).toHaveValue("github.com/example/browser-managed");
  await expect(input).toBeFocused();
  await page.getByRole("button", { name: "Add repository" }).click();

  await expect(page.getByRole("heading", { name: "github.com/example/browser-managed" })).toBeVisible();
  await expect(repositoriesNavigation).toHaveClass(/active/);
  await expect(repositoriesNavigation).not.toHaveAttribute("aria-current");
  await expect(page.getByText(/\d+ workers? (?:is|are) ready to acquire routed work/)).toBeVisible();
  await expect(page.getByText("Managed repository worker")).toBeVisible();
  await page.screenshot({ path: "test-results/screenshots/repository-detail-desktop.png", fullPage: true });

  await page.getByRole("button", { name: "Disable repository" }).click();
  await expect(page.getByText(/Disabling rejects new routed work/)).toBeVisible();
  await page.getByRole("button", { name: "Disable routing" }).click();
  await expect(page.getByText("Routing disabled")).toBeVisible();

  const disabledRoute = await api.post("/api/v1/tasks", {
    data: {
      request_key: "e2e-disabled-managed-route",
      title: "Rejected while repository routing is disabled",
      description: "Prove disabled managed repositories reject new routed work.",
      route: {
        repository_remote_identity: "github.com/example/browser-managed",
        source_access: { provider: "github", hostname: "github.com" },
      },
      timeout_seconds: 60,
    },
  });
  expect(disabledRoute.status()).toBe(409);
  expect(await disabledRoute.json()).toMatchObject({ error: { code: "repository_not_managed" } });

  await page.getByRole("button", { name: "Enable repository" }).click();
  await expect(page.getByRole("button", { name: "Disable repository" })).toBeVisible();
  const enabledRoute = await api.post("/api/v1/tasks", {
    data: {
      request_key: "e2e-enabled-managed-route",
      title: "Eligible after repository routing is enabled",
      description: "Prove enabled managed repositories become eligible for acquisition.",
      route: {
        repository_remote_identity: "github.com/example/browser-managed",
        source_access: { provider: "github", hostname: "github.com" },
      },
      timeout_seconds: 60,
    },
  });
  expect(enabledRoute.status()).toBe(201);
  const enabledTask = await enabledRoute.json() as { execution: { assigned_worker_id: string } };
  expect([managedWorker, realWorker]).toContain(enabledTask.execution.assigned_worker_id);
  await api.dispose();
  browser.assertClean();
});

test("previews and dispatches one typed GitHub issue Automation without duplication", async ({ page }) => {
  const browser = observeBrowser(page);
  const api = await request.newContext({ baseURL: "http://127.0.0.1:17437" });
  await registerWorker(
    api,
    automationWorker,
    "Automation fixture",
    [
      {
        key: "automation-fixture",
        remote_identity: "github.com/example/automation-fixture",
        retained_count: 0,
      },
    ],
    0,
    [],
    "codex",
    [{ provider: "github", hostname: "github.com" }],
  );
  await page.goto("/workflows");
  await page.getByRole("button", { name: "Create workflow" }).first().click();
  const workflow = page.getByRole("dialog", { name: "Create workflow" });
  await workflow.getByLabel("Name").fill("E2E issue Automation");
  await workflow.getByLabel("Summary").fill("Dispatch the safe issue fixture.");
  await workflow.getByLabel("Markdown instructions").fill("Fetch the live issue, implement it, and verify the result.");
  await workflow.getByRole("button", { name: "Create workflow" }).click();
  await expect(page.getByRole("heading", { name: "E2E issue Automation" })).toBeVisible();

  await page.getByRole("button", { name: "Automations", exact: true }).click();
  await page.getByRole("button", { name: "Create Automation" }).first().click();
  const automation = page.getByRole("dialog", { name: "Create Automation" });
  await automation.getByLabel("Name").fill("E2E ready issues");
  await automation.getByLabel("Workflow").selectOption({ label: "E2E issue Automation" });
  await automation.getByLabel("Managed repository").selectOption(identifiers.automationRepository);
  await automation.getByLabel("Trusted Automation context").fill("Use only the safe browser fixture repository.");
  await automation.getByRole("button", { name: "Create Automation" }).click();
  await expect(page.getByRole("heading", { name: "E2E ready issues" })).toBeVisible();

  await page.getByRole("button", { name: "Test trigger" }).click();
  await expect(page.getByText("#184 Typed Automation browser fixture")).toBeVisible();
  await expect(page.getByText("Testing creates no task or durable occurrence.")).toBeVisible();
  await expect(page.getByText("No durable occurrences yet.")).toBeVisible();

  await page.getByRole("button", { name: "Enable" }).click();
  await page.getByRole("checkbox", { name: /factory-poller is stopped/ }).check();
  await page.getByRole("button", { name: "Confirm enable" }).click();
  await expect(page.locator(".automation-health").getByText("healthy", { exact: true })).toBeVisible({ timeout: 15_000 });
  await expect(page.locator(".occurrence-list").getByText("#184 Typed Automation browser fixture", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Open task" })).toBeVisible();

  const before = await json<{ tasks: Array<{ request_key: string }> }>(await api.get("/api/v1/tasks?limit=200"));
  const automationTasksBefore = before.tasks.filter((task) => task.request_key.includes(":github_issue:184"));
  expect(automationTasksBefore).toHaveLength(1);

  await page.getByRole("button", { name: "Run now" }).click();
  await expect(page.locator(".automation-metrics > div").filter({ hasText: "Matched" }).locator("strong")).toHaveText("2", { timeout: 15_000 });
  const after = await json<{ tasks: Array<{ request_key: string }> }>(await api.get("/api/v1/tasks?limit=200"));
  const automationTasksAfter = after.tasks.filter((task) => task.request_key.includes(":github_issue:184"));
  expect(automationTasksAfter).toHaveLength(1);
  await api.dispose();
  browser.assertClean();
});

test("previews and dispatches one typed GitHub pull-request Automation without duplication", async ({ page }) => {
  const browser = observeBrowser(page);
  const api = await request.newContext({ baseURL: "http://127.0.0.1:17437" });
  await registerWorker(
    api,
    automationWorker,
    "Automation fixture",
    [{
      key: "automation-fixture",
      remote_identity: "github.com/example/automation-fixture",
      retained_count: 0,
    }],
    0,
    [],
    "codex",
    [{ provider: "github", hostname: "github.com" }],
  );
  await page.goto("/workflows");
  await page.getByRole("button", { name: "Create workflow" }).first().click();
  const workflow = page.getByRole("dialog", { name: "Create workflow" });
  await workflow.getByLabel("Name").fill("E2E pull-request review");
  await workflow.getByLabel("Summary").fill("Review the safe pull-request fixture.");
  await workflow.getByLabel("Markdown instructions").fill("Fetch and revalidate the live pull request, review it, and do not merge it.");
  await workflow.getByRole("button", { name: "Create workflow" }).click();
  await expect(page.getByRole("heading", { name: "E2E pull-request review" })).toBeVisible();

  await page.getByRole("button", { name: "Automations", exact: true }).click();
  await page.getByRole("button", { name: "Create Automation" }).first().click();
  const automation = page.getByRole("dialog", { name: "Create Automation" });
  await automation.getByLabel("Name").fill("E2E pull-request Automation");
  await automation.getByLabel("Workflow").selectOption({ label: "E2E pull-request review" });
  await automation.getByLabel("Managed repository").selectOption(identifiers.automationRepository);
  await automation.getByLabel("Trigger type").selectOption("github_pull_request");
  await automation.getByLabel("Required labels").fill("factory:review");
  await automation.getByLabel("Base branches").fill("main");
  await automation.getByLabel("Trusted Automation context").fill("Review only the safe synthetic pull request and never merge it.");
  await automation.getByRole("button", { name: "Create Automation" }).click();
  await expect(page.getByRole("heading", { name: "E2E pull-request Automation" })).toBeVisible();

  await page.getByRole("button", { name: "Test trigger" }).click();
  await expect(page.getByText("#185 Typed pull-request Automation browser fixture")).toBeVisible();
  await expect(page.getByText("Testing creates no task or durable occurrence.")).toBeVisible();
  await expect(page.getByText("No durable occurrences yet.")).toBeVisible();

  await page.getByRole("button", { name: "Enable" }).click();
  await page.getByRole("checkbox", { name: /factory-poller is stopped/ }).check();
  await page.getByRole("button", { name: "Confirm enable" }).click();
  await expect(page.locator(".automation-health").getByText("healthy", { exact: true })).toBeVisible({ timeout: 15_000 });
  await expect(page.locator(".occurrence-list").getByText("#185 Typed pull-request Automation browser fixture", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Open task" })).toBeVisible();

  const before = await json<{ tasks: Array<{ request_key: string }> }>(await api.get("/api/v1/tasks?limit=200"));
  expect(before.tasks.filter((task) => task.request_key.includes(":github_pull_request:185"))).toHaveLength(1);

  await page.getByRole("button", { name: "Run now" }).click();
  await expect(page.locator(".automation-metrics > div").filter({ hasText: "Matched" }).locator("strong")).toHaveText("2", { timeout: 15_000 });
  const after = await json<{ tasks: Array<{ request_key: string }> }>(await api.get("/api/v1/tasks?limit=200"));
  expect(after.tasks.filter((task) => task.request_key.includes(":github_pull_request:185"))).toHaveLength(1);
  await api.dispose();
  browser.assertClean();
});
