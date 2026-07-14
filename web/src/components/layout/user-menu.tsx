import { Avatar, AvatarFallback } from "../ui/avatar";
import { Button } from "../ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from "../ui/dropdown-menu";
import { MaterialSymbol } from "../icons/material-symbol";

type UserMenuProps = {
  email: string;
  openLabel: string;
  languageLabel: string;
  signOutLabel: string;
  signingOutLabel: string;
  signingOut: boolean;
  onLanguage: () => void;
  onSignOut: () => void;
};

function avatarInitials(email: string) {
  return email.split("@")[0].slice(0, 2).toUpperCase();
}

export function UserMenu({ email, openLabel, languageLabel, signOutLabel, signingOutLabel, signingOut, onLanguage, onSignOut }: UserMenuProps) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button className="user-menu-trigger" aria-label={openLabel} size="icon" variant="ghost">
          <Avatar>
            <AvatarFallback>{avatarInitials(email)}</AvatarFallback>
          </Avatar>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="user-menu-content">
        <DropdownMenuLabel className="user-menu-email">{email}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={onLanguage}>
          <MaterialSymbol name="language" size={19} />
          {languageLabel}
        </DropdownMenuItem>
        <DropdownMenuItem disabled={signingOut} onSelect={onSignOut}>
          <MaterialSymbol name="logout" size={19} />
          {signingOut ? signingOutLabel : signOutLabel}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
