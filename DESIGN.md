# DESIGN.md — Toft (formerly PaaS Control Panel)

### Aesthetic Direction
**Primary reference**: Coolify — functional, OSS-credible, immediately familiar to the self-hosting audience. Not trying to look like a funded startup.

**Anti-references (hard blocks)**:
- Generic SaaS dashboard — no purple/indigo gradients, no hero metric cards, no illustration-based empty states
- Heavy enterprise UI — no AWS Console density, no modal-on-modal, no deep sidebar trees
- Consumer/playful — no rounded blobs, no large emoji, no bright marketing palette
- Retro hacker aesthetic — no terminal-green-on-black, no cyberpunk decoration

**Theme**: Six themes, picked per user in Settings → Appearance (see [Themes](#themes)). Graphite/Paper and Mono/Daylight come in dark/light pairs, and the OS setting chooses which side; Ink and Contrast are dark-only. Dark is the "home mode" for infrastructure work. Default pair: Graphite (dark) and Paper (light), decided 2026-10-09 (`private/DECISIONS.md` D-04).

**Typography**: Clean, readable, technical. Geist for UI text, Geist Mono (`font-mono`) for all infrastructure values — IPs, ports, commands, IDs, container names.

**Color**: One accent per theme (indigo in the default pair), used only for the main action on a screen, links and the current selection. All other color is semantic only:
- `green` = running / success
- `red` = error / failed / danger
- `amber` = warning / pending / caution; an issue In progress
- `blue` = building / in-progress (deploys); an issue In review
- No decorative color. Color earns its presence by communicating state.
- Status colors keep these meanings in every theme. Each theme tunes them for contrast on its background, and they also differ in lightness.
- Color is never the only signal: every state also has a label or an icon.
- **Environment colour** is the one other semantic use (D-55): it says where an action lands. An environment's chip is mono text with 4 px corners and no dot, so it never reads as a status chip. Amber and blue reuse the status hues of each theme, gray is `--muted`, violet and teal are each theme's own (`--env-violet`, `--env-teal` in `tokens.css`). **Red is not an environment colour**: it means failing (D-142). **A protected environment draws neutral** whatever its colour: text on a faint fill with a faint ring and the lock, in the chip, the switcher pill and the 2 px line under the project bar. The lock says careful; the colour shows again if protection is lifted.

### Themes

Decided 2026-10-09 (`private/DECISIONS.md` D-03).

| Theme | Mode | Pair | Ground | Surface | Raised | Border | Text | Muted | Accent (text on it) | Link | Running | Building | Pending | Failed |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| Graphite | Dark | Paper | `#0C0D10` | `#131418` | `#1A1C21` | `#2A2D33` | `#EDEEF0` | `#868A93` | `#4F46E5` (white) | `#A9A6F2` | `#4CC38A` | `#6CB2F7` | `#E8B04B` | `#F2727F` |
| Mono | Dark | Daylight | `#0A0A0A` | `#111111` | `#1A1A1A` | `#2B2B2B` | `#EDEDED` | `#8F8F8F` | `#EDEDED` (black) | `#FFFFFF` | `#3DD68C` | `#52A8FF` | `#F5B83D` | `#FF6166` |
| Ink | Dark | — | `#0B0E14` | `#10141C` | `#171C26` | `#252C3A` | `#E6EAF2` | `#8892A6` | `#6E56CF` (white) | `#B7A8F7` | `#3FCF8E` | `#5EB1FF` | `#F2B53F` | `#F47174` |
| Paper | Light | Graphite | `#F6F5F1` | `#FFFFFF` | `#EFEDE7` | `#E0DDD5` | `#1C1B19` | `#67645E` | `#4338CA` (white) | `#4338CA` | `#15803D` | `#1D4ED8` | `#B45309` | `#B91C1C` |
| Daylight | Light | Mono | `#F4F5F7` | `#FFFFFF` | `#ECEEF2` | `#DADDE3` | `#111318` | `#5C6370` | `#111318` (white) | `#111318` | `#0F7B4B` | `#1F5FD6` | `#A15C07` | `#C2262E` |
| Contrast | Dark | — | `#000000` | `#0A0A0A` | `#161616` | `#6B6B6B` | `#FFFFFF` | `#CFCFCF` | `#A5A6FF` (black) | `#C3C4FF` | `#5EF2A0` | `#8CCBFF` | `#FFD166` | `#FF9494` |

- Implement these as CSS custom properties: one set per theme, selected by an attribute on `<html>`. Components use the token names (`--ground`, `--surface`, `--accent`, `--status-running`, …), never raw hex values.
- Contrast is the accessibility theme: highest-contrast text, a 2px yellow (`#FFD60A`) focus ring and stronger borders.
- Status chip background = status color at 14% opacity on dark themes, 10% on light themes.
- **Light-theme chip text** is 85% status color mixed with 15% text color (oklab). At 10% opacity, Paper's running and pending chips measure 4.39:1 with the raw status color; the mix brings every light chip above 4.5:1. Dark themes use the status color as is. (Amended 2026-10-10, D-32.)
- **Focus ring** = `--link`, 2px, offset 2px; Contrast overrides it with `#FFD60A`. Graphite's accent `#4F46E5` is only 2.93:1 on its surface, so accent alone never marks focus or the current selection: rings and selection markers use `--link`. (D-32.)
- **Destructive buttons** are solid `--danger` (the theme's failed color). Their text is `--ground` (dark) on dark themes and white on light themes. Never put white text on a status color in a dark theme: it measures 1.4–2.9:1. (D-32.)

Derived tokens, computed per theme in `frontend/src/styles/tokens.css`:

| Token | Value | Use |
|---|---|---|
| `--border-strong` | `--muted` 70% + `--border` | Input and control edges that must reach 3:1 |
| `--selected` | `--accent` at 20% over `--surface`, every theme | The one selected tint: the active nav item, the index's current section, selected and current rows |
| `--selected-muted` | `--text` 75% over `--selected` | Muted text inside a selected item (`--muted` falls under 4.5:1 there on Paper, Daylight and Mono) |
| `--accent-soft` | `--accent` at 16% (dark) or 10% (light) over `--surface` | Chips, chosen options, menu highlights |
| `--focus-ring` | `--link` (Contrast: `#FFD60A`) | Focus outlines |
| `--status-*-soft` | status color at the chip opacity | Chip backgrounds |
| `--status-*-fg` | status color (dark), 85% status + 15% text (light) | Chip text |
| `--danger` / `--on-danger` | `--status-failed` / `--ground` (dark) or white (light) | Destructive buttons |
| `--on-accent` | white or black per theme (the table's "text on it") | Text on the accent, and a switch's knob in both states, so Mono's light accent gets a dark knob |
| `--scrim` | black, used at 50% (dialogs, phone navigation) or 20% (drawers); on Paper and Daylight black at 60%, so 30% and 12% | Behind overlays. Dialogs also blur the page 8px (`backdrop-blur-sm`), drawers 1px; the phone navigation doesn't blur |
| `--env-<colour>`, `-soft`, `-fg` | amber, blue: the status hues; gray: `--muted`; violet and teal per theme; soft and fg as the status chips, gray's fg leaning 40% to text | Environment chips, the switcher, the Services filter swatches (D-55) |
| `--env-protected`, `-soft`, `-fg`, `-ring` | the line halfway from `--muted` to text; text at 9% (fill) and 16% (ring); fg is text | A protected environment's chip, switcher pill, filter swatch and line, whatever its colour (D-142) |
| `--type-<colour>` | gray: `--muted`; blue, red, green, amber: the status hues; violet and teal: the environments' | Issue types' tiles, the glyph in `--ground`; their own tokens so the environment colours can change without them (Bug is red, which no environment is) |

Raw colours outside `tokens.css` fail `npm run lint:tokens` (`frontend/scripts/token-check.mjs`). Allowed exceptions, in `frontend/scripts/token-check.json`: the xterm theme in `pty-terminal.tsx` (the service drawer imports it), because xterm.js takes a theme object of colour strings, not CSS variables; and tests.

`frontend/src/lib/theme/contrast.test.ts` checks the table above against `tokens.css` and checks every pairing reaches WCAG 2.1 AA in all six themes.

### Logo

The drawn `toft` wordmark is the logo and the `ft` ligature the mark (D-119); `docs/brand/README.md` has the files and rules. In code: `ToftWordmark` and `ToftMark` (`components/shell/toft-mark.tsx`), both in the current text colour. Never set "toft" in Geist in its place.

### Typography

Geist and Geist Mono only (`font-sans`, `font-mono`). Mono for every infrastructure value: IPs, ports, commands, IDs, hashes, container names, paths. The interface is dense, so 13 px is the default, not 16. The boards and the code use one scale (D-109); `frontend/src/lib/theme/scales.test.ts` fails on any other size.

| Size | Class | Weight | Use |
|---|---|---|---|
| 10 | `text-[10px]` | 600 | Initials in avatars and project letter tiles, nothing else |
| 11 | `text-[11px]` | 400–600 | Column headers, overlines, key hints, dense mono metadata |
| 12 | `text-xs` | 400–500 | Secondary text: timestamps, chips, captions, mono values in tables |
| 13 | `text-[13px]` | 400–500 | Default: table rows, controls, form labels, descriptions |
| 14 | `text-sm` | 400 | The page's base size and longer body text |
| 15 | `text-[15px]` | 600 | Card and section titles |
| 17 | `text-[17px]` | 600 | Dialog titles; the title of an issue, pull request or deployment |
| 20 | `text-xl` | 600 | Page titles inside the shell |
| 22 | `text-[22px]` | 500–600 | Figures: a metric's current value (mono, or tabular in a row of figures) |
| 26 | `text-[26px]` | 600, −0.015em | A page with no shell: sign-in, set-up, invites |

- **Documents** (rendered Markdown, the docs reader and editor, `.toft-md` and `.toft-doc` in `frontend/src/styles/code.css`) have their own reading scale: body 15/24, headings 24, 19, 17, 16, 15, 14, a document title 28.
- Titles from 17 to 22 px tighten by −0.01em, 26 px and a document title by −0.015em. Column headers and overlines are sentence case, not uppercase or letter-spaced.
- The reference boards (Brand, Palettes, Decisions, Topology) are outside the scale: they present the system, they aren't screens.

### Space, shape, density

- **Spacing** runs in 2 px steps to 16 (2, 4, 6, 8, 10, 12, 14, 16), then 20, 24, 32, 40, 56. Rows are dense: 6–10 px vertical padding in tables and lists, 16–20 px inside cards, 24–32 px between page sections. The boards still have odd values (3, 5, 7, 9, 18 px; about 430 across the boards, mostly gaps between dots and swatches and icon-to-label offsets) used for optical alignment. They stay as drawn; new screens don't add more.
- **Radius** follows the element's role (D-109; the test above checks it):

| Radius | Class | Use |
|---|---|---|
| 2 | `rounded-xs` | Meters and stacked bars, legend swatches, small marks |
| 4 | `rounded-sm` / `rounded` | Chips, badges, key hints, environment chips (D-55) |
| 6 | `rounded-md` | Controls 30 px tall or less |
| 8 | `rounded-lg` | Buttons and inputs 32 px tall or more, code blocks |
| 10 | `rounded-[10px]` | Cards, sections, banners, tables |
| 12 | `rounded-xl` | Dialogs, popovers, the ⌘K palette: whatever floats |
| full | `rounded-full` | Status dots, avatars, pills, switch tracks |

- **Borders** carry the structure: 1 px `--border` between regions, `--border-strong` on inputs and controls (3:1). No shadow on anything that sits on the page.
- **Elevation** has two levels, only for what floats over the page: `shadow-dialog` (dialogs, drawers, sheets, the palette) and `shadow-menu` (menus, popovers, listboxes). Dark themes use one deep drop (the boards' values); light themes use small stacked shadows (`--elevation-*` in `tokens.css`). Depth on the page itself comes from the ground → surface → raised steps.

### Motion

Decided in D-50; the keyframes are in `frontend/src/app/globals.css`.

- **What moves:** only what opens or closes over the page. Overlays fade in 150 ms; dialogs and the palette fade and scale from 0.97 in 160 ms and leave in 120 ms; menus fade and slide 4 px from their trigger in 120 ms and leave in 100 ms; the phone navigation slides in 200 ms and out in 160 ms; toasts keep Sonner's; an import's indeterminate bar slides.
- **Easing:** entering `cubic-bezier(0.16, 1, 0.3, 1)` (fast start, soft landing); leaving `ease-in`, and always quicker than entering.
- **Never animates:** a theme switch (`data-theme-switching` turns transitions off), page navigation, table rows, status changes, numbers, layout. A status change is shown, not performed.
- **Reduced motion** turns all of it off, the progress bar included.
- Properties: `opacity`, `scale` and `translate` only, so nothing re-lays out.

### Data and numbers

Toft is a dashboard: numbers and charts carry most of the meaning. Rules adapted from the dataviz method (see References):

- **Figures in columns** (tables, capacity rows, a log's timestamps) use `tabular-nums` so digits line up. A figure standing alone keeps proportional digits.
- **No hero numbers and no metric cards** (anti-references above). Home's health is one line; a metric's current value is 22 px at most.
- **Status colours are for state only.** They never mark a chart series ("series 2 in green" reads as "healthy"). A meter's fill may carry severity (running → pending → failed as it fills); its track is a quiet step off the surface, so the empty part still reads as part of the bar.
- **Series colours:** the viewer's own share takes the accent family and everything else neutral steps, as Capacity's stacked bars do (own workspace, other workspaces, system). Lines use the neutral inks (`--text` at 60%, `--link`, `--muted`), told apart by line style too. Toft has no categorical palette yet: a chart that needs four or more series gets one first, checked with the dataviz validator against every theme's surface.
- **Marks:** lines 2 px, bars at most 24 px thick, meters 10 px tall with 2 px corners, gridlines 1 px solid `--border`, one y-axis per chart (two measures are two charts).
- **Labels** use text tokens, never the series colour. Two or more series have a legend, and up to four are also labelled at the line's end.
- **Every chart** has an accessible name that states the reading ("Scope 34 points, 21 done, 3 days left"), a hover tooltip with the exact value, and the same numbers available as text or a table.

### Interface patterns (2026-10 redesign)

Screens are on the canvas page "Screens" (`private/design/canvas/`, renders in `private/design/canvas/png/`). The shell (sidebar, ⌘K) and the themes are built (R1, D-30, D-32); the screens themselves are rebuilt to these designs in R2. Issues, Code and Docs come in R3 (`private/DECISIONS.md` D-05 to D-08). **The boards are the visual target for the final product (D-36):** build each screen to look like its board, check it against the PNG, and design a board before building a screen that has none.

- **Shell**: a workspace sidebar (Home, Inbox, My issues, projects with a health signal, Infrastructure, Settings). Inside a project the sidebar switches to the project (D-136): "‹ workspace" with the Inbox count, the project and environment switchers, ⌘K, then Overview, Code, Issues, Docs, Services, Environments, Settings. Built (7g step 0): the project bar is gone; the canvas page "Rework" is the target.
- **Layout (D-136)**: content left-aligned next to the sidebar, capped at 1600 px; a main column up to 1040 px and a right column that takes the rest. The sidebar is full from 1200 px, icons only from 768 px, a drawer below. A long settings page is one scroll, with a sticky "On this page" index on the right from 1200 px and a Jump to select below. Tables keep two or three essential columns and become cards below 640 px. Nothing is shown only on hover, and touch targets are 44 px (`pointer: coarse`): the shared buttons and inputs (`settings-frame`, `components/ui`) carry the `touch-target` utility from `globals.css`.
- **A service** has four tabs: Overview (with usage), Deploys, Logs, Settings (with variables, secrets, domains and config files).
- **State**: No left-border accent bars on active items, selected rows, callouts or cards: state is shown by background and text weight only. The one exception: markdown blockquotes keep their left border.
- **⌘K** is the main action surface: actions first, then PRs, issues and docs.
- **Home**: a one-line health strip (not metric cards), a "Needs you" queue across all modules, deploying-now, activity, and a projects table.
- **Cross-links everywhere**: a deploy shows its PR and issue; a service shows the PRs, issues and runbooks that touch it; a PR's merge box lists what merging will deploy and ship.

### Design Principles

1. **Cockpit clarity** — Every status visible at a glance. No hunting required. The dashboard is a control room, not a marketing page. The most important question ("is my system healthy?") must be answerable in under two seconds.

2. **Density without clutter** — Pack information meaningfully. No whitespace theater. Every pixel earns its place, but grouping, rhythm, and hierarchy prevent density from becoming overwhelm. Lists beat grids when items need comparison. Tables beat cards when columns matter.

3. **Infrastructure-grade trust** — Typography, spacing, and component choices signal precision and reliability. Monospace for technical values. Semantic color only. No shadow glow, no gradient text, no decorative chrome. The interface should look like it was built by someone who cares about correctness.

4. **OSS-ready legibility** — A stranger opening a page for the first time should understand what they are looking at within 5 seconds. Labels are specific (not "Manage" — "View Deployments"). Empty states explain what the section does and how to fill it. Errors explain what went wrong, not just that something did.

5. **Control, not configuration** — Users act, they don't configure workflows. The most common actions (deploy, view logs, check status, stop service) must require the fewest possible clicks and be visually prominent. Destructive actions are confirmable but not buried.

### References

Traits taken from public reconstructions of other products' design systems (VoltAgent/awesome-design-md), translated into Toft's own tokens. Nothing else is borrowed: no names, marks or signature looks.

- **Linear:** depth from the surface ladder and hairline borders rather than shadows, on dark themes (→ Elevation, ground/surface/raised).
- **Vercel:** small stacked shadows instead of one heavy drop on light themes (→ `--elevation-*` on Paper and Daylight); one base radius for controls, a larger one for cards (→ the radius table).
- **The dataviz method** (Claude's dataviz skill): tabular figures only in columns, status colours reserved for state, thin marks, one axis, text in text tokens, a legend and an accessible reading for every chart (→ Data and numbers).
