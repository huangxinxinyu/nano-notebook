import type { ComponentProps } from "react";
import { Button } from "../ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
import { MaterialSymbol } from "./material-symbol";

type IconButtonProps = Omit<ComponentProps<typeof Button>, "aria-label" | "children"> & {
  icon: string;
  label: string;
  tooltip?: string;
  symbolSize?: number;
};

export function IconButton({
  icon,
  label,
  tooltip = label,
  symbolSize = 20,
  className,
  ...props
}: IconButtonProps) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button aria-label={label} className={className} size="icon" variant="ghost" {...props}>
          <MaterialSymbol name={icon} size={symbolSize} />
        </Button>
      </TooltipTrigger>
      <TooltipContent sideOffset={8}>{tooltip}</TooltipContent>
    </Tooltip>
  );
}
