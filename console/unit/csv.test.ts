// Unit checks of plain helpers, run by node (npm run check).
import { test } from "node:test";
import assert from "node:assert/strict";
import { csv, csvField } from "../src/lib/csv.ts";

test("csv quotes and defuses formulas", () => {
  assert.equal(csvField("plain"), '"plain"');
  assert.equal(csvField('say "hi"'), '"say ""hi"""');
  for (const f of ["=1+1", "+1", "-2", "@SUM(A1)", "\tx", "\rx", "\n=1+1", " =1", "\uFF1D1", "\uFF0B1", "\uFF0D1", "\uFF20x"]) assert.equal(csvField(f)[1], "'", f);
  assert.equal(csv([["a", "=b"]]), `"a","'=b"\n`);
});
