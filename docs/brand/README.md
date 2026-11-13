# Toft logo

**Idea:** the f and t share one roof. The f's hook runs on into the t, so the name holds a small house: a
window above the crossbar, a doorway below it. A toft is the plot a house stands on (D-24); the house is
inside the name rather than drawn beside it.

The wordmark is the logo. The `ft` ligature on its own is the icon. There is no symbol-plus-wordmark lockup:
the ligature already sits inside the wordmark, so pairing them would say it twice.

## Files

| File | Use |
|---|---|
| `toft-wordmark.svg` | The logo. Headers, README, website, slides |
| `toft-symbol.svg` | The ligature at 48 px and above (avatars, stickers, social) |
| `toft-symbol-small.svg` | The ligature below 48 px: heavier stems, taller window, so the window stays open at 16 px |
| `wordmark/` | Black, white and indigo one-colour versions, plus 1200 px PNGs |
| `symbol/` | One-colour versions, app icon, favicon (`.svg`, `.ico`, 16/32/48 PNG), PWA icons, `site.webmanifest`, `head-snippet.html` |
| `board/` | Presentation slides with mockups |

Every file is outlined paths: no font is needed to render it. Don't retype the wordmark in Geist or any other
font; the letters are drawn, not set.

## Construction

Canvas height 256. Stems 28, crossbars 26 (horizontals thinned for optical weight), x-height 96, baseline 200.
One radius family: 48 outer, 20 inner. The o overshoots the x-height and baseline by 2. The small cut uses
stems 40 and a 52-unit window.

## Clear space and minimum size

Standing alone (README, slides, a website), keep clear space equal to the height of the o on every side; it
scales with the logo. In the panel's own chrome the boards set the spacing instead (10 px beside the wordmark in
the signed-out header).

| Version | Screen | Print |
|---|---|---|
| Wordmark | 20 px tall (49 px wide) | 20 mm wide |
| Symbol | 48 px (`toft-symbol.svg`) | 12 mm |
| Small cut | 16 px (`toft-symbol-small.svg`, favicon) | 5 mm |

## Colour

The panel's own palette (`DESIGN.md`): no new colours.

| Name | HEX | RGB | Role |
|---|---|---|---|
| Ink | `#1C1B19` | 28 27 25 | The logo on light grounds (Paper's text colour) |
| Paper | `#F6F5F1` | 246 245 241 | The logo on dark grounds |
| Indigo | `#4F46E5` | 79 70 229 | The app-icon tile; the one-colour brand version (Graphite's accent) |

CMYK and Pantone (the codes a print shop needs) are not set yet. Screens only need the HEX values; match the print
codes from a printed proof before the first sticker or shirt, not from a formula.

Approved pairs (WCAG contrast, measured):

| Logo | Ground | Contrast |
|---|---|---|
| Ink | Paper `#F6F5F1` or white | 15.8:1 |
| Paper or white | Graphite `#0C0D10` | 17.8:1 |
| White | Indigo `#4F46E5` | 6.3:1 |
| Indigo | Paper `#F6F5F1` | 5.8:1 |

Don't put indigo on a dark ground (3.1:1 on Graphite): use Paper.

## Typography beside the logo

Geist for text, Geist Mono for commands and values (SIL Open Font License). Sentence case; the wordmark is
always lowercase.

## Don't

Stretch, squash, rotate or slant it. Recolour it outside the table above. Add shadows, outlines, gradients or
glows. Separate the f from the t or close the window. Set "toft" in a font instead of using the file. Place it
on a busy photo without a solid ground.

## Open

- CMYK and Pantone values, from a printed proof, before the first print order.
