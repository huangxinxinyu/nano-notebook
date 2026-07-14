import { toast } from "sonner";
import { IconButton } from "../icons/icon-button";
import { MaterialSymbol } from "../icons/material-symbol";
import { Button } from "../ui/button";

type SourcePanelProps = {
  title: string;
  addSourcesLabel: string;
  searchWebLabel: string;
  webLabel: string;
  fastResearchLabel: string;
  emptyTitle: string;
  emptyBody: string;
  collapseLabel: string;
  comingSoonMessage: string;
};

export function SourcePanelContent(props: SourcePanelProps) {
  const comingSoon = () => toast(props.comingSoonMessage);

  return (
    <div className="workspace-panel-content source-panel-content">
      <div className="workspace-panel-header">
        <h2>{props.title}</h2>
        <IconButton icon="right_panel_close" label={props.collapseLabel} symbolSize={19} onClick={comingSoon} />
      </div>
      <div className="source-panel-controls">
        <Button className="add-sources-action" variant="outline" onClick={comingSoon}>
          <MaterialSymbol name="add" size={20} />
          {props.addSourcesLabel}
        </Button>
        <div className="source-search-card" data-placeholder="true">
          <span className="source-search-label">{props.searchWebLabel}</span>
          <div className="source-search-actions">
            <Button variant="outline" onClick={comingSoon}><MaterialSymbol name="language" size={18} />{props.webLabel}<MaterialSymbol name="arrow_drop_down" size={17} /></Button>
            <Button variant="outline" onClick={comingSoon}><MaterialSymbol name="travel_explore" size={18} />{props.fastResearchLabel}<MaterialSymbol name="arrow_drop_down" size={17} /></Button>
            <IconButton className="source-search-submit" icon="search" label={props.searchWebLabel} onClick={comingSoon} />
          </div>
        </div>
      </div>
      <div className="panel-empty-state">
        <MaterialSymbol name="draft" size={28} />
        <strong>{props.emptyTitle}</strong>
        <p>{props.emptyBody}</p>
      </div>
    </div>
  );
}
