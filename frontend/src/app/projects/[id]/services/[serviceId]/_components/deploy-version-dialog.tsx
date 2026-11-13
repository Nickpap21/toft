"use client";

import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Copy, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { versionService, type VersionInfo } from "@/lib/api/version.service";
import { allDeploymentsQuery } from "@/lib/api/deployment.service";
import type { Service } from "@/lib/api/service.service";
import { cn } from "@/lib/utils";
import { isoTime } from "./deployment-view";
import {
  branchesFirst,
  byPushTime,
  deployBlocked,
  pushedAt,
  shortHash,
  splitImage,
} from "./version-view";

// "Deploy a version" (D-45): the Versions tab as a dialog off the header's
// Deploy menu. No board: built from the boards' parts, its screenshots
// approved by the founders (D-36 exception). Change it as a board would be.

export type VersionChoice = {
  imageTag?: string;
  gitRef?: string;
  gitRefType?: "branch" | "tag";
};

const mono = "font-mono text-xs";
const chip =
  "inline-flex h-5 items-center rounded-full px-2 text-[11px] font-medium";

export function DeployVersionDialog({
  open,
  onOpenChange,
  service,
  canDeploy,
  busy,
  onDeploy,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  service: Service;
  canDeploy: boolean;
  busy: boolean;
  onDeploy: (v: VersionChoice) => void;
}) {
  const { data, isPending, isError, refetch, isFetching } = useQuery({
    queryKey: ["versions", service.id],
    queryFn: () => versionService.get(service.id),
    enabled: open,
    staleTime: 5 * 60 * 1000,
  });
  const { data: deployments } = useQuery({
    ...allDeploymentsQuery(service.id),
    enabled: open,
  });
  const live =
    deployments?.find((d) => d.id === service.active_deployment_id) ??
    deployments?.find((d) => d.status === "running");

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>Deploy a version of {service.name}</DialogTitle>
          <DialogDescription>
            {service.source_type === "docker-image"
              ? `Pick a tag of the image. Deploying it saves it as ${service.name}'s image, so later deploys use it too.`
              : `Pick a branch or a tag. A branch becomes ${service.name}'s branch, and later pushes to it deploy; a tag deploys once and ${service.name} stays on ${service.git_branch || "its branch"}.`}
          </DialogDescription>
        </DialogHeader>
        {isPending ? (
          <div className="h-64 rounded-lg bg-raised/40 motion-safe:animate-pulse" />
        ) : isError || !data ? (
          <div className="flex flex-col items-start gap-2 rounded-lg border border-subtle p-4 text-[13px]">
            <p className="text-fg">Could not load the versions.</p>
            <p className="text-muted">
              The registry or the repository host didn&apos;t answer.
            </p>
            <Button variant="outline" size="sm" onClick={() => refetch()}>
              Try again
            </Button>
          </div>
        ) : data.source_type === "docker-image" ? (
          <ImageVersions
            key={data.current_image}
            info={data}
            liveImage={live?.image_tag ?? null}
            canDeploy={canDeploy}
            busy={busy}
            fetching={isFetching}
            onRefresh={() => refetch()}
            onCancel={() => onOpenChange(false)}
            onDeploy={onDeploy}
          />
        ) : (
          <GitVersions
            key={data.current_branch}
            info={data}
            liveSha={live?.commit_sha || null}
            canDeploy={canDeploy}
            busy={busy}
            fetching={isFetching}
            onRefresh={() => refetch()}
            onCancel={() => onOpenChange(false)}
            onDeploy={onDeploy}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

type ListProps = {
  canDeploy: boolean;
  busy: boolean;
  fetching: boolean;
  onRefresh: () => void;
  onCancel: () => void;
  onDeploy: (v: VersionChoice) => void;
};

function ImageVersions({
  info,
  liveImage,
  canDeploy,
  busy,
  fetching,
  onRefresh,
  onCancel,
  onDeploy,
}: ListProps & { info: VersionInfo; liveImage: string | null }) {
  const saved = info.current_image ?? "";
  const { base, tag: savedTag } = splitImage(saved);
  const running = liveImage ? splitImage(liveImage).tag : null;
  const tags = byPushTime(info.tags ?? []);
  const newest = tags.find((t) => pushedAt(t))?.name;
  const [picked, setPicked] = useState("");
  const [filter, setFilter] = useState("");
  const shown = filter
    ? tags.filter((t) => t.name.includes(filter.trim()))
    : tags;
  const blocked = deployBlocked({
    canDeploy,
    busy,
    picked,
    running,
    label: `${base}:${picked}`,
  });

  return (
    <div className="flex flex-col gap-3">
      <Facts
        rows={[
          [
            "Image",
            <span key="i" className={mono}>
              {base}
            </span>,
          ],
          [
            "Running",
            running ? (
              <span key="r" className={mono}>
                {running}
              </span>
            ) : (
              <span key="r" className="text-muted">
                nothing yet
              </span>
            ),
          ],
          ...(savedTag !== running
            ? ([
                [
                  "Saved",
                  <span key="s">
                    <span className={mono}>{savedTag ?? "no tag"}</span>
                    <span className="text-muted"> · deploys next</span>
                  </span>,
                ],
              ] as [string, ReactNode][])
            : []),
        ]}
      />
      {savedTag === null && (
        <p className="text-[13px] text-status-pending-fg">
          <span className={mono}>{saved}</span> has no tag, so Docker pulls{" "}
          <span className={mono}>latest</span> at each deploy. Pick a tag to pin
          it.
        </p>
      )}
      <ListHead
        title={`Tags · ${tags.length}`}
        hint="Newest push first, up to 25 from the registry."
        fetching={fetching}
        onRefresh={onRefresh}
        filter={tags.length > 8 ? filter : null}
        onFilter={setFilter}
      />
      {tags.length === 0 ? (
        <div className="rounded-lg border border-subtle p-4 text-[13px]">
          <p className="text-muted">No tags found.</p>
          {info.registry_error && (
            <p className="mt-1 break-all font-mono text-xs text-status-failed-fg">
              {info.registry_error}
            </p>
          )}
        </div>
      ) : (
        <div
          role="radiogroup"
          aria-label="Tags"
          className="max-h-80 overflow-y-auto rounded-lg border border-subtle"
        >
          {shown.length === 0 && (
            <p className="px-3 py-2.5 text-[13px] text-muted">
              No tag matches.
            </p>
          )}
          {shown.map((t, i) => (
            <Row
              key={t.name}
              name="tag"
              value={t.name}
              checked={picked === t.name}
              onPick={setPicked}
              first={i === 0}
              title={t.name}
              chips={[
                t.name === newest && "newest",
                t.name === running && "running",
                t.name === savedTag && savedTag !== running && "saved",
              ]}
              when={pushedAt(t)}
              hash={t.digest}
            />
          ))}
        </div>
      )}
      <Footer
        blocked={blocked}
        label={picked ? `Deploy ${picked}` : "Deploy"}
        onCancel={onCancel}
        onDeploy={() => onDeploy({ imageTag: `${base}:${picked}` })}
      />
    </div>
  );
}

function GitVersions({
  info,
  liveSha,
  canDeploy,
  busy,
  fetching,
  onRefresh,
  onCancel,
  onDeploy,
}: ListProps & { info: VersionInfo; liveSha: string | null }) {
  const [kind, setKind] = useState<"branch" | "tag">("branch");
  const current = info.current_branch ?? "";
  const branches = branchesFirst(
    info.branches ?? [],
    current,
    info.latest_commit ?? undefined,
  );
  const tags = [...(info.git_tags ?? [])].sort(
    (a, b) =>
      (b.date ?? "").localeCompare(a.date ?? "") ||
      b.name.localeCompare(a.name),
  );
  const [picked, setPicked] = useState<{
    kind: "branch" | "tag";
    name: string;
  }>({ kind: "branch", name: current });
  const [filter, setFilter] = useState("");
  const f = filter.trim();
  const pickedSha =
    picked.kind === "branch"
      ? branches.find((b) => b.name === picked.name)?.latest_commit?.sha
      : tags.find((t) => t.name === picked.name)?.sha;
  // Nothing new to deploy: the service's own branch, or a tag, already at the running commit.
  const same =
    !!liveSha &&
    !!pickedSha &&
    pickedSha === liveSha &&
    (picked.kind === "tag" || picked.name === current);
  const blocked = deployBlocked({
    canDeploy,
    busy,
    picked: picked.name,
    running: same ? picked.name : null,
    label: `${picked.name} at ${shortHash(liveSha ?? "")}`,
  });
  const list =
    kind === "branch"
      ? branches.filter((b) => b.name.includes(f))
      : tags.filter((t) => t.name.includes(f));
  const total = kind === "branch" ? branches.length : tags.length;

  return (
    <div className="flex flex-col gap-3">
      <Facts
        rows={[
          [
            "Branch",
            <span key="b" className={mono}>
              {current || "none"}
            </span>,
          ],
          [
            "Running",
            liveSha ? (
              <HashCopy key="r" hash={liveSha} />
            ) : (
              <span key="r" className="text-muted">
                nothing yet
              </span>
            ),
          ],
          ...(info.latest_commit && info.latest_commit.sha !== liveSha
            ? ([
                [
                  "Newest",
                  <span key="n" className="min-w-0">
                    <HashCopy hash={info.latest_commit.sha} />{" "}
                    <span className="text-fg/85">
                      {info.latest_commit.message.split("\n")[0]}
                    </span>
                  </span>,
                ],
              ] as [string, ReactNode][])
            : []),
        ]}
      />
      <div
        role="radiogroup"
        aria-label="Branches or tags"
        className="inline-flex gap-0.5 self-start rounded-lg border border-subtle bg-ground p-0.5"
      >
        {(["branch", "tag"] as const).map((k) => (
          <label
            key={k}
            className={cn(
              "inline-flex h-8 cursor-pointer items-center gap-2 rounded-md px-2.5 text-[13px]",
              kind === k ? "bg-raised text-fg" : "text-muted hover:text-fg",
            )}
          >
            <input
              type="radio"
              name="git-kind"
              className="sr-only"
              checked={kind === k}
              onChange={() => {
                setKind(k);
                setFilter("");
              }}
            />
            {k === "branch"
              ? `Branches · ${branches.length}`
              : `Tags · ${tags.length}`}
          </label>
        ))}
      </div>
      <ListHead
        title={kind === "branch" ? "Branches" : "Tags"}
        hint={
          kind === "branch"
            ? `${current} first, then by newest commit.`
            : "Newest first."
        }
        fetching={fetching}
        onRefresh={onRefresh}
        filter={total > 8 ? filter : null}
        onFilter={setFilter}
      />
      {info.scm_error && (
        <p
          role="status"
          className="flex items-start gap-2 rounded-lg border border-status-pending/40 bg-status-pending-soft/40 px-3 py-2.5 text-[13px] text-fg"
        >
          <AlertTriangle
            className="mt-0.5 h-4 w-4 flex-none text-status-pending-fg"
            aria-hidden="true"
          />
          <span>
            {info.scm_error} Deploying {current || "the branch"} still works:
            the deploy clones the repository.
          </span>
        </p>
      )}
      {total === 0 ? (
        <p className="rounded-lg border border-subtle p-4 text-[13px] text-muted">
          {info.scm_error
            ? `The ${kind === "branch" ? "branches" : "tags"} couldn't be listed.`
            : kind === "branch"
              ? "No branches found."
              : "The repository has no tags."}
        </p>
      ) : (
        <div
          role="radiogroup"
          aria-label={kind === "branch" ? "Branches" : "Tags"}
          className="max-h-80 overflow-y-auto rounded-lg border border-subtle"
        >
          {list.length === 0 && (
            <p className="px-3 py-2.5 text-[13px] text-muted">
              Nothing matches.
            </p>
          )}
          {kind === "branch"
            ? branches
                .filter((b) => b.name.includes(f))
                .map((b, i) => (
                  <Row
                    key={b.name}
                    name="git-ref"
                    value={b.name}
                    checked={picked.kind === "branch" && picked.name === b.name}
                    onPick={(n) => setPicked({ kind: "branch", name: n })}
                    first={i === 0}
                    title={b.name}
                    chips={[
                      b.name === current && "its branch",
                      !!liveSha &&
                        b.latest_commit?.sha === liveSha &&
                        "running",
                    ]}
                    when={b.latest_commit?.timestamp ?? ""}
                    hash={b.latest_commit?.sha ?? ""}
                    sub={
                      b.latest_commit
                        ? `${b.latest_commit.message.split("\n")[0]}${b.latest_commit.author ? ` · ${b.latest_commit.author}` : ""}`
                        : undefined
                    }
                  />
                ))
            : tags
                .filter((t) => t.name.includes(f))
                .map((t, i) => (
                  <Row
                    key={t.name}
                    name="git-ref"
                    value={t.name}
                    checked={picked.kind === "tag" && picked.name === t.name}
                    onPick={(n) => setPicked({ kind: "tag", name: n })}
                    first={i === 0}
                    title={t.name}
                    chips={[!!liveSha && t.sha === liveSha && "running"]}
                    when={t.date ?? ""}
                    hash={t.sha}
                  />
                ))}
        </div>
      )}
      <Footer
        blocked={blocked}
        label={`Deploy ${picked.kind === "tag" ? "tag " : ""}${picked.name}`}
        note={
          !blocked && picked.kind === "branch" && picked.name !== current
            ? `${picked.name} becomes the service's branch.`
            : undefined
        }
        onCancel={onCancel}
        onDeploy={() =>
          onDeploy({ gitRef: picked.name, gitRefType: picked.kind })
        }
      />
    </div>
  );
}

function Facts({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="overflow-hidden rounded-lg border border-subtle text-[13px]">
      {rows.map(([k, v], i) => (
        <div
          key={k}
          className={cn(
            "grid grid-cols-[84px_1fr] gap-3 px-3 py-2",
            i > 0 && "border-t border-subtle",
          )}
        >
          <dt className="text-muted">{k}</dt>
          <dd className="flex min-w-0 flex-wrap items-center gap-x-2 text-fg">
            {v}
          </dd>
        </div>
      ))}
    </dl>
  );
}

function ListHead({
  title,
  hint,
  fetching,
  onRefresh,
  filter,
  onFilter,
}: {
  title: string;
  hint: string;
  fetching: boolean;
  onRefresh: () => void;
  filter: string | null;
  onFilter: (v: string) => void;
}) {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
      <span className="text-[13px] font-medium text-fg">{title}</span>
      <span className="text-xs text-muted">{hint}</span>
      <span className="flex-1" />
      {filter !== null && (
        <input
          aria-label="Filter"
          placeholder="Filter"
          value={filter}
          onChange={(e) => onFilter(e.target.value)}
          className="h-8 w-40 rounded-md border border-strong/70 bg-ground px-2.5 font-mono text-xs text-fg placeholder:text-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        />
      )}
      <button
        type="button"
        onClick={onRefresh}
        disabled={fetching}
        className="inline-flex h-8 items-center gap-1.5 rounded-md px-2 text-xs text-muted hover:bg-raised hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-50"
      >
        <RefreshCw
          className={cn("h-3.5 w-3.5", fetching && "motion-safe:animate-spin")}
          aria-hidden="true"
        />
        Refresh
      </button>
    </div>
  );
}

function Row({
  name,
  value,
  title,
  checked,
  onPick,
  first,
  chips,
  when,
  hash,
  sub,
}: {
  name: string;
  value: string;
  title: string;
  checked: boolean;
  onPick: (v: string) => void;
  first: boolean;
  chips: (string | false)[];
  when: string;
  hash: string;
  sub?: string;
}) {
  return (
    <div
      className={cn(
        "flex items-start gap-3 px-3 py-2",
        !first && "border-t border-subtle",
        checked && "bg-accent-soft",
      )}
    >
      <label className="flex min-w-0 flex-1 cursor-pointer items-start gap-3">
        <input
          type="radio"
          name={name}
          value={value}
          checked={checked}
          onChange={() => onPick(value)}
          className="mt-0.5 h-4 w-4 flex-none accent-[var(--link)]"
        />
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex flex-wrap items-center gap-2">
            <span className="break-words font-mono text-[13px] text-fg">
              {title}
            </span>
            {chips.filter(Boolean).map((c) => (
              <span
                key={c as string}
                className={cn(
                  chip,
                  c === "running"
                    ? "bg-status-running-soft text-status-running-fg"
                    : c === "saved"
                      ? "bg-status-pending-soft text-status-pending-fg"
                      : "border border-strong/60 text-fg/85",
                )}
              >
                {c}
              </span>
            ))}
          </span>
          {sub && <span className="truncate text-xs text-muted">{sub}</span>}
          {when && (
            <span className="font-mono text-xs text-muted sm:hidden">
              {isoTime(when)}
            </span>
          )}
        </span>
      </label>
      {/* On phones the time goes under the name; the hash stays here. */}
      <span className="flex flex-none flex-col items-end gap-0.5 text-xs">
        {when && (
          <span className="hidden font-mono text-muted sm:inline">
            {isoTime(when)}
          </span>
        )}
        {hash && <HashCopy hash={hash} />}
      </span>
    </div>
  );
}

function HashCopy({ hash }: { hash: string }) {
  return (
    <span className="inline-flex items-center gap-1">
      <span className="font-mono text-xs text-fg/85" title={hash}>
        {shortHash(hash)}
      </span>
      <button
        type="button"
        aria-label={`Copy ${shortHash(hash)}`}
        onClick={() => {
          void navigator.clipboard.writeText(hash);
          toast.success("Copied");
        }}
        className="inline-flex h-5 w-5 items-center justify-center rounded text-muted hover:bg-raised hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
      >
        <Copy className="h-3 w-3" aria-hidden="true" />
      </button>
    </span>
  );
}

function Footer({
  blocked,
  label,
  note,
  onCancel,
  onDeploy,
}: {
  blocked: string;
  label: string;
  note?: string;
  onCancel: () => void;
  onDeploy: () => void;
}) {
  return (
    <DialogFooter className="flex-wrap items-center gap-y-2">
      <span
        className={cn("mr-auto text-xs", blocked ? "text-muted" : "text-fg/85")}
      >
        {blocked || note || ""}
      </span>
      <Button variant="outline" onClick={onCancel}>
        Cancel
      </Button>
      <Button onClick={onDeploy} disabled={!!blocked}>
        {label}
      </Button>
    </DialogFooter>
  );
}
