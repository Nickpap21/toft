import type { components } from "./schema";
import { fetchAPI, reauthHeader } from "./client";

export type AgentConnection = components["schemas"]["AgentConnection"];
export type AgentPolicy = components["schemas"]["AgentPolicy"];
export type AgentPolicyCell = components["schemas"]["AgentPolicyCell"];
export type AgentProjectPolicyCell = components["schemas"]["AgentProjectPolicyCell"];
export type AgentPause = components["schemas"]["AgentPause"];
export type AgentWaiting = components["schemas"]["AgentWaiting"];
export type AgentApproval = components["schemas"]["AgentApproval"];
export type AgentClientView = components["schemas"]["AgentClientView"];
export type AgentConsentInput = components["schemas"]["AgentConsentInput"];

/** Agents over MCP (D-100): the consent screen, connected agents, the
 *  workspace's policy, requests waiting for a person, and pause-all. */
export const agentService = {
    authorizeRequest: (query: string) => fetchAPI<AgentClientView>(`/api/v1/oauth/authorize/request?${query}`),
    authorize: (input: AgentConsentInput) =>
        fetchAPI<{ redirect_to: string }>("/api/v1/oauth/authorize", { method: "POST", body: JSON.stringify(input) }),

    connections: () => fetchAPI<AgentConnection[]>("/api/v1/agents/connections"),
    revoke: (id: string) => fetchAPI<void>(`/api/v1/agents/connections/${id}`, { method: "DELETE" }),

    policy: () => fetchAPI<AgentPolicy>("/api/v1/agents/policy"),
    /** Loosening a cell needs the password (reauthHeader). */
    setPolicy: (cells: AgentPolicyCell[]) =>
        fetchAPI<AgentPolicy>("/api/v1/agents/policy", { method: "PUT", body: JSON.stringify({ cells }), headers: reauthHeader() }),
    /** A project's own cells (D-101); decision "" drops one. Loosening needs the password. */
    setProjectPolicy: (projectId: string, cells: AgentProjectPolicyCell[]) =>
        fetchAPI<AgentPolicy>(`/api/v1/agents/policy/projects/${projectId}`, { method: "PUT", body: JSON.stringify({ cells }), headers: reauthHeader() }),
    resetProjectPolicy: (projectId: string) =>
        fetchAPI<AgentPolicy>(`/api/v1/agents/policy/projects/${projectId}`, { method: "DELETE", headers: reauthHeader() }),

    pause: () => fetchAPI<AgentPause>("/api/v1/agents/pause"),
    pauseWorkspace: () => fetchAPI<AgentPause>("/api/v1/agents/pause", { method: "POST" }),
    resumeWorkspace: () => fetchAPI<AgentPause>("/api/v1/agents/pause", { method: "DELETE", headers: reauthHeader() }),
    pauseInstance: () => fetchAPI<AgentPause>("/api/v1/instance/agents/pause", { method: "POST" }),
    resumeInstance: () => fetchAPI<AgentPause>("/api/v1/instance/agents/pause", { method: "DELETE", headers: reauthHeader() }),

    waiting: () => fetchAPI<AgentWaiting[]>("/api/v1/agents/approvals"),
    approval: (id: string) => fetchAPI<AgentApproval>(`/api/v1/agents/approvals/${id}`),
    approve: (id: string, note: string, commit?: string) =>
        fetchAPI<AgentApproval>(`/api/v1/agents/approvals/${id}/approve`, { method: "POST", body: JSON.stringify({ note, commit }) }),
    deny: (id: string, note: string) =>
        fetchAPI<AgentApproval>(`/api/v1/agents/approvals/${id}/deny`, { method: "POST", body: JSON.stringify({ note }) }),
};
