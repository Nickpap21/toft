// The Logs tab (board: private/design/canvas/ServiceLogs.dc.html). The pure
// parts: what a streamed line says, and how the tab names a container.

export type LogLevel = "error" | "warn" | "info" | "debug" | "";

/** A line as the stream sends it with ?timestamps=1: Docker's RFC 3339
 *  time, a space, then what the container wrote. */
const STAMP = /^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)) ([\s\S]*)$/;

/** Splits Docker's timestamp from a line: at is milliseconds since the
 *  epoch, null when the line has none. */
export function splitStamp(line: string): { at: number | null; rest: string } {
  const m = STAMP.exec(line);
  if (!m) return { at: null, rest: line };
  const at = Date.parse(m[1]);
  return Number.isNaN(at) ? { at: null, rest: line } : { at, rest: m[2] };
}

const LEVEL_KEYS = ["level", "lvl", "severity", "log.level", "loglevel"];
const MESSAGE_KEYS = ["msg", "message"];
/** Keys a JSON line's message leaves out: shown elsewhere, or noise. */
const DROPPED_KEYS = new Set([...LEVEL_KEYS, ...MESSAGE_KEYS, "time", "ts", "timestamp", "@timestamp", "pid", "hostname"]);

function levelFrom(value: unknown): LogLevel {
  if (typeof value === "number") {
    // Monolog's numeric levels run 100–600, pino's 10–60
    if (value >= 100) return value >= 400 ? "error" : value >= 300 ? "warn" : value >= 200 ? "info" : "debug";
    return value >= 50 ? "error" : value >= 40 ? "warn" : value >= 30 ? "info" : "debug";
  }
  const v = String(value).toLowerCase();
  if (/^(error|err|fatal|panic|critical|crit|alert|emerg)/.test(v)) return "error";
  if (/^warn/.test(v)) return "warn";
  if (/^(info|notice)/.test(v)) return "info";
  if (/^(debug|trace)/.test(v)) return "debug";
  return "";
}

/** A line's level and the text to show. A JSON object's level field wins,
 *  and it reads as its message followed by its other fields as key=value;
 *  any other line's level is what it declares (level=, nginx's [crit]),
 *  else guessed from its words. */
export function readLine(text: string): { level: LogLevel; text: string } {
  const t = text.trim();
  if (t.startsWith("{") && t.endsWith("}")) {
    try {
      const obj = JSON.parse(t) as Record<string, unknown>;
      if (obj && typeof obj === "object" && !Array.isArray(obj)) {
        const levelKey = LEVEL_KEYS.find((k) => k in obj);
        const messageKey = MESSAGE_KEYS.find((k) => typeof obj[k] === "string");
        const rest = Object.entries(obj)
          .filter(([k]) => !DROPPED_KEYS.has(k))
          .map(([k, v]) => `${k}=${typeof v === "string" ? (v.includes("\n") ? v : /\s/.test(v) ? JSON.stringify(v) : v) : JSON.stringify(v)}`);
        const message = [messageKey ? String(obj[messageKey]) : "", ...rest].filter(Boolean).join(" ");
        return { level: levelKey ? levelFrom(obj[levelKey]) : guessLevel(t), text: message || t };
      }
    } catch {
      // not JSON after all: read it as text
    }
  }
  return { level: guessLevel(text), text };
}

/** logfmt's level=, or a level in brackets as nginx writes it: what a line
 *  says of itself, which wins over any word in its message. */
const DECLARED = /(?:^|[\s,])(?:log\.?level|level|lvl|severity)=["']?([a-z]+)|\[(emerg|alert|crit|critical|error|err|warn|warning|notice|info|debug|fatal)\]/i;
/** TypeError:, Error:, NullPointerException, a Python Traceback: crashes named as their language names them. */
const CRASH = /\b\w*(?:Error:|Exception\b)|\bTraceback\b/;
// A word on its own: not a path or a file name (/error-pages/, error.log), nor "no error yet".
const WORD_START = String.raw`(?<![\w./-])(?<!\b(?:no|not|without|zero|0) )`;
const WORD_END = String.raw`(?![\w/-]|\.\w)`;
const ERROR_WORD = new RegExp(`${WORD_START}(?:error|fatal|panic|exception|critical)${WORD_END}(?!=(?:nil|null|none|false|0|<nil>)(?![\\w]))`, "i");
const WARN_WORD = new RegExp(`${WORD_START}warn(?:ing)?${WORD_END}`, "i");
/** A level as a line's first word, after any timestamp: INFO (Spring Boot, Python's logging) or info: (Node's winston). */
const LEADING = /^\s*(?:\d{4}-\d\d-\d\d[T ]\d\d:\d\d:\d\d\S*\s+)?(?:([A-Z]{3,8})\b|([a-z]{3,8}):)/;

function guessLevel(line: string): LogLevel {
  const declared = DECLARED.exec(line);
  const said = declared ? levelFrom(declared[1] ?? declared[2]) : "";
  if (said) return said;
  const leading = LEADING.exec(line);
  const first = leading ? levelFrom(leading[1] ?? leading[2]) : "";
  if (first) return first;
  if (CRASH.test(line) || ERROR_WORD.test(line)) return "error";
  if (WARN_WORD.test(line)) return "warn";
  if (/\b(info|notice)\b/i.test(line)) return "info";
  if (/\b(debug|trace)\b/i.test(line)) return "debug";
  return "";
}

/** The compose service a container runs: "nginx" for
 *  "paas-web-8f14e45f-nginx-1" (the platform's project is
 *  paas-<name>-<id8>). "" when the name is not the stack's. */
export function composeServiceOf(container: string, serviceId: string): string {
  const marker = `-${serviceId.slice(0, 8)}-`;
  const i = container.indexOf(marker);
  if (i < 0) return "";
  return container.slice(i + marker.length).replace(/-\d+$/, "");
}

/** "14:02:11", in the browser's time zone. */
export function clockTime(ms: number): string {
  const d = new Date(ms);
  return [d.getHours(), d.getMinutes(), d.getSeconds()].map((n) => String(n).padStart(2, "0")).join(":");
}

/** The midnight that starts ms's day, in the browser's time zone. */
export function startOfDay(ms: number): number {
  const d = new Date(ms);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}

const DAY_FORMAT = new Intl.DateTimeFormat("en", { month: "short", day: "numeric" });

/** A line's time in the browser's zone: "14:02:11" on the day that starts
 *  at dayStart, "Nov 3 14:02:11" before it. */
export function lineTime(ms: number, dayStart: number): string {
  return ms >= dayStart ? clockTime(ms) : `${DAY_FORMAT.format(ms)} ${clockTime(ms)}`;
}

const STAMP_FORMAT = new Intl.DateTimeFormat("en", { dateStyle: "full", timeStyle: "long", hourCycle: "h23" });

/** A line's full date, time and time zone. */
export function lineStamp(ms: number): string {
  return STAMP_FORMAT.format(ms);
}

const ZONE_FORMAT = new Intl.DateTimeFormat("en", { timeZoneName: "short" });

/** The browser's time zone at ms, as "GMT+2" or "EET". */
export function zoneName(ms: number): string {
  return ZONE_FORMAT.formatToParts(ms).find((p) => p.type === "timeZoneName")?.value ?? "";
}

/** A run of a line's text and the classes its ANSI colour gives it. */
export interface Span {
  text: string;
  cls: string;
}

// The 8 basic colours (30–37, bright 90–97) as theme tokens (D-155).
const ANSI_FG = [
  "text-muted", // black
  "text-status-failed-fg", // red
  "text-status-running-fg", // green
  "text-status-pending-fg", // yellow
  "text-env-blue-fg", // blue
  "text-env-violet-fg", // magenta
  "text-env-teal-fg", // cyan
  "text-fg", // white
];

// An escape: CSI (its parameters and final byte captured; m sets colours),
// OSC (titles, links) up to BEL or ST, a charset or other two-byte escape,
// or a lone ESC.
const ESCAPE = /\x1b(?:\[([0-?]*)[ -/]*([@-~])|\][^\x07\x1b]*(?:\x07|\x1b\\)?|[ -/]+[0-~]|[@-_]|)/g;

/** A line without its escapes (plain, for the level and the filter), and
 *  its coloured runs when it sets a basic colour or bold; spans is null
 *  for a line without them. Every other escape is dropped. */
export function ansiSpans(line: string): { plain: string; spans: Span[] | null } {
  if (!line.includes("\x1b")) return { plain: line, spans: null };
  const spans: Span[] = [];
  let fg = "";
  let bold = false;
  let coloured = false;
  let last = 0;
  let plain = "";
  const push = (text: string) => {
    if (!text) return;
    plain += text;
    const cls = [fg, bold ? "font-semibold" : ""].filter(Boolean).join(" ");
    if (cls) coloured = true;
    spans.push({ text, cls });
  };
  for (const m of line.matchAll(ESCAPE)) {
    push(line.slice(last, m.index));
    last = m.index + m[0].length;
    if (m[2] !== "m") continue;
    const codes = (m[1] ?? "").split(";");
    for (let i = 0; i < codes.length; i++) {
      const n = Number(codes[i] || 0);
      if (n === 0) [fg, bold] = ["", false];
      else if (n === 1) bold = true;
      else if (n === 22) bold = false;
      else if (n >= 30 && n <= 37) fg = ANSI_FG[n - 30];
      else if (n >= 90 && n <= 97) fg = ANSI_FG[n - 90];
      else if (n === 39) fg = "";
      // 256-colour and RGB colours are skipped with their parameters.
      else if (n === 38 || n === 48) i += codes[i + 1] === "5" ? 2 : codes[i + 1] === "2" ? 4 : 0;
    }
  }
  push(line.slice(last));
  return { plain, spans: coloured ? spans : null };
}
