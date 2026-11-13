import { afterEach, describe, expect, it } from "vitest";
import { Editor } from "@tiptap/core";
import StarterKit from "@tiptap/starter-kit";
import { Markdown } from "@tiptap/markdown";
import { TaskItem, TaskList } from "@tiptap/extension-list";
import { Note, ToftBlock } from "./editor-nodes";

const SVC = "319e711d-c161-458c-936a-5013213f6233";
let editor: Editor | undefined;
afterEach(() => editor?.destroy());

function roundTrip(md: string): { out: string; types: string[] } {
  editor = new Editor({
    extensions: [
      StarterKit.configure({ link: { isAllowedUri: (url, ctx) => url.startsWith("page:") || ctx.defaultValidate(url) } }),
      TaskList,
      TaskItem.configure({ nested: true }),
      ToftBlock,
      Note,
      Markdown,
    ],
    content: md,
    contentType: "markdown",
  });
  const types: string[] = [];
  editor.state.doc.forEach((n) => types.push(n.type.name));
  return { out: editor.getMarkdown(), types };
}

describe("the editor's markdown (D-86)", () => {
  it("keeps Toft's blocks, notes, checklists and page links as the server reads them", () => {
    const md = [
      "Use this when it breaks.",
      "",
      "```toft-service " + SVC,
      "```",
      "",
      "## 1. Stop writers",
      "",
      "```toft-run " + SVC + " host",
      "docker stop api \\",
      "  worker",
      "```",
      "",
      "> [!NOTE]",
      "> `--clean` drops each object.",
      "",
      "- [ ] Row count matches",
      "- [x] api answers",
      "",
      "```sh",
      "plain code",
      "```",
      "",
      "See [the checklist](page:7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d).",
      "",
      "> a plain quote",
    ].join("\n");
    const { out, types } = roundTrip(md);
    expect(types).toEqual(["paragraph", "toftBlock", "heading", "toftBlock", "note", "taskList", "codeBlock", "paragraph", "blockquote"]);
    expect(out).toContain("```toft-service " + SVC + "\n```");
    expect(out).toContain("```toft-run " + SVC + " host\ndocker stop api \\\n  worker\n```");
    expect(out).toContain("> [!NOTE]\n> `--clean` drops each object.");
    expect(out).toMatch(/- \[ \] Row count matches\n- \[x\] api answers/);
    expect(out).toContain("```sh\nplain code\n```");
    expect(out).toContain("[the checklist](page:7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d)");
    expect(out).toContain("> a plain quote");
    // A second pass changes nothing.
    editor?.destroy();
    expect(roundTrip(out).out).toBe(out);
  });
});
