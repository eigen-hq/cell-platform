// Unit checks of plain helpers, run by node (npm run check).
import { test } from "node:test";
import assert from "node:assert/strict";
import { backupOf, groupsOf, headline } from "../src/lib/change.ts";

const base = { name: "c", app: "forgejo", requester: { kind: "Human", name: "a" }, reason: "", risk: "medium", approval: { required: true, at: null }, phase: "WaitingForApproval", createdAt: "", startedAt: null, finishedAt: null } as const;

test("headline says what the change does", () => {
  assert.equal(headline({ ...base, action: "update", patch: { version: "11.0.1" } }, "Code"), "Update Code to 11.0.1");
  assert.equal(headline({ ...base, action: "update", patch: { access: { groups: ["x"] } } }, "Code"), "Change who can open Code");
  assert.equal(headline({ ...base, action: "update", patch: { version: "1", replicas: 2 } }, "Code"), "Change the settings of Code");
  assert.equal(headline({ ...base, action: "remove", patch: null, deleteData: true }, "Code"), "Remove Code and delete its data");
  assert.equal(headline({ ...base, action: "install", patch: {} }, "Code"), "Install Code");
});

test("groupsOf reads the groups a change sets, deduplicated", () => {
  assert.deepEqual(groupsOf({ ...base, action: "install", patch: { access: { groups: ["a", "a", "b"] } } }), ["a", "b"]);
  assert.equal(groupsOf({ ...base, action: "update", patch: { version: "1" } }), undefined);
  assert.equal(groupsOf({ ...base, action: "remove", patch: null }), undefined);
});

test("a restore names its backup", () => {
  assert.equal(backupOf({ ...base, action: "restore", patch: null, restores: "memos-x7k2p" }), "memos-x7k2p");
  assert.equal(backupOf({ ...base, action: "restore", patch: null }), "the newest backup");
});
