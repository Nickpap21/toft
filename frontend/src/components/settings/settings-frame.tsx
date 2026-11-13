"use client";

import { useEffect, useRef } from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useAuth } from "@/context/AuthContext";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";

interface Tab {
  href: string;
  label: string;
}

// The strip every Settings board draws (D-46): the workspace's pages, then
// your own. The instance's moved to Instance (D-68).
const WORKSPACE: Tab[] = [
  { href: "/settings", label: "Workspace" },
  { href: "/settings/agents", label: "Agents" },
  { href: "/settings/api-keys", label: "API keys" },
  { href: "/settings/alerts", label: "Alerts" },
  { href: "/settings/storage", label: "Storage" },
  { href: "/settings/backups", label: "Backups" },
];
const YOURS: Tab[] = [
  { href: "/settings/account", label: "Account" },
  { href: "/settings/appearance", label: "Appearance" },
];

function Divider() {
  return (
    <span
      aria-hidden="true"
      className="mx-2.5 h-[18px] w-px flex-none bg-subtle"
    />
  );
}

function TabLink({ tab, current }: { tab: Tab; current: boolean }) {
  return (
    <Link
      href={tab.href}
      aria-current={current ? "page" : undefined}
      className={cn(
        "inline-flex h-[38px] flex-none items-center border-b-2 px-3 text-sm font-medium",
        current
          ? "border-accent text-fg"
          : "border-transparent text-fg/70 hover:text-fg",
      )}
    >
      {tab.label}
    </Link>
  );
}

interface SettingsFrameProps {
  /** The page's name: the tab's label in the document title. The h1 is
   *  Settings on every tab (board ReworkSettings). */
  title: string;
  /** Above the h1: the workspace by default; your name on your own pages. */
  crumb?: string;
  /** The page's main action, top right. */
  action?: React.ReactNode;
  /** The gap between sections: 28px on most boards, 24px on some. */
  gap?: "gap-6" | "gap-7";
  children: React.ReactNode;
}

/** The Settings boards' header (breadcrumb, title, action, tab strip) and
 *  the column below it. Sets the document title. */
export function SettingsFrame({
  title,
  crumb,
  action,
  gap = "gap-7",
  children,
}: SettingsFrameProps) {
  const pathname = usePathname();
  const { user } = useAuth();
  const nav = useRef<HTMLElement>(null);
  const workspace = user?.orgName;

  useDocumentTitle(
    [title, "Settings", workspace, "Toft"].filter(Boolean).join(" · "),
  );

  // On a phone the strip scrolls: keep the open tab in view.

  useEffect(() => {
    nav.current
      ?.querySelector('[aria-current="page"]')
      ?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [pathname]);

  const tab = (t: Tab) => (
    <TabLink
      key={t.href}
      tab={t}
      current={pathname === t.href}
    />
  );

  return (
    <>
      <header className="-mx-4 -mt-4 mb-6 flex flex-col gap-3 border-b border-subtle px-4 pt-4 md:-mx-8 md:-mt-8 md:px-8">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 flex-col gap-0.5">
            <span className="truncate text-xs leading-5 text-muted">
              {crumb ?? workspace}
            </span>
            <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">
              Settings
            </h1>
          </div>
          {action}
        </div>
        <nav
          ref={nav}
          aria-label="Settings"
          className="flex items-center overflow-x-auto whitespace-nowrap [scrollbar-width:none]"
        >
          {WORKSPACE.map((t) => tab(t))}
          <Divider />
          {YOURS.map((t) => tab(t))}
        </nav>
      </header>
      <div className={cn("flex w-full max-w-[1320px] flex-col text-sm", gap)}>
        {children}
      </div>
    </>
  );
}

// ─── The boards' shared parts ────────────────────────────────────────────────

/** A section's heading row: the h2, an optional count, a note on the right. */
export function SectionHead({
  id,
  title,
  count,
  note,
}: {
  id: string;
  title: string;
  count?: number | string;
  note?: React.ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-baseline justify-between gap-2">
      <div className="flex items-center gap-2">
        <h2
          id={id}
          className="text-[15px] font-semibold leading-[22px] text-fg"
        >
          {title}
        </h2>
        {count !== undefined && (
          <span className="rounded-full bg-raised px-[7px] font-mono text-xs leading-5 text-fg">
            {count}
          </span>
        )}
      </div>
      {note && <span className="text-xs text-muted">{note}</span>}
    </div>
  );
}

/** A card's head (the boards' .ch): the h2, a short summary beside it, and
 *  an action or switch on the right. */
export function CardHead({
  id,
  title,
  sum,
  children,
}: {
  id: string;
  title: string;
  sum?: React.ReactNode;
  children?: React.ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3">
      <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
        <h2 id={id} className="text-[15px] font-semibold leading-[22px] text-fg">
          {title}
        </h2>
        {sum && <span className="text-xs text-muted">{sum}</span>}
      </div>
      {children}
    </div>
  );
}

/** A bordered surface: tables, forms and cards. */
export const card = "rounded-[10px] border border-subtle bg-surface";
/** On a card with a Read directly inside it whose answer runs edge to edge: a
 *  failed read's row gets the padding its answer doesn't need. */
export const readInCard = "[&>[data-read-state=failed]]:px-4 [&>[data-read-state=failed]]:py-3";
export const th =
  "border-b border-subtle px-4 py-2.5 text-left text-xs font-medium leading-5 text-muted";
export const td = "border-t border-subtle px-4 py-2.5";
/** A table row that is a two-column card below 640 px (the board's .lt): what
 *  it is on the left, its action on the right. Its table and tbody take
 *  max-sm:block and its thead max-sm:hidden. */
export const phoneRow =
  "max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-1 max-sm:px-4 max-sm:py-3 max-sm:[overflow-wrap:anywhere] max-sm:not-first:border-t max-sm:not-first:border-subtle";
export const phoneTd = `${td} max-sm:block max-sm:border-0 max-sm:p-0`;
/** The one cell of a row that stands for the table: loading, a failure, none yet. */
export const phoneNote = `${td} max-sm:block max-sm:border-0`;
/** A column's heading, for the cells that need one when the card has no thead. */
export const phoneLabel = "text-xs text-muted sm:hidden";
/** The boards' small bordered button: "Change role", "Copy link", "Revoke". */
export const smallButton =
  "touch-target inline-flex h-[30px] items-center gap-1.5 rounded-md border border-subtle bg-raised px-2.5 text-xs text-fg hover:bg-raised/70 disabled:opacity-50";
/** The neutral role chip (D-46): Owner, Developer, Viewer. */
export const roleChip =
  "inline-flex h-[22px] items-center rounded-full border border-subtle px-2 text-xs font-medium capitalize text-fg/85";
export const fieldLabel = "text-xs font-medium leading-5 text-fg/70";
export const fieldInput =
  "touch-target h-9 rounded-lg border border-strong bg-ground px-2.5 text-[13px] text-fg placeholder:text-muted disabled:opacity-60";
export const help = "text-xs leading-5 text-muted";

// The boards' dialogs: 520px, a 17px title, a footer of 36px buttons.
export const dialogContent = "max-w-[520px] rounded-xl p-0";
export const dialogHeader = "mb-0 flex flex-col gap-1 px-5 pt-[18px] pr-12 text-left";
export const dialogTitle = "text-[17px] font-semibold leading-6";
export const dialogText = "text-[13px] leading-[19px] text-fg/70";
export const dialogFooter = "mt-[18px] flex flex-wrap justify-end gap-2 border-t border-subtle px-5 py-3";
export const cancelButton =
  "touch-target inline-flex h-9 items-center rounded-lg border border-subtle bg-raised px-3 text-[13px] font-medium text-fg hover:bg-raised/70 disabled:opacity-50";
export const primaryButton =
  "touch-target inline-flex h-9 items-center gap-1.5 rounded-lg bg-accent px-3.5 text-[13px] font-medium text-on-accent hover:bg-accent/90 disabled:opacity-50";
export const dangerButton =
  "touch-target inline-flex h-9 items-center gap-1.5 rounded-lg bg-danger px-3.5 text-[13px] font-semibold text-on-danger hover:bg-danger/90 disabled:opacity-50";
/** The boards' .seg: one choice of a few, as a row of pressed buttons (a
 *  group with aria-pressed, as the boards mark it up). */
export function Segmented<T extends string | number>({
  label,
  options,
  value,
  onChange,
  disabled,
}: {
  label: string;
  options: readonly { value: T; label: string }[];
  value: T;
  onChange: (v: T) => void;
  disabled?: boolean;
}) {
  return (
    <span
      role="group"
      aria-label={label}
      className="inline-flex max-w-full flex-wrap gap-0.5 self-start rounded-lg border border-strong bg-ground p-0.5"
    >
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          aria-pressed={value === o.value}
          disabled={disabled}
          onClick={() => onChange(o.value)}
          className={cn(
            "h-[26px] rounded-md px-2.5 text-[13px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:cursor-default pointer-coarse:min-h-11 pointer-coarse:min-w-11",
            value === o.value
              ? "bg-selected font-medium text-fg ring-1 ring-inset ring-strong"
              : "text-fg/70 enabled:hover:text-fg disabled:opacity-60",
          )}
        >
          {o.label}
        </button>
      ))}
    </span>
  );
}

/** A selectable card: the role radios in the invite and role dialogs. */
export const choice = (picked: boolean) =>
  cn(
    "flex cursor-pointer gap-3 rounded-lg border px-3 py-2.5",
    picked ? "border-link bg-[color-mix(in_oklab,var(--accent)_10%,var(--surface))]" : "border-subtle bg-surface",
  );
