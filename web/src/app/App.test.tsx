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
  expect(within(sources).getByRole("button", { name: "Add sources" })).toBeInTheDocument();
  expect(within(sources).queryByText("Fast Research")).not.toBeInTheDocument();
  expect(fetch).toHaveBeenCalledWith("/api/v1/notebooks/nb_test/chats", expect.anything());
  expect(fetch).toHaveBeenCalledWith("/api/v1/chats/chat_test", expect.anything());
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
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [], citations: [] });
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

  await waitFor(() => expect(admittedBody?.source_ids).toEqual(["src_ready"]));
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
      { id: "src_failed", notebook_id: "nb_test", title: "broken.docx", format: "docx", byte_size: 4096, state: "failed", failure_reason: "content_unreadable" }
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

test("opens an immutable image Source with its Evidence region highlighted", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [
      { id: "src_image", notebook_id: "nb_test", title: "diagram.png", format: "png", byte_size: 1024, state: "ready" }
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
  const dialog = await screen.findByRole("dialog", { name: "diagram.png" });
  const image = within(dialog).getByRole("img", { name: "diagram.png" });
  expect(image).toHaveAttribute("src", "/api/v1/sources/src_image/viewer-asset");
  Object.defineProperties(image, { naturalWidth: { value: 100 }, naturalHeight: { value: 100 } });
  act(() => image.dispatchEvent(new Event("load")));
  expect(dialog.querySelector(".source-image-highlight")).toHaveStyle({ left: "10%", top: "20%", width: "30%", height: "40%" });
});

test("opens immutable rendered PDF pages without exposing the original file", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [
      { id: "src_pdf", notebook_id: "nb_test", title: "paper.pdf", format: "pdf", byte_size: 2048, state: "ready" }
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
  const dialog = await screen.findByRole("dialog", { name: "paper.pdf" });
  expect(within(dialog).getByRole("img", { name: "paper.pdf, page 1" })).toHaveAttribute("src", "/api/v1/sources/src_pdf/viewer-asset?ordinal=1");
  await user.click(within(dialog).getByRole("button", { name: "Next page" }));
  expect(within(dialog).getByRole("img", { name: "paper.pdf, page 2" })).toHaveAttribute("src", "/api/v1/sources/src_pdf/viewer-asset?ordinal=2");
  expect(within(dialog).queryByText(/original_object_key|download/i)).not.toBeInTheDocument();
});

test("opens a published Citation without exposing retrieval internals", async () => {
  window.history.pushState(null, "", "/notebooks/nb_test");
  fetchHandler = async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/api/v1/session")) return json({ user: { id: "usr_test", email: "learner@example.com" } });
    if (url.endsWith("/api/v1/notebooks/nb_test")) return json({ notebook: { id: "nb_test", title: "My Research Topic" } });
    if (url.endsWith("/api/v1/notebooks/nb_test/sources")) return json({ sources: [] });
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
  const citation = await within(chat).findByRole("button", { name: "Citation 1 for KV caching avoids recomputing prior keys and values." });
  act(() => citation.focus());
  expect(citation).toHaveFocus();
  await waitFor(() => expect(screen.getByRole("tooltip")).toHaveTextContent("transformer-notes.pdf"));
  act(() => citation.blur());
  await waitFor(() => expect(screen.queryByRole("tooltip")).not.toBeInTheDocument());
  await user.hover(citation);
  const tooltip = await screen.findByRole("tooltip");
  expect(within(tooltip).getByText("transformer-notes.pdf")).toBeInTheDocument();
  expect(within(tooltip).getByText("The cache stores keys and values from all prior token positions.")).toBeInTheDocument();
  expect(within(tooltip).getByText("Page 12")).toBeInTheDocument();
  expect(screen.queryByRole("dialog", { name: "transformer-notes.pdf" })).not.toBeInTheDocument();
  await user.click(citation);

  const dialog = await screen.findByRole("dialog", { name: "transformer-notes.pdf" });
  expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  expect(within(dialog).getByText("The cache stores keys and values from all prior token positions.")).toBeInTheDocument();
  expect(within(dialog).getByText("Page 12")).toBeInTheDocument();
  expect(within(dialog).queryByText(/rev_1|unit_1/)).not.toBeInTheDocument();
  await user.click(within(dialog).getByRole("button", { name: "Close" }));
  await user.click(within(chat).getByRole("button", { name: "Citation 2 for KV caching avoids recomputing prior keys and values." }));
  const imageDialog = await screen.findByRole("dialog", { name: "cache-diagram.png" });
  const image = within(imageDialog).getByRole("img", { name: "cache-diagram.png" });
  expect(image).toHaveAttribute("src", "/api/v1/sources/src_image/viewer-asset");
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
          { trace_id: "trace-admin", span_id: "verify-admin", parent_span_id: "root-admin", name: "nano.claim_support", start_sequence: 7, end_sequence: 8, started_at_unix_nano: 1700000003600000000, ended_at_unix_nano: 1700000003700000000, duration_nanoseconds: 100000000, status: "ok", start_attributes: [{ Key: "nano.rag.verifier.claim_count", Value: { Kind: "int64", Int64: 3 } }, { Key: "nano.rag.verifier.evidence_count", Value: { Kind: "int64", Int64: 5 } }], end_attributes: [{ Key: "nano.rag.verifier.supported_count", Value: { Kind: "int64", Int64: 2 } }, { Key: "nano.rag.verifier.unsupported_count", Value: { Kind: "int64", Int64: 1 } }], replay: [], model: null },
          { trace_id: "trace-admin", span_id: "publish-admin", parent_span_id: "root-admin", name: "nano.publication", start_sequence: 9, end_sequence: 10, started_at_unix_nano: 1700000003800000000, ended_at_unix_nano: 1700000003900000000, duration_nanoseconds: 100000000, status: "ok", start_attributes: [], end_attributes: [{ Key: "nano.rag.grounding.outcome", Value: { Kind: "string", String: "supported" } }], replay: [], model: null }
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
  expect(within(rag).getByText("2 supported / 1 unsupported")).toBeInTheDocument();
  expect(within(rag).getByText("supported")).toBeInTheDocument();
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
    if (url.endsWith("/api/v1/notebooks/nb_test/chats") && method === "GET") return json({ chats: [{ id: "chat_test", notebook_id: "nb_test", title: "New chat" }] });
    if (url.endsWith("/api/v1/chats/chat_test") && method === "GET") return json({ chat: { id: "chat_test", notebook_id: "nb_test", title: "New chat" }, messages: [], runs: [], citations: [] });
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
