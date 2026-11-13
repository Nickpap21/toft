import { describe, expect, it } from "vitest";
import type { AddableAccount } from "@/lib/api/org.service";
import { addableNote, suggest } from "./workspace-dialogs";

const jo: AddableAccount = { user_id: "1", name: "jo", email: "jo@paas.local", former: true, former_role: "developer", left_at: "2026-10-16T17:40:00" };
const kim: AddableAccount = { user_id: "2", name: "Kim", email: "kim@example.com", former: false, former_role: "", left_at: null };

describe("Has an account here (D-58)", () => {
  it("filters by name or email, at most six", () => {
    expect(suggest([jo, kim], "")).toHaveLength(2);
    expect(suggest([jo, kim], "KIM")).toEqual([kim]);
    expect(suggest([jo, kim], "paas")).toEqual([jo]);
    expect(suggest(Array.from({ length: 9 }, (_, i) => ({ ...kim, user_id: String(i) })), "")).toHaveLength(6);
  });
  it("says what they were and when they left", () => {
    expect(addableNote(jo)).toBe("Was a developer here until 2026-10-16");
    expect(addableNote({ ...jo, left_at: null })).toBe("Was a developer here");
    expect(addableNote({ ...jo, former_role: "owner" })).toBe("Was an owner here until 2026-10-16");
    expect(addableNote(kim)).toBe("On this instance");
  });
});
