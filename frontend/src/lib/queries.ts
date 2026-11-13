import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { environmentService } from "@/lib/api/environment.service";
import { inboxService } from "@/lib/api/inbox.service";
import { projectService } from "@/lib/api/project.service";
import { serverService } from "@/lib/api/server.service";
import { serviceService } from "@/lib/api/service.service";

/**
 * Query options shared by the shell (sidebar, ⌘K). The keys and
 * fetchers are the ones today's pages already use, so the shell shares their
 * cache instead of adding requests. Pages move onto these in R2.
 */

export const projectsQuery = queryOptions({
  queryKey: ["projects"],
  queryFn: () => projectService.list(),
});

export const projectQuery = (id: string) =>
  queryOptions({
    queryKey: ["project", id],
    queryFn: () => projectService.get(id),
    enabled: !!id,
  });

/** Every service in a project, all environments. Feeds the health dots. */
export const projectServicesQuery = (id: string) =>
  queryOptions({
    queryKey: ["project-services", id],
    queryFn: () => serviceService.listForProject(id),
    enabled: !!id,
    staleTime: 30_000,
  });

export const environmentsQuery = (projectId: string) =>
  queryOptions({
    queryKey: ["environments", projectId],
    queryFn: () => environmentService.list(projectId),
    enabled: !!projectId,
    staleTime: 60_000,
  });

export const serviceQuery = (id: string) =>
  queryOptions({
    queryKey: ["service", id],
    queryFn: () => serviceService.get(id),
    enabled: !!id,
  });

export const serversQuery = queryOptions({
  queryKey: ["servers"],
  queryFn: () => serverService.list(),
  staleTime: 60_000,
});

/** Every service in the workspace, in one request: the sidebar's health dots
 *  and ⌘K's search. Refreshed with ["project-services"] (providers.tsx,
 *  useEventStream). */
export const workspaceServicesQuery = queryOptions({
  queryKey: ["workspace-services"],
  queryFn: () => serviceService.listForWorkspace(),
  staleTime: 30_000,
});

/** Every environment of the workspace: the Projects table and workspace
 *  services' reach. */
export const workspaceEnvironmentsQuery = queryOptions({
  queryKey: ["workspace-environments"],
  queryFn: () => environmentService.listWorkspace(),
  staleTime: 60_000,
});

/** A project's Overview beside its services. Refreshed with deploy events
 *  (["project-overview"], useEventStream). */
export const projectOverviewQuery = (projectId: string) =>
  queryOptions({
    queryKey: ["project-overview", projectId],
    queryFn: () => environmentService.projectOverview(projectId),
    enabled: !!projectId,
  });

/** Needs you (D-88), polled every 30 s wherever it is drawn: Home, the
 *  project page, the Inbox and the sidebar's badge agree. */
export const needsYouQuery = (done = false) =>
  queryOptions({
    queryKey: ["inbox", "needs-you", done],
    queryFn: () => inboxService.needsYou(done),
    refetchInterval: 30_000,
  });

/** What the count badges read: the inbox (Needs you, My issues), a project
 *  (open issues, doc pages) and the projects list (Home's cards). Only the
 *  mounted queries refetch. */
export function refreshCounts(client: QueryClient) {
  for (const key of ["inbox", "project", "projects"]) void client.invalidateQueries({ queryKey: [key] });
}
