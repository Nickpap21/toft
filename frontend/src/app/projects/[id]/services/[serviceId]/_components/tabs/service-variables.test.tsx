import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import type { VarScope } from "@/lib/api/envvar.service";
import type { Environment } from "@/lib/api/environment.service";
import type { Service } from "@/lib/api/service.service";
import { CopyFromDialog, ServiceVariables } from "./service-variables";

// production shares LOG_LEVEL and SENTRY_DSN, and the APP_SECRET and REGION
// secrets; web sets LOG_LEVEL itself and has a secret named REGION.
vi.mock("@/lib/api/envvar.service", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api/envvar.service")>();
  return {
    ...actual,
    envvarService: {
      list: (scope: VarScope) =>
        scope.kind === "environment"
          ? listEnvironment()
          : listOwn(),
      bulkUpsert: (...args: unknown[]) => bulkUpsert(...args),
    },
  };
});
const own = () => [{ key: "LOG_LEVEL", value: "debug", is_secret: false, masked: false }];
const readOwn = async () => ({ data: own(), version: "v1" });
const readEnvironment = async () => ({
  data: [{ key: "LOG_LEVEL", value: "info", is_secret: false }, { key: "SENTRY_DSN", value: "https://sentry", is_secret: false }],
  version: "e1",
});
let listOwn: () => Promise<{ data: unknown[]; version: string }> = readOwn;
let listEnvironment: () => Promise<{ data: unknown[]; version: string }> = readEnvironment;
const bulkUpsert = vi.fn();
const at = { created_at: "2026-11-01T10:00:00Z", updated_at: "2026-11-01T10:00:00Z", updated_by: "alex" };
vi.mock("@/lib/api/secret.service", () => ({
  secretService: {
    list: async (scope: VarScope) =>
      (scope.kind === "environment" ? [{ id: "1", key: "APP_SECRET" }, { id: "2", key: "REGION" }] : [{ id: "3", key: "REGION" }]).map(
        (s) => ({ ...s, ...at }),
      ),
  },
}));

let listServices: () => Promise<unknown[]> = async () => [{ id: "s2", name: "worker" }];
vi.mock("@/lib/api/service.service", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/service.service")>()),
  serviceService: { listOrgScoped: () => listServices() },
}));

// Each test its own service: unsaved drafts are kept per service for the page's life.
let seq = 0;
const env = { id: "env-1", name: "production", color: "gray", protected: true } as unknown as Environment;

function renderView(view: "variables" | "secrets" = "variables", canDeploy = true) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const id = `svc-${++seq}`;
  const service = { id, name: "web", environment_id: "env-1" } as Service;
  return render(
    <QueryClientProvider client={client}>
      <ServiceVariables service={service} serviceId={id} projectId="p-1" projectName="storefront" env={env} view={view} canDeploy={canDeploy} live={null} />
    </QueryClientProvider>,
  );
}
const card = async (heading: string) => (await screen.findByRole("heading", { name: heading })).closest("section")!;
const rowOf = (el: HTMLElement) => el.closest("tr")!;
const editValue = async (value: string) => {
  fireEvent.click(await screen.findByRole("button", { name: "Edit LOG_LEVEL" }));
  fireEvent.change(screen.getByLabelText("Value of LOG_LEVEL"), { target: { value } });
};

afterEach(() => {
  listOwn = readOwn;
  listEnvironment = readEnvironment;
  listServices = async () => [{ id: "s2", name: "worker" }];
  bulkUpsert.mockReset();
});

describe("CopyFromDialog when a read fails", () => {
  const renderDialog = () =>
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <CopyFromDialog open onClose={vi.fn()} name="web" projectId="" serviceId="s1" current={new Map()} onMerge={vi.fn()} />
      </QueryClientProvider>,
    );

  it("says the other service's variables couldn't be read, with Copy off", async () => {
    listOwn = () => Promise.reject(new ApiError("boom", 500, {}));
    renderDialog();
    await screen.findByRole("option", { name: /^worker/ });
    fireEvent.change(screen.getByLabelText("Service"), { target: { value: "s2" } });
    expect(await screen.findByText("Couldn’t load the variables of worker.")).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeTruthy();
    expect(screen.queryByText(/has no variables/)).toBeNull();
    expect((screen.getByRole("button", { name: /^Copy/ }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("says the services couldn't be read, rather than offering none", async () => {
    listServices = () => Promise.reject(new ApiError("boom", 500, {}));
    renderDialog();
    expect(await screen.findByText("Couldn’t load the services to copy from.")).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Retry/ })).toBeTruthy();
  });
});

describe("ServiceVariables", () => {
  it("shows a failed read instead of an empty list, with no Add or Save", async () => {
    listOwn = () => Promise.reject(new ApiError("Toft answered 500", 500, {}));
    renderView();
    const vars = await card("Variables");
    expect(await within(vars).findByRole("heading", { name: "Couldn’t load this page", level: 2 })).toBeTruthy();
    expect(within(vars).queryByText(/gets no variables yet/)).toBeNull();
    expect(within(vars).queryByRole("button", { name: "Add variable" })).toBeNull();
    expect(within(vars).queryByRole("button", { name: "Save variables" })).toBeNull();
  });

  it("shows a failed read of the environment's variables too, with no empty text", async () => {
    listEnvironment = () => Promise.reject(new ApiError("Toft answered 500", 500, {}));
    renderView();
    const vars = await card("Variables");
    expect(await within(vars).findByRole("heading", { name: "Couldn’t load this page", level: 2 })).toBeTruthy();
    expect(within(vars).queryByRole("button", { name: "Add variable" })).toBeNull();
  });

  it("shows no empty state and no Add while the variables are being read", async () => {
    listOwn = () => new Promise(() => {});
    renderView();
    const vars = await card("Variables");
    expect(within(vars).getByRole("status")).toBeTruthy();
    expect(within(vars).queryByText(/gets no variables yet/)).toBeNull();
    expect(within(vars).queryByRole("button", { name: "Add variable" })).toBeNull();
  });

  it("Save sends the version it read, then the one the PUT returned", async () => {
    bulkUpsert.mockImplementation(async (_scope, _version, vars) => ({ data: vars, version: "v2" }));
    renderView();
    await editValue("trace");
    fireEvent.click(screen.getByRole("button", { name: "Save variables" }));
    await waitFor(() => expect(bulkUpsert).toHaveBeenCalledTimes(1));
    expect(bulkUpsert.mock.calls[0][1]).toBe("v1");

    await editValue("warn");
    fireEvent.click(screen.getByRole("button", { name: "Save variables" }));
    await waitFor(() => expect(bulkUpsert).toHaveBeenCalledTimes(2));
    expect(bulkUpsert.mock.calls[1][1]).toBe("v2");
  });

  it("on a 409 says nothing was saved, keeps what was typed and can read them again", async () => {
    bulkUpsert.mockRejectedValueOnce(new ApiError("changed", 409, { code: "changed_since_read" }));
    renderView();
    await editValue("trace");
    fireEvent.click(screen.getByRole("button", { name: "Save variables" }));

    expect(await screen.findByText(/changed since you opened this page, so nothing was saved/)).toBeTruthy();
    expect(screen.getByDisplayValue("trace")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Save variables" }) as HTMLButtonElement).disabled).toBe(true);

    listOwn = async () => ({ data: [...own(), { key: "OTHER", value: "1", is_secret: false, masked: false }], version: "v9" });
    fireEvent.click(screen.getByRole("button", { name: "Read them again" }));
    expect(await screen.findByText("OTHER")).toBeTruthy();
    expect(screen.getByDisplayValue("trace")).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Save variables" })));

    bulkUpsert.mockImplementation(async (_scope, _version, vars) => ({ data: vars, version: "v10" }));
    fireEvent.click(screen.getByRole("button", { name: "Save variables" }));
    await waitFor(() => expect(bulkUpsert).toHaveBeenCalledTimes(2));
    expect(bulkUpsert.mock.calls[1][1]).toBe("v9");
  });

  it("lists what the container gets, each row naming where it comes from", async () => {
    renderView();
    const vars = await card("Variables");
    const sentry = rowOf(await within(vars).findByText("SENTRY_DSN"));
    expect(within(sentry).getByText("from production")).toBeTruthy();
    expect(within(sentry).getByRole("button", { name: "Override SENTRY_DSN" })).toBeTruthy();
    const log = rowOf(within(vars).getByText("LOG_LEVEL"));
    expect(within(log).getByText("debug")).toBeTruthy();
    expect(within(log).getByText("production's: info")).toBeTruthy();
    expect(within(log).getByText("overrides production’s")).toBeTruthy();
    expect(within(vars).getByText(/plain text, anyone in storefront can read them/)).toBeTruthy();
  });

  it("Override adds web's own copy with production's value, unsaved", async () => {
    renderView();
    const vars = await card("Variables");
    fireEvent.click(await within(vars).findByRole("button", { name: "Override SENTRY_DSN" }));
    const input = within(vars).getByDisplayValue("https://sentry");
    expect(within(rowOf(input)).getByText("Not saved")).toBeTruthy();
    expect(within(vars).getByText(/1 unsaved change:/)).toBeTruthy();
    expect(within(vars).getByRole("button", { name: "Save variables" })).toBeTruthy();
  });

  it("shows a new row's name error only once its field is left", async () => {
    renderView();
    const vars = await card("Variables");
    await within(vars).findByText("LOG_LEVEL");
    fireEvent.click(within(vars).getByRole("button", { name: "Add variable" }));
    const key = within(vars).getByPlaceholderText("NAME");
    expect(within(vars).queryByText("A name is required.")).toBeNull();
    expect((within(vars).getByRole("button", { name: "Save variables" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.blur(key);
    expect(within(vars).getByText("A name is required.")).toBeTruthy();
  });

  it("lists secrets without values; Replace… on production's makes web's own", async () => {
    renderView();
    const secrets = await card("Secrets");
    const app = rowOf(await within(secrets).findByText("APP_SECRET"));
    expect(within(app).getByText("from production")).toBeTruthy();
    expect(within(rowOf(within(secrets).getByText("REGION"))).getByText("overrides production’s")).toBeTruthy();
    fireEvent.click(within(app).getByRole("button", { name: "Replace APP_SECRET" }));
    expect(await screen.findByRole("heading", { name: "Replace APP_SECRET for web" })).toBeTruthy();
    expect((within(screen.getByRole("dialog")).getByLabelText("Key") as HTMLInputElement).readOnly).toBe(true);
  });

  it("gives viewers no way to change anything", async () => {
    renderView("variables", false);
    const vars = await card("Variables");
    await within(vars).findByText("SENTRY_DSN");
    expect(screen.queryByRole("button", { name: /^(Add variable|Add secret|Override|Edit|Replace)/ })).toBeNull();
  });

  it("opens at the Secrets card for old ?tab=secrets links", async () => {
    renderView("secrets");
    const heading = await screen.findByRole("heading", { name: "Secrets" });
    await waitFor(() => expect(document.activeElement).toBe(heading));
  });
});
