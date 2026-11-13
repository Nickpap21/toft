import { describe, expect, it } from "vitest";
import { deployStrategyHint } from "./deploy-strategy-field";

describe("deployStrategyHint", () => {
  it("says what Automatic comes to for the saved choice", () => {
    expect(deployStrategyHint("auto", { deploy_strategy: "auto", effective_deploy_strategy: "recreate" })).toMatch(/^Stop then start, as for every database/);
    expect(deployStrategyHint("auto", { deploy_strategy: "auto", effective_deploy_strategy: "rolling" })).toMatch(/^Zero downtime/);
  });

  it("gives the rule when Automatic is not the saved choice", () => {
    expect(deployStrategyHint("auto", { deploy_strategy: "rolling", effective_deploy_strategy: "rolling" }))
      .toBe("Stop then start for a database or cache, zero downtime for anything else.");
  });

  it("names the trade-off of an explicit choice", () => {
    expect(deployStrategyHint("rolling", { effective_deploy_strategy: "recreate" })).toMatch(/no downtime/);
    expect(deployStrategyHint("recreate", { effective_deploy_strategy: "rolling" })).toMatch(/never two containers on the same data/);
  });
});
