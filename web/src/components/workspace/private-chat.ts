import type { AppendMessage } from "@assistant-ui/react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
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
  answer_mode: "model_knowledge" | null;
  created_at: string;
};

type AgentRun = {
  id: string;
  status: "queued" | "running" | "completed" | "failed";
  error_code?: string | null;
};

type ChatSnapshot = {
  chat: Chat;
  messages: ChatMessage[];
  active_run: AgentRun | null;
};

export type ChatController = {
  snapshot: ChatSnapshot | undefined;
  isLoading: boolean;
  error: string | null;
  send: (message: AppendMessage) => Promise<boolean>;
};

export function usePrivateChat(notebookID: string, copy: ChatPanelCopy): ChatController {
  const queryClient = useQueryClient();
  const [bootstrapKey] = useState(() => crypto.randomUUID());
  const [command, setCommand] = useState<{ id: string; content: string } | null>(null);
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
      return (await snapshotResponse.json()) as ChatSnapshot;
    },
    retry: false
  });

  const run = snapshotQuery.data?.active_run;
  const activeRunID = run?.status === "queued" || run?.status === "running" ? run.id : null;
  useEffect(() => {
    if (!activeRunID) return;

    const source = new EventSource(`/api/v1/agent-runs/${activeRunID}/events`);
    const onRun = (event: Event) => {
      let projection: { run: AgentRun; message: ChatMessage | null };
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
        return { ...current, messages, active_run: projection.run };
      });
      if (projection.run.status === "failed") setError(copy.failedLabel);
      if (projection.run.status === "completed" || projection.run.status === "failed") source.close();
    };
    source.addEventListener("run", onRun);
    return () => {
      source.removeEventListener("run", onRun);
      source.close();
    };
  }, [activeRunID, copy.failedLabel, queryClient, queryKey]);

  async function send(message: AppendMessage) {
    const content = message.role === "user" ? appendMessageText(message).trim() : "";
    const snapshot = snapshotQuery.data;
    if (!snapshot || !content) return false;

    const pending = command?.content === content ? command : { id: crypto.randomUUID(), content };
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
        answer_mode: null,
        created_at: new Date().toISOString()
      };
      return {
        ...current,
        messages: upsertMessage(current.messages, userMessage),
        active_run: { id: admitted.run_id, status: admitted.status }
      };
    });
    if (admitted.status === "completed" || admitted.status === "failed") {
      await snapshotQuery.refetch();
    }
    return true;
  }

  return {
    snapshot: snapshotQuery.data,
    isLoading: snapshotQuery.isLoading,
    error: error ?? (snapshotQuery.isError ? copy.unavailableLabel : null),
    send
  };
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
