import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { App } from "./App";
import { queryClient } from "./queryClient";

let fetchHandler: (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

beforeEach(() => {
  localStorage.clear();
  window.history.pushState(null, "", "/");
  document.documentElement.lang = "en";
  queryClient.clear();
  document.cookie = "nn_csrf=test-token";
  Object.defineProperty(window.navigator, "language", { value: "en-US", configurable: true });
  fetchHandler = async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) {
      return json({ error: { code: "session_missing", message_key: "error.session_missing" } }, 401);
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
  await user.type(await screen.findByLabelText("Email"), "learner@example.com");
  await user.type(screen.getByLabelText("Password"), "unique local sprint phrase 2026");
  await user.click(screen.getByRole("button", { name: "Create account" }));

  await screen.findByRole("heading", { name: "Library" });
  await user.click(screen.getByRole("button", { name: "New notebook" }));
  await user.type(screen.getByLabelText("Notebook title"), "My Research Topic");
  await user.click(screen.getByRole("button", { name: "Create notebook" }));

  await screen.findByRole("heading", { name: "My Research Topic" });
  expect(screen.getByRole("tab", { name: "Sources" })).toBeInTheDocument();
  expect(screen.getByRole("tab", { name: "Chat" })).toBeInTheDocument();
  expect(screen.getByRole("tab", { name: "Studio" })).toBeInTheDocument();
});

test("renders a truthful static workspace shell without starting a chat runtime", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = authenticatedWorkspaceHandler();

  render(<App />);
  const user = userEvent.setup();

  await screen.findByRole("heading", { name: "My Research Topic" });
  const sources = screen.getByRole("region", { name: "Sources" });
  expect(sources).toBeInTheDocument();
  const chat = screen.getByRole("region", { name: "Chat" });
  expect(chat).toHaveAttribute("data-placeholder", "true");
  expect(chat).toHaveAttribute("data-chat-framework", "@assistant-ui/react");
  expect(within(chat).getByRole("textbox", { name: "Chat is not available yet" })).toBeDisabled();
  expect(screen.getByRole("region", { name: "Studio" })).toBeInTheDocument();

  await user.click(within(sources).getByRole("button", { name: "Add sources" }));
  expect(await screen.findByText("This feature is coming soon.")).toBeInTheDocument();
  expect(vi.mocked(fetch).mock.calls.every(([input]) => !String(input).includes("/chat"))).toBe(true);
});

test("exposes compact Sources, Chat, and Studio navigation", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = authenticatedWorkspaceHandler();

  render(<App />);

  await screen.findByRole("heading", { name: "My Research Topic" });
  const tabs = screen.getByRole("tablist", { name: "Notebook panels" });
  expect(within(tabs).getByRole("tab", { name: "Sources" })).toBeInTheDocument();
  expect(within(tabs).getByRole("tab", { name: "Chat" })).toBeInTheDocument();
  expect(within(tabs).getByRole("tab", { name: "Studio" })).toBeInTheDocument();
});

test("defaults to Simplified Chinese for zh browser locales and can switch languages", async () => {
  Object.defineProperty(window.navigator, "language", { value: "zh-CN", configurable: true });
  render(<App />);
  const user = userEvent.setup();

  await screen.findByLabelText("邮箱");
  expect(screen.getByRole("button", { name: "切换到 English" })).toBeInTheDocument();
  expect(screen.getByRole("tablist", { name: "认证方式" })).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "切换到 English" }));
  expect(await screen.findByRole("button", { name: "Switch to 简体中文" })).toBeInTheDocument();
  expect(screen.getByRole("tablist", { name: "Authentication mode" })).toBeInTheDocument();
});

test("syncs document language with initial locale and visible switching", async () => {
  Object.defineProperty(window.navigator, "language", { value: "zh-CN", configurable: true });
  render(<App />);
  const user = userEvent.setup();

  await screen.findByLabelText("邮箱");
  await waitFor(() => expect(document.documentElement.lang).toBe("zh-CN"));
  await user.click(screen.getByRole("button", { name: "切换到 English" }));
  await waitFor(() => expect(document.documentElement.lang).toBe("en"));
  await user.click(screen.getByRole("button", { name: "Switch to 简体中文" }));
  await waitFor(() => expect(document.documentElement.lang).toBe("zh-CN"));
});

test("localizes the toast live-region accessible name with locale changes", async () => {
  render(<App />);
  const user = userEvent.setup();

  await screen.findByLabelText("Email");
  expect(screen.getByLabelText(/Notifications alt\+T/)).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Switch to 简体中文" }));
  expect(await screen.findByLabelText(/通知 alt\+T/)).toBeInTheDocument();
  expect(screen.queryByLabelText(/Notifications alt\+T/)).not.toBeInTheDocument();
});

test("keeps first anonymous visit free of expired session feedback", async () => {
  render(<App />);

  await screen.findByRole("button", { name: "Create account" });
  expect(screen.queryByText("Your session expired or was revoked. Sign in again to continue.")).not.toBeInTheDocument();
});

test("shows stale expired session feedback on the auth screen", async () => {
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) {
      return json({ error: { code: "session_expired", message_key: "error.session_expired" } }, 401);
    }
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);

  expect(await screen.findByRole("alert")).toHaveTextContent("Your session expired or was revoked. Sign in again to continue.");
  expect(screen.getByRole("button", { name: "Create account" })).toBeInTheDocument();
});

test("shows localized stale session feedback for Simplified Chinese", async () => {
  Object.defineProperty(window.navigator, "language", { value: "zh-CN", configurable: true });
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) {
      return json({ error: { code: "session_expired", message_key: "error.session_expired" } }, 401);
    }
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);

  expect(await screen.findByRole("alert")).toHaveTextContent("会话已过期或被撤销。请重新登录以继续。");
  expect(screen.getByRole("button", { name: "创建账号" })).toBeInTheDocument();
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

test("shows session loading before rendering authentication choices", () => {
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return new Promise<Response>(() => {});
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);

  expect(screen.getByText("Loading")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Create account" })).not.toBeInTheDocument();
});

test("shows retryable session unreachable state instead of signed-out auth", async () => {
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({ error: { code: "unavailable" } }, 503);
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);

  expect(await screen.findByRole("alert")).toHaveTextContent("Control Plane is unreachable. Retry after starting the local system.");
  expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Create account" })).not.toBeInTheDocument();
});

test("keeps the library visible when sign-out revocation fails", async () => {
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks") && method === "GET") return json({ notebooks: [] });
    if (url.endsWith("/api/v1/auth/sign-out")) return json({ error: { code: "internal" } }, 500);
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  await screen.findByRole("heading", { name: "Library" });
  await user.click(screen.getByRole("button", { name: "Open user menu" }));
  await user.click(screen.getByRole("menuitem", { name: "Sign out" }));

  expect(await screen.findByRole("alert")).toHaveTextContent("Sign out failed. Retry to revoke the server session.");
  expect(screen.getByRole("heading", { name: "Library" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Create account" })).not.toBeInTheDocument();
});

test("renders real notebooks in a table and sorts them by title", async () => {
  fetchHandler = authenticatedLibraryHandler([
    { id: "nb_zulu", title: "Zulu Notes", recent_at: "2026-07-14T10:00:00Z" },
    { id: "nb_alpha", title: "Alpha Notes", recent_at: "2026-07-13T10:00:00Z" }
  ]);

  render(<App />);
  const user = userEvent.setup();
  const table = await screen.findByRole("table", { name: "Recently opened notebooks" });

  expect(within(table).getByRole("columnheader", { name: "Title" })).toBeInTheDocument();
  expect(within(table).getByRole("columnheader", { name: "Source" })).toBeInTheDocument();
  expect(within(table).getByRole("columnheader", { name: "Creation date" })).toBeInTheDocument();
  expect(within(table).getByRole("columnheader", { name: "Role" })).toBeInTheDocument();
  expect((await within(table).findAllByRole("button", { name: /Open .* Notes/ })).map((button) => button.getAttribute("aria-label"))).toEqual([
    "Open Zulu Notes",
    "Open Alpha Notes"
  ]);

  await user.click(screen.getByRole("button", { name: "Sort notebooks" }));
  await user.click(screen.getByRole("menuitem", { name: "Title" }));

  expect(within(table).getAllByRole("button", { name: /Open .* Notes/ }).map((button) => button.getAttribute("aria-label"))).toEqual([
    "Open Alpha Notes",
    "Open Zulu Notes"
  ]);
});

test("expands and closes notebook search while querying the backend", async () => {
  fetchHandler = authenticatedLibraryHandler([{ id: "nb_alpha", title: "Alpha Notes" }]);

  render(<App />);
  const user = userEvent.setup();
  await screen.findByRole("heading", { name: "Library" });

  expect(screen.queryByPlaceholderText("Search notebooks")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Search notebooks" }));
  const input = screen.getByPlaceholderText("Search notebooks");
  await user.type(input, "Alpha");

  await waitFor(() => expect(fetch).toHaveBeenCalledWith("/api/v1/notebooks?query=Alpha", expect.anything()));
  await user.click(screen.getByRole("button", { name: "Close search" }));
  expect(screen.queryByPlaceholderText("Search notebooks")).not.toBeInTheDocument();
});

test("keeps featured notebook rows isolated as coming-soon placeholders", async () => {
  fetchHandler = authenticatedLibraryHandler([]);

  render(<App />);
  const user = userEvent.setup();
  const table = await screen.findByRole("table", { name: "Featured notebooks" });
  const placeholder = table.querySelector('[data-placeholder="true"]');

  expect(placeholder).toBeInTheDocument();
  await user.click(within(table).getByRole("button", { name: /Open Benjamin Franklin/ }));
  expect(await screen.findByText("Featured notebooks are coming soon.")).toBeInTheDocument();
});

function authenticatedLibraryHandler(notebooks: Array<{ id: string; title: string; recent_at?: string }>) {
  return async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.startsWith("/api/v1/notebooks?") && method === "GET") return json({ notebooks });
    if (url.endsWith("/api/v1/auth/sign-out")) return new Response(null, { status: 204 });
    return json({ error: { code: "not_found" } }, 404);
  };
}

function authenticatedWorkspaceHandler() {
  return async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    return json({ error: { code: "not_found" } }, 404);
  };
}

function json(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" }
  });
}
