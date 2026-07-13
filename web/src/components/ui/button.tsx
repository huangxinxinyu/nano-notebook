/* eslint-disable react-refresh/only-export-components */
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import * as React from "react";
import { cn } from "../../lib/utils";

const buttonVariants = cva("nn-button", {
  variants: {
    variant: {
      default: "nn-button--default",
      destructive: "nn-button--destructive",
      outline: "nn-button--outline",
      secondary: "nn-button--secondary",
      ghost: "nn-button--ghost",
      link: "nn-button--link"
    },
    size: {
      default: "nn-button--size-default",
      sm: "nn-button--size-sm",
      lg: "nn-button--size-lg",
      icon: "nn-button--size-icon"
    }
  },
  defaultVariants: {
    variant: "default",
    size: "default"
  }
});

function Button({
  className,
  variant,
  size,
  asChild = false,
  ...props
}: React.ComponentProps<"button"> &
  VariantProps<typeof buttonVariants> & {
    asChild?: boolean;
  }) {
  const Comp = asChild ? Slot : "button";

  return <Comp data-slot="button" className={cn(buttonVariants({ variant, size, className }))} {...props} />;
}

export { Button, buttonVariants };
