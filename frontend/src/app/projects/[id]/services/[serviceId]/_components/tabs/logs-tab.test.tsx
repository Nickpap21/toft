import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Service } from "@/lib/api/service.service";
import { ApiError } from "@/lib/api/client";
import { LogsTab } from "./logs-tab";

interface FakeStream {
  onLine: (line: string) => void;
  onDone: () => void;
  onError?: (msg: string, status?: number) => void;
  opts: { onContainer?: (name: string, running: boolean) => void; onFailure?: (message: string) => void };
}
const streams: FakeStream[] = [];
const latest = () => streams[streams.length - 1];

let containers: () => Promise<string[]> = async () => [];
let deployments: { id: string }[] = [];
let obs: Record<string, string> = {};

vi.mock("@/lib/api/service.service", async (orig) => {
  const mod = await orig<typeof import("@/lib/api/service.service")>();
  return {
    ...mod,
    serviceService: {
      ...mod.serviceService,
      listContainers: () => containers(),
      streamLiveLogs: (
        _id: string,
        onLine: FakeStream["onLine"],
        onDone: FakeStream["onDone"],
        onError: FakeStream["onError"],
        _tail: number,
        _container: string | undefined,
        opts: FakeStream["opts"],
      ) => {
        streams.push({ onLine, onDone, onError, opts });
        return () => {};
      },
    },
  };
});
vi.mock("@/lib/api/deployment.service", async (orig) => ({
  ...(await orig<typeof import("@/lib/api/deployment.service")>()),
  allDeploymentsQuery: (id: string) => ({ queryKey: ["deployments", id, "all"], queryFn: async () => deployments }),
}));
vi.mock("@/lib/api/observability.service", () => ({ observabilityService: { getForProject: async () => ({}), getForOrg: async () => obs } }));
vi.mock("@/lib/api/org.service", () => ({ orgService: { listAuditLog: async () => [] } }));

const web = { id: "svc-1", name: "web", runtime_status: "running", source_type: "docker-image", active_deployment_id: "d1" } as Service;

function tab(service: Service, onDeployments: () => void = () => {}) {
  const props = { serviceId: service.id, projectId: "p1", serverName: "edge-1", serverId: "srv-1", canDeploy: true, deployLabel: "Deploy", onDeploy: () => {}, onDeployments };
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const ui = (s: Service) => (
    <QueryClientProvider client={client}>
      <LogsTab service={s} {...props} />
    </QueryClientProvider>
  );
  const view = render(ui(service));
  return { ...view, update: (s: Service) => view.rerender(ui(s)) };
}

/** Lets a batch of lines reach the screen. */
const tick = (ms = 60) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });
const stamp = (sec: number) => new Date(new Date("2026-11-09T12:00:00Z").getTime() + sec * 1000).toISOString();
async function write(...texts: string[]) {
  act(() => texts.forEach((t, i) => latest().onLine(`${stamp(i)} ${t}`)));
  await tick();
}
/** Lines at given seconds, as a follow that reads its tail again sends them. */
async function writeAt(...entries: [number, string][]) {
  act(() => entries.forEach(([sec, t]) => latest().onLine(`${stamp(sec)} ${t}`)));
  await tick();
}
const follow = (name = "paas-web-blue", running = true) => act(() => latest().opts.onContainer?.(name, running));
const end = async () => { act(() => latest().onDone()); await tick(0); };

beforeEach(() => {
  vi.useFakeTimers({ now: new Date("2026-11-09T12:30:00Z"), toFake: ["setTimeout", "clearTimeout", "Date"] });
  streams.length = 0;
  containers = async () => [];
  deployments = [];
  obs = {};
});
afterEach(() => { vi.useRealTimers(); });

describe("the time column", () => {
  it("dates a line from another day and titles every line with its zone", async () => {
    tab(web);
    follow();
    act(() => latest().onLine("2026-11-03T12:23:10Z old crash"));
    await tick();
    const old = new Date("2026-11-03T12:23:10Z");
    const cell = screen.getByText(new RegExp(`^Nov 3 `));
    expect(cell.textContent).toMatch(/^Nov 3 \d\d:\d\d:\d\d$/);
    expect(cell.getAttribute("title")).toContain(String(old.getFullYear()));
  });
});

describe("what the empty list says", () => {
  it("says a filter and a level covered only the lines loaded", async () => {
    tab(web);
    follow();
    await write("one", "two", "three");

    fireEvent.change(screen.getByRole("searchbox", { name: "Filter lines" }), { target: { value: "zzz" } });
    expect(screen.getByText("No line matches “zzz” in the last 3 lines.")).toBeTruthy();

    fireEvent.change(screen.getByRole("searchbox", { name: "Filter lines" }), { target: { value: "" } });
    const errors = screen.getByRole("button", { name: /Errors/ });
    expect(errors.getAttribute("title")).toBe("In the last 3 lines");
    fireEvent.click(errors);
    expect(screen.getByText("No errors in the last 3 lines.")).toBeTruthy();
  });

  it("names the level a filter searched", async () => {
    tab(web);
    follow();
    await write("foo", "ERROR bar", "WARN baz");
    fireEvent.click(screen.getByRole("button", { name: /Errors/ }));
    fireEvent.change(screen.getByRole("searchbox", { name: "Filter lines" }), { target: { value: "foo" } });
    expect(screen.getByText("No error matches “foo” in the last 3 lines.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /Warnings/ }));
    expect(screen.getByText("No warning matches “foo” in the last 3 lines.")).toBeTruthy();
  });
});

describe("a follow that ends", () => {
  it("reconnects by itself, keeps the lines, and shows the new follow's", async () => {
    tab(web);
    follow();
    await writeAt([0, "older than the new tail"], [10, "before the crash"]);
    await end();

    expect(screen.getByText("Reconnecting…")).toBeTruthy();
    expect(screen.queryByText("Disconnected")).toBeNull();
    expect(screen.queryByRole("button", { name: "Reconnect" })).toBeNull();
    expect(screen.getByText("before the crash")).toBeTruthy();
    expect(streams).toHaveLength(1);

    await tick(2000);
    expect(streams).toHaveLength(2);
    expect(screen.getByText("Reconnecting…")).toBeTruthy();

    follow();
    expect(screen.queryByText("Reconnecting…")).toBeNull();
    expect(screen.getByText("Live")).toBeTruthy();

    await writeAt([10, "before the crash"], [20, "after the restart"]);
    expect(screen.getByText("older than the new tail")).toBeTruthy();
    expect(screen.getAllByText("before the crash")).toHaveLength(1);
    expect(screen.getByText("after the restart")).toBeTruthy();
    const log = screen.getByRole("log").textContent ?? "";
    expect(log.indexOf("older than the new tail")).toBeLessThan(log.indexOf("before the crash"));
    expect(log.indexOf("before the crash")).toBeLessThan(log.indexOf("web restarted"));
    expect(log.indexOf("web restarted")).toBeLessThan(log.indexOf("after the restart"));
  });

  it("replaces the list outright when the new follow's first line has no time", async () => {
    tab(web);
    follow();
    await writeAt([0, "old line"]);
    await end();
    await tick(2000);
    follow();
    act(() => latest().onLine("a line with no time"));
    await tick();
    expect(screen.queryByText("old line")).toBeNull();
    expect(screen.getByText("a line with no time")).toBeTruthy();
  });

  it("keeps each restart where it happened when the tail is read again", async () => {
    tab(web);
    follow();
    await writeAt([0, "first"]);
    await end();
    await tick(2000);
    follow();
    await writeAt([0, "first"], [10, "second"]);
    await end();
    await tick(5000);
    follow();
    await writeAt([0, "first"], [10, "second"], [20, "third"]);
    expect(screen.getAllByText("web restarted")).toHaveLength(2);
    const log = screen.getByRole("log").textContent ?? "";
    expect(log.indexOf("first")).toBeLessThan(log.indexOf("web restarted"));
    expect(log.indexOf("second")).toBeLessThan(log.lastIndexOf("web restarted"));
    expect(log.lastIndexOf("web restarted")).toBeLessThan(log.indexOf("third"));
  });

  it("says a container keeps restarting, not that a connection closed", async () => {
    tab(web);
    follow();
    await write("boot");
    await end();
    for (const wait of [2000, 5000, 10000, 15000, 30000]) {
      await tick(wait);
      follow();
      await end();
    }
    const alert = screen.getByRole("alert").textContent ?? "";
    expect(alert).toContain("web keeps restarting.");
    expect(alert).not.toContain("connection");
  });

  it("names a restart while paused as a restart", async () => {
    tab(web);
    follow();
    await write("line");
    fireEvent.click(screen.getByRole("button", { name: "Pause" }));
    await end();
    const alert = screen.getByRole("alert").textContent ?? "";
    expect(alert).toMatch(/web restarted at \d\d:\d\d:\d\d\./);
    expect(alert).not.toContain("connection");
  });

  it("says the connection closed when it did", async () => {
    tab(web);
    follow();
    for (const wait of [2000, 5000, 10000, 15000, 30000]) {
      act(() => latest().onError?.("Live log stream disconnected."));
      await end();
      await tick(wait);
    }
    act(() => latest().onError?.("Live log stream disconnected."));
    await end();
    expect(screen.getByRole("alert").textContent).toMatch(/The connection to Toft closed at \d\d:\d\d:\d\d\./);
    expect(screen.queryByText("web keeps restarting.")).toBeNull();
  });

  it("gives up after a few tries over about a minute, and Reconnect still works", async () => {
    tab(web);
    follow();
    await end();
    for (const wait of [2000, 5000, 10000, 15000, 30000]) {
      expect(screen.getByText("Reconnecting…")).toBeTruthy();
      await tick(wait);
      await end();
    }
    expect(streams).toHaveLength(6);
    expect(screen.getByText("Disconnected")).toBeTruthy();
    await tick(120_000);
    expect(streams).toHaveLength(6);

    fireEvent.click(screen.getByRole("button", { name: "Reconnect" }));
    expect(streams).toHaveLength(7);
  });

  it("counts tries afresh after a follow that held", async () => {
    tab(web);
    follow();
    await end();
    await tick(2000);
    follow();
    await tick(60_000);
    await end();
    expect(screen.getByText("Reconnecting…")).toBeTruthy();
    await tick(2000);
    expect(streams).toHaveLength(3);
  });

  it("stops reconnecting when paused", async () => {
    tab(web);
    follow();
    await write("line");
    await end();
    fireEvent.click(screen.getByRole("button", { name: "Pause" }));
    expect(screen.getByText("Disconnected")).toBeTruthy();
    await tick(120_000);
    expect(streams).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Reconnect" })).toBeTruthy();
  });

  it("does not reconnect while paused either", async () => {
    tab(web);
    follow();
    await write("line");
    fireEvent.click(screen.getByRole("button", { name: "Pause" }));
    await end();
    await tick(120_000);
    expect(streams).toHaveLength(1);
    expect(screen.getByText("Disconnected")).toBeTruthy();
  });
});

describe("a service with no container", () => {
  const none = "no_container: this service has no container to follow";

  it("while it deploys, looks again until its container appears", async () => {
    tab({ ...web, runtime_status: "deploying" } as Service);
    act(() => latest().opts.onFailure?.(none));
    await end();
    expect(screen.getByText("web hasn’t started yet")).toBeTruthy();
    expect(screen.queryByText("Disconnected")).toBeNull();
    expect(screen.queryByRole("button", { name: "Pause" })).toBeNull();
    expect(streams).toHaveLength(1);

    await tick(5000);
    expect(streams).toHaveLength(2);
    act(() => latest().opts.onFailure?.(none));
    await end();
    await tick(5000);
    expect(streams).toHaveLength(3);

    follow("paas-web-green");
    await write("booting");
    expect(screen.queryByText("web hasn’t started yet")).toBeNull();
    expect(screen.getByText("booting")).toBeTruthy();
  });

  it("after a failed deploy, stops looking and claims nothing about what started", async () => {
    tab({ ...web, runtime_status: "error" } as Service);
    act(() => latest().opts.onFailure?.(none));
    await end();
    await tick(120_000);
    expect(streams).toHaveLength(1);
    expect(screen.getByText("web has no container")).toBeTruthy();
    expect(screen.queryByText(/before a container started/)).toBeNull();
    expect(screen.getByText("No container")).toBeTruthy();
  });

  it("looks again when a new deploy starts", async () => {
    const { update } = tab({ ...web, runtime_status: "error" } as Service);
    act(() => latest().opts.onFailure?.(none));
    await end();
    update({ ...web, runtime_status: "deploying" } as Service);
    await tick(0);
    expect(streams).toHaveLength(2);
  });

  it.each(["deploying", "error"])("any other failure before its container event, while %s, takes the normal path", async (runtime_status) => {
    tab({ ...web, runtime_status } as Service);
    act(() => latest().opts.onFailure?.("the log could not be followed"));
    await end();
    expect(screen.queryByText(/web has no container|web hasn’t started/)).toBeNull();
    expect(screen.getByText("Reconnecting…")).toBeTruthy();
    await tick(2000);
    expect(streams).toHaveLength(2);
  });

  it("links the latest deployment's output", async () => {
    deployments = [{ id: "d9" }, { id: "d8" }];
    tab({ ...web, runtime_status: "error" } as Service);
    act(() => latest().opts.onFailure?.(none));
    await end();
    await tick(0);
    expect(screen.getByRole("link", { name: "Open its deployment" }).getAttribute("href")).toContain("deployment=d9");
  });

  it("with none listed, opens the Deploys tab and says that", async () => {
    const onDeployments = vi.fn();
    tab({ ...web, runtime_status: "error" } as Service, onDeployments);
    act(() => latest().opts.onFailure?.(none));
    await end();
    await tick(0);
    fireEvent.click(screen.getByRole("button", { name: "See its deploys" }));
    expect(onDeployments).toHaveBeenCalledOnce();
  });
});

describe("pausing", () => {
  it("is cleared by a new stream", async () => {
    const { update } = tab(web);
    follow();
    await write("from the old deployment");
    fireEvent.click(screen.getByRole("button", { name: "Pause" }));
    expect(screen.getAllByRole("button", { name: "Resume" }).length).toBeGreaterThan(0);

    update({ ...web, active_deployment_id: "d2" } as Service);
    await tick(0);
    expect(streams).toHaveLength(2);
    follow();
    expect(screen.queryByRole("button", { name: "Resume" })).toBeNull();
    await write("from the new deployment");
    expect(screen.getByText("from the new deployment")).toBeTruthy();
    expect(screen.queryByText(/held/)).toBeNull();
  });

  it("doesn't say nothing was written while lines are held", async () => {
    tab(web);
    follow();
    await tick(0);
    fireEvent.click(screen.getByRole("button", { name: "Pause" }));
    await write("held line");
    expect(screen.queryByText("web hasn’t written anything yet.")).toBeNull();
    expect(screen.getByText(/1 new line is held/)).toBeTruthy();
  });
});

describe("a container that stops", () => {
  it("shows the lines held when it ends", async () => {
    tab(web);
    follow();
    await write("first");
    fireEvent.click(screen.getByRole("button", { name: "Pause" }));
    await write("FATAL: out of memory");
    expect(screen.queryByText("FATAL: out of memory")).toBeNull();

    act(() => latest().opts.onContainer?.("paas-web-blue", false));
    await end();
    expect(screen.getByText("FATAL: out of memory")).toBeTruthy();
    expect(screen.getByText(/These are the last lines it wrote/)).toBeTruthy();
    expect(screen.queryByText(/held/)).toBeNull();
  });

  it("doesn't call nothing the last lines", async () => {
    tab(web);
    follow("paas-web-blue", false);
    await end();
    expect(screen.queryByText(/These are the last lines/)).toBeNull();
    expect(screen.getByText(/It wrote nothing before it stopped/)).toBeTruthy();
  });
});

describe("a failed read", () => {
  const alertText = () => screen.getByRole("alert").textContent ?? "";

  it("says what the API said for a 403 and doesn't retry", async () => {
    tab(web);
    act(() => latest().onError?.("that container does not belong to this service", 403));
    await end();
    await tick(120_000);
    expect(alertText()).toContain("That container does not belong to this service.");
    expect(streams).toHaveLength(1);
    expect(screen.queryByRole("button", { name: "Reconnect" })).toBeNull();
    expect(screen.getByText("Unavailable")).toBeTruthy();
    expect(screen.queryByText("Disconnected")).toBeNull();
  });

  it("says what the API said for a 404", async () => {
    tab(web);
    act(() => latest().onError?.("service not found", 404));
    await end();
    expect(alertText()).toContain("Service not found.");
  });

  it.each([
    ["a 500", (s: FakeStream) => s.onError?.("Failed", 500)],
    ["the server's own message", (s: FakeStream) => s.opts.onFailure?.("the log could not be followed")],
  ])("says the log couldn't be read after %s, claiming nothing about the service", async (_name, failure) => {
    tab(web);
    follow();
    for (const wait of [2000, 5000, 10000, 15000, 30000]) {
      act(() => failure(latest()));
      await end();
      expect(screen.getByText("Reconnecting…")).toBeTruthy();
      await tick(wait);
    }
    act(() => failure(latest()));
    await end();
    expect(alertText()).toContain("The log couldn’t be read.");
    expect(alertText()).not.toContain("still running");
  });

  it("names a server that can't be reached and links it", async () => {
    tab(web);
    const down = "server_unreachable: this service's server can't be reached";
    for (const wait of [2000, 5000, 10000, 15000, 30000]) {
      act(() => latest().opts.onFailure?.(down));
      await end();
      expect(screen.getByText("Reconnecting…")).toBeTruthy();
      await tick(wait);
    }
    act(() => latest().opts.onFailure?.(down));
    await end();
    expect(alertText()).toContain("edge-1 can’t be reached.");
    expect(screen.getByRole("link", { name: "Open edge-1" }).getAttribute("href")).toBe("/servers/srv-1");
    expect(screen.getByRole("button", { name: "Reconnect" })).toBeTruthy();
  });

  it("says a viewer fell behind in its own words", async () => {
    tab(web);
    follow();
    for (const wait of [2000, 5000, 10000, 15000, 30000]) {
      await end();
      await tick(wait);
    }
    act(() => latest().opts.onFailure?.("fell behind the log; reconnect"));
    await end();
    expect(alertText()).toContain("The log moved faster than this page could read it.");
    expect(alertText()).not.toContain("(fell");
  });

  it("is not retried once the session has ended", async () => {
    tab(web);
    act(() => latest().onError?.("Session expired. Please sign in again.", 401));
    await end();
    await tick(120_000);
    expect(streams).toHaveLength(1);
  });
});

describe("the level views", () => {
  it("keeps the row in view when going back to All", async () => {
    const scrollIntoView = vi.spyOn(Element.prototype, "scrollIntoView");
    tab(web);
    follow();
    await write("one", "ERROR the crash", "three");
    fireEvent.click(screen.getByRole("button", { name: /Errors/ }));
    scrollIntoView.mockClear();

    fireEvent.click(screen.getByRole("button", { name: /^All/ }));
    expect(scrollIntoView).toHaveBeenCalledOnce();
    const row = scrollIntoView.mock.contexts[0] as HTMLElement;
    expect(within(row).getByText("ERROR the crash")).toBeTruthy();
    scrollIntoView.mockRestore();
  });
});

describe("the filter", () => {
  it("keeps the row in view when it is cleared", async () => {
    const scrollIntoView = vi.spyOn(Element.prototype, "scrollIntoView");
    tab(web);
    follow();
    await write("one", "the crash", "three");
    const box = screen.getByRole("searchbox", { name: "Filter lines" });
    fireEvent.change(box, { target: { value: "crash" } });
    expect(scrollIntoView).not.toHaveBeenCalled();

    fireEvent.change(box, { target: { value: "" } });
    expect(scrollIntoView).toHaveBeenCalledOnce();
    const row = scrollIntoView.mock.contexts[0] as HTMLElement;
    expect(within(row).getByText("the crash")).toBeTruthy();
    scrollIntoView.mockRestore();
  });

  it("also finds what a JSON line's fields carried", async () => {
    tab(web);
    follow();
    act(() => latest().onLine(`${stamp(0)} {"level":"info","hostname":"web-1","msg":"ready"}`));
    await tick();
    fireEvent.change(screen.getByRole("searchbox", { name: "Filter lines" }), { target: { value: "web-1" } });
    expect(screen.getByText("ready")).toBeTruthy();
  });
});

describe("a compose service", () => {
  it("says when its other containers can't be listed", async () => {
    containers = async () => { throw new ApiError("boom", 500, null); };
    tab({ ...web, source_type: "docker-compose" } as Service);
    follow();
    await tick(0);
    expect(screen.getByText("The other containers couldn’t be listed.")).toBeTruthy();
    expect(screen.queryByRole("combobox", { name: "Container" })).toBeNull();

    containers = async () => ["paas-web-svc-1-web-1", "paas-web-svc-1-db-1"];
    fireEvent.click(screen.getByRole("button", { name: /^Retry/ }));
    await tick(0);
    expect(screen.queryByText("The other containers couldn’t be listed.")).toBeNull();
    expect(screen.getByRole("combobox", { name: "Container" })).toBeTruthy();
  });
});

describe("the footer", () => {
  const footer = () => screen.getByRole("log").parentElement?.textContent ?? "";

  it("says the list is the current container's, where the zone is, and where older lines are", async () => {
    tab(web);
    follow();
    await tick(0);
    expect(footer()).toContain("The current container’s stdout and stderr");
    expect(footer()).toContain("Counts cover the lines loaded here.");
    expect(footer()).toMatch(/Times are in your time zone \(.+\)\./);
    expect(footer()).toContain("Older lines stay in Docker’s logs on edge-1.");
  });

  it("points older lines at Grafana when there is one", async () => {
    obs = { grafana_url: "https://grafana.example", loki_url: "http://loki:3100" };
    tab(web);
    follow();
    await tick(0);
    expect(screen.getByRole("link", { name: "Grafana" }).getAttribute("href")).toBe("https://grafana.example");
    expect(footer()).not.toContain("Docker’s logs on");
  });

  it("points older lines at Workspace services with Loki and no Grafana", async () => {
    obs = { loki_url: "http://loki:3100" };
    tab(web);
    follow();
    await tick(0);
    expect(screen.getByRole("link", { name: "Workspace services → Logs and metrics" }).getAttribute("href")).toBe("/shared?tab=logs");
    expect(footer()).not.toContain("Docker’s logs on");
  });
});
