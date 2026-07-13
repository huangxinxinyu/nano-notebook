import { expect, test } from "@playwright/test";

test("registers, creates, finds, opens, signs out, and signs back in", async ({ page }, testInfo) => {
  const projectSlug = testInfo.project.name.replace(/[^a-z0-9]+/gi, "-").toLowerCase();
  const email = `learner-${projectSlug}-${Date.now()}@example.com`;
  const password = "unique local sprint phrase 2026";

  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Nano Notebook" })).toBeVisible();
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Create account" }).click();

  await expect(page.getByRole("heading", { name: "Library" })).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: "Library" })).toBeVisible();
  await page.getByRole("button", { name: "New notebook" }).click();
  await expect(page.getByLabel("Notebook title")).toBeFocused();
  await page.getByLabel("Notebook title").fill("Field Notes");
  await page.getByRole("button", { name: "Create notebook" }).click();

  await expect(page.getByRole("heading", { name: "Field Notes" })).toBeVisible();
  await expect(page.getByText("Sources are not available in Sprint 1.")).toBeVisible();
  await expect(async () => {
    const overflows = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
    expect(overflows).toBe(false);
  }).toPass();
  await page.reload();
  await expect(page.getByRole("heading", { name: "Field Notes" })).toBeVisible();

  await page.getByRole("button", { name: "Back to Library" }).click();
  await page.getByPlaceholder("Search notebooks").fill("Field");
  await expect(page.getByRole("button", { name: /Field Notes/ })).toBeVisible();
  await page.goto("/notebooks/nb_missing");
  await expect(page.getByText("Notebook not found or unavailable.")).toBeVisible();
  await page.getByRole("button", { name: "Back to Library" }).click();

  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByRole("button", { name: "Create account" })).toBeVisible();
  await page.getByRole("tab", { name: "Sign in" }).click();
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("button", { name: /Field Notes/ })).toBeVisible();
});

test("language switch exposes Simplified Chinese labels", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Switch to 简体中文" }).click();
  await expect(page.getByLabel("邮箱")).toBeVisible();
  await expect(page.getByRole("button", { name: "创建账号" })).toBeVisible();
});

test("keyboard-only user can register and create a notebook with logical focus", async ({ page }, testInfo) => {
  const projectSlug = testInfo.project.name.replace(/[^a-z0-9]+/gi, "-").toLowerCase();
  const email = `keyboard-${projectSlug}-${Date.now()}@example.com`;
  const password = "unique local sprint phrase 2026";

  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Nano Notebook" })).toBeVisible();

  await page.getByLabel("Email").focus();
  await expect(page.getByLabel("Email")).toBeFocused();
  await page.keyboard.type(email);
  await page.keyboard.press("Tab");
  await expect(page.getByLabel("Password")).toBeFocused();
  await page.keyboard.type(password);
  await page.keyboard.press("Tab");
  await expect(page.getByRole("button", { name: "Create account" })).toBeFocused();
  await page.keyboard.press("Enter");

  await expect(page.getByRole("heading", { name: "Library" })).toBeVisible();
  await page.getByRole("button", { name: "New notebook" }).focus();
  await expect(page.getByRole("button", { name: "New notebook" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.getByLabel("Notebook title")).toBeFocused();
  await page.keyboard.type("Keyboard Field Notes");
  await page.keyboard.press("Tab");
  await page.keyboard.press("Tab");
  await expect(page.getByRole("button", { name: "Create notebook" })).toBeFocused();
  await page.keyboard.press("Enter");

  await expect(page.getByRole("heading", { name: "Keyboard Field Notes" })).toBeVisible();
  await expect(async () => {
    const overflows = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
    expect(overflows).toBe(false);
  }).toPass();
});

test("inaccessible notebook route stays localized and recoverable", async ({ page }, testInfo) => {
  const projectSlug = testInfo.project.name.replace(/[^a-z0-9]+/gi, "-").toLowerCase();
  const email = `missing-${projectSlug}-${Date.now()}@example.com`;

  await page.goto("/");
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill("unique local sprint phrase 2026");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page.getByRole("heading", { name: "Library" })).toBeVisible();

  await page.goto("/notebooks/nb_missing");
  await expect(page.getByRole("alert")).toContainText("Notebook not found or unavailable.");
  await expect(page.getByRole("button", { name: "Retry" })).toBeVisible();
  await page.getByRole("button", { name: "Back to Library" }).click();
  await expect(page.getByRole("heading", { name: "Library" })).toBeVisible();
});
