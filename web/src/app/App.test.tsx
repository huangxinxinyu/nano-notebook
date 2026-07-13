import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { App } from "./App";
import { queryClient } from "./queryClient";

let fetchHandler: (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

beforeEach(() => {
  localStorage.clear();
  window.history.pushState(null, "", "/");
  queryClient.clear();
  document.cookie = "nn_csrf=test-token";
  Object.defineProperty(window.navigator, "language", { value: "en-US", configurable: true });
  fetchHandler = async (input: RequestInfo | URL, init?: RequestInit) => {
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
  };
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => fetchHandler(input, init)));
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

test("surfaces duplicate registration as a distinct localized error", async () => {
  fetchHandler = async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({ error: { code: "unauthorized" } }, 401);
    if (url.endsWith("/api/v1/auth/register") && init?.method === "POST") {
      return json({ error: { code: "duplicate_email", message_key: "error.registration_unavailable" } }, 409);
    }
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText("Email"), "learner@example.com");
  await user.type(screen.getByLabelText("Password"), "unique local sprint phrase 2026");
  await user.click(screen.getByRole("button", { name: "Create account" }));

  expect(await screen.findByRole("alert")).toHaveTextContent("Email is already registered for this local workspace.");
});

test("surfaces invalid sign-in credentials as a distinct localized error", async () => {
  fetchHandler = async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({ error: { code: "unauthorized" } }, 401);
    if (url.endsWith("/api/v1/auth/sign-in") && init?.method === "POST") {
      return json({ error: { code: "invalid_credentials", message_key: "error.invalid_credentials" } }, 401);
    }
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("tab", { name: "Sign in" }));
  await user.type(screen.getByLabelText("Email"), "learner@example.com");
  await user.type(screen.getByLabelText("Password"), "unique local sprint phrase 2026");
  await user.click(screen.getByRole("button", { name: "Sign in" }));

  expect(await screen.findByRole("alert")).toHaveTextContent("Email or password is incorrect.");
});

test("surfaces notebook quota as a distinct localized create error", async () => {
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ error: { code: "unauthorized" } }, 401);
    if (url.endsWith("/api/v1/auth/register")) return json({ user: { id: "usr_test", email: "learner@example.com" } }, 201);
    if (url.endsWith("/api/v1/notebooks") && method === "GET") return json({ notebooks: [] });
    if (url.endsWith("/api/v1/notebooks") && method === "POST") {
      return json({ error: { code: "quota_reached", message_key: "error.notebook_quota" } }, 409);
    }
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText("Email"), "learner@example.com");
  await user.type(screen.getByLabelText("Password"), "unique local sprint phrase 2026");
  await user.click(screen.getByRole("button", { name: "Create account" }));
  await user.click(await screen.findByRole("button", { name: "New notebook" }));
  await user.type(screen.getByLabelText("Notebook title"), "Quota Test");
  await user.click(screen.getByRole("button", { name: "Create notebook" }));

  expect(await screen.findByRole("alert")).toHaveTextContent("Notebook limit reached.");
});

function json(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" }
  });
}
