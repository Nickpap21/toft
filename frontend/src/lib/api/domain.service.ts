import { fetchAPI } from "./client";

export type DomainStatus = "pending" | "verified" | "failed";

export interface Domain {
    id: string;
    service_id: string;
    hostname: string;
    tls_enabled: boolean;
    status: DomainStatus;
    /** Who added it; "" when not recorded (before 0023). */
    created_by: string;
    /** The first deploy that routed it; null until then. */
    routed_at: string | null;
    /** When a valid certificate first answered for it; null until then (D-150). */
    certificate_at: string | null;
    /** Under sslip.io: for a first look (D-150). */
    temporary: boolean;
    created_at: string;
    updated_at: string;
}

/** A domain on the workspace's Domains page (GET /domains, D-48). */
export interface WorkspaceDomain {
    id: string;
    hostname: string;
    created_at: string;
    /** "" when not recorded. */
    created_by: string;
    routed_at: string | null;
    certificate_at: string | null;
    temporary: boolean;
    service_id: string;
    service_name: string;
    scope: "environment" | "project" | "org";
    /** "" for a workspace service. */
    project_id: string;
    project_name: string;
    /** "" for a project or workspace service. */
    environment_name: string;
    /** "" is the panel host. */
    server_id: string;
}

export const domainService = {
    /** Every domain of the workspace's services. */
    listWorkspace: () => fetchAPI<WorkspaceDomain[]>(`/api/v1/domains`),

    list: (serviceId: string) =>
        fetchAPI<Domain[]>(`/api/v1/services/${serviceId}/domains`),

    /** Traefik serves every domain over HTTPS with a Let's Encrypt certificate. */
    create: (serviceId: string, hostname: string, tlsEnabled = true) =>
        fetchAPI<Domain>(`/api/v1/services/${serviceId}/domains`, {
            method: "POST",
            body: JSON.stringify({ hostname, tls_enabled: tlsEnabled }),
        }),

    /** DNS, certificate and routing of each domain, looked up now. */
    check: (serviceId: string) =>
        fetchAPI<DomainCheck>(`/api/v1/services/${serviceId}/domains/check`),

    delete: (serviceId: string, domainId: string) =>
        fetchAPI<void>(`/api/v1/services/${serviceId}/domains/${domainId}`, {
            method: "DELETE",
        }),
};

export type DNSState = "here" | "elsewhere" | "none" | "unknown";
export type CertState = "valid" | "invalid" | "none" | "unknown";

export interface DomainCheck {
    checked_at: string;
    server: {
        name: string;
        /** Where the DNS must point; "" when not known. */
        address: string;
        wildcards_ok: boolean;
        /** The caller's workspace owns the server: the provider and provisioning are shown. */
        owner: boolean;
        dns_provider?: string;
        dns_provider_saved_at?: string;
        provisioned_at?: string;
    };
    /** A running container was read; otherwise nothing is routed. */
    routing_known: boolean;
    domains: {
        id: string;
        hostname: string;
        dns: DNSState;
        addresses: string[];
        certificate: CertState;
        not_after?: string;
        issuer?: string;
        routed: boolean;
    }[];
    /** Still routed by the running container, no longer the service's. */
    routed_removed: string[];
}
