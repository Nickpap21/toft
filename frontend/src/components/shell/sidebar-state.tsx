"use client";

import { createContext, useCallback, useContext, useMemo, useSyncExternalStore } from "react";
import { SIDEBAR_KEY } from "@/lib/theme/themes";

/**
 * Whether the desktop sidebar is collapsed to its icon rail: as chosen, and
 * by default from 768 to 1199 px (D-136), where choosing expands it. The
 * head script sets <html data-sidebar="collapsed" | "expanded"> before paint
 * and CSS sizes the rail from it and the width, so the server render never
 * has to know. This store keeps React (aria-expanded, tooltips) and the
 * attribute in step.
 */

const NARROW = "(max-width: 1199px)";
const listeners = new Set<() => void>();
const narrow = () => typeof window.matchMedia === "function" && window.matchMedia(NARROW).matches;

function subscribe(onChange: () => void) {
  listeners.add(onChange);
  const mq = typeof window.matchMedia === "function" ? window.matchMedia(NARROW) : null;
  mq?.addEventListener("change", onChange);
  return () => {
    listeners.delete(onChange);
    mq?.removeEventListener("change", onChange);
  };
}

const getSnapshot = () => {
  const state = document.documentElement.dataset.sidebar;
  return state === "collapsed" || (narrow() && state !== "expanded");
};
const getServerSnapshot = () => false;

function setCollapsed(collapsed: boolean) {
  const html = document.documentElement;
  // Only what differs from the width's default is kept.
  const state = collapsed === narrow() ? "" : collapsed ? "collapsed" : "expanded";
  if (state) html.dataset.sidebar = state;
  else delete html.dataset.sidebar;
  try {
    localStorage.setItem(SIDEBAR_KEY, state === "collapsed" ? "true" : state || "false");
  } catch {
    // Blocked storage: the rail stays as set until the page reloads.
  }
  for (const l of listeners) l();
}

interface SidebarState {
  collapsed: boolean;
  toggle: () => void;
}

const SidebarContext = createContext<SidebarState>({ collapsed: false, toggle: () => {} });

export function SidebarProvider({ children }: { children: React.ReactNode }) {
  const collapsed = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
  const toggle = useCallback(() => setCollapsed(!getSnapshot()), []);
  const value = useMemo(() => ({ collapsed, toggle }), [collapsed, toggle]);
  return <SidebarContext.Provider value={value}>{children}</SidebarContext.Provider>;
}

export function useSidebar() {
  return useContext(SidebarContext);
}
