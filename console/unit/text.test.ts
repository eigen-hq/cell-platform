// Unit checks of plain helpers, run by node (npm run check).
import { test } from "node:test";
import assert from "node:assert/strict";
import { textOf } from "../src/lib/text.ts";

const ok = { ok: true, status: 200 };
const bad = { ok: false, status: 404 };

test("an empty log is empty text, not an error", () => {
  assert.equal(textOf({ data: undefined, response: ok }), "");
  assert.equal(textOf({ data: "a\nb", response: ok }), "a\nb");
});

test("errors keep the API's message, parsed or not", () => {
  assert.throws(() => textOf({ error: { error: "no pod of app notes" }, response: bad }), /no pod of app notes/);
  assert.throws(() => textOf({ error: '{"error":"container has no previous run"}', response: bad }), /container has no previous run/);
  assert.throws(() => textOf({ error: "gateway down", response: bad }), /gateway down/);
  assert.throws(() => textOf({ error: undefined, response: bad }), /HTTP 404/);
});
