import {
  DEFAULT_PREF,
  LEGACY_KEY,
  PAIRS,
  STORAGE_KEY,
  THEME_IDS,
  THEME_PREFS,
  isDarkTheme,
  type PairId,
  type ThemeId,
  type ThemePref,
} from "./themes";

/** A stored preference, or null if it is missing or not one we know. */
export function parsePref(raw: string | null | undefined): ThemePref | null {
  return raw != null && (THEME_PREFS as readonly string[]).includes(raw) ? (raw as ThemePref) : null;
}

/** The old toggle stored "dark" or "light"; an explicit choice stays fixed. */
export function migrateLegacy(raw: string | null | undefined): ThemeId | null {
  if (raw === "dark") return "graphite";
  if (raw === "light") return "paper";
  return null;
}

type ReadableStorage = Pick<Storage, "getItem">;

/** The saved preference, else the migrated legacy toggle, else the default.
 *  Never throws: storage can be blocked or broken. */
export function readPref(storage: ReadableStorage | null | undefined): ThemePref {
  try {
    if (!storage) return DEFAULT_PREF;
    return parsePref(storage.getItem(STORAGE_KEY)) ?? migrateLegacy(storage.getItem(LEGACY_KEY)) ?? DEFAULT_PREF;
  } catch {
    return DEFAULT_PREF;
  }
}

export function resolveTheme(pref: ThemePref, systemDark: boolean): ThemeId {
  if (pref.startsWith("system:")) {
    const pair = PAIRS[pref.slice(7) as PairId];
    return systemDark ? pair.dark : pair.light;
  }
  return pref as ThemeId;
}

/** The theme on the other side of the light/dark divide. Ink and Contrast
 *  have no light partner, so they go to the default light theme. */
export function otherSide(theme: ThemeId): ThemeId {
  switch (theme) {
    case "graphite":
      return "paper";
    case "paper":
      return "graphite";
    case "mono":
      return "daylight";
    case "daylight":
      return "mono";
    default:
      return "paper";
  }
}

export function isThemeId(value: string | null | undefined): value is ThemeId {
  return value != null && (THEME_IDS as readonly string[]).includes(value);
}

/** Puts a theme on an element (normally <html>). The .dark class keeps the
 *  legacy `dark:` classes choosing the right side until R2 removes them. */
export function applyTheme(el: HTMLElement, theme: ThemeId): void {
  const dark = isDarkTheme(theme);
  el.dataset.theme = theme;
  el.classList.toggle("dark", dark);
  el.style.colorScheme = dark ? "dark" : "light";
}
