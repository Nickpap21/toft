import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { DeleteData, deletesData, volumeText } from "./delete-data";

const d = (extra = {}) => ({
  service_id: "s1",
  service_name: "db",
  server_name: "fsn1",
  read: true,
  volumes: [],
  ...extra,
});

describe("volumeText", () => {
  it("names the volume with its size", () => {
    expect(
      volumeText({ name: "paas-vol-1-data", bytes: 2048, from: "volume" }),
    ).toBe("paas-vol-1-data · 2.0 KiB");
  });
  it("says it's reading the size while the sized read runs", () => {
    expect(volumeText({ name: "pg", bytes: -1, from: "volume" }, true)).toBe(
      "pg · reading its size…",
    );
  });
  it("says size unknown rather than leaving the volume out", () => {
    expect(volumeText({ name: "shop_db", bytes: -1, from: "compose" })).toBe(
      "shop_db · size unknown, its compose stack's",
    );
  });
});

describe("deletesData", () => {
  it("is false only when every server was read and holds nothing", () => {
    expect(deletesData([d()])).toBe(false);
    expect(
      deletesData([d({ volumes: [{ name: "v", bytes: 0, from: "volume" }] })]),
    ).toBe(true);
    expect(deletesData([d({ read: false })])).toBe(true);
  });
});

describe("DeleteData", () => {
  it("lists each service's volumes with sizes", () => {
    render(
      <DeleteData
        perService
        data={[
          d({ volumes: [{ name: "pg", bytes: 1024 * 1024, from: "volume" }] }),
          d({
            service_id: "s2",
            service_name: "kuma",
            volumes: [{ name: "kuma_data", bytes: -1, from: "compose" }],
          }),
        ]}
      />,
    );
    expect(screen.getByText("db: pg · 1.0 MiB")).toBeTruthy();
    expect(
      screen.getByText("kuma: kuma_data · size unknown, its compose stack's"),
    ).toBeTruthy();
  });
  it("says when a server couldn't be read", () => {
    render(
      <DeleteData data={[d({ read: false, reason: "couldn't reach fsn1" })]} />,
    );
    expect(
      screen.getByText(/couldn't reach fsn1; any compose volumes go too/),
    ).toBeTruthy();
  });
  it("names the services one server's error covers once", () => {
    render(
      <DeleteData
        perService
        data={[
          d({ service_id: "a", service_name: "web", read: false, reason: "couldn't reach fsn1" }),
          d({ service_id: "b", service_name: "api", read: false, reason: "couldn't reach fsn1" }),
        ]}
      />,
    );
    expect(screen.getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "web, api: couldn't reach fsn1; any compose volumes go too",
    ]);
  });
  it("says none when nothing would be deleted", () => {
    render(<DeleteData data={[d()]} />);
    expect(screen.getByText("none: no volumes")).toBeTruthy();
  });
});
