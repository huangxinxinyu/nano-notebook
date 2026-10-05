import {
  AssistantRuntimeProvider,
  ComposerPrimitive,
  MessagePrimitive,
  ThreadPrimitive,
  useAuiState,
  useExternalStoreRuntime,
  type AssistantRuntime
} from "@assistant-ui/react";
import { MarkdownTextPrimitive } from "@assistant-ui/react-markdown";
import { useCallback, useEffect, useMemo, useRef, useState, type ComponentProps } from "react";
import remarkGfm from "remark-gfm";
import { MaterialSymbol } from "../icons/material-symbol";
import { Button } from "../ui/button";
import { appendMessageText, type AgentActivity, type AgentRun, type ChatController, type ChatMessage, type Citation, type PlanningAnswer, type PlanningQuestion, type ResearchPlan } from "./private-chat";
import { SourceOpenTarget } from "./source-open-target";
import type { MemberSource } from "./sources";

export type ChatPanelCopy = {
  title: string;
  emptyTitle: string;
  emptyBody: string;
  composerPlaceholder: string;
  composerLabel: string;
  sendLabel: string;
  waitingLabel: string;
  generatingLabel: string;
  agentWorkingLabel: string;
  agentWorkedLabel: string;
  analyzingLabel: string;
  searchingSourcesLabel: string;
  discoveringSourcesLabel: string;
  inspectingSourceLabel: string;
  readingPDFLabel: string;
  readingWebpageLabel: string;
  savingSourceLabel: string;
  calculatingLabel: string;
  organizingStepsLabel: string;
  sourceDisclosure: string;
  selectedSourceDisclosure: string;
  failedLabel: string;
  stoppedLabel: string;
  stopLabel: string;
  retryLabel: string;
  unavailableLabel: string;
  citationLabel: string;
  citationUnavailableLabel: string;
  citationPreviewLabel: string;
  closeLabel: string;
  sourcePreviewLabel: string;
  processingLabel: string;
  sourceUnavailableLabel: string;
  coverageWarningLabel: string;
  chatModeLabel: string;
  researchModeLabel: string;
  researchDisclosure: string;
  researchPlanningLabel: string;
  researchPlanTitle: string;
  researchPlanHelp: string;
  savePlanLabel: string;
  startResearchLabel: string;
  savingLabel: string;
  startingLabel: string;
  researchProgressLabel: string;
  discoveredLabel: string;
  readLabel: string;
  failedSourcesLabel: string;
  researchCompletedLabel: string;
  researchFailedLabel: string;
  planInvalidLabel: string;
  researchQuestionsTitle: string;
  researchQuestionsHelp: string;
  recommendedLabel: string;
  otherAnswerLabel: string;
  submitAnswersLabel: string;
  useRecommendedLabel: string;
  revisePlanLabel: string;
  revisePlanPlaceholder: string;
  submitRevisionLabel: string;
};

export function ChatPanelContent({ copy, controller, sources, onOpenSource, selectedSourceCount = 0 }: { copy: ChatPanelCopy; controller: ChatController; sources: MemberSource[]; onOpenSource: (source: MemberSource) => void; selectedSourceCount?: number }) {
  const messages = controller.snapshot?.messages ?? [];
  const runs = controller.snapshot?.runs ?? [];
  const run = runs.find((item) => item.status === "queued" || item.status === "running");
  const isRunning = run?.status === "queued" || run?.status === "running";
  const latestMessageID = messages.at(-1)?.id;
  const runtimeRef = useRef<AssistantRuntime | null>(null);
  const runtime = useExternalStoreRuntime<ChatMessage>({
    messages,
    isLoading: controller.isLoading,
    isDisabled: !controller.snapshot,
    isSendDisabled: isRunning,
    isRunning,
    onNew: async (message) => {
      if (!await controller.send(message)) {
        runtimeRef.current?.thread.composer.setText(appendMessageText(message));
      }
    },
    convertMessage: (message) => ({
      id: message.id,
      role: message.role,
      content: message.content,
      createdAt: new Date(message.created_at),
      ...(message.role === "assistant" ? { status: { type: "complete" as const, reason: "stop" as const } } : {})
    })
  });
  useEffect(() => {
    runtimeRef.current = runtime;
    return () => {
      runtimeRef.current = null;
    };
  }, [runtime]);

  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <div className="workspace-panel-content chat-content" data-chat-framework="@assistant-ui/react">
        <div className="workspace-panel-header">
          <h2>{copy.title}</h2>
          <MaterialSymbol name="more_vert" size={20} />
        </div>
        <p className="chat-source-disclosure">{controller.mode === "research" ? copy.researchDisclosure : selectedSourceCount > 0 ? copy.selectedSourceDisclosure.replace("{count}", String(selectedSourceCount)) : copy.sourceDisclosure}</p>
        <ThreadPrimitive.Root className="chat-thread">
          <ThreadPrimitive.Viewport className="chat-thread-viewport">
            <ThreadPrimitive.Empty>
              <div className="chat-empty-state">
                <span className="chat-empty-icon"><MaterialSymbol name="chat_bubble" size={27} /></span>
                <strong>{copy.emptyTitle}</strong>
                <p>{copy.emptyBody}</p>
              </div>
            </ThreadPrimitive.Empty>
            <div className="chat-message-list">
              <ThreadPrimitive.Messages components={{
                UserMessage: () => <UserMessage controller={controller} copy={copy} latestMessageID={latestMessageID} />,
                AssistantMessage: () => <AssistantMessage controller={controller} copy={copy} sources={sources} onOpenSource={onOpenSource} />
              }} />
            </div>
            <ResearchStatusCard copy={copy} controller={controller} />
            {run ? <AgentActivityCard run={run} copy={copy} onStop={() => void controller.stop(run.id)} /> : null}
            {controller.error ? <div className="chat-error" role="alert">{controller.error}</div> : null}
          </ThreadPrimitive.Viewport>
          <ComposerPrimitive.Root className="chat-composer">
            <div className="chat-mode-selector" aria-label={`${copy.chatModeLabel} / ${copy.researchModeLabel}`}>
              <button type="button" aria-pressed={controller.mode === "chat"} onClick={() => controller.setMode("chat")}>{copy.chatModeLabel}</button>
              <button type="button" aria-pressed={controller.mode === "research"} onClick={() => controller.setMode("research")}><MaterialSymbol name="travel_explore" size={17} />{copy.researchModeLabel}</button>
            </div>
            <ComposerPrimitive.Input className="chat-composer-input" aria-label={copy.composerLabel} placeholder={copy.composerPlaceholder} rows={1} />
            <ComposerPrimitive.Send className="chat-send" aria-label={copy.sendLabel}>
              <MaterialSymbol name="arrow_upward" size={22} />
            </ComposerPrimitive.Send>
          </ComposerPrimitive.Root>
        </ThreadPrimitive.Root>
      </div>
    </AssistantRuntimeProvider>
  );
}

function AgentActivityCard({ run, copy, onStop }: { run: AgentRun; copy: ChatPanelCopy; onStop: () => void }) {
  const now = useCurrentTime(run.status === "running");
  const duration = run.started_at ? formatDuration(run.started_at, undefined, now) : "";
  const activities = run.activities ?? [];
  return (
    <div className="chat-activity" role="status">
      <div className="chat-activity-content">
        <strong>{run.status === "queued" ? copy.waitingLabel : copy.agentWorkingLabel.replace("{time}", duration || "0s")}</strong>
        {run.status === "running" ? (
          <div className="chat-activity-list">
            {(activities.length ? activities : [{ kind: "working" as const, started_at: run.started_at ?? "" }]).map((activity, index) => (
              <div className="chat-activity-item" key={`${activity.started_at}:${index}`}>
                <span>{activityLabel(activity, copy)}</span>
                {activity.detail ? <small>{activity.detail}</small> : null}
              </div>
            ))}
          </div>
        ) : null}
      </div>
      <Button variant="ghost" size="sm" onClick={onStop}>{copy.stopLabel}</Button>
    </div>
  );
}

function activityLabel(activity: AgentActivity, copy: ChatPanelCopy) {
  const labels: Record<AgentActivity["kind"], string> = {
    searching_sources: copy.searchingSourcesLabel,
    discovering_sources: copy.discoveringSourcesLabel,
    inspecting_source: copy.inspectingSourceLabel,
    reading_pdf: copy.readingPDFLabel,
    reading_webpage: copy.readingWebpageLabel,
    saving_source: copy.savingSourceLabel,
    calculating: copy.calculatingLabel,
    organizing_steps: copy.organizingStepsLabel,
    working: copy.analyzingLabel
  };
  return labels[activity.kind] ?? copy.analyzingLabel;
}

function useCurrentTime(active: boolean) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [active]);
  return now;
}

function formatDuration(startedAt: string, finishedAt?: string, now = Date.now()) {
  const start = Date.parse(startedAt);
  const end = finishedAt ? Date.parse(finishedAt) : now;
  if (!Number.isFinite(start) || !Number.isFinite(end)) return "0s";
  const seconds = Math.max(0, Math.floor((end - start) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

function ResearchStatusCard({ copy, controller }: { copy: ChatPanelCopy; controller: ChatController }) {
  const research = controller.research;
  if (!research) return null;
  const { session, evidence, plan } = research;
  if (session.status === "cancelled") return null;
  if (session.status === "awaiting_input" && research.pending_questions) {
    return <ResearchQuestionsCard key={research.pending_questions.action_id} copy={copy} controller={controller} actionID={research.pending_questions.action_id} questions={research.pending_questions.questions} />;
  }
  if (session.status === "planning" || session.status === "awaiting_input") {
    return <section className="research-status-card" aria-label={copy.researchProgressLabel}><span className="research-pulse" />{copy.researchPlanningLabel}</section>;
  }
  if (session.status === "awaiting_confirmation" && plan) {
    return <ResearchPlanEditor key={`${session.id}:${plan.version}`} copy={copy} controller={controller} plan={plan.content} version={plan.version} />;
  }
  const terminal = session.status === "completed" ? copy.researchCompletedLabel : session.status === "failed" ? copy.researchFailedLabel : copy.researchProgressLabel;
  return (
    <section className="research-status-card research-status-card--metrics" aria-label={copy.researchProgressLabel}>
      <strong>{terminal}</strong>
      <span>{copy.discoveredLabel} <b>{evidence.discovered}</b></span>
      <span>{copy.readLabel} <b>{evidence.read}</b></span>
      <span>{copy.failedSourcesLabel} <b>{evidence.failed}</b></span>
    </section>
  );
}

function ResearchQuestionsCard({ copy, controller, actionID, questions }: { copy: ChatPanelCopy; controller: ChatController; actionID: string; questions: PlanningQuestion[] }) {
  const [choices, setChoices] = useState<Record<string, string>>(() => Object.fromEntries(questions.map((question) => [question.id, question.options[question.recommended]?.label ?? ""])));
  const [texts, setTexts] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const submit = async (useRecommended: boolean) => {
    setBusy(true);
    const answers: PlanningAnswer[] = useRecommended ? [] : questions.map((question) => {
      const text = texts[question.id]?.trim();
      return text ? { id: question.id, text } : { id: question.id, choice: choices[question.id] };
    });
    await controller.answerResearchQuestions(actionID, answers, useRecommended);
    setBusy(false);
  };
  return (
    <section className="research-plan-card research-questions-card" aria-label={copy.researchQuestionsTitle}>
      <div><span className="material-symbols-rounded" aria-hidden="true">help</span><div><h3>{copy.researchQuestionsTitle}</h3><p>{copy.researchQuestionsHelp}</p></div></div>
      {questions.map((question) => (
        <fieldset key={question.id} className="research-question">
          <legend>{question.header ? <span className="research-question-chip">{question.header}</span> : null}{question.question}</legend>
          {question.options.map((option, index) => (
            <label key={option.label} className="research-question-option">
              <input type="radio" name={`${actionID}:${question.id}`} checked={!texts[question.id]?.trim() && choices[question.id] === option.label}
                onChange={() => { setChoices((current) => ({ ...current, [question.id]: option.label })); setTexts((current) => ({ ...current, [question.id]: "" })); }} />
              <span><b>{option.label}</b>{index === question.recommended ? <em>{copy.recommendedLabel}</em> : null}{option.description ? <small>{option.description}</small> : null}</span>
            </label>
          ))}
          <input className="research-question-other" type="text" placeholder={copy.otherAnswerLabel} value={texts[question.id] ?? ""} maxLength={1000}
            onChange={(event) => setTexts((current) => ({ ...current, [question.id]: event.target.value }))} />
        </fieldset>
      ))}
      <div className="research-plan-actions">
        <Button variant="outline" disabled={busy} onClick={() => void submit(true)}>{copy.useRecommendedLabel}</Button>
        <Button disabled={busy} onClick={() => void submit(false)}>{copy.submitAnswersLabel}</Button>
      </div>
    </section>
  );
}

function ResearchPlanEditor({ copy, controller, plan, version }: { copy: ChatPanelCopy; controller: ChatController; plan: ResearchPlan; version: number }) {
  const [draft, setDraft] = useState(() => JSON.stringify(plan, null, 2));
  const [planError, setPlanError] = useState(false);
  const [busy, setBusy] = useState<"save" | "start" | "revise" | null>(null);
  const [revision, setRevision] = useState("");
  const revise = async () => {
    setBusy("revise");
    if (await controller.reviseResearchPlan(revision)) setRevision("");
    setBusy(null);
  };
  const save = async () => {
    let parsed: ResearchPlan;
    try {
      parsed = JSON.parse(draft) as ResearchPlan;
    } catch {
      setPlanError(true);
      return;
    }
    setPlanError(false);
    setBusy("save");
    if (!await controller.editResearchPlan(parsed)) setPlanError(true);
    setBusy(null);
  };
  const start = async () => {
    setBusy("start");
    await controller.startResearch(version);
    setBusy(null);
  };
  return (
    <section className="research-plan-card" aria-label={copy.researchPlanTitle}>
      <div><span className="material-symbols-rounded" aria-hidden="true">route</span><div><h3>{plan.title || copy.researchPlanTitle}</h3><p>{copy.researchPlanHelp}</p></div></div>
      <textarea aria-label={copy.researchPlanTitle} value={draft} onChange={(event) => setDraft(event.target.value)} rows={14} spellCheck={false} />
      {planError ? <p className="research-plan-error" role="alert">{copy.planInvalidLabel}</p> : null}
      <label className="research-plan-revision">
        <span>{copy.revisePlanLabel}</span>
        <textarea value={revision} onChange={(event) => setRevision(event.target.value)} rows={3} maxLength={8000} placeholder={copy.revisePlanPlaceholder} />
      </label>
      <div className="research-plan-actions">
        <Button variant="outline" disabled={busy !== null || !revision.trim()} onClick={() => void revise()}>{busy === "revise" ? copy.savingLabel : copy.submitRevisionLabel}</Button>
        <Button variant="outline" disabled={busy !== null} onClick={() => void save()}>{busy === "save" ? copy.savingLabel : copy.savePlanLabel}</Button>
        <Button disabled={busy !== null} onClick={() => void start()}>{busy === "start" ? copy.startingLabel : copy.startResearchLabel}</Button>
      </div>
    </section>
  );
}

function UserMessage({ controller, copy, latestMessageID }: { controller: ChatController; copy: ChatPanelCopy; latestMessageID?: string }) {
  const messageID = useAuiState((state) => state.message.id);
  const run = controller.snapshot?.runs.find((item) => item.input_message_id === messageID);
  const isResearch = controller.snapshot?.research_sessions.some((session) => session.input_message_id === messageID);
  const canRetry = !isResearch && messageID === latestMessageID && (run?.status === "failed" || run?.status === "cancelled");
  const duration = run?.started_at && run.finished_at ? formatDuration(run.started_at, run.finished_at) : "";
  return (
    <MessagePrimitive.Root className="chat-message chat-message--user">
      <MessagePrimitive.Parts />
      {run?.status === "failed" || run?.status === "cancelled" ? (
        <span className="chat-run-terminal">
          {run.status === "failed" ? copy.failedLabel : copy.stoppedLabel}
          {canRetry ? <Button variant="ghost" size="sm" onClick={() => void controller.retry(run.id)}>{copy.retryLabel}</Button> : null}
        </span>
      ) : null}
      {duration ? <span className="chat-run-duration">{copy.agentWorkedLabel.replace("{time}", duration)}</span> : null}
    </MessagePrimitive.Root>
  );
}

function AssistantMessage({ controller, copy, sources, onOpenSource }: { controller: ChatController; copy: ChatPanelCopy; sources: MemberSource[]; onOpenSource: (source: MemberSource) => void }) {
  const messageID = useAuiState((state) => state.message.id);
  const citations = controller.snapshot?.citations.filter((citation) => citation.message_id === messageID) ?? [];
  const sourceCitations = citations
    .filter((citation) => citation.reference_kind === "source")
    .sort((left, right) => (left.reference_ordinal ?? 0) - (right.reference_ordinal ?? 0));
  const preciseCitations = citations.filter((citation) => citation.reference_kind !== "source");
  return (
    <MessagePrimitive.Root className="chat-message chat-message--assistant">
      <MessagePrimitive.Parts>
        {({ part }) => part.type === "text" ? <AssistantMarkdownText citations={sourceCitations} copy={copy} sources={sources} onOpenSource={onOpenSource} /> : null}
      </MessagePrimitive.Parts>
      {preciseCitations.length ? <div className="chat-citations">{preciseCitations.map((citation, index) => <CitationButton key={citation.id} citation={citation} number={index + 1} copy={copy} source={sources.find((item) => item.id === citation.source_id)} onOpenSource={onOpenSource} />)}</div> : null}
    </MessagePrimitive.Root>
  );
}

function AssistantMarkdownText({ citations, copy, sources, onOpenSource }: { citations: Citation[]; copy: ChatPanelCopy; sources: MemberSource[]; onOpenSource: (source: MemberSource) => void }) {
  const bySource = useMemo(() => new Map(citations.map((citation) => [citation.source_id, citation])), [citations]);
  const citationTargets = useMemo(() => new Map(citations.map((citation) => [sourceCitationTarget(citation.source_id), citation])), [citations]);
  const preprocess = useCallback((text: string) => {
    return text.replace(/\[source:([A-Za-z0-9_.-]{1,255})\]/g, (marker, sourceID: string) => {
      const citation = bySource.get(sourceID);
      if (!citation) return marker;
      return `[${(citation.reference_ordinal ?? 0) + 1}](${sourceCitationTarget(sourceID)})`;
    });
  }, [bySource]);
  const components = useMemo<NonNullable<ComponentProps<typeof MarkdownTextPrimitive>["components"]>>(() => ({
    a: ({ href, children, title, className }) => {
      const citation = href ? citationTargets.get(href) : undefined;
      if (citation) return <InlineSourceCitationButton citation={citation} copy={copy} source={sources.find((item) => item.id === citation.source_id)} onOpenSource={onOpenSource} />;
      const external = href?.startsWith("https://") || href?.startsWith("http://");
      return <a href={href} title={title} className={className} {...(external ? { target: "_blank", rel: "noopener noreferrer" } : {})}>{children}</a>;
    }
  }), [citationTargets, copy, onOpenSource, sources]);
  return (
    <MarkdownTextPrimitive
      className="chat-markdown"
      components={components}
      preprocess={preprocess}
      remarkPlugins={[remarkGfm]}
      smooth={false}
    />
  );
}

function sourceCitationTarget(sourceID: string) {
  return `#source-citation-${encodeURIComponent(sourceID)}`;
}

function InlineSourceCitationButton({ citation, copy, source, onOpenSource }: { citation: Citation; copy: ChatPanelCopy; source?: MemberSource; onOpenSource: (source: MemberSource) => void }) {
  const number = (citation.reference_ordinal ?? 0) + 1;
  const sourceLabel = source?.title ?? citation.source_title ?? `${copy.citationLabel} ${number}`;
  return (
    <SourceOpenTarget source={source} className="nn-button nn-button--outline nn-button--size-sm citation-chip citation-chip--inline" ariaLabel={`${copy.citationLabel} ${number} for ${sourceLabel}`} onInlineOriginal={onOpenSource}>[{number}] {sourceLabel}</SourceOpenTarget>
  );
}

function CitationButton({ citation, number, copy, source, onOpenSource }: { citation: Citation; number: number; copy: ChatPanelCopy; source?: MemberSource; onOpenSource: (source: MemberSource) => void }) {
  const sourceLabel = source?.title ?? citation.source_title ?? `${copy.citationLabel} ${number}`;
  const accessibleContext = citation.claim_text ?? sourceLabel;
  return (
    <SourceOpenTarget source={source} className="nn-button nn-button--outline nn-button--size-sm citation-chip" ariaLabel={`${copy.citationLabel} ${number} for ${accessibleContext}`} onInlineOriginal={onOpenSource}>[{number}] {sourceLabel}</SourceOpenTarget>
  );
}
