// Unit checks of plain helpers, run by node (npm run check).
import { test } from "node:test";
import assert from "node:assert/strict";
import { MAX_CHARS, packageProblem } from "../src/lib/package.ts";

const none = new Set<string>();

test("app names follow the API's pattern", () => {
  for (const ok of ["wiki", "a", "wiki-2", "a".repeat(40)]) assert.equal(packageProblem(ok, "x", none), "", ok);
  for (const bad of ["1wiki", "Wiki", "wiki-", "-wiki", "wi_ki", "a".repeat(41)]) assert.match(packageProblem(bad, "x", none), /Starts with a letter/, bad);
  assert.match(packageProblem("wiki", "x", new Set(["wiki"])), /installed/);
});

test("the Compose file may have at most 65,536 characters", () => {
  assert.equal(packageProblem("wiki", "x".repeat(MAX_CHARS), none), "");
  assert.match(packageProblem("wiki", "x".repeat(MAX_CHARS + 1), none), /65,536/);
  assert.equal(packageProblem("wiki", "ä".repeat(MAX_CHARS), none), "", "characters, not bytes");
});
