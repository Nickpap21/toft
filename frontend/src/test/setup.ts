import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, vi } from "vitest";

// jsdom lacks these; Radix and cmdk call them.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= ResizeObserverStub as unknown as typeof ResizeObserver;
Element.prototype.scrollIntoView ??= function scrollIntoView() {};
Element.prototype.hasPointerCapture ??= () => false;
Element.prototype.releasePointerCapture ??= () => {};

// Node 25+ defines its own localStorage and sessionStorage, and Vitest does
// not replace globals Node already has, so they shadow jsdom's (Node's
// localStorage has no methods without --localstorage-file). Use jsdom's.
const { window: domWindow } = (globalThis as unknown as { jsdom: { window: Window } }).jsdom;
for (const key of ["localStorage", "sessionStorage"] as const) {
  Object.defineProperty(globalThis, key, { value: domWindow[key], configurable: true });
}

/** A controllable prefers-color-scheme. Tests flip it with setSystemDark(). */
let systemDark = false;
const mqlListeners = new Set<(e: MediaQueryListEvent) => void>();

export function setSystemDark(dark: boolean) {
  systemDark = dark;
  for (const l of mqlListeners) l({ matches: dark } as MediaQueryListEvent);
}

window.matchMedia = vi.fn((query: string) => {
  const isDarkQuery = query.includes("prefers-color-scheme: dark");
  return {
    get matches() {
      return isDarkQuery ? systemDark : false;
    },
    media: query,
    onchange: null,
    addEventListener: (_: string, l: (e: MediaQueryListEvent) => void) => isDarkQuery && mqlListeners.add(l),
    removeEventListener: (_: string, l: (e: MediaQueryListEvent) => void) => mqlListeners.delete(l),
    addListener: (l: (e: MediaQueryListEvent) => void) => isDarkQuery && mqlListeners.add(l),
    removeListener: (l: (e: MediaQueryListEvent) => void) => mqlListeners.delete(l),
    dispatchEvent: () => false,
  } as unknown as MediaQueryList;
});

afterEach(() => {
  cleanup();
  localStorage.clear();
  sessionStorage.clear();
  systemDark = false;
  mqlListeners.clear();
  const html = document.documentElement;
  html.removeAttribute("data-theme");
  html.removeAttribute("data-sidebar");
  html.removeAttribute("style");
  html.className = "";
});
