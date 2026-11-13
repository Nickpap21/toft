import type { Service } from "@/lib/api/service.service";

/** The address other services reach s at, as the API names it (connect_to):
 *  host:port, the host alone when it listens on no port, null when the API
 *  names none. */
export function connectTo(s: Pick<Service, "connect_to">): string | null {
  const c = s.connect_to;
  if (!c) return null;
  return c.port > 0 ? `${c.host}:${c.port}` : c.host;
}

/** The alias a service named name will get when created without one: with
 *  its environment's name in an environment, "" for a shared service. It
 *  mirrors the backend's defaultNetworkAlias (internal/service/service.go),
 *  held to it by testdata/network_alias.json there: the New service form
 *  names it on every keystroke, for every scope, before any request (a
 *  workspace service has no environment to ask about). */
export function defaultNetworkAlias(name: string, environment = ""): string {
  const s = (environment ? `${name}-${environment}` : name)
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return s.slice(0, 63).replace(/-+$/, "");
}
