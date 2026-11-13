"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Plus } from "lucide-react";
import { useReauth } from "@/components/reauth";
import { codeService, gitInfoQuery, type SSHKey } from "@/lib/api/code.service";
import { sinceShort } from "@/lib/time";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import { cn, formatDateTime } from "@/lib/utils";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  cancelButton,
  card,
  dangerButton,
  dialogContent,
  dialogFooter,
  dialogHeader,
  dialogText,
  dialogTitle,
  fieldInput,
  fieldLabel,
  help,
  primaryButton,
  phoneLabel,
  phoneNote,
  phoneRow,
  phoneTd,
  smallButton,
  th,
} from "@/components/settings/settings-frame";

const KEY_TYPES: Record<string, string> = {
  "ssh-ed25519": "ED25519",
  "sk-ssh-ed25519@openssh.com": "ED25519, security key",
  "ecdsa-sha2-nistp256": "ECDSA",
  "ecdsa-sha2-nistp384": "ECDSA",
  "ecdsa-sha2-nistp521": "ECDSA",
  "sk-ecdsa-sha2-nistp256@openssh.com": "ECDSA, security key",
  "ssh-rsa": "RSA",
};

const errorText = (err: unknown) => (err instanceof Error ? err.message : "Couldn’t reach Toft. Try again.");

/** Account → SSH keys (board: Account, D-71): the keys git over SSH signs
 *  in with. Nothing shows on an instance without git. */
export function SSHKeys() {
  const git = useQuery(gitInfoQuery);
  const enabled = git.data?.enabled === true;
  const keys = useQuery({ queryKey: ["ssh-keys"], queryFn: () => codeService.sshKeys(), enabled });
  const [now] = useState(() => Date.now());
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<SSHKey | null>(null);
  if (readFailed(git)) return <LoadFailed what="your SSH keys" onRetry={() => void git.refetch()} error={git.error} />;
  if (!enabled) return null;

  return (
    <section aria-labelledby="ssh-h" className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2">
        <div className="flex flex-col gap-0.5">
          <h2 id="ssh-h" className="text-[15px] font-semibold leading-[22px] text-fg">
            SSH keys
          </h2>
          <span className="text-xs leading-5 text-muted">
            For git over SSH on port {git.data?.ssh_port}. A key reaches every repository your account can, in every
            workspace.
          </span>
        </div>
        <button type="button" className={cn(smallButton, "h-8 bg-surface text-[13px] rounded-lg")} onClick={() => setAdding(true)}>
          <Plus className="h-3.5 w-3.5" aria-hidden="true" />
          Add SSH key
        </button>
      </div>
      <div role="group" tabIndex={0} aria-label="Your SSH keys" className={cn(card, "overflow-x-auto")}>
        <table className="w-full border-collapse text-[13px] max-sm:block sm:min-w-[640px]">
          <thead className="max-sm:hidden">
            <tr>
              <th scope="col" className={th}>
                Title
              </th>
              <th scope="col" className={th}>
                Key
              </th>
              <th scope="col" className={cn(th, "w-[120px]")}>
                Added
              </th>
              <th scope="col" className={cn(th, "w-[140px]")}>
                Last used
              </th>
              <th scope="col" className={cn(th, "w-[110px]")}>
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody className="max-sm:block">
            {keys.error && (
              <tr className="max-sm:block">
                <td colSpan={5} role="alert" className={cn(phoneNote, "py-3.5 text-status-failed-fg")}>
                  Could not load your SSH keys: {errorText(keys.error)}
                </td>
              </tr>
            )}
            {keys.data?.length === 0 && (
              <tr className="max-sm:block">
                <td colSpan={5} className={cn(phoneNote, "py-3.5 text-muted")}>
                  No SSH keys yet. Add the one line of a .pub file, such as{" "}
                  <span className="font-mono text-xs text-fg">~/.ssh/id_ed25519.pub</span>.
                </td>
              </tr>
            )}
            {keys.data?.map((k) => (
              <tr key={k.id} className={phoneRow}>
                <th scope="row" className={cn(phoneTd, "text-left font-medium text-fg max-sm:col-start-1 max-sm:row-start-1")}>
                  {k.title}
                </th>
                <td className={cn(phoneTd, "max-sm:col-span-2")}>
                  <span className="flex flex-col gap-0.5">
                    <span className="break-all font-mono text-xs text-fg">{k.fingerprint}</span>
                    <span className="text-xs text-muted">{KEY_TYPES[k.key_type] ?? k.key_type}</span>
                  </span>
                </td>
                <td className={cn(phoneTd, "font-mono text-xs text-fg/85 max-sm:col-span-2")}>
                  <span className={cn(phoneLabel, "font-sans")}>Added </span>
                  {formatDateTime(k.created_at).slice(0, 10)}
                </td>
                <td className={cn(phoneTd, "text-xs text-fg/85 max-sm:col-span-2")}>
                  <span className={phoneLabel}>Last used </span>
                  {k.last_used_at ? (
                    <time dateTime={k.last_used_at} title={formatDateTime(k.last_used_at)}>
                      {sinceShort(k.last_used_at, now)}
                    </time>
                  ) : (
                    "Never"
                  )}
                </td>
                <td className={cn(phoneTd, "py-1.5 text-right max-sm:col-start-2 max-sm:row-start-1")}>
                  <button type="button" className={smallButton} onClick={() => setRemoving(k)}>
                    Remove…
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <span className="text-xs leading-5 text-muted">
        Adding a key asks for your password: a key keeps working after you sign out. Removing one stops it at once.
      </span>
      <AddDialog open={adding} onClose={() => setAdding(false)} />
      <RemoveDialog target={removing} onClose={() => setRemoving(null)} />
    </section>
  );
}

function AddDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const queryClient = useQueryClient();
  const confirmPassword = useReauth();
  const [title, setTitle] = useState("");
  // The title follows the key's comment until the person types their own.
  const [ownTitle, setOwnTitle] = useState(false);
  const [key, setKey] = useState("");
  const add = useMutation({
    mutationFn: async () => {
      if (!(await confirmPassword("A key gets your access to code and outlives this session, so Toft asks for your password first."))) {
        throw new Error("The key needs your password to be added.");
      }
      return codeService.addSSHKey(title.trim(), key.trim());
    },
    onSuccess: (k) => {
      queryClient.invalidateQueries({ queryKey: ["ssh-keys"] });
      toast.success(`${k.title} added`);
      close();
    },
  });
  const close = () => {
    setTitle("");
    setOwnTitle(false);
    setKey("");
    add.reset();
    onClose();
  };

  return (
    <Dialog open={open} onOpenChange={(o) => !o && !add.isPending && close()}>
      <DialogContent className={dialogContent}>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            add.mutate();
          }}
        >
          <DialogHeader className={dialogHeader}>
            <DialogTitle className={dialogTitle}>Add an SSH key</DialogTitle>
            <DialogDescription className={dialogText}>
              Git over SSH signs in with it as you. Make one with{" "}
              <span className="font-mono text-xs text-fg">ssh-keygen -t ed25519</span> if you have none.
            </DialogDescription>
          </DialogHeader>
          <div className="mx-5 mt-4 flex flex-col gap-4">
            <div className="flex flex-col gap-1.5">
              <label htmlFor="ssh-key" className={fieldLabel}>
                Public key
              </label>
              <textarea
                id="ssh-key"
                required
                rows={4}
                value={key}
                onChange={(e) => {
                  setKey(e.target.value);
                  // A .pub line ends with its comment, usually user@host.
                  if (!ownTitle) setTitle((e.target.value.trim().split(/\s+/)[2] ?? "").slice(0, 100));
                }}
                spellCheck={false}
                aria-describedby="ssh-key-help"
                placeholder="ssh-ed25519 AAAAC3Nza… you@laptop"
                className={cn(fieldInput, "h-auto break-all py-2 font-mono text-xs leading-5")}
              />
              <span id="ssh-key-help" className={help}>
                The one line of a .pub file, such as ~/.ssh/id_ed25519.pub. Never the private key.
              </span>
            </div>
            <div className="flex flex-col gap-1.5">
              <label htmlFor="ssh-title" className={fieldLabel}>
                Title
              </label>
              <input
                id="ssh-title"
                required
                maxLength={100}
                value={title}
                onChange={(e) => {
                  setTitle(e.target.value);
                  setOwnTitle(true);
                }}
                aria-describedby="ssh-title-help"
                className={fieldInput}
              />
              <span id="ssh-title-help" className={help}>
                Where the key lives, such as the laptop&apos;s name.
              </span>
            </div>
          </div>
          {add.error && (
            <p role="alert" className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">
              {errorText(add.error)}
            </p>
          )}
          <div className={dialogFooter}>
            <button type="button" className={cancelButton} disabled={add.isPending} onClick={close}>
              Cancel
            </button>
            <button type="submit" className={primaryButton} disabled={add.isPending || !key.trim() || !title.trim()}>
              {add.isPending ? "Adding…" : "Add key"}
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function RemoveDialog({ target, onClose }: { target: SSHKey | null; onClose: () => void }) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: (id: string) => codeService.deleteSSHKey(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["ssh-keys"] });
      toast.success(`${target?.title} removed`);
      close();
    },
  });
  const close = () => {
    remove.reset();
    onClose();
  };

  return (
    <Dialog open={target !== null} onOpenChange={(o) => !o && !remove.isPending && close()}>
      <DialogContent role="alertdialog" className={dialogContent} showClose={false}>
        <DialogHeader className={dialogHeader}>
          <DialogTitle className={dialogTitle}>Remove {target?.title}?</DialogTitle>
          <DialogDescription className={dialogText}>
            Git over SSH with <span className="break-all font-mono text-xs text-fg">{target?.fingerprint}</span> stops
            working at once. Copies already cloned keep their files; they can&apos;t fetch or push with this key.
          </DialogDescription>
        </DialogHeader>
        {remove.error && (
          <p role="alert" className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">
            {errorText(remove.error)}
          </p>
        )}
        <div className={dialogFooter}>
          <button type="button" className={cancelButton} disabled={remove.isPending} onClick={close}>
            Cancel
          </button>
          <button
            type="button"
            className={dangerButton}
            disabled={remove.isPending}
            onClick={() => target && remove.mutate(target.id)}
          >
            {remove.isPending ? "Removing…" : "Remove key"}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
