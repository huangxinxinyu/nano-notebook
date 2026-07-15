import {
  AssistantRuntimeProvider,
  ComposerPrimitive,
  MessagePrimitive,
  ThreadPrimitive,
  useExternalStoreRuntime,
  type AssistantRuntime
} from "@assistant-ui/react";
import { useEffect, useRef } from "react";
import { MaterialSymbol } from "../icons/material-symbol";
import { appendMessageText, type ChatController, type ChatMessage } from "./private-chat";

export type ChatPanelCopy = {
  title: string;
  emptyTitle: string;
  emptyBody: string;
  composerPlaceholder: string;
  composerLabel: string;
  sendLabel: string;
  waitingLabel: string;
  generatingLabel: string;
  knowledgeLabel: string;
  failedLabel: string;
  unavailableLabel: string;
};

export function ChatPanelContent({ copy, controller }: { copy: ChatPanelCopy; controller: ChatController }) {
  const messages = controller.snapshot?.messages ?? [];
  const run = controller.snapshot?.active_run;
  const isRunning = run?.status === "queued" || run?.status === "running";
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
              <ThreadPrimitive.Messages components={{ UserMessage, AssistantMessage: () => <AssistantMessage knowledgeLabel={copy.knowledgeLabel} /> }} />
            </div>
            {run?.status === "queued" ? <div className="chat-activity" role="status">{copy.waitingLabel}</div> : null}
            {run?.status === "running" ? <div className="chat-activity" role="status">{copy.generatingLabel}</div> : null}
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

function UserMessage() {
  return <MessagePrimitive.Root className="chat-message chat-message--user"><MessagePrimitive.Parts /></MessagePrimitive.Root>;
}

function AssistantMessage({ knowledgeLabel }: { knowledgeLabel: string }) {
  return (
    <MessagePrimitive.Root className="chat-message chat-message--assistant">
      <span className="chat-answer-mode">{knowledgeLabel}</span>
      <MessagePrimitive.Parts />
    </MessagePrimitive.Root>
  );
}
