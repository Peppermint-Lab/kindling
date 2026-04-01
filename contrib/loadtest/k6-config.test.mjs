import assert from "node:assert/strict";
import test from "node:test";

import { buildOptions } from "./k6-config.mjs";

function peakTarget(options) {
  return Math.max(...options.stages.map((stage) => stage.target));
}

test("default profile uses viral-4000 stages", () => {
  const options = buildOptions({});

  assert.ok(Array.isArray(options.stages));
  assert.equal(peakTarget(options), 4000);
});

test("smoke profile uses a tiny fixed run", () => {
  const options = buildOptions({ PROFILE: "smoke" });

  assert.equal(options.vus, 5);
  assert.equal(options.duration, "15s");
});

test("medium profile uses a staged peak", () => {
  const options = buildOptions({ PROFILE: "medium" });

  assert.ok(Array.isArray(options.stages));
  assert.equal(peakTarget(options), 300);
});

test("large profile uses a larger staged peak", () => {
  const options = buildOptions({ PROFILE: "large" });

  assert.ok(Array.isArray(options.stages));
  assert.equal(peakTarget(options), 1000);
});

test("quick mode still overrides profile selection", () => {
  const options = buildOptions({ PROFILE: "large", QUICK: "1" });

  assert.equal(options.vus, 5);
  assert.equal(options.duration, "15s");
});

test("max vus override still works for staged profiles", () => {
  const options = buildOptions({ PROFILE: "viral-4000", MAX_VUS: "500" });

  assert.ok(Array.isArray(options.stages));
  assert.equal(peakTarget(options), 500);
});

test("invalid profile throws", () => {
  assert.throws(() => buildOptions({ PROFILE: "wat" }), /unknown PROFILE/i);
});
