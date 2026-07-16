import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { App } from "./App";
import { queryClient } from "./queryClient";

let fetchHandler: (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

class FakeEventSource {
  static instances: FakeEventSource[] = [];

  readonly url: string;
  readonly listeners = new Map<string, Set<EventListener>>();
  closed = false;

  constructor(url: string | URL) {
    this.url = String(url);
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: EventListener) {
    const listeners = this.listeners.get(type) ?? new Set<EventListener>();
    listeners.add(listener);
    this.listeners.set(type, listeners);
  }

  removeEventListener(type: string, listener: EventListener) {
    this.listeners.get(type)?.delete(listener);
  }

  close() {
    this.closed = true;
  }

  emit(type: string, data: unknown) {
    const event = new MessageEvent(type, { data: JSON.stringify(data) });
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }
}

beforeEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
  window.history.pushState(null, "", "/");
  document.documentElement.lang = "en";
  queryClient.clear();
  document.cookie = "nn_csrf=test-token";
  FakeEventSource.instances = [];
  HTMLElement.prototype.scrollTo = vi.fn();
  Object.defineProperty(window.navigator, "language", { value: "en-US", configurable: true });
  vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({
    locale: "en-US", calendar: "gregory", numberingSystem: "latn", timeZone: "Asia/Shanghai"
  });
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
  vi.stubGlobal("EventSource", FakeEventSource);
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

test("restores the private Chat and enables the composer", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = authenticatedWorkspaceHandler();

  render(<App />);
  const user = userEvent.setup();

  await screen.findByRole("heading", { name: "My Research Topic" });
  const sources = screen.getByRole("region", { name: "Sources" });
  expect(sources).toBeInTheDocument();
  const chat = screen.getByRole("region", { name: "Chat" });
  expect(chat).toHaveAttribute("data-chat-framework", "@assistant-ui/react");
  expect(await within(chat).findByRole("textbox", { name: "Message Nano Notebook" })).toBeEnabled();
  expect(within(chat).getByText("Chat will start here")).toBeInTheDocument();
  expect(within(chat).getByText("Answers use model knowledge and are not based on Notebook Sources.")).toBeInTheDocument();
  expect(screen.getByRole("region", { name: "Studio" })).toBeInTheDocument();

  await user.click(within(sources).getByRole("button", { name: "Add sources" }));
  expect(await screen.findByText("This feature is coming soon.")).toBeInTheDocument();
  expect(fetch).toHaveBeenCalledWith("/api/v1/notebooks/nb_test/chats", expect.anything());
  expect(fetch).toHaveBeenCalledWith("/api/v1/chats/chat_test", expect.anything());
});

test("submits one durable Message and projects the final answer from Run SSE", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  let admittedMessageID = "";
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [] });
    if (url.endsWith("/api/v1/chats/chat_test/messages") && method === "POST") {
      const body = JSON.parse(String(init?.body)) as { id: string; content: string; time_zone: string };
      admittedMessageID = body.id;
      expect(body.id).toMatch(/^[0-9a-f-]{36}$/);
      expect(body.content).toBe("Why does a KV cache help?");
      expect(body.time_zone).toBe("Asia/Shanghai");
      expect(new Headers(init?.headers).get("X-CSRF-Token")).toBe("test-token");
      return json({ message_id: body.id, run_id: "run_test", status: "queued" }, 202);
    }
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  const chat = await screen.findByRole("region", { name: "Chat" });
  const composer = await within(chat).findByRole("textbox", { name: "Message Nano Notebook" });
  await user.type(composer, "Why does a KV cache help?");
  await user.click(within(chat).getByRole("button", { name: "Send message" }));

  expect(await within(chat).findByText("Why does a KV cache help?")).toBeInTheDocument();
  expect(within(chat).getByRole("status")).toHaveTextContent("Waiting to start…");
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
  expect(FakeEventSource.instances[0]?.url).toBe("/api/v1/agent-runs/run_test/events");

  act(() => {
    FakeEventSource.instances[0]?.emit("run", {
      run: { id: "run_test", input_message_id: admittedMessageID, status: "running", error_code: null },
      message: null
    });
  });
  await waitFor(() => expect(within(chat).getByRole("status")).toHaveTextContent("Generating answer…"));

  act(() => {
    FakeEventSource.instances[0]?.emit("run", {
      run: { id: "run_test", input_message_id: admittedMessageID, status: "completed", error_code: null },
      message: {
        id: "msg_answer",
        role: "assistant",
        content: "It reuses the keys and values already computed for earlier tokens.",
        created_at: "2026-07-14T12:00:00Z"
      }
    });
  });

  expect(await within(chat).findByText("It reuses the keys and values already computed for earlier tokens.")).toBeInTheDocument();
  expect(within(chat).getByText("Answers use model knowledge and are not based on Notebook Sources.")).toBeInTheDocument();
  expect(within(chat).queryByRole("status")).not.toBeInTheDocument();
  expect(FakeEventSource.instances[0]?.closed).toBe(true);
  expect(admittedMessageID).not.toBe("");
});

test("creates the first private Chat with one bootstrap idempotency key", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  let bootstrapKey = "";
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [] });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "POST") {
      bootstrapKey = new Headers(init?.headers).get("Idempotency-Key") ?? "";
      expect(new Headers(init?.headers).get("X-CSRF-Token")).toBe("test-token");
      return json({ chat: { id: "chat_created", notebook_id: "nb_test", title: "New chat" } }, 201);
    }
    if (url.endsWith("/api/v1/chats/chat_created")) return json({ chat: { id: "chat_created", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [] });
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);

  const chat = await screen.findByRole("region", { name: "Chat" });
  expect(await within(chat).findByRole("textbox", { name: "Message Nano Notebook" })).toBeEnabled();
  expect(bootstrapKey).toMatch(/^[0-9a-f-]{36}$/);
  expect(fetch).toHaveBeenCalledWith("/api/v1/notebooks/nb_test/chats", expect.objectContaining({ method: "POST" }));
});

test("reconnects an active Run after refresh and shows terminal failure without an Assistant Message", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats")) return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test")) return json({
      chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" },
      messages: [{ id: "msg_question", chat_id: "chat_test", role: "user", content: "Will this work?", created_at: "2026-07-14T12:00:00Z" }],
      runs: [{ id: "run_active", input_message_id: "msg_question", status: "queued", error_code: null }]
    });
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const chat = await screen.findByRole("region", { name: "Chat" });
  expect(await within(chat).findByText("Will this work?")).toBeInTheDocument();
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));

  act(() => {
    FakeEventSource.instances[0]?.emit("run", {
      run: { id: "run_active", input_message_id: "msg_question", status: "failed", error_code: "model_unavailable" },
      message: null
    });
  });

  expect(await within(chat).findByText("The answer could not be generated. Try again.")).toBeInTheDocument();
  expect(within(chat).getByRole("button", { name: "Retry" })).toBeInTheDocument();
  expect(within(chat).getByText("Answers use model knowledge and are not based on Notebook Sources.")).toBeInTheDocument();
  expect(within(chat).getByRole("textbox", { name: "Message Nano Notebook" })).toBeEnabled();
  expect(FakeEventSource.instances[0]?.closed).toBe(true);
});

test("stops an active Run and retries the same User Message with one idempotency key", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  let retryKey = "";
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats")) return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test")) return json({
      chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" },
      messages: [{ id: "msg_question", chat_id: "chat_test", role: "user", content: "Stop and retry this", created_at: "2026-07-14T12:00:00Z" }],
      runs: [{ id: "run_active", input_message_id: "msg_question", status: "running", error_code: null }]
    });
    if (url.endsWith("/api/v1/agent-runs/run_active/cancel") && method === "POST") {
      expect(new Headers(init?.headers).get("X-CSRF-Token")).toBe("test-token");
      return json({ run: { id: "run_active", input_message_id: "msg_question", status: "cancelled", error_code: null } });
    }
    if (url.endsWith("/api/v1/agent-runs/run_active/retry") && method === "POST") {
      retryKey = new Headers(init?.headers).get("Idempotency-Key") ?? "";
      expect(new Headers(init?.headers).get("X-CSRF-Token")).toBe("test-token");
      expect(JSON.parse(String(init?.body))).toEqual({ time_zone: "Asia/Shanghai" });
      return json({ run: { id: "run_retry", input_message_id: "msg_question", status: "queued", error_code: null } }, 202);
    }
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  const chat = await screen.findByRole("region", { name: "Chat" });
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
  const composer = within(chat).getByRole("textbox", { name: "Message Nano Notebook" });
  await user.click(within(chat).getByRole("button", { name: "Stop" }));
  expect(await within(chat).findByText("Stopped")).toBeInTheDocument();
  expect(composer).toBeEnabled();
  expect(FakeEventSource.instances[0]?.closed).toBe(true);

  await user.click(within(chat).getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(2));
  expect(FakeEventSource.instances[1]?.url).toBe("/api/v1/agent-runs/run_retry/events");
  expect(retryKey).toMatch(/^[0-9a-f-]{36}$/);
  expect(within(chat).getAllByText("Stop and retry this")).toHaveLength(1);
});

test("reuses the User Message UUID when admission must be retried", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  const attemptedIDs: string[] = [];
  const attemptedTimeZones: string[] = [];
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [] });
    if (url.endsWith("/api/v1/chats/chat_test/messages") && method === "POST") {
      const body = JSON.parse(String(init?.body)) as { id: string; content: string; time_zone: string };
      attemptedIDs.push(body.id);
      attemptedTimeZones.push(body.time_zone);
      if (attemptedIDs.length === 1) return json({ error: { code: "active_run_conflict" } }, 409);
      return json({ message_id: body.id, run_id: "run_retry", status: "queued" }, 202);
    }
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  const chat = await screen.findByRole("region", { name: "Chat" });
  const composer = await within(chat).findByRole("textbox", { name: "Message Nano Notebook" });
  await user.type(composer, "Retry this safely");
  await user.click(within(chat).getByRole("button", { name: "Send message" }));

  expect(await within(chat).findByRole("alert")).toHaveTextContent("Waiting to start…");
  expect(composer).toHaveValue("Retry this safely");
  vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({
    locale: "en-US", calendar: "gregory", numberingSystem: "latn", timeZone: "Asia/Tokyo"
  });
  await user.click(within(chat).getByRole("button", { name: "Send message" }));

  expect(await within(chat).findByText("Retry this safely")).toBeInTheDocument();
  expect(attemptedIDs).toHaveLength(2);
  expect(attemptedIDs[1]).toBe(attemptedIDs[0]);
  expect(attemptedTimeZones).toEqual(["Asia/Shanghai", "Asia/Shanghai"]);
});

test("clears the private Chat projection after successful sign-out", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = authenticatedWorkspaceHandler();

  render(<App />);
  const user = userEvent.setup();
  const chat = await screen.findByRole("region", { name: "Chat" });
  await within(chat).findByRole("textbox", { name: "Message Nano Notebook" });
  expect(queryClient.getQueryData(["private-chat", "nb_test"])).toBeDefined();

  await user.click(screen.getByRole("button", { name: "Open user menu" }));
  await user.click(screen.getByRole("menuitem", { name: "Sign out" }));

  expect(await screen.findByRole("button", { name: "Create account" })).toBeInTheDocument();
  expect(queryClient.getQueryData(["private-chat", "nb_test"])).toBeUndefined();
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

test("keeps the source-less Chat disclosure localized in Simplified Chinese", async () => {
  Object.defineProperty(window.navigator, "language", { value: "zh-CN", configurable: true });
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = authenticatedWorkspaceHandler();

  render(<App />);

  const chat = await screen.findByRole("region", { name: "对话" });
  expect(within(chat).getByText("回答使用模型知识，不基于笔记本来源。")).toBeInTheDocument();
});

test("uses the local Material Symbols system throughout authentication", async () => {
  render(<App />);

  const heading = await screen.findByRole("heading", { name: "Nano Notebook" });
  const panel = heading.closest(".auth-panel");
  expect(panel?.querySelector(".material-symbol")).toBeInTheDocument();
  expect(panel?.querySelector("svg")).not.toBeInTheDocument();
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
  return async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [] });
    if (url.endsWith("/api/v1/auth/sign-out") && method === "POST") return new Response(null, { status: 204 });
    return json({ error: { code: "not_found" } }, 404);
  };
}

function json(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" }
  });
}
