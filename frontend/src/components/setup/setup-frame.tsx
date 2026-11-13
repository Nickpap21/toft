"use client";

import { cn } from "@/lib/utils";
import { AuthShell, authH1, useInstanceHost } from "@/components/shell/auth-shell";

/** The setup wizard's steps, in order (7g, board ReworkSetup): four required
 *  ones. Backups, more servers and invites are on Home's checklist. */
export const SETUP_STEPS = [
  { id: "owner", title: "Your account", href: "/setup" },
  { id: "address", title: "Address", href: "/setup/address" },
  { id: "role", title: "This server", href: "/setup/role" },
  { id: "kit", title: "Recovery kit", href: "/setup/kit" },
] as const;

export type SetupStepId = (typeof SETUP_STEPS)[number]["id"];

/** The step after id, for Continue. */
export function nextStep(id: SetupStepId): string {
  const i = SETUP_STEPS.findIndex((s) => s.id === id);
  return SETUP_STEPS[Math.min(i + 1, SETUP_STEPS.length - 1)].href;
}

/** The step before id, for Back. */
export function previousStep(id: SetupStepId): string {
  const i = SETUP_STEPS.findIndex((s) => s.id === id);
  return SETUP_STEPS[Math.max(i - 1, 0)].href;
}

/**
 * The wizard's frame: the signed-out top bar with "Setting up <host>", the
 * four steps as bars, the step's heading, then its card, and the line that
 * says what comes after. Below 640 px only the current step is named.
 */
export function SetupFrame({ step, title, children }: { step: SetupStepId; title: string; children: React.ReactNode }) {
  const at = SETUP_STEPS.findIndex((s) => s.id === step);
  const host = useInstanceHost();
  return (
    <AuthShell right={<span className="text-[13px] text-muted">Setting up {host}</span>} className="pt-6 sm:pt-10">
      <div className="flex w-full max-w-[568px] flex-col gap-4">
        <ol aria-label={`Setup, step ${at + 1} of ${SETUP_STEPS.length}`} className="grid grid-cols-4 gap-2">
          {SETUP_STEPS.map((s, i) => (
            <li
              key={s.id}
              aria-current={i === at ? "step" : undefined}
              className={cn(
                "flex flex-col gap-1.5 text-xs before:block before:h-[3px] before:rounded-xs before:content-['']",
                i < at ? "text-fg/70 before:bg-fg/35" : i === at ? "font-medium text-fg before:bg-link" : "text-muted before:bg-raised",
              )}
            >
              <span className={cn(i !== at && "max-sm:sr-only")}>
                {s.title}
                {i < at && <span className="sr-only"> (done)</span>}
              </span>
            </li>
          ))}
        </ol>
        <h1 id="setup-h" tabIndex={-1} className={authH1}>
          {title}
        </h1>
        {children}
        <p className="text-center text-[13px] text-muted">Backups, more servers and your team come next, on Home. Nothing else is required.</p>
      </div>
    </AuthShell>
  );
}

/** The step's card: a body, then a footer with Back and the step's action. */
export const setupCard = "min-w-0 rounded-[10px] border border-subtle bg-surface";
export const setupBody = "flex flex-col gap-3.5 p-4";
export const setupFoot = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5";
export const setupLead = "text-[13px] leading-[19px] text-fg/70";
export const setupLabel = "text-[13px] font-semibold text-fg";
export const setupInput =
  "h-9 pointer-coarse:h-11 w-full rounded-lg border border-strong bg-ground px-2.5 text-[13px] text-fg focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-link aria-invalid:border-status-failed";
export const setupPrimary =
  "h-9 pointer-coarse:h-11 rounded-lg bg-accent px-3 text-[13px] font-medium text-on-accent hover:bg-accent/90 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-link disabled:opacity-50 max-sm:flex-1";
export const setupBack =
  "h-9 pointer-coarse:h-11 rounded-lg px-3 text-[13px] font-medium text-fg/70 hover:text-fg focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-link disabled:opacity-60";
export const setupOption = (on: boolean) =>
  cn("flex cursor-pointer gap-3 rounded-[10px] border px-3.5 py-3", on ? "border-accent ring-1 ring-inset ring-accent" : "border-strong/60");
