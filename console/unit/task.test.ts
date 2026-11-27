// Unit checks of plain helpers, run by node (npm run check).
import { test } from "node:test";
import assert from "node:assert/strict";
import { findingsTitle, looseChanges, optionKey, transcriptHolds, withTranscript, type Task } from "../src/lib/task.ts";

test("the transcript is read only after the task", async () => {
  const order: string[] = [];
  let finishTask!: (v: string) => void;
  const read = withTranscript(
    () => new Promise<string>((r) => (finishTask = r)).then((v) => (order.push("task"), v)),
    async () => (order.push("items"), ["brief"]),
  );
  await Promise.resolve();
  assert.deepEqual(order, []);
  finishTask("Succeeded");
  assert.deepEqual(await read, { task: "Succeeded", items: ["brief"], err: "" });
  assert.deepEqual(order, ["task", "items"]);
});

test("a transcript that cannot be read leaves the task; a task that cannot be read fails", async () => {
  assert.deepEqual(
    await withTranscript(async () => "Running", () => Promise.reject(new Error("no database"))),
    { task: "Running", items: null, err: "no database" },
  );
  await assert.rejects(withTranscript(() => Promise.reject(new Error("gone")), async () => []), /gone/);
});

test("a transcript holds until the task moves: a reply from anyone, a step, an answer", () => {
  const done = {
    name: "ask-1",
    phase: "Succeeded",
    steps: [{ at: "2026-11-27T10:00:00Z", call: "GET /v0/apps/notes" }],
    finishedAt: "2026-11-27T10:01:00Z",
    reply: null,
  } as unknown as Task;
  assert.equal(transcriptHolds(done, { ...done }), true);
  assert.equal(transcriptHolds(null, done), false);
  // Another admin replied: the task is pending, then answered again.
  const replied = { ...done, phase: "Pending", finishedAt: null, reply: { by: "bob", at: "2026-11-27T10:05:00Z", text: "And?" } } as Task;
  assert.equal(transcriptHolds(done, replied), false);
  const again = { ...replied, phase: "Succeeded", finishedAt: "2026-11-27T10:06:00Z" } as Task;
  assert.equal(transcriptHolds(replied, again), false);
  assert.equal(transcriptHolds(again, { ...again }), true);
  // A question answered by another admin and answered again in one poll.
  const asked = { ...done, phase: "WaitingForInput" } as Task;
  assert.equal(transcriptHolds(asked, { ...asked, phase: "Succeeded", reply: replied.reply }), false);
  // A working task is always read again.
  const working = { ...done, phase: "Running", finishedAt: null } as Task;
  assert.equal(transcriptHolds(working, { ...working }), false);
  assert.equal(transcriptHolds(done, { ...done, steps: [...done.steps, done.steps[0]] }), false);
});

test("an option's change shows with the option; the other changes below, once each", () => {
  const t = {
    kind: "diagnose",
    changes: ["a", "b", "a", "c"],
    answer: { summary: "s", options: [{ title: "Go back", change: "b" }, { title: "Wait" }] },
  } as unknown as Task;
  assert.deepEqual(looseChanges(t), ["a", "c"]);
  assert.deepEqual(looseChanges({ ...t, answer: null }), ["a", "b", "c"]);
  assert.equal(findingsTitle("diagnose"), "Causes");
  assert.equal(findingsTitle("ask"), "What it found");
});

test("an option whose change a follow-up replaced is a new row", () => {
  assert.notEqual(optionKey({ change: "a" }, 0), optionKey({ change: "b" }, 0));
  assert.notEqual(optionKey({}, 0), optionKey({ change: "a" }, 0));
  assert.equal(optionKey({ change: "a" }, 1), optionKey({ change: "a" }, 1));
  assert.notEqual(optionKey({}, 0), optionKey({}, 1));
});
