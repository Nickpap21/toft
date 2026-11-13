import type { components } from "./schema";
import { fetchAPI, fetchPage, fetchVersioned, type Page } from "./client";

export type Issue = components["schemas"]["Issue"];
export type IssueDetail = components["schemas"]["IssueDetail"];
export type IssueStatus = components["schemas"]["IssueStatus"];
export type IssuePriority = components["schemas"]["IssuePriority"];
export type IssueLink = components["schemas"]["IssueLink"];
export type IssueDeploy = components["schemas"]["IssueDeploy"];
export type IssueRef = components["schemas"]["IssueRef"];
export type IssueWork = components["schemas"]["IssueWork"];
export type IssueCounts = components["schemas"]["IssueCounts"];
export type IssueTimelineItem = components["schemas"]["IssueTimelineItem"];
export type IssueLabel = components["schemas"]["IssueLabel"];
export type IssueView = components["schemas"]["IssueView"];
export type IssueFilters = components["schemas"]["IssueFilters"];
export type CreateIssueInput = components["schemas"]["CreateIssueInput"];
export type UpdateIssueInput = components["schemas"]["UpdateIssueInput"];
/** An issue as read: version is its labels', what a change of them must send
 *  back. */
export type IssueRead = IssueDetail & { version: string };
/** A change of an issue. Sending labels replaces them, so it needs the
 *  version they were read with: the API refuses without one or with a stale
 *  one (changedSinceRead). */
export type IssueChange = Omit<UpdateIssueInput, "labels" | "version"> & ({ labels?: undefined; version?: undefined } | { labels: string[]; version: string });
export type WorkspaceSearch = components["schemas"]["WorkspaceSearch"];
export type IssueTypeRef = components["schemas"]["IssueTypeRef"];
export type IssueType = components["schemas"]["IssueType"];
export type IssueTypeInput = components["schemas"]["IssueTypeInput"];
export type IssueTypeUpdate = components["schemas"]["IssueTypeUpdate"];
export type IssueRelation = components["schemas"]["IssueRelation"];
export type IssueRelated = components["schemas"]["IssueRelated"];
export type RelationKind = components["schemas"]["IssueRelationInput"]["kind"];
export type ProjectIssueSettings = components["schemas"]["ProjectIssueSettings"];
export type ProjectIssueSettingsInput = components["schemas"]["ProjectIssueSettingsInput"];
/** A project's settings as read: version is its hidden types', what a change
 *  of them must send back. */
export type ProjectIssueSettingsRead = ProjectIssueSettings & { version: string };
/** A change of a project's settings. Sending hidden_type_ids replaces them,
 *  so it needs the version they were read with (changedSinceRead). */
export type ProjectIssueSettingsChange = Omit<ProjectIssueSettingsInput, "hidden_type_ids" | "version"> & ({ hidden_type_ids?: undefined; version?: undefined } | { hidden_type_ids: string[]; version: string });
export type CloneIssueInput = components["schemas"]["CloneIssueInput"];
export type Cycle = components["schemas"]["Cycle"];
export type CycleRef = components["schemas"]["CycleRef"];
export type CycleDetail = components["schemas"]["CycleDetail"];
export type CycleIssue = components["schemas"]["CycleIssue"];
export type CycleSchedule = components["schemas"]["CycleSchedule"];
export type ProjectCycles = components["schemas"]["ProjectCycles"];
export type EstimateScale = components["schemas"]["EstimateScale"];
export type WorkspaceIssueSettings = components["schemas"]["WorkspaceIssueSettings"];
export type WorkspaceIssueSettingsInput = components["schemas"]["WorkspaceIssueSettingsInput"];
export type IssueTimeline = components["schemas"]["IssueTimeline"];
export type IssueTimelineEntry = components["schemas"]["IssueTimelineEntry"];

/** The list's filters as the API takes them: several values comma-joined. */
export interface IssueQuery {
    status?: IssueStatus[];
    priority?: IssuePriority[];
    assignee?: string;
    label?: string[];
    q?: string;
    type?: string[];
    parent?: string;
    /** current, next, none or a cycle's number (D-90). */
    cycle?: string;
    order?: "priority" | "updated" | "created" | "due";
}

function query(q: Record<string, string | number | string[] | undefined>): string {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) {
        if (v === undefined || v === "" || (Array.isArray(v) && v.length === 0)) continue;
        p.set(k, Array.isArray(v) ? v.join(",") : String(v));
    }
    const s = p.toString();
    return s ? `?${s}` : "";
}

/** A project's issues (D-81). */
export function issueService(projectId: string) {
    const base = `/api/v1/projects/${projectId}`;
    return {
        list(q: IssueQuery, cursor?: string, limit = 100): Promise<Page<Issue[]>> {
            return fetchPage<Issue[]>(`${base}/issues${query({ ...q, limit, cursor })}`);
        },
        counts(q: IssueQuery): Promise<IssueCounts> {
            return fetchAPI<IssueCounts>(
                `${base}/issue-counts${query({ q: q.q, priority: q.priority, assignee: q.assignee, label: q.label, type: q.type, parent: q.parent, cycle: q.cycle })}`,
            );
        },
        async get(n: number): Promise<IssueRead> {
            const { data, version } = await fetchVersioned<IssueDetail>(`${base}/issues/${n}`);
            return { ...data, version };
        },
        create(input: CreateIssueInput): Promise<IssueDetail> {
            return fetchAPI<IssueDetail>(`${base}/issues`, { method: "POST", body: JSON.stringify(input) });
        },
        async update(n: number, input: IssueChange): Promise<IssueRead> {
            const { data, version } = await fetchVersioned<IssueDetail>(`${base}/issues/${n}`, { method: "PATCH", body: JSON.stringify(input) });
            return { ...data, version };
        },
        timeline(n: number, cursor?: string): Promise<Page<IssueTimelineItem[]>> {
            return fetchPage<IssueTimelineItem[]>(`${base}/issues/${n}/timeline${query({ limit: 200, cursor })}`);
        },
        comment(n: number, body: string): Promise<IssueTimelineItem> {
            return fetchAPI<IssueTimelineItem>(`${base}/issues/${n}/comments`, { method: "POST", body: JSON.stringify({ body }) });
        },
        editComment(n: number, id: string, body: string): Promise<IssueTimelineItem> {
            return fetchAPI<IssueTimelineItem>(`${base}/issues/${n}/comments/${id}`, { method: "PATCH", body: JSON.stringify({ body }) });
        },
        deleteComment(n: number, id: string): Promise<void> {
            return fetchAPI<void>(`${base}/issues/${n}/comments/${id}`, { method: "DELETE" });
        },
        labels(): Promise<IssueLabel[]> {
            return fetchAPI<IssueLabel[]>(`${base}/issue-labels`);
        },
        views(): Promise<IssueView[]> {
            return fetchAPI<IssueView[]>(`${base}/issue-views`);
        },
        saveView(name: string, filters: IssueFilters): Promise<IssueView> {
            return fetchAPI<IssueView>(`${base}/issue-views`, { method: "POST", body: JSON.stringify({ name, filters }) });
        },
        updateView(id: string, name: string, filters: IssueFilters): Promise<IssueView> {
            return fetchAPI<IssueView>(`${base}/issue-views/${id}`, { method: "PATCH", body: JSON.stringify({ name, filters }) });
        },
        deleteView(id: string): Promise<void> {
            return fetchAPI<void>(`${base}/issue-views/${id}`, { method: "DELETE" });
        },
        relate(n: number, kind: RelationKind, issue: string): Promise<IssueDetail> {
            return fetchAPI<IssueDetail>(`${base}/issues/${n}/relations`, { method: "POST", body: JSON.stringify({ kind, issue }) });
        },
        unrelate(n: number, id: string): Promise<void> {
            return fetchAPI<void>(`${base}/issues/${n}/relations/${id}`, { method: "DELETE" });
        },
        clone(n: number, input: CloneIssueInput): Promise<IssueDetail> {
            return fetchAPI<IssueDetail>(`${base}/issues/${n}/clone`, { method: "POST", body: JSON.stringify(input) });
        },
        async settings(): Promise<ProjectIssueSettingsRead> {
            const { data, version } = await fetchVersioned<ProjectIssueSettings>(`${base}/issue-settings`);
            return { ...data, version };
        },
        async saveSettings(input: ProjectIssueSettingsChange): Promise<ProjectIssueSettingsRead> {
            const { data, version } = await fetchVersioned<ProjectIssueSettings>(`${base}/issue-settings`, { method: "PATCH", body: JSON.stringify(input) });
            return { ...data, version };
        },
        cycles(): Promise<ProjectCycles> {
            return fetchAPI<ProjectCycles>(`${base}/cycles`);
        },
        issueTimeline(q: { from?: string; assignee?: string; label?: string[]; type?: string[] }): Promise<IssueTimeline> {
            return fetchAPI<IssueTimeline>(`${base}/issue-timeline${query(q)}`);
        },
    };
}

/** The workspace's cycles (D-90). */
export const cycleService = {
    list: (state?: "upcoming" | "current" | "ended", cursor?: string, limit = 25) =>
        fetchPage<Cycle[]>(`/api/v1/workspace/cycles${query({ state, cursor, limit })}`),
    get: (n: number) => fetchAPI<CycleDetail>(`/api/v1/workspace/cycles/${n}`),
    rename: (n: number, name: string) => fetchAPI<CycleDetail>(`/api/v1/workspace/cycles/${n}`, { method: "PATCH", body: JSON.stringify({ name }) }),
    issues: (n: number, projectId?: string) => fetchPage<CycleIssue[]>(`/api/v1/workspace/cycles/${n}/issues${query({ project_id: projectId, limit: 500 })}`),
};

/** The workspace's issue types and defaults (D-83). */
export const issueTypeService = {
    list: () => fetchAPI<IssueType[]>("/api/v1/workspace/issue-types"),
    add: (input: IssueTypeInput) => fetchAPI<IssueType>("/api/v1/workspace/issue-types", { method: "POST", body: JSON.stringify(input) }),
    update: (id: string, input: IssueTypeUpdate) =>
        fetchAPI<IssueType>(`/api/v1/workspace/issue-types/${id}`, { method: "PATCH", body: JSON.stringify(input) }),
    archive: (id: string) => fetchAPI<void>(`/api/v1/workspace/issue-types/${id}`, { method: "DELETE" }),
    settings: () => fetchAPI<WorkspaceIssueSettings>("/api/v1/workspace/issue-settings"),
    saveSettings: (input: WorkspaceIssueSettingsInput) =>
        fetchAPI<WorkspaceIssueSettings>("/api/v1/workspace/issue-settings", { method: "PATCH", body: JSON.stringify(input) }),
};

export const issueTypesQuery = { queryKey: ["issue-types"], queryFn: () => issueTypeService.list(), staleTime: 60_000 };

/** ⌘K's search over the workspace's issues and pull requests (D-81). */
export function searchWorkspace(q: string, types?: "issues" | "pulls" | "docs", projectId?: string, limit?: number): Promise<WorkspaceSearch> {
    return fetchAPI<WorkspaceSearch>(`/api/v1/workspace/search${query({ q, types, project_id: projectId, limit })}`);
}
