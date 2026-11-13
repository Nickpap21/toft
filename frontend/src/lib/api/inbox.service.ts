import type { components } from "./schema";
import { fetchAPI, fetchPage, type Page } from "./client";

export type NeedsYouItem = components["schemas"]["NeedsYouItem"];
export type InboxUpdate = components["schemas"]["InboxUpdate"];
export type InboxCounts = components["schemas"]["InboxCounts"];
export type MyIssue = components["schemas"]["MyIssue"];

/** A person's inbox (D-88). */
export const inboxService = {
    /** done "any": every item, marked or not, for a screen that shows state. */
    needsYou(done: boolean | "any" = false): Promise<NeedsYouItem[]> {
        return fetchAPI<NeedsYouItem[]>(`/api/v1/inbox/needs-you${done ? `?done=${done}` : ""}`);
    },
    updates(q: { kind?: string[]; unread?: boolean; done?: boolean }, cursor?: string): Promise<Page<InboxUpdate[]>> {
        const p = new URLSearchParams();
        if (q.kind?.length) p.set("kind", q.kind.join(","));
        if (q.unread) p.set("unread", "true");
        if (q.done) p.set("done", "true");
        if (cursor) p.set("cursor", cursor);
        const s = p.toString();
        return fetchPage<InboxUpdate[]>(`/api/v1/inbox/updates${s ? `?${s}` : ""}`);
    },
    counts(): Promise<InboxCounts> {
        return fetchAPI<InboxCounts>("/api/v1/inbox/counts");
    },
    done(keys: string[], updates: string[], done = true): Promise<void> {
        return fetchAPI<void>(`/api/v1/inbox/${done ? "done" : "undone"}`, { method: "POST", body: JSON.stringify({ keys, updates }) });
    },
    read(updates: string[], all = false): Promise<void> {
        return fetchAPI<void>("/api/v1/inbox/read", { method: "POST", body: JSON.stringify({ updates, all }) });
    },
    myIssues(): Promise<MyIssue[]> {
        return fetchAPI<MyIssue[]>("/api/v1/workspace/my-issues");
    },
    /** Opens an issue from a failed deploy; 409 already_open names the open one. */
    openFromFailure(serviceId: string, deploymentId: string) {
        return fetchAPI<components["schemas"]["IssueDetail"]>(`/api/v1/services/${serviceId}/deployments/${deploymentId}/issue`, { method: "POST" });
    },
};

export const inboxCountsQuery = {
    queryKey: ["inbox", "counts"],
    queryFn: () => inboxService.counts(),
    staleTime: 30_000,
    refetchInterval: 120_000,
};
