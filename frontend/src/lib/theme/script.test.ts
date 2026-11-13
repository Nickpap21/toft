import { describe, expect, it, vi } from "vitest";
import { setSystemDark } from "@/test/setup";
import { readPref, resolveTheme } from "./resolve";
import { buildThemeScript } from "./script";
import { isDarkTheme, LEGACY_KEY, SIDEBAR_KEY, STORAGE_KEY, THEME_PREFS } from "./themes";

function run() {
  // The same string the layout inlines, executed as the browser would.
  new Function(buildThemeScript())();
  return document.documentElement;
}

describe("theme head script", () => {
  for (const pref of THEME_PREFS) {
    for (const dark of [true, false]) {
      it(`applies ${pref} with the OS ${dark ? "dark" : "light"}, as resolveTheme does`, () => {
        localStorage.setItem(STORAGE_KEY, pref);
        setSystemDark(dark);
        const html = run();
        const expected = resolveTheme(readPref(localStorage), dark);
        expect(html.dataset.theme).toBe(expected);
        expect(html.classList.contains("dark")).toBe(isDarkTheme(expected));
        expect(html.style.colorScheme).toBe(isDarkTheme(expected) ? "dark" : "light");
      });
    }
  }

  const states: [string, Record<string, string>][] = [
    ["nothing stored", {}],
    ["legacy dark", { [LEGACY_KEY]: "dark" }],
    ["legacy light", { [LEGACY_KEY]: "light" }],
    ["legacy junk", { [LEGACY_KEY]: "sepia" }],
    ["junk pref over legacy", { [STORAGE_KEY]: "<script>", [LEGACY_KEY]: "light" }],
    ["pref over legacy", { [STORAGE_KEY]: "mono", [LEGACY_KEY]: "light" }],
  ];
  for (const [label, stored] of states) {
    for (const dark of [true, false]) {
      it(`agrees with resolveTheme for ${label}, OS ${dark ? "dark" : "light"}`, () => {
        for (const [k, v] of Object.entries(stored)) localStorage.setItem(k, v);
        setSystemDark(dark);
        expect(run().dataset.theme).toBe(resolveTheme(readPref(localStorage), dark));
      });
    }
  }

  it("migrates the legacy toggle", () => {
    localStorage.setItem(LEGACY_KEY, "dark");
    expect(run().dataset.theme).toBe("graphite");
    localStorage.setItem(LEGACY_KEY, "light");
    setSystemDark(true);
    expect(run().dataset.theme).toBe("paper");
  });

  it("follows the OS with Graphite and Paper when nothing is stored", () => {
    setSystemDark(true);
    expect(run().dataset.theme).toBe("graphite");
    setSystemDark(false);
    expect(run().dataset.theme).toBe("paper");
  });

  it("ignores junk and falls back to the legacy key", () => {
    localStorage.setItem(STORAGE_KEY, "<script>");
    localStorage.setItem(LEGACY_KEY, "dark");
    expect(run().dataset.theme).toBe("graphite");
  });

  it("restores the collapsed sidebar", () => {
    localStorage.setItem(SIDEBAR_KEY, "true");
    expect(run().dataset.sidebar).toBe("collapsed");
  });

  it("still resolves from the OS when storage throws", () => {
    const spy = vi.spyOn(window, "localStorage", "get").mockImplementation(() => {
      throw new Error("SecurityError");
    });
    try {
      setSystemDark(true);
      expect(run().dataset.theme).toBe("graphite");
      setSystemDark(false);
      expect(run().dataset.theme).toBe("paper");
    } finally {
      spy.mockRestore();
    }
  });
});
