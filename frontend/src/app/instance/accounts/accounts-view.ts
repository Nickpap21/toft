import { ApiError } from "@/lib/api/client";
import type { InstanceAuditEntry, InstanceOperator, InstanceUser, InstanceUserUpdate } from "@/lib/api/instance.service";
import { formatDateTime } from "@/lib/utils";
import { waitFor } from "@/components/shell/auth";
import { sinceShort } from "@/lib/time";
import { gb } from "@/components/servers/disk-view";
import { DOING, DONE } from "@/components/servers/updates-view";
import type { UpdateAction } from "@/lib/api/server.service";

/** "First account on this instance", "Granted by admin": why someone is an
 *  operator. names maps user IDs to names. */
export function operatorHow(op: Pick<InstanceOperator, "source" | "granted_by">, names: Map<string, string>): string {
  switch (op.source) {
    case "first_run":
      return "First account on this instance";
    case "bootstrap":
      return "Earliest account, promoted at startup";
    case "env":
      return "Listed in TOFT_OPERATOR_EMAILS";
    default: {
      const by = op.granted_by ? names.get(op.granted_by) : undefined;
      return by ? `Granted by ${by}` : "Granted by an operator";
    }
  }
}

/** "12 min ago by admin", "2026-10-14 09:12 by admin": under a deactivated
 *  account's status (D-46's dates). "" when nothing was recorded. */
export function deactivatedNote(u: Pick<InstanceUser, "deactivated_at" | "deactivated_by">, now: number): string {
  if (!u.deactivated_at) return "";
  const at = sinceShort(u.deactivated_at, now);
  return u.deactivated_by ? `${at} by ${u.deactivated_by}` : at;
}

/** Under an account's name: "Operator · since <grant date>" (an operator's
 *  date is when they became one, not the account's creation), "since <created>",
 *  or the deactivation note. grantedAt is undefined until the grants load. */
export function statusNote(
  u: Pick<InstanceUser, "is_active" | "is_operator" | "created_at" | "deactivated_at" | "deactivated_by">,
  grantedAt: string | undefined,
  now: number,
): string {
  if (!u.is_active) return deactivatedNote(u, now);
  if (u.is_operator) return grantedAt ? `Operator · since ${formatDateTime(grantedAt).slice(0, 10)}` : "Operator";
  return `since ${formatDateTime(u.created_at).slice(0, 10)}`;
}

/** The status line after a change (board: Instance, view deactivated). */
export function doneText(u: Pick<InstanceUserUpdate, "name" | "is_active" | "invites_revoked">): string {
  if (u.is_active) return `${u.name} can sign in again.`;
  const n = u.invites_revoked;
  if (n === 0) return `${u.name} was deactivated.`;
  return n === 1
    ? `${u.name} was deactivated. 1 pending invite was revoked; its workspace owner has to send a new link.`
    : `${u.name} was deactivated. ${n} pending invites were revoked; their workspace owners have to send new links.`;
}

/** Under an account's status: "Last signed in 3 days ago", relative within
 *  a day as D-46 ("3 h ago"); "Never signed in". */
export function lastSignedIn(at: string | null, now: number): string {
  if (!at) return "Never signed in";
  const days = Math.floor(Math.max(0, now - new Date(at).getTime()) / 86_400_000);
  if (days === 0) return `Last signed in ${sinceShort(at, now)}`;
  return `Last signed in ${days} ${days === 1 ? "day" : "days"} ago`;
}

/** Under a row's buttons while its reset link is live (D-57); "" otherwise. */
export function resetNote(at: string | null): string {
  return at ? `Reset link live until ${formatDateTime(at).slice(11, 16)}` : "";
}

/** Under the Accounts table: the tombstones it leaves out (D-57). */
export function deletedNote(n: number): string {
  if (n === 0) return "";
  return n === 1 ? "1 deleted account isn’t listed." : `${n} deleted accounts aren’t listed.`;
}

/** "1 min 52 s", "40 s". */
function minSec(s: number): string {
  return s >= 60 ? `${Math.floor(s / 60)} min ${s % 60} s` : `${s} s`;
}

/** Which of a disk policy's settings a change touched: "Build cache limit, alert level". */
function diskPolicyChanges(d: Record<string, unknown>): string {
  const before = (d.old ?? {}) as Record<string, unknown>;
  const after = (d.new ?? {}) as Record<string, unknown>;
  const words = { build_cache_limit_bytes: "build cache limit", auto_clean: "automatic clean", alert_percent: "alert level" };
  const changed = Object.entries(words)
    .filter(([k]) => JSON.stringify(before[k]) !== JSON.stringify(after[k]))
    .map(([, w]) => w)
    .join(", ");
  return changed.charAt(0).toUpperCase() + changed.slice(1);
}

/** The instance audit log's row in plain words (D-57), naming the account
 *  acted on in the sentence when there is one (D-139: no "On" column). */
export function auditWords(
  e: Pick<InstanceAuditEntry, "action" | "detail"> & { target?: { name: string } | null },
): { label: string; note: string } {
  const d = e.detail ?? {};
  // A deleted account's tombstone name says nothing; the note keeps its email.
  const t = e.target && e.target.name !== "Deleted account" ? e.target.name : "";
  switch (e.action) {
    case "operator.granted":
      return { label: t ? `Made ${t} an operator` : "Granted operator", note: "" };
    case "operator.revoked":
      return { label: t ? `Revoked ${t} as operator` : "Revoked operator", note: "" };
    case "user.deactivated": {
      const n = typeof d.invites_revoked === "number" ? d.invites_revoked : 0;
      return { label: t ? `Deactivated ${t}` : "Deactivated the account", note: n === 0 ? "" : n === 1 ? "1 pending invite revoked" : `${n} pending invites revoked` };
    }
    case "user.reactivated":
      return { label: t ? `Reactivated ${t}` : "Reactivated the account", note: "" };
    case "user.deleted":
      return { label: t ? `Deleted ${t}` : "Deleted the account", note: typeof d.email === "string" ? `Was ${d.email}` : "" };
    case "password_reset.issued":
      return { label: t ? `Issued ${t} a reset link` : "Issued a reset link", note: "" };
    case "password_reset.used":
      return { label: "Set a new password from a reset link", note: "" };
    // The setup wizard and This panel (D-62).
    case "panel.domain_changed": {
      const to = typeof d.to === "string" && d.to ? d.to : "the server’s IP address";
      return { label: "Changed the panel’s address", note: `Now ${to}` };
    }
    // A restored panel, on its operator's word (D-164).
    case "panel.ip_answer_stopped":
      return { label: "Stopped the panel answering on its IP address", note: typeof d.domain === "string" && d.domain ? `Now only ${d.domain}` : "" };
    // toft answer-on-ip, run on the server: no account.
    case "panel.ip_answer_restored":
      return { label: "Made the panel answer on its IP address again", note: "With toft answer-on-ip on the server" };
    // The save was put back when the routes could not be written.
    case "panel.domain_change_failed": {
      const still = d.undone === false ? "the new address stayed saved and applies at the next restart" : "nothing changed";
      if (d.from === d.to) return { label: "Tried to stop the panel answering on its IP address", note: `The routes could not be written: ${still}` };
      const to = typeof d.to === "string" && d.to ? d.to : "the server’s IP address";
      return { label: `Tried to change the panel’s address to ${to}`, note: `The routes could not be written: ${still}` };
    }
    case "panel.role_changed":
      return { label: "Changed the server role", note: d.to === "control_plane" ? "Now “Only Toft”" : "Now “Run apps here too”" };
    case "panel.upgrade_started":
      return { label: `Started the upgrade to ${String(d.to ?? "")}`, note: d.from ? `From ${String(d.from)}` : "" };
    case "panel.upgraded":
      return { label: `Upgraded Toft to ${String(d.to ?? "")}`, note: `From ${String(d.from ?? "")}${typeof d.seconds === "number" ? ` · ${minSec(d.seconds)}` : ""}` };
    case "panel.installed":
      return { label: `Installed Toft ${String(d.to ?? "")}`, note: "" };
    case "panel.upgrade_rolled_back":
      return { label: `The upgrade to ${String(d.to ?? "")} rolled back`, note: `${String(d.from ?? "")} runs again` };
    case "panel.upgrade_failed":
      return { label: `The upgrade to ${String(d.to ?? "")} stopped`, note: typeof d.error === "string" ? d.error : "" };
    case "panel.watchdog_set":
      return { label: "Set the watchdog’s ping URL", note: typeof d.host === "string" ? d.host : "" };
    case "panel.watchdog_cleared":
      return { label: "Turned the watchdog off", note: "" };
    case "panel.shell_opened":
      return { label: "Opened a shell on the panel’s server", note: typeof d.from === "string" && d.from ? `From ${d.from}` : "" };
    case "panel.shell_closed": {
      const s = typeof d.seconds === "number" ? d.seconds : 0;
      const open = `${s < 60 ? `${s} s` : `${Math.round(s / 60)} min`}`;
      // A restart of the panel left it open; the next backend closed it.
      return d.restart
        ? { label: "Shell on the panel’s server ended by a restart", note: `Open ${open}` }
        : { label: "Closed a shell on the panel’s server", note: `Open ${open}` };
    }
    // gVisor on the panel's server, through its host helper (D-97).
    case "panel.gvisor_installed":
      return { label: `Installed gVisor ${String(d.release ?? "")} on the panel’s server`, note: typeof d.platform === "string" ? d.platform : "" };
    case "panel.gvisor_install_failed":
      return {
        label: `Installing gVisor ${String(d.release ?? "")} on the panel’s server failed`,
        note: [d.step, d.error].filter((x) => typeof x === "string" && x).join(": "),
      };
    // The disk of the panel's server (D-135).
    case "panel.disk_clean_started":
      return { label: "Started a disk clean", note: d.automatic ? "The automatic clean" : "" };
    case "panel.disk_cleaned":
      if (d.outcome === "failed") return { label: "The disk clean failed", note: typeof d.error === "string" ? d.error : "" };
      return {
        label: "Cleaned the disk",
        note: [typeof d.freed_bytes === "number" && `Freed ${gb(d.freed_bytes)}`, typeof d.seconds === "number" && minSec(d.seconds)].filter(Boolean).join(" · "),
      };
    case "panel.volume_removed":
      return { label: typeof d.name === "string" && d.name ? `Removed the volume ${d.name}` : "Removed a volume", note: typeof d.bytes === "number" && d.bytes >= 0 ? gb(d.bytes) : "" };
    case "panel.disk_policy_changed":
      return { label: "Changed the disk policy", note: diskPolicyChanges(d) };
    // Updates to the panel's server (D-133): detail.action is what ran.
    case "panel.updates_started":
      return { label: "Started updates on the panel’s server", note: DOING[d.action as UpdateAction] ?? "" };
    case "panel.updates_succeeded":
      return {
        label: "Finished updates on the panel’s server",
        note: [DONE[d.action as UpdateAction], typeof d.packages === "number" && d.packages > 0 && `${d.packages} ${d.packages === 1 ? "package" : "packages"}`, typeof d.seconds === "number" && minSec(d.seconds)].filter(Boolean).join(" · "),
      };
    case "panel.updates_failed":
      return { label: "Updates on the panel’s server failed", note: typeof d.error === "string" ? d.error : "" };
    // The instance-wide switch; a workspace's own is agent.paused in its log.
    case "agents.paused":
      return { label: "Paused agents on this panel", note: "" };
    case "agents.resumed":
      return { label: "Resumed agents on this panel", note: "" };
    case "recovery_kit.viewed":
      return { label: "Viewed the recovery kit", note: "" };
    case "recovery_kit.stored":
      return { label: "Marked the recovery kit as stored", note: "" };
    case "setup.finished":
      return { label: "Finished setup", note: "" };
    default:
      return { label: e.action, note: "" };
  }
}

/** The reset link the operator sends (D-56). */
export const resetURL = (token: string) =>
  `${typeof window === "undefined" ? "" : window.location.origin}/reset/${token}`;

/** What a dialog says when a change is refused. */
export function changeError(err: unknown): string {
  if (!(err instanceof ApiError)) return "Couldn’t reach Toft. Nothing was changed.";
  const code = (err.body as { data?: { code?: unknown } } | null)?.data?.code;
  if (code === "wrong_password") return "Your password is incorrect. Nothing was changed.";
  if (err.status === 429) return `Too many attempts. Try again in ${waitFor(err)}.`;
  if (err.status === 404) return "No account has this email. Operators can’t be invited before they sign up.";
  const msg = err.message.replace(/ — /g, ": ");
  return msg.charAt(0).toUpperCase() + msg.slice(1) + (msg.endsWith(".") ? "" : ".");
}

/** Accounts whose name or email contains q, ignoring case. */
export function filterAccounts<T extends Pick<InstanceUser, "name" | "email">>(users: T[], q: string): T[] {
  const needle = q.trim().toLowerCase();
  if (!needle) return users;
  return users.filter((u) => u.name.toLowerCase().includes(needle) || u.email.toLowerCase().includes(needle));
}
