import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import {
  formatDay,
  formatMiB,
  type ResourceRow,
  type ServiceResources,
} from "@/lib/api/resources.service";
import type { Service } from "@/lib/api/service.service";
import { ComposeResources, ReservedBars, SingleResources } from "./settings-resources";
import { initForm, type ConfigForm } from "./settings-form";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ user: null }) }));

const getResources = vi.fn();
const setComposeLimits = vi.fn();
vi.mock("@/lib/api/resources.service", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/resources.service")>()),
  resourcesService: {
    service: (...args: unknown[]) => getResources(...args),
    setComposeLimits: (...args: unknown[]) => setComposeLimits(...args),
  },
}));

const noCPU: ResourceRow["cpu"] = {
  limit: "",
  p95_cores: null,
  throttled_max_pct: null,
  wait_avg_pct: null,
  suggestion: null,
};
const noMemory: ResourceRow["memory"] = {
  limit: "",
  source: "none",
  file_limit: "",
  peak_14d_mb: null,
  days: 0,
  oom_kills_7d: 0,
  suggestion: null,
};

const single: ServiceResources = {
  grace_until: "2026-10-26T12:00:00Z",
  latency_24h: null,
  rows: [
    {
      compose_service: "",
      kind: "app",
      restarts_24h: null,
      restarts_since: null,
      memory: {
        limit: "1g",
        source: "panel",
        file_limit: "",
        peak_14d_mb: 200,
        days: 2,
        oom_kills_7d: 1,
        suggestion: {
          direction: "down",
          value: "320m",
          reason: "Peak 200m of 1g over 7 days",
          days: 7,
        },
      },
      cpu: { ...noCPU, p95_cores: 0.12, wait_avg_pct: 3.1 },
    },
  ],
};

const stack: ServiceResources & { version: string } = {
  version: "v1",
  grace_until: "2026-10-26T12:00:00Z",
  latency_24h: null,
  rows: [
    {
      compose_service: "db",
      kind: "database",
      restarts_24h: null,
      restarts_since: null,
      memory: {
        ...noMemory,
        peak_14d_mb: 240,
        suggestion: {
          direction: "set",
          value: "320m",
          reason: "Peak 240m over 3 days, with 30% headroom",
          days: 3,
        },
      },
      cpu: noCPU,
    },
    {
      compose_service: "web",
      kind: "app",
      restarts_24h: null,
      restarts_since: null,
      memory: {
        ...noMemory,
        limit: "256m",
        source: "panel",
        file_limit: "512m",
        peak_14d_mb: 100,
      },
      cpu: { ...noCPU, limit: "0.5" },
    },
    {
      compose_service: "worker",
      kind: "app",
      restarts_24h: null,
      restarts_since: null,
      memory: {
        ...noMemory,
        limit: "512m",
        source: "compose_file",
        file_limit: "512m",
        peak_14d_mb: 90,
      },
      cpu: { ...noCPU, limit: "1" },
    },
  ],
};

const service = {
  id: "svc-1",
  name: "api",
  source_type: "docker-image",
  memory_limit: "1g",
} as unknown as Service;

function wrap(ui: React.ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
  return client;
}

function Single({
  canEdit,
  memory,
  onForm,
}: {
  canEdit: boolean;
  memory: string;
  onForm: (f: ConfigForm) => void;
}) {
  const [form, setForm] = useState<ConfigForm>({
    ...initForm(service),
    memory_limit: memory,
  });
  return (
    <SingleResources
      service={service}
      serviceId="svc-1"
      form={form}
      set={(p) =>
        setForm((f) => {
          const n = { ...f, ...p };
          onForm(n);
          return n;
        })
      }
      canEdit={canEdit}
    />
  );
}

function renderSingle(
  data: ServiceResources,
  { canEdit = true, memory = "1g" } = {},
) {
  getResources.mockResolvedValue(data);
  const onForm = vi.fn();
  wrap(<Single canEdit={canEdit} memory={memory} onForm={onForm} />);
  return { onForm };
}

afterEach(() => {
  vi.useRealTimers();
  vi.clearAllMocks();
});

describe("SingleResources", () => {
  it("shows a down suggestion with its reason, and Use fills the field for the card's Save", async () => {
    const { onForm } = renderSingle(single);

    expect(await screen.findByText("Peak 200m of 1g over 7 days")).toBeTruthy();
    expect(screen.getByText("Lower to 320 MiB")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Use 320 MiB" }));
    expect(onForm).toHaveBeenLastCalledWith(
      expect.objectContaining({ memory_limit: "320m" }),
    );
    expect(
      (screen.getByLabelText("Memory limit") as HTMLInputElement).value,
    ).toBe("320m");
    expect(
      (
        screen.getByRole("button", {
          name: "In the field",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
  });

  it("shows the memory kills of the last 7 days", async () => {
    renderSingle(single);
    expect(
      await screen.findByText("killed for memory 1 time in 7 days"),
    ).toBeTruthy();
  });

  it("lets a viewer see the suggestion but not use it", async () => {
    renderSingle(single, { canEdit: false });
    expect(await screen.findByText("Peak 200m of 1g over 7 days")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Use / })).toBeNull();
  });

  it("says it is measuring while a service without a limit has no suggestion yet", async () => {
    renderSingle(
      {
        grace_until: null,
        latency_24h: null,
        rows: [
          { compose_service: "", kind: "app", memory: noMemory, cpu: noCPU, restarts_24h: null, restarts_since: null },
        ],
      },
      { memory: "" },
    );
    expect(
      await screen.findByText(
        "Measuring: a suggestion from use appears after a day of samples.",
      ),
    ).toBeTruthy();
  });

  it("does not say it is measuring for a service that has a limit", async () => {
    renderSingle(
      {
        grace_until: null,
        latency_24h: null,
        rows: [
          {
            compose_service: "",
            kind: "app",
            restarts_24h: null,
            restarts_since: null,
            memory: { ...noMemory, limit: "512m", source: "panel" },
            cpu: noCPU,
          },
        ],
      },
      { memory: "512m" },
    );
    expect(await screen.findByText("512 MiB limit")).toBeTruthy();
    expect(screen.queryByText(/Measuring/)).toBeNull();
  });

  it("offers the kind's default once grace is over, without the measuring note", async () => {
    const defaultSet = {
      direction: "set" as const,
      value: "512m",
      reason: "Default for app; no usage measured yet",
      days: 0,
    };
    renderSingle(
      {
        grace_until: null,
        latency_24h: null,
        rows: [
          {
            compose_service: "",
            kind: "app",
            restarts_24h: null,
            restarts_since: null,
            memory: { ...noMemory, suggestion: defaultSet },
            cpu: noCPU,
          },
        ],
      },
      { memory: "" },
    );
    expect(await screen.findByText("Set 512 MiB")).toBeTruthy();
    expect(
      screen.getByText("Default for app; no usage measured yet"),
    ).toBeTruthy();
    expect(screen.queryByText(/Measuring/)).toBeNull();
  });

  it("names the day a memory limit becomes required", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-10-12T09:00:00Z"));
    renderSingle(
      {
        grace_until: "2026-10-19T12:00:00Z",
        latency_24h: null,
        rows: [
          { compose_service: "", kind: "app", memory: noMemory, cpu: noCPU, restarts_24h: null, restarts_since: null },
        ],
      },
      { memory: "" },
    );
    expect(
      await screen.findByText(/A memory limit is required from 19 Oct/),
    ).toBeTruthy();
  });
});

describe("ComposeResources", () => {
  function renderStack(data: ServiceResources & { version: string }, canEdit = true) {
    getResources.mockResolvedValue(data);
    const onSaved = vi.fn();
    const compose = { ...service, source_type: "docker-compose" } as Service;
    const client = wrap(
      <ComposeResources
        service={compose}
        serviceId="svc-1"
        canEdit={canEdit}
        onSaved={onSaved}
      />,
    );
    return { onSaved, client };
  }

  it("shows one row per compose service, and saves the edit with the stack's other panel limits", async () => {
    setComposeLimits.mockResolvedValue(stack);
    const { onSaved } = renderStack(stack);

    expect(await screen.findByLabelText("Memory limit of db")).toBeTruthy();
    expect(screen.getByLabelText("Memory limit of web")).toBeTruthy();
    expect(screen.getByLabelText("Memory limit of worker")).toBeTruthy();
    expect(screen.getByText("512 MiB from the compose file")).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: "Use 320 MiB" }));
    expect(screen.getByText("1 unsaved change")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    // worker's limits are the compose file's: the panel sends none for it.
    expect(setComposeLimits).toHaveBeenCalledWith("svc-1", "v1", {
      db: { memory_mb: 320, cpu_cores: null },
      web: { memory_mb: 256, cpu_cores: 0.5 },
    });
    await vi.waitFor(() => expect(onSaved).toHaveBeenCalled());
  });

  it("says so when the limits changed since they were read, keeps what was typed, and saves on the fresh read", async () => {
    setComposeLimits.mockRejectedValueOnce(
      new ApiError("changed", 409, { code: "changed_since_read" }),
    );
    renderStack(stack);

    const db = await screen.findByLabelText("Memory limit of db");
    await userEvent.clear(db);
    await userEvent.type(db, "700m");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByText(/changed since you opened this page/)).toBeTruthy();
    expect((screen.getByLabelText("Memory limit of db") as HTMLInputElement).value).toBe("700m");
    expect((screen.getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(true);

    // Someone else set worker's limit meanwhile: it comes in, db stays typed.
    const fresh = {
      ...stack,
      version: "v2",
      rows: stack.rows.map((r) =>
        r.compose_service === "worker"
          ? { ...r, memory: { ...r.memory, limit: "128m", source: "panel" as const } }
          : r,
      ),
    };
    getResources.mockResolvedValue(fresh);
    await userEvent.click(screen.getByRole("button", { name: "Read them again" }));
    await vi.waitFor(() =>
      expect((screen.getByLabelText("Memory limit of worker") as HTMLInputElement).value).toBe("128m"),
    );
    expect((screen.getByLabelText("Memory limit of db") as HTMLInputElement).value).toBe("700m");
    await vi.waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("button", { name: "Save" })),
    );

    setComposeLimits.mockResolvedValue(fresh);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(setComposeLimits).toHaveBeenLastCalledWith("svc-1", "v2", {
      db: { memory_mb: 700, cpu_cores: null },
      web: { memory_mb: 256, cpu_cores: 0.5 },
      worker: { memory_mb: 128, cpu_cores: 1 },
    });
  });

  it("keeps what was typed through a background read, and Save sends the read it was typed on", async () => {
    setComposeLimits.mockResolvedValue(stack);
    const { client } = renderStack(stack);
    await userEvent.type(await screen.findByLabelText("Memory limit of db"), "700m");

    // A new measurement, and someone else's limit: the read changes under the form.
    getResources.mockResolvedValue({
      ...stack,
      version: "v2",
      rows: stack.rows.map((r) =>
        r.compose_service === "web"
          ? { ...r, memory: { ...r.memory, limit: "300m", peak_14d_mb: 180 } }
          : r,
      ),
    });
    await act(async () => {
      await client.refetchQueries();
    });
    expect(await screen.findByText("180 MiB")).toBeTruthy();

    expect((screen.getByLabelText("Memory limit of db") as HTMLInputElement).value).toBe("700m");
    expect(screen.getByText("1 unsaved change")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(setComposeLimits).toHaveBeenLastCalledWith("svc-1", "v1", {
      db: { memory_mb: 700, cpu_cores: null },
      web: { memory_mb: 256, cpu_cores: 0.5 },
    });
  });

  it("keeps the draft, Save and the banner when reading again fails, so it can be tried again", async () => {
    setComposeLimits.mockRejectedValueOnce(
      new ApiError("changed", 409, { code: "changed_since_read" }),
    );
    renderStack(stack);
    await userEvent.type(await screen.findByLabelText("Memory limit of db"), "700m");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("alert")).toBeTruthy();

    getResources.mockRejectedValue(new ApiError("boom", 500, {}));
    await userEvent.click(screen.getByRole("button", { name: "Read them again" }));
    await vi.waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect((screen.getByLabelText("Memory limit of db") as HTMLInputElement).value).toBe("700m");
    expect(screen.getByRole("button", { name: "Save" })).toBeTruthy();

    getResources.mockResolvedValue({ ...stack, version: "v2" });
    await userEvent.click(screen.getByRole("button", { name: "Read them again" }));
    await vi.waitFor(() =>
      expect((screen.getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(false),
    );
  });

  it("offers Retry in place of the form when the first read failed", async () => {
    getResources.mockRejectedValueOnce(new ApiError("boom", 500, {}));
    renderStack(stack);
    await userEvent.click(await screen.findByRole("button", { name: /^Retry/ }));
    expect(await screen.findByLabelText("Memory limit of db")).toBeTruthy();
  });

  it("refuses a CPU limit without a memory limit, which the API would reject", async () => {
    renderStack(stack);
    await userEvent.type(
      await screen.findByLabelText("CPU limit of db"),
      "0.5",
    );
    expect(screen.getByText(/a CPU limit needs a memory limit/)).toBeTruthy();
    expect(
      (screen.getByRole("button", { name: "Save" }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);
  });

  it("names the compose services that have no memory limit", async () => {
    renderStack(stack);
    expect(
      await screen.findByText(
        /A memory limit is required on every compose service/,
      ),
    ).toBeTruthy();
  });

  it("lets a viewer see the suggestion but not use it", async () => {
    renderStack(stack, false);
    expect(await screen.findByText("Set 320 MiB")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Use / })).toBeNull();
  });
});

describe("formatMiB", () => {
  it("writes GiB with one decimal from 1 GiB, MiB under it", () => {
    expect(formatMiB(704)).toBe("704 MiB");
    expect(formatMiB(1024)).toBe("1.0 GiB");
    expect(formatMiB(2150)).toBe("2.1 GiB");
  });

  it("writes days as the backend does: 19 Sep, not the browser's 19 Sept, in UTC", () => {
    expect(formatDay("2026-09-19T00:00:00Z")).toBe("19 Sep");
    expect(formatDay("2026-10-20T00:00:00Z")).toBe("20 Oct");
    expect(formatDay("2026-10-19")).toBe("19 Oct");
  });
});

describe("ReservedBars on the panel's server", () => {
  const worker = { id: "s1", name: "worker", server_id: "" } as unknown as Service;
  function show(allocatedMiB: number) {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      data: { role: "apps", reservation: { apps_memory_bytes: 747 * 2 ** 20, apps_allocated_memory_bytes: allocatedMiB * 2 ** 20, enforced: true } },
    }), { status: 200 })));
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <ReservedBars service={worker} />
      </QueryClientProvider>,
    );
  }
  afterEach(() => vi.unstubAllGlobals());

  it("shows what is reserved of what the panel's server leaves its apps, and what is free", async () => {
    show(512);
    expect(await screen.findByText("512 MiB of 747 MiB")).toBeTruthy();
    expect(screen.getByText("235 MiB")).toBeTruthy();
    expect(screen.queryByRole("img", { name: "Warning" })).toBeNull();
  });

  it("warns when more is reserved than the apps get: a service can be killed below its own limit", async () => {
    show(1946);
    expect(await screen.findByText("1.9 GiB of 747 MiB")).toBeTruthy();
    expect(screen.getByRole("img", { name: "Warning" })).toBeTruthy();
    expect(screen.getByText(/so one can be\s+killed for memory before it reaches its own limit/)).toBeTruthy();
  });
});
