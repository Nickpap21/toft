import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, streamEvents } from "./client";
import { deploymentService } from "./deployment.service";
import { serviceService } from "./service.service";

/** A response whose body arrives in the given chunks. */
function sse(...chunks: string[]) {
  const enc = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      for (const chunk of chunks) c.enqueue(enc.encode(chunk));
      c.close();
    },
  });
  return new Response(body, { headers: { "Content-Type": "text/event-stream" } });
}

function stubFetch(res: Response) {
  const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(res);
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

/** Lets the stream's promise chain run to the end. */
const settle = () => new Promise((r) => setTimeout(r, 0));

afterEach(() => vi.unstubAllGlobals());

describe("streamEvents", () => {
  it("sends the token in a header, never the URL, and parses events split across chunks", async () => {
    localStorage.setItem("paas_token", "tok");
    const fetch = stubFetch(
      sse(": heartbeat\n\nevent: con", "nected\r\ndata: {}\r\n\ndata: one\ndata:two\n\nid: 7\nda", "ta: three\n\n")
    );
    const events: [string, string][] = [];
    await streamEvents("/api/v1/events", (t, d) => events.push([t, d]), new AbortController().signal);

    const [url, init] = fetch.mock.calls[0];
    expect(url).not.toContain("tok");
    expect(init?.headers).toEqual({ Authorization: "Bearer tok" });
    expect(events).toEqual([
      ["connected", "{}"],
      ["message", "one\ntwo"],
      ["message", "three"],
    ]);
  });

  it("rejects with the server's error for a non-2xx answer", async () => {
    stubFetch(Response.json({ error: "container is not part of this service" }, { status: 403 }));
    const err = await streamEvents("/x", () => {}, new AbortController().signal).catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.message).toBe("container is not part of this service");
    expect(err.status).toBe(403);
  });
});

describe("deploymentService.streamLogs", () => {
  const follow = () => {
    const lines: string[] = [];
    const onDone = vi.fn();
    const onError = vi.fn();
    const stop = deploymentService.streamLogs("s1", "d1", (l) => lines.push(l.message), onDone, onError);
    return { lines, onDone, onError, stop };
  };

  it("passes lines through and ends with the final status", async () => {
    stubFetch(
      sse(
        'id: 1\ndata: {"level":"info","message":"building","created_at":"x"}\n\n',
        "data: not json\n\n",
        'event: done\ndata: {"status":"running","runtime_status":"healthy"}\n\n'
      )
    );
    const { lines, onDone, onError } = follow();
    await settle();
    expect(lines).toEqual(["building", "not json"]);
    expect(onDone).toHaveBeenCalledOnce();
    expect(onDone).toHaveBeenCalledWith({ status: "running", runtime_status: "healthy" });
    expect(onError).not.toHaveBeenCalled();
  });

  it("reports a stream that ends without done", async () => {
    stubFetch(sse('data: {"message":"building"}\n\n'));
    const { onDone, onError } = follow();
    await settle();
    expect(onError).toHaveBeenCalledWith("Log stream disconnected unexpectedly.");
    expect(onDone).toHaveBeenCalledOnce();
  });

  it("is silent once stopped", async () => {
    stubFetch(sse('data: {"message":"building"}\n\n'));
    const { lines, onDone, onError, stop } = follow();
    stop();
    await settle();
    expect(lines).toEqual([]);
    expect(onDone).not.toHaveBeenCalled();
    expect(onError).not.toHaveBeenCalled();
  });
});

describe("serviceService.streamLiveLogs", () => {
  it("streams lines until done", async () => {
    const fetch = stubFetch(sse("data: a\n\ndata: b\n\nevent: done\ndata: {}\n\n"));
    const lines: string[] = [];
    const onDone = vi.fn();
    const onError = vi.fn();
    serviceService.streamLiveLogs("s1", (l) => lines.push(l), onDone, onError, 50, "web-1");
    await settle();
    expect(fetch.mock.calls[0][0]).toMatch(/\/api\/v1\/services\/s1\/logs\/live\?tail=50&container=web-1$/);
    expect(lines).toEqual(["a", "b"]);
    expect(onDone).toHaveBeenCalledOnce();
    expect(onError).not.toHaveBeenCalled();
  });

  it("asks for timestamps and reports the container it follows", async () => {
    const fetch = stubFetch(sse('event: container\ndata: {"name":"paas-web-blue","running":false}\n\ndata: 2026-10-14T14:02:11Z a\n\nevent: error\ndata: the log could not be followed\n\nevent: done\ndata: {}\n\n'));
    const lines: string[] = [];
    const onContainer = vi.fn();
    const onFailure = vi.fn();
    serviceService.streamLiveLogs("s1", (l) => lines.push(l), () => {}, undefined, 100, undefined, { timestamps: true, onContainer, onFailure });
    await settle();
    expect(fetch.mock.calls[0][0]).toMatch(/\/logs\/live\?tail=100&timestamps=1$/);
    expect(onContainer).toHaveBeenCalledWith("paas-web-blue", false);
    expect(onFailure).toHaveBeenCalledWith("the log could not be followed");
    expect(lines).toEqual(["2026-10-14T14:02:11Z a"]);
  });

  it("shows the server's refusal", async () => {
    stubFetch(Response.json({ error: "container is not part of this service" }, { status: 403 }));
    const onDone = vi.fn();
    const onError = vi.fn();
    serviceService.streamLiveLogs("s1", () => {}, onDone, onError);
    await settle();
    expect(onError).toHaveBeenCalledWith("container is not part of this service", 403);
    expect(onDone).toHaveBeenCalledOnce();
  });

  it("ends the session on a 401, as fetchAPI does", async () => {
    const location = vi.spyOn(window, "location", "get").mockReturnValue({ ...window.location, href: "", pathname: "/s", search: "" } as Location);
    localStorage.setItem("paas_token", "tok");
    stubFetch(Response.json({ error: "invalid or expired token" }, { status: 401 }));
    const onDone = vi.fn();
    const onError = vi.fn();
    serviceService.streamLiveLogs("s1", () => {}, onDone, onError);
    await settle();
    expect(localStorage.getItem("paas_token")).toBeNull();
    expect(sessionStorage.getItem("paas_session_expired")).toBe("1");
    expect(onError).toHaveBeenCalledWith("Session expired. Please sign in again.", 401);
    expect(onDone).toHaveBeenCalledOnce();
    location.mockRestore();
  });
});
