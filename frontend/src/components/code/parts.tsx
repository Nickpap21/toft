"use client";

import { useMemo, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { BookMarked, Check, ChevronDown, Copy, GitBranch, GitPullRequest, Lock, Plus, Tag } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { ApiError } from "@/lib/api/client";
import { codeService, type GitCommit, type PullRequest, type RepositoryDetail, type repoService } from "@/lib/api/code.service";
import { useOrgRole } from "@/context/AuthContext";
import type { IssueRef } from "@/lib/api/issue.service";
import { issueHref, STATUS, StatusIcon } from "@/components/issues/meta";
import { cn, initials } from "@/lib/utils";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import { DeployChip } from "./checks";
import { ImportForm } from "./mirrors";
import { ago, codeHref, commitHref, pullHref, repoBase, shortSha, useCode } from "./repo";

type Api = ReturnType<typeof repoService>;

const mono = "font-mono";
const tab = "inline-flex h-[30px] items-center gap-1.5 rounded-md px-2.5 text-[13px]";

/** Files, Commits, Branches, Tags, Pull requests, Pipelines and, for
 *  owners, Settings under the project's Code page (boards Code,
 *  PullRequests, CodePipelines, RepositorySettings). */
export function CodeNav() {
  const { base, detail } = useCode();
  const pathname = usePathname();
  const ref = useSearchParams().get("ref") ?? "";
  const owner = useOrgRole() === "owner";
  const rest = decodeURIComponent(pathname).slice(decodeURIComponent(base).length);
  const current = rest.startsWith("/commits")
    ? "commits"
    : rest.startsWith("/branches")
      ? "branches"
      : rest.startsWith("/tags")
        ? "tags"
        : rest.startsWith("/pulls")
          ? "pulls"
          : rest.startsWith("/settings")
            ? "settings"
            : rest.startsWith("/pipelines")
              ? "pipelines"
              : "files";
  const items: { id: string; label: string; href: string; n?: number }[] = [
    { id: "files", label: "Files", href: codeHref(base, "", ref) },
    { id: "commits", label: "Commits", href: codeHref(base, "commits", ref) },
    { id: "branches", label: "Branches", href: codeHref(base, "branches"), n: detail.branch_count },
    { id: "tags", label: "Tags", href: codeHref(base, "tags"), n: detail.tag_count },
    { id: "pulls", label: "Pull requests", href: codeHref(base, "pulls"), n: detail.pulls.open || undefined },
    { id: "pipelines", label: "Pipelines", href: codeHref(base, "pipelines") },
    ...(owner ? [{ id: "settings", label: "Settings", href: codeHref(base, "settings") }] : []),
  ];
  return (
    <nav aria-label="Code" className="flex flex-wrap gap-1">
      {items.map((i) => (
        <Link
          key={i.id}
          href={i.href}
          aria-current={current === i.id ? "page" : undefined}
          className={cn(tab, current === i.id ? "bg-raised font-medium text-fg" : "text-fg/70 hover:text-fg")}
        >
          {i.label}
          {i.n !== undefined && <span className={cn(mono, "text-[11px] text-muted")}>{i.n}</span>}
        </Link>
      ))}
    </nav>
  );
}

/** The repository a page shows, and the project's others, with New
 *  repository for owners and developers (board CodeRepos). */
export function RepoPicker() {
  const { projectId, repo, repos } = useCode();
  const router = useRouter();
  const pathname = usePathname();
  const role = useOrgRole();
  const canCreate = role === "owner" || role === "developer";
  const [creating, setCreating] = useState(false);
  const [now] = useState(() => Date.now());
  // The same page in another repository, without a ref or path it may lack.
  const page = decodeURIComponent(pathname).slice(decodeURIComponent(repoBase(projectId, repo.name)).length).split("/")[1] ?? "";
  const keep = ["commits", "branches", "tags", "pulls", "settings"].includes(page) ? `/${page}` : "";
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          aria-label={`Repository: ${repo.name}. Switch repository`}
          className="inline-flex h-8 max-w-[260px] items-center gap-[7px] rounded-lg border border-strong bg-surface px-2.5 text-[13px] font-semibold text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        >
          <BookMarked className="h-3.5 w-3.5 flex-none" aria-hidden="true" />
          <span className="truncate">{repo.name}</span>
          <ChevronDown className="h-[13px] w-[13px] flex-none text-muted" aria-hidden="true" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-[320px]">
          <DropdownMenuLabel>Repositories in this project</DropdownMenuLabel>
          {repos.map((r) => (
            <DropdownMenuItem key={r.id} onSelect={() => router.push(repoBase(projectId, r.name) + keep)} className="items-start">
              <BookMarked className="mt-0.5 h-3.5 w-3.5 flex-none text-muted" aria-hidden="true" />
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate text-[13px] font-medium">{r.name}</span>
                <span className="text-xs text-muted">
                  {r.open_pulls > 0 ? `${r.open_pulls} open pull ${r.open_pulls === 1 ? "request" : "requests"} · ` : ""}
                  made {ago(r.created_at, now)}
                </span>
              </span>
              {r.id === repo.id && <Check className="mt-0.5 h-3.5 w-3.5 flex-none" aria-label="Current" />}
            </DropdownMenuItem>
          ))}
          {canCreate && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem onSelect={() => setCreating(true)}>
                <Plus className="h-3.5 w-3.5" aria-hidden="true" />
                New repository
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      <NewRepoDialog open={creating} onClose={() => setCreating(false)} />
    </>
  );
}

/** New repository in the project (board CodeRepos, view=new), empty or
 *  imported from a URL (CodeImport, state=new; D-79). */
function NewRepoDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { projectId, repo, git } = useCode();
  const router = useRouter();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [mode, setMode] = useState<"empty" | "import">("empty");
  const slug = repo.full_name.split("/")[0];
  const clean = name.trim().toLowerCase().replace(/\.git$/, "");
  const valid = /^[a-z0-9]([a-z0-9._-]{0,98}[a-z0-9])?$/.test(clean);
  const create = useMutation({
    mutationFn: () => codeService.createRepository(projectId, clean),
    onSuccess: (r) => {
      queryClient.invalidateQueries({ queryKey: ["repositories", projectId] });
      toast.success(`${r.name} created`);
      setName("");
      onClose();
      router.push(repoBase(projectId, r.name));
    },
  });
  const close = () => {
    setName("");
    setMode("empty");
    create.reset();
    onClose();
  };
  const err = create.error instanceof ApiError ? create.error.message : create.error ? "Couldn’t create the repository" : "";
  const modes = (
    <div role="radiogroup" aria-label="Start with" className="flex rounded-lg border border-subtle bg-ground p-[3px]">
      {(["empty", "import"] as const).map((m) => (
        <button
          key={m}
          type="button"
          role="radio"
          aria-checked={mode === m}
          onClick={() => setMode(m)}
          className={cn(
            "h-[30px] flex-1 rounded-md text-[13px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus",
            mode === m ? "bg-raised font-medium text-fg" : "text-fg/70 hover:text-fg",
          )}
        >
          {m === "empty" ? "Empty" : "Import from a URL"}
        </button>
      ))}
    </div>
  );
  if (mode === "import") {
    return (
      <Dialog open={open} onOpenChange={(o) => !o && close()}>
        <DialogContent className="max-w-[540px]">
          <DialogHeader>
            <DialogTitle>New repository in this project</DialogTitle>
            <DialogDescription>Every branch and tag, with their full history. Pull requests, issues, releases and LFS files stay where they are.</DialogDescription>
          </DialogHeader>
          {modes}
          <ImportForm
            projectId={projectId}
            named={{ slug }}
            onCancel={close}
            onDone={(n) => {
              close();
              if (n) router.push(repoBase(projectId, n));
            }}
          />
        </DialogContent>
      </Dialog>
    );
  }
  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogContent className="max-w-[480px]">
        <form
          className="flex flex-col gap-3.5"
          onSubmit={(e) => {
            e.preventDefault();
            if (valid) create.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>New repository</DialogTitle>
            <DialogDescription>It starts empty. Every member of the workspace reads it; owners and developers push.</DialogDescription>
          </DialogHeader>
          {modes}
          <label className="flex flex-col gap-1.5 text-[13px] font-medium text-fg">
            Name
            <span className="flex items-center overflow-hidden rounded-lg border border-strong bg-ground focus-within:ring-2 focus-within:ring-focus">
              <span className="pl-2.5 pr-1 font-mono text-[13px] text-muted">{slug}/</span>
              <input
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                aria-invalid={!!name && !valid}
                aria-describedby="new-repo-help"
                spellCheck={false}
                autoComplete="off"
                className="h-9 min-w-0 flex-1 bg-transparent pr-2.5 font-mono text-[13px] text-fg outline-none"
              />
            </span>
            <span id="new-repo-help" className={cn("text-xs font-normal", name && !valid ? "text-status-failed-fg" : "text-muted")}>
              Lowercase letters, digits, dots, dashes and underscores, starting and ending with a letter or digit. It’s part of the clone URL and can’t be
              changed yet.
            </span>
          </label>
          {valid && git?.ssh_host && (
            <div className="flex flex-col gap-1 rounded-lg border border-subtle bg-ground px-3 py-2.5">
              <span className="text-xs text-muted">Clone URL</span>
              <code className="break-all font-mono text-xs text-fg">
                ssh://git@{git.ssh_host}:{git.ssh_port}/{slug}/{clean}.git
              </code>
            </div>
          )}
          {err && (
            <p role="alert" className="text-[13px] text-status-failed-fg">
              {err}
            </p>
          )}
          <DialogFooter>
            <button type="button" onClick={close} className="h-9 rounded-lg border border-subtle px-3 text-[13px] font-medium text-fg hover:bg-raised">
              Cancel
            </button>
            <button
              type="submit"
              disabled={!valid || create.isPending}
              className="h-9 rounded-lg bg-accent px-3.5 text-[13px] font-medium text-on-accent hover:bg-accent/90 disabled:opacity-50"
            >
              {create.isPending ? "Creating…" : "Create repository"}
            </button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** The branch or tag a page shows, and a menu to switch (Code's selector);
 *  branchesOnly for a pull request's base and head. */
export function RefPicker({
  api,
  current,
  hrefFor,
  onPick,
  label = "Branch or tag",
  branchesOnly,
}: {
  api: Api;
  current: string;
  hrefFor?: (ref: string) => string;
  onPick?: (ref: string) => void;
  label?: string;
  branchesOnly?: boolean;
}) {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");
  const branches = useQuery({ queryKey: ["code-branches-all", api.id], queryFn: () => api.branches(undefined, 200), enabled: open });
  const tags = useQuery({ queryKey: ["code-tags-all", api.id], queryFn: () => api.tags(undefined, 200), enabled: open && !branchesOnly });
  const f = filter.trim().toLowerCase();
  const shown = <T extends { name: string }>(xs?: T[]) => (xs ?? []).filter((x) => x.name.toLowerCase().includes(f)).slice(0, 50);
  const isSha = /^[0-9a-f]{40,64}$/.test(current);
  const pick = (ref: string) => (onPick ? onPick(ref) : hrefFor && router.push(hrefFor(ref)));
  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger
        aria-label={`${isSha ? "Commit" : label}: ${current || "none"}. Change`}
        className="inline-flex h-8 max-w-[260px] items-center gap-1.5 rounded-lg border border-strong bg-surface px-2.5 text-[13px] text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
      >
        <GitBranch className="h-3.5 w-3.5 flex-none" aria-hidden="true" />
        <span className={cn(mono, "truncate text-xs")}>{isSha ? shortSha(current) : current || "Pick a branch"}</span>
        <ChevronDown className="h-[13px] w-[13px] flex-none text-muted" aria-hidden="true" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-[300px]">
        <div className="p-1.5">
          <input
            autoFocus
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            // The menu's typeahead would take the keys otherwise.
            onKeyDown={(e) => e.key !== "Escape" && e.key !== "ArrowDown" && e.stopPropagation()}
            placeholder={branchesOnly ? "Find a branch" : "Find a branch or tag"}
            aria-label={branchesOnly ? "Find a branch" : "Find a branch or tag"}
            className="h-8 w-full rounded-md border border-strong bg-ground px-2 text-[13px] text-fg placeholder:text-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
          />
        </div>
        <div className="max-h-[320px] overflow-y-auto">
          {[
            { label: "Branches", icon: GitBranch, items: shown(branches.data?.data) },
            { label: "Tags", icon: Tag, items: branchesOnly ? [] : shown(tags.data?.data) },
          ].map((g) =>
            g.items.length ? (
              <div key={g.label}>
                <DropdownMenuLabel>{g.label}</DropdownMenuLabel>
                {g.items.map((x) => (
                  <DropdownMenuItem key={g.label + x.name} onSelect={() => pick(x.name)}>
                    <g.icon className="h-3.5 w-3.5 flex-none text-muted" aria-hidden="true" />
                    <span className={cn(mono, "min-w-0 flex-1 truncate text-xs")}>{x.name}</span>
                    {x.name === current && <Check className="h-3.5 w-3.5 flex-none" aria-label="Current" />}
                  </DropdownMenuItem>
                ))}
              </div>
            ) : null,
          )}
          {branches.isLoading && <p className="px-3 py-2 text-[13px] text-muted">Loading…</p>}
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** A person's initials; an agent's (agent set) in the link tint, as AgentWork. */
export function Avatar({ name, size = 22, agent }: { name: string; size?: number; agent?: string }) {
  return (
    <span
      aria-hidden="true"
      style={{ width: size, height: size, fontSize: 10 }}
      className={cn("flex flex-none items-center justify-center rounded-full font-semibold", agent ? "bg-link/15 text-link" : "bg-raised text-fg")}
    >
      {initials(agent || name)}
    </span>
  );
}

/** The newest commit of what a page shows, above its files (Code, CodeFile). */
export function CommitBar({ commit, now }: { commit: GitCommit; now: number }) {
  const { base } = useCode();
  return (
    <div className="flex flex-wrap items-center gap-2.5 border-b border-subtle bg-raised/50 px-4 py-2.5 text-[13px]">
      <Avatar name={commit.author_name} />
      <span className="font-medium text-fg" title={commit.author_email}>
        {commit.author_name}
      </span>
      <Link href={commitHref(base, commit.sha)} className="min-w-0 flex-[1_1_200px] truncate text-fg/80 hover:text-fg hover:underline">
        {commit.subject}
      </Link>
      <DeployChip sha={commit.sha} />
      <Link href={commitHref(base, commit.sha)} className={cn(mono, "text-xs text-fg/70 hover:text-fg")}>
        {shortSha(commit.sha)}
      </Link>
      <span className="text-xs text-muted">{ago(commit.committed_at, now)}</span>
    </div>
  );
}

/** Copy text, then a check for a moment. */
export function CopyButton({ text, label, className, children }: { text: string; label: string; className?: string; children?: React.ReactNode }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      aria-label={children ? undefined : copied ? "Copied" : label}
      className={className}
      onClick={() =>
        navigator.clipboard.writeText(text).then(
          () => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          },
          () => toast.error("Couldn’t copy: select it and copy it"),
        )
      }
    >
      {copied ? <Check className="h-[13px] w-[13px]" aria-hidden="true" /> : <Copy className="h-[13px] w-[13px]" aria-hidden="true" />}
      {children}
    </button>
  );
}

/** The repository's name, then each directory of path as a link, the last
 *  one current (CodeFile). */
export function PathCrumbs({ refName, path }: { refName: string; path: string }) {
  const { base, repo } = useCode();
  const parts = path ? path.split("/") : [];
  return (
    <nav aria-label="Path" className="min-w-0 flex-[1_1_320px]">
      <ol className={cn(mono, "flex flex-wrap items-center gap-1 text-sm")}>
        <li>
          <Link href={codeHref(base, "", refName)} className="font-medium text-link hover:underline">
            {repo.name}
          </Link>
        </li>
        {parts.map((p, i) => (
          <li key={i} className="flex items-center gap-1">
            <span aria-hidden="true" className="text-muted">
              /
            </span>
            {i === parts.length - 1 ? (
              <span aria-current="page" className="font-semibold text-fg">
                {p}
              </span>
            ) : (
              <Link href={codeHref(base, "", refName, parts.slice(0, i + 1).join("/"))} className="text-link hover:underline">
                {p}
              </Link>
            )}
          </li>
        ))}
      </ol>
    </nav>
  );
}

/** Clone over SSH or HTTPS (Code's Clone card). */
export function CloneCard({ detail, sshHost, sshPort }: { detail: RepositoryDetail; sshHost?: string; sshPort?: number }) {
  const [proto, setProto] = useState<"ssh" | "https">("ssh");
  const keys = useQuery({ queryKey: ["ssh-keys"], queryFn: () => codeService.sshKeys() });
  const ssh = proto === "ssh";
  const url = ssh ? detail.ssh_url : detail.https_url;
  const n = keys.data?.length;
  const pressed = (on: boolean) => cn("h-[26px] rounded-md px-2.5 text-xs font-medium", on ? "bg-raised text-fg" : "text-fg/70 hover:text-fg");
  return (
    <section aria-labelledby="clone-h" className="overflow-hidden rounded-[10px] border border-subtle bg-surface">
      <div className="flex items-center justify-between gap-2 border-b border-subtle px-4 py-3">
        <h2 id="clone-h" className="text-[15px] font-semibold leading-[22px] text-fg">
          Clone
        </h2>
        <span role="group" aria-label="Protocol" className="inline-flex gap-0.5 rounded-lg border border-subtle bg-ground p-0.5">
          <button type="button" aria-pressed={ssh} onClick={() => setProto("ssh")} className={pressed(ssh)}>
            SSH
          </button>
          <button type="button" aria-pressed={!ssh} onClick={() => setProto("https")} className={pressed(!ssh)}>
            HTTPS
          </button>
        </span>
      </div>
      <div className="flex flex-col gap-2.5 px-4 pb-4 pt-3.5">
        <span id="clone-url-label" className="text-xs font-medium text-muted">
          {ssh ? `Clone over SSH, port ${sshPort ?? 2222}` : "Clone over HTTPS"}
        </span>
        <span className="flex items-start overflow-hidden rounded-lg border border-strong bg-ground">
          <code aria-labelledby="clone-url-label" className={cn(mono, "min-w-0 flex-1 select-all break-all px-2.5 py-[7px] text-xs leading-[18px] text-fg")}>
            {url}
          </code>
          <CopyButton
            text={url}
            label={ssh ? "Copy SSH clone URL" : "Copy HTTPS clone URL"}
            className="flex min-h-8 w-[34px] flex-none items-center justify-center self-stretch border-l border-subtle text-muted hover:text-fg"
          />
        </span>
        <p className="text-[13px] leading-[19px] text-fg/70">
          {ssh ? (
            <>
              Git listens on port {sshPort ?? 2222}, so the server’s own SSH on port 22 is untouched.
              {n !== undefined && ` You have ${n === 0 ? "no SSH keys" : n === 1 ? "1 SSH key" : `${n} SSH keys`}.`}{" "}
              <Link href="/settings/account#ssh-h" className="inline-link">
                {n === 0 ? "Add an SSH key" : "Manage SSH keys"}
              </Link>
            </>
          ) : (
            <>
              Use an API key as the password; your account password doesn’t work here.{" "}
              <Link href="/settings/api-keys" className="inline-link">
                Create an API key
              </Link>
            </>
          )}
        </p>
        {detail.pull_mirror && (
          <p className="text-xs text-env-amber-fg">
            Clones and fetches work as usual. Pushes are refused while it mirrors: push to {detail.pull_mirror.url.replace(/^https:\/\//, "").replace(/\.git$/, "")}.
          </p>
        )}
        {ssh && sshHost && (
          <details open className="border-t border-subtle pt-2.5 text-[13px] text-fg/70">
            <summary className="min-h-7 cursor-pointer text-fg">Shorter URLs with an SSH host alias</summary>
            <div className="flex flex-col gap-2 pt-1.5">
              <span>
                Add this to <code className={cn(mono, "text-fg")}>~/.ssh/config</code>:
              </span>
              <pre role="group" tabIndex={0} aria-label="SSH config for the toft alias" className={cn(mono, "overflow-x-auto rounded-lg border border-subtle bg-ground px-3 py-2.5 leading-[19px] text-fg")}>
                {`Host toft\n  HostName ${sshHost}\n  Port ${sshPort ?? 2222}\n  User git`}
              </pre>
              <span>
                Then clone with <code className={cn(mono, "text-fg")}>git clone toft:{detail.full_name}.git</code>
              </span>
            </div>
          </details>
        )}
      </div>
    </section>
  );
}

/** The default branch and the most recently committed to (Code's Branches
 *  card); the default says when it is protected. */
export function BranchesCard({ now }: { now: number }) {
  const { base, api, detail } = useCode();
  const branches = useQuery({ queryKey: ["code-branches-card", api.id], queryFn: () => api.branches(undefined, 6), enabled: !detail.empty });
  const pulls = useOpenPullsByHead();
  return (
    <section aria-labelledby="br-h" className="overflow-hidden rounded-[10px] border border-subtle bg-surface">
      <div className="flex items-center justify-between gap-2 border-b border-subtle px-4 py-3">
        <h2 id="br-h" className="text-[15px] font-semibold leading-[22px] text-fg">
          Branches
        </h2>
        {!detail.empty && (
          <Link href={codeHref(base, "branches")} className="text-[13px] text-link hover:underline">
            All {detail.branch_count}
          </Link>
        )}
      </div>
      {detail.empty ? (
        <p className="px-4 pb-4 pt-3.5 text-[13px] text-fg/70">No branches yet. The first push creates them.</p>
      ) : readFailed(branches) ? (
        <div className="px-4 py-3">
          <LoadFailed what="the branches" onRetry={() => void branches.refetch()} error={branches.error} />
        </div>
      ) : (
        <ul className="py-1">
          {branches.data?.data.map((b) => (
            <li key={b.name} className="flex flex-col gap-[3px] px-4 py-2">
              <Link href={codeHref(base, "", b.name)} className="flex min-w-0 items-center gap-2 text-fg hover:underline">
                <GitBranch className="h-[13px] w-[13px] flex-none text-muted" aria-hidden="true" />
                <span className={cn(mono, "truncate text-xs")}>{b.name}</span>
              </Link>
              <span className="flex flex-wrap items-center gap-1.5 pl-[21px] text-xs text-muted">
                {b.default ? (
                  <>
                    <DefaultChip protect={detail.protect_default_branch} />
                    <span>
                      {shortSha(b.commit.sha)} · {ago(b.commit.committed_at, now)}
                    </span>
                  </>
                ) : (
                  <>
                    {pulls.get(b.name) ? (
                      <PullChip base={base} pull={pulls.get(b.name)!} />
                    ) : (
                      b.issues?.map((i) => <IssueChip key={i.key} base={base} issue={i} />)
                    )}
                    <span>
                      {aheadBehind(b.ahead, b.behind)}
                      {ago(b.commit.committed_at, now)}
                    </span>
                  </>
                )}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/** The repository's open pull requests by head branch (its first 100). */
export function useOpenPullsByHead(): Map<string, PullRequest> {
  const { api, detail } = useCode();
  const q = useQuery({ queryKey: ["code-pulls", api.id, "open"], queryFn: () => api.pulls("open"), enabled: detail.pulls.open > 0 });
  return useMemo(() => new Map((q.data?.data ?? []).map((p) => [p.head_branch, p])), [q.data]);
}

/** "!42 open", "!44 · conflicts" or "!44 · 1 check failing" (board Code's
 *  branch chips). */
export function PullChip({ base, pull }: { base: string; pull: PullRequest }) {
  const failing = pull.checks?.failed ?? 0;
  const conflict = pull.conflicts === true || failing > 0;
  return (
    <Link
      href={pullHref(base, pull.number)}
      className={cn(
        "inline-flex h-5 items-center gap-1 rounded-sm px-1.5 text-[11px] font-medium hover:underline",
        conflict ? "bg-status-pending-soft text-status-pending-fg" : "bg-accent-soft text-link",
      )}
    >
      <GitPullRequest className="h-[11px] w-[11px]" aria-hidden="true" />!{pull.number}{" "}
      {pull.conflicts ? "· conflicts" : failing ? `· ${failing === 1 ? "1 check" : `${failing} checks`} failing` : "open"}
    </Link>
  );
}

/** The issue a branch names, in its status's colours (board Code's
 *  branch chips; D-81). */
export function IssueChip({ base, issue }: { base: string; issue: IssueRef }) {
  return (
    <Link
      href={issueHref(base.split("/code/")[0].replace("/projects/", ""), issue.number)}
      className={cn("inline-flex h-5 items-center gap-1 rounded-sm px-1.5 text-[11px] font-medium hover:underline", STATUS[issue.status].chip)}
    >
      <StatusIcon status={issue.status} size={11} className="text-current" />
      {issue.key}
    </Link>
  );
}

/** "default", or "default · protected" with a lock (board Code). */
export function DefaultChip({ protect }: { protect: boolean }) {
  return (
    <span className="inline-flex h-5 items-center gap-1 rounded-sm bg-raised px-1.5 text-[11px] font-medium text-fg/80">
      {protect && <Lock className="h-[10px] w-[10px]" aria-hidden="true" />}
      {protect ? "default · protected" : "default"}
    </span>
  );
}

/** "6 ahead · ", "1 ahead, 4 behind · ", or "" for no counts. */
export function aheadBehind(ahead: number | null, behind: number | null): string {
  if (ahead === null || behind === null) return "";
  if (ahead === 0 && behind === 0) return "even · ";
  return [ahead ? `${ahead} ahead` : "", behind ? `${behind} behind` : ""].filter(Boolean).join(", ") + " · ";
}
