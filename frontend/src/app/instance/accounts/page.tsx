"use client";

import { useState } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Check, Copy, Lock, Plus, Search } from "lucide-react";
import { useAuth, useIsOperator } from "@/context/AuthContext";
import {
  instanceService,
  type PasswordReset,
  type InstanceOperator,
  type InstanceUser,
  type InstanceUserUpdate,
} from "@/lib/api/instance.service";
import { cn, formatDateTime } from "@/lib/utils";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  CardHead,
  cancelButton,
  card,
  dangerButton,
  dialogContent,
  dialogHeader,
  dialogText,
  dialogTitle,
  fieldInput,
  fieldLabel,
  help,
  primaryButton,
  smallButton,
  td,
  th,
} from "@/components/settings/settings-frame";
import { InstanceFrame } from "@/components/instance/instance-frame";
import {
  changeError,
  deletedNote,
  doneText,
  filterAccounts,
  lastSignedIn,
  operatorHow,
  resetNote,
  resetURL,
  statusNote,
} from "./accounts-view";
import { LoadingRow, Person } from "../_components/people";
import { fullPageLoad } from "@/lib/full-page-load";

/** A change waiting for the operator's password. */
type Change =
  | { kind: "grant" }
  | { kind: "revoke"; op: InstanceOperator }
  | { kind: "deactivate" | "reactivate" | "reset" | "delete"; user: InstanceUser };

const USERS = ["instance", "users"];
const OPERATORS = ["instance", "operators"];
const AUDIT = ["instance", "audit"];

/** Instance → Accounts (board ReworkInstanceAccounts, D-139): the operators,
 *  every account on the instance, and the changes an operator makes to them,
 *  each with the operator's password; Sign-up and what operators can do
 *  beside them. Operators only. */
export default function AccountsPage() {
  const { user, isLoading: authLoading } = useAuth();
  const isOperator = useIsOperator();
  const [now] = useState(() => Date.now());
  const [q, setQ] = useState("");
  const [change, setChange] = useState<Change | null>(null);
  const [done, setDone] = useState("");
  const users = useQuery({ queryKey: USERS, queryFn: instanceService.listUsers, enabled: isOperator, retry: false });
  const operators = useQuery({
    queryKey: OPERATORS,
    queryFn: instanceService.listOperators,
    enabled: isOperator,
    retry: false,
  });

  const names = new Map((users.data?.users ?? []).map((u) => [u.id, u.name]));
  const grantedAt = new Map((operators.data ?? []).map((o) => [o.user_id, o.granted_at]));
  const activeOperators = (operators.data ?? []).filter((o) => o.is_active);
  const shown = filterAccounts(users.data?.users ?? [], q);
  const error = users.error ?? operators.error;
  const you = (id: string) => id === user?.id;

  return (
    <InstanceFrame title="Accounts" gap="gap-6">
      {authLoading ? null : !isOperator ? (
        <div className={cn(card, "p-4 text-[13px] text-fg/70")}>
          Only instance operators see the accounts and operators of this instance.
        </div>
      ) : error ? (
        <div
          role="alert"
          className="flex flex-wrap items-center justify-between gap-3 rounded-[10px] border border-danger/35 bg-surface px-4 py-3.5"
        >
          <span className="flex items-center gap-2.5 text-[13px] text-status-failed-fg">
            <AlertTriangle className="h-4 w-4 flex-none" aria-hidden="true" />
            Could not load the accounts: {error.message}. Nothing was changed.
          </span>
          <button
            type="button"
            className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")}
            onClick={() => {
              users.refetch();
              operators.refetch();
            }}
          >
            Retry
          </button>
        </div>
      ) : (
        <div className="grid items-start gap-6 min-[1280px]:grid-cols-[minmax(0,1040px)_minmax(320px,1fr)]">
          <div className="flex min-w-0 flex-col gap-5">
            <section aria-labelledby="ops-h" className={card}>
              <CardHead
                id="ops-h"
                title="Operators"
                sum={`${operators.data?.length ?? "–"} · there is always at least one`}
              >
                <button
                  type="button"
                  className={cn(smallButton, "h-8 px-2.5 text-[13px] rounded-lg")}
                  onClick={() => setChange({ kind: "grant" })}
                >
                  <Plus className="h-3.5 w-3.5" aria-hidden="true" />
                  Grant operator…
                </button>
              </CardHead>
              <table className="w-full border-collapse text-[13px] max-sm:block">
                <thead className="max-sm:hidden">
                  <tr>
                    <th scope="col" className={th}>Operator</th>
                    <th scope="col" className={th}>How</th>
                    <th scope="col" className={th}><span className="sr-only">Actions</span></th>
                  </tr>
                </thead>
                <tbody className="max-sm:block">
                  {operators.isLoading ? (
                    <LoadingRow cols={3} />
                  ) : (
                    (operators.data ?? []).map((o, i) => {
                      const last = o.is_active && activeOperators.length === 1;
                      return (
                        <tr key={o.user_id} className={cn(phoneRow, i > 0 && "max-sm:border-t max-sm:border-subtle")}>
                          <td className={cn(cell, i === 0 && "border-t-0", "max-sm:col-span-2 max-sm:row-start-1")}>
                            <Person name={o.name} email={o.email} you={you(o.user_id)} muted={!o.is_active} />
                          </td>
                          <td className={cn(cell, i === 0 && "border-t-0", "text-muted max-sm:col-start-1 max-sm:row-start-2")}>
                            {operatorHow(o, names)} · <span className="whitespace-nowrap">{formatDateTime(o.granted_at).slice(0, 10)}</span>
                            {!o.is_active && <> · deactivated</>}
                          </td>
                          <td
                            className={cn(
                              cell,
                              i === 0 && "border-t-0",
                              "whitespace-nowrap py-1.5 text-right max-sm:col-start-2 max-sm:row-start-2 max-sm:self-center",
                            )}
                          >
                            {last ? (
                              <span className="text-xs text-muted">The last active operator</span>
                            ) : (
                              <button
                                type="button"
                                className={smallButton}
                                aria-label={`Revoke ${o.name} as operator`}
                                onClick={() => setChange({ kind: "revoke", op: o })}
                              >
                                Revoke
                              </button>
                            )}
                          </td>
                        </tr>
                      );
                    })
                  )}
                </tbody>
              </table>
            </section>

            <section aria-labelledby="acc-h" className={card}>
              <CardHead
                id="acc-h"
                title="Accounts"
                sum={`${users.data?.users.length ?? "–"} across all workspaces`}
              >
                <div className="relative flex flex-[0_1_240px] items-center max-sm:flex-auto">
                  <label htmlFor="acc-q" className="sr-only">
                    Find an account by name or email
                  </label>
                  <Search className="pointer-events-none absolute left-2.5 h-3.5 w-3.5 text-muted" aria-hidden="true" />
                  <input
                    id="acc-q"
                    type="search"
                    placeholder="Name or email"
                    value={q}
                    onChange={(e) => setQ(e.target.value)}
                    className={cn(fieldInput, "w-full pl-[30px]")}
                  />
                </div>
              </CardHead>
              {done && (
                <p role="status" className="flex items-center gap-2 border-b border-subtle px-4 py-2.5 text-[13px] text-fg/85">
                  <Check className="h-3.5 w-3.5 flex-none text-status-running" strokeWidth={2.2} aria-hidden="true" />
                  {done}
                </p>
              )}
              <table className="w-full border-collapse text-[13px] max-sm:block">
                <thead className="max-sm:hidden">
                  <tr>
                    <th scope="col" className={cn(th, "w-[40%]")}>Account</th>
                    <th scope="col" className={th}>Status</th>
                    <th scope="col" className={th}><span className="sr-only">Actions</span></th>
                  </tr>
                </thead>
                <tbody className="max-sm:block">
                  {users.isLoading ? (
                    <LoadingRow cols={3} />
                  ) : shown.length === 0 ? (
                    <tr className="max-sm:block">
                      <td colSpan={3} className={cn(td, "border-t-0 py-3.5 text-muted max-sm:block")}>
                        No account matches &ldquo;{q.trim()}&rdquo;.
                      </td>
                    </tr>
                  ) : (
                    shown.map((u, i) => {
                      const sub = statusNote(u, grantedAt.get(u.id), now);
                      return (
                        <tr key={u.id} className={cn(phoneRow, i > 0 && "max-sm:border-t max-sm:border-subtle")}>
                          <td className={cn(cell, i === 0 && "border-t-0", "max-sm:col-span-2 max-sm:row-start-1")}>
                            <Person name={u.name} email={u.email} you={you(u.id)} muted={!u.is_active} />
                          </td>
                          <td className={cn(cell, i === 0 && "border-t-0", "max-sm:col-start-1 max-sm:row-start-2")}>
                            <span
                              className={cn(
                                "inline-flex items-center gap-1.5 font-medium",
                                u.is_active ? "text-status-running" : "text-muted",
                              )}
                            >
                              <span
                                aria-hidden="true"
                                className={cn("h-1.5 w-1.5 rounded-full", u.is_active ? "bg-status-running" : "bg-muted")}
                              />
                              {u.is_active ? "Active" : "Deactivated"}
                            </span>
                            {sub && <span className={cn(help, "block [overflow-wrap:anywhere]")}>{sub}</span>}
                            <span className={cn(help, "block")}>{lastSignedIn(u.last_login_at, now)}</span>
                          </td>
                          <td
                            className={cn(
                              cell,
                              i === 0 && "border-t-0",
                              "whitespace-nowrap py-1.5 text-right max-sm:col-start-2 max-sm:row-start-2 max-sm:self-center",
                            )}
                          >
                            {you(u.id) ? (
                              <span className="text-xs text-muted">You can&rsquo;t deactivate yourself</span>
                            ) : (
                              <span className="inline-flex flex-col items-end gap-1">
                                <span className="flex flex-wrap justify-end gap-1.5 max-sm:flex-col max-sm:items-end">
                                  {u.is_active && (
                                    <button
                                      type="button"
                                      className={smallButton}
                                      aria-label={`${u.reset_expires_at ? "New reset link" : "Reset password"} for ${u.name}`}
                                      onClick={() => setChange({ kind: "reset", user: u })}
                                    >
                                      {u.reset_expires_at ? "New reset link…" : "Reset password…"}
                                    </button>
                                  )}
                                  <button
                                    type="button"
                                    className={smallButton}
                                    aria-label={`${u.is_active ? "Deactivate" : "Reactivate"} ${u.name}`}
                                    onClick={() => setChange({ kind: u.is_active ? "deactivate" : "reactivate", user: u })}
                                  >
                                    {u.is_active ? "Deactivate" : "Reactivate"}
                                  </button>
                                  {!u.is_active && (
                                    <button
                                      type="button"
                                      className={cn(smallButton, "text-status-failed-fg")}
                                      aria-label={`Delete ${u.name}`}
                                      onClick={() => setChange({ kind: "delete", user: u })}
                                    >
                                      Delete…
                                    </button>
                                  )}
                                </span>
                                {u.is_active && u.reset_expires_at && (
                                  <span className="text-xs text-muted">{resetNote(u.reset_expires_at)}</span>
                                )}
                              </span>
                            )}
                          </td>
                        </tr>
                      );
                    })
                  )}
                </tbody>
              </table>
              <p className="border-t border-subtle px-4 py-2.5 text-[13px] leading-5 text-muted">
                {deletedNote(users.data?.deleted ?? 0)}{users.data?.deleted ? " " : ""}
                Deactivating keeps the account, its workspaces and what they run, and revokes its pending invites.
                Workspace roles are set in each workspace&rsquo;s Settings.
              </p>
            </section>
          </div>

          <aside className="flex min-w-0 flex-col gap-5">
            <section aria-labelledby="reg-h" className={card}>
              <CardHead id="reg-h" title="Sign-up">
                <span className="inline-flex h-[22px] items-center gap-1.5 rounded-full border border-subtle px-2 text-xs font-medium text-fg/85">
                  <Lock className="h-3 w-3" strokeWidth={2} aria-hidden="true" />
                  Invite only
                </span>
              </CardHead>
              <dl className="flex flex-col text-[13px]">
                {[
                  ["New accounts", <>Only from a workspace&rsquo;s invite link</>],
                  ["New workspaces", <>Only operators create them</>],
                  [
                    "Every operator lost",
                    <>
                      Set <span className="font-mono text-xs [overflow-wrap:anywhere]">TOFT_OPERATOR_EMAILS</span> and
                      restart the panel
                    </>,
                  ],
                ].map(([dt, dd], i) => (
                  <div
                    key={i}
                    className={cn(
                      "grid grid-cols-[120px_minmax(0,1fr)] gap-3 px-4 py-2.5 max-sm:grid-cols-1 max-sm:gap-0.5",
                      i > 0 && "border-t border-subtle",
                    )}
                  >
                    <dt className="text-muted">{dt}</dt>
                    <dd className="min-w-0 text-fg">{dd}</dd>
                  </div>
                ))}
              </dl>
            </section>
            <section aria-labelledby="can-h" className={card}>
              <CardHead id="can-h" title="What operators can do" />
              <div className="flex flex-col gap-3 px-4 py-3">
                <ul className="list-disc pl-[18px] text-[13px] leading-5 text-fg/85 [&>li+li]:mt-1">
                  <li>Create workspaces, and manage every account and operator</li>
                  <li>Upgrade, reboot and clean the panel&rsquo;s server, and open shells on servers&rsquo; hosts</li>
                  <li>See the deploy policy report for every workspace and grant exceptions</li>
                  <li>Replace SSH host keys</li>
                </ul>
                <p className={help}>
                  Every change here applies at once and goes to the{" "}
                  <Link href="/instance/audit-log" className="inline-link">
                    audit log
                  </Link>
                  .
                </p>
              </div>
            </section>
          </aside>
        </div>
      )}

      <ChangeDialog
        change={change}
        selfId={user?.id ?? ""}
        others={activeOperators.filter((o) => change?.kind === "revoke" && o.user_id !== change.op.user_id)}
        onClose={() => setChange(null)}
        onDone={(text) => setDone(text)}
      />
    </InstanceFrame>
  );
}

// A table row that is a two-column card on a phone (the board's .lt below
// 640px), as Settings' members.
const phoneRow =
  "max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-1 max-sm:px-4 max-sm:py-3";
const cell = cn(td, "align-middle max-sm:block max-sm:border-0 max-sm:p-0");

/** The board's dialogs: grant, revoke, deactivate, reactivate (which the
 *  board draws none for, D-50) and the reset link (board InstanceReset,
 *  D-56). Each asks for the password. */
function ChangeDialog({
  change,
  selfId,
  others,
  onClose,
  onDone,
}: {
  change: Change | null;
  selfId: string;
  /** The other active operators, for a revoke's text. */
  others: InstanceOperator[];
  onClose: () => void;
  onDone: (text: string) => void;
}) {
  const queryClient = useQueryClient();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [reset, setReset] = useState<PasswordReset | null>(null);
  const [copied, setCopied] = useState(false);
  const mutation = useMutation({
    mutationFn: async (c: Change): Promise<InstanceUserUpdate | PasswordReset | null> => {
      switch (c.kind) {
        case "grant":
          await instanceService.grantOperator(email.trim(), password);
          return null;
        case "revoke":
          await instanceService.revokeOperator(c.op.user_id, password);
          return null;
        case "reset":
          return instanceService.issuePasswordReset(c.user.id, password);
        case "delete":
          await instanceService.deleteUser(c.user.id, password);
          return null;
        default:
          return instanceService.setUserActive(c.user.id, c.kind === "reactivate", password);
      }
    },
    onSuccess: (result, c) => {
      if (c.kind === "reset") {
        // The dialog stays open and turns into the link to send.
        setReset(result as PasswordReset);
        queryClient.invalidateQueries({ queryKey: USERS });
        queryClient.invalidateQueries({ queryKey: AUDIT });
        return;
      }
      if (c.kind === "revoke" && c.op.user_id === selfId) {
        // Operator rights end with this page: a full load, so the shell
        // reads them again and nothing of the instance pages stays cached.
        fullPageLoad("/settings/account");
        return;
      }
      queryClient.invalidateQueries({ queryKey: USERS });
      queryClient.invalidateQueries({ queryKey: OPERATORS });
      queryClient.invalidateQueries({ queryKey: AUDIT });
      if (result && "invites_revoked" in result) onDone(doneText(result));
      if (c.kind === "delete") onDone(`${c.user.name} was deleted; ${c.user.email} can be used for a new account.`);
      close();
    },
  });
  const close = () => {
    onClose();
    setEmail("");
    setPassword("");
    setReset(null);
    setCopied(false);
    mutation.reset();
  };
  const copy = (url: string) =>
    navigator.clipboard.writeText(url).then(
      () => setCopied(true),
      () => setCopied(false),
    );

  const destructive = change?.kind === "revoke" || change?.kind === "deactivate" || change?.kind === "delete";
  let title = "";
  let text: React.ReactNode = null;
  let action = "";
  switch (change?.kind) {
    case "grant":
      title = "Grant operator";
      text =
        "An operator can deactivate any account, grant deploy policy exceptions to any service and open a shell on any server’s host, in every workspace on this instance. Grant it only to people who run the instance.";
      action = "Grant operator";
      break;
    case "revoke": {
      const self = change.op.user_id === selfId;
      const stays =
        others.length === 1 ? `${others[0].name} stays an operator.` : `${others.length} other operators stay.`;
      title = `Revoke operator for ${change.op.name}?`;
      text = self
        ? `You keep your account and workspaces and lose operator rights at once, including this page. ${stays}`
        : `${change.op.name} keeps the account and every workspace role, and loses operator rights on their next request. You stay an operator.`;
      action = "Revoke operator";
      break;
    }
    case "deactivate":
      title = `Deactivate ${change.user.name}?`;
      text = (
        <>
          <span className="font-mono text-xs text-fg">{change.user.email}</span> can&rsquo;t sign in from now on.
        </>
      );
      action = `Deactivate ${change.user.name}`;
      break;
    case "reset":
      title = `Reset password for ${change.user.name}`;
      text = (
        <>
          Toft makes a one-time link; you send it. {change.user.name} opens it, sets a new password and is signed in,
          and every other session of theirs ends. Until then their current password keeps working.
        </>
      );
      action = "Make reset link";
      break;
    case "delete":
      title = `Delete ${change.user.name} for good?`;
      text = (
        <>
          <span className="font-mono text-xs text-fg">{change.user.email}</span> leaves every workspace, its API keys
          stop working, and the email can be used for a new account. Projects, servers and past actions keep
          &ldquo;Deleted account&rdquo; where the name was. This can&rsquo;t be undone.
        </>
      );
      action = `Delete ${change.user.name} for good`;
      break;
    case "reactivate":
      title = `Reactivate ${change.user.name}?`;
      text = (
        <>
          <span className="font-mono text-xs text-fg">{change.user.email}</span> can sign in again, with the
          workspace roles it had. Invites revoked when it was deactivated stay revoked.
        </>
      );
      action = `Reactivate ${change.user.name}`;
      break;
  }

  return (
    <Dialog open={change !== null} onOpenChange={(o) => !o && !mutation.isPending && close()}>
      <DialogContent
        role={destructive ? "alertdialog" : "dialog"}
        showClose={false}
        className={cn(dialogContent, change?.kind === "revoke" && "max-w-[480px]")}
      >
        {reset && change?.kind === "reset" ? (
          <>
            <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
              <DialogTitle className={dialogTitle}>Reset link for {change.user.name}</DialogTitle>
              <DialogDescription className={dialogText}>
                Send it to {change.user.name} yourself; Toft sends no email. It works once, until{" "}
                <span className="font-mono text-xs text-fg">{formatDateTime(reset.expires_at)}</span>. Making another
                link replaces this one.
              </DialogDescription>
            </DialogHeader>
            <div className="mx-5 mt-4 flex flex-col gap-1.5">
              <label htmlFor="reset-link" className={fieldLabel}>
                Reset link
              </label>
              <div className="flex gap-2">
                <input
                  id="reset-link"
                  readOnly
                  value={resetURL(reset.token)}
                  onFocus={(e) => e.target.select()}
                  className={cn(fieldInput, "h-[34px] min-w-0 flex-1 font-mono text-xs")}
                />
                <button type="button" className={cancelButton} onClick={() => copy(resetURL(reset.token))}>
                  {copied ? (
                    <Check className="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />
                  ) : (
                    <Copy className="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />
                  )}
                  {copied ? "Copied" : "Copy"}
                </button>
              </div>
              <span role="status" className={help}>
                {copied ? "Link copied." : "Anyone with the link can set the password, so send it directly to them."}
              </span>
            </div>
            <div className="flex justify-end px-5 pb-5 pt-[18px]">
              <button type="button" className={primaryButton} onClick={close}>
                Done
              </button>
            </div>
          </>
        ) : (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (change && password && (change.kind !== "grant" || email.trim())) mutation.mutate(change);
          }}
        >
          <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
            <DialogTitle className={dialogTitle}>{title}</DialogTitle>
            <DialogDescription className={dialogText}>{text}</DialogDescription>
          </DialogHeader>
          {change?.kind === "deactivate" && (
            <ul className="mx-5 mt-3 flex list-disc flex-col gap-1.5 pl-[18px] text-[13px] leading-[19px] text-fg/85">
              <li>
                {change.user.name}&rsquo;s sessions and API keys are refused at once. Open terminals and log streams
                close within 15 seconds.
              </li>
              <li>
                Pending invites {change.user.name} created, or can read as a workspace owner, are revoked. Their owners
                send new links.
              </li>
              <li>
                {change.user.name}&rsquo;s workspaces, services and deploys keep running. Reactivating restores sign-in,
                not the revoked invites.
              </li>
            </ul>
          )}
          {change?.kind === "grant" && (
            <div className="mx-5 mt-4 flex flex-col gap-1.5">
              <label htmlFor="go-email" className={fieldLabel}>
                Account email
              </label>
              <input
                id="go-email"
                type="email"
                autoComplete="off"
                required
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                aria-describedby="go-email-help"
                className={cn(fieldInput, "h-[34px] font-mono")}
              />
              <span id="go-email-help" className={help}>
                An existing account. Operators can&rsquo;t be invited before they sign up.
              </span>
            </div>
          )}
          <div className={cn("mx-5 flex flex-col gap-1.5", change?.kind === "grant" ? "mt-3.5" : "mt-4")}>
            <label htmlFor="ch-pw" className={fieldLabel}>
              Your password
            </label>
            <input
              id="ch-pw"
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              aria-describedby="ch-pw-help"
              className={cn(fieldInput, "h-[34px]")}
            />
            <span id="ch-pw-help" className={help}>
              Asked again for every change to accounts and operators.
            </span>
          </div>
          {mutation.error && (
            <p
              role="alert"
              className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg"
            >
              {changeError(mutation.error)}
            </p>
          )}
          <div className="flex flex-wrap justify-end gap-2 px-5 pb-5 pt-[18px]">
            <button type="button" className={cancelButton} disabled={mutation.isPending} onClick={close}>
              Cancel
            </button>
            <button
              type="submit"
              className={destructive ? dangerButton : primaryButton}
              disabled={mutation.isPending || !password || (change?.kind === "grant" && !email.trim())}
            >
              {mutation.isPending ? "Saving…" : action}
            </button>
          </div>
        </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
