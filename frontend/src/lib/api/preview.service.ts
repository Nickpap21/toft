import type { components } from "./schema";
import { fetchAPI, fetchPage, type Page } from "./client";

export type Preview = components["schemas"]["Preview"];
export type PreviewService = components["schemas"]["PreviewService"];
export type PreviewStatus = Preview["status"];
export type PreviewSettings = components["schemas"]["PreviewSettings"];
export type PreviewDomainCheck = components["schemas"]["PreviewDomainCheck"];

/** Pull requests' previews (D-97): a project's settings and open previews,
 *  and one pull request's preview with what owners and developers do to it. */
export const previewService = {
    list(projectId: string, cursor?: string): Promise<Page<Preview[]>> {
        const q = cursor ? `?cursor=${encodeURIComponent(cursor)}` : "";
        return fetchPage<Preview[]>(`/api/v1/projects/${projectId}/previews${q}`);
    },
    settings(projectId: string): Promise<PreviewSettings> {
        return fetchAPI<PreviewSettings>(`/api/v1/projects/${projectId}/previews/settings`);
    },
    /** max: how many run at once (1 to 20); omitted keeps the project's. */
    saveSettings(projectId: string, environmentId: string | null, domain: string | null, max?: number): Promise<PreviewSettings> {
        return fetchAPI<PreviewSettings>(`/api/v1/projects/${projectId}/previews/settings`, {
            method: "PUT",
            body: JSON.stringify({ environment_id: environmentId, domain, max_per_project: max }),
        });
    },
    domainCheck(projectId: string): Promise<PreviewDomainCheck> {
        return fetchAPI<PreviewDomainCheck>(`/api/v1/projects/${projectId}/previews/domain-check`);
    },
    /** One pull request's preview, as a repository's code API sees it. */
    of(projectId: string, repositoryId: string, number: number) {
        const base = `/api/v1/projects/${projectId}/repositories/${repositoryId}/pulls/${number}/preview`;
        return {
            get: () => fetchAPI<Preview>(base),
            start: () => fetchAPI<Preview>(base, { method: "POST" }),
            approve: (sha: string) => fetchAPI<Preview>(`${base}/approve`, { method: "POST", body: JSON.stringify({ sha }) }),
            stop: () => fetchAPI<Preview>(`${base}/stop`, { method: "POST" }),
            redeploy: () => fetchAPI<Preview>(`${base}/redeploy`, { method: "POST" }),
        };
    },
};
