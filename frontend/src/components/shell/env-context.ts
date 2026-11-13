"use client";

import { useCallback, useEffect, useSyncExternalStore } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import type { Environment } from "@/lib/api/environment.service";

/**
 * The environment a project is being looked at in (D-55): "all", or one
 * environment's id. Chosen in the project sidebar, followed by the Overview,
 * Services and Environment tabs, remembered per project in localStorage.
 * A `?env=` in the URL wins for that page load, so links can carry it.
 */
export const ALL = "all";
export const envStorageKey = (projectId: string) => `toft_env:${projectId}`;

const listeners = new Set<() => void>();
function read(projectId: string): string {
  try {
    return localStorage.getItem(envStorageKey(projectId)) ?? "";
  } catch {
    return "";
  }
}
function subscribe(onChange: () => void) {
  listeners.add(onChange);
  return () => {
    listeners.delete(onChange);
  };
}

/** What the page should show, from the URL, the stored choice and the
 *  project's environments: a stale or foreign id falls back to "all", and a
 *  project with one environment opens on it. */
export function resolveEnv(
  param: string | null,
  stored: string,
  environments: Environment[] | undefined,
): string {
  if (!environments) return param || stored || ALL;
  const known = (id: string) => environments.some((e) => e.id === id);
  if (param === ALL || (param && known(param))) return param;
  if (stored === ALL || (stored && known(stored))) return stored;
  return environments.length === 1 ? environments[0].id : ALL;
}

/** Remember an environment for its project without touching the URL: an
 *  Environment page opened from anywhere becomes the context. */
export function rememberEnv(projectId: string, id: string) {
  try {
    localStorage.setItem(envStorageKey(projectId), id);
  } catch {}
  listeners.forEach((l) => l());
}

export function useEnvContext(projectId: string, environments: Environment[] | undefined) {
  const params = useSearchParams();
  const router = useRouter();
  const stored = useSyncExternalStore(
    subscribe,
    () => read(projectId),
    () => "",
  );
  const param = params.get("env");
  const envId = resolveEnv(param, stored, environments);
  const env = envId === ALL ? undefined : environments?.find((e) => e.id === envId);

  // A link that carries ?env= chooses for the next pages too.
  useEffect(() => {
    if (param && environments && envId === param && stored !== param) rememberEnv(projectId, param);
  }, [param, environments, envId, stored, projectId]);

  const set = useCallback(
    (id: string) => {
      rememberEnv(projectId, id);
      const next = new URLSearchParams(params.toString());
      if (id === ALL) next.delete("env");
      else next.set("env", id);
      const qs = next.toString();
      router.replace(qs ? `?${qs}` : "?", { scroll: false });
    },
    [params, projectId, router],
  );

  return { envId, env, set, all: envId === ALL };
}

/** `?env=<id>` for a link that should open in the chosen environment. */
export function withEnv(href: string, envId: string): string {
  if (envId === ALL) return href;
  return `${href}${href.includes("?") ? "&" : "?"}env=${envId}`;
}
