"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useSyncExternalStore } from "react";
import { applyTheme, otherSide, readPref, resolveTheme } from "@/lib/theme/resolve";
import { DARK_QUERY, STORAGE_KEY, THEMES, type ThemeId, type ThemeMode, type ThemePref } from "@/lib/theme/themes";

interface ThemeContextValue {
  /** What the user picked. Null until mounted (the server has no storage). */
  pref: ThemePref | null;
  /** The theme in use now. Null until mounted. */
  theme: ThemeId | null;
  mode: ThemeMode | null;
  setPref: (pref: ThemePref) => void;
  /** Switch to the other side of the light/dark divide (a fixed choice). */
  toggleMode: () => void;
}

const ThemeContext = createContext<ThemeContextValue>({
  pref: null,
  theme: null,
  mode: null,
  setPref: () => {},
  toggleMode: () => {},
});

// Same-tab writes don't fire the storage event, so setPref notifies directly.
const localListeners = new Set<() => void>();

function subscribe(onChange: () => void) {
  localListeners.add(onChange);
  const onStorage = (e: StorageEvent) => {
    if (e.key === null || e.key === STORAGE_KEY) onChange();
  };
  window.addEventListener("storage", onStorage);
  const mql = window.matchMedia(DARK_QUERY);
  mql.addEventListener("change", onChange);
  return () => {
    localListeners.delete(onChange);
    window.removeEventListener("storage", onStorage);
    mql.removeEventListener("change", onChange);
  };
}

function safeStorage(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

// Holds a choice that storage refused to save, for the rest of this page.
let unsavedPref: ThemePref | null = null;

// One string, so React can compare snapshots: "<pref>|<1 if the OS is dark>".
function getSnapshot(): string {
  const pref = unsavedPref ?? readPref(safeStorage());
  return `${pref}|${window.matchMedia(DARK_QUERY).matches ? 1 : 0}`;
}

function getServerSnapshot(): null {
  return null;
}

let switchTimer: ReturnType<typeof setTimeout> | undefined;

/** Applies a theme with transitions off for a moment, so a switch is instant. */
function applyNow(theme: ThemeId) {
  const html = document.documentElement;
  if (html.dataset.theme === theme) return;
  html.setAttribute("data-theme-switching", "");
  applyTheme(html, theme);
  clearTimeout(switchTimer);
  switchTimer = setTimeout(() => html.removeAttribute("data-theme-switching"), 50);
}

export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const snapshot = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);

  const [pref, theme] = useMemo((): [ThemePref | null, ThemeId | null] => {
    if (snapshot === null) return [null, null];
    const [p, dark] = snapshot.split("|");
    const parsed = p as ThemePref;
    return [parsed, resolveTheme(parsed, dark === "1")];
  }, [snapshot]);

  // The head script applied the theme before paint; this keeps <html> in
  // step when the preference changes here, in another tab or in the OS.
  useEffect(() => {
    if (theme) applyNow(theme);
  }, [theme]);

  const setPref = useCallback((next: ThemePref) => {
    try {
      // Only the new key: the legacy "theme" key stays as it was, so the
      // previous build still finds the user's choice after a rollback.
      localStorage.setItem(STORAGE_KEY, next);
      unsavedPref = null;
    } catch {
      // Blocked storage: the choice lasts until the page reloads.
      unsavedPref = next;
    }
    applyNow(resolveTheme(next, window.matchMedia(DARK_QUERY).matches));
    for (const l of localListeners) l();
  }, []);

  const toggleMode = useCallback(() => {
    if (theme) setPref(otherSide(theme));
  }, [theme, setPref]);

  const value = useMemo<ThemeContextValue>(
    () => ({ pref, theme, mode: theme ? THEMES[theme].mode : null, setPref, toggleMode }),
    [pref, theme, setPref, toggleMode],
  );

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme() {
  return useContext(ThemeContext);
}
