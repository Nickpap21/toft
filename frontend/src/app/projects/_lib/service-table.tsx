"use client";

import Link from "next/link";
import { Box } from "lucide-react";
import { serviceHref } from "@/components/home/home";
import { TemporaryChip, isTemporary } from "@/components/ui/temporary-chip";
import type { Environment, ProjectOverview } from "@/lib/api/environment.service";
import type { Server } from "@/lib/api/server.service";
import type { Service } from "@/lib/api/service.service";
import { CATALOG_LOGOS } from "@/lib/catalog-logos";
import { connectTo } from "@/lib/network-alias";
import { ago } from "@/lib/time";
import { cn } from "@/lib/utils";
import { deploymentRef, deploymentRefTitle } from "../[id]/services/[serviceId]/_components/service-view";
import { inProse, kindOf, serverName, sourceOf } from "./projects-view";
import { ServiceRowActions, StatusText } from "./service-actions";
import { mono } from "./ui";

/** What the project overview says of one service: domains, last deploy. */
export type ServiceOverview = ProjectOverview["services"][number];
type EnvLike = Pick<Environment, "id" | "name" | "color" | "protected">;

export const svcTh = "px-4 py-2 text-left text-xs font-medium text-muted";
/** A row of the Services board's tables; below 640 px a card, the
 *  service and its status on top, the last deploy and actions below. */
export const svcRow = "max-sm:relative max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:items-start max-sm:gap-x-3 max-sm:gap-y-2 max-sm:border-t max-sm:border-subtle max-sm:px-4 max-sm:py-3 max-sm:first:border-t-0";
export const svcTd = "border-t border-subtle px-4 py-2.5 align-middle max-sm:border-0 max-sm:p-0";

/** A catalog entry's logo (D-141), else a box. */
export function ServiceMark({ slug, className }: { slug?: string; className?: string }) {
  const d = slug ? CATALOG_LOGOS[slug] : undefined;
  const cls = cn("mt-0.5 h-4 w-4 flex-none self-start text-muted", className);
  return d ? (
    <svg viewBox="0 0 24 24" className={cls} fill="currentColor" aria-hidden="true">
      <path d={d} />
    </svg>
  ) : (
    <Box className={cls} strokeWidth={1.8} aria-hidden="true" />
  );
}

/** Where a service takes requests: its first domain, its alias inside the
 *  network, or nothing for a worker. Whether it has no domain is only known
 *  once the overview was read. */
function Reach({ s, o, known }: { s: Service; o?: ServiceOverview; known: boolean }) {
  if (o?.domains[0]) {
    return (
      <span className="flex min-w-0 items-center">
        {/* A temporary name's HTTP link always works: it serves while the certificate is pending, then redirects to HTTPS (D-150). */}
        <a href={`${isTemporary(o.domains[0]) ? "http" : "https"}://${o.domains[0]}`} target="_blank" rel="noopener noreferrer" className={cn(mono, "relative z-[1] min-w-0 text-fg [overflow-wrap:anywhere] hover:underline")}>
          {o.domains[0]}
        </a>
        {isTemporary(o.domains[0]) && <TemporaryChip className="ml-1.5 self-center" />}
      </span>
    );
  }
  if (s.type === "worker") return <span className={cn(mono, "text-fg/80")}>no inbound traffic</span>;
  if (s.connect_to?.port) return <span className={cn(mono, "text-fg/80")}>{connectTo(s)} internal</span>;
  return known ? <span className="text-xs text-muted">No domain</span> : null;
}

/**
 * A project's services as the Services board's tables draw them: the
 * service with its kind, source and where it takes requests; its status;
 * its last deploy; Deploy and the ⋯ menu.
 */
export function ServiceTable({
  label: tableLabel,
  services,
  byService,
  servers,
  env,
  canDeploy,
  grafanaUrl,
  now,
}: {
  label: string;
  services: Service[];
  /** Undefined until the overview was read: what wasn't read isn't "never". */
  byService: Map<string, ServiceOverview> | undefined;
  servers: Server[] | undefined;
  /** The environment every row is in; a protected one confirms Deploy. */
  env?: EnvLike;
  canDeploy: boolean;
  grafanaUrl?: string;
  now: number;
}) {
  return (
    <table aria-label={tableLabel} className="w-full border-collapse text-[13px] max-sm:block sm:table-fixed">
      <thead className="max-sm:hidden">
        <tr>
          <th scope="col" className={svcTh}>Service</th>
          <th scope="col" className={cn(svcTh, "w-[150px]")}>Status</th>
          <th scope="col" className={cn(svcTh, "w-[210px]")}>Last deploy</th>
          <th scope="col" className={cn(svcTh, "w-[124px]")}><span className="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody className="max-sm:block">
        {services.map((s) => {
          const o = byService?.get(s.id);
          const last = o?.last_deployment;
          const src = sourceOf(s);
          return (
            <tr key={s.id} className={svcRow}>
              <td className={cn(svcTd, "max-sm:col-start-1 max-sm:row-start-1")}>
                <span className="flex min-w-0 items-start gap-2.5">
                  <ServiceMark slug={s.addon_slug} />
                  <span className="flex min-w-0 flex-col gap-0.5">
                    {/* On phones the name's link covers the card, under the reach link and the actions. */}
                    <Link href={serviceHref(s.project_id, s.id)} className="font-medium text-fg [overflow-wrap:anywhere] hover:underline max-sm:after:absolute max-sm:after:inset-0 max-sm:after:content-['']">
                      {s.name}
                    </Link>
                    <span className="text-xs text-fg/75">
                      {kindOf(s)} ·{" "}
                      <span className={cn(mono, "text-fg/80 [overflow-wrap:anywhere]")} title={s.source_type === "docker-image" ? s.docker_image : undefined}>
                        {src}
                      </span>
                    </span>
                    <Reach s={s} o={o} known={!!byService} />
                  </span>
                </span>
              </td>
              <td className={cn(svcTd, "whitespace-nowrap max-sm:col-start-2 max-sm:row-start-1 max-sm:text-right")}>
                <StatusText status={s.runtime_status} />
              </td>
              <td className={cn(svcTd, "whitespace-nowrap text-muted sm:truncate max-sm:col-start-1 max-sm:row-start-2 max-sm:self-center")}>
                {last ? (
                  <>
                    <span className={cn(mono, "mr-2 text-fg")} title={deploymentRefTitle(last)}>{deploymentRef(last) || "—"}</span>
                    {ago(last.finished_at ?? last.created_at, now)}
                  </>
                ) : byService ? (
                  "never"
                ) : (
                  "—"
                )}
              </td>
              <td className={cn(svcTd, "whitespace-nowrap py-1.5 text-right max-sm:relative max-sm:z-[1] max-sm:col-start-2 max-sm:row-start-2")}>
                <ServiceRowActions
                  service={s}
                  last={last}
                  serverName={inProse(serverName(s.server_id, servers)) || "its server"}
                  env={env}
                  domain={o?.domains[0]}
                  canDeploy={canDeploy}
                  grafanaUrl={grafanaUrl}
                />
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}
