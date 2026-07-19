import { expect, test, type Route } from "@playwright/test";

test("operator explores a Trace and explicitly loads Replay", async ({ page }) => {
  let replayRequests = 0;
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/v1/session") {
      return fulfill(route, {
        user: { id: "usr_operator", email: "operator@example.com" },
        platform_capabilities: ["platform.trace.read", "platform.trace.replay"]
      });
    }
    if (url.pathname === "/api/admin/traces") {
      return fulfill(route, { schema_version: 1, data: { items: [{ summary, committed_sequence: 4, projected_sequence: 4, projection_lagged: false }] } });
    }
    if (url.pathname === "/api/admin/traces/trace-browser") {
      return fulfill(route, { schema_version: 1, data: detail });
    }
    if (url.pathname.includes("/api/admin/traces/trace-browser/replay/")) {
      replayRequests++;
      return fulfill(route, { schema_version: 1, data: { payload: { prompt: "Explain durable batching", response: "Use an Outbox." } } });
    }
    return fulfill(route, { error: { code: "not_found" } }, 404);
  });

  await page.goto("/admin/traces");
  await expect(page.getByRole("heading", { name: "Trace Explorer" })).toBeVisible();
  await page.getByRole("button", { name: "Open Trace run-browser" }).click();

  await expect(page.getByRole("region", { name: "Trace summary" })).toContainText("trace-browser");
  const tree = page.getByRole("tree", { name: "Trace Tree" });
  const timeline = page.getByRole("region", { name: "Trace Timeline" });
  await timeline.getByRole("button", { name: "Select gen_ai.model.call in Timeline" }).click();
  await expect(tree.getByRole("treeitem", { name: /gen_ai.model.call/ })).toHaveAttribute("aria-selected", "true");
  await expect(page).toHaveURL(/span=model-browser/);

  await page.getByRole("tab", { name: "Replay" }).click();
  expect(replayRequests).toBe(0);
  await page.getByRole("button", { name: "Load sensitive Replay" }).click();
  await expect(page.getByText("Explain durable batching", { exact: true })).toBeAttached();
  expect(replayRequests).toBe(1);
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
});

const summary = {
  trace_id: "trace-browser", run_id: "run-browser", chat_id: "chat-browser", notebook_id: "notebook-browser",
  root_span_id: "root-browser", agent_name: "nano-research-agent", started_at_unix_nano: 1700000000000000000,
  last_observed_unix_nano: 1700000003000000000, ended_at_unix_nano: 1700000003000000000,
  duration_nanoseconds: 3000000000, status: "ok", active: false, models: ["qwen-flash"], input_tokens: 12,
  output_tokens: 8, total_tokens: 20, cost: { known: true, amount: 0.002, currency: "USD", source: "provider_reported" }, attempt_count: 1
};

const detail = {
  committed_sequence: 4,
  projected_sequence: 4,
  projection: {
    summary,
    spans: [
      { trace_id: "trace-browser", span_id: "root-browser", parent_span_id: "", name: "agent.execution", start_sequence: 1, end_sequence: 4, started_at_unix_nano: 1700000000000000000, ended_at_unix_nano: 1700000003000000000, duration_nanoseconds: 3000000000, status: "ok", start_attributes: [], end_attributes: [], replay: [], model: null },
      { trace_id: "trace-browser", span_id: "model-browser", parent_span_id: "root-browser", name: "gen_ai.model.call", start_sequence: 2, end_sequence: 3, started_at_unix_nano: 1700000000500000000, ended_at_unix_nano: 1700000002500000000, duration_nanoseconds: 2000000000, status: "ok", start_attributes: [], end_attributes: [], replay: [{ attachment_id: "replay-browser", class: "model_exchange", record_sequence: 3 }], model: { requested_model: "qwen-flash", selected_model: "qwen-flash", provider: "aliyun", input_tokens: 12, output_tokens: 8, total_tokens: 20, cached_tokens: 0, reasoning_tokens: 0, gateway_retries: 0, gateway_fallbacks: 0, duration_nanoseconds: 2000000000, cost: { known: true, amount: 0.002, currency: "USD", source: "provider_reported" } } }
    ],
    events: [],
    links: []
  }
};

async function fulfill(route: Route, payload: unknown, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify(payload) });
}
