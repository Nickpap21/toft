"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { inboxService } from "@/lib/api/inbox.service";

/** A server's Needs attention row while nothing backs up the databases and
 *  volumes on it: the Inbox's no_backup (serverId null is the panel host).
 *  Marked done in the Inbox, it still shows here while it holds. href is
 *  where its backups are set up, for whoever may. */
export function NoBackupRow({ serverId, href }: { serverId: string | null; href?: string }) {
  const needs = useQuery({ queryKey: ["inbox", "needs-you", "any"], queryFn: () => inboxService.needsYou("any") });
  const item = needs.data?.find((it) => it.kind === "no_backup" && (it.server_id ?? null) === serverId);
  if (!item) return null;
  return (
    <section
      aria-labelledby="nb-h"
      className="flex flex-wrap items-baseline gap-x-3 gap-y-1.5 rounded-lg border border-status-pending/40 bg-status-pending-soft/20 px-3.5 py-2.5 text-[13px] leading-[19px]"
    >
      <h2 id="nb-h" className="font-semibold text-status-pending-fg">Needs attention</h2>
      <p className="min-w-0 flex-1 text-fg/85">{item.detail}: if this server’s disk is lost, the data goes with it.</p>
      {href && (
        <Link href={href} className="text-link hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus">
          Set up backups
        </Link>
      )}
    </section>
  );
}
