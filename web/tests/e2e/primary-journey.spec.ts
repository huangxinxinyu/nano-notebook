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
  const newNotebook = page.getByRole("button", { name: "New notebook" });
  await newNotebook.click();
  const dialog = page.getByRole("dialog", { name: "New notebook" });
  await expect(page.getByLabel("Notebook title")).toBeFocused();
  await page.keyboard.press("Shift+Tab");
  await expectFocusInside(page, dialog);
  await page.keyboard.press("Escape");
  await expect(newNotebook).toBeFocused();

  await newNotebook.click();
  await page.getByLabel("Notebook title").fill("Alpha Field Notes");
  await page.getByRole("button", { name: "Create notebook" }).click();

  await expect(page.getByRole("heading", { name: "Alpha Field Notes" })).toBeVisible();
  await expectWorkspaceSourcesVisible(page, testInfo, {
    tablist: "Notebook panels",
    sources: "Sources",
    sourceBody: "Sources are not available in Sprint 1."
  });
  await expect(async () => {
    const overflows = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
    expect(overflows).toBe(false);
  }).toPass();
  await page.reload();
  await expect(page.getByRole("heading", { name: "Alpha Field Notes" })).toBeVisible();

  await page.getByRole("button", { name: "Back to Library" }).click();
  await page.getByRole("button", { name: "New notebook" }).click();
  await page.getByLabel("Notebook title").fill("Beta Field Notes");
  await page.getByRole("button", { name: "Create notebook" }).click();
  await expect(page.getByRole("heading", { name: "Beta Field Notes" })).toBeVisible();
  await page.getByRole("button", { name: "Back to Library" }).click();

  await expectNotebookOrder(page, ["Beta Field Notes", "Alpha Field Notes"]);
  await page.getByRole("button", { name: "Search notebooks" }).click();
  await page.getByPlaceholder("Search notebooks").fill("Alpha");
  await expect(page.getByRole("button", { name: "Open Alpha Field Notes", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Open Beta Field Notes", exact: true })).toHaveCount(0);
  await page.getByPlaceholder("Search notebooks").fill("No Match");
  await expect(page.getByText("No notebooks match that search.")).toBeVisible();
  await page.getByPlaceholder("Search notebooks").fill("");
  await expectNotebookOrder(page, ["Beta Field Notes", "Alpha Field Notes"]);

  await page.goto("/notebooks/nb_missing");
  await expect(page.getByText("Notebook not found or unavailable.")).toBeVisible();
  await page.getByRole("button", { name: "Back to Library" }).click();

  await page.getByRole("button", { name: "Open user menu" }).click();
  await page.getByRole("menuitem", { name: "Sign out" }).click();
  await expect(page.getByRole("button", { name: "Create account" })).toBeVisible();
  await page.getByRole("tab", { name: "Sign in" }).click();
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expectNotebookOrder(page, ["Beta Field Notes", "Alpha Field Notes"]);
});

test("language switch exposes Simplified Chinese labels", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Switch to 简体中文" }).click();
  await expect(page.getByLabel("邮箱")).toBeVisible();
  await expect(page.getByRole("button", { name: "创建账号" })).toBeVisible();
  await expect(page.getByRole("tablist", { name: "认证方式" })).toBeVisible();
});

test.describe("document language and live-region localization", () => {
  test.use({ locale: "zh-CN" });

  test("tracks initial Chinese locale and explicit switching", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByLabel("邮箱")).toBeVisible();
    await expect.poll(() => page.evaluate(() => document.documentElement.lang)).toBe("zh-CN");
    await expect(page.getByLabel(/通知 alt\+T/)).toBeAttached();

    await page.getByRole("button", { name: "切换到 English" }).click();
    await expect.poll(() => page.evaluate(() => document.documentElement.lang)).toBe("en");
    await expect(page.getByLabel(/Notifications alt\+T/)).toBeAttached();

    await page.getByRole("button", { name: "Switch to 简体中文" }).click();
    await expect.poll(() => page.evaluate(() => document.documentElement.lang)).toBe("zh-CN");
    await expect(page.getByLabel(/通知 alt\+T/)).toBeAttached();
  });
});

test("Simplified Chinese journey exposes localized product states and a11y names", async ({ page }, testInfo) => {
  const projectSlug = testInfo.project.name.replace(/[^a-z0-9]+/gi, "-").toLowerCase();
  const email = `zh-${projectSlug}-${Date.now()}@example.com`;

  await page.goto("/");
  await page.getByRole("button", { name: "Switch to 简体中文" }).click();
  await page.getByLabel("邮箱").fill(email);
  await page.getByLabel("密码").fill("unique local sprint phrase 2026");
  await page.getByRole("button", { name: "创建账号" }).click();
  await expect(page.getByRole("heading", { name: "笔记库" })).toBeVisible();

  await page.getByRole("button", { name: "新建笔记本" }).click();
  await expect(page.getByRole("dialog", { name: "新建笔记本" })).toBeVisible();
  await expect(page.getByLabel("笔记本标题")).toBeFocused();
  await page.getByLabel("笔记本标题").fill("中文研究笔记");
  await page.getByRole("button", { name: "创建笔记本" }).click();
  await expect(page.getByRole("heading", { name: "中文研究笔记" })).toBeVisible();
  await expectWorkspaceSourcesVisible(page, testInfo, {
    tablist: "笔记本面板",
    sources: "资料",
    sourceBody: "Sprint 1 尚不支持资料导入"
  });

  await page.goto("/notebooks/nb_missing");
  await expect(page.getByRole("alert")).toContainText("笔记本不存在或不可访问。");
  await expect(page.getByRole("button", { name: "重试" })).toBeVisible();
  await expect(async () => {
    const overflows = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
    expect(overflows).toBe(false);
  }).toPass();
});

test("desktop workspace shows sources, chat, and outputs as simultaneous panels", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "chromium-desktop", "desktop hierarchy is only asserted at the desktop viewport");
  const title = await createNotebook(page, testInfo, "Desktop Hierarchy Notes", "desktop");

  await expect(page.getByRole("heading", { name: title })).toBeVisible();
  const workspacePanels = page.locator(".workspace-panels");
  const panels = workspacePanels.getByRole("region");
  await expect(panels).toHaveCount(3);
  const sourcesPanel = workspacePanels.getByRole("region", { name: "Sources" });
  const chatPanel = workspacePanels.getByRole("region", { name: "Chat" });
  const outputsPanel = workspacePanels.getByRole("region", { name: "Outputs" });
  await expect(sourcesPanel).toBeVisible();
  await expect(chatPanel).toBeVisible();
  await expect(outputsPanel).toBeVisible();
  await expect(sourcesPanel).toContainText("Sources are not available in Sprint 1.");
  await expect(chatPanel).toContainText("Chat is intentionally empty until source processing and retrieval exist.");
  await expect(outputsPanel).toContainText("Outputs are reserved without generation controls in this sprint.");
  await expect(page.getByRole("tablist", { name: "Notebook panels" })).toBeHidden();

  const boxes = await panels.evaluateAll((nodes) =>
    nodes.map((node) => {
      const rect = node.getBoundingClientRect();
      return { left: rect.left, top: rect.top, width: rect.width };
    })
  );
  expect(boxes).toHaveLength(3);
  expect(new Set(boxes.map((box) => Math.round(box.top))).size).toBe(1);
  expect(boxes[0].left).toBeLessThan(boxes[1].left);
  expect(boxes[1].left).toBeLessThan(boxes[2].left);
  for (const box of boxes) {
    expect(box.width).toBeGreaterThan(180);
  }
});

test("compact workspace keeps one-panel tab navigation without horizontal overflow", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "chromium-compact", "compact tab behavior is only asserted at the compact viewport");
  const title = await createNotebook(page, testInfo, "Compact Tab Notes", "compact");

  await expect(page.getByRole("heading", { name: title })).toBeVisible();
  await expect(page.getByRole("tablist", { name: "Notebook panels" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Sources" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Chat" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Outputs" })).toBeVisible();

  const compactWorkspace = page.locator(".workspace-compact-tabs");
  const sourcesPanel = compactWorkspace.locator('[role="tabpanel"]').filter({ hasText: "Sources are not available in Sprint 1." });
  const chatPanel = compactWorkspace.locator('[role="tabpanel"]').filter({ hasText: "Chat is intentionally empty until source processing and retrieval exist." });
  const outputsPanel = compactWorkspace.locator('[role="tabpanel"]').filter({ hasText: "Outputs are reserved without generation controls in this sprint." });
  await expect(sourcesPanel).toBeVisible();
  await expect(chatPanel).toBeHidden();
  await expect(outputsPanel).toBeHidden();

  await page.getByRole("tab", { name: "Chat" }).click();
  await expect(sourcesPanel).toBeHidden();
  await expect(chatPanel).toBeVisible();
  await expect(outputsPanel).toBeHidden();

  await page.getByRole("tab", { name: "Outputs" }).click();
  await expect(sourcesPanel).toBeHidden();
  await expect(chatPanel).toBeHidden();
  await expect(outputsPanel).toBeVisible();
  await expect(async () => {
    const overflows = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
    expect(overflows).toBe(false);
  }).toPass();
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

test("expired session shows persistent localized feedback before sign-in", async ({ page, context }) => {
  await context.addCookies([{
    name: "nn_session",
    value: "stale-token",
    domain: "127.0.0.1",
    path: "/",
    httpOnly: true,
    sameSite: "Lax"
  }]);

  await page.goto("/");

  await expect(page.getByRole("alert")).toContainText("Your session expired or was revoked. Sign in again to continue.");
  await expect(page.getByRole("button", { name: "Create account" })).toBeVisible();
});

async function expectNotebookOrder(page: import("@playwright/test").Page, expected: string[]) {
  const cards = page.locator(".library-item-action");
  await expect(cards).toHaveCount(expected.length);
  await expect(cards).toContainText(expected);
}

async function expectWorkspaceSourcesVisible(
  page: import("@playwright/test").Page,
  testInfo: import("@playwright/test").TestInfo,
  labels: { tablist: string; sources: string; sourceBody: string }
) {
  if (testInfo.project.name === "chromium-desktop") {
    const sourcesPanel = page.locator(".workspace-panels").getByRole("region", { name: labels.sources });
    await expect(sourcesPanel).toBeVisible();
    await expect(sourcesPanel).toContainText(labels.sourceBody);
    await expect(page.getByRole("tablist", { name: labels.tablist })).toBeHidden();
    return;
  }

  const sourcesPanel = page.locator(".workspace-compact-tabs").locator('[role="tabpanel"]').filter({ hasText: labels.sourceBody });
  await expect(page.getByRole("tablist", { name: labels.tablist })).toBeVisible();
  await expect(page.getByRole("tab", { name: labels.sources })).toBeVisible();
  await expect(sourcesPanel).toBeVisible();
}

async function createNotebook(page: import("@playwright/test").Page, testInfo: import("@playwright/test").TestInfo, title: string, prefix: string) {
  const projectSlug = testInfo.project.name.replace(/[^a-z0-9]+/gi, "-").toLowerCase();
  const email = `${prefix}-${projectSlug}-${Date.now()}@example.com`;

  await page.goto("/");
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill("unique local sprint phrase 2026");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page.getByRole("heading", { name: "Library" })).toBeVisible();
  await page.getByRole("button", { name: "New notebook" }).click();
  await page.getByLabel("Notebook title").fill(title);
  await page.getByRole("button", { name: "Create notebook" }).click();
  return title;
}

async function expectFocusInside(page: import("@playwright/test").Page, container: import("@playwright/test").Locator) {
  await expect(async () => {
    const focusedInside = await container.evaluate((node) => node.contains(document.activeElement));
    expect(focusedInside).toBe(true);
  }).toPass();
}
