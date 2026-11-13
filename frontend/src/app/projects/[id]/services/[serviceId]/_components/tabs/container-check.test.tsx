import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Service } from "@/lib/api/service.service";
import { initForm, type ConfigForm } from "../settings-form";
import { ContainerCheckField } from "./settings-tab";

const worker = {
  id: "svc-1",
  name: "worker",
  source_type: "git",
} as unknown as Service;

function Harness({ canEdit = true }: { canEdit?: boolean }) {
  const [form, setForm] = useState<ConfigForm>(() => initForm(worker));
  return (
    <>
      <ContainerCheckField
        service={worker}
        form={form}
        set={(p) => setForm((f) => ({ ...f, ...p }))}
        canEdit={canEdit}
      />
      <output data-testid="form">
        {form.container_health}|{form.container_health_command}
      </output>
    </>
  );
}

describe("ContainerCheckField", () => {
  it("starts on the image's check, with no command field", () => {
    render(<Harness />);
    expect(screen.getByRole("radio", { name: "The image's" })).toBeChecked();
    expect(screen.queryByLabelText("Command", { selector: "input#set-ccheck" })).toBeNull();
  });

  it("turns the check off, or takes a command", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole("radio", { name: "Off" }));
    expect(screen.getByTestId("form")).toHaveTextContent("off|");
    expect(screen.getByText(/Docker runs no check/)).toBeInTheDocument();

    await user.click(screen.getByRole("radio", { name: "Command" }));
    await user.type(screen.getByRole("textbox"), "pgrep -f worker");
    expect(screen.getByTestId("form")).toHaveTextContent("command|pgrep -f worker");
  });

  it("is read-only without the right to deploy", () => {
    render(<Harness canEdit={false} />);
    for (const r of screen.getAllByRole("radio")) expect(r).toBeDisabled();
  });
});
