import type { components } from "./schema";
import { fetchAPI, fetchPage, type Page } from "./client";

export type APIKey = components["schemas"]["APIKey"];

/** deploy: deploy, follow and roll back the project's services.
 *  variables: their env vars and secrets, and their environments'.
 *  code_read, code_write: clone the project's repository; push too (D-71). */
export type APIKeyScope = NonNullable<components["schemas"]["ApikeyCreateInput"]["scopes"]>[number];

export type CreatedAPIKey = components["schemas"]["CreatedResponse"];

export type ProjectAPIKey = components["schemas"]["ProjectAPIKey"];

export const apiKeyService = {
    list(): Promise<APIKey[]> {
        return fetchAPI<APIKey[]>("/api/v1/user/api-keys");
    },

    /** The keys scoped to a project: an owner gets every member's, anyone
     *  else their own. The first page (50); next says more follow. */
    listForProject(projectId: string): Promise<Page<ProjectAPIKey[]>> {
        return fetchPage<ProjectAPIKey[]>(`/api/v1/projects/${projectId}/api-keys`);
    },

    /** project: a key limited to one project's deploys and/or variables,
     *  and with serviceIds to some of its services (deploy and variables
     *  only); omitted: a key with everything the user can do. expiresAt:
     *  null never expires. */
    create(
        name: string,
        project: { id: string; scopes: APIKeyScope[]; serviceIds: string[] } | undefined,
        expiresAt: string | null,
    ): Promise<CreatedAPIKey> {
        return fetchAPI<CreatedAPIKey>("/api/v1/user/api-keys", {
            method: "POST",
            body: JSON.stringify(
                project
                    ? { name, project_id: project.id, scopes: project.scopes, service_ids: project.serviceIds, expires_at: expiresAt }
                    : { name, expires_at: expiresAt },
            ),
        });
    },

    delete(keyId: string): Promise<void> {
        return fetchAPI<void>(`/api/v1/user/api-keys/${keyId}`, {
            method: "DELETE",
        });
    },
};
