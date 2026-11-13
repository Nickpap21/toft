import type { components } from "./schema";
import { fetchAPI } from "./client";

export type ConfigFile = components["schemas"]["ConfigFile"];

export const configFileService = {
  list: (serviceId: string) =>
    fetchAPI<ConfigFile[]>(`/api/v1/services/${serviceId}/config-files`),

  upsert: (serviceId: string, input: { path: string; content: string }) =>
    fetchAPI<ConfigFile>(`/api/v1/services/${serviceId}/config-files`, {
      method: "PUT",
      body: JSON.stringify(input),
    }),

  delete: (serviceId: string, fileId: string) =>
    fetchAPI<void>(`/api/v1/services/${serviceId}/config-files/${fileId}`, {
      method: "DELETE",
    }),
};
