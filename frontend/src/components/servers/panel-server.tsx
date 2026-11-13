"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { useIsOperator } from "@/context/AuthContext";
import { panelHostQuery } from "@/lib/api/instance.service";
import { size } from "@/components/instance/instance-view";
import { workspaceServicesQuery } from "@/lib/queries";
import { ToftMark } from "@/components/shell/toft-mark";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import { Chip } from "./resource-bar";

// The panel's own server in Servers (D-68): listed only when apps deploy
// there, as one slim row after the servers apps deploy to (D-70), with a
// Panel badge and what a deploy needs; under "Only Toft" the App servers
// card's foot says where it is (board ReworkServers). Its health, shell and upgrades live in Instance, for
// operators.

/** The slim row, when apps run on the panel's server. */
export function PanelServerCard() {
  const isOperator = useIsOperator();
  const hostRead = useQuery(panelHostQuery);
  const host = hostRead.data;
  const services = useQuery(workspaceServicesQuery);
  if (readFailed(hostRead)) {
    return (
      <div className="border-t border-subtle px-4 py-3">
        <LoadFailed what="the panel’s server" onRetry={() => void hostRead.refetch()} />
      </div>
    );
  }
  if (!host || host.role !== "apps") return null;
  const here = (services.data ?? []).filter((s) => s.server_id === "");
  const r = host.reservation;
  const meta = [host.address, `${host.cpu_cores} vCPU`, size(host.memory_bytes)].filter(Boolean).join(" · ");
  return (
    <section aria-labelledby="srv-panel" className="flex flex-col gap-1.5 border-t border-subtle px-4 py-3">
      <div className="flex flex-wrap items-center gap-2.5">
        <span aria-hidden="true" className="h-2 w-2 rounded-full bg-status-running" />
        <h3 id="srv-panel" className="text-sm font-semibold leading-5 text-fg">
          The panel’s server
        </h3>
        <span className="inline-flex h-[22px] items-center gap-1.5 rounded-full bg-[color-mix(in_oklab,var(--accent)_22%,transparent)] px-2 text-xs font-medium text-fg">
          <ToftMark className="h-3 w-3" />
          Panel
        </span>
        <Chip>Run apps here too</Chip>
        <span className="font-mono text-xs text-muted">{meta}</span>
        <span className="flex-1" />
        {isOperator && (
          <Link href="/instance/server" className="touch-target inline-flex items-center text-xs font-medium text-link hover:underline">
            Health, shell and upgrades in Instance →
          </Link>
        )}
      </div>
      <p className="flex flex-wrap gap-x-2 pl-[18px] text-xs leading-[17px] text-fg/70">
        <span>
          Apps get {r.apps_cpu} vCPU and {size(r.apps_memory_bytes)} after the panel’s share
          {r.enforced ? ", and are held to it." : "."}
        </span>
        <span>
          <span className="text-muted">Running here</span>{" "}
          {!services.data
            ? "—"
            : here.length === 0
            ? "Nothing of this workspace"
            : `${here.length} ${here.length === 1 ? "service" : "services"} of this workspace`}
        </span>
      </p>
    </section>
  );
}

/** The App servers card's foot when the panel's server is Only Toft, so nobody looks for it among the servers apps run on. */
export function PanelServerNote() {
  const isOperator = useIsOperator();
  const host = useQuery({ ...panelHostQuery, meta: { quietFailure: "PanelServerCard, on the same read just above this note, draws the failed row; this foot would only say it twice." } }).data;
  if (!host || host.role !== "control_plane") return null;
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
      <span className="inline-flex items-center gap-2">
        <ToftMark className="h-3.5 w-3.5 flex-none" />
        The panel’s own server is Only Toft, so it isn’t listed here.
      </span>
      {isOperator ? (
        <Link href="/instance/server" className="text-link hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus">
          It’s in Instance
        </Link>
      ) : (
        <span>An instance operator looks after it.</span>
      )}
    </div>
  );
}
