// Unit checks of plain helpers, run by node (npm run check).
import { test } from "node:test";
import assert from "node:assert/strict";
import { begin, changes, draftState, replaced, saved, snapshot, startOver, stateOf, toggle } from "../src/lib/draft.ts";

test("a draft follows its source until edited, then guards against overwrites", () => {
  assert.equal(draftState(["a"], ["a"], ["a"]), "clean");
  assert.equal(draftState(["a"], ["a", "b"], ["a"]), "edited");
  // someone else changed the group while the draft was untouched: follow it
  assert.equal(draftState(["a"], ["a"], ["b"]), "clean");
  // both changed: saving would bring back what the other admin removed
  assert.equal(draftState(["a", "x"], ["a", "x", "b"], ["a"]), "conflict");
  assert.equal(draftState(["a", "a"], ["a"], ["a"]), "clean");
});

test("a save holds until a newer snapshot; a failed reload rolls nothing back", () => {
  let e = begin(["alice"]);
  e = toggle(e, "bob");
  assert.equal(stateOf(e), "edited");
  e = saved(["alice", "bob"]);
  // the reload failed: no snapshot arrives, the next edit builds on the save
  e = toggle(e, "carol");
  assert.equal(stateOf(e), "edited");
  assert.deepEqual(e.draft, ["alice", "bob", "carol"]);
  assert.deepEqual(startOver(e).draft, ["alice", "bob"]);
});

test("a newer snapshot moves a clean edit and flags an edited one", () => {
  let e = saved(["alice", "bob"]);
  e = snapshot(e, ["alice", "bob", "dave"]); // another admin added dave
  assert.deepEqual(e.draft, ["alice", "bob", "dave"]);
  e = toggle(e, "carol");
  e = snapshot(e, ["alice"]);
  assert.equal(stateOf(e), "conflict");
  assert.deepEqual(startOver(e).draft, ["alice"]);
});

test("a saved group survives a failed reload when the drawer reopens", () => {
  let groups = [{ name: "team", members: ["alice"] }, { name: "ops", members: [] }];
  groups = replaced(groups, { name: "team", members: ["alice", "bob"] }); // the save's answer
  // the reload failed: the list keeps the save; reopening starts from it
  const e = toggle(begin(groups.find((g) => g.name === "team")!.members), "carol");
  assert.deepEqual(e.draft, ["alice", "bob", "carol"]);
  assert.deepEqual(groups[1], { name: "ops", members: [] });
});

test("a save sends only who changed", () => {
  const e = toggle(toggle(begin(["alice", "bob"]), "bob"), "carol");
  assert.deepEqual(changes(e), { add: ["carol"], remove: ["bob"] });
});
