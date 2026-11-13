"use client";

import { useState } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, ArrowUp, Check, ChevronDown, Circle, Clock, GitCommitHorizontal, GitMerge, GitPullRequest, GitPullRequestClosed, MoreHorizontal, Pencil, Rocket, Square, Trash2, UserCheck } from "lucide-react";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { ApiError, fetchAPI } from "@/lib/api/client";
import type { MergeMethod, PullRequest, PullRequestDetail, TimelineItem } from "@/lib/api/code.service";
import { useAuth, useOrgRole } from "@/context/AuthContext";
import { cn } from "@/lib/utils";
import { deploymentHref } from "@/components/home/home";
import { CheckIcon, checkTitle, deployLines } from "./checks";
import { Avatar, CopyButton } from "./parts";
import { ago, commitHref, pullHref, shortSha, useCode } from "./repo";
import { MentionTextarea } from "./mention-textarea";
import { Markdown } from "./views";
import { LoadFailed } from "@/components/shell/unreachable";
import { personName, Who } from "@/components/agent-person";
import { checksLine, isPreviewCheck, whoMerges } from "@/app/projects/[id]/code/[repo]/pulls/[number]/view";
import { outline } from "@/app/projects/_lib/ui";

// Pull requests' shared pieces (boards PullRequest, PullRequestMerge,
// PullRequests, NewPullRequest; D-75).

const STATE = {
  open: { label: "Open", icon: GitPullRequest, tone: "bg-status-running-soft text-status-running-fg" },
  merged: { label: "Merged", icon: GitMerge, tone: "bg-env-violet-soft text-env-violet-fg" },
  closed: { label: "Closed", icon: GitPullRequestClosed, tone: "bg-status-failed-soft text-status-failed-fg" },
} as const;

export function stateIcon(state: PullRequest["state"]) {
  return STATE[state];
}

export function StateChip({ state }: { state: PullRequest["state"] }) {
  const s = STATE[state];
  return (
    <span className={cn("inline-flex h-6 items-center gap-1.5 rounded-full px-[9px] text-xs font-medium", s.tone)}>
      <s.icon className="h-3 w-3" aria-hidden="true" />
      {s.label}
    </span>
  );
}

export function useCanWrite(): boolean {
  const role = useOrgRole();
  return role === "owner" || role === "developer";
}

/** Markdown with Write and Preview; the preview is rendered on the server
 *  as it will be shown (POST /markdown). ⌘/Ctrl Enter submits. */
export function MarkdownField({
  id,
  label,
  value,
  onChange,
  rows = 5,
  placeholder,
  onSubmit,
  autoFocus,
  projectId,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (v: string) => void;
  rows?: number;
  placeholder?: string;
  onSubmit?: () => void;
  autoFocus?: boolean;
  /** Links that project's issue keys in the preview (D-81). */
  projectId?: string;
}) {
  const [preview, setPreview] = useState(false);
  const html = useQuery({
    queryKey: ["markdown", value, projectId],
    queryFn: () => fetchAPI<{ html: string }>("/api/v1/markdown", { method: "POST", body: JSON.stringify({ text: value, project_id: projectId }) }),
    enabled: preview && value.trim() !== "",
    staleTime: Infinity,
  });
  const pressed = (on: boolean) => cn("touch-target h-6 rounded-md px-2.5 text-xs font-medium", on ? "bg-raised text-fg" : "text-fg/70 hover:text-fg");
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center justify-between gap-2">
        <label htmlFor={id} className="text-[13px] font-medium text-fg">
          {label}
        </label>
        <span role="group" aria-label="Show" className="inline-flex gap-0.5 rounded-lg border border-subtle bg-ground p-0.5">
          <button type="button" aria-pressed={!preview} onClick={() => setPreview(false)} className={pressed(!preview)}>
            Write
          </button>
          <button type="button" aria-pressed={preview} onClick={() => setPreview(true)} className={pressed(preview)}>
            Preview
          </button>
        </span>
      </div>
      {preview ? (
        <div className="min-h-[120px] rounded-lg border border-subtle bg-ground px-3 py-2.5">
          {value.trim() === "" ? (
            <p className="text-[13px] text-muted">Nothing to preview.</p>
          ) : html.data ? (
            <div className="toft-md" style={{ fontSize: 14, lineHeight: "22px" }} dangerouslySetInnerHTML={{ __html: html.data.html }} />
          ) : html.error ? (
            <LoadFailed what="the preview" onRetry={() => void html.refetch()} />
          ) : (
            <p className="text-[13px] text-muted">Rendering…</p>
          )}
        </div>
      ) : (
        <MentionTextarea
          id={id}
          rows={rows}
          value={value}
          autoFocus={autoFocus}
          placeholder={placeholder}
          onChange={onChange}
          onKeyDown={(e) => {
            if (onSubmit && e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              onSubmit();
            }
          }}
          className="resize-y rounded-lg border border-strong bg-ground px-2.5 py-2 text-[13px] leading-5 text-fg placeholder:text-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        />
      )}
      <span className="text-xs text-muted">Markdown. Every member of the workspace sees it; @name tells them.</span>
    </div>
  );
}

/** A comment or an event of the conversation. */
export function TimelineEntry({ item, pull, now }: { item: TimelineItem; pull: PullRequestDetail; now: number }) {
  if (item.type === "comment") return <Comment item={item} pull={pull} now={now} />;
  return <Event item={item} now={now} />;
}

function Comment({ item, pull, now }: { item: TimelineItem; pull: PullRequestDetail; now: number }) {
  const { api } = useCode();
  const { user } = useAuth();
  const owner = useOrgRole() === "owner";
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(item.body ?? "");
  const mine = !!user && item.actor?.id === user.id;
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["pull-timeline", api.id, pull.number] });
  const save = useMutation({
    mutationFn: () => api.editComment(pull.number, item.id, text),
    onSuccess: () => {
      setEditing(false);
      refresh();
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : "Couldn’t save the comment"),
  });
  const remove = useMutation({
    mutationFn: () => api.deleteComment(pull.number, item.id),
    onSuccess: () => {
      refresh();
      queryClient.invalidateQueries({ queryKey: ["pull", api.id, pull.number] });
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : "Couldn’t delete the comment"),
  });
  return (
    <article className="overflow-hidden rounded-[10px] border border-subtle bg-surface">
      <div className="flex items-center gap-2.5 border-b border-subtle px-4 py-2.5 text-[13px] text-fg/70">
        <Avatar name={item.actor?.name ?? "?"} agent={item.actor?.agent} size={24} />
        <span>
          <Who person={item.actor} fallback="A deleted account" className="font-medium text-fg" /> commented {ago(item.created_at, now)}
          {item.edited_at && <span className="text-muted"> · edited</span>}
        </span>
        <span className="flex-1" />
        {(mine || owner) && !editing && (
          <DropdownMenu>
            <DropdownMenuTrigger aria-label="Comment options" className="flex h-[26px] w-[26px] items-center justify-center rounded-md text-muted hover:bg-raised hover:text-fg">
              <MoreHorizontal className="h-4 w-4" aria-hidden="true" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {mine && (
                <DropdownMenuItem onSelect={() => setEditing(true)}>
                  <Pencil className="h-3.5 w-3.5" aria-hidden="true" />
                  Edit
                </DropdownMenuItem>
              )}
              <DropdownMenuItem onSelect={() => remove.mutate()} className="text-status-failed-fg">
                <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                Delete
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
      {editing ? (
        <div className="flex flex-col gap-2 p-3">
          <MarkdownField id={`edit-${item.id}`} label="Edit comment" value={text} onChange={setText} onSubmit={() => save.mutate()} autoFocus />
          <div className="flex justify-end gap-2">
            <button type="button" onClick={() => setEditing(false)} className="h-8 rounded-lg border border-subtle px-3 text-[13px] font-medium text-fg hover:bg-raised">
              Cancel
            </button>
            <button
              type="button"
              disabled={!text.trim() || save.isPending}
              onClick={() => save.mutate()}
              className="h-8 rounded-lg bg-raised px-3 text-[13px] font-medium text-fg hover:bg-raised/70 disabled:opacity-50"
            >
              Save
            </button>
          </div>
        </div>
      ) : (
        <div className="px-4 py-3.5">
          <Markdown html={item.html ?? ""} refName={pull.head_branch} />
        </div>
      )}
    </article>
  );
}

type EventData = {
  count?: number;
  forced?: boolean;
  commits?: { sha: string; subject: string }[];
  method?: MergeMethod;
  sha?: string;
  base?: string;
  deleted_branch?: boolean;
  branch?: string;
  from?: string;
  to?: string;
  // A preview's (D-97).
  reason?: string;
  note?: string;
  services?: string[];
  // The migrations a preview skipped (D-144).
  files?: string[];
};

/** Why a preview was removed, in the conversation's words. */
const REMOVED: Record<string, string> = {
  merged: "this merged",
  closed: "this closed",
  "previews off": "previews were turned off",
  "copies another environment": "previews copy another environment now",
};

function Event({ item, now }: { item: TimelineItem; now: number }) {
  const { base } = useCode();
  const { user } = useAuth();
  const d = (item.data ?? {}) as EventData;
  const you = !!user && item.actor?.id === user.id && !item.actor.agent;
  const who = <span className="font-medium text-fg">{you ? "You" : personName(item.actor, "Toft")}</span>;
  let icon = GitCommitHorizontal;
  let tone = "text-muted";
  let text: React.ReactNode = null;
  switch (item.kind) {
    case "pushed":
      icon = ArrowUp;
      text = (
        <>
          {who} {d.forced ? "force-pushed" : "pushed"} {d.count === 1 ? "1 commit" : `${d.count ?? 0} commits`}
        </>
      );
      break;
    case "merged":
      icon = GitMerge;
      tone = "text-env-violet-fg";
      text = (
        <>
          {who} {d.method === "squash" ? "squashed and merged" : d.method === "fast_forward" ? "fast-forwarded" : "merged"} this into{" "}
          <code className="font-mono text-xs text-fg">{d.base}</code>
          {d.sha && (
            <>
              {" "}
              as{" "}
              <Link href={commitHref(base, d.sha)} className="font-mono text-xs inline-link">
                {shortSha(d.sha)}
              </Link>
            </>
          )}
          {d.deleted_branch && " and deleted the branch"}
        </>
      );
      break;
    case "closed":
      icon = GitPullRequestClosed;
      tone = "text-status-failed-fg";
      text = <>{who} closed this</>;
      break;
    case "reopened":
      icon = GitPullRequest;
      tone = "text-status-running-fg";
      text = <>{who} reopened this</>;
      break;
    case "head_deleted":
      icon = Trash2;
      text = (
        <>
          {who} deleted the branch <code className="font-mono text-xs text-fg">{d.branch}</code>
        </>
      );
      break;
    case "preview_started":
      icon = Rocket;
      tone = "text-status-building-fg";
      text = <>Preview started from {d.sha && <code className="font-mono text-xs text-fg">{shortSha(d.sha)}</code>}</>;
      break;
    case "preview_updated":
      icon = Rocket;
      tone = "text-status-building-fg";
      text = <>Preview updated to {d.sha && <code className="font-mono text-xs text-fg">{shortSha(d.sha)}</code>}</>;
      break;
    case "preview_waiting":
      icon = Clock;
      tone = "text-status-pending-fg";
      text = <>Preview waiting for an owner or developer to approve {d.sha && <code className="font-mono text-xs text-fg">{shortSha(d.sha)}</code>}</>;
      break;
    case "preview_approved":
      icon = Check;
      tone = "text-status-running-fg";
      text = <>{who} approved the preview at {d.sha && <code className="font-mono text-xs text-fg">{shortSha(d.sha)}</code>}</>;
      break;
    case "preview_migrations_skipped":
      icon = AlertTriangle;
      tone = "text-status-pending-fg";
      text = (
        <>
          Preview skipped {d.files?.length === 1 ? "1 migration file" : `${d.files?.length ?? 0} migration files`}:{" "}
          {d.files?.map((f, i) => (
            <span key={f}>
              {i > 0 && ", "}
              <code className="break-all font-mono text-xs text-fg">{f}</code>
            </span>
          ))}
        </>
      );
      break;
    case "preview_stopped":
      icon = Square;
      text = d.reason === "room" ? <>Preview stopped to make room: {d.note}</> : <>{who} stopped the preview</>;
      break;
    case "preview_removed":
      icon = Trash2;
      text = (
        <>
          Preview removed{d.services?.length ? `: ${d.services.join(", ")}` : ""}
          {d.reason && REMOVED[d.reason] ? `, as ${REMOVED[d.reason]}` : d.reason ? ` (${d.reason})` : ""}
        </>
      );
      break;
    case "title_changed":
      icon = Pencil;
      text = (
        <>
          {who} changed the title from <span className="text-fg line-through">{d.from}</span> to <span className="text-fg">{d.to}</span>
        </>
      );
      break;
  }
  const Icon = icon;
  return (
    <div className="flex flex-col">
      <div className="flex min-h-9 items-center gap-3 px-4 text-[13px] text-fg/70">
        <Icon className={cn("h-[15px] w-[15px] flex-none", tone)} aria-hidden="true" />
        <span className="min-w-0 flex-1">{text}</span>
        <span className="flex-none text-xs text-muted">{ago(item.created_at, now)}</span>
      </div>
      {item.kind === "pushed" &&
        d.commits?.map((c) => (
          <div key={c.sha} className="flex min-h-[30px] items-center gap-3 pl-[43px] pr-4 text-[13px]">
            <GitCommitHorizontal className="h-[13px] w-[13px] flex-none text-muted" aria-hidden="true" />
            <Link href={commitHref(base, c.sha)} className="min-w-0 flex-1 truncate text-fg/80 hover:text-fg hover:underline">
              {c.subject}
            </Link>
            <Link href={commitHref(base, c.sha)} className="font-mono text-xs text-muted hover:text-fg">
              {shortSha(c.sha)}
            </Link>
          </div>
        ))}
    </div>
  );
}

const METHODS: { id: MergeMethod; button: string; deploy: string; label: string; help: string }[] = [
  { id: "squash", button: "Squash and merge", deploy: "Squash, merge and deploy", label: "Squash and merge", help: "One commit on the base with every change" },
  { id: "merge", button: "Merge", deploy: "Merge and deploy", label: "Create a merge commit", help: "Every commit, joined by a merge commit" },
  { id: "fast_forward", button: "Fast-forward", deploy: "Fast-forward and deploy", label: "Fast-forward", help: "Move the base to the branch; no new commit" },
];

/** What the person merging has chosen, kept above the tabs so a look at the
 *  diff doesn't lose it. */
export type MergeChoice = { method: MergeMethod; setMethod: (m: MergeMethod) => void; deleteBranch: boolean; setDeleteBranch: (d: boolean) => void };

/** The Merge card (boards ReworkPull, PullRequestMerge): ready, conflicts,
 *  merged or closed, with what merging does and the merge itself. waiting
 *  is the commit the preview waits to have approved: its checks wait too,
 *  and Approve is the page's main action, not Merge. */
export function MergeBox({ pull, onDone, hasPreview, waiting, choice }: { pull: PullRequestDetail; onDone: () => void; hasPreview?: boolean; waiting?: string; choice: MergeChoice }) {
  const { api, base, detail, projectId } = useCode();
  const canWrite = useCanWrite();
  const { method, setMethod, deleteBranch, setDeleteBranch } = choice;
  // The default branch and a branch a service deploys from stay.
  const deleteHead = deleteBranch && !pull.head_kept;
  const cmp = pull.compare;
  // What the merge deploys (D-77): the base branch's services with deploy
  // on push, one line per environment and server.
  const deploys = deployLines(pull.deploys ?? [], pull.base_branch);
  // The issues it closes ship once its deploys are live (D-81).
  const ships = (pull.issues ?? []).filter((i) => i.status !== "shipped" && i.status !== "cancelled");
  const services = (pull.deploys ?? []).filter((d) => d.branch === pull.base_branch && d.deploy_on_push).length;
  const checks = checksLine((pull.head_checks?.checks ?? []).filter((c) => !waiting || !isPreviewCheck(c.name)), pull.head_sha);
  // A merge's deploys report on its commit a moment after it: the card
  // follows them for two minutes, or while one runs.
  const mergeChecks = useQuery({
    queryKey: ["checks", api.id, pull.merge_sha],
    queryFn: () => api.checks(pull.merge_sha!),
    enabled: pull.state === "merged" && !!pull.merge_sha,
    meta: { quietFailure: "Only lists the deploys a merged pull request started: a failed read leaves the list out and the Merged card claims nothing about deploys." },
    refetchInterval: (q) => {
      const started = (q.state.data?.checks ?? []).filter((c) => c.deployment);
      const fresh = !!pull.merged_at && Date.now() - Date.parse(pull.merged_at) < 120_000;
      return (fresh && started.length < services) || started.some((c) => c.state === "pending" || c.state === "running") ? 4000 : false;
    },
  });
  const merge = useMutation({
    mutationFn: () => api.merge(pull.number, { method, expected_head_sha: cmp!.head.sha, delete_branch: deleteHead }),
    onSuccess: () => {
      toast.success(`!${pull.number} merged into ${pull.base_branch}`, deploys.length ? { description: `Deploying ${deploys.join("; ")}` } : undefined);
      onDone();
    },
    onError: (err) => {
      if (err instanceof ApiError && err.status === 409) onDone();
      toast.error(err instanceof ApiError && err.status < 500 ? err.message : "Couldn’t merge");
    },
  });
  const reopen = useMutation({
    mutationFn: () => api.updatePull(pull.number, { state: "open" }),
    onSuccess: () => {
      toast.success(`!${pull.number} reopened`);
      onDone();
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : "Couldn’t reopen it"),
  });

  if (pull.state === "merged") {
    const started = (mergeChecks.data?.checks ?? []).filter((c) => c.deployment);
    return (
      <Box icon={GitMerge} tone="bg-env-violet-soft text-env-violet-fg" title="Merged" into={pull.base_branch}>
        {pull.merged_by?.name ?? "Someone"} {pull.merge_method === "squash" ? "squashed" : pull.merge_method === "fast_forward" ? "fast-forwarded" : "merged"} this into{" "}
        <code className="font-mono text-xs text-fg">{pull.base_branch}</code>
        {pull.merge_sha && <> as {shortSha(pull.merge_sha)}</>}.
        {started.length > 0 && (
          <ul aria-label="Its deploys" className="mt-2.5 flex flex-col gap-1">
            {started.map((c) => (
              <li key={c.name} className="flex items-center gap-2">
                <CheckIcon state={c.state} />
                <Link href={deploymentHref(projectId, c.deployment!.service_id, started.length === 1 ? c.deployment!.id : "")} className="touch-target inline-flex items-center text-link hover:underline">
                  {checkTitle(c.name).title}
                </Link>
              </li>
            ))}
          </ul>
        )}
      </Box>
    );
  }
  if (pull.state === "closed") {
    return (
      <Box icon={GitPullRequestClosed} tone="bg-status-failed-soft text-status-failed-fg" title="Closed without merging" into={pull.base_branch}>
        {pull.head_exists ? "The branch is still there." : "Its branch was deleted. Push it again to reopen this."}
        {canWrite && pull.head_exists && (
          <button
            type="button"
            onClick={() => reopen.mutate()}
            disabled={reopen.isPending}
            className="mt-3 block h-[34px] rounded-lg border border-strong bg-raised px-3 text-[13px] font-medium text-fg hover:bg-raised/70 disabled:opacity-50"
          >
            {reopen.isPending ? "Reopening…" : "Reopen pull request"}
          </button>
        )}
      </Box>
    );
  }
  if (!pull.head_exists || !cmp) {
    return (
      <Box icon={AlertTriangle} tone="bg-status-pending-soft text-status-pending-fg" title="The branch is gone" into={pull.base_branch}>
        <code className="font-mono text-xs text-fg">{pull.head_branch}</code> was deleted. Push it again to merge this, or close it.
      </Box>
    );
  }
  if (cmp.ahead === 0) {
    return (
      <Box icon={Check} tone="bg-status-running-soft text-status-running-fg" title="Nothing left to merge" into={pull.base_branch}>
        <code className="font-mono text-xs text-fg">{pull.base_branch}</code> already has every commit of this branch. You can close it.
      </Box>
    );
  }
  if (!cmp.mergeable) {
    return (
      <Box icon={AlertTriangle} tone="bg-status-pending-soft text-status-pending-fg" title={`Conflicts with ${pull.base_branch}`} into={pull.base_branch} warn>
        <p>These files changed on both branches, so it can’t be merged yet:</p>
        <ul className="mt-2 flex flex-col gap-1 font-mono text-xs text-fg">
          {cmp.conflicts.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ul>
        <p className="mt-3">Merge {pull.base_branch} into the branch, resolve the files, and push. This updates the pull request.</p>
        <pre role="group" tabIndex={0} aria-label={`Commands to bring ${pull.base_branch} into ${pull.head_branch}`} className="mt-2 overflow-x-auto rounded-lg border border-subtle bg-ground px-3 py-2.5 font-mono text-xs leading-[19px] text-fg">
          {`git switch ${pull.head_branch}\ngit pull toft ${pull.base_branch}\ngit push toft ${pull.head_branch}`}
        </pre>
      </Box>
    );
  }
  const m = METHODS.find((x) => x.id === method)!;
  const ffBlocked = method === "fast_forward" && !cmp.fast_forward;
  return (
    <Box title="Merge" into={pull.base_branch}>
      <ul aria-label="Before it merges" className="flex flex-col gap-2 text-fg">
        <Requirement icon={<UserCheck className="h-3.5 w-3.5 text-muted" aria-hidden="true" />}>{whoMerges(pull)}</Requirement>
        {checks && (
          <Requirement icon={<CheckIcon state={checks.state} />}>
            {checks.state === "failure" ? (
              <Link href={pullHref(base, pull.number, "checks")} className="touch-target inline-flex items-center underline">
                {checks.text}
              </Link>
            ) : (
              checks.text
            )}
          </Requirement>
        )}
        {waiting && <Requirement icon={<Circle className="h-3.5 w-3.5 text-muted" aria-hidden="true" />}>The preview checks wait for {shortSha(waiting)}’s approval</Requirement>}
      </ul>
      {detail.protect_default_branch && pull.base_branch === detail.default_branch && (
        <p className="mt-2 text-xs text-muted">{pull.base_branch} is protected: merging is how it changes.</p>
      )}
      <div className="mt-3.5 flex flex-col gap-1.5 rounded-lg border border-subtle bg-ground p-3 text-[13px]">
        <span className="font-medium text-fg">What merging does</span>
        <span>
          1.{" "}
          {method === "squash"
            ? `Squash the ${cmp.ahead === 1 ? "commit" : `${cmp.ahead} commits`} into one commit on ${pull.base_branch}, by ${personName(pull.author, "the author")}`
            : method === "merge"
              ? `Add ${cmp.ahead === 1 ? "1 commit" : `${cmp.ahead} commits`} to ${pull.base_branch} with a merge commit`
              : `Move ${pull.base_branch} to ${shortSha(cmp.head.sha)}, adding ${cmp.ahead === 1 ? "1 commit" : `${cmp.ahead} commits`}`}
        </span>
        {deploys.map((d, i) => (
          <span key={d}>
            {i + 2}. Deploy {d}
          </span>
        ))}
        {ships.length > 0 && (
          <span>
            {deploys.length + 2}. Move <code className="font-mono text-xs text-fg">{ships.map((i) => i.key).join(", ")}</code> to Shipped
            {services === 0 ? "" : services === 1 ? " once it is live" : services === 2 ? " once both are live" : ` once all ${services} are live`}
          </span>
        )}
        {deleteHead && (
          <span>
            {deploys.length + (ships.length > 0 ? 3 : 2)}. Delete <code className="break-all font-mono text-xs text-fg">{pull.head_branch}</code>; its commits stay in !{pull.number}
          </span>
        )}
        {hasPreview && (
          <span>
            {deploys.length + (ships.length > 0 ? 3 : 2) + (deleteHead ? 1 : 0)}. Remove the preview environment
          </span>
        )}
      </div>
      {ffBlocked && (
        <p className="mt-2.5 text-status-pending-fg">
          {pull.base_branch} has {cmp.behind === 1 ? "a commit" : `${cmp.behind} commits`} the branch doesn’t, so it can’t fast-forward. Squash or merge instead.
        </p>
      )}
      <div className="mt-3.5 flex flex-wrap items-center gap-2.5">
        <Link href={pullHref(base, pull.number, "files")} className={outline}>
          Review changes
        </Link>
        {canWrite && (
          <>
            <span className="inline-flex overflow-hidden rounded-lg">
              <button
                type="button"
                disabled={merge.isPending || ffBlocked}
                onClick={() => merge.mutate()}
                className={cn(buttonBase, "rounded-none px-3.5", waiting ? "border border-r-0 border-subtle bg-raised text-fg hover:bg-raised/70" : "bg-accent text-on-accent hover:bg-accent/90")}
              >
                <GitMerge className="h-[15px] w-[15px]" aria-hidden="true" />
                {merge.isPending ? "Merging…" : deploys.length ? m.deploy : m.button}
              </button>
              <DropdownMenu>
                <DropdownMenuTrigger
                  aria-label="Merge options"
                  className={cn("touch-target flex h-9 w-8 items-center justify-center", waiting ? "border border-subtle bg-raised text-fg hover:bg-raised/70" : "border-l border-on-accent/25 bg-accent text-on-accent hover:bg-accent/90")}
                >
                  <ChevronDown className="h-3.5 w-3.5" aria-hidden="true" />
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start" className="w-[300px]">
                  <DropdownMenuRadioGroup value={method} onValueChange={(v) => setMethod(v as MergeMethod)}>
                    {METHODS.map((x) => (
                      <DropdownMenuRadioItem key={x.id} value={x.id} className="items-start">
                        <span className="flex flex-col">
                          <span className="text-[13px] font-medium">{x.label}</span>
                          <span className="text-xs text-muted">{x.help}</span>
                        </span>
                      </DropdownMenuRadioItem>
                    ))}
                  </DropdownMenuRadioGroup>
                </DropdownMenuContent>
              </DropdownMenu>
            </span>
            {!pull.head_kept && (
              <label className="inline-flex min-h-9 cursor-pointer items-center gap-2 text-[13px]">
                <input type="checkbox" checked={deleteBranch} onChange={(e) => setDeleteBranch(e.target.checked)} className="h-4 w-4 accent-accent" />
                Delete branch after merge
              </label>
            )}
          </>
        )}
      </div>
    </Box>
  );
}

const buttonBase = "touch-target inline-flex h-9 items-center gap-1.5 rounded-lg text-[13px] font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-50";

function Requirement({ icon, children }: { icon: React.ReactNode; children: React.ReactNode }) {
  return (
    <li className="flex items-start gap-2.5 text-[13px] leading-[19px]">
      <span className="flex h-[19px] flex-none items-center">{icon}</span>
      <span>{children}</span>
    </li>
  );
}

function Box({
  icon: Icon,
  tone,
  title,
  into,
  warn,
  children,
}: {
  icon?: React.ComponentType<{ className?: string; "aria-hidden"?: boolean | "true" }>;
  tone?: string;
  title: string;
  into: string;
  warn?: boolean;
  children: React.ReactNode;
}) {
  return (
    <section aria-labelledby="merge-h" className={cn("overflow-hidden rounded-[10px] border bg-surface", warn ? "border-status-pending/45" : "border-subtle")}>
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-b border-subtle px-4 py-3">
        <h2 id="merge-h" className="flex items-center gap-2 text-[15px] font-semibold leading-[22px] text-fg">
          {Icon && (
            <span className={cn("flex h-6 w-6 flex-none items-center justify-center rounded-full", tone)}>
              <Icon className="h-3.5 w-3.5" aria-hidden="true" />
            </span>
          )}
          {title}
        </h2>
        <span className="text-xs text-muted">into {into}</span>
      </div>
      <div className="px-4 pb-4 pt-3.5 text-[13px] text-fg/70">{children}</div>
    </section>
  );
}

/** Check a pull request out locally (board PullRequestMerge's aside). */
export function CheckoutCard({ pull }: { pull: PullRequest }) {
  const commands = `git fetch toft\ngit switch ${pull.head_branch}`;
  return (
    <section aria-labelledby="co-h" className="overflow-hidden rounded-[10px] border border-subtle bg-surface">
      <div className="flex items-center justify-between border-b border-subtle px-4 py-3">
        <h2 id="co-h" className="text-[15px] font-semibold leading-[22px] text-fg">
          Check it out locally
        </h2>
        <CopyButton text={commands} label="Copy the commands" className="touch-target flex h-7 w-7 items-center justify-center rounded-md text-muted hover:bg-raised hover:text-fg pointer-coarse:w-11" />
      </div>
      <div className="flex flex-col gap-2 px-4 pb-4 pt-3.5 text-[13px] text-fg/70">
        <pre role="group" tabIndex={0} aria-label={`Commands to check out ${pull.head_branch}`} className="overflow-x-auto rounded-lg border border-subtle bg-ground px-3 py-2.5 font-mono text-xs leading-[19px] text-fg">{commands}</pre>
        <span>
          Or by number, even after the branch is deleted: <code className="font-mono text-xs text-fg">git fetch toft pull/{pull.number}/head</code>
        </span>
      </div>
    </section>
  );
}
