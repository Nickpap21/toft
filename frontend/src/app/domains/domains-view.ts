// The Domains page's rules (board: private/design/canvas/Domains.dc.html, D-48),
// kept apart from the page so they are tested. DNS and certificates come
// from each service's check (GET /services/:id/domains/check, D-45).

import { possessive } from "@/lib/utils";
import type { DomainCheck, WorkspaceDomain } from "@/lib/api/domain.service";

export type Fact = DomainCheck["domains"][number];
export type Server = DomainCheck["server"];

/** Let's Encrypt renews 30 days before expiry, so a certificate this close
 *  to it has failed to renew for a few days. */
export const RENEW_WARN_DAYS = 25;
const DAY = 86_400_000;

export const isWildcard = (host: string) => host.startsWith("*.");

/** The DNS record a domain needs: "www.example.com  A  203.0.113.11". */
export function recordOf(host: string, address: string): string {
  return `${host}  A  ${address || "<the server's address>"}`;
}

export function daysLeft(iso: string, now: number): number {
  return Math.floor((new Date(iso).getTime() - now) / DAY);
}

export interface Need {
  /** "DNS points elsewhere", "Waiting for DNS and a deploy", … */
  kind: string;
  tone: "failed" | "pending";
  icon: "globe" | "clock" | "lock";
  meta: string;
  todo: string;
  /** The button on the right; "deploy" deploys the service. */
  primary: "check" | "deploy" | "dns";
  secondary?: "check";
}

/**
 * What a domain needs from someone, or null: its DNS doesn't point at its
 * server, it waits for a deploy, its certificate doesn't verify or isn't
 * renewing, or a wildcard sits on a server with no DNS provider.
 */
export function needOf(
  d: WorkspaceDomain,
  f: Fact | undefined,
  server: Server | undefined,
  running: boolean,
  serverName: string,
  now: number,
): Need | null {
  if (!f || !server) return null;
  // The board's "storefront / api · production · fsn1-prod-01".
  const where = [`${d.project_name ? `${d.project_name} / ` : ""}${d.service_name}`, d.environment_name, serverName].filter(Boolean).join(" · ");
  const addr = server.address;
  if (isWildcard(d.hostname) && !server.wildcards_ok) {
    return {
      kind: "No DNS provider",
      tone: "failed",
      icon: "lock",
      meta: where,
      todo: `A wildcard’s certificate needs a DNS check, so ${possessive(d.service_name)} next deploy is refused before anything changes. Set a DNS provider on ${serverName}, or move the domain to a service on a server that has one.`,
      primary: server.owner ? "dns" : "check",
    };
  }
  if (f.dns === "elsewhere") {
    const theirs = f.addresses.filter((a) => !a.includes(":")).join(", ");
    const cert = f.certificate === "valid" && f.not_after ? `, and Let’s Encrypt can’t renew the certificate, which expires on ${f.not_after.slice(0, 10)}` : "";
    return {
      kind: "DNS points elsewhere",
      tone: "failed",
      icon: "globe",
      meta: `${where} · the record answers ${theirs}`,
      todo: d.temporary
        ? `${d.hostname} is a temporary name that spells out ${theirs}, an address ${serverName} no longer has, so nobody can change its record. Remove it and add a domain of your own.`
        : `Set the A record of ${d.hostname} to ${addr} at your DNS host. Until it points here, visitors reach ${theirs}${cert}.`,
      primary: "check",
    };
  }
  if (f.dns === "none") {
    const who = d.created_by ? ` · added by ${d.created_by}` : "";
    if (running && !f.routed) {
      return {
        kind: "Waiting for DNS and a deploy",
        tone: "pending",
        icon: "clock",
        meta: `${where}${who}`,
        todo: `Create this record at your DNS host, then deploy ${d.service_name}. From that deploy, Traefik on ${serverName} routes the name and asks Let’s Encrypt for its certificate, which works once the record points here.`,
        primary: "deploy",
        secondary: "check",
      };
    }
    return {
      kind: "No DNS record yet",
      tone: "pending",
      icon: "globe",
      meta: `${where}${who}`,
      todo: `Create this record at your DNS host. Until then nobody reaches ${d.hostname}, and Let’s Encrypt can’t issue its certificate.`,
      primary: "check",
    };
  }
  if (running && !f.routed) {
    return {
      kind: "Waiting for a deploy",
      tone: "pending",
      icon: "clock",
      meta: where,
      todo: `The record points here. Deploy ${d.service_name}: from that deploy, Traefik on ${serverName} routes the name and gets its certificate.`,
      primary: "deploy",
    };
  }
  if (f.certificate === "invalid") {
    return {
      kind: "Certificate doesn’t verify",
      tone: "failed",
      icon: "lock",
      meta: where,
      todo: `${d.hostname} answers with a certificate that doesn’t verify${f.not_after ? ` (it expires ${f.not_after.slice(0, 10)})` : ""}, so browsers warn visitors. Traefik renews it while the DNS points here: check the record, then check again.`,
      primary: "check",
    };
  }
  if (f.certificate === "valid" && f.not_after && daysLeft(f.not_after, now) < RENEW_WARN_DAYS) {
    return {
      kind: "Certificate isn’t renewing",
      tone: "pending",
      icon: "lock",
      meta: where,
      todo: `It expires on ${f.not_after.slice(0, 10)}. Let’s Encrypt renews 30 days before, so renewal has been failing: check that the DNS still points here and that ${serverName} answers on port 80.`,
      primary: "check",
    };
  }
  return null;
}

/** The server card's wildcard line. */
export function wildcardLine(panelHost: boolean, provider: string | null): { text: string; code?: string; warn: boolean } {
  if (panelHost) return { text: "Not supported on the panel host", warn: false };
  if (provider) return { text: "Let’s Encrypt over DNS, through", code: provider, warn: false };
  return { text: "No DNS provider: a deploy with a wildcard is refused here", warn: true };
}
