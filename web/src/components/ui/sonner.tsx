import type * as React from "react";
import type { ToasterProps } from "sonner";
import { Toaster as Sonner } from "sonner";

const Toaster = ({ ...props }: ToasterProps) => (
  <Sonner
    theme="dark"
    className="toaster group"
    style={
      {
        "--normal-bg": "var(--popover)",
        "--normal-text": "var(--popover-foreground)",
        "--normal-border": "var(--border)",
        "--success-bg": "var(--popover)",
        "--success-text": "var(--popover-foreground)",
        "--success-border": "var(--border)"
      } as React.CSSProperties
    }
    {...props}
  />
);

export { Toaster };
