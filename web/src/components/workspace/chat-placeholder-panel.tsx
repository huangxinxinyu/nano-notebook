import {
  AssistantRuntimeProvider,
  ComposerPrimitive,
  MessagePrimitive,
  ThreadPrimitive,
  useAuiState,
  useExternalStoreRuntime,
  type AssistantRuntime
} from "@assistant-ui/react";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { MaterialSymbol } from "../icons/material-symbol";
import { Button } from "../ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "../ui/dialog";
import { appendMessageText, type ChatController, type ChatMessage, type Citation } from "./private-chat";

export type ChatPanelCopy = {
  title: string;
  emptyTitle: string;
  emptyBody: string;
  composerPlaceholder: string;
  composerLabel: string;
  sendLabel: string;
  waitingLabel: string;
  generatingLabel: string;
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
};

export function ChatPanelContent({ copy, controller, selectedSourceCount = 0 }: { copy: ChatPanelCopy; controller: ChatController; selectedSourceCount?: number }) {
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
        <p className="chat-source-disclosure">{selectedSourceCount > 0 ? copy.selectedSourceDisclosure.replace("{count}", String(selectedSourceCount)) : copy.sourceDisclosure}</p>
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
                AssistantMessage: () => <AssistantMessage controller={controller} copy={copy} />
              }} />
            </div>
            {run ? (
              <div className="chat-activity" role="status">
                <span>{run.status === "queued" ? copy.waitingLabel : copy.generatingLabel}</span>
                <Button variant="ghost" size="sm" onClick={() => void controller.stop(run.id)}>{copy.stopLabel}</Button>
              </div>
            ) : null}
            {controller.error ? <div className="chat-error" role="alert">{controller.error}</div> : null}
          </ThreadPrimitive.Viewport>
          <ComposerPrimitive.Root className="chat-composer">
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

function UserMessage({ controller, copy, latestMessageID }: { controller: ChatController; copy: ChatPanelCopy; latestMessageID?: string }) {
  const messageID = useAuiState((state) => state.message.id);
  const run = controller.snapshot?.runs.find((item) => item.input_message_id === messageID);
  const canRetry = messageID === latestMessageID && (run?.status === "failed" || run?.status === "cancelled");
  return (
    <MessagePrimitive.Root className="chat-message chat-message--user">
      <MessagePrimitive.Parts />
      {run?.status === "failed" || run?.status === "cancelled" ? (
        <span className="chat-run-terminal">
          {run.status === "failed" ? copy.failedLabel : copy.stoppedLabel}
          {canRetry ? <Button variant="ghost" size="sm" onClick={() => void controller.retry(run.id)}>{copy.retryLabel}</Button> : null}
        </span>
      ) : null}
    </MessagePrimitive.Root>
  );
}

function AssistantMessage({ controller, copy }: { controller: ChatController; copy: ChatPanelCopy }) {
  const messageID = useAuiState((state) => state.message.id);
  const citations = controller.snapshot?.citations.filter((citation) => citation.message_id === messageID) ?? [];
  return (
    <MessagePrimitive.Root className="chat-message chat-message--assistant">
      <MessagePrimitive.Parts />
      {citations.length ? <div className="chat-citations">{citations.map((citation, index) => <CitationButton key={citation.id} citation={citation} number={index + 1} copy={copy} />)}</div> : null}
    </MessagePrimitive.Root>
  );
}

type Coordinate = { page?: number; slide?: number; start_ms?: number; end_ms?: number };

type CitationView = {
  citation: Citation;
  source_title: string;
  source_format: string;
  unit_kind: string;
  preview: string;
  coordinate?: Coordinate;
};

function CitationButton({ citation, number, copy }: { citation: Citation; number: number; copy: ChatPanelCopy }) {
  const [open, setOpen] = useState(false);
  const view = useQuery({
    queryKey: ["citation", citation.id],
    enabled: open,
    queryFn: async (): Promise<CitationView> => {
      const response = await fetch(`/api/v1/citations/${citation.id}`, { credentials: "include" });
      if (!response.ok) throw new Error(copy.citationUnavailableLabel);
      return ((await response.json()) as { citation: CitationView }).citation;
    },
    retry: false
  });
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <Button className="citation-chip" variant="outline" size="sm" aria-label={`${copy.citationLabel} ${number} for ${citation.claim_text}`} onClick={() => setOpen(true)}>[{number}]</Button>
      <DialogContent className="citation-dialog" closeLabel={copy.closeLabel}>
        <DialogTitle>{view.data?.source_title ?? copy.citationPreviewLabel}</DialogTitle>
        <DialogDescription>{view.data ? citationLocation(view.data) : copy.citationPreviewLabel}</DialogDescription>
        {view.isError ? <p role="alert">{copy.citationUnavailableLabel}</p> : null}
        {view.data ? <blockquote>{view.data.preview}</blockquote> : null}
      </DialogContent>
    </Dialog>
  );
}

function citationLocation(view: CitationView) {
  const coordinate = view.coordinate;
  if (coordinate?.page) return `Page ${coordinate.page}`;
  if (coordinate?.slide) return `Slide ${coordinate.slide}`;
  if (coordinate?.start_ms !== undefined) return formatTimeRange(coordinate.start_ms, coordinate.end_ms);
  return `${view.source_format.toUpperCase()} · ${view.unit_kind}`;
}

function formatTimeRange(startMS: number, endMS?: number) {
  const seconds = (value: number) => `${Math.floor(value / 60000)}:${String(Math.floor(value / 1000) % 60).padStart(2, "0")}`;
  return endMS === undefined ? seconds(startMS) : `${seconds(startMS)}–${seconds(endMS)}`;
}
