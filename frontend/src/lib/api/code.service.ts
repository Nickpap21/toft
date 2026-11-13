import type { components } from "./schema";
import { fetchAPI, fetchBlob, fetchPage, reauthHeader, type Page } from "./client";

export type GitInfo = components["schemas"]["GitInfo"];
export type Repository = components["schemas"]["Repository"];
export type RepositoryDetail = components["schemas"]["RepositoryDetail"];
export type SSHKey = components["schemas"]["SSHKey"];
export type GitBranch = components["schemas"]["GitBranch"];
export type GitTag = components["schemas"]["GitTag"];
export type GitTree = components["schemas"]["GitTree"];
export type GitTreeEntry = components["schemas"]["GitTreeEntry"];
export type GitBlob = components["schemas"]["GitBlob"];
export type GitCommit = components["schemas"]["GitCommit"];
export type GitCommitDetail = components["schemas"]["GitCommitDetail"];
export type GitFileDiff = components["schemas"]["GitFileDiff"];
export type GitCompare = components["schemas"]["GitCompare"];
export type PullRequest = components["schemas"]["PullRequest"];
export type PullRequestDetail = components["schemas"]["PullRequestDetail"];
export type TimelineItem = components["schemas"]["PullRequestTimelineItem"];
export type MergeMethod = components["schemas"]["MergePullRequestInput"]["method"];
export type CheckSummary = components["schemas"]["CheckSummary"];
export type CommitCheck = components["schemas"]["CommitCheck"];
export type CommitChecks = components["schemas"]["CommitChecks"];
export type CheckState = CommitCheck["state"];
export type Pipeline = components["schemas"]["Pipeline"];
export type DeployTarget = components["schemas"]["DeployTarget"];
export type Mirror = components["schemas"]["Mirror"];
export type ImportInput = components["schemas"]["ImportInput"];
export type MirrorInput = components["schemas"]["MirrorInput"];
export type Webhook = components["schemas"]["Webhook"];
export type WebhookCreated = components["schemas"]["WebhookCreated"];
export type WebhookEvent = Webhook["events"][number];
export type WebhookDelivery = components["schemas"]["WebhookDelivery"];
export type WebhookDeliveryDetail = components["schemas"]["WebhookDeliveryDetail"];

/** ?ref, ?path, ?limit and ?cursor, the ones that are set. */
function query(q: Record<string, string | number | undefined>): string {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== "") p.set(k, String(v));
    const s = p.toString();
    return s ? `?${s}` : "";
}

/** Git hosting (D-71): whether this instance hosts git, a project's
 *  repositories and the account's SSH keys. */
export const codeService = {
    info(): Promise<GitInfo> {
        return fetchAPI<GitInfo>("/api/v1/git");
    },

    repositories(projectId: string): Promise<Repository[]> {
        return fetchAPI<Repository[]>(`/api/v1/projects/${projectId}/repositories`);
    },

    /** A new, empty repository in the project (D-75). */
    createRepository(projectId: string, name: string): Promise<Repository> {
        return fetchAPI<Repository>(`/api/v1/projects/${projectId}/repositories`, { method: "POST", body: JSON.stringify({ name }) });
    },

    sshKeys(): Promise<SSHKey[]> {
        return fetchAPI<SSHKey[]>("/api/v1/user/ssh-keys");
    },

    /** Needs the password confirmed first (useReauth): a key outlives the
     *  session that adds it. */
    addSSHKey(title: string, publicKey: string): Promise<SSHKey> {
        return fetchAPI<SSHKey>("/api/v1/user/ssh-keys", {
            method: "POST",
            headers: reauthHeader(),
            body: JSON.stringify({ title, public_key: publicKey }),
        });
    },

    deleteSSHKey(id: string): Promise<void> {
        return fetchAPI<void>(`/api/v1/user/ssh-keys/${id}`, { method: "DELETE" });
    },
};

/** The repository browser (D-73): read-only, every member of the workspace. */
export function repoService(projectId: string, repoId: string) {
    const base = `/api/v1/projects/${projectId}/repositories/${repoId}`;
    return {
        /** The repository's id, for query keys: never this object, whose
         *  functions React Query can neither hash nor match. */
        id: repoId,
        detail: () => fetchAPI<RepositoryDetail>(base),
        branches: (cursor?: string, limit?: number): Promise<Page<GitBranch[]>> =>
            fetchPage<GitBranch[]>(`${base}/branches${query({ cursor, limit })}`),
        tags: (cursor?: string, limit?: number): Promise<Page<GitTag[]>> =>
            fetchPage<GitTag[]>(`${base}/tags${query({ cursor, limit })}`),
        tree: (ref: string, path: string) => fetchAPI<GitTree>(`${base}/tree${query({ ref, path })}`),
        blob: (ref: string, path: string) => fetchAPI<GitBlob>(`${base}/blob${query({ ref, path })}`),
        raw: (ref: string, path: string) => fetchBlob(`${base}/raw${query({ ref, path })}`),
        commits: (ref: string, path: string, cursor?: string): Promise<Page<GitCommit[]>> =>
            fetchPage<GitCommit[]>(`${base}/commits${query({ ref, path, cursor })}`),
        commit: (sha: string) => fetchAPI<GitCommitDetail>(`${base}/commits/${sha}`),
        diff: (sha: string, cursor?: string): Promise<Page<GitFileDiff[]>> =>
            fetchPage<GitFileDiff[]>(`${base}/commits/${sha}/diff${query({ cursor })}`),

        // Settings (owners); changing the default branch, turning protection
        // off and deleting need the password confirmed first (useReauth).
        update: (change: { default_branch?: string; protect_default_branch?: boolean }) =>
            fetchAPI<RepositoryDetail>(base, {
                method: "PATCH",
                headers: change.default_branch !== undefined || change.protect_default_branch === false ? reauthHeader() : {},
                body: JSON.stringify(change),
            }),
        remove: (confirm: string) => fetchAPI<void>(`${base}${query({ confirm })}`, { method: "DELETE", headers: reauthHeader() }),

        // Pull requests (D-75).
        compare: (b: string, h: string) => fetchAPI<GitCompare>(`${base}/compare${query({ base: b, head: h })}`),
        compareDiff: (b: string, h: string, cursor?: string): Promise<Page<GitFileDiff[]>> =>
            fetchPage<GitFileDiff[]>(`${base}/compare/diff${query({ base: b, head: h, cursor })}`),
        pulls: (state: "open" | "merged" | "closed", cursor?: string): Promise<Page<PullRequest[]>> =>
            fetchPage<PullRequest[]>(`${base}/pulls${query({ state, cursor })}`),
        openPull: (input: { title: string; body: string; base: string; head: string }) =>
            fetchAPI<PullRequest>(`${base}/pulls`, { method: "POST", body: JSON.stringify(input) }),
        pull: (n: number) => fetchAPI<PullRequestDetail>(`${base}/pulls/${n}`),
        updatePull: (n: number, change: { title?: string; body?: string; state?: "open" | "closed" }) =>
            fetchAPI<PullRequest>(`${base}/pulls/${n}`, { method: "PATCH", body: JSON.stringify(change) }),
        pullDiff: (n: number, cursor?: string): Promise<Page<GitFileDiff[]>> =>
            fetchPage<GitFileDiff[]>(`${base}/pulls/${n}/diff${query({ cursor })}`),
        timeline: (n: number, cursor?: string): Promise<Page<TimelineItem[]>> =>
            fetchPage<TimelineItem[]>(`${base}/pulls/${n}/timeline${query({ cursor })}`),
        comment: (n: number, body: string) =>
            fetchAPI<TimelineItem>(`${base}/pulls/${n}/comments`, { method: "POST", body: JSON.stringify({ body }) }),
        editComment: (n: number, id: string, body: string) =>
            fetchAPI<TimelineItem>(`${base}/pulls/${n}/comments/${id}`, { method: "PATCH", body: JSON.stringify({ body }) }),
        deleteComment: (n: number, id: string) => fetchAPI<void>(`${base}/pulls/${n}/comments/${id}`, { method: "DELETE" }),
        merge: (n: number, input: { method: MergeMethod; expected_head_sha: string; delete_branch: boolean }) =>
            fetchAPI<PullRequest>(`${base}/pulls/${n}/merge`, { method: "POST", body: JSON.stringify(input) }),

        // Checks and pipelines (D-77).
        checks: (sha: string) => fetchAPI<CommitChecks>(`${base}/commits/${sha}/checks`),
        pipelines: (branch?: string, cursor?: string): Promise<Page<Pipeline[]>> =>
            fetchPage<Pipeline[]>(`${base}/pipelines${query({ branch, cursor, limit: 25 })}`),

        // Import and mirrors (D-79); adding a push mirror needs the password
        // confirmed first (useReauth).
        importFrom: (input: ImportInput) => fetchAPI<Mirror>(`${base}/import`, { method: "POST", body: JSON.stringify(input) }),
        mirrors: () => fetchAPI<Mirror[]>(`${base}/mirrors`),
        addPushMirror: (input: MirrorInput) =>
            fetchAPI<Mirror>(`${base}/mirrors`, { method: "POST", headers: reauthHeader(), body: JSON.stringify(input) }),
        removeMirror: (id: string) => fetchAPI<void>(`${base}/mirrors/${id}`, { method: "DELETE" }),
        syncMirror: (id: string) => fetchAPI<Mirror>(`${base}/mirrors/${id}/sync`, { method: "POST" }),

        // Outbound webhooks (D-79, owners).
        webhooks: () => fetchAPI<Webhook[]>(`${base}/webhooks`),
        webhook: (id: string) => fetchAPI<Webhook>(`${base}/webhooks/${id}`),
        createWebhook: (input: { url: string; events: WebhookEvent[] }) =>
            fetchAPI<WebhookCreated>(`${base}/webhooks`, { method: "POST", body: JSON.stringify(input) }),
        updateWebhook: (id: string, change: { url?: string; events?: WebhookEvent[]; active?: boolean }) =>
            fetchAPI<Webhook>(`${base}/webhooks/${id}`, { method: "PATCH", body: JSON.stringify(change) }),
        deleteWebhook: (id: string) => fetchAPI<void>(`${base}/webhooks/${id}`, { method: "DELETE" }),
        rotateWebhookSecret: (id: string) => fetchAPI<{ secret: string }>(`${base}/webhooks/${id}/secret`, { method: "POST" }),
        pingWebhook: (id: string) => fetchAPI<WebhookDelivery>(`${base}/webhooks/${id}/test`, { method: "POST" }),
        deliveries: (id: string, cursor?: string): Promise<Page<WebhookDelivery[]>> =>
            fetchPage<WebhookDelivery[]>(`${base}/webhooks/${id}/deliveries${query({ cursor, limit: 25 })}`),
        delivery: (id: string, deliveryId: string) => fetchAPI<WebhookDeliveryDetail>(`${base}/webhooks/${id}/deliveries/${deliveryId}`),
        redeliver: (id: string, deliveryId: string) =>
            fetchAPI<WebhookDelivery>(`${base}/webhooks/${id}/deliveries/${deliveryId}/redeliver`, { method: "POST" }),
    };
}

/** GET /git, cached for the session: the instance doesn't change it while
 *  it runs. */
export const gitInfoQuery = {
    queryKey: ["git"],
    queryFn: () => codeService.info(),
    staleTime: Infinity,
} as const;
