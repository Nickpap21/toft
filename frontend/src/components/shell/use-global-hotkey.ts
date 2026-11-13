"use client";

import { useEffect, useRef } from "react";

/** True for ⌘K / Ctrl+K, unless the keystroke belongs to a terminal: in the
 *  exec terminals (xterm) Ctrl+K is readline's kill-line. Any element can
 *  opt out with data-hotkeys="off". */
export function isPaletteHotkey(e: KeyboardEvent): boolean {
  if (!(e.metaKey || e.ctrlKey) || e.altKey || e.shiftKey || e.key.toLowerCase() !== "k") return false;
  const target = e.target instanceof Element ? e.target : null;
  return !target?.closest(".xterm, [data-hotkeys='off']");
}

export function usePaletteHotkey(onToggle: () => void) {
  const handler = useRef(onToggle);
  useEffect(() => {
    handler.current = onToggle;
  });
  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (!isPaletteHotkey(e)) return;
      e.preventDefault();
      handler.current();
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);
}
