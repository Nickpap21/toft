"use client";

import { Suspense, useMemo, useState } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Check, Clock, Copy, ExternalLink, Globe, Lock, Minus, MoreHorizontal, Plus, RotateCw, Trash2, XCircle } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import { domainService, type DomainCheck, type WorkspaceDomain } from "@/lib/api/domain.service";
import { dnsProviderService, type Server as ServerRecord } from "@/lib/api/server.service";
import type { WorkspaceService } from "@/lib/api/service.service";
import { projectsQuery, serversQuery, workspaceEnvironmentsQuery, workspaceServicesQuery } from "@/lib/queries";
import { isServing } from "@/lib/status";
import { sinceShort } from "@/lib/time";
import { cn, possessive } from "@/lib/utils";
import { useAuth, useIsOperator, useOrgRole } from "@/context/AuthContext";
import { useDocumentTitle } from "@/lib/use-document-title";
import { LoadFailed, Read, readFailed } from "@/components/shell/unreachable";
import { EnvChip } from "@/components/ui/env-chip";
import type { WorkspaceEnvironment } from "@/lib/api/environment.service";
import { DeployButton, useDomainsDeploy } from "./deploy";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { cancelButton, card, dangerButton, dialogContent, dialogHeader, dialogText, dialogTitle, fieldInput, primaryButton } from "@/components/settings/settings-frame";
import { TEMPORARY_NOTE, TemporaryChip, domainURL } from "@/components/ui/temporary-chip";
import { daysLeft, isWildcard, needOf, recordOf, RENEW_WARN_DAYS, wildcardLine, type Fact, type Need } from "./domains-view";

// Domains (board: private/design/canvas/ReworkDomains.dc.html, D-45, D-48): every
// hostname the workspace serves, with where its DNS points, its certificate
// and whether its server routes it, checked from the panel per service.

const h2 = "text-[15px] font-semibold leading-[22px] text-fg";
const th = "px-4 py-2.5 text-left text-xs font-medium text-muted";
const PANEL = "";

export default function DomainsPage() {
  return (
    <Suspense>
      <Domains />
    </Suspense>
  );
}

function Domains() {
  const { user } = useAuth();
  const role = useOrgRole();
  const canEdit = role === "owner" || role === "developer";
  const ws = user?.orgName ?? "this workspace";
  const params = useSearchParams();
  const queryClient = useQueryClient();
  const [q, setQ] = useState("");
  const [project, setProject] = useState("all");
  const [adding, setAdding] = useState(params.has("add"));
  // Add and deploy's deploy, which outlives the add dialog.
  const addDeploy = useDomainsDeploy();
  const [removing, setRemoving] = useState<WorkspaceDomain | null>(null);
  const [now] = useState(() => Date.now());

  useDocumentTitle(`Domains · ${user?.orgName ? `${user.orgName} · ` : ""}Toft`);

  const domainsRead = useQuery({ queryKey: ["workspace-domains"], queryFn: () => domainService.listWorkspace() });
  const domains = domainsRead.data;
  const servicesRead = useQuery(workspaceServicesQuery);
  const services = servicesRead.data;
  const serversRead = useQuery(serversQuery);
  const servers = serversRead.data;
  const { data: environments } = useQuery(workspaceEnvironmentsQuery);
  const operator = useIsOperator();

  // One check per service with domains; rows fill in as each answers.
  const serviceIds = useMemo(() => [...new Set((domains ?? []).map((d) => d.service_id))], [domains]);
  const checks = useQueries({
    queries: serviceIds.map((id) => ({ queryKey: ["domain-check", id], queryFn: () => domainService.check(id), staleTime: 60_000, retry: false })),
  });
  const checkOf = new Map<string, { data?: DomainCheck; loading: boolean; at: number; refetch: () => void; failed: boolean; error: unknown }>();
  serviceIds.forEach((id, i) => checkOf.set(id, { data: checks[i].data, loading: checks[i].isFetching, at: checks[i].dataUpdatedAt, refetch: () => checks[i].refetch(), failed: readFailed(checks[i]), error: checks[i].error }));
  const failedChecks = serviceIds.filter((id) => checkOf.get(id)?.failed);
  const checkedAt = Math.min(...checks.filter((c) => c.dataUpdatedAt).map((c) => c.dataUpdatedAt));

  const svcOf = new Map((services ?? []).map((s) => [s.id, s]));
  const serverName = (id: string) => (id === PANEL ? "the panel host" : servers?.find((s) => s.id === id)?.name ?? "a server");
  const factOf = (d: WorkspaceDomain): Fact | undefined => checkOf.get(d.service_id)?.data?.domains.find((x) => x.id === d.id);
  // Routed or not is known only once the running container was read.
  const running = (d: WorkspaceDomain) => isServing(svcOf.get(d.service_id)?.runtime_status) && !!checkOf.get(d.service_id)?.data?.routing_known;

  const rows = (domains ?? []).map((d) => {
    const c = checkOf.get(d.service_id);
    return { d, f: factOf(d), server: c?.data?.server, loading: !!c?.loading && !c?.data, need: needOf(d, factOf(d), c?.data?.server, running(d), serverName(d.server_id), now) };
  });
  const needs = rows.filter((r) => r.need);
  const groupKey = (d: WorkspaceDomain) => d.project_id || "workspace";
  const projects = [...new Map((domains ?? []).map((d) => [groupKey(d), d.project_name || "Workspace services"])).entries()];
  const visible = rows.filter(
    (r) =>
      (project === "all" || groupKey(r.d) === project) &&
      (!q.trim() || `${r.d.hostname} ${r.d.service_name} ${r.d.project_name}`.toLowerCase().includes(q.trim().toLowerCase())),
  );
  const groups = projects
    .map(([key, name]) => ({ key, name, rows: visible.filter((r) => groupKey(r.d) === key) }))
    .filter((g) => g.rows.length > 0);
  const empty = domains?.length === 0;

  const waiting = needs.filter((r) => r.f?.dns === "none").length;
  const onlyDns = waiting === needs.length;
  const envOf = (d: WorkspaceDomain) => environments?.find((e) => e.project_id === d.project_id && e.name === d.environment_name);
  const checked = Number.isFinite(checkedAt) ? sinceShort(new Date(checkedAt).toISOString(), Math.max(now, checkedAt)) : "";
  const cell = "border-t border-subtle px-4 py-2.5 align-middle max-sm:block max-sm:border-0 max-sm:p-0";

  return (
    <div className="flex w-full max-w-[1600px] flex-col gap-5 pb-14 max-sm:gap-4 md:-mt-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 flex-wrap items-center gap-x-3 max-sm:flex-col max-sm:items-start max-sm:gap-0.5">
          <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">Domains</h1>
          <span className="text-[13px] text-muted">
            {domains
              ? domains.length
                ? [`${domains.length} in ${ws}`, needs.length > 0 && (onlyDns ? `${waiting} waiting for DNS` : `${needs.length} ${needs.length === 1 ? "needs" : "need"} you`)].filter(Boolean).join(" · ")
                : `None in ${ws} yet`
              : " "}
          </span>
        </div>
        {canEdit && !empty && (
          <button type="button" className={cn(primaryButton, "max-sm:w-full max-sm:justify-center")} onClick={() => setAdding(true)}>
            <Plus className="h-[15px] w-[15px]" aria-hidden="true" />
            Add domain
          </button>
        )}
      </div>

      <div className="grid items-start gap-6 max-lg:grid-cols-1 max-sm:gap-4 lg:grid-cols-[minmax(0,1040px)_minmax(320px,1fr)]">
        <div className="flex min-w-0 flex-col gap-5 max-sm:gap-4">
          {readFailed(servicesRead) && (
            <section className={cn(card, "px-4 py-3")}>
              <LoadFailed what="the services" onRetry={() => void servicesRead.refetch()} />
            </section>
          )}
          {failedChecks.length > 0 && (
            <section className={cn(card, "flex flex-col gap-2 px-4 py-3")}>
              {failedChecks.map((id) => (
                <LoadFailed key={id} what={`the DNS check of ${domains?.find((d) => d.service_id === id)?.service_name ?? "a service"}`} onRetry={() => checkOf.get(id)?.refetch()} error={checkOf.get(id)?.error} />
              ))}
            </section>
          )}
          <Read
            query={domainsRead}
            what="the domains"
            heading="h2"
            loading={
              <div className="flex flex-col gap-5 motion-safe:animate-pulse" aria-label="Loading domains">
                <div className="h-40 rounded-[10px] bg-surface" />
                <div className="h-72 rounded-[10px] bg-surface" />
              </div>
            }
            isEmpty={(list) => list.length === 0}
            empty={
            <section aria-labelledby="empty-h" className={cn(card, "flex max-w-[640px] flex-col items-start gap-3 p-8 max-sm:p-5")}>
              <h2 id="empty-h" className="text-[17px] font-semibold text-fg">No domains yet</h2>
              <p className="text-sm leading-[21px] text-fg/75">
                A web app gets a temporary address under sslip.io when it’s created: {TEMPORARY_NOTE.charAt(0).toLowerCase()}
                {TEMPORARY_NOTE.slice(1)} Add your own: point its DNS record at the service’s server, and from the service’s next deploy that server routes it
                and gets a certificate from Let’s Encrypt.
              </p>
              {canEdit && (
                <button type="button" className={primaryButton} onClick={() => setAdding(true)}>
                  <Plus className="h-[15px] w-[15px]" aria-hidden="true" />
                  Add domain
                </button>
              )}
            </section>
            }
          >
            {(domains) => (
            <>
              {needs.length > 0 && (
                <section aria-labelledby="needs-h" className={card}>
                  <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 border-b border-subtle px-4 py-3">
                    <h2 id="needs-h" className={h2}>{onlyDns ? "Waiting for DNS" : "Needs you"}</h2>
                    <span className="text-xs text-muted">{needs.length}</span>
                  </div>
                  {needs.map((r, i) => (
                    <NeedRow
                      key={r.d.id}
                      d={r.d}
                      need={r.need!}
                      address={r.server?.address ?? ""}
                      canEdit={canEdit}
                      checking={!!checkOf.get(r.d.service_id)?.loading}
                      checked={checkOf.get(r.d.service_id)?.at ?? 0}
                      now={now}
                      first={i === 0}
                      onCheck={() => checkOf.get(r.d.service_id)?.refetch()}
                      service={svcOf.get(r.d.service_id)}
                    />
                  ))}
                </section>
              )}

              <section aria-labelledby="all-h" className={card}>
                <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 border-b border-subtle px-4 py-3">
                  <h2 id="all-h" className={h2}>All domains</h2>
                  <span className="text-xs text-muted">
                    {domains.length}
                    {checked && failedChecks.length === 0 && ` · DNS checked ${checked}`}
                  </span>
                </div>
                <form role="search" aria-label="Filter domains" className="flex flex-wrap items-center gap-2 border-b border-subtle px-4 py-3" onSubmit={(e) => e.preventDefault()}>
                  <input
                    type="search"
                    value={q}
                    onChange={(e) => setQ(e.target.value)}
                    placeholder="Search domains"
                    aria-label="Search domains"
                    className={cn(fieldInput, "h-8 max-w-[320px] flex-[1_1_220px] text-[13px] max-sm:h-11 max-sm:max-w-none")}
                  />
                  <select
                    value={project}
                    onChange={(e) => setProject(e.target.value)}
                    aria-label="Project"
                    className={cn(fieldInput, "h-8 flex-[0_1_180px] px-2 text-[13px] max-sm:h-11")}
                  >
                    <option value="all">All projects</option>
                    {projects.map(([key, name]) => (
                      <option key={key} value={key}>{name}</option>
                    ))}
                  </select>
                </form>

                <table aria-label={`Domains of ${ws}`} className="w-full border-collapse text-[13px] max-sm:block sm:table-fixed">
                  <thead className="max-sm:hidden">
                    <tr>
                      <th scope="col" className={cn(th, "w-[31%] border-b border-subtle py-2")}>Domain</th>
                      <th scope="col" className={cn(th, "w-[25%] border-b border-subtle py-2")}>Routes to</th>
                      <th scope="col" className={cn(th, "border-b border-subtle py-2")}>DNS</th>
                      <th scope="col" className={cn(th, "border-b border-subtle py-2")}>Certificate</th>
                      <th scope="col" className="w-14 border-b border-subtle py-2"><span className="sr-only">Actions</span></th>
                    </tr>
                  </thead>
                  {groups.map((g, gi) => (
                    <tbody key={g.key} className="max-sm:block">
                      <tr className="max-sm:block">
                        <th
                          scope="rowgroup"
                          colSpan={5}
                          className={cn("px-4 pb-1.5 pt-4 text-left text-[13px] font-semibold text-fg max-sm:block max-sm:pb-1", gi > 0 && "border-t border-subtle")}
                        >
                          {g.name}
                          <span className="ml-2 text-xs font-normal text-muted">{g.rows.length}</span>
                        </th>
                      </tr>
                      {g.rows.map((r, i) => (
                        <DomainRow
                          key={r.d.id}
                          d={r.d}
                          f={r.f}
                          server={r.server}
                          env={envOf(r.d)}
                          loading={r.loading}
                          running={services ? running(r.d) : undefined}
                          cell={cn(cell, i === 0 && "border-t-0")}
                          divided={i > 0}
                          canEdit={canEdit}
                          now={now}
                          onCheck={() => checkOf.get(r.d.service_id)?.refetch()}
                          onRemove={() => setRemoving(r.d)}
                        />
                      ))}
                    </tbody>
                  ))}
                  {groups.length === 0 && (
                    <tbody className="max-sm:block">
                      <tr className="max-sm:block">
                        <td colSpan={5} className="px-4 py-3 text-muted max-sm:block">No domain matches.</td>
                      </tr>
                    </tbody>
                  )}
                </table>
                <p className="border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
                  A name is routed by its service’s server from the service’s next deploy. On one server, an exact name wins over a wildcard.
                </p>
              </section>
            </>
            )}
          </Read>
        </div>

        <aside className="flex min-w-0 flex-col gap-5 max-sm:gap-4">
          {readFailed(serversRead) ? (
            <section className={cn(card, "px-4 py-3")}>
              <LoadFailed what="the servers" onRetry={() => void serversRead.refetch()} />
            </section>
          ) : (
            <ServerList servers={servers ?? []} domains={domains} checks={[...checkOf.values()].map((c) => c.data).filter(Boolean) as DomainCheck[]} />
          )}
          <section aria-labelledby="else-h" className={card}>
            <div className="border-b border-subtle px-4 py-3">
              <h2 id="else-h" className={h2}>Elsewhere</h2>
            </div>
            <dl className="text-[13px]">
              <div className="grid grid-cols-[110px_minmax(0,1fr)] gap-3 px-4 py-2 max-sm:grid-cols-1 max-sm:gap-0.5">
                <dt className="text-muted">The panel</dt>
                <dd className="text-fg">
                  Its own address is set in{" "}
                  {operator ? <Link href="/instance" className="inline-link">Instance</Link> : "Instance, by the instance’s operators"}.
                </dd>
              </div>
              <div className="grid grid-cols-[110px_minmax(0,1fr)] gap-3 border-t border-subtle px-4 py-2 max-sm:grid-cols-1 max-sm:gap-0.5">
                <dt className="text-muted">Previews</dt>
                <dd className="text-fg">One wildcard per project, set in its Settings → Previews.</dd>
              </div>
            </dl>
          </section>
        </aside>
      </div>

      {addDeploy.dialogs}
      {adding && (
        <AddDomain
          services={(services ?? []).filter((s) => s.port > 0)}
          servicesRead={servicesRead}
          servers={servers ?? []}
          onClose={() => setAdding(false)}
          onAdded={(svc, deploy) => {
            queryClient.invalidateQueries({ queryKey: ["workspace-domains"] });
            queryClient.invalidateQueries({ queryKey: ["domain-check", svc.id] });
            queryClient.invalidateQueries({ queryKey: ["domains", svc.id] });
            setAdding(false);
            if (deploy) void addDeploy.start(svc);
          }}
        />
      )}
      <RemoveDomain
        d={removing}
        serverName={removing ? serverName(removing.server_id) : ""}
        onClose={() => setRemoving(null)}
        onRemoved={(d) => {
          queryClient.invalidateQueries({ queryKey: ["workspace-domains"] });
          queryClient.invalidateQueries({ queryKey: ["domain-check", d.service_id] });
          queryClient.invalidateQueries({ queryKey: ["domains", d.service_id] });
          setRemoving(null);
        }}
      />
    </div>
  );
}

// ─── Waiting for DNS, and what else needs someone ────────────────────────────

const NEED_TEXT = { failed: "text-status-failed-fg", pending: "text-status-pending-fg" };
const smallButton = "touch-target inline-flex h-[30px] items-center rounded-md border border-strong bg-raised px-2.5 text-xs font-medium text-fg hover:bg-raised/70 disabled:opacity-50";
const ghostButton = "touch-target inline-flex h-[30px] items-center rounded-md px-2.5 text-xs font-medium text-fg/80 hover:bg-raised hover:text-fg disabled:opacity-50";
const copyRecord = (host: string, address: string) =>
  navigator.clipboard?.writeText(recordOf(host, address).replace(/ {2}/g, " ")).then(() => toast.success("Record copied"));

/** The record a domain needs, as the board draws it: name, A, address. */
function Record({ host, address, copy }: { host: string; address: string; copy?: boolean }) {
  return (
    <div className="flex items-center gap-2 rounded-lg border border-subtle bg-ground px-3 py-2 font-mono text-xs text-fg">
      <span className="flex min-w-0 flex-1 flex-wrap items-center gap-2 [overflow-wrap:anywhere]">
        <span>{host}</span>
        <span className="text-muted">A</span>
        <span>{address || "<the server’s address>"}</span>
      </span>
      {copy && (
        <button type="button" aria-label={`Copy record for ${host}`} className={cn(smallButton, "font-sans")} onClick={() => copyRecord(host, address)}>
          Copy
        </button>
      )}
    </div>
  );
}

function NeedRow({
  d,
  need,
  address,
  canEdit,
  checking,
  checked,
  now,
  first,
  onCheck,
  service,
}: {
  d: WorkspaceDomain;
  need: Need;
  address: string;
  canEdit: boolean;
  checking: boolean;
  checked: number;
  now: number;
  first: boolean;
  onCheck: () => void;
  service?: WorkspaceService;
}) {
  const Icon = need.icon === "globe" ? Globe : need.icon === "clock" ? Clock : Lock;
  const record = need.primary !== "dns" && need.kind !== "Certificate doesn’t verify" && need.kind !== "Certificate isn’t renewing" && !d.temporary;
  return (
    <div className={cn("flex gap-3 px-4 py-3.5", !first && "border-t border-subtle")}>
      <Icon className={cn("mt-0.5 h-4 w-4 flex-none", NEED_TEXT[need.tone])} aria-hidden="true" />
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <div className="[overflow-wrap:anywhere]">
          <span className="font-mono text-[13px] text-fg">{d.hostname}</span> <span className="text-xs text-muted">→ {need.meta}</span>
        </div>
        <p className="max-w-[760px] text-[13px] leading-[19px] text-muted">
          <span className={cn("font-medium", NEED_TEXT[need.tone])}>{need.kind}.</span> {need.todo}
        </p>
        {record && <Record host={d.hostname} address={address} />}
        <div className="flex flex-wrap items-center gap-2">
          {record && (
            <button type="button" aria-label={`Copy record for ${d.hostname}`} className={smallButton} onClick={() => copyRecord(d.hostname, address)}>
              Copy record
            </button>
          )}
          {need.primary === "dns" ? (
            <Link href={`/servers/${d.server_id}#dns-h`} className={smallButton}>DNS provider</Link>
          ) : need.primary === "deploy" && canEdit && service ? (
            <DeployButton service={service} className={smallButton} />
          ) : null}
          <button type="button" aria-label={`${checking ? "Checking…" : "Check again"}: ${d.hostname}`} className={ghostButton} disabled={checking} onClick={onCheck}>
            {checking ? "Checking…" : "Check again"}
          </button>
          {checked > 0 && <span className="text-xs text-muted">Checked {sinceShort(new Date(checked).toISOString(), Math.max(now, checked))}</span>}
        </div>
      </div>
    </div>
  );
}

// ─── The table ───────────────────────────────────────────────────────────────

/** A state with its icon, as the board's DNS and Certificate cells. */
function Cell({ icon, tone, text, sub }: { icon: React.ReactNode; tone: string; text: string; sub?: React.ReactNode }) {
  return (
    <span className="flex flex-col gap-0.5">
      <span className={cn("inline-flex items-center gap-1.5 whitespace-nowrap [&>svg]:shrink-0", tone)}>
        {icon}
        {text}
      </span>
      {sub && <span className="text-xs leading-[18px] text-muted">{sub}</span>}
    </span>
  );
}

const ok = <Check className="h-3.5 w-3.5 text-status-running" aria-hidden="true" />;
const clock = <Clock className="h-3.5 w-3.5" aria-hidden="true" />;
const warn = <AlertTriangle className="h-3.5 w-3.5" aria-hidden="true" />;
const bad = <XCircle className="h-3.5 w-3.5" aria-hidden="true" />;
const dash = <Minus className="h-3.5 w-3.5 text-muted" aria-hidden="true" />;

function DomainRow({
  d,
  f,
  server,
  env,
  loading,
  running,
  cell,
  divided,
  canEdit,
  now,
  onCheck,
  onRemove,
}: {
  d: WorkspaceDomain;
  f?: Fact;
  server?: DomainCheck["server"];
  env?: WorkspaceEnvironment;
  loading: boolean;
  /** Undefined when the service's state wasn't read. */
  running?: boolean;
  cell: string;
  divided: boolean;
  canEdit: boolean;
  now: number;
  onCheck: () => void;
  onRemove: () => void;
}) {
  const wild = isWildcard(d.hostname);
  const addr = server?.address ?? "";
  const routed = !!f?.routed;
  const pending = <span className="text-xs text-muted motion-safe:animate-pulse">Checking…</span>;
  const href = `${d.scope === "org" || !d.project_id ? `/shared/${d.service_id}` : `/projects/${d.project_id}/services/${d.service_id}`}`;
  const elsewhere = f?.dns === "elsewhere" ? f.addresses.filter((a) => !a.includes(":")) : [];

  const dns = !f ? null : f.dns === "here" ? (
    <Cell icon={ok} tone="text-fg/80" text={d.temporary ? "sslip.io" : "Points here"} />
  ) : f.dns === "elsewhere" ? (
    <Cell icon={bad} tone="text-status-failed-fg" text="Points elsewhere" sub={<span className="font-mono">A {elsewhere.join(", ") || f.addresses.join(", ")}</span>} />
  ) : f.dns === "none" ? (
    <Cell icon={clock} tone="text-status-pending-fg" text="No record yet" />
  ) : (
    <Cell icon={dash} tone="text-fg/80" text={f.addresses.length ? "Can’t compare" : "Lookup failed"} sub={f.addresses.length ? <span className="font-mono">A {f.addresses.join(", ")}</span> : "try Check again"} />
  );

  const left = f?.not_after ? daysLeft(f.not_after, now) : null;
  const how = wild ? `over DNS${server?.dns_provider ? `, ${server.dns_provider}` : ""}` : f?.issuer || "Let’s Encrypt";
  const cert = !f ? null : f.certificate === "valid" ? (
    left !== null && left < RENEW_WARN_DAYS ? (
      <Cell icon={warn} tone="text-status-pending-fg" text={`Expires ${f.not_after!.slice(0, 10)}`} sub={`in ${left} days · renewal failing`} />
    ) : (
      <Cell icon={ok} tone="text-fg" text={`Until ${f.not_after?.slice(0, 10)}`} sub={how} />
    )
  ) : f.certificate === "invalid" ? (
    <Cell icon={bad} tone="text-status-failed-fg" text={left !== null && left < 0 ? "Expired" : "Doesn’t verify"} sub={f.not_after ? `expires ${f.not_after.slice(0, 10)}` : how} />
  ) : f.certificate === "unknown" ? (
    <Cell icon={dash} tone="text-fg/80" text="Couldn’t read" sub={addr ? "the server didn’t answer on 443" : "the server’s address is unknown"} />
  ) : running && routed && d.temporary ? (
    <Cell icon={clock} tone="text-status-pending-fg" text="Pending" sub="answers over HTTP meanwhile" />
  ) : (
    <Cell icon={dash} tone="text-fg/80" text="Not yet" sub={running === undefined ? undefined : !running ? "the service isn’t running" : routed ? "check the DNS" : "after DNS and a deploy"} />
  );

  return (
    <tr className={cn("max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-1.5 max-sm:px-4 max-sm:py-3", divided && "max-sm:border-t max-sm:border-subtle")}>
      <td className={cn(cell, "max-sm:col-start-1 max-sm:row-start-1 max-sm:self-center")}>
        {!wild && routed ? (
          <a href={domainURL(d)} target="_blank" rel="noreferrer" className="font-mono text-xs text-fg [overflow-wrap:anywhere] hover:underline">
            {d.hostname}
            <span className="sr-only">, opens in a new tab</span>
          </a>
        ) : (
          <span className="font-mono text-xs text-fg [overflow-wrap:anywhere]">{d.hostname}</span>
        )}
        {wild && <span className="ml-1.5 whitespace-nowrap rounded bg-raised px-[5px] text-[11px] text-fg/80">wildcard</span>}
        {d.temporary && <TemporaryChip className="ml-1.5" />}
      </td>
      <td className={cn(cell, "max-sm:col-span-2 max-sm:row-start-2")}>
        <span className="flex flex-wrap items-center gap-1.5">
          <Link href={href} className="text-fg hover:underline">{d.service_name}</Link>
          {env ? (
            <EnvChip env={env} size="md" className="max-w-full overflow-hidden" />
          ) : (
            <span className="text-muted">{d.environment_name || (d.scope === "org" ? "workspace" : "project service")}</span>
          )}
        </span>
      </td>
      <td className={cn(cell, "max-sm:col-start-1 max-sm:row-start-3")}>{loading ? pending : dns}</td>
      <td className={cn(cell, "max-sm:col-start-2 max-sm:row-start-3 max-sm:text-right")}>{loading ? pending : cert}</td>
      <td className={cn(cell, "py-1 text-right max-sm:col-start-2 max-sm:row-start-1")}>
        <DropdownMenu>
          <DropdownMenuTrigger
            aria-label={`More for ${d.hostname}`}
            className="touch-target inline-flex h-9 w-9 items-center justify-center rounded-lg text-fg/70 hover:bg-raised hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus data-[state=open]:bg-raised max-sm:h-11 max-sm:w-11"
          >
            <MoreHorizontal className="h-4 w-4" aria-hidden="true" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-[212px]">
            {!wild && (
              <DropdownMenuItem asChild>
                <a href={domainURL(d)} target="_blank" rel="noreferrer">
                  <ExternalLink className="h-3.5 w-3.5" aria-hidden="true" />
                  Open {d.hostname}
                </a>
              </DropdownMenuItem>
            )}
            {!d.temporary && (
              <DropdownMenuItem onSelect={() => copyRecord(d.hostname, addr)}>
                <Copy className="h-3.5 w-3.5" aria-hidden="true" />
                Copy the DNS record
              </DropdownMenuItem>
            )}
            <DropdownMenuItem onSelect={onCheck}>
              <RotateCw className="h-3.5 w-3.5" aria-hidden="true" />
              Check DNS again
            </DropdownMenuItem>
            {canEdit && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem onSelect={onRemove} className="text-status-failed-fg focus:text-status-failed-fg">
                  <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                  Remove…
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </td>
    </tr>
  );
}

// ─── Certificates by server ──────────────────────────────────────────────────

function ServerList({ servers, domains, checks }: { servers: ServerRecord[]; domains: WorkspaceDomain[] | undefined; checks: DomainCheck[] }) {
  const own = servers.filter((s) => !s.shared);
  const shared = servers.filter((s) => s.shared && domains?.some((d) => d.server_id === s.id));
  const panelDomains = (domains ?? []).filter((d) => d.server_id === PANEL).length;
  const panelAddress = checks.find((c) => c.server.name === "the panel host")?.server.address ?? "";
  // Unknown, not none, until the domains were read.
  const count = (id: string) => domains?.filter((d) => d.server_id === id).length;
  return (
    <section aria-labelledby="srv-h" className={card}>
      <div className="border-b border-subtle px-4 py-3">
        <h2 id="srv-h" className={h2}>Certificates by server</h2>
      </div>
      <ul className="flex flex-col pb-2 pt-1">
        {own.map((s) => (
          <OwnServerRow key={s.id} server={s} count={count(s.id)} />
        ))}
        {shared.map((s) => {
          const c = checks.find((x) => x.server.name === s.name);
          return (
            <ServerRow
              key={s.id}
              name={s.name}
              facts={[domainsLine(count(s.id), c?.server.address ?? ""), `Shared by ${s.shared_by}`]}
              wild={{ text: c?.server.wildcards_ok ? "Wildcards over DNS" : "Wildcards only if its owner sets a DNS provider", warn: false }}
            />
          );
        })}
        {panelDomains > 0 && (
          <ServerRow name="panel host" facts={[domainsLine(panelDomains, panelAddress)]} wild={{ text: wildcardLine(true, null).text, warn: false }} />
        )}
      </ul>
      <p className="border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
        Exact names: Let’s Encrypt over HTTP, so DNS must point at the server first. Wildcards: over DNS, through the server’s DNS provider.
      </p>
    </section>
  );
}

const domainsLine = (n: number | undefined, addr: string) => [n === undefined ? "—" : n === 0 ? "No domains" : n === 1 ? "1 domain" : `${n} domains`, addr].filter(Boolean).join(" · ");

function OwnServerRow({ server, count }: { server: ServerRecord; count: number | undefined }) {
  const { data: dns, error } = useQuery({ queryKey: ["dns-provider", server.id], queryFn: () => dnsProviderService.get(server.id), retry: false });
  const none = error instanceof ApiError && error.status === 404;
  const known = !!dns || none;
  return (
    <ServerRow
      name={server.name}
      href={`/servers/${server.id}`}
      facts={[domainsLine(count, server.ip_address)]}
      wild={known ? (dns?.provider ? { text: `Wildcards through ${dns.provider}`, warn: false } : { text: wildcardLine(false, null).text, warn: true }) : undefined}
      link={known ? { href: `/servers/${server.id}#dns-h`, text: dns ? "DNS provider" : "Set one" } : undefined}
    />
  );
}

function ServerRow({
  name,
  href,
  facts,
  wild,
  link,
}: {
  name: string;
  href?: string;
  facts: string[];
  wild?: { text: string; warn: boolean };
  link?: { href: string; text: string };
}) {
  return (
    <li className="flex items-start gap-2.5 px-4 py-1 text-[13px]">
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        {href ? (
          <Link href={href} className="self-start font-mono text-[13px] text-fg hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center">{name}</Link>
        ) : (
          <span className="font-mono text-[13px] text-fg">{name}</span>
        )}
        {facts.map((x) => (
          <span key={x} className="text-xs leading-[18px] text-muted">{x}</span>
        ))}
        {wild && <span className={cn("text-xs leading-[18px]", wild.warn ? "text-status-pending-fg" : "text-muted")}>{wild.text}</span>}
      </span>
      {link && <Link href={link.href} className="text-[13px] text-fg hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center">{link.text}</Link>}
    </li>
  );
}

// ─── Add and remove ──────────────────────────────────────────────────────────

function AddDomain({
  services,
  servicesRead,
  servers,
  onClose,
  onAdded,
}: {
  services: WorkspaceService[];
  servicesRead: { data: unknown; error: unknown; refetch: () => unknown };
  servers: ServerRecord[];
  onClose: () => void;
  /** deploy: Add and deploy (D-145): the service deploys once it is added. */
  onAdded: (service: WorkspaceService, deploy: boolean) => void;
}) {
  const { data: projects } = useQuery(projectsQuery);
  const { data: envs } = useQuery(workspaceEnvironmentsQuery);
  // Opened from ?add, the services may load after the dialog: the first
  // one stands until the person picks another.
  const [picked, setServiceId] = useState("");
  const serviceId = picked || services[0]?.id || "";
  const [host, setHost] = useState("");
  const { data: check } = useQuery({ queryKey: ["domain-check", serviceId], queryFn: () => domainService.check(serviceId), enabled: !!serviceId, staleTime: 60_000, retry: false });
  const svc = services.find((s) => s.id === serviceId);
  const name = host.trim().toLowerCase();
  const wild = isWildcard(name);
  const serverName = svc?.server_id ? servers.find((s) => s.id === svc.server_id)?.name ?? "its server" : "the panel host";
  const addr = check?.server.address ?? "";
  const blocked = wild && check && !check.server.wildcards_ok;
  // The variable: Add and deploy, which deploys once it is added.
  const add = useMutation<unknown, Error, boolean>({
    mutationFn: () => domainService.create(serviceId, name),
    onSuccess: (_d, deploy) => {
      toast.success(deploy ? `${name} added` : `${name} added: ${possessive(svc?.name ?? "the service")} next deploy routes it`);
      if (svc) onAdded(svc, deploy);
    },
  });
  const confirms = !!svc?.environment_id && !!envs?.find((e) => e.id === svc.environment_id)?.protected;
  const projectName = (id: string) => projects?.find((p) => p.id === id)?.name ?? "";
  const envName = (id: string) => envs?.find((e) => e.id === id)?.name ?? "";
  const groups = [...new Map(services.map((s) => [s.project_id || "workspace", s.project_id ? projectName(s.project_id) : "Workspace services"])).entries()];

  return (
    <Dialog open onOpenChange={(o) => !o && !add.isPending && onClose()}>
      <DialogContent className="flex flex-col overflow-hidden p-0 sm:top-16 sm:max-h-[calc(100dvh-96px)] sm:translate-y-0 max-sm:inset-0 max-sm:h-full max-sm:max-h-none max-sm:w-full max-sm:max-w-none max-sm:translate-x-0 max-sm:translate-y-0 max-sm:rounded-none max-sm:border-0 rounded-xl sm:max-w-[560px]">
        <form
          className="flex min-h-0 flex-1 flex-col"
          onSubmit={(e) => {
            e.preventDefault();
            if (serviceId && name && !blocked) add.mutate(true);
          }}
        >
          <div className="min-h-0 flex-1 overflow-y-auto pb-4">
          <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
            <DialogTitle className={dialogTitle}>Add a domain</DialogTitle>
            <DialogDescription className={dialogText}>The service’s server routes it from the service’s next deploy.</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-3.5 px-5 pt-4">
            {services.length === 0 ? (
              <Read query={servicesRead} what="the services" loading={<></>}>
                {() => <p className="text-[13px] text-fg/85">No service here has a port, so none can take a domain. Set a port in a service’s Settings first.</p>}
              </Read>
            ) : (
              <div className="flex flex-col gap-1.5">
                <label htmlFor="add-svc" className="text-[13px] font-medium text-fg">Service</label>
                <select id="add-svc" value={serviceId} onChange={(e) => setServiceId(e.target.value)} className={cn(fieldInput, "px-2")}>
                  {groups.map(([key, label]) => (
                    <optgroup key={key} label={label}>
                      {services
                        .filter((s) => (s.project_id || "workspace") === key)
                        .map((s) => (
                          <option key={s.id} value={s.id}>
                            {s.project_id ? `${projectName(s.project_id)} / ` : ""}
                            {s.name}
                            {s.environment_id ? ` · ${envName(s.environment_id)}` : ""}
                          </option>
                        ))}
                    </optgroup>
                  ))}
                </select>
                <span className="text-xs text-muted">
                  Runs on <span className="font-mono">{serverName}</span>
                  {addr && <> · <span className="font-mono">{addr}</span></>}. Services with no port, such as workers, take no domain.
                </span>
              </div>
            )}
            <div className="flex flex-col gap-1.5">
              <label htmlFor="add-host" className="text-[13px] font-medium text-fg">Hostname</label>
              <input
                id="add-host"
                value={host}
                onChange={(e) => setHost(e.target.value)}
                spellCheck={false}
                autoComplete="off"
                placeholder="app.example.com"
                className={cn(fieldInput, "font-mono")}
              />
              <span className="text-xs text-muted">
                An exact name, such as <span className="font-mono">app.example.com</span>, or a wildcard, <span className="font-mono">*.example.com</span>, which
                matches one label in front of it.
              </span>
            </div>
            {svc && (
              <div className="flex flex-col gap-2 rounded-lg border border-subtle bg-surface p-3">
                <span className="text-[13px] font-medium text-fg">Create this record at your DNS host</span>
                <Record host={name || "app.example.com"} address={addr} copy />
                <span className="text-xs leading-[18px] text-fg/70">
                  {wild
                    ? `One wildcard record covers every name under ${name.slice(2) || "it"} that has no record of its own.${check?.server.dns_provider ? ` The certificate comes over DNS, through ${check.server.dns_provider}.` : ""}`
                    : `Let’s Encrypt checks the name over HTTP when ${svc.name} next deploys, so the record has to point here by then. A CNAME to a name that already points here works too.`}
                </span>
              </div>
            )}
            {confirms && svc && (
              <p className="flex items-center gap-1.5 text-xs text-muted">
                <Lock className="h-3 w-3 flex-none" aria-hidden="true" />
                {envName(svc.environment_id)} is protected: deploying {svc.name} asks you to confirm, as every deploy there does.
              </p>
            )}
            {blocked && (
              <div role="alert" className="flex gap-2.5 rounded-lg border border-status-pending/40 bg-status-pending-soft/40 p-3">
                <AlertTriangle className="mt-0.5 h-4 w-4 flex-none text-status-pending-fg" aria-hidden="true" />
                <div className="flex flex-col gap-1 text-[13px] leading-[19px]">
                  <p className="font-semibold text-fg">{serverName} has no DNS provider</p>
                  <p className="text-fg/85">
                    A wildcard’s certificate needs a DNS check, so {possessive(svc?.name ?? "the service")} next deploy would be refused before anything changes. Set a DNS provider on{" "}
                    {serverName} first, or use a service on a server that has one.
                  </p>
                  {svc?.server_id && check?.server.owner && (
                    <Link href={`/servers/${svc.server_id}#dns-h`} className="self-start text-[13px] text-link hover:underline">
                      DNS provider on {serverName}
                    </Link>
                  )}
                </div>
              </div>
            )}
            {add.error && (
              <p role="alert" className="rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">
                {add.error instanceof ApiError && add.error.status < 500 ? add.error.message : "The domain was not added."}
              </p>
            )}
          </div>
          </div>
          <div className="flex flex-none flex-wrap justify-end gap-2 border-t border-subtle px-4 py-2.5 max-sm:pb-[calc(10px+env(safe-area-inset-bottom))]">
            <button type="button" className={cn(cancelButton, "justify-center max-sm:flex-1 sm:mr-auto")} onClick={onClose} disabled={add.isPending}>Cancel</button>
            <button type="button" className={cn(cancelButton, "justify-center max-sm:flex-1")} onClick={() => add.mutate(false)} disabled={!serviceId || !name || !!blocked || add.isPending}>
              Add only
            </button>
            <button type="submit" className={cn(primaryButton, "justify-center max-sm:order-first max-sm:basis-full")} disabled={!serviceId || !name || !!blocked || add.isPending}>
              {add.isPending ? "Adding…" : `Add and deploy ${svc?.name ?? "it"}${confirms ? "…" : ""}`}
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function RemoveDomain({
  d,
  serverName,
  onClose,
  onRemoved,
}: {
  d: WorkspaceDomain | null;
  serverName: string;
  onClose: () => void;
  onRemoved: (d: WorkspaceDomain) => void;
}) {
  const remove = useMutation({
    mutationFn: (x: WorkspaceDomain) => domainService.delete(x.service_id, x.id),
    onSuccess: (_, x) => {
      toast.success(`${x.hostname} removed: ${possessive(x.service_name)} next deploy stops routing it`);
      onRemoved(x);
      remove.reset();
    },
  });
  return (
    <Dialog open={!!d} onOpenChange={(o) => !o && !remove.isPending && (onClose(), remove.reset())}>
      <DialogContent role="alertdialog" className={dialogContent} showClose={false}>
        <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
          <DialogTitle className={dialogTitle}>
            Remove <span className="font-mono font-medium">{d?.hostname}</span>?
          </DialogTitle>
          <DialogDescription className={dialogText}>
            Traefik stops routing it at {possessive(d?.service_name ?? "the service")} next deploy. Until then requests to it still reach {d?.service_name}.
          </DialogDescription>
        </DialogHeader>
        <dl className="mx-5 mt-3.5 flex flex-col text-[13px]">
          <div className="flex gap-3 py-1.5">
            <dt className="w-[92px] flex-none text-muted">Service</dt>
            <dd className="text-fg/85">
              {d?.project_name ? `${d.project_name} / ` : ""}
              {d?.service_name} on <span className="font-mono text-xs">{serverName}</span>
            </dd>
          </div>
          <div className="flex gap-3 border-t border-subtle py-1.5">
            <dt className="w-[92px] flex-none text-muted">DNS record</dt>
            <dd className="text-fg/85">Stays at your DNS host. Delete it there if nothing else uses the name.</dd>
          </div>
        </dl>
        {remove.error && (
          <p role="alert" className="mx-5 mt-2 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">
            {remove.error instanceof ApiError && remove.error.status < 500 ? remove.error.message : "The domain was not removed."}
          </p>
        )}
        <div className="flex flex-wrap justify-end gap-2 px-5 pb-5 pt-[18px]">
          <button type="button" className={cancelButton} disabled={remove.isPending} onClick={onClose}>Cancel</button>
          <button type="button" className={dangerButton} disabled={remove.isPending || !d} onClick={() => d && remove.mutate(d)}>
            {remove.isPending ? "Removing…" : "Remove domain"}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
