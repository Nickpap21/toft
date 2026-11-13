import { describe, expect, it } from "vitest";
import { ansiSpans, clockTime, composeServiceOf, lineStamp, lineTime, readLine, splitStamp, startOfDay, zoneName } from "./log-view";

describe("splitStamp", () => {
  it("takes Docker's timestamp off a line", () => {
    const { at, rest } = splitStamp("2026-10-14T14:02:11.204123456Z GET /v1/cart 200");
    expect(rest).toBe("GET /v1/cart 200");
    expect(at).toBe(Date.parse("2026-10-14T14:02:11.204Z"));
  });

  it("leaves a line without one as it is", () => {
    expect(splitStamp("2026/10/14 14:02:01 [warn] buffered")).toEqual({ at: null, rest: "2026/10/14 14:02:01 [warn] buffered" });
  });
});

describe("readLine", () => {
  it("reads a JSON line's level and message, with its other fields", () => {
    expect(readLine('{"level":"warn","time":"x","msg":"pool busy","in_use":17,"path":"/v1 x"}')).toEqual({
      level: "warn",
      text: 'pool busy in_use=17 path="/v1 x"',
    });
    expect(readLine('{"level":50,"pid":1,"hostname":"h","msg":"boom"}')).toEqual({ level: "error", text: "boom" });
  });

  it("reads Monolog's numeric levels", () => {
    expect(readLine('{"message":"trace","level":100,"level_name":"DEBUG"}').level).toBe("debug");
    expect(readLine('{"message":"handled","level":200,"level_name":"INFO"}').level).toBe("info");
    expect(readLine('{"message":"notice","level":250,"level_name":"NOTICE"}').level).toBe("info");
    expect(readLine('{"message":"failed","level":300,"level_name":"WARNING"}').level).toBe("warn");
    expect(readLine('{"message":"boom","level":400,"level_name":"ERROR"}').level).toBe("error");
  });

  it("guesses a text line's level from its words", () => {
    expect(readLine("ERROR signature mismatch").level).toBe("error");
    expect(readLine("pool: consider DATABASE_POOL_MAX warning").level).toBe("warn");
    expect(readLine("level=info msg=ok").level).toBe("info");
    expect(readLine('198.51.100.41 - - "GET / HTTP/2.0" 200').level).toBe("");
  });

  it.each([
    // What a line declares itself wins over any word in its message (D-45's JSON level comes first).
    ['level=info msg="retrying after error"', "info"],
    ['level=warning msg="no error yet"', "warn"],
    ["time=2026 level=ERROR msg=boom", "error"],
    ["2026/11/09 10:23:10 [notice] 12#12: gracefully shutting down after error", "info"],
    ["2026/11/09 10:23:10 [warn] 12#12: an upstream response is buffered", "warn"],
    ["2026/11/09 10:23:10 [error] 12#12: open() failed", "error"],
    ["2026/11/09 10:23:10 [crit] 12#12: SSL_do_handshake() failed", "error"],
    ["2026/11/09 10:23:10 [emerg] 1#1: bind() to 0.0.0.0:80 failed", "error"],
    ["2026/11/09 10:23:10 [alert] 1#1: worker process exited", "error"],
    // Crashes, named as their language names them.
    ["TypeError: Cannot read properties of undefined (reading 'x')", "error"],
    ["ValueError: invalid literal for int()", "error"],
    ["Error: connect ECONNREFUSED 127.0.0.1:5432", "error"],
    ["Uncaught RangeError: too much recursion", "error"],
    ["org.postgresql.util.PSQLException: connection refused", "error"],
    ["Traceback (most recent call last):", "error"],
    ["panic: runtime error: index out of range", "error"],
    ["FATAL: out of memory", "error"],
    ["fatal: not a git repository", "error"],
    // Words that only look like a failure.
    ["GET /error-pages/404.html 200", ""],
    ["served /assets/error.log 200", ""],
    ["no error yet", ""],
    ["finished without error", ""],
    // Still errors.
    ["ERROR signature mismatch", "error"],
    ["connection error: reset by peer", "error"],
  ])("reads the level of %s", (line, level) => {
    expect(readLine(line).level).toBe(level);
  });

  it.each([
    ["INFO retrying after error", "info"],
    ["INFO:root:connection error, will retry", "info"],
    ["2026-11-09 12:00:00 INFO  c.a.Foo - handled exception, continuing", "info"],
    ["2026-11-09T12:00:00.123Z  INFO 1 --- [main] c.a.Foo : error handler registered", "info"],
    ["2026-11-09 12:00:00,123 DEBUG retrying after error", "debug"],
    ["WARN  the error was retried", "warn"],
    ["DEBUG error handler registered", "debug"],
    ["info: connection error, retrying", "info"],
    ["error=nil status=200", ""],
    ["ERROR the disk is full", "error"],
    ["error: the disk is full", "error"],
    ["panic: runtime error", "error"],
    ["error=timeout status=500", "error"],
    ["GET /error 500", ""],
  ])("reads a line's leading level before any word in it: %s", (line, level) => {
    expect(readLine(line).level).toBe(level);
  });

  it("keeps a multi-line field's real line breaks", () => {
    expect(readLine(JSON.stringify({ level: "error", msg: "boom", stack: "Error: boom\n    at f (a.js:1:1)" })).text).toBe(
      "boom stack=Error: boom\n    at f (a.js:1:1)",
    );
  });

  it("reads broken JSON as text", () => {
    expect(readLine("{not json}")).toEqual({ level: "", text: "{not json}" });
  });
});

describe("lineTime", () => {
  const now = new Date(2026, 10, 9, 15, 0, 0).getTime();
  const today = startOfDay(now);

  it("is the time alone for a line from today", () => {
    const ms = new Date(2026, 10, 9, 0, 0, 1).getTime();
    expect(lineTime(ms, today)).toBe(clockTime(ms));
    expect(lineTime(ms, today)).toBe("00:00:01");
  });

  it("names the day of a line from another day", () => {
    expect(lineTime(new Date(2026, 10, 3, 12, 23, 10).getTime(), today)).toBe("Nov 3 12:23:10");
    expect(lineTime(new Date(2026, 10, 8, 23, 59, 59).getTime(), today)).toBe("Nov 8 23:59:59");
  });

  it("gives the full date and time zone for the row's title", () => {
    const text = lineStamp(new Date(2026, 10, 3, 12, 23, 10).getTime());
    expect(text).toContain("2026");
    expect(text).toContain("12:23:10");
    expect(text).toMatch(/\b[A-Z]{3,4}\b|GMT|UTC/);
  });
});

describe("zoneName", () => {
  it("names a time zone", () => {
    expect(zoneName(Date.parse("2026-11-09T12:00:00Z"))).toMatch(/\S/);
  });
});

describe("composeServiceOf", () => {
  it("names the compose service of the platform's stack", () => {
    expect(composeServiceOf("paas-web-8f14e45f-nginx-1", "8f14e45f-0000-0000-0000-000000000000")).toBe("nginx");
    expect(composeServiceOf("paas-web-8f14e45f-web-worker-2", "8f14e45f-1111")).toBe("web-worker");
    expect(composeServiceOf("something-else", "8f14e45f-1111")).toBe("");
  });
});

describe("ansiSpans", () => {
  it("leaves a line without escapes alone", () => {
    expect(ansiSpans("plain line")).toEqual({ plain: "plain line", spans: null });
  });

  it("colours basic and bright colours with theme tokens, bold, and resets", () => {
    expect(ansiSpans("\x1b[31mERR\x1b[0m done \x1b[1;92mok\x1b[22m!\x1b[m")).toEqual({
      plain: "ERR done ok!",
      spans: [
        { text: "ERR", cls: "text-status-failed-fg" },
        { text: " done ", cls: "" },
        { text: "ok", cls: "text-status-running-fg font-semibold" },
        { text: "!", cls: "text-status-running-fg" },
      ],
    });
  });

  it("strips every other escape", () => {
    // cursor moves, clear line, an OSC title and link, a charset, 256-colour and RGB, backgrounds
    const line = "\x1b[2K\x1b[1Ga\x1b]0;title\x07b\x1b]8;;https://x\x1b\\c\x1b(Bd\x1b[38;5;196me\x1b[38;2;1;2;3mf\x1b[41mg\x1b";
    expect(ansiSpans(line)).toEqual({ plain: "abcdefg", spans: null });
  });

  it("keeps a colour after a skipped 256-colour parameter", () => {
    expect(ansiSpans("\x1b[38;5;1;33my")).toEqual({ plain: "y", spans: [{ text: "y", cls: "text-status-pending-fg" }] });
  });
});
