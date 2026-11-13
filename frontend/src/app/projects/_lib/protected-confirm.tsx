"use client";

import { useRef } from "react";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import { outline, primary } from "./ui";

/** The frame of a deploy into a protected environment (D-143): an
 *  alertdialog whose Cancel has the focus; on a phone a full-screen sheet
 *  with Cancel and the action pinned at the bottom. New service's confirm
 *  and DeployConfirm put their own content in it. */
export function ProtectedConfirm({
  open,
  title,
  description,
  action,
  pending,
  pendingAction,
  onCancel,
  onConfirm,
  children,
}: {
  open: boolean;
  title: React.ReactNode;
  description: React.ReactNode;
  /** "Deploy c58e0a1 to production". */
  action: string;
  pending: boolean;
  /** The action's label while it runs: "Deploying…". */
  pendingAction: string;
  onCancel: () => void;
  onConfirm: () => void;
  children: React.ReactNode;
}) {
  const cancel = useRef<HTMLButtonElement>(null);
  return (
    <Dialog open={open} onOpenChange={(o) => !o && !pending && onCancel()}>
      <DialogContent
        role="alertdialog"
        showClose={false}
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          cancel.current?.focus();
        }}
        className="flex max-w-[600px] flex-col overflow-hidden p-0 sm:top-16 sm:max-h-[calc(100dvh-96px)] sm:translate-y-0 max-sm:inset-0 max-sm:flex max-sm:h-full max-sm:max-h-none max-sm:w-full max-sm:max-w-none max-sm:translate-x-0 max-sm:translate-y-0 max-sm:flex-col max-sm:rounded-none max-sm:border-0"
      >
        <div className="flex min-h-0 flex-1 flex-col gap-3.5 overflow-y-auto p-5">
          <DialogTitle className="flex flex-wrap items-center gap-2 text-[17px] font-semibold leading-6">{title}</DialogTitle>
          <DialogDescription className="-mt-1.5 text-[13px] text-fg/70">{description}</DialogDescription>
          {children}
        </div>
        <div className="flex flex-none justify-end gap-2 border-t border-subtle px-4 py-2.5 max-sm:pb-[calc(10px+env(safe-area-inset-bottom))] max-sm:[&>button]:flex-1">
          <button ref={cancel} type="button" className={cn(outline, "justify-center focus:ring-2 focus:ring-focus")} disabled={pending} onClick={onCancel}>
            Cancel
          </button>
          <button type="button" className={cn(primary, "justify-center")} disabled={pending} onClick={onConfirm}>
            {pending ? pendingAction : action}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
