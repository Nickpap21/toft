import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render as rtlRender, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Service } from "@/lib/api/service.service";
import { initForm, type ConfigForm } from "../settings-form";
import { WorkerCheckField } from "./settings-tab";

const worker = {
  id: "svc-1",
  name: "mailer",
  type: "worker",
  source_type: "git",
} as unknown as Service;

const render = (ui: React.ReactElement) =>
  rtlRender(ui, {
    wrapper: ({ children }) => (
      <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider>
    ),
  });

function Harness({ service = worker }: { service?: Service }) {
  const saved = initForm(service);
  const [form, setForm] = useState<ConfigForm>(saved);
  return (
    <>
      <WorkerCheckField
        service={service}
        serviceId={service.id}
        form={form}
        set={(p) => setForm((f) => ({ ...f, ...p }))}
        canEdit
        saved={saved}
      />
      <output data-testid="form">
        {form.health_check_retries}|{form.container_health}|
        {form.container_health_command}
      </output>
    </>
  );
}

describe("WorkerCheckField", () => {
  it("starts on the image's check, with no HTTP fields", () => {
    render(<Harness />);
    expect(screen.getByRole("radio", { name: "The image's" })).toBeChecked();
    expect(screen.queryByLabelText("Path")).toBeNull();
  });

  it("takes a command, with HTTP off", async () => {
    const user = userEvent.setup();
    render(<Harness service={{ ...worker, health_check_retries: 3 } as Service} />);
    expect(screen.getByRole("radio", { name: "HTTP" })).toBeChecked();
    const check = within(screen.getByRole("group", { name: "Check" }));
    await user.click(check.getByRole("radio", { name: "Command" }));
    await user.type(screen.getByRole("textbox"), "pgrep -f mailer");
    expect(screen.getByTestId("form")).toHaveTextContent("0|command|pgrep -f mailer");
  });

  it("turns every check off with None", async () => {
    const user = userEvent.setup();
    render(<Harness service={{ ...worker, health_check_retries: 3 } as Service} />);
    await user.click(screen.getByRole("radio", { name: "None" }));
    expect(screen.getByTestId("form")).toHaveTextContent("0|off|");
    expect(screen.getByText(/checks nothing/)).toBeInTheDocument();
  });

  it("keeps HTTP open while its attempts are retyped", async () => {
    const user = userEvent.setup();
    render(<Harness service={{ ...worker, health_check_retries: 3 } as Service} />);
    await user.clear(screen.getByLabelText("Attempts"));
    expect(screen.getByRole("radio", { name: "HTTP" })).toBeChecked();
    expect(screen.getByLabelText("Attempts")).toHaveFocus();
  });

  it("goes back to the saved container check when None is left for HTTP", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const check = within(screen.getByRole("group", { name: "Check" }));
    await user.click(check.getByRole("radio", { name: "None" }));
    await user.click(check.getByRole("radio", { name: "HTTP" }));
    expect(screen.getByTestId("form")).toHaveTextContent("5|image|");
  });

  it("opens the HTTP fields with HTTP, its attempts back on", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole("radio", { name: "HTTP" }));
    expect(screen.getByLabelText("Path")).toBeInTheDocument();
    expect(screen.getByTestId("form")).toHaveTextContent("5|image|");
  });
});
