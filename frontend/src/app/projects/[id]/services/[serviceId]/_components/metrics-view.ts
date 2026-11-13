// The Overview's Usage card (board: private/design/canvas/ReworkService.dc.html).
// The pure parts: the lines, their scale and the peak.

export type Range = "1h" | "24h" | "7d";

/** How far back each range reaches, in milliseconds. */
export const SPAN: Record<Range, number> = { "1h": 3_600_000, "24h": 86_400_000, "7d": 7 * 86_400_000 };

/** A gap longer than this between two samples breaks the line: a minute a
 *  sample for 1h and 24h, a day a point for 7d (D-156). */
const GAP: Record<Range, number> = { "1h": 3 * 60_000, "24h": 3 * 60_000, "7d": 1.5 * 86_400_000 };

/** The chart's box: the SVG's viewBox is 0 0 W H. */
export const W = 300;
export const H = 84;

export interface Point {
  timestamp: string;
  cpu_percent: number;
  memory_mb: number;
  /** 7d: a day kept before the summed peaks, its largest container's. */
  largest_container?: boolean;
}

/** The line of pick(point) over the range ending at end, scaled so top is
 *  the chart's top: one polyline's points per run of samples without a gap. */
export function lines(points: Point[], range: Range, end: number, top: number, pick: (p: Point) => number): string[] {
  const start = end - SPAN[range];
  const runs: string[][] = [];
  let last = -Infinity;
  for (const p of points) {
    const t = new Date(p.timestamp).getTime();
    if (t < start || t > end) continue;
    if (t - last > GAP[range] || runs.length === 0) runs.push([]);
    last = t;
    const x = ((t - start) / SPAN[range]) * W;
    const y = H - 1 - Math.min(1, Math.max(0, pick(p) / (top || 1))) * (H - 2);
    runs[runs.length - 1].push(`${+x.toFixed(1)},${+y.toFixed(1)}`);
  }
  // A lone sample is a dot: a short flat line ending on it, inside the box.
  return runs.map((r) => (r.length === 1 ? `${r[0].replace(/^[\d.]+/, (x) => `${Math.max(0, +x - 1)}`)} ${r[0]}` : r.join(" ")));
}

/** The sample nearest to frac (0 to 1) of the way across the range. */
export function nearest(points: Point[], range: Range, end: number, frac: number): Point | null {
  const t = end - SPAN[range] * (1 - frac);
  let best: Point | null = null;
  let gap = Infinity;
  for (const p of points) {
    const d = Math.abs(new Date(p.timestamp).getTime() - t);
    if (d < gap) {
      gap = d;
      best = p;
    }
  }
  return best;
}

/** The highest sample. */
export function peak(points: Point[], pick: (p: Point) => number): number | null {
  return points.length === 0 ? null : Math.max(...points.map(pick));
}

/** A scale's top for values without a limit: the next round number above
 *  the highest, so the line never touches the top. */
export function niceTop(high: number): number {
  if (high <= 0) return 1;
  const pow = 10 ** Math.floor(Math.log10(high));
  for (const m of [1, 2, 2.5, 5, 10]) {
    if (m * pow > high) return m * pow;
  }
  return 10 * pow;
}
