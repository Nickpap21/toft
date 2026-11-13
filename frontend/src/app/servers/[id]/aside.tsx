"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Check, Copy } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import { capabilityService, dnsProviderService, serverService, type CapacitySummary, type Server } from "@/lib/api/server.service";
import { cn, formatDateTime } from "@/lib/utils";
import { card } from "@/components/settings/settings-frame";
import { btnSm } from "@/app/projects/_lib/ui";
import { stagesOf } from "@/components/servers/provisioning";
import { hostKeyFile } from "@/components/servers/host-key-dialog";
import { dnsProviderName, imageTag } from "./server-view";

const h2 = "text-[15px] font-semibold leading-[22px] text-fg";
/** The Settings cards' head (the board's .ch) and its title with a summary (.hl). */
export const settingsHead = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3";
export const titleRow = "flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5";
/** A card's footer (the board's .cf). */
export const settingsFoot = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-muted";

/** A Settings card's row (the board's .kv): the name in a 120 px column, above the value on phones. */
export function Row({ k, children, first }: { k: string; children: React.ReactNode; first?: boolean }) {
  return (
    <div className={cn("grid grid-cols-[120px_minmax(0,1fr)] gap-3 px-4 py-2.5 text-[13px] max-sm:grid-cols-1 max-sm:gap-0.5", !first && "border-t border-subtle")}>
      <dt className="text-muted">{k}</dt>
      <dd className="flex min-w-0 flex-col gap-0.5">{children}</dd>
    </div>
  );
}

/** Settings' Connection card of a server reached over SSH; addedBy names
 *  who added it. */
export function Connection({ server, isOperator, onHostKey, addedBy }: { server: Server; isOperator: boolean; onHostKey: () => void; addedBy?: string }) {
  const [copied, setCopied] = useState(false);
  const fp = server.host_key_fingerprint;
  return (
    <section aria-labelledby="conn-h" className={card}>
      <div className={settingsHead}>
        <div className={titleRow}>
          <h2 id="conn-h" className={h2}>Connection</h2>
          <span className="text-xs text-muted">SSH · one connection kept open</span>
        </div>
      </div>
      <dl>
        <Row k="Address" first>
          <span className="font-mono text-[13px] [overflow-wrap:anywhere]">{server.ip_address} · {server.user} · port {server.port}</span>
        </Row>
        <Row k="Private key">
          <span>Stored encrypted; never shown again.</span>
        </Row>
        <Row k="Host key">
          {fp ? (
            <>
              <span className="break-all font-mono text-xs leading-[18px] text-fg">
                {server.host_key_type} {fp}
              </span>
              {server.host_key_pinned_at && (
                <span className="text-xs leading-[18px] text-muted">
                  Pinned <span className="font-mono">{formatDateTime(server.host_key_pinned_at).slice(0, 10)}</span> on the first connection; every
                  connection must present it.
                </span>
              )}
            </>
          ) : server.host_key_pinned_at ? (
            <span className="inline-flex items-start gap-1.5 text-status-failed-fg">
              <AlertTriangle className="mt-0.5 h-3.5 w-3.5 flex-none" aria-hidden="true" />
              The pinned key can’t be read: an instance operator must replace it.
            </span>
          ) : (
            <span className="text-status-pending-fg">Not pinned yet: the next connection pins it.</span>
          )}
        </Row>
        <Row k="Server ID">
          <span className="flex items-center gap-1.5">
            <span className="min-w-0 font-mono text-xs [overflow-wrap:anywhere]">{server.id}</span>
            <button
              type="button"
              aria-label="Copy server ID"
              className="touch-target flex h-7 w-7 flex-none items-center justify-center rounded-md text-fg/70 hover:bg-raised hover:text-fg pointer-coarse:min-w-11"
              onClick={() => navigator.clipboard?.writeText(server.id).then(() => setCopied(true))}
            >
              {copied ? <Check className="h-3.5 w-3.5" aria-hidden="true" /> : <Copy className="h-3.5 w-3.5" aria-hidden="true" />}
            </button>
          </span>
        </Row>
        <Row k="Added">
          <span>
            <span className="font-mono text-xs">{formatDateTime(server.created_at).slice(0, 10)}</span>
            {addedBy && <> by {addedBy}</>}
          </span>
        </Row>
      </dl>
      <div className={settingsFoot}>
        <span className="min-w-0 flex-[1_1_240px] text-xs">
          {isOperator ? "Instance operators only" : `Check the host key with ssh-keygen -lf ${hostKeyFile(server.host_key_type)}; an instance operator replaces it.`}
        </span>
        {isOperator && (
          <button type="button" className={btnSm} onClick={onHostKey}>
            Verify or replace the host key
          </button>
        )}
      </div>
    </section>
  );
}

/** Traefik's state from the stats read: the stats read answers with
 *  Docker's version; no Traefik in it then means there is no paas-traefik
 *  container. */
export function traefikState(summary: CapacitySummary | undefined): { label: string; tone: "running" | "failed" | "muted" } {
  if (!summary?.docker_version) return { label: "Not read", tone: "muted" };
  const status = summary.traefik_status;
  if (status === "running") return { label: "Running", tone: "running" };
  return { label: status ? status.charAt(0).toUpperCase() + status.slice(1) : "Not found", tone: "failed" };
}

const STATE_TEXT = {
  running: "text-status-running-fg",
  failed: "text-status-failed-fg",
  muted: "text-muted",
} as const;

export function Traefik({ server, summary }: { server: Server; summary: CapacitySummary | undefined }) {
  const { data: dns, error } = useQuery({
    queryKey: ["dns-provider", server.id],
    queryFn: () => dnsProviderService.get(server.id),
    retry: false,
  });
  const noDNS = error instanceof ApiError && error.status === 404;
  const read = !!summary?.docker_version;
  const status = summary?.traefik_status;
  const state = traefikState(summary);
  return (
    <section aria-labelledby="tr-h" className={card}>
      <div className={settingsHead}>
        <div className={titleRow}>
          <h2 id="tr-h" className={h2}>Traefik</h2>
          <span className={cn("inline-flex items-center gap-1.5 text-xs font-medium", STATE_TEXT[state.tone])}>
            <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-current" />
            {state.label}
          </span>
        </div>
        <span className="text-xs text-muted">keeps serving if the panel is down</span>
      </div>
      <dl>
        <Row k="Container" first>
          <span className="font-mono text-[13px]">paas-traefik{summary?.traefik_image ? ` · ${imageTag(summary.traefik_image)}` : ""}</span>
        </Row>
        <Row k="Listens on">
          <span className="font-mono text-[13px]">80, 443</span>
        </Row>
        <Row k="Certificates">
          <span>
            Let’s Encrypt: exact names by HTTP-01
            {dns ? (
              <>, wildcards by DNS-01 through {dnsProviderName(dns.provider) ?? <span className="font-mono text-xs">{dns.provider}</span>}.</>
            ) : noDNS ? (
              "; no DNS provider, so no wildcards."
            ) : (
              "."
            )}
          </span>
        </Row>
      </dl>
      {read && status !== "running" && (
        <p className="mx-4 mb-3.5 mt-1 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">
          {status ? `paas-traefik is ${status}: this server’s domains don’t answer.` : "No paas-traefik container: this server’s domains don’t answer."} Re-provision
          starts it again.
        </p>
      )}
    </section>
  );
}

export function Firewall({ server }: { server: Server }) {
  // The last run's outcome for the firewall stage (D-48).
  const { data: run } = useQuery({ queryKey: ["provision-run", server.id], queryFn: () => serverService.lastProvision(server.id) });
  const last = run?.finished_at ? stagesOf(run.log.replace(/\n$/, "").split("\n"), run.ok ? "ok" : "failed").find((s) => s.id === "firewall") : null;
  // The pool Docker on the server uses, as the capability probe read it
  // (the Isolation card's query); null until a probe read it.
  const { data: caps, isPending, isError } = useQuery({ queryKey: ["capabilities", server.id], queryFn: () => capabilityService.get(server.id) });
  const pools = caps?.address_pools;
  // As Overview’s Needs attention decides to give the fix.
  const fixAbove = run?.ok && run.warnings.includes("the app network address pool");
  // The last flag draws the value as words (Geist), not as an infrastructure value (mono).
  const rows: [string, string, string, boolean?, boolean?][] = [
    ["Internet", `${server.port} SSH · 80 HTTP · 443 HTTPS`, "Everything else inbound is refused (ufw). fail2ban watches SSH."],
    ["App containers", "80 · 443 · DNS on this host", "Nothing else on the host, and no cloud metadata (169.254.0.0/16)."],
    server.app_networks !== "isolated"
      ? ["App networks", "paas-network", "Every app shares one network (legacy); isolated networks keep environments apart."]
      : pools?.length
        ? ["App networks", pools.map((p) => `${p.base} in /${p.size}s`).join(", "), "One network per environment; no routes between them."]
        : pools
          ? [
              "App networks",
              "not set",
              `Docker gives app networks its own ranges, about 30 networks, which can run out or clash with this server’s LAN or VPN. ${fixAbove ? "Needs attention, on Overview, says how to set it." : "Re-provision says how to set it."}`,
              true,
              true,
            ]
          : [
              "App networks",
              isPending ? "Reading…" : isError ? "couldn’t be read" : "not known yet",
              isError
                ? "Toft couldn’t read what this server can run. Check again under Isolation, or reload this page."
                : "Toft reads Docker’s pool when it checks what this server can run: after provisioning, daily, or Check again under Isolation.",
              false,
              true,
            ],
  ];
  return (
    <section aria-labelledby="fw-h" className={card}>
      <div className={settingsHead}>
        <div className={titleRow}>
          <h2 id="fw-h" className={h2}>Firewall and ports</h2>
          <span className={cn("text-xs", last?.sub === "warning" || last?.state === "failed" ? "text-status-pending-fg" : "text-muted")}>
            {last?.sub === "warning"
              ? "the last run warned: see Provisioning, under Maintenance"
              : last?.state === "failed"
                ? "the last provisioning run stopped before it"
                : "set up by provisioning"}
          </span>
        </div>
      </div>
      <dl>
        {rows.map(([k, v, note, warn, words], i) => (
          <Row key={k} k={k} first={i === 0}>
            <span className={cn(words ? "text-[13px]" : "font-mono text-[13px]", warn ? "text-status-pending-fg" : "text-fg")}>{v}</span>
            <span className="text-xs leading-[18px] text-muted">{note}</span>
          </Row>
        ))}
      </dl>
    </section>
  );
}
