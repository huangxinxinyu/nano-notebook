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
  discovery_session_id?: string;
  started_at?: string;
  finished_at?: string;
  activities?: AgentActivity[];
};

export type AgentActivity = {
  kind: "searching_sources" | "discovering_sources" | "inspecting_source" | "reading_pdf" | "reading_webpage" | "saving_source" | "calculating" | "organizing_steps" | "working";
  detail?: string;
  started_at: string;
};

export type ChatMode = "chat" | "research";

export type ResearchPlan = {
  title: string;
  objective: string;
  scope: string;
  research_questions: string[];
  investigation_tracks: string[];
  source_strategy: string[];
  analysis_method: string[];
  deliverable_outline: string[];
  completion_criteria: string[];
  clarifying_questions: string[];
};

export type ResearchSessionSummary = {
  id: string;
  input_message_id: string;
  status: "planning" | "awaiting_input" | "awaiting_confirmation" | "queued" | "running" | "publishing" | "completed" | "failed" | "cancelled";
  planning_run_id?: string;
  accepted_plan_version?: number;
  execution_run_id?: string;
  current_report_version?: number;
  error_code?: string;
};

export type PlanningQuestion = {
  id: string;
  header?: string;
  question: string;
  options: { label: string; description?: string }[];
  recommended: number;
};

export type PlanningAnswer = { id: string; choice?: string; text?: string };

export type ResearchSessionSnapshot = {
  session: ResearchSessionSummary & { chat_id: string };
  plan?: { version: number; content: ResearchPlan };
  report?: { version: number; content_markdown: string };
  evidence: { discovered: number; read: number; failed: number };
  pending_questions?: { action_id: string; questions: PlanningQuestion[] };
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
  source_ids: string[];
  research_sessions: ResearchSessionSummary[];
};

export type ChatController = {
  snapshot: ChatSnapshot | undefined;
  isLoading: boolean;
  error: string | null;
  mode: ChatMode;
  setMode: (mode: ChatMode) => void;
  research: ResearchSessionSnapshot | undefined;
  isResearchLoading: boolean;
  send: (message: AppendMessage) => Promise<boolean>;
  editResearchPlan: (plan: ResearchPlan) => Promise<boolean>;
  startResearch: (planVersion: number) => Promise<boolean>;
  answerResearchQuestions: (actionID: string, answers: PlanningAnswer[], useRecommended: boolean) => Promise<boolean>;
  reviseResearchPlan: (content: string) => Promise<boolean>;
  stop: (runID: string) => Promise<boolean>;
  retry: (runID: string) => Promise<boolean>;
};

export function usePrivateChat(notebookID: string, copy: ChatPanelCopy): ChatController {
  const queryClient = useQueryClient();
  const [bootstrapKey] = useState(() => crypto.randomUUID());
  const [mode, setMode] = useState<ChatMode>("chat");
  const [selectedResearchSessionID, setSelectedResearchSessionID] = useState<string | null>(null);
  const [command, setCommand] = useState<{ id: string; content: string; time_zone: string; mode: ChatMode } | null>(null);
  const retryCommand = useRef<{ sourceRunID: string; key: string; timeZone: string } | null>(null);
  const completedResearchRefresh = useRef<string | null>(null);
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
      return { ...snapshot, citations: snapshot.citations ?? [], source_ids: snapshot.source_ids ?? [], research_sessions: snapshot.research_sessions ?? [] };
    },
    retry: false
  });

  const restoredResearchSessionID = selectedResearchSessionID ?? snapshotQuery.data?.research_sessions.at(-1)?.id ?? null;
  const researchQuery = useQuery({
    queryKey: ["research-session", restoredResearchSessionID],
    enabled: Boolean(restoredResearchSessionID),
    queryFn: async (): Promise<ResearchSessionSnapshot> => {
      const response = await api(`/api/v1/research-sessions/${restoredResearchSessionID}`);
      if (!response.ok) throw new Error(copy.unavailableLabel);
      return response.json() as Promise<ResearchSessionSnapshot>;
    },
    refetchInterval: (query) => {
      const status = (query.state.data as ResearchSessionSnapshot | undefined)?.session.status;
      return status === "planning" || status === "queued" || status === "running" || status === "publishing" ? 1000 : false;
    },
    retry: false
  });

  useEffect(() => {
    const research = researchQuery.data;
    if (!research) return;
    queryClient.setQueryData<ChatSnapshot>(queryKey, (current) => current ? {
      ...current,
      research_sessions: upsertResearchSession(current.research_sessions, research.session)
    } : current);
    if (research.session.status === "completed" && completedResearchRefresh.current !== research.session.id) {
      completedResearchRefresh.current = research.session.id;
      void queryClient.invalidateQueries({ queryKey });
    }
  }, [queryClient, queryKey, researchQuery.data]);

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

    const pending = command?.content === content && command.mode === mode
      ? command
      : { id: crypto.randomUUID(), content, time_zone: browserTimeZone(), mode };
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
    const admitted = (await response.json()) as { message_id: string; mode?: ChatMode; research_session_id?: string; run_id: string; status: AgentRun["status"] | "planning" };
    setCommand(null);
    if (admitted.research_session_id) setSelectedResearchSessionID(admitted.research_session_id);
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
        runs: upsertRun(current.runs, { id: admitted.run_id, input_message_id: admitted.message_id, status: admitted.status === "planning" ? "queued" : admitted.status }),
        research_sessions: admitted.research_session_id ? upsertResearchSession(current.research_sessions, {
          id: admitted.research_session_id, input_message_id: admitted.message_id, status: "planning", planning_run_id: admitted.run_id
        }) : current.research_sessions
      };
    });
    if (admitted.status === "completed" || admitted.status === "failed" || admitted.status === "cancelled") {
      await snapshotQuery.refetch();
    }
    return true;
  }

  async function editResearchPlan(plan: ResearchPlan) {
    const research = researchQuery.data;
    if (!research || research.session.status !== "awaiting_confirmation") return false;
    setError(null);
    const response = await api(`/api/v1/research-sessions/${research.session.id}/plan`, {
      method: "PATCH",
      headers: { "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ plan })
    });
    if (!response.ok) {
      setError(copy.unavailableLabel);
      return false;
    }
    await researchQuery.refetch();
    return true;
  }

  async function startResearch(planVersion: number) {
    const research = researchQuery.data;
    if (!research || research.session.status !== "awaiting_confirmation") return false;
    setError(null);
    const response = await api(`/api/v1/research-sessions/${research.session.id}/start`, {
      method: "POST",
      headers: { "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ plan_version: planVersion, time_zone: browserTimeZone() })
    });
    if (!response.ok) {
      setError(copy.unavailableLabel);
      return false;
    }
    const started = (await response.json()) as { run_id: string; status: "queued" };
    queryClient.setQueryData<ChatSnapshot>(queryKey, (current) => current ? {
      ...current,
      runs: upsertRun(current.runs, { id: started.run_id, input_message_id: research.session.input_message_id, status: started.status }),
      research_sessions: upsertResearchSession(current.research_sessions, {
        ...research.session, status: "queued", accepted_plan_version: planVersion, execution_run_id: started.run_id
      })
    } : current);
    await researchQuery.refetch();
    return true;
  }

  async function answerResearchQuestions(actionID: string, answers: PlanningAnswer[], useRecommended: boolean) {
    const research = researchQuery.data;
    if (!research || research.session.status !== "awaiting_input") return false;
    setError(null);
    const response = await api(`/api/v1/research-sessions/${research.session.id}/answers`, {
      method: "POST",
      headers: { "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ action_id: actionID, answers, use_recommended: useRecommended })
    });
    if (!response.ok) {
      if (response.status === 409) await researchQuery.refetch();
      else setError(copy.unavailableLabel);
      return false;
    }
    await researchQuery.refetch();
    return true;
  }

  async function reviseResearchPlan(content: string) {
    const research = researchQuery.data;
    if (!research || research.session.status !== "awaiting_confirmation" || !content.trim()) return false;
    setError(null);
    const response = await api(`/api/v1/research-sessions/${research.session.id}/revisions`, {
      method: "POST",
      headers: { "X-CSRF-Token": csrfToken(), "Idempotency-Key": crypto.randomUUID() },
      body: JSON.stringify({ id: crypto.randomUUID(), content: content.trim(), time_zone: browserTimeZone() })
    });
    if (!response.ok) {
      setError(copy.unavailableLabel);
      return false;
    }
    await Promise.all([snapshotQuery.refetch(), researchQuery.refetch()]);
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
    const research = researchQuery.data;
    if (research && (research.session.planning_run_id === runID || research.session.execution_run_id === runID)) {
      queryClient.setQueryData<ResearchSessionSnapshot>(["research-session", research.session.id], {
        ...research,
        session: { ...research.session, status: "cancelled" }
      });
    }
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
    mode,
    setMode,
    research: researchQuery.data,
    isResearchLoading: researchQuery.isLoading,
    send,
    editResearchPlan,
    startResearch,
    answerResearchQuestions,
    reviseResearchPlan,
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

function upsertResearchSession(sessions: ResearchSessionSummary[], session: ResearchSessionSummary) {
  const existing = sessions.findIndex((item) => item.id === session.id);
  if (existing < 0) return [...sessions, session];
  return sessions.map((item, index) => index === existing ? session : item);
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
