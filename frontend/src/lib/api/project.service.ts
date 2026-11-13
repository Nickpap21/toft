import type { components } from "./schema";
import { fetchAPI, reauthHeader } from "./client";

export type Project = components["schemas"]["Project"];

/** The project's GitHub webhook: one URL and secret for every repository
 *  it deploys from, and the last signed push GitHub sent. */
export type ProjectWebhook = components["schemas"]["WebhookInfo"];

export type CreateProjectInput = components["schemas"]["CreateProjectInput"];

/** A created project, with its first service when the create carried one (7g). */
export type CreatedProject = components["schemas"]["CreatedProject"];

export type UpdateProjectInput = components["schemas"]["UpdateProjectInput"];

export const projectService = {
    /** Owners and developers only; created the first time it is opened. */
    getWebhook: (id: string) => fetchAPI<ProjectWebhook>(`/api/v1/projects/${id}/webhook`),

    /** A new secret: pushes signed with the old one stop deploying. */
    rotateWebhook: (id: string) =>
        fetchAPI<ProjectWebhook>(`/api/v1/projects/${id}/webhook/rotate`, { method: "POST" }),

    list: () => fetchAPI<Project[]>("/api/v1/projects"),

    get: (id: string) => fetchAPI<Project>(`/api/v1/projects/${id}`),

    /** With service, the project and its first service in one transaction (7g). */
    create: (input: CreateProjectInput) =>
        fetchAPI<CreatedProject>("/api/v1/projects", {
            method: "POST",
            body: JSON.stringify(input),
        }),

    update: (id: string, input: UpdateProjectInput) =>
        fetchAPI<Project>(`/api/v1/projects/${id}`, {
            method: "PUT",
            body: JSON.stringify(input),
        }),

    /** Owners, with the password (reauthHeader) and the project's name typed. */
    delete: (id: string, confirm: string) =>
        fetchAPI<void>(`/api/v1/projects/${id}?confirm=${encodeURIComponent(confirm)}`, { method: "DELETE", headers: reauthHeader() }),
};
