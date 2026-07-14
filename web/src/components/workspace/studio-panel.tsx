import { toast } from "sonner";
import { IconButton } from "../icons/icon-button";
import { MaterialSymbol } from "../icons/material-symbol";
import { Badge } from "../ui/badge";
import { Button } from "../ui/button";

type StudioAction = { icon: string; label: string; tone: string; beta?: boolean };

type StudioPanelProps = {
  title: string;
  actions: StudioAction[];
  betaLabel: string;
  emptyTitle: string;
  emptyBody: string;
  addNoteLabel: string;
  collapseLabel: string;
  comingSoonMessage: string;
};

export function StudioPanelContent(props: StudioPanelProps) {
  const comingSoon = () => toast(props.comingSoonMessage);

  return (
    <div className="workspace-panel-content studio-panel-content">
      <div className="workspace-panel-header">
        <h2>{props.title}</h2>
        <IconButton icon="left_panel_close" label={props.collapseLabel} symbolSize={19} onClick={comingSoon} />
      </div>
      <div className="studio-action-grid" data-placeholder="true">
        {props.actions.map((action) => (
          <Button className="studio-action-card" data-tone={action.tone} key={action.label} variant="ghost" onClick={comingSoon}>
            <MaterialSymbol name={action.icon} size={19} />
            <span>{action.label}</span>
            {action.beta ? <Badge variant="outline">{props.betaLabel}</Badge> : null}
            <MaterialSymbol className="studio-action-arrow" name="chevron_right" size={20} />
          </Button>
        ))}
      </div>
      <div className="studio-empty-state">
        <MaterialSymbol name="auto_awesome" size={30} />
        <strong>{props.emptyTitle}</strong>
        <p>{props.emptyBody}</p>
      </div>
      <Button className="studio-add-note" onClick={comingSoon}><MaterialSymbol name="note_add" size={19} />{props.addNoteLabel}</Button>
    </div>
  );
}
