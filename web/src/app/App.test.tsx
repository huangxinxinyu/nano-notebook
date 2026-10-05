import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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

  await screen.findByRole("heading", { name: "My Research Topic" });
  const sources = screen.getByRole("region", { name: "Sources" });
  expect(sources).toBeInTheDocument();
  const chat = screen.getByRole("region", { name: "Chat" });
  expect(chat).toHaveAttribute("data-chat-framework", "@assistant-ui/react");
  const composer = await within(chat).findByRole("textbox", { name: "Message Nano Notebook" });
  await waitFor(() => expect(composer).toBeEnabled());
  expect(within(chat).getByText("Chat will start here")).toBeInTheDocument();
  expect(within(chat).getByText("Answers use model knowledge and are not based on Notebook Sources.")).toBeInTheDocument();
  expect(screen.getByRole("region", { name: "Studio" })).toBeInTheDocument();

  expect(await within(sources).findByText("Saved sources will appear here")).toBeInTheDocument();
  expect(within(sources).getByRole("searchbox", { name: "Search the web for new sources" })).toBeInTheDocument();
  expect(within(sources).getByRole("button", { name: "Add sources" })).toBeInTheDocument();
  expect(within(sources).queryByText("Fast Research")).not.toBeInTheDocument();
  expect(fetch).toHaveBeenCalledWith("/api/v1/notebooks/nb_test/chats", expect.anything());
  expect(fetch).toHaveBeenCalledWith("/api/v1/chats/chat_test", expect.anything());
});

test("offers exactly the four Sprint 11 Studio outputs and excludes Quiz", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = authenticatedWorkspaceHandler();

  render(<App />);

  const studio = await screen.findByRole("region", { name: "Studio" });
  expect(within(studio).getByRole("button", { name: "Report" })).toBeInTheDocument();
  expect(within(studio).getByRole("button", { name: "Flashcards" })).toBeInTheDocument();
  expect(within(studio).getByRole("button", { name: "Mind map" })).toBeInTheDocument();
  expect(within(studio).getByRole("button", { name: "Data table" })).toBeInTheDocument();
  expect(within(studio).queryByRole("button", { name: "Quiz" })).not.toBeInTheDocument();
  expect(studio.querySelectorAll(".studio-action-card")).toHaveLength(4);
});

test("creates a Report from the selected ready Sources", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  let admittedBody: Record<string, unknown> | undefined;
  const workspace = authenticatedWorkspaceHandler();
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [
      { id: "src_ready", notebook_id: "nb_test", title: "research.pdf", format: "pdf", byte_size: 2048, state: "ready", open_action: { kind: "none" } }
    ] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [], citations: [], source_ids: ["src_ready"] });
    if (url.endsWith("/api/v1/notebooks/nb_test/studio-outputs") && method === "GET") return json({ outputs: [] });
    if (url.endsWith("/api/v1/notebooks/nb_test/studio-outputs") && method === "POST") {
      admittedBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
      expect(new Headers(init?.headers).get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
      expect(new Headers(init?.headers).get("X-CSRF-Token")).toBe("test-token");
      return json({ output: { id: "out_report", notebook_id: "nb_test", kind: "report", locale: "en", title: null, run_id: "run_report", source_count: 1, status: "queued", created_at: "2026-07-28T08:00:00Z", updated_at: "2026-07-28T08:00:00Z" } }, 202);
    }
    return workspace(input, init);
  };

  render(<App />);
  const user = userEvent.setup();
  const studio = await screen.findByRole("region", { name: "Studio" });
  await user.click(within(studio).getByRole("button", { name: "Report" }));

  await waitFor(() => expect(admittedBody).toEqual({ kind: "report", locale: "en", source_ids: ["src_ready"] }));
  expect(await within(studio).findByText("Generating…")).toBeInTheDocument();
  expect(within(studio).getByText(/1 source/)).toBeInTheDocument();
  await waitFor(() => expect(FakeEventSource.instances.some((item) => item.url === "/api/v1/studio-outputs/out_report/events")).toBe(true));
  act(() => FakeEventSource.instances.find((item) => item.url === "/api/v1/studio-outputs/out_report/events")?.emit("studio-output", {
    output: { id: "out_report", notebook_id: "nb_test", kind: "report", locale: "en", title: "Generated Report", run_id: "run_report", source_count: 1, status: "completed", artifact: { title: "Generated Report", summary: "Done.", sections: [{ id: "sec_1", heading: "Summary", markdown: "Grounded.", source_ids: ["src_ready"] }] }, created_at: "2026-07-28T08:00:00Z", updated_at: "2026-07-28T08:01:00Z" }
  }));
  expect(await within(studio).findByRole("button", { name: "Generated Report" })).toBeEnabled();
});

test("explains Source verification and lets maintainers approve a pinned report", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  const workspace = authenticatedWorkspaceHandler();
  let reviewBody: Record<string, unknown> | undefined;
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [{
      id: "src_review", notebook_id: "nb_test", title: "Unconfirmed report", format: "html", byte_size: 2048,
      state: "processing", open_action: { kind: "none" }, admission: {
        report_id: "sar_11111111111111111111111111111111", status: "review_required", score: 0.52,
        signal_coverage: 0.7, exact_identity_match: false, policy_id: "source-admission-v1",
        policy_sha256: "a".repeat(64), mode: "enforcement"
      }
    }] });
    if (url.endsWith("/api/v1/sources/src_review/admission") && method === "GET") return json({ admission: {
      source_id: "src_review", notebook_id: "nb_test", revision_id: "evr_review", mode: "enforcement",
      report: {
        id: "sar_11111111111111111111111111111111", policy_id: "source-admission-v1", policy_sha256: "a".repeat(64),
        status: "review_required", score: 0.52, signal_coverage: 0.7, exact_identity_match: false,
        components: { provenance: 0.3, extraction: 0.95 },
        reasons: ["extraction_complete", "exact_identity_required", "score_below_threshold"]
      },
      input: { provider_id: "brave", provider_attempts: 1, searches: [{ query: "Unconfirmed report", results: [] }] },
      provider_id: "brave", provider_attempts: 1, created_at: "2026-08-23T00:00:00Z"
    } });
    if (url.endsWith("/api/v1/sources/src_review/admission-review") && method === "POST") {
      reviewBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
      return json({ review: { id: "sarv_22222222222222222222222222222222", decision: "approve" } });
    }
    return workspace(input, init);
  };

  render(<App />);
  const user = userEvent.setup();
  const sources = await screen.findByRole("region", { name: "Sources" });
  await user.click(await within(sources).findByRole("button", { name: "Needs review · 52%" }));
  expect(await screen.findByRole("dialog", { name: "Source verification" })).toBeVisible();
  expect(screen.getByText("No exact source identity was found.")).toBeVisible();
  await user.click(screen.getByRole("button", { name: "Approve source" }));
  await waitFor(() => expect(reviewBody).toEqual({
    report_id: "sar_11111111111111111111111111111111", decision: "approve", note: ""
  }));
});

test("opens a durable Report from Studio Recent", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  const workspace = authenticatedWorkspaceHandler();
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/notebooks/nb_test/studio-outputs") && method === "GET") return json({ outputs: [{
      id: "out_report", notebook_id: "nb_test", kind: "report", locale: "en", title: "Attention Research", run_id: "run_report", source_count: 1, status: "completed",
      artifact: { title: "Attention Research", summary: "A compact account of the key findings.", sections: [{ id: "sec_1", heading: "Key finding", markdown: "Attention is all you need.", source_ids: [] }] },
      created_at: "2026-07-28T08:00:00Z", updated_at: "2026-07-28T08:01:00Z"
    }] });
    return workspace(input, init);
  };

  render(<App />);
  const user = userEvent.setup();
  const studio = await screen.findByRole("region", { name: "Studio" });
  await user.click(await within(studio).findByRole("button", { name: "Attention Research" }));

  const viewer = await screen.findByRole("dialog", { name: "Attention Research" });
  expect(within(viewer).getByText("A compact account of the key findings.")).toBeInTheDocument();
  expect(within(viewer).getByRole("heading", { name: "Key finding" })).toBeInTheDocument();
});

test("opens Owner access management and queues a local invitation", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  let invitation: Record<string, unknown> | undefined;
  const workspace = authenticatedWorkspaceHandler();
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic", role: "owner" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/members")) return json({ members: [{ user_id: "usr_test", display_email: "learner@example.com", role: "owner" }] });
    if (url.endsWith("/api/v1/notebooks/nb_test/invitations") && method === "GET") return json({ invitations: [] });
    if (url.endsWith("/api/v1/notebooks/nb_test/invitations") && method === "POST") {
      invitation = JSON.parse(String(init?.body)) as Record<string, unknown>;
      return json({ invitation: { id: "inv_test", ...invitation, state: "pending" } }, 201);
    }
    return workspace(input, init);
  };

  render(<App />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Manage access" }));
  const dialog = await screen.findByRole("dialog", { name: "Manage access" });
  await user.type(within(dialog).getByLabelText("Invite by email"), "viewer@example.com");
  await user.click(within(dialog).getByRole("button", { name: "Send invitation" }));
  await waitFor(() => expect(invitation).toEqual({ email: "viewer@example.com", role: "viewer", locale: "en" }));
  expect(await screen.findByText("Invitation queued in the local mailbox.")).toBeInTheDocument();
});

test("selects only ready Sources and pins them when admitting a Message", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  let admittedBody: Record<string, unknown> | undefined;
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [
      { id: "src_ready", notebook_id: "nb_test", title: "attention.pdf", format: "pdf", byte_size: 2048, state: "ready" },
      { id: "src_processing", notebook_id: "nb_test", title: "meeting.mp3", format: "mp3", byte_size: 4096, state: "processing" }
    ] });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [], citations: [], source_ids: ["src_ready"] });
    if (url.endsWith("/api/v1/chats/chat_test/messages") && method === "POST") {
      admittedBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
      return json({ message_id: admittedBody.id, run_id: "run_selected", status: "queued" }, 202);
    }
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  const sources = await screen.findByRole("region", { name: "Sources" });
  const readySource = await within(sources).findByRole("checkbox", { name: "Use attention.pdf" });
  await waitFor(() => expect(readySource).toBeChecked());
  expect(within(sources).getByText("Processing")).toBeInTheDocument();
  expect(within(sources).queryByRole("checkbox", { name: "Use meeting.mp3" })).not.toBeInTheDocument();

  const chat = screen.getByRole("region", { name: "Chat" });
  await user.type(await within(chat).findByRole("textbox", { name: "Message Nano Notebook" }), "Summarize attention.");
  await user.click(within(chat).getByRole("button", { name: "Send message" }));

  await waitFor(() => expect(admittedBody?.id).toBeTruthy());
  expect(admittedBody).not.toHaveProperty("source_ids");
  expect(within(chat).getByText("Answers can use the selected Sources (1) and include citations.")).toBeInTheDocument();
});

test("adds a URL Source through the real admission flow", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  let admittedURL = "";
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources") && method === "GET") return json({ sources: [] });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources/urls") && method === "POST") {
      admittedURL = (JSON.parse(String(init?.body)) as { url: string }).url;
      expect(new Headers(init?.headers).get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
      expect(new Headers(init?.headers).get("X-CSRF-Token")).toBe("test-token");
      return json({ source: { id: "src_url", notebook_id: "nb_test", title: "example.com", format: "html", byte_size: 100, state: "processing" } }, 201);
    }
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [], citations: [] });
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  const sources = await screen.findByRole("region", { name: "Sources" });
  await user.click(within(sources).getByRole("button", { name: "Add sources" }));
  const dialog = await screen.findByRole("dialog", { name: "Add sources" });
  expect(within(dialog).queryByRole("searchbox")).not.toBeInTheDocument();
  await user.type(within(dialog).getByLabelText("Web page or YouTube URL"), "https://example.com/research");
  await user.click(within(dialog).getByRole("button", { name: "Add URL" }));

  await waitFor(() => expect(admittedURL).toBe("https://example.com/research"));
});

test("uploads multiple files independently when one item fails", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  const intents: string[] = [];
  const finalized: string[] = [];
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources") && method === "GET") return json({ sources: [] });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources/upload-intents") && method === "POST") {
      const title = (JSON.parse(String(init?.body)) as { title: string }).title;
      intents.push(title);
      const id = title === "paper.pdf" ? "upl_paper" : "upl_photo";
      return json({ upload_intent: { id }, upload: { method: "POST", url: `https://objects.example/${id}`, fields: { key: id } } }, 201);
    }
    if (url === "https://objects.example/upl_paper") return new Response(null, { status: 204 });
    if (url === "https://objects.example/upl_photo") return json({ error: "rejected" }, 400);
    if (url.endsWith("/api/v1/source-upload-intents/upl_paper/finalize")) {
      finalized.push("upl_paper");
      return json({ source: { id: "src_paper" } }, 201);
    }
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [], citations: [] });
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  const sources = await screen.findByRole("region", { name: "Sources" });
  await user.click(within(sources).getByRole("button", { name: "Add sources" }));
  const dialog = await screen.findByRole("dialog", { name: "Add sources" });
  const paperBytes = new TextEncoder().encode("%PDF-good");
  const imageBytes = new Uint8Array([137, 80, 78, 71]);
  const paper = new File([paperBytes], "paper.pdf", { type: "application/pdf" });
  const photo = new File([imageBytes], "photo.png", { type: "image/png" });
  Object.defineProperty(paper, "arrayBuffer", { value: async () => paperBytes.buffer });
  Object.defineProperty(photo, "arrayBuffer", { value: async () => imageBytes.buffer });
  await user.upload(within(dialog).getByLabelText("Choose files"), [paper, photo]);

  await waitFor(() => expect(intents.sort()).toEqual(["paper.pdf", "photo.png"]));
  await waitFor(() => expect(finalized).toEqual(["upl_paper"]));
  expect(await within(dialog).findByText("photo.png · Failed")).toBeInTheDocument();
});

test("renames, retries, and confirms permanent Source removal", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  const actions: string[] = [];
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [
      { id: "src_ready", notebook_id: "nb_test", title: "old-name.pdf", format: "pdf", byte_size: 2048, state: "ready" },
      { id: "src_failed", notebook_id: "nb_test", title: "broken.docx", format: "docx", byte_size: 4096, state: "failed", failure_reason: "content_unreadable" },
      { id: "src_retrieval", notebook_id: "nb_test", title: "unindexed.pdf", format: "pdf", byte_size: 1024, state: "failed", failure_reason: "retrieval_unavailable" }
    ] });
    if (url.endsWith("/api/v1/sources/src_ready") && method === "PATCH") {
      actions.push(`rename:${(JSON.parse(String(init?.body)) as { title: string }).title}`);
      return json({ source: { id: "src_ready", title: "new-name.pdf", state: "ready" } });
    }
    if (url.endsWith("/api/v1/sources/src_failed/retry") && method === "POST") {
      actions.push("retry:src_failed");
      return json({ source_id: "src_failed", state: "processing" }, 202);
    }
    if (url.endsWith("/api/v1/sources/src_ready") && method === "DELETE") {
      actions.push("delete:src_ready");
      return new Response(null, { status: 204 });
    }
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [], citations: [] });
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  const sources = await screen.findByRole("region", { name: "Sources" });
  await within(sources).findByText("old-name.pdf");
  expect(within(sources).getByText("The file content could not be read.")).toBeInTheDocument();
  expect(within(sources).getByText("Search indexing is not configured. Ask an administrator to activate a Retrieval Index Version.")).toBeInTheDocument();
  await user.click(within(sources).getByRole("button", { name: "Retry broken.docx" }));
  await user.click(within(sources).getByRole("button", { name: "Rename old-name.pdf" }));
  const renameDialog = await screen.findByRole("dialog", { name: "Rename source" });
  const title = within(renameDialog).getByLabelText("Source title");
  await user.clear(title);
  await user.type(title, "new-name.pdf");
  await user.click(within(renameDialog).getByRole("button", { name: "Save" }));
  await user.click(within(sources).getByRole("button", { name: "Delete old-name.pdf" }));
  const removeDialog = await screen.findByRole("dialog", { name: "Delete source permanently?" });
  expect(within(removeDialog).getByText("Its citations will remain visible but can no longer reveal the passage.")).toBeInTheDocument();
  await user.click(within(removeDialog).getByRole("button", { name: "Delete permanently" }));

  await waitFor(() => expect(actions).toEqual(["retry:src_failed", "rename:new-name.pdf", "delete:src_ready"]));
});

test("opens an immutable image original only in the left Source panel", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [
      { id: "src_image", notebook_id: "nb_test", title: "diagram.png", format: "png", byte_size: 1024, state: "ready", open_action: { kind: "inline_original", href: "/api/v1/sources/src_image/original-asset", media_type: "image/png" } }
    ] });
    if (url.endsWith("/api/v1/sources/src_image") && method === "GET") return json({ source: {
      id: "src_image", title: "diagram.png", format: "png",
      revision: { coverage: { status: "complete", gaps: [] }, units: [
        { id: "unit_image", kind: "paragraph", text: "Architecture diagram", coordinate: { kind: "image_region", x: 10, y: 20, width: 30, height: 40 } }
      ] }
    } });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [], citations: [] });
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  const sources = await screen.findByRole("region", { name: "Sources" });
  await user.click(await within(sources).findByRole("button", { name: "diagram.png" }));
  const original = await within(sources).findByRole("region", { name: "Original source diagram.png" });
  expect(within(original).getByRole("img", { name: "diagram.png" })).toHaveAttribute("src", "/api/v1/sources/src_image/original-asset");
  expect(screen.queryByRole("dialog", { name: "diagram.png" })).not.toBeInTheDocument();
  expect(within(original).queryByText("Architecture diagram")).not.toBeInTheDocument();
  expect(screen.getByRole("region", { name: "Chat" })).toBeInTheDocument();
  expect(screen.getByRole("region", { name: "Studio" })).toBeInTheDocument();
});

test("opens an immutable PDF original and returns to the Source list", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [
      { id: "src_pdf", notebook_id: "nb_test", title: "paper.pdf", format: "pdf", byte_size: 2048, state: "ready", open_action: { kind: "inline_original", href: "/api/v1/sources/src_pdf/original-asset", media_type: "application/pdf" } }
    ] });
    if (url.endsWith("/api/v1/sources/src_pdf") && method === "GET") return json({ source: {
      id: "src_pdf", title: "paper.pdf", format: "pdf",
      revision: { viewer: { kind: "pages", page_count: 2 }, coverage: { status: "complete", gaps: [] }, units: [
        { id: "unit_pdf", kind: "paragraph", text: "Page evidence", coordinate: { kind: "pdf_region", page: 1 } }
      ] }
    } });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [], citations: [] });
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  const sources = await screen.findByRole("region", { name: "Sources" });
  await user.click(await within(sources).findByRole("button", { name: "paper.pdf" }));
  const original = await within(sources).findByRole("region", { name: "Original source paper.pdf" });
  expect(within(original).getByTitle("paper.pdf")).toHaveAttribute("src", "/api/v1/sources/src_pdf/original-asset");
  expect(within(original).queryByText("Page evidence")).not.toBeInTheDocument();
  expect(screen.queryByRole("dialog", { name: "paper.pdf" })).not.toBeInTheDocument();
  await user.click(within(sources).getByRole("button", { name: "Back to Sources" }));
  expect(await within(sources).findByRole("button", { name: "paper.pdf" })).toBeInTheDocument();
});

test("uses external links and disables unsupported Source opening", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [
      { id: "src_web", notebook_id: "nb_test", title: "Go guide", format: "html", byte_size: 100, state: "ready", open_action: { kind: "external", href: "https://go.dev/doc/" } },
      { id: "src_docx", notebook_id: "nb_test", title: "brief.docx", format: "docx", byte_size: 200, state: "ready", open_action: { kind: "none" } }
    ] });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [], citations: [] });
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);

  const sources = await screen.findByRole("region", { name: "Sources" });
  const link = await within(sources).findByRole("link", { name: "Go guide" });
  expect(link).toHaveAttribute("href", "https://go.dev/doc/");
  expect(link).toHaveAttribute("target", "_blank");
  expect(link).toHaveAttribute("rel", "noreferrer noopener");
	const webSourceRow = link.closest(".source-list-item");
	const favicon = webSourceRow?.querySelector<HTMLImageElement>(".source-site-icon img");
	expect(favicon).toHaveAttribute("src", "https://go.dev/favicon.ico");
	fireEvent.error(favicon!);
	expect(webSourceRow?.querySelector(".source-site-icon .material-symbol")).toHaveTextContent("language");
  const unsupported = within(sources).getByText("brief.docx");
  expect(unsupported.closest("a,button")).toBeNull();
});

test("opens a published Citation without exposing retrieval internals", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [
      { id: "src_1", notebook_id: "nb_test", title: "transformer-notes.pdf", format: "pdf", byte_size: 100, state: "ready", open_action: { kind: "external", href: "https://example.com/transformer-notes" } },
      { id: "src_image", notebook_id: "nb_test", title: "cache-diagram.png", format: "png", byte_size: 100, state: "ready", open_action: { kind: "inline_original", href: "/api/v1/sources/src_image/original-asset", media_type: "image/png" } }
    ] });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({
      chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" },
      messages: [{ id: "msg_answer", role: "assistant", content: "KV caching avoids recomputing prior keys and values.", created_at: "2026-07-20T12:00:00Z" }],
      runs: [],
      citations: [
        { id: "cit_1", message_id: "msg_answer", claim_ordinal: 0, citation_ordinal: 0, claim_text: "KV caching avoids recomputing prior keys and values.", source_id: "src_1", evidence_revision_id: "rev_1", unit_id: "unit_1", start_rune: 10, end_rune: 67 },
        { id: "cit_2", message_id: "msg_answer", claim_ordinal: 0, citation_ordinal: 1, claim_text: "KV caching avoids recomputing prior keys and values.", source_id: "src_image", evidence_revision_id: "rev_image", unit_id: "unit_image", start_rune: 0, end_rune: 7 }
      ]
    });
    if (url.endsWith("/api/v1/citations/cit_1")) return json({ citation: {
      citation: { id: "cit_1", message_id: "msg_answer", claim_ordinal: 0, citation_ordinal: 0, claim_text: "KV caching avoids recomputing prior keys and values.", source_id: "src_1", evidence_revision_id: "rev_1", unit_id: "unit_1", start_rune: 10, end_rune: 67 },
      source_title: "transformer-notes.pdf", source_format: "pdf", unit_kind: "paragraph",
      preview: "The cache stores keys and values from all prior token positions.", coordinate: { page: 12 }
    } });
    if (url.endsWith("/api/v1/citations/cit_2")) return json({ citation: {
      citation: { id: "cit_2", message_id: "msg_answer", claim_ordinal: 0, citation_ordinal: 1, claim_text: "KV caching avoids recomputing prior keys and values.", source_id: "src_image", evidence_revision_id: "rev_image", unit_id: "unit_image", start_rune: 0, end_rune: 7 },
      source_title: "cache-diagram.png", source_format: "png", unit_kind: "paragraph",
      preview: "Diagram", coordinate: { x: 10, y: 20, width: 30, height: 40 }
    } });
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  const chat = await screen.findByRole("region", { name: "Chat" });
  const citation = await within(chat).findByRole("link", { name: "Citation 1 for KV caching avoids recomputing prior keys and values." });
  expect(citation).toHaveAttribute("href", "https://example.com/transformer-notes");
  expect(citation).toHaveAttribute("target", "_blank");
  expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  await user.click(screen.getByRole("tab", { name: "Chat" }));
  expect(screen.getByRole("tab", { name: "Chat" })).toHaveAttribute("aria-selected", "true");
  await user.click(within(chat).getByRole("button", { name: "Citation 2 for KV caching avoids recomputing prior keys and values." }));
  expect(screen.getByRole("tab", { name: "Sources" })).toHaveAttribute("aria-selected", "true");
  const sources = screen.getByRole("region", { name: "Sources" });
  const original = await within(sources).findByRole("region", { name: "Original source cache-diagram.png" });
  expect(within(original).getByRole("img", { name: "cache-diagram.png" })).toHaveAttribute("src", "/api/v1/sources/src_image/original-asset");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
});

test("renders assistant responses as GitHub Flavored Markdown", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = markdownWorkspaceHandler([
    {
      id: "msg_markdown",
      role: "assistant",
      content: [
        "## Study plan",
        "",
        "Use **retrieval** with ~~duplicate work~~ `cached context`.",
        "",
        "- Read the notes",
        "- [x] Keep citations",
        "",
        "| Topic | Status |",
        "| --- | --- |",
        "| Cache | Ready |",
        "",
        "> Verify the evidence.",
        "",
        "[Open the guide](https://example.com/guide)",
        "",
        "```ts",
        "const cached = true;",
        "```"
      ].join("\n"),
      created_at: "2026-07-25T12:00:00Z"
    }
  ]);

  render(<App />);

  const chat = await screen.findByRole("region", { name: "Chat" });
  expect(await within(chat).findByRole("heading", { name: "Study plan", level: 2 })).toBeInTheDocument();
  expect(within(chat).getByText("retrieval").tagName).toBe("STRONG");
  expect(within(chat).getByText("duplicate work").tagName).toBe("DEL");
  expect(within(chat).getByText("cached context").tagName).toBe("CODE");
  expect(within(chat).getByRole("list")).toBeInTheDocument();
  const taskCheckbox = within(chat).getByRole("checkbox");
  expect(taskCheckbox).toBeChecked();
  expect(taskCheckbox.closest("li")).toHaveTextContent("Keep citations");
  expect(within(chat).getByRole("table")).toHaveTextContent("CacheReady");
  expect(within(chat).getByText("Verify the evidence.").closest("blockquote")).toBeInTheDocument();
  const guideLink = within(chat).getByRole("link", { name: "Open the guide" });
  expect(guideLink).toHaveAttribute("href", "https://example.com/guide");
  expect(guideLink).toHaveAttribute("target", "_blank");
  expect(guideLink).toHaveAttribute("rel", "noopener noreferrer");
  expect(within(chat).getByText("const cached = true;").tagName).toBe("CODE");
});

test("keeps user Markdown syntax as literal text", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = markdownWorkspaceHandler([
    { id: "msg_user_markdown", role: "user", content: "## Literal **question**", created_at: "2026-07-25T12:00:00Z" }
  ]);

  render(<App />);

  const chat = await screen.findByRole("region", { name: "Chat" });
  expect(await within(chat).findByText("## Literal **question**")).toBeInTheDocument();
  expect(within(chat).queryByRole("heading", { name: "Literal question" })).not.toBeInTheDocument();
});

test("does not turn raw assistant HTML into DOM elements", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = markdownWorkspaceHandler([
    { id: "msg_raw_html", role: "assistant", content: "Before <img src=x alt=unsafe onerror=alert(1)> after", created_at: "2026-07-25T12:00:00Z" }
  ]);

  render(<App />);

  const chat = await screen.findByRole("region", { name: "Chat" });
  expect(await within(chat).findByText(/Before/)).toHaveTextContent("Before <img src=x alt=unsafe onerror=alert(1)> after");
  expect(within(chat).queryByRole("img", { name: "unsafe" })).not.toBeInTheDocument();
});

test("renders Source references inline and opens the normal Source viewer", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [
      { id: "src_degree", notebook_id: "nb_test", title: "degree-plan.pdf", format: "pdf", byte_size: 100, state: "ready", open_action: { kind: "inline_original", href: "/api/v1/sources/src_degree/original-asset", media_type: "application/pdf" } }
    ] });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({
      chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" },
      messages: [{ id: "msg_source_answer", role: "assistant", content: "**Complete 120 credits** [source:src_degree]. Keep a 2.0 GPA [source:src_degree].", created_at: "2026-07-22T12:00:00Z" }],
      runs: [],
      citations: [{ id: "cit_source", message_id: "msg_source_answer", reference_kind: "source", reference_ordinal: 0, source_id: "src_degree", source_title: "degree-plan.pdf" }]
    });
    if (url.endsWith("/api/v1/sources/src_degree")) return json({ source: {
      id: "src_degree", title: "degree-plan.pdf", format: "pdf",
      revision: { viewer: { kind: "pages", page_count: 2 }, coverage: { status: "complete", gaps: [] }, units: [
        { id: "unit_degree", kind: "paragraph", text: "Complete 120 credits and keep a 2.0 GPA.", coordinate: { kind: "pdf_region", page_number: 1 } }
      ] }
    } });
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  const chat = await screen.findByRole("region", { name: "Chat" });
  expect(within(chat).queryByText(/\[source:/)).not.toBeInTheDocument();
  const references = await within(chat).findAllByRole("button", { name: "Citation 1 for degree-plan.pdf" });
  expect(references).toHaveLength(2);
  expect(within(chat).getByText("Complete 120 credits").tagName).toBe("STRONG");
  expect(references[0]).toHaveTextContent("[1] degree-plan.pdf");
  expect(within(chat).queryByText("[2]")).not.toBeInTheDocument();
  await user.click(references[0]);
  const sources = screen.getByRole("region", { name: "Sources" });
  const original = await within(sources).findByRole("region", { name: "Original source degree-plan.pdf" });
  expect(within(original).getByTitle("degree-plan.pdf")).toHaveAttribute("src", "/api/v1/sources/src_degree/original-asset");
  expect(within(original).queryByText("Complete 120 credits and keep a 2.0 GPA.")).not.toBeInTheDocument();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
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
  await waitFor(() => expect(FakeEventSource.instances.some((item) => item.url === "/api/v1/agent-runs/run_test/events")).toBe(true));
  const runEvents = FakeEventSource.instances.find((item) => item.url === "/api/v1/agent-runs/run_test/events");

  act(() => {
    runEvents?.emit("run", {
      run: {
        id: "run_test", input_message_id: admittedMessageID, status: "running", error_code: null,
        started_at: new Date(Date.now() - 65_000).toISOString(),
        activities: [
          { kind: "discovering_sources", detail: "latest Go release", started_at: new Date(Date.now() - 5_000).toISOString() },
          { kind: "reading_pdf", detail: "Roadmap.pdf · 3–5", started_at: new Date(Date.now() - 3_000).toISOString() }
        ]
      },
      message: null
    });
  });
  await waitFor(() => expect(within(chat).getByRole("status")).toHaveTextContent("Agent working · 1m 5s"));
  expect(within(chat).getByRole("status")).toHaveTextContent("Searching the weblatest Go release");
  expect(within(chat).getByRole("status")).toHaveTextContent("Reading PDFRoadmap.pdf · 3–5");
  expect(within(chat).queryByText(/discovering_sources|reading_pdf/)).not.toBeInTheDocument();

  act(() => {
    runEvents?.emit("run", {
      run: {
        id: "run_test", input_message_id: admittedMessageID, status: "completed", error_code: null,
        started_at: "2026-07-14T12:00:00Z", finished_at: "2026-07-14T12:01:10Z", activities: []
      },
      message: {
        id: "msg_answer",
        role: "assistant",
        content: "It reuses the keys and values already computed for earlier tokens.",
        created_at: "2026-07-14T12:00:00Z"
      }
    });
  });

  expect(await within(chat).findByText("It reuses the keys and values already computed for earlier tokens.")).toBeInTheDocument();
  expect(within(chat).getByText("Worked for 1m 10s")).toBeVisible();
  expect(within(chat).getByText("Answers use model knowledge and are not based on Notebook Sources.")).toBeInTheDocument();
  expect(within(chat).queryByRole("status")).not.toBeInTheDocument();
  expect(runEvents?.closed).toBe(true);
  expect(admittedMessageID).not.toBe("");
});

test("switches one message to Research, exposes the editable plan, and starts the accepted version", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  let admissionBody: Record<string, unknown> | undefined;
  let editedPlan: Record<string, unknown> | undefined;
  let startedVersion = 0;
  let sessionStatus: "awaiting_confirmation" | "queued" = "awaiting_confirmation";
  const plan = {
    title: "Agent Harness architecture research",
    objective: "Choose a durable harness architecture",
    scope: "Open-source Agent Harness implementations",
    research_questions: ["How do their AgentLoops differ?"],
    investigation_tracks: ["Source code", "Evaluations"],
    source_strategy: ["Primary repositories", "Independent reports"],
    analysis_method: ["Compare on shared dimensions"],
    deliverable_outline: ["Executive summary", "Comparison", "Recommendation"],
    completion_criteria: ["Important claims have read-backed links"],
    clarifying_questions: []
  };
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [] });
    if (url.endsWith("/api/v1/notebooks/nb_test/studio-outputs")) return json({ outputs: [] });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [], citations: [], research_sessions: [] });
    if (url.endsWith("/api/v1/chats/chat_test/messages") && method === "POST") {
      admissionBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
      return json({ message_id: admissionBody.id, mode: "research", research_session_id: "research_test", run_id: "run_plan", status: "planning" }, 202);
    }
    if (url.endsWith("/api/v1/research-sessions/research_test") && method === "GET") return json({
      session: { id: "research_test", chat_id: "chat_test", input_message_id: admissionBody?.id, status: sessionStatus, planning_run_id: "run_plan", ...(sessionStatus === "queued" ? { accepted_plan_version: 2, execution_run_id: "run_research" } : {}) },
      plan: { version: editedPlan ? 2 : 1, content: editedPlan ?? plan },
      evidence: { discovered: 34, read: 0, failed: 0 }
    });
    if (url.endsWith("/api/v1/research-sessions/research_test/plan") && method === "PATCH") {
      editedPlan = (JSON.parse(String(init?.body)) as { plan: Record<string, unknown> }).plan;
      return json({ session_id: "research_test", version: 2, plan: editedPlan });
    }
    if (url.endsWith("/api/v1/research-sessions/research_test/start") && method === "POST") {
      startedVersion = (JSON.parse(String(init?.body)) as { plan_version: number }).plan_version;
      sessionStatus = "queued";
      return json({ session_id: "research_test", run_id: "run_research", status: "queued" }, 202);
    }
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  const chat = await screen.findByRole("region", { name: "Chat" });
  await user.click(within(chat).getByRole("button", { name: /Research/ }));
  expect(within(chat).getByText(/Research searches and reads/)).toBeInTheDocument();
  await user.type(within(chat).getByRole("textbox", { name: "Message Nano Notebook" }), "Compare Agent Harnesses.");
  await user.click(within(chat).getByRole("button", { name: "Send message" }));
  await waitFor(() => expect(admissionBody?.mode).toBe("research"));

  const planEditor = await within(chat).findByRole("textbox", { name: "Research plan" });
  const changedPlan = { ...plan, title: "Edited Agent Harness plan" };
  fireEvent.change(planEditor, { target: { value: JSON.stringify(changedPlan, null, 2) } });
  await user.click(within(chat).getByRole("button", { name: "Save plan" }));
  await waitFor(() => expect(editedPlan?.title).toBe("Edited Agent Harness plan"));
  await user.click(within(chat).getByRole("button", { name: "Start research" }));
  await waitFor(() => expect(startedVersion).toBe(2));
  expect(await within(chat).findByText("Discovered")).toBeInTheDocument();
  expect(within(chat).getByText("34")).toBeInTheDocument();
});

test("answers planner questions, then asks for a plan revision", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  let admissionBody: Record<string, unknown> | undefined;
  let answersBody: Record<string, unknown> | undefined;
  let revisionBody: Record<string, unknown> | undefined;
  let sessionStatus: "awaiting_input" | "awaiting_confirmation" | "planning" = "awaiting_input";
  const plan = {
    title: "Iterative retrieval research", objective: "Decide when to adopt iterative retrieval", scope: "Published methods. Assumption: engineers are the audience.",
    research_questions: ["When does iteration help?"], investigation_tracks: ["Candidate methods"], source_strategy: ["Primary papers"],
    analysis_method: ["Compare on shared dimensions"], deliverable_outline: ["Recommendation"], completion_criteria: ["Claims are read-backed"], clarifying_questions: []
  };
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [] });
    if (url.endsWith("/api/v1/notebooks/nb_test/studio-outputs")) return json({ outputs: [] });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [], citations: [], research_sessions: [] });
    if (url.endsWith("/api/v1/chats/chat_test/messages") && method === "POST") {
      admissionBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
      return json({ message_id: admissionBody.id, mode: "research", research_session_id: "research_test", run_id: "run_plan", status: "planning" }, 202);
    }
    if (url.endsWith("/api/v1/research-sessions/research_test") && method === "GET") return json({
      session: { id: "research_test", chat_id: "chat_test", input_message_id: admissionBody?.id, status: sessionStatus, planning_run_id: "run_plan" },
      ...(sessionStatus === "awaiting_confirmation" ? { plan: { version: 1, content: plan } } : {}),
      ...(sessionStatus === "awaiting_input" ? { pending_questions: { action_id: "decision:2/action:0", questions: [
        { id: "audience", header: "Audience", question: "Who reads the report?", options: [{ label: "Backend engineers", description: "Implementation depth" }, { label: "Product leads" }], recommended: 0 },
        { id: "depth", question: "How deep should it go?", options: [{ label: "Survey" }, { label: "Deep dive" }], recommended: 1 }
      ] } } : {}),
      evidence: { discovered: 0, read: 0, failed: 0 }
    });
    if (url.endsWith("/api/v1/research-sessions/research_test/answers") && method === "POST") {
      answersBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
      sessionStatus = "awaiting_confirmation";
      return json({ session_id: "research_test", status: "planning" }, 202);
    }
    if (url.endsWith("/api/v1/research-sessions/research_test/revisions") && method === "POST") {
      revisionBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
      sessionStatus = "planning";
      return json({ session_id: "research_test", message_id: revisionBody.id, run_id: "run_plan_2", status: "planning" }, 202);
    }
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  const chat = await screen.findByRole("region", { name: "Chat" });
  await user.click(within(chat).getByRole("button", { name: /Research/ }));
  await user.type(within(chat).getByRole("textbox", { name: "Message Nano Notebook" }), "When is iterative retrieval worth it?");
  await user.click(within(chat).getByRole("button", { name: "Send message" }));

  const questions = await within(chat).findByRole("region", { name: "A few questions before planning" });
  expect(within(questions).getByRole("radio", { name: /Backend engineers/ })).toBeChecked();
  expect(within(questions).getByRole("radio", { name: /Deep dive/ })).toBeChecked();
  await user.click(within(questions).getByRole("radio", { name: /Product leads/ }));
  await user.type(within(questions).getAllByPlaceholderText("Or write your own answer")[1], "Only the stopping rules");
  await user.click(within(questions).getByRole("button", { name: "Submit answers" }));
  await waitFor(() => expect(answersBody).toEqual({
    action_id: "decision:2/action:0", use_recommended: false,
    answers: [{ id: "audience", choice: "Product leads" }, { id: "depth", text: "Only the stopping rules" }]
  }));

  await within(chat).findByRole("textbox", { name: "Research plan" });
  await user.type(within(chat).getByPlaceholderText(/Describe what to change/), "Focus on open-source methods");
  await user.click(within(chat).getByRole("button", { name: "Revise plan" }));
  await waitFor(() => expect(revisionBody?.content).toBe("Focus on open-source methods"));
  expect(await within(chat).findByText("Building a research plan…")).toBeInTheDocument();
});

test("loads the exact delegated Research Session without opening full Discovery while it is searching", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  let admittedMessageID = "";
  let discoverySessionReads = 0;
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic", role: "owner" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [
      { id: "src_existing", notebook_id: "nb_test", title: "Existing source.pdf", format: "pdf", byte_size: 100, state: "ready", failure_reason: null, open_action: { kind: "inline_original", href: "/api/v1/sources/src_existing/original-asset", media_type: "application/pdf" } }
    ] });
    if (url.endsWith("/api/v1/notebooks/nb_test/source-discovery-sessions/latest")) return new Response(null, { status: 204 });
    if (url.endsWith("/api/v1/source-discovery-sessions/dss_research")) {
      discoverySessionReads++;
      return json({ session: {
        id: "dss_research", notebook_id: "nb_test", query: "Go learning material", status: "searching", candidates: []
      } });
    }
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [] });
    if (url.endsWith("/api/v1/chats/chat_test/messages") && method === "POST") {
      const body = JSON.parse(String(init?.body)) as { id: string };
      admittedMessageID = body.id;
      return json({ message_id: body.id, run_id: "run_research", status: "queued" }, 202);
    }
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  const chat = await screen.findByRole("region", { name: "Chat" });
  await user.type(await within(chat).findByRole("textbox", { name: "Message Nano Notebook" }), "Help me collect Go learning material");
  await user.click(within(chat).getByRole("button", { name: "Send message" }));
  await waitFor(() => expect(FakeEventSource.instances.some((item) => item.url === "/api/v1/agent-runs/run_research/events")).toBe(true));
  const runEvents = FakeEventSource.instances.find((item) => item.url === "/api/v1/agent-runs/run_research/events");

  act(() => {
    runEvents?.emit("run", {
      run: { id: "run_research", input_message_id: admittedMessageID, status: "running", error_code: null, discovery_session_id: "dss_research" },
      message: null
    });
  });

  const sources = screen.getByRole("region", { name: "Sources" });
  expect(await within(sources).findByDisplayValue("Go learning material")).toBeInTheDocument();
  expect(within(sources).queryByText("Source discovery")).not.toBeInTheDocument();
  expect(within(sources).getByRole("status")).toHaveTextContent("Searching…");
  expect(within(sources).getByText("Existing source.pdf")).toBeVisible();
  expect(within(sources).getByRole("checkbox", { name: "Use Existing source.pdf" })).toBeVisible();
  expect(sources.querySelector(".source-panel-existing-peek")).not.toBeInTheDocument();
  expect(screen.queryByRole("dialog", { name: "Add sources" })).not.toBeInTheDocument();
  expect(document.querySelector(".workspace-panels")).not.toHaveClass("workspace-panels--source-discovery");
  const readsBeforeOpen = discoverySessionReads;
  await user.click(within(sources).getByRole("button", { name: "Existing source.pdf" }));
  expect(await within(sources).findByRole("region", { name: "Original source Existing source.pdf" })).toBeInTheDocument();
  await user.click(within(sources).getByRole("button", { name: "Back to Sources" }));
  expect(within(sources).getByDisplayValue("Go learning material")).toBeInTheDocument();
  expect(within(sources).getByRole("status")).toHaveTextContent("Searching…");
  expect(discoverySessionReads).toBe(readsBeforeOpen);

  const discoveryEvents = FakeEventSource.instances.find((item) => item.url === "/api/v1/source-discovery-sessions/dss_research/events");
  act(() => discoveryEvents?.emit("discovery", { session: {
    id: "dss_research", notebook_id: "nb_test", query: "Go learning material", status: "ready",
    candidates: [{
      id: "candidate_novel", ordinal: 0, title: "New Go guide", canonical_url: "https://example.com/go-guide",
      display_url: "example.com/go-guide", snippet: "A new source.", selected: true, status: "discovered"
    }]
  } }));
  await waitFor(() => expect(document.querySelector(".workspace-panels")).toHaveClass("workspace-panels--source-discovery"));
});

test("opens all ready Research sources only after View and returns to the compact card", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  const workspace = authenticatedWorkspaceHandler();
  const candidates = Array.from({ length: 4 }, (_, index) => ({
    id: `candidate_${index + 1}`,
    ordinal: index,
    title: `Research source ${index + 1}`,
    canonical_url: `https://example.com/source-${index + 1}`,
    display_url: `example.com/source-${index + 1}`,
    snippet: `Summary ${index + 1}.`,
    selected: true,
    status: "discovered"
  }));
  fetchHandler = async (input, init) => {
    if (String(input).endsWith("/api/v1/notebooks/nb_test/source-discovery-sessions/latest")) {
      return json({ session: { id: "dss_ready", notebook_id: "nb_test", query: "ready research", status: "ready", candidates } });
    }
    return workspace(input, init);
  };

  render(<App />);
  const user = userEvent.setup();
  const sources = await screen.findByRole("region", { name: "Sources" });

  expect(await within(sources).findByText("Research completed")).toBeVisible();
  expect(within(sources).getByRole("link", { name: /Research source 3/ })).toBeVisible();
  expect(within(sources).queryByRole("link", { name: /Research source 4/ })).not.toBeInTheDocument();
  expect(within(sources).getByText("Additional sources: 1")).toBeVisible();
  expect(document.querySelector(".workspace-panels")).not.toHaveClass("workspace-panels--source-discovery");

  await user.click(within(sources).getByRole("button", { name: "View" }));
  expect(await within(sources).findByText("Source discovery")).toBeVisible();
  expect(within(sources).getByRole("link", { name: /Research source 4/ })).toBeVisible();
  expect(within(sources).getByRole("checkbox", { name: "Research source 4" })).toBeVisible();
  expect(document.querySelector(".workspace-panels")).toHaveClass("workspace-panels--source-discovery");

  await user.click(within(sources).getByRole("button", { name: "Close" }));
  expect(await within(sources).findByText("Research completed")).toBeVisible();
  expect(within(sources).queryByRole("link", { name: /Research source 4/ })).not.toBeInTheDocument();
  expect(document.querySelector(".workspace-panels")).not.toHaveClass("workspace-panels--source-discovery");
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
  const composer = await within(chat).findByRole("textbox", { name: "Message Nano Notebook" });
  await waitFor(() => expect(composer).toBeEnabled());
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
  await waitFor(() => expect(FakeEventSource.instances.some((item) => item.url === "/api/v1/agent-runs/run_active/events")).toBe(true));
  const runEvents = FakeEventSource.instances.find((item) => item.url === "/api/v1/agent-runs/run_active/events");

  act(() => {
    runEvents?.emit("run", {
      run: { id: "run_active", input_message_id: "msg_question", status: "failed", error_code: "model_unavailable" },
      message: null
    });
  });

  expect(await within(chat).findByText("The answer could not be generated. Try again.")).toBeInTheDocument();
  expect(within(chat).getByRole("button", { name: "Retry" })).toBeInTheDocument();
  expect(within(chat).getByText("Answers use model knowledge and are not based on Notebook Sources.")).toBeInTheDocument();
  expect(within(chat).getByRole("textbox", { name: "Message Nano Notebook" })).toBeEnabled();
  expect(runEvents?.closed).toBe(true);
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
  await waitFor(() => expect(FakeEventSource.instances.some((item) => item.url === "/api/v1/agent-runs/run_active/events")).toBe(true));
  const activeRunEvents = FakeEventSource.instances.find((item) => item.url === "/api/v1/agent-runs/run_active/events");
  const composer = within(chat).getByRole("textbox", { name: "Message Nano Notebook" });
  await user.click(within(chat).getByRole("button", { name: "Stop" }));
  expect(await within(chat).findByText("Stopped")).toBeInTheDocument();
  expect(composer).toBeEnabled();
  expect(activeRunEvents?.closed).toBe(true);

  await user.click(within(chat).getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(FakeEventSource.instances.some((item) => item.url === "/api/v1/agent-runs/run_retry/events")).toBe(true));
  expect(retryKey).toMatch(/^[0-9a-f-]{36}$/);
  expect(within(chat).getAllByText("Stop and retry this")).toHaveLength(1);
});

test("hides the Deep Research progress panel immediately after its Run is stopped", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [] });
    if (url.endsWith("/api/v1/notebooks/nb_test/studio-outputs")) return json({ outputs: [] });
    if (url.endsWith("/api/v1/notebooks/nb_test/source-discovery-sessions/latest")) return new Response(null, { status: 204 });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({
      chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" },
      messages: [{ id: "msg_research", chat_id: "chat_test", role: "user", content: "Research this", created_at: "2026-07-14T12:00:00Z" }],
      runs: [{ id: "run_research", input_message_id: "msg_research", status: "running", error_code: null }],
      citations: [],
      research_sessions: [{ id: "research_test", input_message_id: "msg_research", status: "running", execution_run_id: "run_research" }]
    });
    if (url.endsWith("/api/v1/research-sessions/research_test") && method === "GET") return json({
      session: { id: "research_test", chat_id: "chat_test", input_message_id: "msg_research", status: "running", execution_run_id: "run_research" },
      evidence: { discovered: 8, read: 3, failed: 0 }
    });
    if (url.endsWith("/api/v1/agent-runs/run_research/cancel") && method === "POST") return json({
      run: { id: "run_research", input_message_id: "msg_research", status: "cancelled", error_code: null }
    });
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  const chat = await screen.findByRole("region", { name: "Chat" });
  expect(await within(chat).findByRole("region", { name: "Research progress" })).toBeVisible();

  await user.click(within(chat).getByRole("button", { name: "Stop" }));

  await waitFor(() => expect(within(chat).queryByRole("region", { name: "Research progress" })).not.toBeInTheDocument());
  expect(within(chat).getByText("Stopped")).toBeVisible();
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

test("refreshes platform capabilities after an operator signs in", async () => {
  let sessionRequests = 0;
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) {
      sessionRequests++;
      if (sessionRequests === 1) return json({ error: { code: "unauthorized" } }, 401);
      return json({
        user: { id: "usr_operator", email: "operator@example.com" },
        platform_capabilities: ["platform.trace.read", "platform.trace.replay"]
      });
    }
    if (url.endsWith("/api/v1/auth/sign-in") && method === "POST") return json({ user: { id: "usr_operator", email: "operator@example.com" } });
    if (url.endsWith("/api/v1/notebooks") && method === "GET") return json({ notebooks: [] });
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("tab", { name: "Sign in" }));
  await user.type(screen.getByLabelText("Email"), "operator@example.com");
  await user.type(screen.getByLabelText("Password"), "unique local sprint phrase 2026");
  await user.click(screen.getByRole("button", { name: "Sign in" }));

  expect(await screen.findByRole("button", { name: /Traces/ })).toBeInTheDocument();
  expect(sessionRequests).toBe(2);
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

  await waitFor(() => expect(fetch).toHaveBeenCalledWith("/api/v1/notebooks?scope=all&query=Alpha", expect.anything()));
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

test("blocks the Trace Explorer route when the session lacks platform Trace capability", async () => {
  window.history.pushState(null, "", "/admin/traces");
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "owner@example.com" }, platform_capabilities: [] });
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);

  expect(await screen.findByRole("heading", { name: "Trace access restricted" })).toBeInTheDocument();
  expect(fetch).not.toHaveBeenCalledWith(expect.stringContaining("/api/admin/traces"), expect.anything());
});

test("does not treat unrelated path prefixes as Trace admin routes", async () => {
  window.history.pushState(null, "", "/admin/traces-archive");
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "owner@example.com" }, platform_capabilities: [] });
    if (url.endsWith("/api/v1/notebooks")) return json({ notebooks: [] });
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);

  expect(await screen.findByRole("heading", { name: "Library" })).toBeInTheDocument();
  expect(screen.queryByRole("heading", { name: "Trace access restricted" })).not.toBeInTheDocument();
});

test("shows a forbidden Trace Explorer state when the server revokes a stale session grant", async () => {
  window.history.pushState(null, "", "/admin/traces");
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({
      user: { id: "usr_operator", email: "operator@example.com" },
      platform_capabilities: ["platform.trace.read"]
    });
    if (url.startsWith("/api/admin/traces?")) return json({ error: { code: "trace_forbidden" } }, 403);
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);

  expect(await screen.findByText("Trace access restricted")).toBeInTheDocument();
  expect(screen.queryByText("Trace data is temporarily unavailable.")).not.toBeInTheDocument();
});

test("retries a temporarily unavailable Trace Explorer without losing the route", async () => {
  window.history.pushState(null, "", "/admin/traces");
  let requests = 0;
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({
      user: { id: "usr_operator", email: "operator@example.com" },
      platform_capabilities: ["platform.trace.read"]
    });
    if (url.startsWith("/api/admin/traces?")) {
      requests++;
      if (requests === 1) return json({ error: { code: "trace_temporarily_unavailable" } }, 503);
      return json({ schema_version: 1, data: { items: [] } });
    }
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  expect(await screen.findByText("Trace data is temporarily unavailable.")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Retry" }));

  expect(await screen.findByText("No Traces match these filters.")).toBeInTheDocument();
  expect(window.location.pathname).toBe("/admin/traces");
  expect(requests).toBe(2);
});

test("applies a bounded time range to Trace Explorer queries", async () => {
  window.history.pushState(null, "", "/admin/traces");
  const traceQueries: string[] = [];
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({
      user: { id: "usr_operator", email: "operator@example.com" },
      platform_capabilities: ["platform.trace.read"]
    });
    if (url.startsWith("/api/admin/traces?")) {
      traceQueries.push(url);
      const cursor = new URL(url, window.location.origin).searchParams.get("cursor");
      return json({ schema_version: 1, data: { items: [], next_cursor: cursor ? undefined : "cursor-page-2" } });
    }
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  expect(await screen.findByRole("heading", { name: "Trace Explorer" })).toBeInTheDocument();
  expect(await screen.findByText("No Traces match these filters.")).toBeInTheDocument();
  await user.type(screen.getByLabelText("Trace, Run, or Chat prefix"), "run-admin");
  await user.type(screen.getByLabelText("Agent"), "nano-research-agent");
  await user.type(screen.getByLabelText("Model"), "qwen-flash");
  await user.selectOptions(screen.getByLabelText("Status"), "error");
  await user.selectOptions(screen.getByLabelText("State"), "false");
  await user.selectOptions(screen.getByLabelText("Time range"), "24h");
  await user.click(screen.getByRole("button", { name: "Apply filters" }));

  await waitFor(() => expect(traceQueries.some((url) => {
    const query = new URL(url, window.location.origin).searchParams;
    return query.has("started_after") && !query.has("started_before") && query.get("identity_prefix") === "run-admin" &&
      query.get("agent") === "nano-research-agent" && query.get("model") === "qwen-flash" &&
      query.get("status") === "error" && query.get("active") === "false";
  })).toBe(true));
  await user.click(screen.getByRole("button", { name: "Next page" }));
  await waitFor(() => expect(traceQueries.some((url) => new URL(url, window.location.origin).searchParams.get("cursor") === "cursor-page-2")).toBe(true));
  expect(screen.getByRole("button", { name: "Previous page" })).toBeEnabled();
});

test("explores a Trace with synchronized Tree, Timeline, and explicit Replay loading", async () => {
  window.history.pushState(null, "", "/admin/traces");
  let replayRequests = 0;
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({
      user: { id: "usr_operator", email: "operator@example.com" },
      platform_capabilities: ["platform.trace.read", "platform.trace.replay"]
    });
    if (url.startsWith("/api/admin/traces?") || url === "/api/admin/traces") return json({ schema_version: 1, data: {
      items: [{
        summary: {
          trace_id: "trace-admin", run_id: "run-admin", chat_id: "chat-admin", notebook_id: "notebook-admin",
          root_span_id: "root-admin", agent_name: "nano-research-agent", started_at_unix_nano: 1700000000000000000,
          last_observed_unix_nano: 1700000005000000000, ended_at_unix_nano: null, duration_nanoseconds: null,
          status: "", active: true, models: ["qwen-flash"], input_tokens: 12, output_tokens: null,
          total_tokens: null, cost: { known: true, amount: 0.002, currency: "USD", source: "provider_reported" }, attempt_count: 1
        },
        committed_sequence: 6, projected_sequence: 5, projection_lagged: true
      }],
      next_cursor: "next-page"
    }});
    if (url === "/api/admin/traces/trace-admin") return json({ schema_version: 1, data: {
      committed_sequence: 6, projected_sequence: 6,
      projection: {
        summary: {
          trace_id: "trace-admin", run_id: "run-admin", chat_id: "chat-admin", notebook_id: "notebook-admin",
          root_span_id: "root-admin", agent_name: "nano-research-agent", started_at_unix_nano: 1700000000000000000,
          last_observed_unix_nano: 1700000005000000000, ended_at_unix_nano: null, duration_nanoseconds: null,
          status: "", active: true, models: ["qwen-flash"], input_tokens: 12, output_tokens: null,
          total_tokens: null, cost: { known: false, amount: null, currency: "", source: "" }, attempt_count: 1
        },
        spans: [
          { trace_id: "trace-admin", span_id: "root-admin", parent_span_id: "", name: "agent.execution", start_sequence: 1, end_sequence: null, started_at_unix_nano: 1700000000000000000, ended_at_unix_nano: null, duration_nanoseconds: null, status: "", start_attributes: [], end_attributes: [], replay: [], model: null },
          { trace_id: "trace-admin", span_id: "model-admin", parent_span_id: "root-admin", name: "gen_ai.model.call", start_sequence: 2, end_sequence: 4, started_at_unix_nano: 1700000001000000000, ended_at_unix_nano: 1700000003000000000, duration_nanoseconds: 2000000000, status: "ok", start_attributes: [], end_attributes: [{ Key: "agent.error.kind", Value: { Kind: "string", String: "gateway_timeout" } }], replay: [{ attachment_id: "019bf000-0000-7000-8000-000000000555", class: "model_request", record_sequence: 2 }], model: { requested_model: "qwen-flash", selected_model: "qwen-flash", provider: "aliyun", input_tokens: 12, output_tokens: null, total_tokens: null, cached_tokens: null, reasoning_tokens: null, gateway_retries: 0, gateway_fallbacks: 0, duration_nanoseconds: 2000000000, cost: { known: true, amount: 0.002, currency: "USD", source: "provider_reported" } } },
          { trace_id: "trace-admin", span_id: "search-admin", parent_span_id: "root-admin", name: "agent.action", start_sequence: 5, end_sequence: 6, started_at_unix_nano: 1700000003100000000, ended_at_unix_nano: 1700000003500000000, duration_nanoseconds: 400000000, status: "ok", start_attributes: [{ Key: "agent.action.name", Value: { Kind: "string", String: "search_evidence" } }, { Key: "nano.rag.search.purpose", Value: { Kind: "string", String: "compare methods" } }], end_attributes: [{ Key: "nano.rag.dense.candidate_count", Value: { Kind: "int64", Int64: 12 } }, { Key: "nano.rag.bm25.candidate_count", Value: { Kind: "int64", Int64: 9 } }, { Key: "nano.rag.rrf.candidate_ids", Value: { Kind: "string", String: "[\"chunk-b\",\"chunk-a\"]" } }, { Key: "nano.rag.rerank.candidate_ids", Value: { Kind: "string", String: "[\"chunk-a\"]" } }, { Key: "nano.rag.retrieval.degraded", Value: { Kind: "bool", Bool: true } }, { Key: "nano.rag.retrieval.degradations", Value: { Kind: "string", String: "[\"reranker_unavailable\"]" } }], replay: [], model: null },
          { trace_id: "trace-admin", span_id: "grounding-admin", parent_span_id: "root-admin", name: "nano.grounding", start_sequence: 7, end_sequence: 8, started_at_unix_nano: 1700000003600000000, ended_at_unix_nano: 1700000003700000000, duration_nanoseconds: 100000000, status: "ok", start_attributes: [], end_attributes: [{ Key: "nano.rag.grounding.research_performed", Value: { Kind: "bool", Bool: true } }, { Key: "nano.rag.source_reference.eligible_source_count", Value: { Kind: "int64", Int64: 2 } }, { Key: "nano.rag.source_reference.valid_count", Value: { Kind: "int64", Int64: 1 } }, { Key: "nano.rag.source_reference.discarded_marker_count", Value: { Kind: "int64", Int64: 1 } }], replay: [], model: null },
          { trace_id: "trace-admin", span_id: "publish-admin", parent_span_id: "root-admin", name: "nano.publication", start_sequence: 9, end_sequence: 10, started_at_unix_nano: 1700000003800000000, ended_at_unix_nano: 1700000003900000000, duration_nanoseconds: 100000000, status: "ok", start_attributes: [], end_attributes: [{ Key: "nano.rag.grounding.outcome", Value: { Kind: "string", String: "source_cited" } }], replay: [], model: null }
        ],
        events: [{ trace_id: "trace-admin", sequence: 3, span_id: "root-admin", name: "nano.run.admitted", occurred_at_unix_nano: 1700000000500000000, attributes: [] }],
        links: [{ trace_id: "trace-admin", sequence: 5, span_id: "model-admin", name: "retries", target_trace_id: "trace-previous", target_span_id: "child-previous", occurred_at_unix_nano: 1700000004000000000, attributes: [] }]
      }
    }});
    if (url === "/api/admin/traces/trace-previous") return json({ schema_version: 1, data: linkedTraceDetail() });
    if (url.includes("/api/admin/traces/trace-admin/replay/019bf000-0000-7000-8000-000000000555")) {
      replayRequests++;
      return json({ schema_version: 1, data: {
        replay_id: "019bf000-0000-7000-8000-000000000555", trace_id: "trace-admin", span_id: "model-admin",
        class: "model_request", payload: { messages: [{ role: "user", content: "Explain KV cache" }] }
      }});
    }
    return json({ error: { code: "not_found" } }, 404);
  };

  const view = render(<App />);
  const user = userEvent.setup();
  expect(await screen.findByRole("heading", { name: "Trace Explorer" })).toBeInTheDocument();
  expect(await screen.findByText("Projection lagged")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Open Trace run-admin" }));

  const summary = await screen.findByRole("region", { name: "Trace summary" });
  expect(within(summary).getByText("Started")).toBeInTheDocument();
  expect(within(summary).getByText("Last observed")).toBeInTheDocument();
  expect(within(summary).getByText("Attempts")).toBeInTheDocument();
  const rag = await screen.findByRole("region", { name: "RAG execution" });
  expect(within(rag).getByText("compare methods")).toBeInTheDocument();
  expect(within(rag).getByText("12 → 9")).toBeInTheDocument();
  expect(within(rag).getByText("chunk-b → chunk-a")).toBeInTheDocument();
  expect(within(rag).getByText("reranker_unavailable")).toBeInTheDocument();
  expect(within(rag).getByText("1 valid / 1 discarded")).toBeInTheDocument();
  expect(within(rag).getByText("2 eligible Sources")).toBeInTheDocument();
  expect(within(rag).getByText("source_cited")).toBeInTheDocument();
  const tree = await screen.findByRole("tree", { name: "Trace Tree" });
  const timeline = screen.getByRole("region", { name: "Trace Timeline" });
  expect(within(tree).getByText("agent.execution")).toBeInTheDocument();
  expect(within(timeline).getByText("Unfinished")).toBeInTheDocument();
  await user.click(within(timeline).getByRole("button", { name: "Select gen_ai.model.call in Timeline" }));
  expect(within(tree).getByRole("treeitem", { name: /gen_ai.model.call/ })).toHaveAttribute("aria-selected", "true");
  const inspector = screen.getByRole("region", { name: "Inspector" });
  expect(within(inspector).getByText("Kind")).toBeInTheDocument();
  expect(within(inspector).getByText("Model call")).toBeInTheDocument();
  expect(within(inspector).getByText("Started")).toBeInTheDocument();
  expect(within(inspector).getByText("Ended")).toBeInTheDocument();
  expect(within(inspector).getByText("gateway_timeout")).toBeInTheDocument();
  await user.click(within(tree).getByRole("button", { name: "Collapse agent.execution" }));
  expect(within(tree).queryByRole("treeitem", { name: /gen_ai.model.call/ })).not.toBeInTheDocument();
  await user.click(within(timeline).getByRole("button", { name: "Select gen_ai.model.call in Timeline" }));
  expect(within(tree).getByRole("treeitem", { name: /gen_ai.model.call/ })).toHaveAttribute("aria-selected", "true");
  expect(window.location.search).toBe("?span=model-admin");

  await user.click(screen.getByRole("tab", { name: "Replay" }));
  expect(fetch).not.toHaveBeenCalledWith(expect.stringContaining("/replay/019bf000"), expect.anything());
  await user.click(screen.getByRole("button", { name: "Load sensitive Replay" }));
  expect(await screen.findByText("Explain KV cache")).toBeInTheDocument();
  expect(screen.getAllByText("Unknown").length).toBeGreaterThan(0);
  expect(screen.getAllByText("0.002 USD").length).toBeGreaterThan(0);
  expect(screen.getByText("provider_reported")).toBeInTheDocument();
  expect(JSON.stringify(localStorage)).not.toContain("Explain KV cache");
  expect(replayRequests).toBe(1);

  view.unmount();
  queryClient.clear();
  render(<App />);
  const refreshedTree = await screen.findByRole("tree", { name: "Trace Tree" });
  expect(within(refreshedTree).getByRole("treeitem", { name: /gen_ai.model.call/ })).toHaveAttribute("aria-selected", "true");
  expect(screen.queryByText("Explain KV cache")).not.toBeInTheDocument();
  expect(replayRequests).toBe(1);

  const refreshedTimeline = screen.getByRole("region", { name: "Trace Timeline" });
  await user.click(within(refreshedTimeline).getByRole("button", { name: "Open retries link to trace-previous" }));
  expect(window.location.pathname).toBe("/admin/traces/trace-previous");
  expect(window.location.search).toBe("?span=child-previous");
  const linkedTree = await screen.findByRole("tree", { name: "Trace Tree" });
  expect(within(linkedTree).getByRole("treeitem", { name: /agent.action/ })).toHaveAttribute("aria-selected", "true");
});

test("labels Source-processing Traces by workload instead of pretending they are Runs", async () => {
  window.history.pushState(null, "", "/admin/traces");
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({
      user: { id: "usr_operator", email: "operator@example.com" },
      platform_capabilities: ["platform.trace.read"]
    });
    if (url.startsWith("/api/admin/traces")) return json({ schema_version: 1, data: {
      items: [{
        summary: {
          trace_id: "trace-source", workload_kind: "source_processing", workload_id: "job-source/attempt-2",
          run_id: "", chat_id: "", notebook_id: "notebook-source", root_span_id: "root-source",
          agent_name: "nano-source-processor", started_at_unix_nano: 1700000000000000000,
          last_observed_unix_nano: 1700000001000000000, ended_at_unix_nano: 1700000001000000000,
          duration_nanoseconds: 1000000000, status: "ok", active: false, models: [], input_tokens: null,
          output_tokens: null, total_tokens: null, cost: { known: false, amount: null, currency: "", source: "" }, attempt_count: 0
        },
        committed_sequence: 2, projected_sequence: 2, projection_lagged: false
      }]
    }});
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  expect(await screen.findByText("job-source/attempt-2")).toBeInTheDocument();
  expect(screen.getByText("Source processing")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Open Trace job-source/attempt-2" })).toBeInTheDocument();
});

test("renders Trace Analytics with partial failure, stale coverage, and Explorer drilldown", async () => {
  window.history.pushState(null, "", "/admin/traces/analytics?range=24h&agent=agent-a");
  const freshThrough = new Date(Date.now() - 10_000).toISOString();
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({
      user: { id: "usr_analytics", email: "analytics@example.com" },
      platform_capabilities: ["platform.trace.analytics"]
    });
    if (url.startsWith("/api/admin/trace-analytics/overview")) return json({
      schema_version: 1, generated_at: new Date().toISOString(), fresh_through: freshThrough,
      filters: { started_after: "2026-08-16T00:00:00Z", started_before: "2026-08-17T00:00:00Z", workload_kind: "agent_run", agent: "agent-a" },
      bucket: "1h", coverage: { total_samples: 10, token_samples: 6, cost_samples: 0 },
      data: { run_count: 10, completed_count: 8, success_rate: 0.75, error_rate: 0.25, retry_rate: 0.125, p95_duration_nanoseconds: 2_000_000_000, input_tokens: 100, output_tokens: 50, total_tokens: 150, costs: [] }
    });
    if (url.startsWith("/api/admin/trace-analytics/timeseries")) return json({ error: { code: "trace_analytics_temporarily_unavailable" } }, 503);
    if (url.includes("/api/admin/trace-analytics/latency")) return json({ schema_version: 1, fresh_through: freshThrough, coverage: { total_samples: 8 }, data: [{ value: "agent-a", sample_count: 8, p50_duration_nanoseconds: 1_000_000_000, p95_duration_nanoseconds: 2_000_000_000, p99_duration_nanoseconds: 3_000_000_000 }] });
    if (url.includes("/api/admin/trace-analytics/breakdowns")) return json({ schema_version: 1, fresh_through: freshThrough, coverage: { total_samples: 10 }, data: [{ value: "agent-a", run_count: 10, completed_count: 8, success_rate: 0.75, error_rate: 0.25, retry_rate: 0.125, p95_duration_nanoseconds: 2_000_000_000 }] });
    if (url.includes("/api/admin/trace-analytics/tools")) return json({ schema_version: 1, fresh_through: freshThrough, coverage: { total_samples: 0 }, data: [] });
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  expect(await screen.findByRole("heading", { name: "Trace Analytics" })).toBeInTheDocument();
  expect(await screen.findByText("Analytics data may be delayed")).toBeInTheDocument();
  expect(screen.getByText("Token coverage 60%")).toBeInTheDocument();
  expect(screen.getByText("Cost unavailable · 0% coverage")).toBeInTheDocument();
  expect(screen.getByText("Trend is temporarily unavailable.")).toBeInTheDocument();
  expect(screen.getByText("No tool calls in this range.")).toBeInTheDocument();
	expect(screen.getByLabelText("Behavior dimension")).toHaveValue("provider");
	expect(screen.getByRole("option", { name: "RAG degradation" })).toBeInTheDocument();

	await userEvent.setup().click(screen.getAllByRole("button", { name: "Open Traces for agent-a" })[0]);
  expect(window.location.pathname).toBe("/admin/traces");
  expect(window.location.search).toContain("range=24h");
  expect(window.location.search).toContain("agent=agent-a");
});

test("keeps Analytics hidden from a Trace-read-only operator", async () => {
	window.history.pushState(null, "", "/admin/traces/analytics");
	fetchHandler = async (input) => {
		const url = String(input);
		if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_reader", email: "reader@example.com" }, platform_capabilities: ["platform.trace.read"] });
		return json({ error: { code: "not_found" } }, 404);
	};
	render(<App />);
	expect(await screen.findByRole("heading", { name: "Analytics access restricted" })).toBeInTheDocument();
	expect(fetch).not.toHaveBeenCalledWith(expect.stringContaining("/api/admin/trace-analytics"), expect.anything());
});

test("renders a real Trace Detail when empty repeated fields arrive as null", async () => {
  window.history.pushState(null, "", "/admin/traces/trace-null-collections");
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({
      user: { id: "usr_operator", email: "operator@example.com" },
      platform_capabilities: ["platform.trace.read", "platform.trace.replay"]
    });
    if (url === "/api/admin/traces/trace-null-collections") return json({ schema_version: 1, data: {
      committed_sequence: 2, projected_sequence: 2,
      projection: {
        summary: {
          trace_id: "trace-null-collections", run_id: "run-null-collections", chat_id: "chat-null-collections", notebook_id: "notebook-null-collections",
          root_span_id: "root-null-collections", agent_name: "nano-research-agent", started_at_unix_nano: 1700000000000000000,
          last_observed_unix_nano: 1700000001000000000, ended_at_unix_nano: 1700000001000000000, duration_nanoseconds: 1000000000,
          status: "ok", active: false, models: [], input_tokens: null, output_tokens: null,
          total_tokens: null, cost: { known: false, amount: null, currency: "", source: "" }, attempt_count: 0
        },
        spans: [{
          trace_id: "trace-null-collections", span_id: "root-null-collections", parent_span_id: "", name: "agent.execution",
          start_sequence: 1, end_sequence: 2, started_at_unix_nano: 1700000000000000000, ended_at_unix_nano: 1700000001000000000,
          duration_nanoseconds: 1000000000, status: "ok", start_attributes: null, end_attributes: null, replay: null, model: null
        }],
        events: null,
        links: null
      }
    }});
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  const tree = await screen.findByRole("tree", { name: "Trace Tree" });
  expect(within(tree).getByText("agent.execution")).toBeInTheDocument();
  expect(screen.getByRole("region", { name: "Trace Timeline" })).toBeInTheDocument();
  await user.click(screen.getByRole("tab", { name: "Attributes" }));
  expect(within(screen.getByRole("region", { name: "Inspector" })).getByText("Unknown")).toBeInTheDocument();
  await user.click(screen.getByRole("tab", { name: "Replay" }));
  expect(screen.getByText("This Span has no Replay payload.")).toBeInTheDocument();
});

test("distinguishes expired Replay from a transient unavailable response", async () => {
  window.history.pushState(null, "", "/admin/traces/trace-expired?span=model-expired");
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({
      user: { id: "usr_operator", email: "operator@example.com" },
      platform_capabilities: ["platform.trace.read", "platform.trace.replay"]
    });
    if (url === "/api/admin/traces/trace-expired") return json({ schema_version: 1, data: replayTraceDetail("trace-expired", "model-expired", "replay-expired") });
    if (url.includes("/replay/replay-expired")) return json({ error: { code: "replay_expired" } }, 410);
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("tab", { name: "Replay" }));
  await user.click(screen.getByRole("button", { name: "Load sensitive Replay" }));

  expect(await screen.findByText("Replay has expired.")).toBeInTheDocument();
  expect(screen.queryByText("Replay is unavailable.")).not.toBeInTheDocument();
  expect(screen.getAllByText("Unknown cost").length).toBeGreaterThan(0);
});

test("keeps Replay forbidden when Trace read is granted without Replay capability", async () => {
  window.history.pushState(null, "", "/admin/traces/trace-forbidden?span=model-forbidden");
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({
      user: { id: "usr_operator", email: "operator@example.com" },
      platform_capabilities: ["platform.trace.read"]
    });
    if (url === "/api/admin/traces/trace-forbidden") return json({ schema_version: 1, data: replayTraceDetail("trace-forbidden", "model-forbidden", "replay-forbidden") });
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("tab", { name: "Replay" }));

  expect(screen.getByText("Replay capability is not granted.")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Load sensitive Replay" })).not.toBeInTheDocument();
  expect(fetch).not.toHaveBeenCalledWith(expect.stringContaining("/replay/"), expect.anything());
});

test("renders unavailable Replay as retryable without hiding Trace metadata", async () => {
  window.history.pushState(null, "", "/admin/traces/trace-unavailable?span=model-unavailable");
  fetchHandler = async (input) => {
    const url = String(input);
    if (url.endsWith("/api/v1/session")) return json({
      user: { id: "usr_operator", email: "operator@example.com" },
      platform_capabilities: ["platform.trace.read", "platform.trace.replay"]
    });
    if (url === "/api/admin/traces/trace-unavailable") return json({ schema_version: 1, data: replayTraceDetail("trace-unavailable", "model-unavailable", "replay-unavailable") });
    if (url.includes("/replay/replay-unavailable")) return json({ error: { code: "replay_unavailable" } }, 503);
    return json({ error: { code: "not_found" } }, 404);
  };

  render(<App />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("tab", { name: "Replay" }));
  await user.click(screen.getByRole("button", { name: "Load sensitive Replay" }));

  expect(await screen.findByText("Replay is unavailable.")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  expect(screen.getByText("trace-unavailable")).toBeInTheDocument();
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

function replayTraceDetail(traceID: string, spanID: string, replayID: string) {
  return {
    committed_sequence: 2,
    projected_sequence: 2,
    projection: {
      summary: {
        trace_id: traceID, run_id: `run-${traceID}`, chat_id: `chat-${traceID}`, notebook_id: `notebook-${traceID}`,
        root_span_id: spanID, agent_name: "nano-research-agent", started_at_unix_nano: 1700000000000000000,
        last_observed_unix_nano: 1700000001000000000, ended_at_unix_nano: 1700000001000000000,
        duration_nanoseconds: 1000000000, status: "ok", active: false, models: ["qwen-flash"], input_tokens: 4,
        output_tokens: 8, total_tokens: 12, cost: { known: false, amount: null, currency: "", source: "" }, attempt_count: 1
      },
      spans: [{
        trace_id: traceID, span_id: spanID, parent_span_id: "", name: "gen_ai.model.call",
        start_sequence: 1, end_sequence: 2, started_at_unix_nano: 1700000000000000000,
        ended_at_unix_nano: 1700000001000000000, duration_nanoseconds: 1000000000, status: "ok",
        start_attributes: [], end_attributes: [], replay: [{ attachment_id: replayID, class: "model_response", record_sequence: 2 }], model: null
      }],
      events: [], links: []
    }
  };
}

function linkedTraceDetail() {
  return {
    committed_sequence: 3,
    projected_sequence: 3,
    projection: {
      summary: {
        trace_id: "trace-previous", run_id: "run-previous", chat_id: "chat-previous", notebook_id: "notebook-previous",
        root_span_id: "root-previous", agent_name: "nano-research-agent", started_at_unix_nano: 1699999990000000000,
        last_observed_unix_nano: 1699999992000000000, ended_at_unix_nano: 1699999992000000000,
        duration_nanoseconds: 2000000000, status: "ok", active: false, models: [], input_tokens: null,
        output_tokens: null, total_tokens: null, cost: { known: false, amount: null, currency: "", source: "" }, attempt_count: 1
      },
      spans: [
        { trace_id: "trace-previous", span_id: "root-previous", parent_span_id: "", name: "agent.execution", start_sequence: 1, end_sequence: 3, started_at_unix_nano: 1699999990000000000, ended_at_unix_nano: 1699999992000000000, duration_nanoseconds: 2000000000, status: "ok", start_attributes: [], end_attributes: [], replay: [], model: null },
        { trace_id: "trace-previous", span_id: "child-previous", parent_span_id: "root-previous", name: "agent.action", start_sequence: 2, end_sequence: 3, started_at_unix_nano: 1699999990500000000, ended_at_unix_nano: 1699999991500000000, duration_nanoseconds: 1000000000, status: "ok", start_attributes: [], end_attributes: [], replay: [], model: null }
      ],
      events: [], links: []
    }
  };
}

function authenticatedWorkspaceHandler() {
  return async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [] });
    if (url.endsWith("/api/v1/notebooks/nb_test/studio-outputs") && method === "GET") return json({ outputs: [] });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [], citations: [] });
    if (url.endsWith("/api/v1/auth/sign-out") && method === "POST") return new Response(null, { status: 204 });
    return json({ error: { code: "not_found" } }, 404);
  };
}

function markdownWorkspaceHandler(messages: Array<{ id: string; role: "user" | "assistant"; content: string; created_at: string }>) {
  return async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [] });
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({
      chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" },
      messages,
      runs: [],
      citations: []
    });
    return json({ error: { code: "not_found" } }, 404);
  };
}

function json(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" }
  });
}
