"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertCircle, Check, LogOut, Plus } from "lucide-react";
import { useAuth, useIsOperator } from "@/context/AuthContext";
import { ApiError } from "@/lib/api/client";
import { userService, type UserProfile } from "@/lib/api/user.service";
import { cn, formatDateTime } from "@/lib/utils";
import { PASSWORD_MIN, PASSWORD_MIN_HELP, PASSWORD_TOO_LONG, passwordTooLong } from "@/lib/password";
import { CreateWorkspaceDialog } from "@/components/create-workspace-dialog";
import { SSHKeys } from "./ssh-keys";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  SettingsFrame,
  cancelButton,
  card,
  dialogContent,
  dialogFooter,
  dialogHeader,
  dialogText,
  dialogTitle,
  fieldInput,
  fieldLabel,
  help,
  primaryButton,
  roleChip,
  phoneNote,
  phoneRow,
  phoneTd,
  smallButton,
  th,
} from "@/components/settings/settings-frame";

const h2 = "text-[15px] font-semibold leading-[22px] text-fg";
const footer =
  "flex flex-wrap items-center justify-end gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5";
const saveButton = cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg");

/** When this browser's session token was issued and when it expires. */
function sessionOf(
  token: string | null,
): { since: string; until: string } | null {
  try {
    const p = JSON.parse(atob((token ?? "").split(".")[1]));
    return {
      since: new Date(p.iat * 1000).toISOString(),
      until: new Date(p.exp * 1000).toISOString(),
    };
  } catch {
    return null;
  }
}

/** Settings → Account (board: Account): your profile, password, workspaces,
 *  SSH keys and sessions. */
export default function AccountSettingsPage() {
  const { user } = useAuth();
  const isOperator = useIsOperator();
  const [creating, setCreating] = useState(false);
  const profile = useQuery({
    queryKey: ["me"],
    queryFn: () => userService.getMe(),
  });

  return (
    <SettingsFrame title="Account" crumb={user?.name || user?.email}>
      <div className="flex flex-wrap items-stretch gap-6">
        <section
          aria-labelledby="prof-h"
          className="flex min-w-0 flex-[1_1_440px] flex-col gap-3"
        >
          <h2 id="prof-h" className={h2}>
            Profile
          </h2>
          {profile.data ? (
            <ProfileForm
              key={profile.data.name}
              profile={profile.data}
              isOperator={isOperator}
            />
          ) : profile.error ? (
            <p
              role="alert"
              className={cn(
                card,
                "flex-1 p-4 text-[13px] text-status-failed-fg",
              )}
            >
              Could not load your profile:{" "}
              {profile.error instanceof Error
                ? profile.error.message
                : "unknown error"}
            </p>
          ) : (
            <div
              className={cn(card, "h-[400px] motion-safe:animate-pulse")}
              aria-label="Loading your profile"
            />
          )}
        </section>
        <section
          aria-labelledby="pw-h"
          className="flex min-w-0 flex-[1_1_440px] flex-col gap-3"
        >
          <h2 id="pw-h" className={h2}>
            Password
          </h2>
          <PasswordForm />
        </section>
      </div>

      <section aria-labelledby="ws-h" className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2">
          <div className="flex flex-col gap-0.5">
            <h2 id="ws-h" className={h2}>
              Workspaces
            </h2>
            <span className="text-xs leading-5 text-muted">
              The open workspace is saved to your account: switching changes it
              in every browser you’re signed in on, and for API keys that aren’t
              tied to a project.
            </span>
          </div>
          {isOperator && (
            <button
              type="button"
              className={cn(smallButton, "h-8 bg-surface text-[13px] rounded-lg")}
              onClick={() => setCreating(true)}
            >
              <Plus className="h-3.5 w-3.5" aria-hidden="true" />
              Create workspace
            </button>
          )}
        </div>
        <Workspaces />
        <span className="text-xs leading-5 text-muted">
          {isOperator
            ? "Only instance operators create workspaces, here or from the workspace menu. Other people join one from an invite link."
            : "You join a workspace from an invite link. Instance operators create workspaces."}
        </span>
      </section>

      <SSHKeys />

      <Sessions />

      {isOperator && (
        <CreateWorkspaceDialog open={creating} onOpenChange={setCreating} />
      )}
    </SettingsFrame>
  );
}

function ProfileForm({
  profile,
  isOperator,
}: {
  profile: UserProfile;
  isOperator: boolean;
}) {
  const queryClient = useQueryClient();
  const [name, setName] = useState(profile.name);
  const changed = name.trim() !== profile.name;
  const valid = name.trim().length >= 2;
  const save = useMutation({
    mutationFn: () => userService.updateProfile(name.trim()),
    onSuccess: (updated) => {
      queryClient.setQueryData(["me"], updated);
      toast.success("Name saved");
    },
  });
  const row =
    "flex flex-wrap gap-x-3 gap-y-0.5 border-t border-subtle px-3 py-[9px] first:border-t-0";
  const dt = "w-[120px] flex-none text-muted";

  return (
    <form
      className={cn(card, "flex flex-1 flex-col")}
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div className="flex flex-1 flex-col gap-4 p-4">
        <div className="flex flex-col gap-1.5">
          <label htmlFor="acc-name" className={fieldLabel}>
            Name
          </label>
          <input
            id="acc-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            autoComplete="name"
            maxLength={100}
            aria-describedby="acc-name-help"
            className={fieldInput}
          />
          <span id="acc-name-help" className={help}>
            At least 2 characters. Members of your workspaces see it next to
            what you do.
          </span>
        </div>
        <dl className="flex flex-col rounded-lg border border-subtle text-[13px] leading-[19px]">
          <div className={row}>
            <dt className={dt}>Email</dt>
            <dd className="flex flex-[1_1_220px] flex-col">
              <span className="font-mono text-xs leading-[19px] text-fg">{profile.email}</span>
              <span className="text-xs leading-[19px] text-muted">
                Used to sign in. It can’t be changed.
              </span>
            </dd>
          </div>
          <div className={row}>
            <dt className={dt}>Account type</dt>
            <dd className="flex flex-[1_1_220px] flex-col">
              <span className="text-fg">
                {isOperator ? "Instance operator" : "Member"}
              </span>
              <span className="text-xs leading-[19px] text-muted">
                {isOperator
                  ? "You can create workspaces, grant deploy-policy exceptions and open shells on servers."
                  : "You belong to workspaces through invites. Instance operators run this install."}
              </span>
            </dd>
          </div>
          <div className={row}>
            <dt className={dt}>Created</dt>
            <dd className="font-mono text-xs leading-[19px] text-fg">
              {formatDateTime(profile.created_at).slice(0, 10)}
            </dd>
          </div>
          <div className={row}>
            <dt className={dt}>Last sign-in</dt>
            <dd className="font-mono text-xs leading-[19px] text-fg">
              {profile.last_login_at
                ? formatDateTime(profile.last_login_at)
                : "–"}
            </dd>
          </div>
        </dl>
        {save.error && (
          <p role="alert" className="text-xs text-status-failed-fg">
            {save.error instanceof Error
              ? save.error.message
              : "The name was not saved."}
          </p>
        )}
      </div>
      <div className={footer}>
        <span
          aria-live="polite"
          className={cn(
            "flex-1 text-[13px]",
            !changed
              ? "text-muted"
              : valid
                ? "text-status-pending-fg"
                : "text-status-failed-fg",
          )}
        >
          {!changed
            ? "No unsaved changes"
            : valid
              ? "1 unsaved change"
              : "A name needs at least 2 characters."}
        </span>
        <button
          type="submit"
          className={saveButton}
          disabled={!changed || !valid || save.isPending}
        >
          {save.isPending ? "Saving…" : "Save name"}
        </button>
      </div>
    </form>
  );
}

function PasswordForm() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [repeat, setRepeat] = useState("");
  const { sessionRenewed } = useAuth();
  const change = useMutation({
    mutationFn: () => userService.updatePassword(current, next),
    onSuccess: (res) => {
      // The change ended every session of the account, this one's too;
      // keep the token the API answered with.
      if (res?.token) sessionRenewed(res.token);
      toast.success("Password changed. Your other browsers were signed out.");
      setCurrent("");
      setNext("");
      setRepeat("");
    },
  });
  const wrongCurrent =
    change.error instanceof ApiError && change.error.status === 401;
  const short = next !== "" && next.length < PASSWORD_MIN;
  const long = passwordTooLong(next);
  const mismatch = repeat !== "" && repeat !== next;
  const ready = current !== "" && next.length >= PASSWORD_MIN && !long && repeat === next;
  const red = "border-status-failed";

  return (
    <form
      className={cn(card, "flex flex-1 flex-col")}
      onSubmit={(e) => {
        e.preventDefault();
        change.mutate();
      }}
    >
      <div className="flex flex-1 flex-col gap-3.5 p-4">
        <div className="flex flex-col gap-1.5">
          <label htmlFor="pw-current" className={fieldLabel}>
            Current password
          </label>
          <input
            id="pw-current"
            type="password"
            autoComplete="current-password"
            value={current}
            onChange={(e) => {
              setCurrent(e.target.value);
              change.reset();
            }}
            aria-invalid={wrongCurrent}
            aria-describedby={wrongCurrent ? "pw-current-err" : undefined}
            className={cn(fieldInput, wrongCurrent && red)}
          />
          {wrongCurrent && (
            <span
              id="pw-current-err"
              className="flex items-center gap-1.5 text-xs text-status-failed-fg"
            >
              <AlertCircle className="h-[13px] w-[13px]" aria-hidden="true" />
              The current password is wrong. Nothing was changed.
            </span>
          )}
        </div>
        <div className="flex flex-wrap gap-3.5">
          <div className="flex min-w-0 flex-[1_1_200px] flex-col gap-1.5">
            <label htmlFor="pw-new" className={fieldLabel}>
              New password
            </label>
            <input
              id="pw-new"
              type="password"
              autoComplete="new-password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
              aria-invalid={short || long}
              aria-describedby="pw-new-help"
              className={cn(fieldInput, (short || long) && red)}
            />
            <span
              id="pw-new-help"
              className={cn(
                "text-xs",
                short || long ? "text-status-failed-fg" : "text-muted",
              )}
            >
              {long ? PASSWORD_TOO_LONG : PASSWORD_MIN_HELP}
            </span>
          </div>
          <div className="flex min-w-0 flex-[1_1_200px] flex-col gap-1.5">
            <label htmlFor="pw-confirm" className={fieldLabel}>
              Repeat new password
            </label>
            <input
              id="pw-confirm"
              type="password"
              autoComplete="new-password"
              value={repeat}
              onChange={(e) => setRepeat(e.target.value)}
              aria-invalid={mismatch}
              aria-describedby="pw-confirm-msg"
              className={cn(fieldInput, mismatch && red)}
            />
            <span
              id="pw-confirm-msg"
              className={cn(
                "text-xs",
                mismatch ? "text-status-failed-fg" : "text-muted",
              )}
            >
              {mismatch
                ? "Doesn’t match the new password."
                : repeat && repeat === next
                  ? "Matches."
                  : "Type it again to catch typos."}
            </span>
          </div>
        </div>
        <p className={cn(help, "leading-[18px]")}>
          Changing it signs out your other browsers. This one stays signed in.
        </p>
        {change.error && !wrongCurrent && (
          <p role="alert" className="text-xs text-status-failed-fg">
            {change.error instanceof Error
              ? change.error.message
              : "The password was not changed."}
          </p>
        )}
      </div>
      <div className={footer}>
        <span role="status" className="flex-1 text-[13px] text-muted">
          {wrongCurrent
            ? "Retype the current password and try again."
            : mismatch || short || long
              ? "Fix the highlighted field to continue."
              : ready
                ? "Ready to change."
                : "Fill in all three fields."}
        </span>
        <button
          type="submit"
          className={saveButton}
          disabled={!ready || change.isPending}
        >
          {change.isPending ? "Changing…" : "Change password"}
        </button>
      </div>
    </form>
  );
}

function Workspaces() {
  const { user, switchOrg } = useAuth();
  const switching = useMutation({
    mutationFn: (orgId: string) => switchOrg(orgId, "/settings/account"),
    onError: (err) =>
      toast.error(
        err instanceof Error ? err.message : "Failed to switch workspace",
      ),
  });
  const orgs = user?.orgs ?? [];
  return (
    <div role="group" tabIndex={0} aria-label="Your workspaces" className={cn(card, "overflow-x-auto")}>
      <table className="w-full border-collapse text-[13px] max-sm:block sm:min-w-[560px]">
        <thead className="max-sm:hidden">
          <tr>
            <th scope="col" className={th}>
              Workspace
            </th>
            <th scope="col" className={cn(th, "w-[212px]")}>
              Your role
            </th>
            <th scope="col" className={cn(th, "w-[272px]")}>
              <span className="sr-only">Status</span>
            </th>
          </tr>
        </thead>
        <tbody className="max-sm:block">
          {orgs.length === 0 && (
            <tr className="max-sm:block">
              <td colSpan={3} className={cn(phoneNote, "py-3.5 text-muted")}>
                You belong to no workspace yet. Ask an owner for an invite link.
              </td>
            </tr>
          )}
          {orgs.map((o) => {
            const open = o.org_id === user?.orgId;
            return (
              <tr
                key={o.org_id}
                className={cn(
                  phoneRow,
                  open &&
                    "bg-[color-mix(in_oklab,var(--accent)_7%,transparent)]",
                )}
              >
                <th scope="row" className={cn(phoneTd, "text-left font-medium max-sm:col-start-1 max-sm:row-start-1")}>
                  <span className="inline-flex items-center gap-2.5 text-fg">
                    <span
                      aria-hidden="true"
                      className={cn(
                        "flex h-[22px] w-[22px] items-center justify-center rounded-md border text-[11px] font-bold",
                        open
                          ? "border-accent bg-accent text-on-accent"
                          : "border-subtle bg-raised text-fg/75",
                      )}
                    >
                      {(o.name.trim()[0] ?? "?").toUpperCase()}
                    </span>
                    {o.name}
                  </span>
                </th>
                <td className={cn(phoneTd, "max-sm:col-start-1 max-sm:row-start-2")}>
                  <span className={roleChip}>{o.role}</span>
                </td>
                <td className={cn(phoneTd, "py-1.5 text-right max-sm:col-start-2 max-sm:row-span-2 max-sm:row-start-1 max-sm:self-center")}>
                  {open ? (
                    <span className="inline-flex items-center gap-1.5 text-xs font-medium text-fg">
                      <Check
                        className="h-3.5 w-3.5 text-link"
                        aria-hidden="true"
                      />
                      Open now
                    </span>
                  ) : (
                    <button
                      type="button"
                      className={smallButton}
                      disabled={switching.isPending}
                      onClick={() => switching.mutate(o.org_id)}
                    >
                      Switch to {o.name}
                    </button>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function Sessions() {
  const { token, logout, sessionRenewed } = useAuth();
  const [confirming, setConfirming] = useState(false);
  const session = sessionOf(token);
  const signOut = useMutation({
    mutationFn: () => userService.signOutOthers(),
    onSuccess: (res) => {
      if (res?.token) sessionRenewed(res.token);
      setConfirming(false);
      toast.success("Your other browsers were signed out");
    },
  });
  const mono = "font-mono text-xs text-fg";

  return (
    <section aria-labelledby="sess-h" className="flex flex-col gap-3">
      <h2 id="sess-h" className={h2}>
        Sessions
      </h2>
      <div className={cn(card, "flex flex-col")}>
        <div className="flex flex-wrap items-center gap-x-6 gap-y-3 px-4 py-3.5">
          <div className="flex min-w-0 flex-[1_1_420px] flex-col gap-0.5">
            <h3 className="text-sm font-semibold text-fg">This browser</h3>
            <p className="text-[13px] leading-[19px] text-fg/70">
              {session ? (
                <>
                  Signed in on{" "}
                  <span className={mono}>{formatDateTime(session.since)}</span>.
                  The session ends on{" "}
                  <span className={mono}>{formatDateTime(session.until)}</span>,
                  30 days after sign-in, or when you sign out.
                </>
              ) : (
                "Signed in. The session ends 30 days after sign-in, or when you sign out."
              )}
            </p>
          </div>
          <button
            type="button"
            className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")}
            onClick={() => logout()}
          >
            <LogOut className="h-3.5 w-3.5" aria-hidden="true" />
            Sign out of this browser
          </button>
        </div>
        <div className="flex flex-wrap items-center gap-x-6 gap-y-3 border-t border-subtle px-4 py-3.5">
          <div className="flex min-w-0 flex-[1_1_420px] flex-col gap-0.5">
            <h3 className="text-sm font-semibold text-fg">Other browsers</h3>
            <p className="max-w-[820px] text-[13px] leading-[19px] text-fg/70">
              Toft doesn’t list them. Each stays signed in for 30 days after it
              signed in, until you change your password or sign them out here.
              If a device is lost, sign out the other browsers, then change your
              password.
            </p>
          </div>
          <button
            type="button"
            className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")}
            onClick={() => setConfirming(true)}
          >
            Sign out other browsers…
          </button>
        </div>
      </div>

      <Dialog
        open={confirming}
        onOpenChange={(o) =>
          !o && !signOut.isPending && (setConfirming(false), signOut.reset())
        }
      >
        <DialogContent
          role="alertdialog"
          className={dialogContent}
          showClose={false}
        >
          <DialogHeader className={dialogHeader}>
            <DialogTitle className={dialogTitle}>
              Sign out your other browsers?
            </DialogTitle>
            <DialogDescription className={dialogText}>
              Every other browser and device signed in as you has to sign in
              again, on its next request. This one stays signed in. API keys
              keep working; revoke them under API keys.
            </DialogDescription>
          </DialogHeader>
          {signOut.error && (
            <p
              role="alert"
              className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg"
            >
              {signOut.error instanceof Error
                ? signOut.error.message
                : "Nothing was signed out."}
            </p>
          )}
          <div className={dialogFooter}>
            <button
              type="button"
              className={cancelButton}
              disabled={signOut.isPending}
              onClick={() => setConfirming(false)}
            >
              Cancel
            </button>
            <button
              type="button"
              className={primaryButton}
              disabled={signOut.isPending}
              onClick={() => signOut.mutate()}
            >
              {signOut.isPending ? "Signing out…" : "Sign out other browsers"}
            </button>
          </div>
        </DialogContent>
      </Dialog>
    </section>
  );
}
