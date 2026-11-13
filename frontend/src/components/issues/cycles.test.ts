import { describe, expect, it } from "vitest";
import { cycleEnd, day, dueOf, estimateText, iso } from "./cycles";
import { _test } from "./timeline";

// D-90: due dates read as the board draws them, the schedule as the
// backend draws it, T-shirt estimates, and where a Timeline bar sits.

describe("dueOf", () => {
  it("reads late, today, tomorrow and soon while open; plain once shipped", () => {
    expect(dueOf("2026-10-13", "todo", "2026-10-14")).toEqual({ text: "Oct 13 · 1 day late", tone: "late" });
    expect(dueOf("2026-10-14", "in_progress", "2026-10-14").text).toBe("Due today");
    expect(dueOf("2026-10-15", "todo", "2026-10-14")).toEqual({ text: "Due tomorrow", tone: "soon" });
    expect(dueOf("2026-10-16", "todo", "2026-10-14").tone).toBe("soon");
    expect(dueOf("2026-10-17", "todo", "2026-10-14").tone).toBe("plain");
    expect(dueOf("2026-10-01", "shipped", "2026-10-14").tone).toBe("plain");
  });
});

describe("cycleEnd", () => {
  it("runs the weeks, and longer until the next start day", () => {
    expect(iso(cycleEnd(day("2026-10-19"), 2, 1))).toBe("2026-11-01");
    expect(iso(cycleEnd(day("2026-11-02"), 1, 3))).toBe("2026-11-10");
  });
});

describe("estimateText", () => {
  it("shows points, or the nearest T-shirt size", () => {
    expect(estimateText(5, "fibonacci")).toBe("5");
    expect(estimateText(5, "tshirt")).toBe("L");
    expect(estimateText(13, "tshirt")).toBe("XL");
  });
});

describe("the Timeline's bars", () => {
  const it0 = { key: "STO-1", number: 1, title: "", status: "in_progress", priority: "none", labels: [], assignee: null, estimate: null, progress: null, start_on: null, due_on: null, started_at: null, shipped_at: null } as const;
  const task = { ...it0, type: { id: "t", name: "Task", kind: "standard", icon: "task", color: "blue" } } as never;
  const epic = { ...it0, key: "STO-2", type: { id: "e", name: "Epic", kind: "container", icon: "epic", color: "violet" } } as never;
  it("runs from the start date, or when it started, to the due date", () => {
    expect(_test.barOf({ ...(task as object), started_at: "2026-10-07T09:00:00Z" } as never, [], new Map())).toEqual({ start: "2026-10-07", end: null, own: false });
    expect(_test.barOf({ ...(task as object), start_on: "2026-10-08", due_on: "2026-10-17" } as never, [], new Map())).toEqual({ start: "2026-10-08", end: "2026-10-17", own: true });
  });
  it("spans an epic's children when it has no dates", () => {
    const kids = [
      { ...(task as object), key: "STO-3", start_on: "2026-10-08", due_on: "2026-10-12" },
      { ...(task as object), key: "STO-4", start_on: "2026-10-10", due_on: "2026-10-20" },
    ] as never[];
    expect(_test.barOf(epic, kids, new Map())).toEqual({ start: "2026-10-08", end: "2026-10-20", own: false });
  });
});
