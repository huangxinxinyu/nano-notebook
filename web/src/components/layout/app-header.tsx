import { toast } from "sonner";
import { IconButton } from "../icons/icon-button";
import { MaterialSymbol } from "../icons/material-symbol";
import { Button } from "../ui/button";
import { ProductMark } from "./product-mark";
import { UserMenu } from "./user-menu";

type LibraryHeaderProps = {
  appName: string;
  email: string;
  settingsLabel: string;
  appsLabel: string;
  openUserMenuLabel: string;
  languageLabel: string;
  signOutLabel: string;
  signingOutLabel: string;
  comingSoonMessage: string;
  signingOut: boolean;
  onLanguage: () => void;
  onSignOut: () => void;
};

export function LibraryHeader(props: LibraryHeaderProps) {
  return (
    <header className="app-header">
      <ProductMark name={props.appName} />
      <div className="app-header-actions">
        <Button className="header-settings" variant="outline" onClick={() => toast(props.comingSoonMessage)}>
          <MaterialSymbol name="settings" size={19} />
          {props.settingsLabel}
        </Button>
        <IconButton icon="apps" label={props.appsLabel} onClick={() => toast(props.comingSoonMessage)} />
        <UserMenu
          email={props.email}
          openLabel={props.openUserMenuLabel}
          languageLabel={props.languageLabel}
          signOutLabel={props.signOutLabel}
          signingOutLabel={props.signingOutLabel}
          signingOut={props.signingOut}
          onLanguage={props.onLanguage}
          onSignOut={props.onSignOut}
        />
      </div>
    </header>
  );
}
