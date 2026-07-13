import type { ButtonHTMLAttributes } from "react";

type ButtonVariant = "primary" | "secondary" | "icon-text" | "notebook-card";

export function Button({ variant = "primary", className, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant }) {
  const classes = [variant, className].filter(Boolean).join(" ");
  return <button className={classes} {...props} />;
}
