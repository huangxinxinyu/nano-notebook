import { MaterialSymbol } from "../icons/material-symbol";
import { Textarea } from "../ui/textarea";

type ChatPlaceholderPanelProps = {
  title: string;
  unavailableLabel: string;
  emptyTitle: string;
  emptyBody: string;
  composerPlaceholder: string;
};

export function ChatPlaceholderPanelContent(props: ChatPlaceholderPanelProps) {
  return (
    <div className="workspace-panel-content chat-placeholder-content" data-placeholder="true" data-chat-framework="@assistant-ui/react">
      <div className="workspace-panel-header">
        <h2>{props.title}</h2>
        <MaterialSymbol name="more_vert" size={20} />
      </div>
      <div className="chat-empty-state">
        <span className="chat-empty-icon"><MaterialSymbol name="chat_bubble" size={27} /></span>
        <strong>{props.emptyTitle}</strong>
        <p>{props.emptyBody}</p>
      </div>
      <div className="chat-composer-placeholder">
        <Textarea aria-label={props.unavailableLabel} disabled placeholder={props.composerPlaceholder} rows={1} />
        <span className="chat-source-count">0</span>
        <span className="chat-send-placeholder"><MaterialSymbol name="arrow_upward" size={22} /></span>
      </div>
    </div>
  );
}
