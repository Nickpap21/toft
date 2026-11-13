// The Logs and metrics tab's rules (board: ReworkSharedServices, tab metrics,
// D-48, D-147), kept apart from the tab so they are tested. The backend's
// rules: deployment.Service.lokiFor and observability.Service.UpsertLoki.

import type { ContainerLog, ObservabilityConfig, ServerLogs } from "@/lib/api/observability.service";

export type StackKind = "loki" | "prometheus" | "grafana";

/** The workspace's Loki, Prometheus and Grafana: the service registered as
 *  each, else its workspace service from the catalog. */
export function stackOf<S extends { id: string; scope: string; addon_slug: string }>(cfg: Partial<ObservabilityConfig>, services: S[]): { kind: StackKind; svc: S | undefined }[] {
  const ids: Record<StackKind, string | undefined> = { loki: cfg.loki_service_id, prometheus: cfg.prometheus_service_id, grafana: cfg.grafana_service_id };
  return (["loki", "prometheus", "grafana"] as const).map((kind) => ({
    kind,
    svc: services.find((s) => s.id === ids[kind]) ?? services.find((s) => s.scope === "org" && s.addon_slug === kind),
  }));
}

/** A bare host such as "loki": a Docker network alias, which resolves only
 *  inside containers, never in the daemon's own log driver. */
export function isContainerAlias(raw: string): boolean {
  try {
    const host = new URL(raw).hostname;
    return !!host && !host.includes(".") && !host.includes(":") && !host.startsWith("[") && host !== "localhost";
  } catch {
    return false;
  }
}

/** Can a container's logs reach Loki, given its address? */
export type LokiState = "none" | "alias" | "ok";
export function lokiState(url: string | undefined): LokiState {
  if (!url) return "none";
  return isContainerAlias(url) ? "alias" : "ok";
}

/** Loki's loopback address on its own server (D-148), "127.0.0.1:39100",
 *  from a container that ships to it; "" when none does yet. */
export function localLokiAddress(logs: ServerLogs[] | undefined): string {
  for (const g of logs ?? [])
    for (const s of g.services)
      for (const c of s.containers)
        if (c.loki_url?.startsWith("http://127.0.0.1:")) return new URL(c.loki_url).host;
  return "";
}

export type LogsTone = "running" | "pending" | "muted";
export interface LogsCell {
  label: string;
  tone: LogsTone;
  why: string;
  /** Its logs reach Loki (so "Logs in Grafana" makes sense). */
  ships: boolean;
}

/**
 * Where a service's logs go, from its containers' log drivers: Loki (at the
 * address now set, or an older one until its next deploy), or this server
 * only, and why. here, given only when no usable address is set (none, or
 * an alias), says where the catalog Loki runs (D-148): this row is Loki itself, an app on its
 * server (which ships to its 127.0.0.1 port), or a service elsewhere. On an
 * agent server the containers log to Docker's local driver and the agent
 * ships them.
 */
export function logsCell(
  containers: ContainerLog[] | undefined,
  running: boolean,
  compose: boolean,
  loki: string | undefined,
  here?: "loki" | "app" | "elsewhere",
  agent = false,
): LogsCell {
  const state = lokiState(loki);
  if (!running || !containers?.length) return { label: "Not running", tone: "muted", why: "", ships: false };
  if (state !== "ok" && here === "loki") return { label: "This server only", tone: "muted", why: "Loki keeps its own logs here", ships: false };
  if (state !== "ok" && here === "app" && !compose)
    return agent || containers.some((c) => c.driver === "loki" && c.loki_url?.startsWith("http://127.0.0.1:"))
      ? { label: "Loki, on this server", tone: "running", why: "", ships: true }
      : { label: "This server only", tone: "pending", why: "ships to Loki on this server from its next deploy", ships: false };
  const toLoki = containers.filter((c) => c.driver === "loki");
  if (toLoki.length) {
    const now = loki ? `${loki.replace(/\/$/, "")}/loki/api/v1/push` : "";
    const stale = toLoki.some((c) => c.loki_url && c.loki_url !== now);
    return stale
      ? { label: "Loki, old address", tone: "pending", why: `pushes to ${toLoki[0].loki_url?.replace("/loki/api/v1/push", "")} until its next deploy`, ships: state === "ok" && !isContainerAlias(toLoki[0].loki_url ?? "") }
      : { label: "Loki", tone: "running", why: "", ships: true };
  }
  if (compose) return { label: "This server only", tone: "muted", why: "compose stacks keep their logs on the server", ships: false };
  if (state === "none" || here === "elsewhere")
    return { label: "This server only", tone: "muted", why: here === "elsewhere" ? "needs an external Loki address" : "", ships: false };
  if (state === "alias") return { label: "This server only", tone: "pending", why: "Loki’s address is an alias", ships: false };
  return { label: "This server only", tone: "pending", why: "started before Loki was set up; ships from its next deploy", ships: false };
}

/** Where a server's logs go, its services' cells in one line (the Logs
 *  table's third column). at: a Loki address is set (address), Loki runs on
 *  this server (here) or on another (elsewhere), or there is none. */
export function serverLogsLine(cells: LogsCell[], at: "address" | "here" | "elsewhere" | "none"): { label: string; tone: LogsTone; why: string } {
  const running = cells.filter((c) => c.label !== "Not running");
  if (running.length === 0) return { label: "Nothing running", tone: "muted", why: "" };
  if (at === "elsewhere") return { label: "This server only", tone: "pending", why: "needs an external Loki address" };
  if (at === "none") return { label: "This server only", tone: "muted", why: "" };
  const ships = running.filter((c) => c.ships).length;
  const old = running.filter((c) => c.ships && c.label === "Loki, old address").length;
  const behind = running.filter((c) => !c.ships && c.tone === "pending").length;
  const label = ships === 0 ? "This server only" : old === ships ? "Loki, old address" : at === "here" ? "Loki, on this server" : "Loki";
  if (behind > 0) return { label, tone: "pending", why: `${behind} ${behind === 1 ? "ships from its" : "ship from their"} next deploy` };
  if (old > 0) return { label, tone: "pending", why: `${old} ${old === 1 ? "moves to the new address at its" : "move to the new address at their"} next deploy` };
  if (ships === 0) return { label: running[0].label, tone: "muted", why: running[0].why };
  return { label, tone: "running", why: "and a copy on the server" };
}

/** The warning when some servers' logs stay on them (D-148's words). */
export function localLogsWarning(lokiServer: string, servers: string[], services: number): { title: string; text: string } {
  const names = new Intl.ListFormat("en").format(servers);
  const one = services === 1;
  return {
    title: `Logs from ${names} stay on ${servers.length === 1 ? "that server" : "their servers"}`,
    text: `Loki runs on ${lokiServer}, and only the services there ship to it, on a port of that server that nothing else can reach. Central logs from other servers need an external Loki address for now. Until then ${one ? "the service" : `the ${services} services`} on ${names} ${one ? "keeps its logs on its own server: its Logs tab works" : "keep their logs on their own server: their Logs tabs work"}, Grafana doesn’t see them.`,
  };
}
