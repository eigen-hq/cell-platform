// Unit checks of plain helpers, run by node (npm run check).
import { test } from "node:test";
import assert from "node:assert/strict";
import { backoff, follow, parseSSE, type CellEvent } from "../src/lib/live.ts";

test("events are cut at blank lines; comments and half events wait", () => {
  const p = parseSSE(': keepalive\n\nevent: app\ndata: {"kind":"App"}\n\nevent: backup\ndata: {"ki');
  assert.deepEqual(p.data, ['{"kind":"App"}']);
  assert.equal(p.rest, 'event: backup\ndata: {"ki');
  assert.deepEqual(parseSSE("data: a\r\ndata: b\r\n\r\n").data, ["a\nb"]);
});

test("the pause grows to thirty seconds", () => {
  assert.deepEqual([0, 1, 2, 5, 9].map(backoff), [1000, 2000, 4000, 30000, 30000]);
});

test("follow reads events split across chunks and reports the stream's state", async () => {
  const chunks = ['event: app\ndata: {"type":"MODIFIED","kind":"App","na', 'me":"wiki"}\n\n: keepalive\n\n'];
  const got: CellEvent[] = [];
  const states: boolean[] = [];
  let stop!: () => void;
  await new Promise<void>((done) => {
    stop = follow(
      async () => ({
        data: new ReadableStream({ start(c) { for (const s of chunks) c.enqueue(new TextEncoder().encode(s)); c.close(); } }),
        response: new Response(null, { status: 200 }),
      }),
      (e) => got.push(e),
      (up) => (states.push(up), !up && done()),
    );
  });
  stop();
  assert.deepEqual(got, [{ type: "MODIFIED", kind: "App", name: "wiki" }]);
  assert.deepEqual(states, [true, false]);
});

test("a refused stream is reported, not read", async () => {
  let why = "";
  const stop = follow(async () => ({ data: null, response: new Response(null, { status: 503 }) }), () => assert.fail("no events"), (_, w) => (why = w ?? ""));
  await new Promise((r) => setTimeout(r, 20));
  stop();
  assert.match(why, /HTTP 503/);
});
