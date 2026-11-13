import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WorkChip } from "./meta";

const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("sonner", () => ({ toast }));

const branch = "fix/sto-118-webhook-retry";

async function copy(clipboard: { writeText: (t: string) => Promise<void> } | undefined) {
  const user = userEvent.setup();
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: clipboard });
  render(<WorkChip work={{ kind: "branch", branch }} issueKey="STO-118" />);
  await user.click(screen.getByRole("button", { name: `Branch ${branch}` }));
  await user.click(await screen.findByRole("menuitem", { name: "Copy branch name" }));
}

afterEach(() => vi.clearAllMocks());

describe("the branch chip's Copy", () => {
  it("says copied once the clipboard has it", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    await copy({ writeText });
    expect(writeText).toHaveBeenCalledWith(branch);
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Branch name copied"));
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("says it couldn't when the clipboard refuses", async () => {
    await copy({ writeText: () => Promise.reject(new Error("denied")) });
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Couldn’t copy the branch name"));
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("says it couldn't with no clipboard at all (plain http)", async () => {
    await copy(undefined);
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Couldn’t copy the branch name"));
    expect(toast.success).not.toHaveBeenCalled();
  });
});
