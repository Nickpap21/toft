"use client";

import { usePathname, useRouter } from "next/navigation";
import { Suspense, useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { LogOut } from "lucide-react";
import { useAuth, useIsOperator } from "@/context/AuthContext";
import { setupQuery } from "@/lib/api/instance.service";
import { Button } from "@/components/ui/button";
import { CommandPaletteProvider } from "./command-palette-provider";
import { MobileNav } from "./mobile-nav";
import { Sidebar } from "./sidebar";
import { SearchButton } from "./search-button";
import { EnvHairline } from "./env-switcher";
import { useProjectShell } from "./project-sidebar";
import { SidebarProvider } from "./sidebar-state";
import { ReachablePage } from "./unreachable";
import { useHashLanding } from "./hash-landing";

function NoWorkspacePanel({ onSignOut }: { onSignOut: () => void }) {
  return (
    <div className="mx-auto mt-16 max-w-md space-y-3 rounded-lg border border-subtle bg-surface p-6">
      <h1 className="text-[17px] font-semibold text-fg">You&apos;re not a member of any workspace</h1>
      <p className="text-sm text-muted">Ask a workspace owner for an invite link. Instance operators can create workspaces.</p>
      <Button variant="outline" className="gap-2" onClick={onSignOut}>
        <LogOut aria-hidden="true" className="h-4 w-4" />
        Sign out
      </Button>
    </div>
  );
}

/** While setup is in progress and its required steps (the server role and
 *  the recovery kit) aren't done, an operator is sent back to it (D-62). */
function useSetupRedirect(isOperator: boolean) {
  const router = useRouter();
  const setup = useQuery({ ...setupQuery, enabled: isOperator }).data;
  const pending = !!setup?.in_progress && (!setup.role_set || !setup.kit_acknowledged_at);
  useEffect(() => {
    if (pending) router.replace("/setup");
  }, [pending, router]);
}

/** Along the top of a protected environment's pages (D-136, D-142). */
function ProjectEnvLine() {
  const { shown, missing } = useProjectShell();
  return missing ? null : <EnvHairline env={shown} />;
}

/**
 * The signed-in shell (D-05, D-136): the sidebar, which is the project's on
 * project pages, and the page in <main>. It lives in the root layout, so it stays mounted
 * across sections. It renders no h1; each page has its own.
 */
export function AppShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const { user, logout } = useAuth();
  const isOperator = useIsOperator();
  useSetupRedirect(isOperator);
  useHashLanding(pathname);
  // Signed in but a member of no workspace (removed from all of them), as
  // confirmed by the server: an unknown org context (loading it failed) does
  // not count, and AuthProvider retries it. Every page here is
  // workspace-scoped, so explain instead of rendering broken pages. Operators
  // get a workspace auto-created instead.
  const noWorkspace = user?.noWorkspace === true && !isOperator;
  const inProject = /^\/projects\/[^/]+/.test(pathname);

  return (
    <SidebarProvider>
      <CommandPaletteProvider>
        <a
          href="#main"
          className="sr-only z-50 rounded-md border border-strong bg-surface text-sm font-medium text-fg shadow-menu focus:not-sr-only focus:fixed focus:left-3 focus:top-3 focus:px-3 focus:py-2"
        >
          Skip to content
        </a>
        <div className="flex h-dvh overflow-hidden bg-ground">
          <div
            id="sidebar-rail"
            // The rail-collapsed: variant only applies inside [data-rail], so
            // the mobile sheet is never collapsed.
            data-rail=""
            className="hidden w-62 shrink-0 flex-col border-r border-subtle bg-[color-mix(in_oklab,var(--ground)_50%,var(--surface))] transition-[width] duration-150 md:flex rail-collapsed:w-16"
          >
            <Sidebar pathname={pathname} variant="rail" search={<SearchButton />} />
          </div>
          <div className="flex min-w-0 flex-1 flex-col">
            <MobileNav pathname={pathname} search={<SearchButton />} inProject={inProject && !noWorkspace} />
            {inProject && !noWorkspace && (
              <Suspense>
                <ProjectEnvLine />
              </Suspense>
            )}
            {/* The only scroller: the window never scrolls (2026-11-07). relative
                makes main the containing block of absolute descendants (sr-only
                labels), which otherwise escape its overflow and stretch the
                window; overscroll-none keeps a trackpad's bounce off the window. */}
            <main id="main" tabIndex={-1} className="relative flex-1 overflow-auto overscroll-none p-4 focus-visible:outline-none md:p-8">
              {noWorkspace ? <NoWorkspacePanel onSignOut={() => logout()} /> : <ReachablePage>{children}</ReachablePage>}
            </main>
          </div>
        </div>
      </CommandPaletteProvider>
    </SidebarProvider>
  );
}
