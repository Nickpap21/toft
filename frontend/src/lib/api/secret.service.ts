import { fetchAPI } from "./client";
import { scopePath, type VarScope } from "./envvar.service";

export interface Secret {
  id: string;
  service_id?: string;
  environment_id?: string;
  key: string;
  created_at: string;
  updated_at: string;
  /** Who last set it; "" when not recorded. */
  updated_by?: string;
}

export const secretService = {
  list: (scope: VarScope): Promise<Secret[]> =>
    fetchAPI(`${scopePath(scope)}/secrets`),

  create: (scope: VarScope, key: string, value: string): Promise<Secret> =>
    fetchAPI(`${scopePath(scope)}/secrets`, {
      method: "POST",
      body: JSON.stringify({ key, value }),
    }),

  replace: (scope: VarScope, key: string, value: string): Promise<Secret> =>
    fetchAPI(`${scopePath(scope)}/secrets/${encodeURIComponent(key)}`, {
      method: "PUT",
      body: JSON.stringify({ value }),
    }),

  delete: (scope: VarScope, key: string): Promise<void> =>
    fetchAPI(`${scopePath(scope)}/secrets/${encodeURIComponent(key)}`, { method: "DELETE" }),
};
