"use client";

import { useSyncExternalStore } from "react";
import { useCommandPalette } from "./command-palette-provider";
import { SearchIcon } from "./nav-icons";

const noop = () => () => {};
const isMac = () => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

/** "⌘K" on Apple platforms, "Ctrl K" elsewhere. Read after hydration. */
export function useShortcutLabel(): string {
  return useSyncExternalStore(noop, () => (isMac() ? "⌘K" : "Ctrl K"), () => "Ctrl K");
}

/** The sidebar's way into the palette. */
export function SearchButton() {
  const { openPalette } = useCommandPalette();
  const shortcut = useShortcutLabel();
  return (
    <button
      type="button"
      onClick={() => openPalette()}
      aria-keyshortcuts="Meta+K Control+K"
      title={`Search or run a command (${shortcut})`}
      className="flex h-9 w-full items-center gap-1.5 rounded-lg border border-subtle bg-surface px-2 text-left text-[13px] text-muted transition-colors hover:border-strong hover:text-fg rail-collapsed:justify-center rail-collapsed:px-0"
    >
      <SearchIcon aria-hidden="true" className="h-[15px] w-[15px] shrink-0" />
      {/* The board's phrase fits beside "⌘K", not beside the wider "Ctrl K". */}
      <span className="min-w-0 flex-1 truncate rail-collapsed:sr-only">
        {shortcut === "⌘K" ? "Search or run a command" : <>Search <span className="sr-only">or run a command</span></>}
      </span>
      <kbd aria-hidden="true" className="rounded border border-strong/60 px-[5px] font-mono text-[11px] leading-4 text-fg/75 rail-collapsed:hidden">
        {shortcut}
      </kbd>
    </button>
  );
}

/** The mobile top bar's icon-only version. */
export function SearchIconButton() {
  const { openPalette } = useCommandPalette();
  return (
    <button
      type="button"
      onClick={() => openPalette()}
      aria-label="Search or run a command"
      className="flex h-11 w-11 shrink-0 items-center justify-center rounded-lg text-fg/70 hover:bg-raised hover:text-fg"
    >
      <SearchIcon aria-hidden="true" className="h-[18px] w-[18px]" />
    </button>
  );
}
