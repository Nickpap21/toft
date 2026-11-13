import { ApiError } from "@/lib/api/client";
import type { NoticeKind } from "./auth-shell";

function errorData(err: unknown): Record<string, unknown> | undefined {
  if (!(err instanceof ApiError)) return undefined;
  const data = (err.body as { data?: unknown } | null)?.data;
  return data && typeof data === "object" ? (data as Record<string, unknown>) : undefined;
}

/** "90 seconds", "3 minutes": how long a 429 asked to wait. */
export function waitFor(err: unknown): string {
  const n = Number(errorData(err)?.retry_after);
  if (!Number.isFinite(n) || n <= 0) return "a few seconds";
  if (n <= 90) return `${n} second${n === 1 ? "" : "s"}`;
  return `${Math.ceil(n / 60)} minutes`;
}

/** The invite error's stable code (invite_not_found, invite_expired,
 *  invite_used), or "" for any other error. */
export function inviteCode(err: unknown): string {
  const code = errorData(err)?.code;
  return typeof code === "string" && code.startsWith("invite_") ? code : "";
}

/** The reset link's code (reset_not_found, reset_used, reset_expired), or "". */
export function resetCode(err: unknown): string {
  const code = errorData(err)?.code;
  return typeof code === "string" && code.startsWith("reset_") ? code : "";
}

/** What a sign-in form says when signing in fails (board SignIn). */
export function signInError(err: unknown): { kind: NoticeKind; text: string } {
  if (!(err instanceof ApiError)) {
    return { kind: "error", text: "Couldn’t reach Toft. Check your connection and try again." };
  }
  switch (err.status) {
    case 401:
      return { kind: "error", text: "That email and password don’t match an account on this instance." };
    case 403:
      return { kind: "banned", text: "This account is deactivated. Ask an instance operator to reactivate it." };
    case 429:
      return { kind: "wait", text: `Too many sign-in attempts. Try again in ${waitFor(err)}.` };
    default:
      return { kind: "error", text: err.message };
  }
}
