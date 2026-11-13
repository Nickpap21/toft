import { render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { useHashLanding } from "./hash-landing";

function Shell() {
  useHashLanding("/settings");
  return null;
}
const frame = () => new Promise((r) => requestAnimationFrame(r));

afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
  history.replaceState(null, "", "/");
});

it("lands on the anchor each time the page above it grows, until the person scrolls", async () => {
  history.replaceState(null, "", "/settings#cycles");
  const main = Object.assign(document.createElement("main"), { id: "main" });
  document.body.append(main);
  const scroll = vi.spyOn(Element.prototype, "scrollIntoView");
  render(<Shell />);

  const target = Object.assign(document.createElement("section"), { id: "cycles" });
  main.append(target);
  await frame();
  expect(scroll).toHaveBeenCalledTimes(1);

  main.prepend(document.createElement("section")); // a card above it loads
  await frame();
  expect(scroll).toHaveBeenCalledTimes(2);
  expect(scroll.mock.contexts).toEqual([target, target]);

  window.dispatchEvent(new WheelEvent("wheel"));
  main.prepend(document.createElement("section"));
  await frame();
  expect(scroll).toHaveBeenCalledTimes(2);
});
