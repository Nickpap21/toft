import { describe, expect, it } from "vitest";
import { limitMB } from "./resources.service";

// The forms backend/internal/resources.ParseMB accepts, with its answers.
describe("limitMB", () => {
  it.each([
    ["512m", 512],
    ["1g", 1024],
    ["1G", 1024],
    ["1.5g", 1536],
    ["2GB", 2048],
    ["256MB", 256],
    ["524288k", 512],
    ["1073741824", 1024],
    [" 1g ", 1024],
    ["", 0],
    ["lots", 0],
    ["512b", 0],
  ])("%s is %d MiB", (limit, mb) => {
    expect(limitMB(limit)).toBe(mb);
  });
});
