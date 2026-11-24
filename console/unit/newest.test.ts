// Unit checks of plain helpers, run by node (npm run check).
import { test } from "node:test";
import assert from "node:assert/strict";
import { newest, STALE } from "../src/lib/newest.ts";

test("a poll from before a save cannot overwrite the reload after it", async () => {
  const guard = newest();
  let finishOld!: (v: string[]) => void;
  const oldPoll = guard(new Promise<string[]>((r) => (finishOld = r)));
  const reload = guard(Promise.resolve(["a"]));
  assert.deepEqual(await reload, ["a"]);
  finishOld(["a", "revoked-admin"]);
  assert.equal(await oldPoll, STALE);
});

test("an older poll that fails late does not count either; the newest error does", async () => {
  const guard = newest();
  let failOld!: (e: Error) => void;
  const oldPoll = guard(new Promise<string[]>((_, reject) => (failOld = reject)));
  await guard(Promise.resolve(["a"]));
  failOld(new Error("network"));
  assert.equal(await oldPoll, STALE);
  await assert.rejects(guard(Promise.reject(new Error("now"))), /now/);
});
