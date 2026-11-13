"use client";

import { useState } from "react";
import { toast } from "sonner";
import { useAuth } from "@/context/AuthContext";
import type { Server } from "@/lib/api/server.service";
import { cn } from "@/lib/utils";

/** On a server another workspace shares with this one: a member of the
 *  owning workspace can switch to it to see the server's details there
 *  (D-39). Renders nothing for anyone else. */
export function OpenInOwner({
  server,
  className,
  label,
}: {
  server: Pick<Server, "id" | "shared_by" | "shared_by_org_id">;
  className?: string;
  /** In place of "Open in <owner> →". */
  label?: React.ReactNode;
}) {
  const { user, switchOrg } = useAuth();
  const [busy, setBusy] = useState(false);
  const orgId = server.shared_by_org_id;
  if (!orgId || !(user?.orgs ?? []).some((o) => o.org_id === orgId)) return null;

  return (
    <button
      type="button"
      disabled={busy}
      onClick={() => {
        setBusy(true);
        switchOrg(orgId, `/servers/${server.id}`).catch((err: unknown) => {
          toast.error(err instanceof Error && err.message ? err.message : "Failed to switch workspace");
          setBusy(false);
        });
      }}
      className={cn(
        "inline-flex h-[30px] items-center rounded-md px-2.5 text-xs text-muted hover:bg-raised hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-50",
        className,
      )}
    >
      {busy ? "Switching…" : (label ?? <>Open in {server.shared_by} →</>)}
    </button>
  );
}
