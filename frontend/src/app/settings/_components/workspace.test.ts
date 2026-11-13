import { describe, expect, it } from "vitest";
import { initials } from "@/lib/utils";
import { deleteRows, listOf } from "./workspace";

describe("initials", () => {
  it("takes two words' first letters, or one word's first two", () => {
    expect(initials("Local Admin")).toBe("LA");
    expect(initials("admin")).toBe("AD");
    expect(initials("  ")).toBe("?");
  });
});

describe("listOf", () => {
  it("joins with commas and a final and", () => {
    expect(listOf(["a"])).toBe("a");
    expect(listOf(["a", "b"])).toBe("a and b");
    expect(listOf(["a", "b", "c"])).toBe("a, b and c");
  });
});

describe("deleteRows", () => {
  it("names everything that goes", () => {
    const rows = deleteRows({ projects: ["storefront", "billing-api"], services: 14, volumes: 3, servers: ["fsn1", "fsn2"], members: 3, invites: 1 });
    expect(rows.map((r) => r.v)).toEqual([
      "2: storefront, billing-api",
      "14, removed from their servers with their volumes and data",
      "3, with every file in them",
      "The entries for fsn1 and fsn2. The machines are not touched.",
      "3 members lose access; 1 pending invite stops working",
      "Settings, the alert webhook and the audit log",
    ]);
  });
  it("says none for an empty workspace", () => {
    const rows = deleteRows({ projects: [], services: 0, volumes: 0, servers: [], members: 1, invites: 0 });
    expect(rows.map((r) => r.v).slice(0, 5)).toEqual(["None", "None", "None", "None of its own", "1 member loses access"]);
  });
  it("says Unknown for what couldn't be read, never None", () => {
    const rows = deleteRows({ invites: 0 });
    expect(rows.map((r) => r.v).slice(0, 5)).toEqual(["Unknown", "Unknown", "Unknown", "Unknown", "Unknown"]);
  });
});
