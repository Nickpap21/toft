"use client";

import { useEffect } from "react";

/**
 * A link to #anchor lands on its section once the page has drawn. The
 * browser scrolls as soon as the element exists, before the cards above it
 * have loaded, and ends hundreds of pixels off (/settings#cyc-h: 1,640 px).
 * Each change under <main> lands it again until the person scrolls, clicks
 * or types.
 */
// ponytail: gives up after 10 s; a page slower than that stays where the browser left it.
export function useHashLanding(pathname: string) {
  useEffect(() => {
    const main = document.getElementById("main");
    const id = decodeURIComponent(window.location.hash.slice(1));
    if (!main || !id || id === "main") return;
    let frame = 0;
    const land = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => document.getElementById(id)?.scrollIntoView({ block: "start" }));
    };
    const observer = new MutationObserver(land);
    const events = ["wheel", "touchstart", "keydown", "pointerdown"] as const;
    const stop = () => {
      observer.disconnect();
      cancelAnimationFrame(frame);
      clearTimeout(timer);
      for (const e of events) window.removeEventListener(e, stop, true);
    };
    const timer = setTimeout(stop, 10_000);
    for (const e of events) window.addEventListener(e, stop, { capture: true, passive: true });
    observer.observe(main, { childList: true, subtree: true });
    land();
    return stop;
  }, [pathname]);
}
