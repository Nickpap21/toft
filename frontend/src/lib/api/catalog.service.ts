import type { components } from "./schema";
import { fetchAPI } from "./client";

export interface CatalogEnvVarSpec {
    key: string;
    description: string;
    default: string;
    is_secret: boolean;
}

export interface CatalogVolumeMount {
    name: string;
    target: string;
}

export type CatalogTemplate = components["schemas"]["Template"];

export const catalogService = {
    list: () => fetchAPI<CatalogTemplate[]>("/api/v1/catalog"),
    get: (slug: string) => fetchAPI<CatalogTemplate>(`/api/v1/catalog/${slug}`),
};
