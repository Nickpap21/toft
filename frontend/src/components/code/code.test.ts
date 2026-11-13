import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { repoService } from "@/lib/api/code.service";
import { languageFor } from "./highlight";
import { aheadBehind } from "./parts";
import { codeHref, compactAgo, formatSize, pullHref, repoBase } from "./repo";
import { parsePatch } from "./views";

describe("parsePatch", () => {
  it("numbers old and new lines across hunks", () => {
    const rows = parsePatch("@@ -1,3 +1,3 @@ func x() {\n a\n-b\n+c\n d\n@@ -10 +10,2 @@\n e\n+f\n\\ No newline at end of file\n");
    expect(rows.map((r) => [r.kind, r.old ?? null, r.new ?? null, r.text])).toEqual([
      ["hunk", null, null, "@@ -1,3 +1,3 @@ func x() {"],
      ["context", 1, 1, "a"],
      ["del", 2, null, "b"],
      ["add", null, 2, "c"],
      ["context", 3, 3, "d"],
      ["hunk", null, null, "@@ -10 +10,2 @@"],
      ["context", 10, 10, "e"],
      ["add", null, 11, "f"],
      ["note", null, null, "No newline at end of file"],
    ]);
  });
});

describe("languageFor", () => {
  it("knows files by extension and by name", () => {
    expect(languageFor("services/api/main.go")).toBe("go");
    expect(languageFor("web/app/page.tsx")).toBe("tsx");
    expect(languageFor("Dockerfile")).toBe("docker");
    expect(languageFor("deploy/Dockerfile.prod")).toBe("docker");
    expect(languageFor("Makefile")).toBe("make");
    expect(languageFor(".env")).toBe("ini");
    expect(languageFor("LICENSE")).toBeNull();
    expect(languageFor("archive.tar.gz")).toBeNull();
  });
});

describe("helpers", () => {
  it("builds the Code pages' URLs, the repository in them (D-75)", () => {
    const base = repoBase("p1", "shop");
    expect(base).toBe("/projects/p1/code/shop");
    expect(codeHref(base, "")).toBe("/projects/p1/code/shop");
    expect(codeHref(base, "blob", "main", "a b/c.md")).toBe("/projects/p1/code/shop/blob?ref=main&path=a+b%2Fc.md");
    expect(codeHref(base, "commits", "", "src")).toBe("/projects/p1/code/shop/commits?path=src");
    expect(codeHref(base, "pulls/new")).toBe("/projects/p1/code/shop/pulls/new");
    expect(pullHref(base, 42, "files")).toBe("/projects/p1/code/shop/pulls/42?tab=files");
  });
  it("says how old something is, compactly", () => {
    const now = Date.parse("2026-10-19T12:00:00Z");
    expect(compactAgo("2026-10-19T11:58:00Z", now)).toBe("2m");
    expect(compactAgo("2026-10-19T09:00:00Z", now)).toBe("3h");
    expect(compactAgo("2026-09-28T12:00:00Z", now)).toBe("3w");
    expect(compactAgo("2026-08-01T12:00:00Z", now)).toBe("2mo");
  });
  it("sizes and ahead/behind", () => {
    expect(formatSize(712)).toBe("712 B");
    expect(formatSize(4200)).toBe("4.1 KB");
    expect(aheadBehind(6, 0)).toBe("6 ahead · ");
    expect(aheadBehind(1, 4)).toBe("1 ahead, 4 behind · ");
    expect(aheadBehind(null, null)).toBe("");
  });
});

// A repository's queries are keyed by its id. Keyed by the repoService
// object, they hashed alike for every repository and stopped matching once
// the object was made again (a refetch of the repositories), so a pull
// request closed from its page stayed open on screen.
describe("query keys", () => {
  it("match across repoService objects and differ across repositories", async () => {
    const qc = new QueryClient();
    let fetched = 0;
    const key = (projectId: string, repoId: string) => ["pull", repoService(projectId, repoId).id, 1];
    await qc.fetchQuery({ queryKey: key("p1", "r1"), queryFn: () => ++fetched });
    await qc.invalidateQueries({ queryKey: key("p1", "r1") });
    expect(qc.getQueryState(key("p1", "r1"))?.isInvalidated).toBe(true);
    await qc.fetchQuery({ queryKey: key("p1", "r2"), queryFn: () => -1 });
    expect(qc.getQueryData(key("p1", "r1"))).toBe(1);
    expect(qc.getQueryData(key("p1", "r2"))).toBe(-1);
  });
});
