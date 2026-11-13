"use client";

import * as React from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";

const Dialog = DialogPrimitive.Root;
const DialogTrigger = DialogPrimitive.Trigger;
const DialogPortal = DialogPrimitive.Portal;
const DialogClose = DialogPrimitive.Close;

const DialogOverlay = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Overlay>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Overlay>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Overlay
    ref={ref}
    className={cn(
      "toft-overlay fixed inset-0 z-50 bg-scrim/50 backdrop-blur-sm",
      className
    )}
    {...props}
  />
));
DialogOverlay.displayName = DialogPrimitive.Overlay.displayName;

const DialogContent = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Content>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Content> & {
    /** Render the corner close button (default). The command palette
     *  turns it off: Escape closes it. */
    showClose?: boolean;
    /** Where focus goes at close when the element focused at open is gone
     *  or still disabled, or nothing had focus: a dialog opened from a
     *  mutation's onError, after its button was disabled while pending. */
    returnFocus?: React.RefObject<HTMLElement | null>;
  }
>(({ className, children, showClose = true, onCloseAutoFocus, returnFocus, ...props }, ref) => {
  // Radix returns focus only to a Dialog.Trigger, and the app's dialogs are
  // controlled without one: remember what had focus when it opened.
  const opener = React.useRef<HTMLElement | null>(null);
  return (
  <DialogPortal>
    <DialogOverlay />
    <DialogPrimitive.Content
      ref={ref}
      className={cn(
        "toft-pop fixed left-1/2 top-1/2 z-50 -translate-x-1/2 -translate-y-1/2",
        "w-[calc(100%-2rem)] max-w-md max-h-[90vh] overflow-y-auto",
        "bg-surface text-fg border border-subtle rounded-xl shadow-dialog p-6",
        className
      )}
      {...props}
      onCloseAutoFocus={(e) => {
        onCloseAutoFocus?.(e);
        // Gone (its row was deleted) or disabled: returnFocus, else Radix's default.
        const el = [opener.current, returnFocus?.current].find((x) => x?.isConnected && !x.matches(":disabled"));
        opener.current = null;
        if (e.defaultPrevented || !el) return;
        e.preventDefault();
        el.focus();
      }}
    >
      <RememberFocus intoRef={opener} />
      {children}
      {showClose && (
        <DialogPrimitive.Close className="touch-target absolute right-4 top-4 flex items-center justify-center rounded-md p-1 text-muted hover:text-fg hover:bg-raised pointer-coarse:right-1.5 pointer-coarse:top-1.5 pointer-coarse:min-w-11">
          <X className="h-4 w-4" aria-hidden="true" />
          <span className="sr-only">Close</span>
        </DialogPrimitive.Close>
      )}
    </DialogPrimitive.Content>
  </DialogPortal>
  );
});
DialogContent.displayName = DialogPrimitive.Content.displayName;

/** Mounts with the dialog's content and, as its first child, records the
 *  focused element in a layout effect: before an `autoFocus` field below it
 *  or Radix's own open focus can move focus inside. */
function RememberFocus({ intoRef }: { intoRef: React.RefObject<HTMLElement | null> }) {
  React.useLayoutEffect(() => {
    let el = document.activeElement;
    // A menu item leaves with its menu; the trigger that labels the menu stays.
    const menu = el?.closest('[role="menu"]');
    if (menu) el = document.getElementById(menu.getAttribute("aria-labelledby") ?? "");
    intoRef.current = el instanceof HTMLElement && el !== document.body ? el : null;
  }, [intoRef]);
  return null;
}

function DialogHeader({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cn("mb-5", className)} {...props} />
  );
}

function DialogFooter({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cn("flex justify-end gap-3 pt-2", className)} {...props} />
  );
}

const DialogTitle = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Title>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Title>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Title
    ref={ref}
    className={cn("text-xl font-semibold text-fg", className)}
    {...props}
  />
));
DialogTitle.displayName = DialogPrimitive.Title.displayName;

const DialogDescription = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Description>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Description>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Description
    ref={ref}
    className={cn("text-sm text-muted mt-1", className)}
    {...props}
  />
));
DialogDescription.displayName = DialogPrimitive.Description.displayName;

export {
  Dialog,
  DialogTrigger,
  DialogPortal,
  DialogOverlay,
  DialogClose,
  DialogContent,
  DialogHeader,
  DialogFooter,
  DialogTitle,
  DialogDescription,
};
