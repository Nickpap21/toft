import type { components } from "./schema";
import { fetchAPI, reauthHeader } from "./client";

/** The colours an environment's chip can take (D-55). */
export const ENV_COLORS = ["amber", "violet", "blue", "teal", "gray"] as const;
export type EnvColor = (typeof ENV_COLORS)[number];

export type Environment = components["schemas"]["Environment"];

/** An environment as GET /workspace/environments lists it. */
export type WorkspaceEnvironment = components["schemas"]["WorkspaceEnvironment"];

/** A project's Overview beside its services (GET /projects/:id/overview). */
export type ProjectOverview = components["schemas"]["ProjectOverview"];

export const environmentService = {
    listWorkspace: () => fetchAPI<WorkspaceEnvironment[]>(`/api/v1/workspace/environments`),

    projectOverview: (projectId: string) => fetchAPI<ProjectOverview>(`/api/v1/projects/${projectId}/overview`),

    list: (projectId: string) =>
        fetchAPI<Environment[]>(`/api/v1/projects/${projectId}/environments`),

    create: (projectId: string, name: string) =>
        fetchAPI<Environment>(`/api/v1/projects/${projectId}/environments`, {
            method: "POST",
            body: JSON.stringify({ name }),
        }),

    /** A PATCH: fields left out keep their value. Lifting protection is the
     *  owners', with the password (reauthHeader). */
    update: (environmentId: string, patch: { name?: string; color?: EnvColor; protected?: boolean }) =>
        fetchAPI<Environment>(`/api/v1/environments/${environmentId}`, {
            method: "PATCH",
            body: JSON.stringify(patch),
            headers: patch.protected === false ? reauthHeader() : undefined,
        }),

    /** Owners, with the password (reauthHeader) and the environment's name typed. */
    delete: (environmentId: string, confirm: string) =>
        fetchAPI<void>(`/api/v1/environments/${environmentId}?confirm=${encodeURIComponent(confirm)}`, { method: "DELETE", headers: reauthHeader() }),
};
