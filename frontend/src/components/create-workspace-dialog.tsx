"use client";

import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { useAuth } from "@/context/AuthContext";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  cancelButton,
  dialogContent,
  dialogFooter,
  dialogHeader,
  dialogText,
  dialogTitle,
  fieldInput,
  fieldLabel,
  primaryButton,
} from "@/components/settings/settings-frame";

interface CreateWorkspaceDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Where focus goes when it closes, when it didn't open from a trigger. */
  onCloseAutoFocus?: (e: Event) => void;
}

/** Instance operators create workspaces (D-21), from the workspace menu or
 *  Settings → Account. Creating one opens it. */
export function CreateWorkspaceDialog({
  open,
  onOpenChange,
  onCloseAutoFocus,
}: CreateWorkspaceDialogProps) {
  const { createOrg } = useAuth();
  const [name, setName] = useState("");
  const create = useMutation({ mutationFn: () => createOrg(name.trim()) });
  const close = (o: boolean) => {
    if (create.isPending) return;
    onOpenChange(o);
    if (!o) {
      setName("");
      create.reset();
    }
  };
  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent
        className={dialogContent}
        onCloseAutoFocus={onCloseAutoFocus}
      >
        <form
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <DialogHeader className={dialogHeader}>
            <DialogTitle className={dialogTitle}>
              Create a workspace
            </DialogTitle>
            <DialogDescription className={dialogText}>
              A separate workspace with its own projects, servers, members and
              settings. You switch to it when it is created.
            </DialogDescription>
          </DialogHeader>
          <div className="mx-5 mt-4 flex flex-col gap-1.5">
            <label htmlFor="new-workspace-name" className={fieldLabel}>
              Workspace name
            </label>
            <input
              id="new-workspace-name"
              required
              maxLength={100}
              value={name}
              onChange={(e) => setName(e.target.value)}
              className={fieldInput}
            />
          </div>
          {create.error && (
            <p
              role="alert"
              className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg"
            >
              {create.error instanceof Error
                ? create.error.message
                : "The workspace was not created."}
            </p>
          )}
          <div className={dialogFooter}>
            <button
              type="button"
              className={cancelButton}
              onClick={() => close(false)}
            >
              Cancel
            </button>
            <button
              type="submit"
              className={primaryButton}
              disabled={!name.trim() || create.isPending}
            >
              {create.isPending ? "Creating…" : "Create workspace"}
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
