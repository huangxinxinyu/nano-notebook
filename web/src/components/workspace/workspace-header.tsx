import type { ReactNode } from "react";
import { toast } from "sonner";
import { IconButton } from "../icons/icon-button";
import { MaterialSymbol } from "../icons/material-symbol";
import { Button } from "../ui/button";
import { UserMenu } from "../layout/user-menu";

type WorkspaceHeaderProps = {
  title: string;
  backLabel: string;
  createAction: ReactNode;
  analyzeLabel: string;
  shareLabel: string;
  shareAction?: ReactNode;
  settingsLabel: string;
  appsLabel: string;
  email: string;
  openUserMenuLabel: string;
  languageLabel: string;
  signOutLabel: string;
  signingOutLabel: string;
  signingOut: boolean;
  comingSoonMessage: string;
  onBack: () => void;
  onLanguage: () => void;
  onSignOut: () => void;
};

export function WorkspaceHeader(props: WorkspaceHeaderProps) {
  const comingSoon = () => toast(props.comingSoonMessage);

  return (
    <header className="workspace-header">
      <div className="workspace-header-title">
        <Button className="workspace-home" size="icon" variant="ghost" aria-label={props.backLabel} onClick={props.onBack}>
          <MaterialSymbol name="notebook" size={28} weight={500} />
        </Button>
        <h1>{props.title}</h1>
      </div>
      <div className="workspace-header-actions">
        {props.createAction}
        <Button className="workspace-header-pill secondary-workspace-action" variant="outline" onClick={comingSoon}><MaterialSymbol name="monitoring" size={19} />{props.analyzeLabel}</Button>
        {props.shareAction ?? <Button className="workspace-header-pill secondary-workspace-action" variant="outline" onClick={comingSoon}><MaterialSymbol name="share" size={19} />{props.shareLabel}</Button>}
        <Button className="workspace-header-pill secondary-workspace-action" variant="outline" onClick={comingSoon}><MaterialSymbol name="settings" size={19} />{props.settingsLabel}</Button>
        <IconButton icon="apps" label={props.appsLabel} onClick={comingSoon} />
        <UserMenu email={props.email} openLabel={props.openUserMenuLabel} languageLabel={props.languageLabel} signOutLabel={props.signOutLabel} signingOutLabel={props.signingOutLabel} signingOut={props.signingOut} onLanguage={props.onLanguage} onSignOut={props.onSignOut} />
      </div>
    </header>
  );
}
