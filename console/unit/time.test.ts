// Unit checks of plain helpers, run by node (npm run check).
import { test } from "node:test";
import assert from "node:assert/strict";
import { ago } from "../src/lib/time.ts";

test("ago speaks like a person", () => {
  const now = new Date(2026, 10, 24, 15, 0).getTime();
  const at = (min: number) => new Date(now - min * 60000).toISOString();
  assert.equal(ago(null, now), "never");
  assert.equal(ago(at(0.5), now), "just now");
  assert.equal(ago(at(12), now), "12 min ago");
  assert.equal(ago(at(150), now), "3 h ago");
  assert.match(ago(at(7 * 60), now), /^today /);
  assert.match(ago(at(20 * 60), now), /^yesterday /);
  assert.doesNotMatch(ago(at(3 * 1440), now), /ago|today|yesterday/);
});
