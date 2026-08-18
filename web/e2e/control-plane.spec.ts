import { expect, test } from "@playwright/test";

test.describe.configure({ mode: "serial" });
test.setTimeout(120_000);

const taskName = "E2E repository review";

test.beforeAll(async ({ request }) => {
  await expect.poll(async () => {
    const response = await request.get("/api/v1/repositories");
    if (!response.ok()) return 0;
    const body = await response.json() as { repositories?: unknown[] };
    return body.repositories?.length ?? 0;
  }, { timeout: 30_000 }).toBeGreaterThan(0);
});

test("creates a Task and completes its Run", async ({ page }) => {
  await page.goto("/tasks");
  await expect(page.getByRole("heading", { name: "Tasks", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Definitions" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Automations" })).toHaveCount(0);

  await page.getByRole("button", { name: "New Task", exact: true }).first().click();
  const dialog = page.getByRole("dialog", { name: "New Task" });
  await expect(dialog.getByText("Tools", { exact: true })).toHaveCount(0);
  await dialog.getByLabel("Name").fill(taskName);
  await dialog.getByLabel("Prompt").fill("Review this repository and leave deterministic browser evidence.");
  await dialog.locator(".repository-picker button").first().click();
  await dialog.getByRole("button", { name: "Save Task" }).click();

  const task = page.locator("article").filter({ hasText: taskName });
  await expect(task).toContainText("1 repos");
  await task.getByRole("button", { name: "Run now" }).click();
  const runDialog = page.getByRole("dialog", { name: `Run ${taskName}` });
  await expect(runDialog.getByLabel("Run on")).toHaveValue("persistent-auto");
  await runDialog.getByRole("button", { name: "Run now" }).click();

  await expect(page).toHaveURL(/\/runs\/[0-9a-f-]+$/);
  await expect(page.getByRole("heading", { name: taskName })).toBeVisible();
  await expect(page.getByText("Succeeded", { exact: true })).toBeVisible({ timeout: 45_000 });
  await page.locator(".session-row summary").click();
  await expect(page.getByText("Completed by deterministic fake Codex.", { exact: false })).toBeVisible();
  await expect(page.getByText("Attempt 1", { exact: true })).toBeVisible();
  await expect(page.locator(".attempt-events")).toContainText("Inspected the assigned repository.");
});

test("shows the same Runs as a table, list, and board", async ({ page }) => {
  await page.goto("/runs");
  await expect(page.getByRole("heading", { name: "Runs", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: new RegExp(taskName) })).toBeVisible();

  await page.getByRole("button", { name: "List", exact: true }).click();
  await expect(page).toHaveURL(/view=list/);
  await expect(page.getByText(taskName, { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Board", exact: true }).click();
  await expect(page).toHaveURL(/view=kanban/);
  await expect(page.getByText("Done", { exact: true })).toBeVisible();
  await expect(page.getByText(taskName, { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Table", exact: true }).click();
  await expect(page).toHaveURL(/\/runs$/);
});

test("keeps Overview operational and the product navigation small", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Overview", exact: true })).toBeVisible();
  await expect(page.getByText("Active runs", { exact: true })).toBeVisible();
  await expect(page.getByText("Completed · 24h", { exact: true })).toBeVisible();
  const performance = page.getByRole("region", { name: "Run performance" });
  await expect(performance.getByText("Runs", { exact: true })).toBeVisible();
  await expect(performance.getByText("Completion rate", { exact: true })).toBeVisible();
  await expect(performance.getByText("Average cycle time", { exact: true })).toBeVisible();
  await expect(performance).toContainText("1 completed");
  const navigation = page.getByRole("navigation", { name: "Primary navigation" });
  await expect(navigation.getByRole("button")).toHaveCount(5);
  await expect(navigation.getByRole("group", { name: "Infrastructure" }).getByRole("button")).toHaveText([
    "Workers",
    "Repositories",
  ]);
  await expect(page.getByText(taskName, { exact: true })).toBeVisible();

  await page.setViewportSize({ width: 390, height: 560 });
  await expect(navigation.getByRole("button", { name: "Overview", exact: true })).toBeHidden();
  await page.keyboard.press("Tab");
  const mobileMenu = page.getByRole("button", { name: "Toggle navigation" });
  await expect(mobileMenu).toBeFocused();
  await mobileMenu.click();
  await expect(mobileMenu).toHaveAttribute("aria-expanded", "true");
  await expect(navigation.getByRole("button", { name: "Overview", exact: true })).toBeVisible();
});
