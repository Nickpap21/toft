"use client";

import { useSyncExternalStore } from "react";
import { Ban, CircleCheck, CircleX, Clock, Info, Link2, TriangleAlert } from "lucide-react";
import { cn } from "@/lib/utils";
import { ToftWordmark } from "./toft-mark";

const noop = () => () => {};

/** The instance's host, as the browser reached it ("" while prerendering). */
export function useInstanceHost(): string {
  return useSyncExternalStore(noop, () => window.location.host, () => "");
}

/**
 * The signed-out layout (boards SignIn, FirstRun, InviteAccept): a top bar
 * with the logo, an optional section ("Setup", "Invite") and the instance's
 * host, then the page's column. `className` sets the column's top padding.
 */
export function AuthShell({ section, right, className = "pt-[72px]", children }: {
  section?: string;
  /** The top bar's right side; the instance's host by default. */
  right?: React.ReactNode;
  className?: string;
  children: React.ReactNode;
}) {
  const host = useInstanceHost();
  return (
    <div className="flex min-h-dvh flex-col bg-ground text-sm leading-5 text-fg">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b border-subtle px-4 py-3.5 sm:px-8">
        <div className="flex items-center gap-2.5">
          <ToftWordmark className="h-[22px] w-auto" />
          {section && <span className="text-muted">{section}</span>}
        </div>
        {right ?? <span className="font-mono text-xs text-muted">{host}</span>}
      </header>
      <main className={cn("flex flex-col items-center px-4 pb-[88px]", className)}>{children}</main>
    </div>
  );
}

/** The 400px column the sign-in and invite pages use. */
export function AuthColumn({ className, children }: { className?: string; children: React.ReactNode }) {
  return <div className={cn("flex w-full max-w-[400px] flex-col gap-6", className)}>{children}</div>;
}

export function AuthCard({ className, children }: { className?: string; children: React.ReactNode }) {
  return <div className={cn("flex flex-col gap-4 rounded-[10px] border border-subtle bg-surface p-5", className)}>{children}</div>;
}

export const authH1 = "m-0 text-[26px] font-semibold leading-[34px] tracking-[-0.015em]";
export const authLabel = "text-[13px] font-medium text-fg/85";
export const authInput =
  "h-10 w-full rounded-lg border border-strong bg-ground px-3 text-sm text-fg focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-link aria-invalid:border-status-failed";
export const authHelp = "text-xs leading-[17px] text-muted";
export const authButton =
  "h-10 rounded-lg bg-accent px-[18px] font-medium text-on-accent hover:bg-accent/90 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-link disabled:opacity-60";
export const authLink = "inline-link font-medium";

const NOTICE = {
  info: { Icon: Info, box: "border-strong/60 bg-raised text-fg/85", icon: "text-fg/70" },
  next: { Icon: Link2, box: "border-strong/60 bg-raised text-fg/85", icon: "text-fg/70" },
  done: { Icon: CircleCheck, box: "border-strong/60 bg-raised text-fg/85", icon: "text-status-running" },
  wait: { Icon: Clock, box: "border-status-pending/40 bg-status-pending/5 text-fg/85", icon: "text-status-pending" },
  warn: { Icon: TriangleAlert, box: "border-status-pending/40 bg-status-pending/5 text-fg/85", icon: "text-status-pending" },
  error: { Icon: CircleX, box: "border-status-failed/40 bg-status-failed/10 text-status-failed-fg", icon: "text-status-failed" },
  banned: { Icon: Ban, box: "border-status-failed/40 bg-status-failed/10 text-status-failed-fg", icon: "text-status-failed" },
} as const;

export type NoticeKind = keyof typeof NOTICE;

/** The boards' notice: an icon and one sentence. Errors are alerts. */
export function Notice({ kind, alert = kind === "error" || kind === "banned", className, children }: {
  kind: NoticeKind;
  /** An alert interrupts a screen reader; a status waits its turn. */
  alert?: boolean;
  className?: string;
  children: React.ReactNode;
}) {
  const { Icon, box, icon } = NOTICE[kind];
  return (
    <div
      role={alert ? "alert" : "status"}
      className={cn("flex items-start gap-2.5 rounded-lg border px-3 py-2.5 text-[13px] leading-[19px]", box, className)}
    >
      <Icon aria-hidden="true" className={cn("mt-px h-4 w-4 flex-none", icon)} strokeWidth={1.9} />
      <span>{children}</span>
    </div>
  );
}
