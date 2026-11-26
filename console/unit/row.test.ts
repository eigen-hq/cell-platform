// Unit checks of plain helpers, run by node (npm run check).
import { test } from "node:test";
import assert from "node:assert/strict";
import { resultRow } from "../src/lib/row.ts";

test("a result shows in its person's row, above the list without one", () => {
  const rows = [{ username: "alice" }];
  assert.equal(resultRow("alice", rows), "alice");
  assert.equal(resultRow("bob", rows), null);
  assert.equal(resultRow(null, rows), null);
});
