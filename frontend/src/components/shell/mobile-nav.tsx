"use client";

import { Suspense, useState } from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { Menu } from "lucide-react";
import { useCommandPalette } from "./command-palette-provider";
import { WorkspaceSwitcher } from "@/components/workspace-switcher";
import { InboxIconLink, ProjectEnvSwitcher, ProjectSwitcher } from "./project-sidebar";
import { SearchIconButton } from "./search-button";
import { Sidebar } from "./sidebar";

/** Below md: a top bar with the navigation in a slide-over sheet (D-136):
 *  the workspace, or the project and its environment, then search and
 *  Inbox. Radix Dialog gives the sheet its focus trap, Escape and focus
 *  return. */
interface MobileNavProps {
  pathname: string;
  /** The ⌘K trigger inside the sheet. */
  search?: React.ReactNode;
  /** On a project's pages: the project and its environment. */
  inProject: boolean;
}

export function MobileNav({ pathname, search, inProject }: MobileNavProps) {
  const [open, setOpen] = useState(false);
  const { open: paletteOpen } = useCommandPalette();

  // Close the sheet once a link inside it navigates (adjusting state during
  // render rather than in an effect).
  const [prevPathname, setPrevPathname] = useState(pathname);
  if (pathname !== prevPathname) {
    setPrevPathname(pathname);
    setOpen(false);
  }
  // The sheet gives way to ⌘K rather than stacking two modals.
  if (open && paletteOpen) setOpen(false);

  return (
    <div className="flex h-14 shrink-0 items-center gap-0.5 border-b border-subtle bg-[color-mix(in_oklab,var(--ground)_50%,var(--surface))] pl-1 pr-1.5 md:hidden">
      <DialogPrimitive.Root open={open} onOpenChange={setOpen}>
        <DialogPrimitive.Trigger
          id="mobile-nav-trigger"
          aria-label="Open navigation"
          className="flex h-11 w-11 shrink-0 items-center justify-center rounded-lg text-muted hover:bg-raised hover:text-fg"
        >
          <Menu aria-hidden="true" className="h-5 w-5" />
        </DialogPrimitive.Trigger>
        <DialogPrimitive.Portal>
          <DialogPrimitive.Overlay className="toft-overlay fixed inset-0 z-40 bg-scrim/50 md:hidden" />
          <DialogPrimitive.Content className="toft-sheet fixed inset-y-0 left-0 z-50 flex w-72 md:hidden max-w-[85vw] flex-col border-r border-subtle bg-surface text-fg shadow-dialog">
            <DialogPrimitive.Title className="sr-only">Navigation</DialogPrimitive.Title>
            <DialogPrimitive.Description className="sr-only">
              {inProject
                ? "The project's pages, its environment and the way back to the workspace. Press Escape to close."
                : "Workspace, projects, infrastructure and settings. Press Escape to close."}
            </DialogPrimitive.Description>
            <Sidebar pathname={pathname} variant="sheet" search={search} />
          </DialogPrimitive.Content>
        </DialogPrimitive.Portal>
      </DialogPrimitive.Root>
      {inProject ? (
        <Suspense fallback={<span className="flex-1" />}>
          <ProjectSwitcher variant="top" />
          <ProjectEnvSwitcher variant="top" />
        </Suspense>
      ) : (
        <div className="flex min-w-0 flex-1">
          <WorkspaceSwitcher variant="top" />
        </div>
      )}
      <SearchIconButton />
      <InboxIconLink className="h-11 w-11" />
    </div>
  );
}
