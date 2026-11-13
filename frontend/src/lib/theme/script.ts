import {
  DARK_QUERY,
  DEFAULT_PREF,
  LEGACY_KEY,
  PAIRS,
  SIDEBAR_KEY,
  STORAGE_KEY,
  THEMES,
  THEME_PREFS,
} from "./themes";

/**
 * The inline <head> script that applies the theme before the first paint,
 * so nobody sees a flash of the wrong theme. It mirrors readPref,
 * resolveTheme and applyTheme in resolve.ts (script.test.ts checks they
 * agree) and also restores the sidebar as collapsed or expanded.
 *
 * Its content is static, so when the strict CSP (D-23) lands, script-src
 * can allow it by its sha256 hash.
 */
export function buildThemeScript(): string {
  const darkThemes = Object.values(THEMES)
    .filter((t) => t.mode === "dark")
    .map((t) => t.id);
  const c = JSON.stringify({
    k: STORAGE_KEY,
    l: LEGACY_KEY,
    s: SIDEBAR_KEY,
    d: DEFAULT_PREF,
    v: THEME_PREFS,
    p: PAIRS,
    dk: darkThemes,
    q: DARK_QUERY,
  });
  return (
    `(function(){var c=${c};var h=document.documentElement;var p=null,o=null,sb=null;` +
    // Storage can throw (blocked cookies, sandboxed frames): keep going.
    `try{var ls=window.localStorage;p=ls.getItem(c.k);o=ls.getItem(c.l);sb=ls.getItem(c.s)}catch(e){}` +
    `if(c.v.indexOf(p)<0){p=o==="dark"?"graphite":o==="light"?"paper":c.d}` +
    `var t=p;if(p.indexOf("system:")===0){var sd=false;try{sd=window.matchMedia(c.q).matches}catch(e){}` +
    `var pr=c.p[p.slice(7)];t=sd?pr.dark:pr.light}` +
    `var dk=c.dk.indexOf(t)>=0;h.setAttribute("data-theme",t);` +
    `if(dk){h.classList.add("dark")}else{h.classList.remove("dark")}h.style.colorScheme=dk?"dark":"light";` +
    `if(sb==="true"){h.setAttribute("data-sidebar","collapsed")}else if(sb==="expanded"){h.setAttribute("data-sidebar","expanded")}})();`
  );
}

export const THEME_SCRIPT = buildThemeScript();
