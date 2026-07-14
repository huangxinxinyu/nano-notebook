import * as AvatarPrimitive from "@radix-ui/react-avatar";
import * as React from "react";
import { cn } from "../../lib/utils";

function Avatar({ className, ...props }: React.ComponentProps<typeof AvatarPrimitive.Root>) {
  return <AvatarPrimitive.Root data-slot="avatar" className={cn("nn-avatar", className)} {...props} />;
}

function AvatarImage({ className, ...props }: React.ComponentProps<typeof AvatarPrimitive.Image>) {
  return <AvatarPrimitive.Image data-slot="avatar-image" className={cn("nn-avatar-image", className)} {...props} />;
}

function AvatarFallback({ className, ...props }: React.ComponentProps<typeof AvatarPrimitive.Fallback>) {
  return <AvatarPrimitive.Fallback data-slot="avatar-fallback" className={cn("nn-avatar-fallback", className)} {...props} />;
}

export { Avatar, AvatarFallback, AvatarImage };
