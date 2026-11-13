/**
 * The six themes from DESIGN.md → Themes (D-03, D-04). Token values live in
 * src/styles/tokens.css; this file holds what scripts and components need
 * to choose between them.
 */

export const THEME_IDS = ["graphite", "mono", "ink", "paper", "daylight", "contrast"] as const;
export type ThemeId = (typeof THEME_IDS)[number];

export type ThemeMode = "dark" | "light";

/** A theme pair: the OS setting picks its dark or light side. */
export const PAIRS = {
  "graphite-paper": { dark: "graphite", light: "paper" },
  "mono-daylight": { dark: "mono", light: "daylight" },
} as const satisfies Record<string, { dark: ThemeId; light: ThemeId }>;
export type PairId = keyof typeof PAIRS;

/** What the user picked: one fixed theme, or a pair that follows the OS. */
export type ThemePref = ThemeId | `system:${PairId}`;

export interface ThemeMeta {
  id: ThemeId;
  label: string;
  mode: ThemeMode;
  description: string;
}

export const THEMES: Record<ThemeId, ThemeMeta> = {
  graphite: { id: "graphite", label: "Graphite", mode: "dark", description: "Neutral graphite with an indigo accent." },
  paper: { id: "paper", label: "Paper", mode: "light", description: "Warm off-white with a deep indigo accent. Suits long reading." },
  mono: { id: "mono", label: "Mono", mode: "dark", description: "No hue in the interface. Colour appears only for status." },
  daylight: { id: "daylight", label: "Daylight", mode: "light", description: "Cool white with black main buttons. The light side of Mono." },
  ink: { id: "ink", label: "Ink", mode: "dark", description: "Cooler blue-black with a violet accent." },
  contrast: { id: "contrast", label: "Contrast", mode: "dark", description: "The accessibility theme: highest-contrast text, stronger borders and a yellow focus ring." },
};

export const PAIR_LABELS: Record<PairId, { label: string; description: string }> = {
  "graphite-paper": { label: "Graphite and Paper", description: "Graphite when your system is dark, Paper when it is light." },
  "mono-daylight": { label: "Mono and Daylight", description: "Mono when your system is dark, Daylight when it is light." },
};

/** Every valid preference, in the order Settings → Appearance lists them. */
export const THEME_PREFS: readonly ThemePref[] = [
  "system:graphite-paper",
  "system:mono-daylight",
  "graphite",
  "paper",
  "mono",
  "daylight",
  "ink",
  "contrast",
];

export const DEFAULT_PREF: ThemePref = "system:graphite-paper";

/** localStorage keys. The paas_ prefix matches the other keys; renaming them
 *  is the separate Toft rename (D-32). */
export const STORAGE_KEY = "paas_theme";
/** The old light/dark toggle's key. Read only, so rolling back still works. */
export const LEGACY_KEY = "theme";
export const SIDEBAR_KEY = "sidebar_collapsed";

export const DARK_QUERY = "(prefers-color-scheme: dark)";

export function isDarkTheme(id: ThemeId): boolean {
  return THEMES[id].mode === "dark";
}

export function prefLabel(pref: ThemePref): string {
  return pref.startsWith("system:")
    ? `${PAIR_LABELS[pref.slice(7) as PairId].label} (follow system)`
    : THEMES[pref as ThemeId].label;
}
