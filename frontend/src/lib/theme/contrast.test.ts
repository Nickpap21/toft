import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { contrast, declarationsFor, over, readStyle, resolveToken } from "@/test/css-tokens";
import { THEME_IDS, THEMES } from "./themes";

const tokensCss = readStyle("tokens.css");

function token(theme: string) {
  const vars = declarationsFor(tokensCss, theme);
  return (name: string) => resolveToken(vars, name);
}

describe.each(THEME_IDS)("%s meets WCAG 2.1 AA", (theme) => {
  const t = token(theme);
  const ground = t("--ground");
  const surface = t("--surface");
  const raised = t("--raised");

  it("text, muted and link colours reach 4.5:1", () => {
    for (const bg of [ground, surface, raised]) {
      expect(contrast(t("--text"), bg)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(t("--muted"), bg)).toBeGreaterThanOrEqual(4.5);
    }
    expect(contrast(t("--link"), surface)).toBeGreaterThanOrEqual(4.5);
    expect(contrast(t("--link"), ground)).toBeGreaterThanOrEqual(4.5);
  });

  it("text on the accent, the selected tint, the soft accent and the danger fill reaches 4.5:1", () => {
    expect(contrast(t("--on-accent"), t("--accent"))).toBeGreaterThanOrEqual(4.5);
    expect(contrast(t("--text"), t("--selected"))).toBeGreaterThanOrEqual(4.5);
    expect(contrast(t("--selected-muted"), t("--selected"))).toBeGreaterThanOrEqual(4.5);
    expect(contrast(t("--text"), t("--accent-soft"))).toBeGreaterThanOrEqual(4.5);
    expect(contrast(t("--on-danger"), t("--danger"))).toBeGreaterThanOrEqual(4.5);
  });

  it.each(["running", "building", "pending", "failed"])("%s chip text reaches 4.5:1", (status) => {
    for (const bg of [ground, surface, raised]) {
      const chip = over(t(`--status-${status}-soft`), bg);
      expect(contrast(t(`--status-${status}-fg`), chip)).toBeGreaterThanOrEqual(4.5);
    }
  });

  it.each(["amber", "violet", "blue", "teal", "gray", "protected"])("%s environment chip text reaches 4.5:1 and its swatch 3:1", (color) => {
    for (const bg of [ground, surface, raised]) {
      const chip = over(t(`--env-${color}-soft`), bg);
      expect(contrast(t(`--env-${color}-fg`), chip)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(t(`--env-${color}`), bg)).toBeGreaterThanOrEqual(3);
    }
  });

  it.each(["gray", "blue", "red", "green", "amber", "violet", "teal"])("%s issue-type tile reaches 3:1 against its glyph and the page", (color) => {
    expect(contrast(t("--ground"), t(`--type-${color}`))).toBeGreaterThanOrEqual(3);
    expect(contrast(t(`--type-${color}`), surface)).toBeGreaterThanOrEqual(3);
  });

  it("focus ring, strong borders and status dots reach 3:1", () => {
    for (const bg of [ground, surface]) {
      expect(contrast(t("--focus-ring"), bg)).toBeGreaterThanOrEqual(3);
      expect(contrast(t("--border-strong"), bg)).toBeGreaterThanOrEqual(3);
      for (const status of ["running", "building", "pending", "failed"]) {
        expect(contrast(t(`--status-${status}`), bg)).toBeGreaterThanOrEqual(3);
      }
    }
  });
});

it("Contrast uses the yellow focus ring", () => {
  const t = token("contrast");
  expect(t("--focus-ring")).toEqual(token("contrast")("--focus-ring"));
  const { r, g, b } = t("--focus-ring");
  expect([r, g, b].map((c) => Math.round(c * 255))).toEqual([0xff, 0xd6, 0x0a]);
});

describe("DESIGN.md Themes table", () => {
  const design = readFileSync(join(__dirname, "..", "..", "..", "..", "DESIGN.md"), "utf8");
  const columns = ["--ground", "--surface", "--raised", "--border", "--text", "--muted", "--accent", "--link",
    "--status-running", "--status-building", "--status-pending", "--status-failed"];

  it.each(THEME_IDS)("matches tokens.css for %s", (theme) => {
    const label = THEMES[theme].label;
    const row = design.split("\n").find((line) => line.startsWith(`| ${label} |`));
    expect(row, `DESIGN.md has a row for ${label}`).toBeDefined();
    const cells = row!.split("|").slice(1, -1).map((c) => c.trim());
    expect(cells[1].toLowerCase()).toBe(THEMES[theme].mode);
    const hexes = cells.slice(3).map((c) => /#[0-9A-Fa-f]{6}/.exec(c)?.[0].toUpperCase());
    const vars = declarationsFor(tokensCss, theme);
    columns.forEach((name, i) => expect(vars.get(name)?.toUpperCase(), `${label} ${name}`).toBe(hexes[i]));
    const onAccent = /\((white|black)\)/.exec(cells[9])?.[1];
    expect(vars.get("--on-accent")?.toUpperCase()).toBe(onAccent === "white" ? "#FFFFFF" : "#000000");
  });
});
