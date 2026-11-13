import { describe, expect, it } from "vitest";
import { applyTheme, migrateLegacy, otherSide, parsePref, readPref, resolveTheme } from "./resolve";
import { DEFAULT_PREF, LEGACY_KEY, STORAGE_KEY, THEME_IDS, THEME_PREFS, type ThemePref } from "./themes";

function storage(values: Record<string, string>) {
  return { getItem: (k: string) => values[k] ?? null };
}

describe("parsePref", () => {
  it("accepts the eight preferences", () => {
    expect(THEME_PREFS).toHaveLength(8);
    for (const p of THEME_PREFS) expect(parsePref(p)).toBe(p);
  });

  it.each([null, undefined, "", "dark", "light", "system", "system:ink-paper", "Graphite"])("rejects %s", (raw) => {
    expect(parsePref(raw)).toBeNull();
  });
});

describe("migrateLegacy", () => {
  it("keeps an explicit old choice fixed", () => {
    expect(migrateLegacy("dark")).toBe("graphite");
    expect(migrateLegacy("light")).toBe("paper");
  });
  it.each([null, "x", "system"])("ignores %s", (raw) => expect(migrateLegacy(raw)).toBeNull());
});

describe("readPref", () => {
  it("prefers the new key over the legacy one", () => {
    expect(readPref(storage({ [STORAGE_KEY]: "ink", [LEGACY_KEY]: "light" }))).toBe("ink");
  });
  it("falls back to the legacy key, then the default", () => {
    expect(readPref(storage({ [LEGACY_KEY]: "dark" }))).toBe("graphite");
    expect(readPref(storage({ [STORAGE_KEY]: "junk", [LEGACY_KEY]: "light" }))).toBe("paper");
    expect(readPref(storage({}))).toBe(DEFAULT_PREF);
    expect(readPref(null)).toBe(DEFAULT_PREF);
  });
  it("survives storage that throws", () => {
    const broken = {
      getItem: () => {
        throw new Error("SecurityError");
      },
    };
    expect(readPref(broken)).toBe(DEFAULT_PREF);
  });
});

describe("resolveTheme", () => {
  const cases: [ThemePref, boolean, string][] = [
    ["system:graphite-paper", true, "graphite"],
    ["system:graphite-paper", false, "paper"],
    ["system:mono-daylight", true, "mono"],
    ["system:mono-daylight", false, "daylight"],
  ];
  it.each(cases)("%s with systemDark=%s → %s", (pref, dark, expected) => {
    expect(resolveTheme(pref, dark)).toBe(expected);
  });
  it("ignores the OS for a fixed theme", () => {
    for (const id of THEME_IDS) {
      expect(resolveTheme(id, true)).toBe(id);
      expect(resolveTheme(id, false)).toBe(id);
    }
  });
});

describe("otherSide", () => {
  it("crosses the pair, and sends dark-only themes to Paper", () => {
    expect(otherSide("graphite")).toBe("paper");
    expect(otherSide("paper")).toBe("graphite");
    expect(otherSide("mono")).toBe("daylight");
    expect(otherSide("daylight")).toBe("mono");
    expect(otherSide("ink")).toBe("paper");
    expect(otherSide("contrast")).toBe("paper");
  });
});

describe("applyTheme", () => {
  it("sets data-theme, the dark class and color-scheme", () => {
    const el = document.createElement("html");
    applyTheme(el, "contrast");
    expect(el.dataset.theme).toBe("contrast");
    expect(el.classList.contains("dark")).toBe(true);
    expect(el.style.colorScheme).toBe("dark");
    applyTheme(el, "daylight");
    expect(el.dataset.theme).toBe("daylight");
    expect(el.classList.contains("dark")).toBe(false);
    expect(el.style.colorScheme).toBe("light");
  });
});
