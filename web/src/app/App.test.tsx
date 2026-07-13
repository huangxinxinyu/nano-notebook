import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { App } from "./App";

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) {
      return json({ error: { code: "unauthorized" } }, 401);
    }
    if (url.endsWith("/api/v1/auth/register")) {
      return json({ user: { id: "usr_test", email: "learner@example.com" } }, 201);
    }
    if (url.endsWith("/api/v1/notebooks") && method === "GET") {
      return json({ notebooks: [] });
    }
    if (url.endsWith("/api/v1/notebooks") && method === "POST") {
      return json({ notebook: { id: "nb_test", title: "My Research Topic" } }, 201);
    }
    if (url.endsWith("/api/v1/notebooks/nb_test")) {
      return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    }
    if (url.endsWith("/api/v1/auth/sign-out")) {
      return new Response(null, { status: 204 });
    }
    return json({ error: { code: "not_found" } }, 404);
  }));
});

test("completes the first notebook journey in English", async () => {
  render(<App />);
  const user = userEvent.setup();

  await screen.findByRole("heading", { name: "Nano Notebook" });
  await user.type(screen.getByLabelText("Email"), "learner@example.com");
  await user.type(screen.getByLabelText("Password"), "unique local sprint phrase 2026");
  await user.click(screen.getByRole("button", { name: "Create account" }));

  await screen.findByRole("heading", { name: "Library" });
  await user.click(screen.getByRole("button", { name: "New notebook" }));
  await user.type(screen.getByLabelText("Notebook title"), "My Research Topic");
  await user.click(screen.getByRole("button", { name: "Create notebook" }));

  await screen.findByRole("heading", { name: "My Research Topic" });
  expect(screen.getByRole("tab", { name: "Sources" })).toBeInTheDocument();
  expect(screen.getByRole("tab", { name: "Chat" })).toBeInTheDocument();
  expect(screen.getByRole("tab", { name: "Outputs" })).toBeInTheDocument();
});

test("defaults to Simplified Chinese for zh browser locales and can switch languages", async () => {
  Object.defineProperty(window.navigator, "language", { value: "zh-CN", configurable: true });
  render(<App />);
  await screen.findByRole("heading", { name: "Nano Notebook" });
  expect(screen.getByRole("button", { name: "切换到 English" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "切换到 English" }));
  expect(screen.getByRole("button", { name: "Switch to 简体中文" })).toBeInTheDocument();
});

function json(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" }
  });
}
