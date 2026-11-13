"use client";

import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ExternalLink } from "lucide-react";
import { toast } from "sonner";
import { useAuth, useIsOperator, useOrgRole } from "@/context/AuthContext";
import type { DashboardData, LiveDeploy } from "@/lib/api/dashboard.service";
import { domainService, type Domain } from "@/lib/api/domain.service";
import { panelHostQuery, panelStatusQuery, setupQuery } from "@/lib/api/instance.service";
import { orgService } from "@/lib/api/org.service";
import { backupPath, backupService } from "@/lib/api/server.service";
import type { WorkspaceService } from "@/lib/api/service.service";
import { useDeployLog } from "@/lib/hooks/useDeployLog";
import { serversQuery } from "@/lib/queries";
import { placeable } from "@/components/servers/agent";
import { PANEL_KEY } from "@/app/settings/backups/panel-host";
import { TemporaryChip } from "@/components/ui/temporary-chip";
import { reachedIndex, stageDefs, type PipelineType } from "@/components/ui/deploy-stage-tracker";
import { deploymentHref, serviceHref } from "@/components/home/home";
import { cn, formatDateTime } from "@/lib/utils";
import { ago } from "@/lib/time";

// Home before the first deploy, and until Finish setting up is complete or an
// owner hides it (board: private/design/canvas/ReworkWelcome.dc.html, 7g).
// The first-app card collapses to one line once a service is live.

const card = "min-w-0 overflow-hidden rounded-[10px] border border-subtle bg-surface";
const head = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3";
const h2 = "text-[15px] font-semibold leading-[22px] text-fg";
const sum = "text-xs text-muted";
const lnk =
  "text-[13px] text-link hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center";
const button =
  "inline-flex h-[30px] pointer-coarse:h-11 items-center rounded-md border border-strong/60 bg-raised px-2.5 text-xs font-medium text-fg hover:border-strong focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus";

/** D-147's five, as the catalog names them. */
const APPS = [
  { slug: "uptime-kuma", name: "Uptime Kuma" },
  { slug: "plausible", name: "Plausible" },
  { slug: "n8n", name: "n8n" },
  { slug: "ghost", name: "Ghost" },
  { slug: "umami", name: "Umami" },
];

export interface SetupRow {
  key: string;
  label: string;
  sub: string;
  done: boolean;
  act?: { label: string; href: string };
}

/** The first live service of the workspace's projects, the one the card shows. */
export function firstLive(services: WorkspaceService[]): WorkspaceService | undefined {
  return services.find((s) => s.project_id && (s.runtime_status === "running" || s.runtime_status === "unhealthy"));
}

/** Finish setting up: the instance's rows for operators (its address, the
 *  recovery kit, backups), the workspace's for everyone. */
export function useFinishSetup(live: WorkspaceService | undefined) {
  const { user } = useAuth();
  const role = useOrgRole();
  const operator = useIsOperator();
  const settings = useQuery({ queryKey: ["org-settings"], queryFn: orgService.getSettings });
  const members = useQuery({ queryKey: ["org-members"], queryFn: () => orgService.listMembers(), meta: { quietFailure: "Its rows are drawn only once `known` is true, which needs this read's answer." } });
  const servers = useQuery({ ...serversQuery, meta: { quietFailure: "Its rows are drawn only once `known` is true, which needs this read's answer." } });
  const panelHost = useQuery(panelHostQuery);
  const setup = useQuery({ ...setupQuery, enabled: operator });
  const panel = useQuery({ ...panelStatusQuery, enabled: operator, refetchInterval: false });
  const backup = useQuery({ queryKey: PANEL_KEY, queryFn: () => backupService.get(backupPath(null)), enabled: operator });

  const loading =
    settings.isLoading || members.isLoading || servers.isLoading || panelHost.isLoading || (operator && (setup.isLoading || panel.isLoading || backup.isLoading));
  // Every read the rows below rest on has answered. Until then Home isn't
  // the setup layout: a read that failed is not a step that isn't done.
  const known = [settings, members, servers, panelHost, ...(operator ? [setup, panel, backup] : [])].every((q) => q.data !== undefined);
  const n = members.data?.length ?? 0;
  const own = placeable(servers.data ?? []).length;
  const appsHere = panelHost.data?.role !== "control_plane";
  const domain = setup.data?.domain ?? "";
  const cert = !!panel.data?.certificate;
  const kit = setup.data?.kit_acknowledged_at;
  const kitStale = !!setup.data?.kit_out_of_date;
  const dest = backup.data;

  const rows: SetupRow[] = [
    { key: "account", label: "Your account", sub: [user?.name, role].filter(Boolean).join(", "), done: true },
    ...(operator
      ? [
          {
            key: "address",
            label: "Toft’s address",
            sub: !domain ? "It answers on its IP address. Give it a domain." : cert ? `${domain}, with a certificate` : `${domain}, certificate pending`,
            done: !!domain && cert,
            act: { label: "Set", href: "/setup/address" },
          },
          {
            key: "kit",
            label: "Recovery kit",
            sub: !kit
              ? "Not stored yet. Without it the encrypted secrets can’t be restored."
              : kitStale
                ? `Out of date: the encryption key or the backup settings changed after it was stored on ${formatDateTime(kit).slice(0, 10)}.`
                : `Stored on ${formatDateTime(kit).slice(0, 10)}`,
            done: !!kit && !kitStale,
            act: { label: kitStale ? "Store again" : "Store", href: "/instance/kit" },
          },
        ]
      : []),
    {
      key: "deploy",
      label: "Deploy an app",
      sub: live ? `${live.name}, live` : "The card on this page",
      done: !!live,
    },
    ...(operator
      ? [
          {
            key: "backups",
            label: "Backups",
            sub: dest ? `Nightly and encrypted, to ${dest.bucket}` : "Nightly and encrypted, to any S3 bucket. Not set yet.",
            done: !!dest,
            act: { label: "Set up", href: "/settings/backups" },
          },
        ]
      : []),
    {
      key: "team",
      label: "Your team",
      sub: n > 1 ? `${n} members` : "An invite link for each person.",
      done: n > 1,
      act: role === "owner" ? { label: "Invite", href: "/settings#inv-h" } : undefined,
    },
    {
      key: "servers",
      label: "More servers",
      sub: own > 0 ? `${own} ${own === 1 ? "server" : "servers"} added` : appsHere ? "Optional: this one runs apps." : "Needed: this one runs only Toft.",
      done: own > 0,
      act: role === "owner" ? { label: "Add", href: "/servers/new" } : undefined,
    },
  ];
  return {
    loading,
    known,
    hidden: !!settings.data?.setup_hidden_at,
    // A kit that goes out of date later is Needs you's row: it doesn't bring the checklist back.
    complete: rows.every((r) => r.done || (r.key === "kit" && !!kit)),
    rows,
    appsHere,
  };
}

export function Welcome({
  rows,
  services,
  data,
  now,
}: {
  rows: SetupRow[];
  services: WorkspaceService[];
  data: DashboardData;
  now: number;
}) {
  const live = firstLive(services);
  const deploying = data.deploying[0] as LiveDeploy | undefined;
  const failed = !live && !deploying ? data.failed_deploys[0] : undefined;
  return (
    <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1040px)_minmax(320px,1fr)]">
      {/* The card changes from deploying to live, or failed, in place. */}
      <div aria-live="polite" className="flex min-w-0 flex-col gap-5">
        {live ? (
          <LiveLine svc={live} />
        ) : deploying ? (
          <Deploying live={deploying} now={now} />
        ) : (
          <>
            {failed && (
              <section aria-label={`${failed.service_name}’s deploy failed`} className={cn(card, "flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-3 text-[13px]")}>
                <span className="font-medium text-status-failed-fg">{failed.service_name}’s deploy failed</span>
                <span className="min-w-0 flex-[1_1_240px] text-fg/70 [overflow-wrap:anywhere]">{failed.error_summary}</span>
                <Link href={deploymentHref(failed.project_id, failed.service_id, failed.id)} className={lnk}>
                  Its log
                </Link>
                <Link href={`${serviceHref(failed.project_id, failed.service_id)}?tab=settings`} className={lnk}>
                  Settings
                </Link>
              </section>
            )}
            <FirstApp />
          </>
        )}
      </div>
      <Checklist rows={rows} />
    </div>
  );
}

function FirstApp() {
  const { user } = useAuth();
  const role = useOrgRole();
  const canCreate = role === "owner" || role === "developer";
  const hostQ = useQuery(panelHostQuery);
  const panelHost = hostQ.data;
  // The server New service picks for it (the workspace's own first).
  const serversQ = useQuery({ ...serversQuery, select: placeable });
  const servers = serversQ.data ?? [];
  const target = servers.find((s) => !s.shared) ?? servers[0];
  // Until both answer, which server it runs on isn't known: say none.
  if (hostQ.isPending || serversQ.isPending) {
    return (
      <section aria-labelledby="fd-h" className={card}>
        <div className={head}>
          <h2 id="fd-h" className={h2}>Deploy your first app</h2>
        </div>
      </section>
    );
  }
  // Under Only Toft the panel's server runs no apps (D-62).
  if (panelHost?.role === "control_plane" && !target) {
    return (
      <section aria-labelledby="fd-h" className={card}>
        <div className={head}>
          <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
            <h2 id="fd-h" className={h2}>Deploy your first app</h2>
            <span className={sum}>apps need a server of their own</span>
          </div>
          {role === "owner" && (
            <Link href="/servers/new" className={button}>Add a server</Link>
          )}
        </div>
        <p className="px-4 py-3.5 text-[13px] leading-[19px] text-muted">
          This server runs only Toft, so nothing deploys here. Add a server, then deploy your code, an app from the catalog or an
          image to it.{role === "owner" ? "" : ` Owners of ${user?.orgName ?? "this workspace"} add servers.`}
        </p>
      </section>
    );
  }
  const ways = [
    {
      key: "git",
      title: "Your code",
      body: <>From a Git repository: GitHub, GitLab or one in Toft. A Dockerfile, or none: Toft works out how to build it. A push deploys it again.</>,
    },
    { key: "catalog", title: "From the catalog", body: <>Apps, databases and tools, set up and ready to use.</> },
    {
      key: "image",
      title: "An image",
      body: (
        <>
          Any image from a registry, such as <span className="font-mono text-xs">ghcr.io/you/app:1.4</span>. Private ones need a login.
        </>
      ),
    },
  ];
  return (
    <section aria-labelledby="fd-h" className={card}>
      <div className={head}>
        <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
          <h2 id="fd-h" className={h2}>Deploy your first app</h2>
          <span className={sum}>
            {target ? (
              <>
                it runs on <span className="font-mono">{target.name}</span> and answers at once
              </>
            ) : (
              "it runs on this server and answers at once"
            )}
          </span>
        </div>
      </div>
      <ul className="grid gap-3 p-4 lg:grid-cols-3">
        {ways.map((w) => (
          // The title's link covers the card; the catalog's chips sit above it.
          <li key={w.key} className={cn("relative flex flex-col gap-2 rounded-[10px] border border-strong/60 p-3.5", canCreate && "hover:border-strong")}>
            {canCreate ? (
              <Link
                href={`/services/new?source=${w.key}`}
                aria-describedby={`way-${w.key}`}
                className="text-sm font-semibold text-fg after:absolute after:inset-0 after:rounded-[10px] focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-focus"
              >
                {w.title}
              </Link>
            ) : (
              <span className="text-sm font-semibold text-fg">{w.title}</span>
            )}
            <span id={`way-${w.key}`} className="text-[13px] leading-[19px] text-muted">{w.body}</span>
            {w.key === "catalog" && (
              <span className="relative flex flex-wrap gap-1.5">
                {APPS.map((a) =>
                  canCreate ? (
                    <Link
                      key={a.slug}
                      href={`/services/new?template=${a.slug}`}
                      className="rounded-full border border-strong/60 px-2 py-0.5 pointer-coarse:py-3 text-xs text-fg/85 hover:border-strong hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
                    >
                      {a.name}
                    </Link>
                  ) : (
                    <span key={a.slug} className="rounded-full border border-strong/60 px-2 py-0.5 text-xs text-fg/85">
                      {a.name}
                    </span>
                  ),
                )}
              </span>
            )}
          </li>
        ))}
      </ul>
      <p className="border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
        {canCreate ? (
          <>
            It goes in a new project named after it; rename it any time. It gets a temporary address under sslip.io when it’s created, so it
            answers as soon as it’s live: for a first look; use your own domain for anything with logins.
          </>
        ) : (
          `Owners and developers of ${user?.orgName ?? "this workspace"} create services.`
        )}
      </p>
    </section>
  );
}

/** A person's own domain first, else the temporary one (D-150). */
function shownDomain(domains: Domain[] | undefined): Domain | undefined {
  return domains?.find((d) => !d.temporary) ?? domains?.[0];
}

const url = (d: Domain) => `${d.temporary && !d.certificate_at ? "http" : "https"}://${d.hostname}`;

const FROM: Record<string, string> = { git: "from its repository", "docker-image": "from its image", "docker-compose": "from its compose file" };

function Deploying({ live, now }: { live: LiveDeploy; now: number }) {
  const lines = useDeployLog(live.service_id, live.id);
  const { data: domains } = useQuery({ queryKey: ["domains", live.service_id], queryFn: () => domainService.list(live.service_id) });
  const d = shownDomain(domains);
  const stages = stageDefs((live.source_type || "git") as PipelineType, live.has_compose_repo);
  const current = live.status === "building" ? reachedIndex(lines, stages) : -1;
  return (
    <section aria-labelledby="dp-h" className={card}>
      <div className={head}>
        <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
          <h2 id="dp-h" className={h2}>Deploying {live.service_name}</h2>
          <span className={sum}>{[FROM[live.source_type || "git"], `started ${ago(live.created_at, now)}`, live.server_name || "this server"].join(" · ")}</span>
        </div>
        <Link href={deploymentHref(live.project_id, live.service_id, live.id)} className={lnk}>Follow the log</Link>
      </div>
      <div className="flex flex-col gap-3.5 p-4">
        <ol aria-label="Deploy progress" className="grid gap-2" style={{ gridTemplateColumns: `repeat(${stages.length}, minmax(0, 1fr))` }}>
          {stages.map((s, i) => (
            <li key={s.key} className="flex min-w-0 flex-col gap-1.5 text-xs text-muted">
              <span className={cn("h-1 rounded-xs", i < current ? "bg-status-running" : i === current ? "bg-status-building" : "bg-raised")} />
              <span className="font-medium text-fg">{s.label}</span>
              <span>{i < current ? "done" : i === current ? "running" : "next"}</span>
            </li>
          ))}
        </ol>
        <p className="text-[13px] leading-[19px] text-muted">
          {d && (
            <>
              It will answer at <span className="font-mono text-xs text-fg [overflow-wrap:anywhere]">{url(d)}</span>
              {d.temporary && <TemporaryChip className="mx-1.5 inline-flex align-middle" />}.{" "}
            </>
          )}
          You can leave this page: Home says when it’s live, and Home and the Inbox say if it fails, and why.
        </p>
      </div>
    </section>
  );
}

function LiveLine({ svc }: { svc: WorkspaceService }) {
  const { data: domains } = useQuery({ queryKey: ["domains", svc.id], queryFn: () => domainService.list(svc.id) });
  const d = shownDomain(domains);
  const pending = !!d?.temporary && !d.certificate_at;
  return (
    <section aria-label={`${svc.name} is live`} className={card}>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3 text-[13px] leading-5 text-muted">
        <span className="inline-flex items-center gap-2">
          <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-status-running" />
          <span>
            <Link
              href={serviceHref(svc.project_id, svc.id)}
              className="font-semibold text-fg hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
            >
              {svc.name}
            </Link>{" "}
            is live
          </span>
        </span>
        {!d && domains && (
          <Link href={`${serviceHref(svc.project_id, svc.id)}?tab=domains`} className={lnk}>Add a domain</Link>
        )}
        {d && (
          <>
            <span className="inline-flex min-w-0 flex-wrap items-center gap-2">
              <span className="font-mono text-xs text-fg [overflow-wrap:anywhere]">{url(d)}</span>
              {d.temporary && <TemporaryChip />}
            </span>
            <span className="inline-flex flex-wrap items-center gap-2">
              <a href={url(d)} target="_blank" rel="noopener noreferrer" className={cn(lnk, "inline-flex items-center gap-1")}>
                Open
                <ExternalLink aria-hidden="true" className="h-3 w-3" />
                <span className="sr-only"> (opens in a new tab)</span>
              </a>
              {d.temporary && (
                <>
                  <span aria-hidden="true" className="text-muted">·</span>
                  <Link href={`${serviceHref(svc.project_id, svc.id)}?tab=domains`} className={lnk}>Add your domain</Link>
                </>
              )}
            </span>
          </>
        )}
        <span className="flex-1" />
        <Link href="/services/new" className={button}>Deploy another app</Link>
      </div>
      {pending && (
        <p className="px-4 pb-3 text-xs leading-[18px] text-status-pending-fg">
          Certificate pending: it answers over HTTP until Let’s Encrypt issues one.
        </p>
      )}
    </section>
  );
}

function Checklist({ rows }: { rows: SetupRow[] }) {
  const role = useOrgRole();
  const queryClient = useQueryClient();
  const hide = useMutation({
    mutationFn: orgService.hideSetup,
    onSuccess: (s) => queryClient.setQueryData(["org-settings"], s),
    onError: (err: Error) => toast.error(err.message || "Couldn’t hide the checklist"),
  });
  return (
    <aside aria-labelledby="fs-h" className={card}>
      <div className={head}>
        <h2 id="fs-h" className={h2}>Finish setting up</h2>
        <span className="flex items-center gap-2.5">
          <span className={sum}>
            {rows.filter((r) => r.done).length} of {rows.length}
            <span className="sr-only"> done</span>
          </span>
          {role === "owner" && (
            <button type="button" onClick={() => hide.mutate()} disabled={hide.isPending} className={lnk}>
              Hide
            </button>
          )}
        </span>
      </div>
      <ul>
        {rows.map((r) => (
          <li key={r.key} className="flex items-start gap-2.5 border-t border-subtle px-4 py-2.5 text-[13px] first:border-t-0">
            {r.done ? (
              <Check aria-hidden="true" className="mt-0.5 h-4 w-4 flex-none text-status-running" strokeWidth={2} />
            ) : (
              <span aria-hidden="true" className="mt-0.5 h-4 w-4 flex-none rounded-full border-[1.5px] border-strong" />
            )}
            <span className="flex min-w-0 flex-1 flex-col gap-0.5">
              <span className={r.done ? "text-muted" : "font-medium text-fg"}>
                {r.label}
                <span className="sr-only">{r.done ? ", done" : ", to do"}</span>
              </span>
              <span className="text-xs leading-[18px] text-muted [overflow-wrap:anywhere]">{r.sub}</span>
            </span>
            {!r.done && r.act && (
              <Link href={r.act.href} className={lnk}>
                {r.act.label}
                <span className="sr-only"> {r.label}</span>
              </Link>
            )}
          </li>
        ))}
      </ul>
    </aside>
  );
}
