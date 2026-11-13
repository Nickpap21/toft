import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { GitFileDiff } from "@/lib/api/code.service";
import { DiffFile } from "./views";

const file = (path: string) => ({ path, status: "modified", additions: 1, deletions: 0, patch: "@@ -1 +1 @@\n+a\n", binary: false }) as GitFileDiff;

describe("DiffFile", () => {
  it("names each file's scrolling diff by its path", () => {
    render(
      <>
        <DiffFile f={file("src/a.ts")} />
        <DiffFile f={file("src/b.ts")} />
      </>,
    );
    expect(screen.getByRole("group", { name: "Changes in src/a.ts" })).toBeTruthy();
    expect(screen.getByRole("group", { name: "Changes in src/b.ts" })).toBeTruthy();
  });
});
