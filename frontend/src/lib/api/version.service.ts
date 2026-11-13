import type { components } from "./schema";
import { fetchAPI } from "./client";

export interface TagEntry {
  name: string;
  pushed_at: string;
  digest: string;
  is_latest: boolean;
}

export interface CommitInfo {
  sha: string;
  message: string;
  author: string;
  timestamp: string;
}

export interface BranchInfo {
  name: string;
  latest_commit: CommitInfo;
}

export interface GitTagInfo {
  name: string;
  sha: string;
  date?: string;
}

export type VersionInfo = components["schemas"]["VersionInfo"];

export const versionService = {
  get: (serviceId: string) =>
    fetchAPI<VersionInfo>(`/api/v1/services/${serviceId}/versions`),
};
