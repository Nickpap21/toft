"use client";

import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { streamEvents } from "@/lib/api/client";
import { useAuth } from "@/context/AuthContext";
import { refreshCounts } from "@/lib/queries";

// What a "changed" event's kinds refresh (prefix matches; only mounted
// queries refetch). The count badges read ["project"], ["projects"] and
// ["inbox"]; an alert or a service's state is a health dot and a Needs you row.
const SERVICE_KEYS = ["inbox", "workspace-services", "project-services", "project-overview", "dashboard", "env-services", "project-scoped-services", "service"];
const CHANGED_KEYS: Record<string, string[]> = {
  issues: ["project", "projects", "issues", "issue", "issue-counts", "inbox"],
  docs: ["project", "projects", "docs", "inbox"],
  alerts: ["inbox", "alerts-recent"],
  services: SERVICE_KEYS,
  pulls: ["project", "projects", "repositories", "repository", "code-pulls", "code-pulls-page"],
};
// Keys whose second part is the project: an event naming a project
// refetches only that project's.
const PER_PROJECT = new Set(["project", "issues", "issue", "issue-counts", "docs", "repositories", "repository"]);
// "changed" events are gathered this long before refetching: a push that
// moves ten issues sends ten.
const CHANGED_WAIT = 250;

interface DeploymentEventPayload {
  type: string;
  deployment_id: string;
  service_id: string;
  service_name: string;
  project_id: string;
  status: string;
  error?: string;
}

/**
 * Opens a long-lived SSE connection to /api/v1/events.
 * On deployment status transitions, invalidates relevant React Query caches
 * and shows a toast notification.
 *
 * The server scopes the stream to the workspace active when it connects, so
 * it is reopened whenever the session (a sign-in, which navigates without a
 * page load) or the workspace changes.
 *
 * Must be mounted once inside QueryClientProvider and AuthProvider.
 */
export function useEventStream() {
  const queryClient = useQueryClient();
  const { token, user } = useAuth();
  const orgId = user?.orgId;
  const userId = user?.id;

  useEffect(() => {
    if (!token) return;

    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let closed = false;
    let ctrl: AbortController | null = null;
    let connectedBefore = false;
    // Exponential backoff: 3s → 6s → 12s → … capped at 60s.
    let retryDelay = 3_000;
    const MAX_RETRY_DELAY = 60_000;
    // What gathered "changed" events refetch: a key, with the projects it is
    // limited to, or null for all of it.
    const pending = new Map<string, Set<string> | null>();
    let changedTimer: ReturnType<typeof setTimeout> | null = null;

    function refetchChanged() {
      changedTimer = null;
      for (const [key, projects] of pending) {
        if (projects === null) queryClient.invalidateQueries({ queryKey: [key] });
        else for (const id of projects) queryClient.invalidateQueries({ queryKey: [key, id] });
      }
      pending.clear();
    }

    function scheduleRetry() {
      if (closed) return;
      retryTimer = setTimeout(() => {
        retryDelay = Math.min(retryDelay * 2, MAX_RETRY_DELAY);
        connect();
      }, retryDelay);
    }

    function connect() {
      ctrl = new AbortController();
      // Ended or dropped — reconnect with backoff.
      streamEvents("/api/v1/events", handleEvent, ctrl.signal).then(scheduleRetry, scheduleRetry);
    }

    function handleEvent(type: string, rawData: string) {
      if (type === "connected") {
        // Connection established — reset backoff. After a reconnect, refetch
        // what the events refresh: those sent while the stream was down (a
        // backend restart, a network drop) are lost.
        retryDelay = 3_000;
        if (connectedBefore) {
          const keys = new Set(["service", "deployments", "env-services", "project-scoped-services", "project-services", "workspace-services", "dashboard", "project-overview", "inbox", "project", "projects"]);
          for (const key of Object.values(CHANGED_KEYS).flat()) keys.add(key);
          for (const key of keys) queryClient.invalidateQueries({ queryKey: [key] });
        }
        connectedBefore = true;
        return;
      }

      // The inbox: someone's updates changed (D-88).
      if (type === "inbox") {
        try {
          const { users } = JSON.parse(rawData) as { users?: string[] };
          if (userId && users?.includes(userId)) queryClient.invalidateQueries({ queryKey: ["inbox"] });
        } catch {
          // A malformed event changes nothing.
        }
        return;
      }

      // Issues, docs, alerts or services changed (by anyone, the monitor or
      // git included): refetch what reads them.
      if (type === "changed") {
        try {
          const { kinds, project_id } = JSON.parse(rawData) as { kinds?: string[]; project_id?: string };
          for (const key of (kinds ?? []).flatMap((kind) => CHANGED_KEYS[kind] ?? [])) {
            const projects = pending.get(key);
            if (projects === null) continue;
            if (project_id && PER_PROJECT.has(key)) pending.set(key, (projects ?? new Set()).add(project_id));
            else pending.set(key, null);
          }
          changedTimer ??= setTimeout(refetchChanged, CHANGED_WAIT);
        } catch {
          // A malformed event changes nothing.
        }
        return;
      }

      let payload: DeploymentEventPayload;
      try {
        payload = JSON.parse(rawData);
      } catch {
        return;
      }

      const { service_id, service_name, project_id, error } = payload;

      // Invalidate caches so status badges, deployment lists, and service detail
      // all refresh automatically without per-page polling.
      queryClient.invalidateQueries({ queryKey: ["service", service_id] });
      queryClient.invalidateQueries({ queryKey: ["deployments", service_id] });
      // A stack's own IMAGE_TAG follows what went live (the Deploy label).
      queryClient.invalidateQueries({ queryKey: ["envvars", "service", service_id] });
      // Prefix-match: invalidates all env-services and project-scoped-services queries
      // regardless of which env/project the user is currently viewing.
      queryClient.invalidateQueries({ queryKey: ["env-services"] });
      queryClient.invalidateQueries({ queryKey: ["project-scoped-services"] });
      // The sidebar's health dots and running counts read these.
      if (project_id) queryClient.invalidateQueries({ queryKey: ["project-services", project_id] });
      queryClient.invalidateQueries({ queryKey: ["workspace-services"] });
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      // A deploy failing or going live changes Needs you and the project's
      // counts (the badges).
      refreshCounts(queryClient);
      if (project_id) queryClient.invalidateQueries({ queryKey: ["project-overview", project_id] });

      if (type === "deployment.running") {
        toast.success(`${service_name} deployed successfully`);
      } else if (type === "deployment.failed") {
        toast.error(`${service_name} deployment failed`, {
          description: error,
        });
      }
    }

    connect();

    return () => {
      closed = true;
      if (retryTimer) clearTimeout(retryTimer);
      if (changedTimer) clearTimeout(changedTimer);
      ctrl?.abort();
    };
  }, [queryClient, token, orgId, userId]);
}
