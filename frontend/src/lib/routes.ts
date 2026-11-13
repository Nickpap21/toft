/**
 * Pages anyone can open without signing in. Shared by proxy.ts (which
 * redirects everything else without the paas_auth cookie) and the app
 * chrome (which renders these without the signed-in shell). No React here:
 * the proxy runs outside the React tree.
 */
export const PUBLIC_PATHS = ["/", "/login", "/register", "/setup"] as const;
export const PUBLIC_PREFIXES = ["/invite", "/reset"] as const;

export function isPublicPath(pathname: string): boolean {
  return (
    (PUBLIC_PATHS as readonly string[]).includes(pathname) ||
    PUBLIC_PREFIXES.some((p) => pathname === p || pathname.startsWith(`${p}/`))
  );
}

/** Pages with their own frame instead of the signed-in shell: the public
 *  ones, the setup wizard's steps (/setup/*, signed in, D-62) and an agent's
 *  consent screen (/oauth/authorize, signed in, D-100). */
export function isBarePath(pathname: string): boolean {
  return isPublicPath(pathname) || pathname.startsWith("/setup/") || pathname.startsWith("/oauth/");
}

/**
 * A URL saved by a member that the page renders as a link (Grafana, Loki,
 * Prometheus). Only http(s) is a link: a javascript: or data: URL would run
 * in the panel's origin when clicked. The API refuses these at save; this
 * covers values saved before it did, and the next URL field someone adds.
 */
export function safeHref(url: string | null | undefined): string | undefined {
  if (!url) return undefined;
  try {
    const u = new URL(url);
    return u.protocol === "http:" || u.protocol === "https:" ? url : undefined;
  } catch {
    return undefined;
  }
}

/**
 * Where to go after signing in: ?next= when it is a path on this origin,
 * else Home. A full URL, "//host" or a path that a browser would read as
 * one ("/\t/host") goes Home, so ?next= is never an open redirect.
 */
export function safeNext(next: string | null | undefined): string {
  if (!next || !next.startsWith("/")) return "/dashboard";
  const base = "http://toft.invalid";
  try {
    const u = new URL(next, base);
    // Dot segments ("/.//host") resolve to a path a browser reads as a host.
    if (u.origin !== base || u.pathname.startsWith("//")) return "/dashboard";
    return u.pathname + u.search + u.hash;
  } catch {
    return "/dashboard";
  }
}

/** The sign-in page, keeping the page someone was sent to (not Home or a
 *  public page, which need no notice). */
export function signInHref(pathWithSearch: string): string {
  const path = pathWithSearch.split("?")[0];
  if (path === "/dashboard" || isPublicPath(path)) return "/";
  return `/?next=${encodeURIComponent(pathWithSearch)}`;
}
