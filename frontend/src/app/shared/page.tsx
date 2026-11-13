"use client";

import { Suspense, useState } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { Boxes, ExternalLink, Plus } from "lucide-react";
import { serviceHref } from "@/components/home/home";
import { TemporaryChip } from "@/components/ui/temporary-chip";
import { domainService } from "@/lib/api/domain.service";
import { observabilityService } from "@/lib/api/observability.service";
import { serviceService } from "@/lib/api/service.service";
import { connectTo } from "@/lib/network-alias";
import { useAuth, useOrgRole } from "@/context/AuthContext";
import {
  projectsQuery,
  serversQuery,
  workspaceEnvironmentsQuery,
  workspaceServicesQuery,
} from "@/lib/queries";
import { safeHref } from "@/lib/routes";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn, possessive } from "@/lib/utils";
import { inProse, serverName, sourceOf } from "../projects/_lib/projects-view";
import {
  ServiceRowActions,
  StatusText,
} from "../projects/_lib/service-actions";
import {
  ServiceMark,
  svcRow,
  svcTd,
  svcTh,
} from "../projects/_lib/service-table";
import {
  card,
  h1,
  h2,
  help,
  mono,
  outline,
  primary,
  sum,
} from "../projects/_lib/ui";
import { kv, LogsTab } from "./logs-tab";
import { stackOf } from "./observability-view";
import {
  liveFor,
  reachedFrom,
  serverPart,
  statusLine,
  whoReaches,
} from "./shared-view";

// Workspace services (board: ReworkSharedServices, D-147): services of the
// workspace rather than one project, which every project's services on the
// same server reach by alias (D-31). Observability is its Logs and metrics
// tab (?tab=logs, as a server's page does). "Created in <project>" is not
// recorded, so it is not shown (D-47).

const th = cn(svcTh, "leading-5");

export default function SharedServicesPage() {
  return (
    <Suspense>
      <SharedServices />
    </Suspense>
  );
}

function SharedServices() {
  const role = useOrgRole();
  const { user } = useAuth();
  const canManage = role === "owner" || role === "developer";
  const workspace = user?.orgName ?? "this workspace";
  useDocumentTitle(
    `Workspace services · ${user?.orgName ? `${user.orgName} · ` : ""}Toft`,
  );
  const tab = useSearchParams().get("tab") === "logs" ? "logs" : "services";
  const [now] = useState(() => Date.now());

  const {
    data: services,
    isLoading,
    error,
  } = useQuery({
    queryKey: ["org-scoped-services"],
    queryFn: () => serviceService.listOrgScoped(),
  });
  const { data: obs } = useQuery({
    queryKey: ["observability"],
    queryFn: () => observabilityService.getForOrg(),
  });
  const { data: everything, error: everythingError } = useQuery(
    workspaceServicesQuery,
  );
  const { data: environments, error: environmentsError } = useQuery(
    workspaceEnvironmentsQuery,
  );
  const { data: projects, error: projectsError } = useQuery(projectsQuery);
  const { data: servers, error: serversError } = useQuery(serversQuery);
  const { data: domains, error: domainsError } = useQuery({
    queryKey: ["workspace-domains"],
    queryFn: () => domainService.listWorkspace(),
  });

  const cfg = obs && "org_id" in obs ? obs : undefined;
  const grafanaUrl = safeHref(cfg?.grafana_url);
  const list = services ?? [];
  const has = list.length > 0;
  const attention = stackOf(cfg ?? {}, everything ?? []).filter(
    (s) => s.svc && s.svc.scope !== "org",
  ).length;
  const who = whoReaches(
    servers,
    everything ?? [],
    environments ?? [],
    projects ?? [],
    domains ?? [],
  );
  // What reaches a service is unknown until all five reads are in.
  const reachKnown = !!(everything && environments && projects && domains && servers);
  const reachError = reachKnown
    ? null
    : everythingError || environmentsError || projectsError || domainsError || serversError;
  const reachUnknown = reachError ? "Unknown" : "Reading…";
  const part = serverPart(list, servers);
  const tabClass = (on: boolean) =>
    cn(
      "-mb-px inline-flex h-[38px] items-center gap-1.5 border-b-2 px-3 text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus pointer-coarse:min-h-11",
      on ? "border-link text-fg" : "border-transparent text-fg/70 hover:text-fg",
    );

  return (
    <div className="flex w-full max-w-[1600px] flex-col gap-5 pb-14 max-sm:gap-4 md:-mt-3">
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 flex-wrap items-center gap-x-3 max-md:flex-col max-md:items-start max-md:gap-0.5">
            <h1 className={h1}>Workspace services</h1>
            <span className="text-[13px] text-muted">
              {!services
                ? "\u00a0"
                : has
                  ? [
                      list.length,
                      `${possessive(workspace)} own, outside any project`,
                      part,
                    ]
                      .filter(Boolean)
                      .join(" · ")
                  : `None in ${workspace} yet`}
            </span>
          </div>
          {has && (
            <div className="flex flex-wrap items-center gap-2 max-md:w-full">
              {grafanaUrl && (
                <a
                  href={grafanaUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className={cn(outline, "max-md:hidden")}
                >
                  Open Grafana
                  <ExternalLink className="h-3.5 w-3.5" aria-hidden="true" />
                  <span className="sr-only">, opens in a new tab</span>
                </a>
              )}
              {canManage && (
                <Link
                  href="/shared/new"
                  className={cn(
                    primary,
                    "max-md:h-11 max-md:flex-1 max-md:justify-center",
                  )}
                >
                  <Plus className="h-[15px] w-[15px]" aria-hidden="true" />
                  Add workspace service
                </Link>
              )}
            </div>
          )}
        </div>
        <nav
          aria-label="Workspace services"
          className="flex max-w-full gap-1 overflow-x-auto whitespace-nowrap border-b border-subtle"
        >
          <Link
            href="/shared"
            replace
            scroll={false}
            aria-current={tab === "services" ? "page" : undefined}
            className={tabClass(tab === "services")}
          >
            Services
            {has && (
              <span className="font-mono text-xs text-muted">
                {list.length}
              </span>
            )}
          </Link>
          <Link
            href="/shared?tab=logs"
            replace
            scroll={false}
            aria-current={tab === "logs" ? "page" : undefined}
            className={tabClass(tab === "logs")}
          >
            Logs and metrics
            {attention > 0 && (
              <span className="rounded-full bg-status-pending-soft px-1.5 font-mono text-xs font-semibold leading-[18px] text-status-pending-fg">
                {attention}
                <span className="sr-only">
                  {" "}
                  {attention === 1 ? "needs" : "need"} attention
                </span>
              </span>
            )}
          </Link>
        </nav>
      </div>

      {tab === "logs" ? (
        <LogsTab />
      ) : (
        <>
          {error && (
            <p
              role="alert"
              className="rounded-lg bg-status-failed-soft px-4 py-3 text-sm text-status-failed-fg"
            >
              Couldn&apos;t load the workspace services:{" "}
              {(error as Error).message}
            </p>
          )}
          {has && reachError && (
            <p
              role="alert"
              className="rounded-lg bg-status-failed-soft px-4 py-3 text-sm text-status-failed-fg"
            >
              Couldn&apos;t read what reaches these services:{" "}
              {reachError.message}
            </p>
          )}
          {isLoading ? (
            <div
              role="status"
              className="h-48 rounded-[10px] bg-surface motion-safe:animate-pulse"
              aria-label="Loading workspace services"
            />
          ) : !has && !error ? (
            <section
              aria-labelledby="empty-h"
              className={cn(card, "flex flex-wrap items-start gap-4 p-6")}
            >
              <span className="flex h-10 w-10 flex-none items-center justify-center rounded-lg border border-strong bg-raised text-fg/75">
                <Boxes className="h-[18px] w-[18px]" aria-hidden="true" />
              </span>
              <div className="flex min-w-0 flex-[1_1_360px] flex-col gap-1.5">
                <h2 id="empty-h" className={h2}>
                  No workspace services in {workspace}
                </h2>
                <p className="max-w-[620px] text-sm text-fg/75">
                  A workspace service runs once for the whole workspace, and
                  every project&apos;s services on the same server reach it by
                  alias: a shared Postgres, a mail catcher, or Loki, Prometheus
                  and Grafana for logs and metrics. You create one like any
                  service, with its scope set to Workspace.
                </p>
                {canManage && (
                  <div className="mt-2">
                    <Link href="/shared/new" className={primary}>
                      <Plus className="h-3.5 w-3.5" aria-hidden="true" />
                      Add workspace service
                    </Link>
                  </div>
                )}
              </div>
            </section>
          ) : has ? (
            <div className="grid items-start gap-6 max-sm:gap-4 min-[1200px]:grid-cols-[minmax(0,1040px)_minmax(320px,1fr)]">
              <section aria-labelledby="ws-h" className={cn(card, "min-w-0")}>
                <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5 border-b border-subtle px-4 py-3">
                  <h2 id="ws-h" className={h2}>
                    Services
                  </h2>
                  <span className={sum}>{statusLine(list)}</span>
                </div>
                <table
                  aria-label={`Workspace services of ${workspace}`}
                  className="w-full border-collapse text-[13px] leading-5 max-sm:block sm:table-fixed"
                >
                  <thead className="max-sm:hidden">
                    <tr>
                      <th scope="col" className={cn(th, "w-[34%]")}>
                        Service
                      </th>
                      <th scope="col" className={cn(th, "w-[15%]")}>
                        Status
                      </th>
                      <th scope="col" className={cn(th, "w-[24%]")}>
                        Reached as
                      </th>
                      <th scope="col" className={th}>
                        Reached from
                      </th>
                      <th scope="col" className={cn(th, "w-14")}>
                        <span className="sr-only">Actions</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody className="max-sm:block">
                    {list.map((s) => {
                      const where = serverName(s.server_id, servers);
                      const domain = domains?.find((d) => d.service_id === s.id);
                      const reach = reachedFrom(
                        s,
                        everything ?? [],
                        environments ?? [],
                        projects ?? [],
                        domains ?? [],
                      );
                      const live = liveFor(s.runtime_status, s.live_since, now);
                      return (
                        <tr key={s.id} className={cn(svcRow, "max-sm:gap-y-1")}>
                          <td
                            className={cn(
                              svcTd,
                              "max-sm:col-start-1 max-sm:row-start-1",
                            )}
                          >
                            <span className="flex min-w-0 items-start gap-2.5">
                              <ServiceMark
                                slug={s.addon_slug}
                                className="text-fg/75"
                              />
                              <span className="flex min-w-0 flex-col">
                                {/* On phones the name's link covers the row, under the menu. */}
                                <Link
                                  href={serviceHref("", s.id)}
                                  className="font-medium text-fg [overflow-wrap:anywhere] hover:underline max-sm:after:absolute max-sm:after:inset-0 max-sm:after:content-['']"
                                >
                                  {s.name}
                                </Link>
                                <span
                                  className={cn(
                                    mono,
                                    "truncate leading-[18px] text-muted",
                                  )}
                                  title={
                                    s.source_type === "docker-image"
                                      ? s.docker_image
                                      : undefined
                                  }
                                >
                                  {sourceOf(s)}
                                </span>
                              </span>
                            </span>
                          </td>
                          <td
                            className={cn(
                              svcTd,
                              "max-sm:col-start-1 max-sm:row-start-2",
                            )}
                          >
                            <StatusText status={s.runtime_status} />
                            {live && <div className={help}>{live}</div>}
                          </td>
                          <td
                            className={cn(
                              svcTd,
                              "max-sm:col-span-2 max-sm:row-start-3",
                            )}
                          >
                            <span
                              className={cn(
                                mono,
                                "block leading-5 text-fg [overflow-wrap:anywhere]",
                              )}
                            >
                              {s.connect_to?.port ? (
                                connectTo(s)
                              ) : (
                                <span className="font-sans text-muted">
                                  no alias
                                </span>
                              )}
                            </span>
                            <span
                              className={cn(
                                help,
                                "flex flex-wrap items-center gap-x-1.5 [overflow-wrap:anywhere]",
                              )}
                            >
                              {domain ? domain.hostname : "no address"}
                              {domain?.temporary && <TemporaryChip />}
                            </span>
                          </td>
                          <td
                            className={cn(
                              svcTd,
                              "text-fg max-sm:col-span-2 max-sm:row-start-4",
                            )}
                          >
                            {!reachKnown ? (
                              <span className="text-muted">{reachUnknown}</span>
                            ) : reach ? (
                              <>
                                {reach.by}
                                <div className={help}>{reach.via}</div>
                              </>
                            ) : (
                              <span className="text-status-pending-fg">
                                No project runs on{" "}
                                {inProse(where) || "its server"}
                              </span>
                            )}
                          </td>
                          <td
                            className={cn(
                              svcTd,
                              "py-1.5 text-right max-sm:relative max-sm:z-[1] max-sm:col-start-2 max-sm:row-start-1",
                            )}
                          >
                            <ServiceRowActions
                              service={s}
                              serverName={inProse(where) || "its server"}
                              canDeploy={canManage}
                              menuOnly
                            />
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
                <p className="border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
                  An alias answers only on its own server. A service elsewhere
                  reaches one through an address:{" "}
                  <Link href="/domains" className="inline-link">
                    Domains
                  </Link>
                  .
                </p>
              </section>

              <aside aria-label="Who reaches them" className="flex min-w-0 flex-col gap-5 max-sm:gap-4">
                <section aria-labelledby="who-h" className={card}>
                  <div className="border-b border-subtle px-4 py-3">
                    <h2 id="who-h" className={h2}>
                      Who reaches them
                    </h2>
                  </div>
                  {reachKnown && who.length > 0 ? (
                    <dl>
                      {who.map((r) => (
                        <div key={r.id} className={kv}>
                          <dt className={cn(mono, "leading-5 text-muted [overflow-wrap:anywhere]")}>{r.server}</dt>
                          <dd className="text-fg">{r.text}</dd>
                        </div>
                      ))}
                    </dl>
                  ) : (
                    <p className="px-4 py-2 text-[13px] text-muted">
                      {reachKnown
                        ? "No project's services run on a server yet."
                        : reachUnknown}
                    </p>
                  )}
                </section>
              </aside>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
