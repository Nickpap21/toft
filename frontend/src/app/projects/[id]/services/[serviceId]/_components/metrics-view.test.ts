import { describe, expect, it } from "vitest";
import { lines, nearest, niceTop, peak, type Point } from "./metrics-view";

const at = (iso: string, mem: number, cpu = 1): Point => ({ timestamp: iso, memory_mb: mem, cpu_percent: cpu });
const END = Date.parse("2026-10-14T14:00:00Z");
const mem = (p: Point) => p.memory_mb;

describe("lines", () => {
  it("places samples across the range, scaled to the top", () => {
    const l = lines([at("2026-10-14T13:00:00Z", 0), at("2026-10-14T13:01:00Z", 512), at("2026-10-14T13:02:00Z", 256)], "1h", END, 512, mem);
    expect(l).toEqual(["0,83 5,1 10,42"]);
  });

  it("breaks the line where samples are missing", () => {
    const l = lines([at("2026-10-14T13:00:00Z", 10), at("2026-10-14T13:01:00Z", 10), at("2026-10-14T13:30:00Z", 10), at("2026-10-14T13:31:00Z", 10)], "1h", END, 20, mem);
    expect(l).toHaveLength(2);
  });

  it("keeps daily points of 7d in one line", () => {
    const days = [8, 9, 10, 11, 12, 13, 14].map((d) => at(`2026-10-${String(d).padStart(2, "0")}T00:00:00Z`, 100));
    expect(lines(days, "7d", END, 200, mem)).toHaveLength(1);
  });

  it("leaves out samples outside the range and clamps over the top", () => {
    expect(lines([at("2026-10-14T12:00:00Z", 9)], "1h", END, 10, mem)).toEqual([]);
    expect(lines([at("2026-10-14T13:59:00Z", 999)], "1h", END, 10, mem)).toEqual(["294,1 295,1"]);
    expect(lines([at("2026-10-14T14:00:00Z", 0)], "1h", END, 10, mem)).toEqual(["299,83 300,83"]);
  });
});

describe("nearest", () => {
  it("picks the sample closest to the pointer", () => {
    const pts = [at("2026-10-14T13:00:00Z", 1), at("2026-10-14T13:30:00Z", 2), at("2026-10-14T13:59:00Z", 3)];
    expect(nearest(pts, "1h", END, 0.45)?.memory_mb).toBe(2);
    expect(nearest(pts, "1h", END, 1)?.memory_mb).toBe(3);
    expect(nearest([], "1h", END, 0.5)).toBeNull();
  });
});

describe("scale", () => {
  it("finds the peak", () => {
    expect(peak([at("x", 194), at("x", 236), at("x", 210)], mem)).toBe(236);
    expect(peak([], mem)).toBeNull();
  });

  it("rounds a scale's top up", () => {
    expect(niceTop(236)).toBe(250);
    expect(niceTop(31)).toBe(50);
    expect(niceTop(0)).toBe(1);
  });
});
