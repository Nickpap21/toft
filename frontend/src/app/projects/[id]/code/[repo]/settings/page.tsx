"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Lock } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useOrgRole } from "@/context/AuthContext";
import { useReauth } from "@/components/reauth";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { RefPicker } from "@/components/code/parts";
import { useCode } from "@/components/code/repo";
import { MirrorsCard, WebhooksCard } from "@/components/code/mirrors";
import { card, outline } from "@/app/projects/_lib/ui";

// Repository settings (board RepositorySettings, owners): the default
// branch, its protection, and deleting the repository (D-75); mirrors and
// webhooks (D-79).
export default function RepositorySettingsPage() {
  const { projectId, api, detail, repo, repos } = useCode();
  const owner = useOrgRole() === "owner";
  const queryClient = useQueryClient();
  const confirmPassword = useReauth();
  const [branch, setBranch] = useState(detail.default_branch);
  const [deleting, setDeleting] = useState(false);
  useDocumentTitle(`Settings · ${repo.name} · Toft`);
  const refresh = (d: Awaited<ReturnType<typeof api.update>>) => {
    queryClient.setQueryData(["repository", projectId, repo.id], d);
    queryClient.invalidateQueries({ queryKey: ["repositories", projectId] });
  };
  const saveBranch = useMutation({
    mutationFn: async () => {
      const why = detail.protect_default_branch
        ? `Protection moves from ${detail.default_branch} to ${branch}, so Toft asks for your password first.`
        : `Changing the default branch asks for your password first.`;
      if (!(await confirmPassword(why))) {
        throw new Error("The default branch stays without your password.");
      }
      return api.update({ default_branch: branch });
    },
    onSuccess: (d) => {
      refresh(d);
      toast.success(`${branch} is the default branch`);
    },
    onError: (err) => toast.error(err instanceof Error ? err.message : "Couldn’t change the default branch"),
  });
  const protect = useMutation({
    mutationFn: async (on: boolean) => {
      if (!on && !(await confirmPassword(`Turning protection off lets anyone who can push rewrite ${detail.default_branch}, so Toft asks for your password first.`))) {
        throw new Error("Protection stays on without your password.");
      }
      return api.update({ protect_default_branch: on });
    },
    onSuccess: (d) => {
      refresh(d);
      toast.success(d.protect_default_branch ? `${d.default_branch} is protected` : `${d.default_branch} is no longer protected`);
    },
    onError: (err) => toast.error(err instanceof Error ? err.message : "Couldn’t change protection"),
  });

  if (!owner) {
    return <p className={cn(card, "max-w-[640px] p-5 text-[13px] text-fg/70")}>Only owners change a repository’s settings.</p>;
  }
  const on = detail.protect_default_branch;
  return (
    <>
      <div className="flex flex-col gap-1">
        <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">Repository settings</h1>
        <p className="text-[13px] text-fg/70">
          <code className="font-mono text-xs text-fg">{repo.full_name}</code> · only owners change these
        </p>
      </div>
      <div className="flex max-w-[760px] flex-col gap-4">
        <section aria-labelledby="def-h" className={cn(card, "flex flex-col gap-3 px-5 py-[18px]")}>
          <div className="flex flex-col gap-1">
            <h2 id="def-h" className="text-[15px] font-semibold leading-[22px] text-fg">
              Default branch
            </h2>
            <p className="text-[13px] text-fg/70">A clone checks it out, Files opens on it, and new pull requests go into it.</p>
          </div>
          {detail.empty ? (
            <p className="text-[13px] text-muted">The first push creates it.</p>
          ) : (
            <div className="flex flex-wrap items-center gap-2.5">
              <RefPicker api={api} current={branch} onPick={setBranch} label="Default branch" branchesOnly />
              <button type="button" onClick={() => saveBranch.mutate()} disabled={branch === detail.default_branch || saveBranch.isPending} className={outline}>
                Save
              </button>
            </div>
          )}
        </section>

        <section aria-labelledby="prot-h" className={cn(card, "flex flex-col gap-3 px-5 py-[18px]")}>
          <div className="flex items-start justify-between gap-4">
            <div className="flex flex-col gap-1">
              <h2 id="prot-h" className="flex items-center gap-2 text-[15px] font-semibold leading-[22px] text-fg [overflow-wrap:anywhere]">
                <Lock className="h-[15px] w-[15px]" aria-hidden="true" />
                Protect {detail.default_branch}
              </h2>
              <p id="prot-d" className="text-[13px] text-fg/70 [overflow-wrap:anywhere]">
                Nobody pushes to {detail.default_branch}, force-pushes it or deletes it, owners included. It changes only by merging a pull request.
              </p>
            </div>
            <button
              type="button"
              role="switch"
              aria-checked={on}
              aria-labelledby="prot-h"
              aria-describedby="prot-d"
              disabled={protect.isPending}
              onClick={() => protect.mutate(!on)}
              className={cn(
                "relative mt-0.5 h-[22px] w-10 flex-none rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-60",
                on ? "bg-accent" : "bg-strong",
              )}
            >
              <span className={cn("absolute top-[3px] h-4 w-4 rounded-full bg-on-accent transition-[translate] left-[3px]", on && "translate-x-[18px]")} />
            </button>
          </div>
          {on && detail.protected_branch && detail.protected_branch !== detail.default_branch && (
            <p role="alert" className="text-[13px] text-status-failed-fg [overflow-wrap:anywhere]">
              Pushes are refused to {detail.protected_branch}, not {detail.default_branch}. Turn protection off and on again to move it.
            </p>
          )}
          <p className="border-t border-subtle pt-3 text-xs text-muted">
            {on
              ? "Turning it off asks for your password; both are in the workspace’s audit log."
              : `Off: owners and developers push to ${detail.default_branch} directly. Turning it on or off is in the audit log.`}
          </p>
        </section>

        <MirrorsCard />
        <WebhooksCard />

        <section aria-labelledby="del-h" className="flex flex-wrap items-center justify-between gap-3 rounded-[10px] border border-status-failed/35 bg-surface px-5 py-[18px]">
          <div className="flex flex-[1_1_360px] flex-col gap-1">
            <h2 id="del-h" className="text-[15px] font-semibold leading-[22px] text-fg">
              Delete this repository
            </h2>
            <p className="text-[13px] text-fg/70">
              {repos.length < 2
                ? "A project keeps at least one repository, so this one can’t be deleted."
                : `Deletes ${repo.name} with every branch, tag and pull request. It can’t be undone.`}
            </p>
          </div>
          <button
            type="button"
            disabled={repos.length < 2}
            onClick={() => setDeleting(true)}
            className="h-9 rounded-lg bg-danger px-3.5 text-[13px] font-semibold text-on-danger hover:bg-danger/90 disabled:opacity-50"
          >
            Delete repository…
          </button>
        </section>
      </div>
      <DeleteDialog open={deleting} onClose={() => setDeleting(false)} />
    </>
  );
}

function DeleteDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { projectId, api, repo, detail } = useCode();
  const router = useRouter();
  const queryClient = useQueryClient();
  const confirmPassword = useReauth();
  const [typed, setTyped] = useState("");
  const remove = useMutation({
    mutationFn: async () => {
      if (!(await confirmPassword(`Deleting ${repo.name} can’t be undone, so Toft asks for your password first.`))) {
        throw new Error("The repository stays without your password.");
      }
      return api.remove(typed);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["repositories", projectId] });
      toast.success(`${repo.name} deleted`);
      onClose();
      router.push(`/projects/${projectId}/code`);
    },
  });
  const close = () => {
    setTyped("");
    remove.reset();
    onClose();
  };
  const err = remove.error ? (remove.error instanceof Error ? remove.error.message : "Couldn’t delete it") : "";
  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogContent className="max-w-[460px]">
        <form
          className="flex flex-col gap-3.5"
          onSubmit={(e) => {
            e.preventDefault();
            if (typed === repo.name) remove.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>Delete {repo.name}?</DialogTitle>
            <DialogDescription>
              Its {detail.branch_count === 1 ? "branch" : `${detail.branch_count} branches`}, {detail.tag_count === 1 ? "tag" : `${detail.tag_count} tags`} and{" "}
              {detail.pulls.open + detail.pulls.merged + detail.pulls.closed} pull requests go with it. Clones elsewhere keep working; pushes to this URL stop.
            </DialogDescription>
          </DialogHeader>
          <label className="flex flex-col gap-1.5 text-[13px] font-medium text-fg">
            <span>
              Type <code className="font-mono text-xs">{repo.name}</code> to confirm
            </span>
            <input
              autoFocus
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              spellCheck={false}
              autoComplete="off"
              className="h-9 rounded-lg border border-strong bg-ground px-2.5 font-mono text-[13px] text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
            />
          </label>
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
              disabled={typed !== repo.name || remove.isPending}
              className="h-9 rounded-lg bg-danger px-3.5 text-[13px] font-semibold text-on-danger hover:bg-danger/90 disabled:opacity-50"
            >
              {remove.isPending ? "Deleting…" : "Delete repository"}
            </button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
