import type { AppendMessage } from "@assistant-ui/react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import type { ChatPanelCopy } from "./chat-placeholder-panel";

type Chat = {
  id: string;
  notebook_id: string;
  title: string;
};

export type ChatMessage = {
  id: string;
  chat_id?: string;
  role: "user" | "assistant";
  content: string;
  created_at: string;
};

export type AgentRun = {
  id: string;
  input_message_id: string;
  status: "queued" | "running" | "completed" | "failed" | "cancelled";
  error_code?: string | null;
};

export type Citation = {
  id: string;
  message_id: string;
  reference_kind?: "precise" | "source";
  reference_ordinal?: number;
  claim_ordinal?: number;
  citation_ordinal?: number;
  claim_text?: string;
  source_id: string;
  source_title?: string;
  evidence_revision_id?: string;
  unit_id?: string;
  start_rune?: number;
  end_rune?: number;
};

export type ChatSnapshot = {
  chat: Chat;
  messages: ChatMessage[];
  runs: AgentRun[];
  citations: Citation[];
};

export type ChatController = {
  snapshot: ChatSnapshot | undefined;
  isLoading: boolean;
  error: string | null;
  send: (message: AppendMessage) => Promise<boolean>;
  stop: (runID: string) => Promise<boolean>;
  retry: (runID: string) => Promise<boolean>;
};

export function usePrivateChat(notebookID: string, copy: ChatPanelCopy, selectedSourceIDs: string[] = []): ChatController {
  const queryClient = useQueryClient();
  const [bootstrapKey] = useState(() => crypto.randomUUID());
  const [command, setCommand] = useState<{ id: string; content: string; time_zone: string; source_ids: string[] } | null>(null);
  const retryCommand = useRef<{ sourceRunID: string; key: string; timeZone: string } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const queryKey = useMemo(() => ["private-chat", notebookID] as const, [notebookID]);
  const snapshotQuery = useQuery({
    queryKey,
    queryFn: async (): Promise<ChatSnapshot> => {
      const listResponse = await api(`/api/v1/notebooks/${notebookID}/chats`);
      if (!listResponse.ok) throw new Error(copy.unavailableLabel);
      const listed = (await listResponse.json()) as { chats: Chat[] };
      let selected = listed.chats[0];
      if (!selected) {
        const createResponse = await api(`/api/v1/notebooks/${notebookID}/chats`, {
          method: "POST",
          headers: { "Idempotency-Key": bootstrapKey, "X-CSRF-Token": csrfToken() }
        });
        if (!createResponse.ok) throw new Error(copy.unavailableLabel);
        selected = ((await createResponse.json()) as { chat: Chat }).chat;
      }
      const snapshotResponse = await api(`/api/v1/chats/${selected.id}`);
      if (!snapshotResponse.ok) throw new Error(copy.unavailableLabel);
      const snapshot = (await snapshotResponse.json()) as ChatSnapshot;
      return { ...snapshot, citations: snapshot.citations ?? [] };
    },
    retry: false
  });

  const run = snapshotQuery.data?.runs.find((item) => item.status === "queued" || item.status === "running");
  const activeRunID = run?.status === "queued" || run?.status === "running" ? run.id : null;
  useEffect(() => {
    if (!activeRunID) return;

    const source = new EventSource(`/api/v1/agent-runs/${activeRunID}/events`);
    const onRun = (event: Event) => {
      let projection: { run: AgentRun; message: ChatMessage | null; citations?: Citation[] };
      try {
        projection = JSON.parse((event as MessageEvent<string>).data) as typeof projection;
      } catch {
        return;
      }
      queryClient.setQueryData<ChatSnapshot>(queryKey, (current) => {
        if (!current || projection.run.id !== activeRunID) return current;
        const messages = projection.message
          ? upsertMessage(current.messages, projection.message)
          : current.messages;
        return { ...current, messages, runs: upsertRun(current.runs, projection.run), citations: upsertCitations(current.citations, projection.citations ?? []) };
      });
      if (projection.run.status === "completed" || projection.run.status === "failed" || projection.run.status === "cancelled") source.close();
    };
    source.addEventListener("run", onRun);
    return () => {
      source.removeEventListener("run", onRun);
      source.close();
    };
  }, [activeRunID, queryClient, queryKey]);

  async function send(message: AppendMessage) {
    const content = message.role === "user" ? appendMessageText(message).trim() : "";
    const snapshot = snapshotQuery.data;
    if (!snapshot || !content) return false;

    const pending = command?.content === content
      ? command
      : { id: crypto.randomUUID(), content, time_zone: browserTimeZone(), source_ids: [...selectedSourceIDs] };
    setCommand(pending);
    setError(null);
    const response = await api(`/api/v1/chats/${snapshot.chat.id}/messages`, {
      method: "POST",
      headers: { "X-CSRF-Token": csrfToken() },
      body: JSON.stringify(pending)
    });
    if (!response.ok) {
      setError(await safeAdmissionError(response, copy));
      return false;
    }
    const admitted = (await response.json()) as { message_id: string; run_id: string; status: AgentRun["status"] };
    setCommand(null);
    queryClient.setQueryData<ChatSnapshot>(queryKey, (current) => {
      if (!current) return current;
      const userMessage: ChatMessage = {
        id: admitted.message_id,
        chat_id: current.chat.id,
        role: "user",
        content,
        created_at: new Date().toISOString()
      };
      return {
        ...current,
        messages: upsertMessage(current.messages, userMessage),
        runs: upsertRun(current.runs, { id: admitted.run_id, input_message_id: admitted.message_id, status: admitted.status })
      };
    });
    if (admitted.status === "completed" || admitted.status === "failed" || admitted.status === "cancelled") {
      await snapshotQuery.refetch();
    }
    return true;
  }

  async function stop(runID: string) {
    setError(null);
    const response = await api(`/api/v1/agent-runs/${runID}/cancel`, {
      method: "POST",
      headers: { "X-CSRF-Token": csrfToken() }
    });
    if (!response.ok) {
      if (response.status === 409) await snapshotQuery.refetch();
      else setError(copy.unavailableLabel);
      return false;
    }
    const body = (await response.json()) as { run: AgentRun };
    queryClient.setQueryData<ChatSnapshot>(queryKey, (current) => current
      ? { ...current, runs: upsertRun(current.runs, body.run) }
      : current);
    return true;
  }

  async function retry(runID: string) {
    const pending = retryCommand.current?.sourceRunID === runID
      ? retryCommand.current
      : { sourceRunID: runID, key: crypto.randomUUID(), timeZone: browserTimeZone() };
    retryCommand.current = pending;
    setError(null);
    const response = await api(`/api/v1/agent-runs/${runID}/retry`, {
      method: "POST",
      headers: { "Idempotency-Key": pending.key, "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ time_zone: pending.timeZone })
    });
    if (!response.ok) {
      setError(copy.unavailableLabel);
      return false;
    }
    const body = (await response.json()) as { run: AgentRun };
    retryCommand.current = null;
    queryClient.setQueryData<ChatSnapshot>(queryKey, (current) => current
      ? { ...current, runs: upsertRun(current.runs, body.run) }
      : current);
    return true;
  }

  return {
    snapshot: snapshotQuery.data,
    isLoading: snapshotQuery.isLoading,
    error: error ?? (snapshotQuery.isError ? copy.unavailableLabel : null),
    send,
    stop,
    retry
  };
}

export function browserTimeZone() {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone?.trim() || "UTC";
  } catch {
    return "UTC";
  }
}

export function appendMessageText(message: AppendMessage) {
  return message.role === "user"
    ? message.content.filter((part) => part.type === "text").map((part) => part.text).join("")
    : "";
}

function upsertMessage(messages: ChatMessage[], message: ChatMessage) {
  const existing = messages.findIndex((item) => item.id === message.id);
  if (existing < 0) return [...messages, message];
  return messages.map((item, index) => index === existing ? message : item);
}

function upsertRun(runs: AgentRun[], run: AgentRun) {
  const existing = runs.findIndex((item) => item.id === run.id || item.input_message_id === run.input_message_id);
  if (existing < 0) return [...runs, run];
  return runs.map((item, index) => index === existing ? run : item);
}

function upsertCitations(current: Citation[], additions: Citation[]) {
  const result = new Map(current.map((citation) => [citation.id, citation]));
  for (const citation of additions) result.set(citation.id, citation);
  return [...result.values()].sort((left, right) =>
    (left.reference_ordinal ?? left.claim_ordinal ?? 0) - (right.reference_ordinal ?? right.claim_ordinal ?? 0) ||
    (left.citation_ordinal ?? 0) - (right.citation_ordinal ?? 0));
}

async function safeAdmissionError(response: Response, copy: ChatPanelCopy) {
  try {
    const payload = (await response.json()) as { error?: { code?: string } };
    if (payload.error?.code === "active_run_conflict") return copy.waitingLabel;
  } catch {
    // The safe localized fallback below is enough for an unreadable response.
  }
  return copy.unavailableLabel;
}

async function api(path: string, init: RequestInit = {}) {
  const headers = new Headers(init.headers);
  if (init.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  return fetch(path, { credentials: "include", ...init, headers });
}

function csrfToken() {
  return document.cookie
    .split(";")
    .map((part) => part.trim())
    .find((part) => part.startsWith("nn_csrf="))
    ?.slice("nn_csrf=".length) ?? "";
}
