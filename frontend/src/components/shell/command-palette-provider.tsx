"use client";

import { createContext, useCallback, useContext, useMemo, useRef, useState } from "react";
import { CommandPalette } from "./command-palette";
import { usePaletteHotkey } from "./use-global-hotkey";

interface PaletteState {
  open: boolean;
  setOpen: (open: boolean) => void;
  /** Opens it, with query typed in the search (Home's Deploy… and Create). */
  openPalette: (query?: string) => void;
}

const PaletteContext = createContext<PaletteState>({ open: false, setOpen: () => {}, openPalette: () => {} });

export function CommandPaletteProvider({ children }: { children: React.ReactNode }) {
  const [open, setOpenState] = useState(false);
  const [initial, setInitial] = useState("");
  // Radix returns focus only to a Dialog.Trigger. The palette opens from the
  // keyboard and from more than one button, so remember where focus was.
  const returnFocus = useRef<HTMLElement | null>(null);

  const setOpen = useCallback(
    (next: boolean) => {
      if (next && !open && document.activeElement instanceof HTMLElement) returnFocus.current = document.activeElement;
      if (!next) setInitial("");
      setOpenState(next);
    },
    [open],
  );
  const openPalette = useCallback(
    (query = "") => {
      setInitial(query);
      setOpen(true);
    },
    [setOpen],
  );
  usePaletteHotkey(() => setOpen(!open));

  const onCloseAutoFocus = useCallback((e: Event) => {
    e.preventDefault();
    const el = returnFocus.current;
    returnFocus.current = null;
    // The opener can be gone (the mobile sheet closes for the palette): fall
    // back to the mobile menu button when it is shown, else the page.
    const fallback = [document.getElementById("mobile-nav-trigger"), document.getElementById("main")].find(
      (c) => c && c.offsetParent !== null,
    );
    (el?.isConnected ? el : fallback)?.focus();
  }, []);

  const value = useMemo(() => ({ open, setOpen, openPalette }), [open, setOpen, openPalette]);
  return (
    <PaletteContext.Provider value={value}>
      {children}
      <CommandPalette open={open} initialQuery={initial} onOpenChange={setOpen} onCloseAutoFocus={onCloseAutoFocus} />
    </PaletteContext.Provider>
  );
}

export function useCommandPalette() {
  return useContext(PaletteContext);
}
