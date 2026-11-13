"use client";

import { useRef, useState } from "react";
import { Check, ChevronDown, ChevronsUpDown, Plus } from "lucide-react";
import { toast } from "sonner";
import { useAuth, useIsOperator } from "@/context/AuthContext";
import { CreateWorkspaceDialog } from "@/components/create-workspace-dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

/** The workspace's initial on a small tile, as in the canvas sidebar. */
export function WorkspaceTile({ name, className = "" }: { name?: string; className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={`flex h-6 w-6 shrink-0 items-center justify-center rounded-md bg-accent text-xs font-bold text-on-accent ${className}`}
    >
      {(name?.trim()[0] ?? "T").toUpperCase()}
    </span>
  );
}

/** The sidebar's, or with variant "top" the phone's top bar's: the name
 *  and a chevron, without the tile (D-136). */
export function WorkspaceSwitcher({ variant = "side" }: { variant?: "side" | "top" }) {
  const { user, switchOrg } = useAuth();
  // Only instance operators can create workspaces (D-21); the server refuses
  // everyone else, so don't offer it.
  const isOperator = useIsOperator();

  const [showCreate, setShowCreate] = useState(false);
  const [busy, setBusy] = useState(false);
  // The create dialog opens from a menu item, not a Dialog.Trigger, so Radix
  // has nowhere to return focus: send it back to the switcher.
  const triggerRef = useRef<HTMLButtonElement>(null);

  async function handleSwitch(orgId: string) {
    if (orgId === user?.orgId) return;
    setBusy(true);
    try {
      await switchOrg(orgId);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to switch workspace");
    } finally {
      setBusy(false);
    }
  }

  const orgs = user?.orgs ?? [];
  const name = user?.orgName ?? "Toft";

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          ref={triggerRef}
          title={name}
          className={
            variant === "top"
              ? "flex h-11 min-w-0 items-center gap-1.5 rounded-lg px-1 text-left text-[15px] font-semibold text-fg transition-colors hover:bg-raised"
              : "flex h-10 w-full min-w-0 items-center gap-2.5 rounded-lg px-2 text-left text-sm font-semibold text-fg transition-colors hover:bg-raised rail-collapsed:justify-center rail-collapsed:px-0"
          }
        >
          {variant === "side" && <WorkspaceTile name={user?.orgName} />}
          <span className="min-w-0 flex-1 truncate rail-collapsed:sr-only">
            {name}
            {/* The name comes first, as it is seen (WCAG 2.5.3). */}
            <span className="sr-only">, switch workspace</span>
          </span>
          {variant === "side" ? (
            <ChevronsUpDown aria-hidden="true" className="h-4 w-4 shrink-0 text-muted rail-collapsed:hidden" />
          ) : (
            <ChevronDown aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-muted" />
          )}
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-64">
          <DropdownMenuLabel>Workspaces</DropdownMenuLabel>
          {orgs.map((o) => {
            const current = o.org_id === user?.orgId;
            return (
              <DropdownMenuItem key={o.org_id} disabled={busy} onSelect={() => handleSwitch(o.org_id)}>
                <Check aria-hidden="true" className={`h-3.5 w-3.5 shrink-0 ${current ? "opacity-100" : "opacity-0"}`} />
                <span className="min-w-0 flex-1 truncate">{o.name}</span>
                {current && <span className="sr-only">(current)</span>}
                <span className="text-xs capitalize text-muted">{o.role}</span>
              </DropdownMenuItem>
            );
          })}
          {orgs.length === 0 && <p className="px-2 py-1.5 text-xs text-muted">You&apos;re not a member of any workspace.</p>}
          {isOperator && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem onSelect={() => setShowCreate(true)} className="text-link">
                <Plus aria-hidden="true" className="h-3.5 w-3.5 shrink-0" />
                Create workspace
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      {isOperator && (
        <CreateWorkspaceDialog
          open={showCreate}
          onOpenChange={setShowCreate}
          onCloseAutoFocus={(e) => {
            e.preventDefault();
            triggerRef.current?.focus();
          }}
        />
      )}
    </>
  );
}
