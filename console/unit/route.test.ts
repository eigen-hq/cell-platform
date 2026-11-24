// Unit checks of plain helpers, run by node (npm run check).
import { test } from "node:test";
import assert from "node:assert/strict";
import { itemPath, route } from "../src/lib/route.ts";

test("routes decode the item and survive bad escapes", () => {
  assert.deepEqual(route("/"), { section: "/", rest: undefined });
  assert.deepEqual(route("/apps/memos"), { section: "/apps", rest: "memos" });
  assert.deepEqual(route(itemPath("/organization", "HR Team")), { section: "/organization", rest: "HR Team" });
  assert.equal(itemPath("/organization", "a/b"), "/organization/a%2Fb");
  assert.deepEqual(route("/organization/%E0%A4%A"), { section: "/organization", rest: undefined });
});
