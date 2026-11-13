"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { MoreHorizontal, SquareTerminal } from "lucide-react";
import { useAuth, useIsOperator } from "@/context/AuthContext";
import {
  instanceService,
  panelHostQuery,
  panelStatusQuery,
  setupQuery,
  upgradeQuery,
} from "@/lib/api/instance.service";
import { diskService, updatesService } from "@/lib/api/server.service";
import { TONE_CLASSES } from "@/lib/status";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { card } from "@/components/settings/settings-frame";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { ToftMark } from "@/components/shell/toft-mark";
import { PanelShellDrawer } from "./panel-shell";
import { UpgradeButton, UpgradeCard, UpgradeDialog } from "./upgrade";
import { healthChip, panelMaintenance, retryUpgrade, uptimeWords } from "./instance-view";
import { maintenanceCount } from "@/app/servers/[id]/server-view";

/** Instance's tabs (D-68, board InstanceHead): the panel itself, then what
 *  moved here from Settings. */
export const INSTANCE_TABS = [
  { href: "/instance", label: "Overview" },
  { href: "/instance/server", label: "Server" },
  { href: "/instance/maintenance", label: "Maintenance" },
  { href: "/instance/accounts", label: "Accounts" },
  { href: "/instance/deploy-policy", label: "Deploy policy" },
  { href: "/instance/audit-log", label: "Audit log" },
] as const;

interface InstanceFrameProps {
  /** The tab's name, for the document title. */
  title: string;
  /** Top right, beside the panel's own actions. */
  action?: React.ReactNode;
  gap?: "gap-4" | "gap-6";
  children: React.ReactNode;
}

/** The header every Instance tab shares: "This panel", its health and
 *  version, its address, role and uptime, and the tabs. Operators only:
 *  anyone else sees one line instead of the page. */
export function InstanceFrame({
  title,
  action,
  gap = "gap-6",
  children,
}: InstanceFrameProps) {
  const pathname = usePathname();
  const { isLoading } = useAuth();
  const isOperator = useIsOperator();
  const nav = useRef<HTMLElement>(null);
  const [shell, setShell] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const upgradeQ = useQuery({
    ...upgradeQuery,
    enabled: isOperator,
    retry: false,
  });
  const upgrade = upgradeQ.data;
  // While an upgrade runs the panel restarts: its requests fail for a moment.
  const unreachable = upgrade?.run?.outcome === "running" && upgradeQ.isError;
  const [opened] = useState(() => Date.now());
  const canUpgrade =
    upgrade?.install === "installer" &&
    upgrade.available &&
    upgrade.run?.outcome !== "running" &&
    !retryUpgrade(upgrade);
  const status = useQuery({
    ...panelStatusQuery,
    enabled: isOperator,
    retry: false,
  });
  const setup = useQuery({ ...setupQuery, enabled: isOperator });
  const host = useQuery({ ...panelHostQuery, enabled: isOperator });
  // The keys Maintenance's cards read, so the count and the cards agree.
  const updates = useQuery({ queryKey: ["updates", null], queryFn: () => updatesService.get(null), enabled: isOperator }).data;
  const disk = useQuery({ queryKey: ["disk", null], queryFn: () => diskService.get(null), enabled: isOperator, retry: false }).data;
  const due = maintenanceCount(panelMaintenance(updates ?? undefined, disk));
  // The Accounts tab's key, so its count and its table agree.
  const accounts = useQuery({ queryKey: ["instance", "users"], queryFn: instanceService.listUsers, enabled: isOperator, retry: false }).data;

  useDocumentTitle([title, "Instance", "Toft"].join(" · "));
  useEffect(() => {
    nav.current
      ?.querySelector('[aria-current="page"]')
      ?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [pathname]);

  const chip = status.data ? healthChip(status.data) : null;
  const address = setup.data?.domain || host.data?.address;
  const facts: [string, React.ReactNode][] = [];
  if (address)
    facts.push([
      "Address",
      <span key="a" className="font-mono text-xs">
        {address}
      </span>,
    ]);
  if (setup.data)
    facts.push([
      "Role",
      setup.data.role === "control_plane"
        ? "Only Toft"
        : "Run apps here too",
    ]);
  if (status.data?.uptime_seconds)
    facts.push([
      "Up",
      <span key="u" className="font-mono text-xs">
        {uptimeWords(status.data.uptime_seconds)}
      </span>,
    ]);
  if (upgrade)
    facts.push([
      "Installed",
      upgrade.install === "installer" ? "with install.sh" : "with deploy.sh",
    ]);

  return (
    <>
      <header className="-mx-4 -mt-4 mb-6 flex flex-col gap-3.5 border-b border-subtle px-4 pt-4 md:-mx-8 md:-mt-8 md:px-8">
        <span className="-mb-1.5 text-[13px] text-muted">Instance</span>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 flex-wrap items-center gap-3">
            <span className="flex h-9 w-9 flex-none items-center justify-center rounded-lg border border-subtle bg-raised text-fg">
              <ToftMark className="h-[22px] w-[22px]" />
            </span>
            <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">
              This panel
            </h1>
            {chip && (
              <span
                className={cn(
                  "inline-flex h-6 items-center gap-1.5 rounded-full px-2.5 text-xs font-medium",
                  TONE_CLASSES[chip.tone].chip,
                )}
              >
                <span
                  aria-hidden="true"
                  className={cn(
                    "h-1.5 w-1.5 rounded-full",
                    TONE_CLASSES[chip.tone].dot,
                  )}
                />
                {chip.text}
              </span>
            )}
            {status.data && (
              <span className="rounded-full border border-subtle px-2 font-mono text-xs leading-[22px] text-fg/85">
                Toft {status.data.version}
              </span>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-2 max-sm:w-full max-sm:flex-nowrap">
            {action}
            {status.data?.shell && (
              <button
                type="button"
                onClick={() => setShell(true)}
                className={cn(
                  "touch-target inline-flex h-9 items-center gap-1.5 rounded-lg border border-subtle bg-raised px-3 text-[13px] font-medium text-fg hover:bg-raised/70",
                  canUpgrade && "max-sm:hidden",
                )}
              >
                <SquareTerminal
                  className="h-[15px] w-[15px]"
                  aria-hidden="true"
                />
                Host shell
              </button>
            )}
            {canUpgrade && (
              <span className="max-sm:flex-1 max-sm:[&>button]:h-11 max-sm:[&>button]:w-full max-sm:[&>button]:justify-center">
                <UpgradeButton
                  status={upgrade}
                  onClick={() => setConfirm(true)}
                />
              </span>
            )}
            {/* On a phone the one accent button takes the row; the shell folds into ⋯ (board). */}
            {canUpgrade && status.data?.shell && (
              <DropdownMenu>
                <DropdownMenuTrigger
                  aria-label="More actions: host shell"
                  className="inline-flex h-11 w-11 flex-none items-center justify-center rounded-lg border border-subtle bg-raised text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus sm:hidden"
                >
                  <MoreHorizontal className="h-[18px] w-[18px]" aria-hidden="true" />
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onSelect={() => setShell(true)}>Host shell</DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            )}
          </div>
        </div>
        {[status, setup, upgradeQ].some(readFailed) && (
          <LoadFailed
            what="some of this panel’s details"
            onRetry={() => [status, setup, upgradeQ].filter(readFailed).forEach((q) => void q.refetch())}
          />
        )}
        {facts.length > 0 && (
          <dl className="flex flex-wrap gap-x-7 gap-y-2 text-[13px] max-sm:flex-col max-sm:gap-y-1">
            {facts.map(([dt, dd]) => (
              <div key={dt} className="flex gap-2">
                <dt className="text-muted">{dt}</dt>
                <dd className="text-fg">{dd}</dd>
              </div>
            ))}
          </dl>
        )}
        <nav
          ref={nav}
          aria-label="Instance"
          className="flex items-center overflow-x-auto whitespace-nowrap [scrollbar-width:none]"
        >
          {INSTANCE_TABS.map((t) => {
            // The recovery kit is Overview's page (board ReworkInstanceKit).
            const current = pathname === t.href || (t.href === "/instance" && pathname === "/instance/kit");
            return (
              <Link
                key={t.href}
                href={t.href}
                aria-current={current ? "page" : undefined}
                className={cn(
                  "touch-target inline-flex h-[38px] flex-none items-center border-b-2 px-3 text-sm font-medium",
                  current
                    ? "border-link text-fg"
                    : "border-transparent text-fg/70 hover:text-fg",
                )}
              >
                {t.label}
                {t.href === "/instance/maintenance" && due.n > 0 && (
                  <span
                    className={cn(
                      "ml-2 rounded-full px-1.5 font-mono text-xs font-semibold leading-[18px]",
                      due.tone === "bad" ? "bg-status-failed-soft text-status-failed-fg" : "bg-status-pending-soft text-status-pending-fg",
                    )}
                  >
                    {due.n}
                    <span className="sr-only"> {due.n === 1 ? "item needs" : "items need"} action</span>
                  </span>
                )}
                {t.href === "/instance/accounts" && accounts && (
                  <span className="ml-2 font-mono text-xs text-muted">{accounts.users.length}</span>
                )}
              </Link>
            );
          })}
        </nav>
      </header>
      <div className={cn("flex w-full max-w-[1320px] flex-col text-sm", gap)}>
        {isLoading ? null : isOperator ? (
          <>
            {/* Above every tab until dismissed (D-139). */}
            {upgrade?.run && (
              <UpgradeCard
                key={upgrade.run.started_at}
                status={upgrade}
                unreachable={unreachable}
                opened={opened}
                onRetry={() => setConfirm(true)}
              />
            )}
            {children}
          </>
        ) : (
          <div className={cn(card, "p-4 text-[13px] text-fg/70")}>
            Only instance operators see the panel’s own pages.
          </div>
        )}
      </div>
      <PanelShellDrawer open={shell} onClose={() => setShell(false)} />
      {upgrade && (
        <UpgradeDialog
          status={upgrade}
          open={confirm}
          onClose={() => setConfirm(false)}
        />
      )}
    </>
  );
}
