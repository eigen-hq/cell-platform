// Unit checks of plain helpers, run by node (npm run check).
import { test } from "node:test";
import assert from "node:assert/strict";
import { withTranscript } from "../src/lib/task.ts";

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
