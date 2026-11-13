"use client";

import { useState } from "react";
import Link from "next/link";
import { useParams, useSearchParams } from "next/navigation";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { GitCommitHorizontal } from "lucide-react";
import { ApiError, notFound } from "@/lib/api/client";
import { LoadFailed, NextPageFailed, readFailed, ReadFailed } from "@/components/shell/unreachable";
import type { CommitCheck, MergeMethod, PullRequestDetail } from "@/lib/api/code.service";
import { useAuth } from "@/context/AuthContext";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { Avatar } from "@/components/code/parts";
import { byAttention, checkDuration, CheckIcon, CheckLine, checkTitle, deploymentHref, ReportChecksCard, summaryText } from "@/components/code/checks";
import { CheckoutCard, MarkdownField, MergeBox, StateChip, TimelineEntry, useCanWrite, type MergeChoice } from "@/components/code/pulls";
import { ago, codeHref, commitHref, pullHref, shortSha, useCode } from "@/components/code/repo";
import { DiffFile, Markdown } from "@/components/code/views";
import { card, outline } from "@/app/projects/_lib/ui";
import { PullPreviewCard, usePullPreview, WaitingCard } from "@/components/previews/pull-card";
import { issueHref, StatusChip as IssueStatusChip } from "@/components/issues/meta";
import { byline, checksSummary, commitWaits, headerLead, passwordLine, tabOf, type Tab } from "./view";


// A pull request (boards ReworkPull, PullRequestChanges, PullRequestChecks):
// a commit waiting for a person (D-97), its conversation, the Merge card
// (with what it deploys, D-77) and a comment box; beside them its preview,
// its checks and About; its commits, files and checks. Left for later:
// reviews (Follow-ups), labels (not planned).
export default function PullPage() {
  const { number: param } = useParams<{ number: string }>();
  const n = Number(param);
  const { api, base, repo } = useCode();
  const sp = useSearchParams();
  const tab = tabOf(sp.get("tab"));
  const queryClient = useQueryClient();
  // Kept here, above the tab switch, so a look at the diff doesn't lose them.
  const [method, setMethod] = useState<MergeMethod>("squash");
  const [deleteBranch, setDeleteBranch] = useState(true);
  const [comment, setComment] = useState("");
  const pull = useQuery({
    queryKey: ["pull", api.id, n],
    queryFn: () => api.pull(n),
    retry: false,
    enabled: n > 0,
    // While its checks run, they're followed.
    refetchInterval: (q) => (q.state.data?.head_checks?.summary?.state === "pending" ? 5000 : false),
  });
  const p = pull.data;
  useDocumentTitle(`${p ? `${p.title} !${p.number}` : `!${n}`} · ${repo.name} · Toft`);
  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ["pull", api.id, n] });
    queryClient.invalidateQueries({ queryKey: ["pull-timeline", api.id, n] });
    queryClient.invalidateQueries({ queryKey: ["pull-preview", api.id, n] });
    queryClient.invalidateQueries({ queryKey: ["repository"] });
    queryClient.invalidateQueries({ queryKey: ["repositories"] });
    queryClient.invalidateQueries({ queryKey: ["code-pulls-page"] });
  };

  if (pull.error && !notFound(pull.error) && !p) {
    return <ReadFailed error={pull.error} onRetry={() => void pull.refetch()} />;
  }
  if (notFound(pull.error)) {
    return (
      <section className={cn(card, "flex max-w-[640px] flex-col gap-2 p-5")}>
        <h1 className="text-[15px] font-semibold text-fg">No pull request !{param} in {repo.name}</h1>
        <Link href={codeHref(base, "pulls")} className={cn(outline, "self-start")}>
          Pull requests
        </Link>
      </section>
    );
  }
  if (!p) return <div role="status" aria-label="Loading" className="h-72 rounded-[10px] bg-surface motion-safe:animate-pulse" />;
  const cmp = p.compare;
  const count = (n: number | undefined) => n !== undefined && <span className="font-mono text-xs text-muted">{n}</span>;
  const tabs: { id: Tab; label: string; extra?: React.ReactNode }[] = [
    { id: "conversation", label: "Conversation" },
    { id: "commits", label: "Commits", extra: cmp && count(cmp.ahead || cmp.commits.length) },
    { id: "checks", label: "Checks", extra: count(p.head_checks?.summary?.total) },
    { id: "files", label: "Files", extra: cmp && count(cmp.stats.files) },
  ];

  return (
    <>
      <Header p={p} onSaved={refresh} />
      <nav aria-label="Pull request" className="-mt-1 flex gap-0.5 overflow-x-auto border-b border-subtle">
        {tabs.map((t) => (
          <Link
            key={t.id}
            href={pullHref(base, n, t.id === "conversation" ? undefined : t.id)}
            aria-current={tab === t.id ? "page" : undefined}
            className={cn(
              "touch-target -mb-px inline-flex h-[38px] items-center gap-1.5 whitespace-nowrap border-b-2 px-3 text-sm font-medium",
              tab === t.id ? "border-accent text-fg" : "border-transparent text-fg/70 hover:text-fg",
            )}
          >
            {t.label} {t.extra}
          </Link>
        ))}
      </nav>
      {tab === "conversation" && <Conversation p={p} onChange={refresh} choice={{ method, setMethod, deleteBranch, setDeleteBranch }} comment={comment} setComment={setComment} />}
      {tab === "commits" && <Commits p={p} />}
      {tab === "files" && <Changes p={p} />}
      {tab === "checks" && <Checks p={p} />}
    </>
  );
}

/** Edit takes focus back when its form closes: the form leaves focus on body
 *  as it goes, unless the person moved on while a save ran. A stable ref, so
 *  it runs when the button mounts and not on every render. */
const focusIfLost = (el: HTMLButtonElement | null) => {
  if (el && document.activeElement === document.body) el.focus();
};

function Header({ p, onSaved }: { p: PullRequestDetail; onSaved: () => void }) {
  const { api } = useCode();
  const canWrite = useCanWrite();
  // null until it has opened: Edit takes focus back only when the form closes.
  const [editing, setEditing] = useState<boolean | null>(null);
  const [title, setTitle] = useState(p.title);
  const n = p.compare?.ahead || p.compare?.commits.length || 0;
  const save = useMutation({
    mutationFn: () => api.updatePull(p.number, { title }),
    onSuccess: () => {
      setEditing(false);
      onSaved();
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : "Couldn’t save the title"),
  });
  return (
    <div className="flex min-w-0 flex-col gap-2 [overflow-wrap:anywhere]">
      {editing ? (
        <form
          className="flex flex-wrap gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (title.trim()) save.mutate();
          }}
        >
          <input
            autoFocus
            aria-label="Title"
            value={title}
            maxLength={256}
            onChange={(e) => setTitle(e.target.value)}
            className="h-10 min-w-0 flex-[1_1_360px] rounded-lg border border-strong bg-ground px-2.5 text-[17px] font-semibold text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
          />
          <button type="submit" disabled={!title.trim() || save.isPending} className={outline}>
            Save
          </button>
          <button type="button" onClick={() => setEditing(false)} className="h-9 px-2 text-[13px] text-fg/70 hover:text-fg">
            Cancel
          </button>
        </form>
      ) : (
        <div>
          <h1 className="inline text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">
            {p.title} <span className="font-normal text-muted">!{p.number}</span>
          </h1>
          {canWrite && (
            <button
              type="button"
              ref={editing === false ? focusIfLost : undefined}
              onClick={() => {
                setTitle(p.title);
                setEditing(true);
              }}
              className="touch-target ml-1.5 inline-flex h-7 items-center rounded-md px-2 align-[3px] text-[13px] text-fg/70 hover:bg-raised hover:text-fg"
            >
              Edit
            </button>
          )}
        </div>
      )}
      <div className="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-[13px] text-fg/70">
        <StateChip state={p.state} />
        <span className="ml-0.5">{headerLead(p)}</span>
        <code className={cn("break-all font-mono text-xs text-fg", !p.head_exists && p.state !== "merged" && "line-through")}>{p.head_branch}</code>
        <span>into</span>
        <code className="font-mono text-xs text-fg">{p.base_branch}</code>
        <span>· {n === 1 ? "1 commit" : `${n} commits`}</span>
      </div>
    </div>
  );
}

function Conversation({ p, onChange, choice, comment, setComment }: { p: PullRequestDetail; onChange: () => void; choice: MergeChoice; comment: string; setComment: (v: string) => void }) {
  const { api, projectId } = useCode();
  const { user } = useAuth();
  const canWrite = useCanWrite();
  const queryClient = useQueryClient();
  const [now] = useState(() => Date.now());
  const [editingBody, setEditingBody] = useState<boolean | null>(null);
  const [body, setBody] = useState(p.body);
  const timeline = useInfiniteQuery({
    queryKey: ["pull-timeline", api.id, p.number],
    queryFn: ({ pageParam }) => api.timeline(p.number, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next,
  });
  const items = timeline.data?.pages.flatMap((pg) => pg.data) ?? [];
  const { data: preview } = usePullPreview(projectId, api.id, p.number);
  // The head commit waits for the preview's approval (its checks wait too).
  const waiting = preview && commitWaits(preview, true) && preview.head_sha === p.head_sha ? p.head_sha : undefined;
  const add = useMutation({
    mutationFn: () => api.comment(p.number, comment),
    onSuccess: () => {
      setComment("");
      queryClient.invalidateQueries({ queryKey: ["pull-timeline", api.id, p.number] });
      onChange();
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : "Couldn’t add the comment"),
  });
  const setState = useMutation({
    mutationFn: (state: "open" | "closed") => api.updatePull(p.number, { state }),
    onSuccess: (pr) => {
      toast.success(pr.state === "closed" ? `!${pr.number} closed` : `!${pr.number} reopened`);
      onChange();
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : "Couldn’t change it"),
  });
  const saveBody = useMutation({
    mutationFn: () => api.updatePull(p.number, { body }),
    onSuccess: () => {
      setEditingBody(false);
      onChange();
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : "Couldn’t save the description"),
  });

  return (
    <div className="flex flex-wrap items-start gap-7 [overflow-wrap:anywhere]">
      <div className="flex min-w-0 flex-[999_1_560px] flex-col gap-5">
        <WaitingCard pull={p} items={items} />
        <article aria-labelledby="conv-h" className={card}>
          <h2 id="conv-h" className="sr-only">
            Conversation
          </h2>
          <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 px-4 pt-3.5">
            <span className="text-[15px] font-semibold leading-[22px] text-fg">{byline(p.author, "A deleted account")}</span>
            <span className="text-xs text-muted">opened this {ago(p.created_at, now)}</span>
            <span className="flex-1" />
            {canWrite && !editingBody && (
              <button type="button" ref={editingBody === false ? focusIfLost : undefined} onClick={() => setEditingBody(true)} className="touch-target -my-1 inline-flex h-7 items-center rounded-md px-2 text-xs text-link hover:bg-raised">
                Edit
              </button>
            )}
          </div>
          {editingBody ? (
            <div className="flex flex-col gap-2 p-3">
              <MarkdownField id="pr-body" label="Description" value={body} onChange={setBody} rows={7} autoFocus onSubmit={() => saveBody.mutate()} />
              <div className="flex justify-end gap-2">
                <button type="button" onClick={() => setEditingBody(false)} className="h-8 rounded-lg border border-subtle px-3 text-[13px] font-medium text-fg hover:bg-raised">
                  Cancel
                </button>
                <button type="button" onClick={() => saveBody.mutate()} disabled={saveBody.isPending} className="h-8 rounded-lg bg-raised px-3 text-[13px] font-medium text-fg hover:bg-raised/70 disabled:opacity-50">
                  Save
                </button>
              </div>
            </div>
          ) : (
            <div className="px-4 pb-3.5 pt-1.5">
              {p.body.trim() ? <Markdown html={p.html} refName={p.head_branch} /> : <p className="text-[13px] italic text-muted">No description.</p>}
            </div>
          )}
          {readFailed(timeline) && (
            <div className="border-t border-subtle px-4 py-2.5">
              <LoadFailed what="the conversation" onRetry={() => void timeline.refetch()} />
            </div>
          )}
          {items.length > 0 && (
            <ol aria-label="Timeline" className="flex flex-col border-t border-subtle py-2">
              {items.map((it) => (
                <li key={it.id} className={cn(it.type === "comment" && "px-4 py-2")}>
                  <TimelineEntry item={it} pull={p} now={now} />
                </li>
              ))}
            </ol>
          )}
          <NextPageFailed query={timeline} what="more of the conversation" className="border-t border-subtle px-4 py-3" />
          {timeline.hasNextPage && (
            <div className="flex justify-center border-t border-subtle py-3">
              <button type="button" onClick={() => timeline.fetchNextPage()} className={outline}>
                Load more of the conversation
              </button>
            </div>
          )}
        </article>

        <MergeBox pull={p} onDone={onChange} hasPreview={!!preview && preview.status !== "removing"} waiting={waiting} choice={choice} />

        {canWrite && (
          <section aria-labelledby="comment-h" className="flex gap-3">
            <Avatar name={user?.name ?? "?"} size={28} />
            <div className={cn(card, "flex min-w-0 flex-1 flex-col gap-2 p-3")}>
              <h2 id="comment-h" className="sr-only">
                Add a comment
              </h2>
              <MarkdownField id="new-comment" label="Add a comment" value={comment} onChange={setComment} rows={3} placeholder="⌘ Enter to comment" onSubmit={() => comment.trim() && add.mutate()} />
              <div className="flex flex-wrap justify-end gap-2">
                {p.state === "open" && (
                  <button type="button" onClick={() => setState.mutate("closed")} disabled={setState.isPending} className={outline}>
                    {setState.isPending ? "Closing…" : "Close pull request"}
                  </button>
                )}
                {p.state === "closed" && p.head_exists && (
                  <button type="button" onClick={() => setState.mutate("open")} disabled={setState.isPending} className={outline}>
                    {setState.isPending ? "Reopening…" : "Reopen pull request"}
                  </button>
                )}
                <button
                  type="button"
                  onClick={() => add.mutate()}
                  disabled={!comment.trim() || add.isPending}
                  className="inline-flex h-9 items-center rounded-lg bg-raised px-3.5 text-[13px] font-medium text-fg hover:bg-raised/70 disabled:opacity-50"
                >
                  Comment
                </button>
              </div>
            </div>
          </section>
        )}
      </div>

      <aside className="flex min-w-0 flex-[1_1_300px] flex-col gap-5">
        <PullPreviewCard pull={p} removed={items.findLast((i) => i.kind === "preview_removed")} />
        <ChecksCard p={p} now={now} waiting={!!waiting} />
        <About p={p} now={now} />
        {p.state !== "merged" && p.head_exists && <CheckoutCard pull={p} />}
      </aside>
    </div>
  );
}

/** The head commit's checks (board ReworkPull's Checks on <sha>). */
function ChecksCard({ p, now, waiting }: { p: PullRequestDetail; now: number; waiting: boolean }) {
  const { base } = useCode();
  const checks = byAttention(p.head_checks?.checks ?? []);
  if (checks.length === 0) return null;
  return (
    <section aria-labelledby="checks-h" className={card}>
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-b border-subtle px-4 py-3">
        <h2 id="checks-h" className="text-[15px] font-semibold leading-[22px] text-fg">
          Checks on {shortSha(p.head_sha)}
        </h2>
        <span className="text-xs text-muted">{checksSummary(checks, waiting)}</span>
      </div>
      <ul>
        {checks.slice(0, 8).map((c, i) => (
          <li key={c.name} className={cn(i > 0 && "border-t border-subtle")}>
            <CheckRow c={c} now={now} />
          </li>
        ))}
      </ul>
      {checks.length > 8 && (
        <Link href={pullHref(base, p.number, "checks")} className="touch-target flex items-center border-t border-subtle px-4 py-2.5 text-xs text-link hover:underline">
          All {checks.length} checks
        </Link>
      )}
    </section>
  );
}

const TOOK: Partial<Record<CommitCheck["state"], string>> = { success: "Passed in", failure: "Failed after", cancelled: "Cancelled after", running: "Running for" };

function CheckRow({ c, now }: { c: CommitCheck; now: number }) {
  const { projectId } = useCode();
  const t = checkTitle(c.name);
  const href = c.deployment ? deploymentHref(projectId, c.deployment) : c.details_url;
  const link = c.deployment ? (c.state === "running" ? "Follow" : "Logs") : "Details ↗";
  const linkClass = "touch-target inline-flex flex-none items-center text-xs text-link hover:underline";
  const name = <span className={cn("min-w-0 truncate text-fg", t.deploy ? "text-[13px]" : "font-mono text-xs leading-[19px]")}>{t.title}</span>;
  // "Passed in 2m 12s" (the board's line) when the check says nothing itself.
  const took = checkDuration(c, now);
  const line = c.description ? [c.description, took].filter(Boolean).join(" · ") : took && TOOK[c.state] ? `${TOOK[c.state]} ${took}` : took;
  return (
    <div className="flex items-start gap-2.5 px-4 py-3">
      <CheckIcon state={c.state} className="mt-[3px]" />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="flex min-w-0 items-baseline gap-2">
          {name}
          {href &&
            (c.deployment ? (
              <Link href={href} className={linkClass}>
                {link}
              </Link>
            ) : (
              <a href={href} target="_blank" rel="noreferrer noopener" className={linkClass}>
                {link}
              </a>
            ))}
        </span>
        {line && <span className={cn("text-xs", c.state === "failure" ? "text-status-failed-fg" : "text-muted")}>{line}</span>}
      </div>
    </div>
  );
}

/** A name beside its value from md up, above it on a phone (board ReworkPull). */
function Row({ name, children }: { name: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1 border-t border-subtle py-2.5 first:border-t-0 first:pt-0 last:pb-0 md:grid md:grid-cols-[96px_minmax(0,1fr)] md:gap-3">
      <dt className="text-muted">{name}</dt>
      <dd className="min-w-0 text-fg">{children}</dd>
    </div>
  );
}

/** The issues it names (D-81) with what moves them, the agent whose branch
 *  it is (D-102), and what it changed. */
function About({ p, now }: { p: PullRequestDetail; now: number }) {
  const { base } = useCode();
  const projectId = useParams<{ id: string }>().id;
  const issues = p.issues ?? [];
  const changed = (p.deploys ?? []).filter((d) => (d.files_changed ?? 0) > 0);
  const ab = p.agent_branch;
  if (issues.length === 0 && !ab && changed.length === 0 && !p.merge_sha && !p.docs?.length) return null;
  const note = (status: string) =>
    status === "shipped"
      ? "Shipped when the merge went live."
      : status === "in_review" && p.state === "open"
        ? "Moved to In review when the PR opened. Moves to Shipped when the merge deploy is live."
        : status === "in_review"
          ? "Moves to Shipped when the merge deploy is live."
          : "";
  return (
    <section aria-labelledby="about-h" className={card}>
      <div className="border-b border-subtle px-4 py-3">
        <h2 id="about-h" className="text-[15px] font-semibold leading-[22px] text-fg">
          About
        </h2>
      </div>
      <dl className="flex flex-col px-4 py-3.5 text-[13px]">
        {issues.length > 0 && (
          <Row name={issues.length === 1 ? "Issue" : "Issues"}>
            <span className="flex flex-col gap-2.5">
              {issues.map((i) => (
                <span key={i.key} className="flex flex-col gap-1">
                  <Link href={issueHref(projectId, i.number)} className="hover:underline">
                    <span className="text-link">{i.key}</span> {i.title}
                  </Link>
                  <span className="flex flex-wrap items-center gap-2">
                    <IssueStatusChip status={i.status} small />
                    {note(i.status) && <span className="text-xs text-muted">{note(i.status)}</span>}
                  </span>
                </span>
              ))}
            </span>
          </Row>
        )}
        {ab && (
          <Row name="Agent">
            {ab.agent}, on{" "}
            <Link href="/settings/agents" className="inline-link">
              {ab.person}’s connection
            </Link>
            ; {passwordLine(ab.password_expires_at, now)}
          </Row>
        )}
        {changed.length > 0 && (
          <Row name="Services changed">
            <span className="flex flex-wrap gap-1.5">
              {changed.map((d) => (
                <span key={d.service_id} className="inline-flex min-h-6 items-center gap-1.5 rounded-md border border-strong px-2 py-0.5 font-mono text-xs text-fg">
                  {d.service_name}
                  <span className="text-muted">·</span>
                  <span className="flex-none">{d.files_changed === 1 ? "1 file" : `${d.files_changed} files`}</span>
                </span>
              ))}
            </span>
          </Row>
        )}
        {p.merge_sha && (
          <Row name="Merge commit">
            <Link href={commitHref(base, p.merge_sha)} className="font-mono text-xs text-link hover:underline">
              {shortSha(p.merge_sha)}
            </Link>
          </Row>
        )}
        {!!p.docs?.length && (
          <Row name="Referenced in docs">
            <span className="flex flex-col gap-1">
              {p.docs.map((d) => (
                <Link key={d.id} href={`/projects/${d.project_id}/docs/${d.id}`} className="text-link hover:underline">
                  {d.title}
                </Link>
              ))}
            </span>
          </Row>
        )}
      </dl>
    </section>
  );
}

function Commits({ p }: { p: PullRequestDetail }) {
  const { base } = useCode();
  const [now] = useState(() => Date.now());
  const commits = p.compare?.commits ?? [];
  if (!p.compare) return <p className={cn(card, "p-5 text-[13px] text-fg/70")}>Its commits can’t be read: the branch is gone.</p>;
  return (
    <>
      <ul aria-label="Commits" className={cn(card, "text-sm")}>
        {commits.map((c, i) => (
          <li key={c.sha} className={cn("flex flex-wrap items-center gap-3 px-4 py-3", i > 0 && "border-t border-subtle")}>
            <GitCommitHorizontal className="h-[15px] w-[15px] flex-none text-muted" aria-hidden="true" />
            <div className="flex min-w-0 flex-[1_1_360px] flex-col gap-[3px]">
              <Link href={commitHref(base, c.sha)} className="truncate font-medium text-fg hover:underline">
                {c.subject}
              </Link>
              <span className="flex items-center gap-1.5 text-xs text-muted">
                <Avatar name={c.author_name} size={18} />
                <span className="text-fg/80" title={c.author_email}>
                  {c.author_name}
                </span>
                committed {ago(c.committed_at, now)}
              </span>
            </div>
            <Link href={commitHref(base, c.sha)} className="rounded-md border border-strong px-2.5 font-mono text-xs leading-7 text-fg hover:bg-raised">
              {shortSha(c.sha)}
            </Link>
          </li>
        ))}
      </ul>
      {p.compare.commits_truncated && <p className="text-xs text-muted">The first 250 commits are listed.</p>}
    </>
  );
}

function Changes({ p }: { p: PullRequestDetail }) {
  const { api, base } = useCode();
  const diff = useInfiniteQuery({
    queryKey: ["pull-diff", api.id, p.number, p.head_sha],
    queryFn: ({ pageParam }) => api.pullDiff(p.number, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next,
    enabled: !!p.compare,
  });
  const files = diff.data?.pages.flatMap((pg) => pg.data) ?? [];
  const [key, setKey] = useState(0);
  const [allOpen, setAllOpen] = useState(true);
  if (!p.compare) return <p className={cn(card, "p-5 text-[13px] text-fg/70")}>Its changes can’t be read: the branch is gone.</p>;
  const ref = p.state === "merged" || !p.head_exists ? p.head_sha : p.head_branch;
  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-2.5 text-[13px] text-fg/70">
        <span>
          Changes from <code className="font-mono text-xs text-fg">{p.head_branch}</code> since it left <code className="font-mono text-xs text-fg">{p.base_branch}</code> at{" "}
          <Link href={commitHref(base, p.compare.merge_base)} className="font-mono text-xs inline-link">
            {shortSha(p.compare.merge_base)}
          </Link>
        </span>
        <button
          type="button"
          onClick={() => {
            setAllOpen(!allOpen);
            setKey((k) => k + 1);
          }}
          className="touch-target h-[30px] rounded-md border border-strong bg-surface px-2.5 text-xs text-fg hover:bg-raised"
        >
          {allOpen ? "Collapse all" : "Expand all"}
        </button>
      </div>
      {diff.isLoading && <div role="status" aria-label="Loading" className="h-40 rounded-[10px] bg-surface motion-safe:animate-pulse" />}
      {readFailed(diff) && (
        <div className={cn(card, "px-4 py-3")}>
          <LoadFailed what="the changes" onRetry={() => void diff.refetch()} />
        </div>
      )}
      {files.map((f) => (
        <DiffFile key={`${key}-${f.old_path ?? ""}${f.path}`} f={f} open={allOpen} view={f.status === "deleted" ? undefined : codeHref(base, "blob", ref, f.path)} />
      ))}
      <NextPageFailed query={diff} what="more files" />
      {diff.hasNextPage && (
        <div className="flex justify-center">
          <button type="button" onClick={() => diff.fetchNextPage()} disabled={diff.isFetchingNextPage} className={outline}>
            {diff.isFetchingNextPage ? "Loading…" : `More files`}
          </button>
        </div>
      )}
      {p.compare.files_truncated && <p className="text-xs text-muted">More than 3,000 files changed; the first 3,000 are listed.</p>}
    </>
  );
}

function Checks({ p }: { p: PullRequestDetail }) {
  const { base } = useCode();
  const [now] = useState(() => Date.now());
  const checks = byAttention(p.head_checks?.checks ?? []);
  const s = p.head_checks?.summary;
  return (
    <div className="flex flex-wrap items-start gap-7">
      <div className="flex min-w-0 flex-[999_1_560px] flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2 text-[13px] text-fg/70">
          {s && (
            <span
              className={cn(
                "inline-flex h-6 items-center gap-1.5 rounded-full px-2.5 text-xs font-medium",
                s.state === "failure" ? "bg-status-failed-soft text-status-failed-fg" : s.state === "success" ? "bg-status-running-soft text-status-running-fg" : "bg-status-building-soft text-status-building-fg",
              )}
            >
              <CheckIcon state={s.state === "pending" ? "running" : s.state} className="h-3 w-3 text-current" />
              {summaryText(s)}
            </span>
          )}
          <span>
            on the head commit{" "}
            <Link href={commitHref(base, p.head_sha)} className="font-mono text-xs inline-link">
              {shortSha(p.head_sha)}
            </Link>
            ; each push to the branch starts them again.
          </span>
        </div>
        {checks.length === 0 ? (
          <section className="flex flex-col gap-2 rounded-[10px] border border-dashed border-strong bg-surface/60 px-6 py-7">
            <h2 className="text-[15px] font-semibold leading-[22px] text-fg">No checks on {shortSha(p.head_sha)}</h2>
            <p className="max-w-[560px] text-[13px] leading-[19px] text-fg/70">
              Nothing builds from {p.head_branch}, and CI has reported nothing. A CI reports its runs here with an API key that has the checks scope.
            </p>
          </section>
        ) : (
          <>
            <ul className={card}>
              {checks.map((c, i) => (
                <li key={c.name} className={cn(i > 0 && "border-t border-subtle")}>
                  <CheckLine c={c} now={now} />
                </li>
              ))}
            </ul>
            <p className="text-xs leading-[17px] text-muted">Checks don’t block a merge yet: the merge box says when one fails.</p>
          </>
        )}
      </div>
      <aside className="flex min-w-0 flex-[1_1_300px] flex-col gap-4">
        <ReportChecksCard />
      </aside>
    </div>
  );
}
