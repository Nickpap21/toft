"use client";

import * as React from "react";
import * as MenuPrimitive from "@radix-ui/react-dropdown-menu";
import { ChevronRight } from "lucide-react";
import { cn } from "@/lib/utils";

// A thin Radix DropdownMenu on theme tokens. Radix supplies the keyboard
// model (arrow keys, typeahead, Escape, focus return) and aria-expanded.

const DropdownMenu = MenuPrimitive.Root;
const DropdownMenuTrigger = MenuPrimitive.Trigger;
const DropdownMenuGroup = MenuPrimitive.Group;
const DropdownMenuRadioGroup = MenuPrimitive.RadioGroup;
const DropdownMenuSub = MenuPrimitive.Sub;

const contentClasses = "toft-menu z-50 min-w-48 overflow-hidden rounded-lg border border-subtle bg-surface p-1 text-fg shadow-menu";

const DropdownMenuContent = React.forwardRef<
  React.ElementRef<typeof MenuPrimitive.Content>,
  React.ComponentPropsWithoutRef<typeof MenuPrimitive.Content>
>(({ className, sideOffset = 6, ...props }, ref) => (
  <MenuPrimitive.Portal>
    <MenuPrimitive.Content
      ref={ref}
      sideOffset={sideOffset}
      className={cn(contentClasses, className)}
      {...props}
    />
  </MenuPrimitive.Portal>
));
DropdownMenuContent.displayName = MenuPrimitive.Content.displayName;

const DropdownMenuSubContent = React.forwardRef<
  React.ElementRef<typeof MenuPrimitive.SubContent>,
  React.ComponentPropsWithoutRef<typeof MenuPrimitive.SubContent>
>(({ className, sideOffset = 4, collisionPadding = 8, ...props }, ref) => (
  <MenuPrimitive.Portal>
    <MenuPrimitive.SubContent
      ref={ref}
      sideOffset={sideOffset}
      collisionPadding={collisionPadding}
      className={cn(contentClasses, className)}
      {...props}
    />
  </MenuPrimitive.Portal>
));
DropdownMenuSubContent.displayName = MenuPrimitive.SubContent.displayName;

const itemClasses =
  "touch-target relative flex cursor-pointer select-none items-center gap-2 rounded-md px-2 py-1.5 text-sm text-fg outline-none data-[highlighted]:bg-raised data-[highlighted]:outline-2 data-[highlighted]:-outline-offset-2 data-[highlighted]:outline-focus data-[disabled]:pointer-events-none data-[disabled]:opacity-50";

const DropdownMenuItem = React.forwardRef<
  React.ElementRef<typeof MenuPrimitive.Item>,
  React.ComponentPropsWithoutRef<typeof MenuPrimitive.Item>
>(({ className, ...props }, ref) => (
  <MenuPrimitive.Item ref={ref} className={cn(itemClasses, className)} {...props} />
));
DropdownMenuItem.displayName = MenuPrimitive.Item.displayName;

const DropdownMenuSubTrigger = React.forwardRef<
  React.ElementRef<typeof MenuPrimitive.SubTrigger>,
  React.ComponentPropsWithoutRef<typeof MenuPrimitive.SubTrigger>
>(({ className, children, ...props }, ref) => (
  <MenuPrimitive.SubTrigger ref={ref} className={cn(itemClasses, "data-[state=open]:bg-raised", className)} {...props}>
    {children}
    <ChevronRight className="ml-auto h-3.5 w-3.5 text-muted" aria-hidden="true" />
  </MenuPrimitive.SubTrigger>
));
DropdownMenuSubTrigger.displayName = MenuPrimitive.SubTrigger.displayName;

const DropdownMenuRadioItem = React.forwardRef<
  React.ElementRef<typeof MenuPrimitive.RadioItem>,
  React.ComponentPropsWithoutRef<typeof MenuPrimitive.RadioItem>
>(({ className, children, ...props }, ref) => (
  <MenuPrimitive.RadioItem ref={ref} className={cn(itemClasses, "pl-7", className)} {...props}>
    <span className="absolute left-2 flex h-3.5 w-3.5 items-center justify-center">
      <MenuPrimitive.ItemIndicator>
        <span aria-hidden="true" className="block h-1.5 w-1.5 rounded-full bg-fg" />
      </MenuPrimitive.ItemIndicator>
    </span>
    {children}
  </MenuPrimitive.RadioItem>
));
DropdownMenuRadioItem.displayName = MenuPrimitive.RadioItem.displayName;

const DropdownMenuLabel = React.forwardRef<
  React.ElementRef<typeof MenuPrimitive.Label>,
  React.ComponentPropsWithoutRef<typeof MenuPrimitive.Label>
>(({ className, ...props }, ref) => (
  <MenuPrimitive.Label ref={ref} className={cn("px-2 py-1.5 text-xs font-medium text-muted", className)} {...props} />
));
DropdownMenuLabel.displayName = MenuPrimitive.Label.displayName;

const DropdownMenuSeparator = React.forwardRef<
  React.ElementRef<typeof MenuPrimitive.Separator>,
  React.ComponentPropsWithoutRef<typeof MenuPrimitive.Separator>
>(({ className, ...props }, ref) => (
  <MenuPrimitive.Separator ref={ref} className={cn("-mx-1 my-1 h-px bg-subtle", className)} {...props} />
));
DropdownMenuSeparator.displayName = MenuPrimitive.Separator.displayName;

export {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubTrigger,
  DropdownMenuSubContent,
};
