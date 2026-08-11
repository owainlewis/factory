import { expect, test } from "@playwright/test";

test.describe.configure({ mode: "serial" });
test.setTimeout(120_000);

const routineName = "E2E repository review";

test.beforeAll(async ({ request }) => {
  await expect.poll(async () => {
    const response = await request.get("/api/v1/repositories");
    if (!response.ok()) return 0;
    const body = await response.json() as { repositories?: unknown[] };
    return body.repositories?.length ?? 0;
  }, { timeout: 30_000 }).toBeGreaterThan(0);
});

test("creates a Routine and completes its Work", async ({ page }) => {
  await page.goto("/routines");
  await expect(page.getByRole("heading", { name: "Routines", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Definitions" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Automations" })).toHaveCount(0);

  await page.getByRole("button", { name: "New Routine", exact: true }).first().click();
  const dialog = page.getByRole("dialog", { name: "New Routine" });
  await expect(dialog.getByText("Tools", { exact: true })).toHaveCount(0);
  await dialog.getByLabel("Name").fill(routineName);
  await dialog.getByLabel("Prompt").fill("Review this repository and leave deterministic browser evidence.");
  await dialog.locator(".repository-picker button").first().click();
  await dialog.getByRole("button", { name: "Save Routine" }).click();

  const routine = page.locator("article").filter({ hasText: routineName });
  await expect(routine).toContainText("1 repos");
  await routine.getByRole("button", { name: "Run now" }).click();

  await expect(page).toHaveURL(/\/work\/[0-9a-f-]+$/);
  await expect(page.getByRole("heading", { name: routineName })).toBeVisible();
  await expect(page.getByText("Succeeded", { exact: true })).toBeVisible({ timeout: 45_000 });
  await page.locator(".target-row summary").click();
  await expect(page.getByText("Completed by deterministic fake Codex.", { exact: false })).toBeVisible();
  await expect(page.getByText("Attempt 1", { exact: true })).toBeVisible();
  await expect(page.locator(".attempt-events")).toContainText("Inspected the assigned repository.");
});

test("shows the same Work as a table, list, and board", async ({ page }) => {
  await page.goto("/work");
  await expect(page.getByRole("heading", { name: "Work", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: new RegExp(routineName) })).toBeVisible();

  await page.getByRole("button", { name: "List", exact: true }).click();
  await expect(page).toHaveURL(/view=list/);
  await expect(page.getByText(routineName, { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Board", exact: true }).click();
  await expect(page).toHaveURL(/view=kanban/);
  await expect(page.getByText("Done", { exact: true })).toBeVisible();
  await expect(page.getByText(routineName, { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Table", exact: true }).click();
  await expect(page).toHaveURL(/\/work$/);
});

test("keeps Overview operational and the product navigation small", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Overview", exact: true })).toBeVisible();
  await expect(page.getByText("Active work", { exact: true })).toBeVisible();
  await expect(page.getByText("Completed · 24h", { exact: true })).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Primary navigation" }).getByRole("button")).toHaveCount(5);
  await expect(page.getByText(routineName, { exact: true })).toBeVisible();
});
