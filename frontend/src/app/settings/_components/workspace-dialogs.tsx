"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Check, Copy } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  cancelButton,
  choice,
  dangerButton,
  dialogContent,
  dialogFooter,
  dialogHeader,
  dialogText,
  dialogTitle,
  fieldInput,
  fieldLabel,
  primaryButton,
} from "@/components/settings/settings-frame";
import { useAuth } from "@/context/AuthContext";
import {
  orgService,
  type AddableAccount,
  type OrgInvite,
  type OrgMember,
} from "@/lib/api/org.service";
import { formatDateTime } from "@/lib/utils";

type Role = "owner" | "developer" | "viewer";

export const ROLE_TEXT: Record<Role, string> = {
  owner:
    "Everything a developer can do, plus members, invites, alerts, storage and this page. Can delete the workspace.",
  developer:
    "Creates, configures and deploys projects and services, and adds servers.",
  viewer: "Sees projects, services, deploys and logs. Changes nothing.",
};

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
const article = (role: string) => (role === "owner" ? "an owner" : `a ${role}`);
const reason = (err: unknown, fallback: string) =>
  err instanceof Error && err.message ? err.message : fallback;

export const inviteURL = (token: string) =>
  `${typeof window === "undefined" ? "" : window.location.origin}/invite/${token}`;

function ErrorLine({ error }: { error: unknown }) {
  if (!error) return null;
  return (
    <p
      role="alert"
      className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] leading-[19px] text-status-failed-fg"
    >
      {reason(error, "Something went wrong.")}
    </p>
  );
}

// ─── Invite ──────────────────────────────────────────────────────────────────

/** Invite a member: pick a role, then the link to send. */
export function InviteDialog({
  open,
  workspace,
  onClose,
}: {
  open: boolean;
  workspace: string;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [role, setRole] = useState<Role>("developer");
  // "link": a one-time sign-up link. "email": an account that already signs
  // in to this instance (someone removed earlier, or in another workspace)
  // is added at once (D-56).
  const [mode, setMode] = useState<"link" | "email">("link");
  const [email, setEmail] = useState("");
  const [made, setMade] = useState<OrgInvite | null>(null);
  const [copied, setCopied] = useState(false);
  const create = useMutation({
    mutationFn: () => orgService.createInvite(role),
    onSuccess: (inv) => {
      setMade(inv);
      queryClient.invalidateQueries({ queryKey: ["org-invites"] });
    },
  });
  const add = useMutation({
    mutationFn: () => orgService.addMember(email.trim(), role),
    onSuccess: (m) => {
      queryClient.invalidateQueries({ queryKey: ["org-members"] });
      toast.success(`${m.user_name || m.user_email} is ${article(m.role)} of ${workspace} now`);
      close();
    },
  });
  const close = () => {
    onClose();
    setRole("developer");
    setMode("link");
    setEmail("");
    setMade(null);
    setCopied(false);
    create.reset();
    add.reset();
  };
  const byEmail = mode === "email";
  // Suggestions: the workspace's former members, and every account for an
  // instance operator (D-58). Typing an email that isn't listed still works.
  const addable = useQuery({
    queryKey: ["org-addable"],
    queryFn: orgService.listAddable,
    enabled: open && byEmail,
    retry: false,
  });
  const suggestions = suggest(addable.data ?? [], email);
  const copy = (url: string) =>
    navigator.clipboard.writeText(url).then(
      () => setCopied(true),
      () => toast.error("Couldn't copy: select the link and copy it"),
    );

  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogContent className={dialogContent}>
        {made ? (
          <>
            <DialogHeader className={dialogHeader}>
              <DialogTitle className={dialogTitle}>
                Invite link for {article(made.role)}
              </DialogTitle>
              <DialogDescription className={dialogText}>
                Send it to the person you&apos;re inviting. It works once, until{" "}
                <span className="font-mono text-xs text-fg">
                  {formatDateTime(made.expires_at)}
                </span>
                .
              </DialogDescription>
            </DialogHeader>
            <div className="mx-5 mt-4 flex flex-col gap-1.5">
              <label htmlFor="invite-link" className={fieldLabel}>
                Invite link
              </label>
              <div className="flex gap-2">
                <input
                  id="invite-link"
                  readOnly
                  value={inviteURL(made.token)}
                  onFocus={(e) => e.target.select()}
                  className={`${fieldInput} min-w-0 flex-1 font-mono text-xs`}
                />
                <button
                  type="button"
                  className={cancelButton}
                  onClick={() => copy(inviteURL(made.token))}
                >
                  {copied ? (
                    <Check className="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />
                  ) : (
                    <Copy className="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />
                  )}
                  {copied ? "Copied" : "Copy"}
                </button>
              </div>
              <span role="status" className="text-xs text-muted">
                {copied
                  ? "Link copied."
                  : "Anyone with the link can use it, so send it directly to the person."}
              </span>
            </div>
            <div className={dialogFooter}>
              <button type="button" className={primaryButton} onClick={close}>
                Done
              </button>
            </div>
          </>
        ) : (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              if (byEmail) add.mutate();
              else create.mutate();
            }}
          >
            <DialogHeader className={dialogHeader}>
              <DialogTitle className={dialogTitle}>
                Invite a member to {workspace}
              </DialogTitle>
              <DialogDescription className={dialogText}>
                {byEmail
                  ? "Someone who already signs in here, in another workspace or removed from this one earlier, is added at once. No link."
                  : "Toft makes a link; you send it. It works once, for 7 days, and it is listed under Pending invites until then."}
              </DialogDescription>
            </DialogHeader>
            <div
              role="group"
              aria-label="Who"
              className="mx-5 mt-4 grid grid-cols-2 gap-0.5 rounded-lg border border-subtle bg-ground p-0.5"
            >
              {(["link", "email"] as const).map((m) => (
                <button
                  key={m}
                  type="button"
                  aria-pressed={mode === m}
                  onClick={() => setMode(m)}
                  className={`h-8 rounded-md px-2.5 text-[13px] font-medium focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-link ${
                    mode === m ? "bg-raised text-fg" : "text-fg/70 hover:text-fg"
                  }`}
                >
                  {m === "link" ? "Someone new" : "Has an account here"}
                </button>
              ))}
            </div>
            {byEmail && (
              <div className="mx-5 mt-4 flex flex-col gap-1.5">
                <label htmlFor="add-email" className={fieldLabel}>
                  Account email
                </label>
                <input
                  id="add-email"
                  type="email"
                  autoComplete="off"
                  required
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  aria-describedby="add-email-help"
                  className={`${fieldInput} font-mono`}
                />
                {suggestions.length > 0 ? (
                  <ul
                    role="listbox"
                    aria-label="Accounts"
                    className="flex flex-col overflow-hidden rounded-lg border border-subtle bg-ground"
                  >
                    {suggestions.map((a) => (
                      <li key={a.user_id} role="option" aria-selected={a.email === email}>
                        <button
                          type="button"
                          onClick={() => setEmail(a.email)}
                          className={`flex w-full items-center justify-between gap-3 px-3 py-2 text-left text-[13px] hover:bg-raised ${
                            a.email === email ? "bg-raised" : ""
                          }`}
                        >
                          <span className="flex min-w-0 flex-col">
                            <span className="font-medium text-fg">{a.name}</span>
                            <span className="truncate font-mono text-xs text-muted">{a.email}</span>
                          </span>
                          <span className="flex-none text-xs text-muted">{addableNote(a)}</span>
                        </button>
                      </li>
                    ))}
                  </ul>
                ) : null}
                <span id="add-email-help" className="text-xs text-muted">
                  {addable.isLoading
                    ? "Looking for former members…"
                    : addable.isError && !addable.data
                      ? "Could not load former members; type the email."
                    : (addable.data?.length ?? 0) === 0
                      ? "No former members to suggest; type the email."
                      : "Former members of this workspace first. Pick one or type an email."}
                </span>
              </div>
            )}
            <fieldset className="mx-5 mt-4 flex min-w-0 flex-col gap-2">
              <legend className={`${fieldLabel} pb-2`}>Role</legend>
              {(["developer", "viewer", "owner"] as Role[]).map((r) => (
                <label key={r} className={choice(role === r)}>
                  <input
                    type="radio"
                    name="invite-role"
                    checked={role === r}
                    onChange={() => setRole(r)}
                    className="mt-0.5 h-4 w-4 flex-none accent-[var(--link)]"
                  />
                  <span className="flex min-w-0 flex-col gap-0.5">
                    <span className="text-[13px] font-medium text-fg">
                      {cap(r)}
                    </span>
                    <span className="text-[13px] leading-[19px] text-fg/70">
                      {ROLE_TEXT[r]}
                    </span>
                  </span>
                </label>
              ))}
            </fieldset>
            {role === "owner" && (
              <p
                role="alert"
                className="mx-5 mt-3 flex gap-2.5 rounded-lg border border-status-pending/35 bg-status-pending-soft px-3 py-2.5 text-[13px] leading-[19px] text-fg"
              >
                <AlertTriangle
                  className="mt-0.5 h-4 w-4 flex-none text-status-pending"
                  aria-hidden="true"
                />
                Owners can delete this workspace, remove members and change
                every setting.{" "}
                {byEmail
                  ? "Add only the person you mean."
                  : "Anyone who has this link can join as an owner, so send it only to the person you mean."}
              </p>
            )}
            <ErrorLine error={byEmail ? add.error : create.error} />
            <div className={dialogFooter}>
              <button type="button" className={cancelButton} onClick={close}>
                Cancel
              </button>
              <button
                type="submit"
                className={primaryButton}
                disabled={create.isPending || add.isPending || (byEmail && !email.trim())}
              >
                {byEmail
                  ? add.isPending
                    ? "Adding…"
                    : `Add as ${article(role)}`
                  : create.isPending
                    ? "Creating…"
                    : role === "owner"
                      ? "Create owner invite link"
                      : "Create invite link"}
              </button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

/** The suggestions that match what was typed, former members first, at most six. */
export function suggest(all: AddableAccount[], typed: string): AddableAccount[] {
  const q = typed.trim().toLowerCase();
  return all.filter((a) => !q || a.email.toLowerCase().includes(q) || a.name.toLowerCase().includes(q)).slice(0, 6);
}

/** "Was a developer here until 2026-10-16", or "On this instance". */
export function addableNote(a: Pick<AddableAccount, "former" | "former_role" | "left_at">): string {
  if (!a.former) return "On this instance";
  const role = a.former_role ? article(a.former_role) : "a member";
  return a.left_at ? `Was ${role} here until ${formatDateTime(a.left_at).slice(0, 10)}` : `Was ${role} here`;
}

// ─── Change role ─────────────────────────────────────────────────────────────

export function RoleDialog({
  member,
  onClose,
}: {
  member: OrgMember | null;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [picked, setPicked] = useState<Role | null>(null);
  const current = (member?.role ?? "viewer") as Role;
  const role = picked ?? current;
  const name = member?.user_name || member?.user_email || "";
  const save = useMutation({
    mutationFn: () => orgService.updateMemberRole(member!.user_id, role),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["org-members"] });
      toast.success(`${name} is ${article(role)} now`);
      close();
    },
  });
  const close = () => {
    onClose();
    setPicked(null);
    save.reset();
  };

  return (
    <Dialog open={member !== null} onOpenChange={(o) => !o && close()}>
      <DialogContent className={dialogContent}>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <DialogHeader className={dialogHeader}>
            <DialogTitle className={dialogTitle}>
              Change {name}&apos;s role
            </DialogTitle>
            <DialogDescription className={dialogText}>
              {name} is {article(current)} now. The new role applies to their
              next request.
            </DialogDescription>
          </DialogHeader>
          <fieldset className="mx-5 mt-4 flex min-w-0 flex-col gap-2">
            <legend className={`${fieldLabel} pb-2`}>Role</legend>
            {(["owner", "developer", "viewer"] as Role[]).map((r) => (
              <label key={r} className={choice(role === r)}>
                <input
                  type="radio"
                  name="member-role"
                  checked={role === r}
                  onChange={() => setPicked(r)}
                  className="mt-0.5 h-4 w-4 flex-none accent-[var(--link)]"
                />
                <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                  <span className="text-[13px] font-medium text-fg">
                    {cap(r)}
                  </span>
                  <span className="text-[13px] leading-[19px] text-fg/70">
                    {ROLE_TEXT[r]}
                  </span>
                </span>
                {r === current && (
                  <span className="flex-none text-xs text-muted">Current</span>
                )}
              </label>
            ))}
          </fieldset>
          <ErrorLine error={save.error} />
          <div className={dialogFooter}>
            <button type="button" className={cancelButton} onClick={close}>
              Cancel
            </button>
            <button
              type="submit"
              className={primaryButton}
              disabled={role === current || save.isPending}
            >
              {save.isPending ? "Saving…" : "Save role"}
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ─── Confirmations ───────────────────────────────────────────────────────────

interface ConfirmProps {
  open: boolean;
  title: string;
  children: React.ReactNode;
  confirm: string;
  pending: boolean;
  error: unknown;
  onConfirm: () => void;
  onClose: () => void;
}

/** A destructive yes or no: what goes, then Cancel and the named action. */
function Confirm({
  open,
  title,
  children,
  confirm,
  pending,
  error,
  onConfirm,
  onClose,
}: ConfirmProps) {
  return (
    <Dialog open={open} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent
        role="alertdialog"
        className={dialogContent}
        showClose={false}
      >
        <DialogHeader className={dialogHeader}>
          <DialogTitle className={dialogTitle}>{title}</DialogTitle>
          <DialogDescription className={dialogText}>
            {children}
          </DialogDescription>
        </DialogHeader>
        <ErrorLine error={error} />
        <div className={dialogFooter}>
          <button
            type="button"
            className={cancelButton}
            disabled={pending}
            onClick={onClose}
          >
            Cancel
          </button>
          <button
            type="button"
            className={dangerButton}
            disabled={pending}
            onClick={onConfirm}
          >
            {confirm}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

export function RemoveDialog({
  member,
  workspace,
  reach,
  onClose,
}: {
  member: OrgMember | null;
  workspace: string;
  reach: string;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const name = member?.user_name || member?.user_email || "";
  const remove = useMutation({
    mutationFn: () => orgService.removeMember(member!.user_id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["org-members"] });
      toast.success(`${name} removed from ${workspace}`);
      close();
    },
  });
  const close = () => {
    onClose();
    remove.reset();
  };
  return (
    <Confirm
      open={member !== null}
      title={`Remove ${name} from ${workspace}?`}
      confirm={remove.isPending ? "Removing…" : `Remove ${name}`}
      pending={remove.isPending}
      error={remove.error}
      onConfirm={() => remove.mutate()}
      onClose={close}
    >
      {name} loses access to {workspace} on their next request: {reach}. Project
      API keys {name} made here stop working. Their account and any other
      workspaces stay. To undo, invite them again.
    </Confirm>
  );
}

export function RevokeDialog({
  invite,
  by,
  onClose,
}: {
  invite: OrgInvite | null;
  by: string;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const revoke = useMutation({
    mutationFn: () => orgService.revokeInvite(invite!.id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["org-invites"] });
      toast.success("Invite revoked");
      close();
    },
  });
  const close = () => {
    onClose();
    revoke.reset();
  };
  return (
    <Confirm
      open={invite !== null}
      title={`Revoke this ${invite?.role ?? ""} invite?`}
      confirm={revoke.isPending ? "Revoking…" : "Revoke link"}
      pending={revoke.isPending}
      error={revoke.error}
      onConfirm={() => revoke.mutate()}
      onClose={close}
    >
      The link {by} created stops working at once. Nobody has used it yet. You
      can create a new one at any time.
    </Confirm>
  );
}

export function LeaveDialog({
  open,
  workspace,
  reach,
  lastWorkspace,
  onClose,
}: {
  open: boolean;
  workspace: string;
  reach: string;
  lastWorkspace: boolean;
  onClose: () => void;
}) {
  const { leaveOrg } = useAuth();
  const leave = useMutation({ mutationFn: leaveOrg });
  return (
    <Confirm
      open={open}
      title={`Leave ${workspace}?`}
      confirm={leave.isPending ? "Leaving…" : `Leave ${workspace}`}
      pending={leave.isPending}
      error={leave.error}
      onConfirm={() => leave.mutate()}
      onClose={() => {
        onClose();
        leave.reset();
      }}
    >
      You lose access to {workspace} at once: {reach}. Project API keys you made
      for its projects stop working. Your account
      {lastWorkspace ? "" : " and your other workspaces"} stay. To come back, an
      owner has to invite you again.
      {lastWorkspace &&
        " You belong to no other workspace, so Toft will have nothing to show you until someone invites you."}
    </Confirm>
  );
}

// ─── Delete ──────────────────────────────────────────────────────────────────

export function DeleteDialog({
  open,
  workspace,
  rows,
  onClose,
}: {
  open: boolean;
  workspace: string;
  rows: { k: string; v: string }[];
  onClose: () => void;
}) {
  const { deleteOrg } = useAuth();
  const [typed, setTyped] = useState("");
  const remove = useMutation({ mutationFn: () => deleteOrg(typed) });
  const close = () => {
    if (remove.isPending) return;
    onClose();
    setTyped("");
    remove.reset();
  };
  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogContent
        role="alertdialog"
        className={`${dialogContent} max-w-[540px]`}
        showClose={false}
      >
        <form
          onSubmit={(e) => {
            e.preventDefault();
            remove.mutate();
          }}
        >
          <DialogHeader className={`${dialogHeader} gap-1.5`}>
            <DialogTitle className={dialogTitle}>
              Delete {workspace}?
            </DialogTitle>
            <DialogDescription className={dialogText}>
              Everything below is deleted, data included. It can&apos;t be
              undone, and nothing is backed up first.
            </DialogDescription>
          </DialogHeader>
          <ul className="mx-5 mt-3.5 rounded-lg border border-subtle text-[13px] leading-[19px]">
            {rows.map((r) => (
              <li
                key={r.k}
                className="flex gap-3 border-t border-subtle px-3 py-2 first:border-t-0"
              >
                <span className="w-[132px] flex-none text-muted">{r.k}</span>
                <span className="min-w-0 text-fg/85 [overflow-wrap:anywhere]">
                  {r.v}
                </span>
              </li>
            ))}
          </ul>
          <p className="mx-5 mt-2.5 text-xs leading-[18px] text-muted">
            If a server is shared with another workspace that still runs
            services on it, the delete stops before anything is removed.
          </p>
          <div className="mx-5 mt-3.5 flex flex-col gap-1.5">
            <label htmlFor="delete-confirm" className={fieldLabel}>
              Type <span className="font-mono text-fg">{workspace}</span> to
              confirm
            </label>
            <input
              id="delete-confirm"
              autoComplete="off"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              className={`${fieldInput} font-mono`}
            />
          </div>
          <ErrorLine error={remove.error} />
          <div className="flex flex-wrap justify-end gap-2 px-5 pb-5 pt-[18px]">
            <button
              type="button"
              className={cancelButton}
              disabled={remove.isPending}
              onClick={close}
            >
              Cancel
            </button>
            <button
              type="submit"
              className={dangerButton}
              disabled={typed !== workspace || remove.isPending}
            >
              {remove.isPending ? "Deleting…" : `Delete ${workspace}`}
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
