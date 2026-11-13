import type {
  BranchInfo,
  CommitInfo,
  TagEntry,
} from "@/lib/api/version.service";

// The Deploy a version dialog's rules, kept apart so they are tested.

const NEVER = "0001-01-01T00:00:00Z";

/** The time a registry gives for a tag, or "" when it gives none. */
export const pushedAt = (t: Pick<TagEntry, "pushed_at">) =>
  t.pushed_at && t.pushed_at !== NEVER ? t.pushed_at : "";

/** Newest push first; tags without a time last, by name. */
export function byPushTime(tags: TagEntry[]): TagEntry[] {
  return [...tags].sort((a, b) => {
    const pa = pushedAt(a);
    const pb = pushedAt(b);
    if (pa && pb) return pb.localeCompare(pa);
    if (pa || pb) return pa ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
}

/** The first 12 characters of a digest or commit, without "sha256:". */
export const shortHash = (h: string) => h.replace(/^sha256:/, "").slice(0, 12);

/** "ghcr.io:5000/acme/api:v1" → { base: "ghcr.io:5000/acme/api", tag: "v1" };
 *  no tag → tag null. */
export function splitImage(image: string): {
  base: string;
  tag: string | null;
} {
  const slash = image.lastIndexOf("/");
  const colon = image.lastIndexOf(":");
  return colon > slash
    ? { base: image.slice(0, colon), tag: image.slice(colon + 1) }
    : { base: image, tag: null };
}

/** Why Deploy is off, or "" when it is on. */
export function deployBlocked(opts: {
  canDeploy: boolean;
  busy: boolean;
  picked: string;
  running: string | null;
  label: string;
}): string {
  if (!opts.canDeploy) return "Only owners and developers deploy.";
  if (opts.busy) return "A deploy is running.";
  if (!opts.picked) return "Pick a version.";
  if (opts.running !== null && opts.picked === opts.running)
    return `${opts.label} is running.`;
  return "";
}

/** The service's own branch first, then by newest commit. The host lists a
 *  page of branches by name, which can leave the service's own out: it is
 *  added with the latest commit the host gave for it. */
export function branchesFirst(
  branches: BranchInfo[],
  current: string,
  latest?: CommitInfo,
): BranchInfo[] {
  const rest = branches
    .filter((b) => b.name !== current)
    .sort((a, b) =>
      (b.latest_commit?.timestamp ?? "").localeCompare(
        a.latest_commit?.timestamp ?? "",
      ),
    );
  const own =
    branches.find((b) => b.name === current) ??
    (current && latest ? { name: current, latest_commit: latest } : null);
  return own ? [own, ...rest] : rest;
}
