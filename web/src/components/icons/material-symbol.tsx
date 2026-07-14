import type { ComponentPropsWithoutRef, CSSProperties } from "react";
import { cn } from "../../lib/utils";

type MaterialSymbolFamily = "rounded" | "outlined";

type MaterialSymbolStyle = CSSProperties & {
  "--symbol-fill": 0 | 1;
  "--symbol-grade": number;
  "--symbol-opsz": number;
  "--symbol-weight": number;
};

type MaterialSymbolProps = Omit<ComponentPropsWithoutRef<"span">, "aria-label" | "children" | "role"> & {
  name: string;
  label?: string;
  family?: MaterialSymbolFamily;
  size?: number;
  opticalSize?: number;
  weight?: number;
  fill?: boolean;
  grade?: number;
};

export function MaterialSymbol({
  name,
  label,
  family = "rounded",
  size = 24,
  opticalSize = size,
  weight = 400,
  fill = false,
  grade = 0,
  className,
  style,
  ...props
}: MaterialSymbolProps) {
  const symbolStyle: MaterialSymbolStyle = {
    ...style,
    "--symbol-fill": fill ? 1 : 0,
    "--symbol-grade": grade,
    "--symbol-opsz": opticalSize,
    "--symbol-weight": weight,
    fontSize: size
  };

  return (
    <span
      {...props}
      aria-hidden={label ? undefined : true}
      aria-label={label}
      className={cn("material-symbol", `material-symbol--${family}`, className)}
      role={label ? "img" : undefined}
      style={symbolStyle}
    >
      {name}
    </span>
  );
}
