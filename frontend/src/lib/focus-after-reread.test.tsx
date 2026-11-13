import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { focusAfterReread } from "./focus-after-reread";

function renderForm(save: "on" | "disabled" | "none") {
  render(
    <>
      <h2 id="h" tabIndex={-1}>
        Heading
      </h2>
      {save !== "none" && (
        <button id="save" disabled={save === "disabled"}>
          Save
        </button>
      )}
    </>,
  );
  focusAfterReread("save", "h");
}

describe("focusAfterReread", () => {
  it("goes to Save when it can be pressed", async () => {
    renderForm("on");
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Save" })));
  });

  it.each(["disabled", "none"] as const)("goes to the heading when Save is %s", async (save) => {
    renderForm(save);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("heading", { name: "Heading" })));
  });
});
