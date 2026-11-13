import type { components } from "./schema";
import { fetchAPI, fetchPage, reauthHeader, streamEvents, type Page } from "./client";

export type DocTreePage = components["schemas"]["DocTreePage"];
export type DocPage = components["schemas"]["DocPage"];
export type DocBlock = components["schemas"]["DocBlock"];
export type DocService = components["schemas"]["DocService"];
export type DocChange = components["schemas"]["DocChange"];
export type DocRef = components["schemas"]["DocRef"];
export type DocVersion = components["schemas"]["DocVersion"];
export type DocVersionDetail = components["schemas"]["DocVersionDetail"];
export type ServiceWork = components["schemas"]["ServiceWork"];
export type CreateDocPageInput = components["schemas"]["CreateDocPageInput"];
export type UpdateDocPageInput = components["schemas"]["UpdateDocPageInput"];

/** A project's docs (D-86). */
export function docService(projectId: string) {
    const base = `/api/v1/projects/${projectId}/docs`;
    return {
        tree(deleted = false): Promise<DocTreePage[]> {
            return fetchAPI<DocTreePage[]>(deleted ? `${base}?deleted=true` : base);
        },
        get(pageId: string): Promise<DocPage> {
            return fetchAPI<DocPage>(`${base}/${pageId}`);
        },
        create(input: CreateDocPageInput): Promise<DocPage> {
            return fetchAPI<DocPage>(base, { method: "POST", body: JSON.stringify(input) });
        },
        update(pageId: string, input: UpdateDocPageInput): Promise<DocPage> {
            return fetchAPI<DocPage>(`${base}/${pageId}`, { method: "PATCH", body: JSON.stringify(input) });
        },
        remove(pageId: string): Promise<void> {
            return fetchAPI<void>(`${base}/${pageId}`, { method: "DELETE" });
        },
        restore(pageId: string): Promise<DocPage> {
            return fetchAPI<DocPage>(`${base}/${pageId}/restore`, { method: "POST" });
        },
        verify(pageId: string): Promise<DocPage> {
            return fetchAPI<DocPage>(`${base}/${pageId}/verify`, { method: "POST" });
        },
        versions(pageId: string, cursor?: string): Promise<Page<DocVersion[]>> {
            return fetchPage<DocVersion[]>(`${base}/${pageId}/versions${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`);
        },
        version(pageId: string, version: number): Promise<DocVersionDetail> {
            return fetchAPI<DocVersionDetail>(`${base}/${pageId}/versions/${version}`);
        },
        /** Runs the saved page's command number step; a host step sends the
         *  password's proof (D-53). Returns a stop function. */
        run(pageId: string, step: number, version: number, host: boolean,
            onLine: (line: string) => void, onDone: () => void, onError: (msg: string) => void): () => void {
            const ctrl = new AbortController();
            streamEvents(`${base}/${pageId}/steps/${step}/run`, (type, data) => {
                if (type === "done") ctrl.abort();
                else if (data) onLine(data);
            }, ctrl.signal, {
                method: "POST",
                body: JSON.stringify({ version }),
                headers: host ? reauthHeader() : undefined,
            }).then(onDone, (err: Error) => {
                if (err.name !== "AbortError") onError(err.message ?? "The command failed");
                onDone();
            });
            return () => ctrl.abort();
        },
    };
}

export const docTreeQuery = (projectId: string) => ({
    queryKey: ["docs", projectId, "tree"],
    queryFn: () => docService(projectId).tree(),
    staleTime: 15_000,
});

export const docPageQuery = (projectId: string, pageId: string) => ({
    queryKey: ["docs", projectId, "page", pageId],
    queryFn: () => docService(projectId).get(pageId),
});

export function serviceWork(serviceId: string): Promise<ServiceWork> {
    return fetchAPI<ServiceWork>(`/api/v1/services/${serviceId}/work`);
}
