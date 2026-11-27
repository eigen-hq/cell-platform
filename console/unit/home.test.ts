// Unit checks of plain helpers, run by node (npm run check).
import { test } from "node:test";
import assert from "node:assert/strict";
import { appState, doneLately, hero, isDown, lastBackup, reach } from "../src/lib/home.ts";

const app = (name: string, phase = "Ready", groups: string[] = [], mode = "oidc", up = phase === "Ready") =>
  ({ name, phase, groups, spec: { access: { auth: { mode } } }, conditions: [{ type: "Ready", status: up ? "True" : "False" }] }) as never;
const change = (app: string, phase = "WaitingForApproval") => ({ app, phase }) as never;
const title = (n: string) => n[0]!.toUpperCase() + n.slice(1);

test("the headline names a down app and whether a fix waits", () => {
  assert.deepEqual(hero([app("notes", "NeedsAttention"), app("wiki")], [change("notes")], title), { lines: ["Notes is down.", "A fix is waiting for your OK."], mood: "you" });
  assert.deepEqual(hero([app("notes", "NeedsAttention")], [], title).lines, ["Notes is down.", "Ops can find out why."]);
  assert.deepEqual(hero([app("a", "NeedsAttention"), app("b", "NeedsAttention")], [], title).lines[0], "2 apps are down.");
  assert.deepEqual(hero([app("wiki")], [change("wiki"), change("chat")], title).lines, ["Everything is running.", "2 changes wait for your OK."]);
  assert.deepEqual(hero([app("wiki")], [change("wiki", "Succeeded")], title), { lines: ["Everything is running.", "Nothing waits for you."], mood: "good" });
  assert.equal(hero([], [], title).lines[1], "Add the first app.");
});

test("reach counts the people who can open an app", () => {
  const people = [{ username: "ann" }, { username: "bo" }, { username: "cy" }] as never[];
  const groups = [{ name: "eng", members: ["ann", "bo", "ops-agent"] }, { name: "hr", members: ["bo", "cy"] }] as never[];
  assert.equal(reach(app("wiki"), people, groups), 3);
  assert.equal(reach(app("code", "Ready", ["eng"]), people, groups), 2); // agents are no people
  assert.equal(reach(app("code", "Ready", ["eng", "hr"]), people, groups), 3);
  assert.equal(reach(app("site", "Ready", ["eng"], "none"), people, groups), 3);
});

test("the last backup is the newest one that ended", () => {
  const b = (phase: string, at: string) => ({ phase, createdAt: at, completedAt: phase === "Running" ? null : at }) as never;
  assert.equal(lastBackup([]), null);
  assert.deepEqual(lastBackup([b("Completed", "2026-11-24T02:00:00Z"), b("Running", "2026-11-25T02:00:00Z")]), { at: "2026-11-24T02:00:00Z", ok: true });
  assert.deepEqual(lastBackup([b("Completed", "2026-11-24T02:00:00Z"), b("Failed", "2026-11-25T02:00:00Z")]), { at: "2026-11-25T02:00:00Z", ok: false });
});

test("a failed change on a running app is no outage, and busy apps are not called running", () => {
  const stuck = app("notes", "NeedsAttention", [], "oidc", true);
  assert.equal(isDown(stuck), false);
  assert.equal(isDown(app("notes", "NeedsAttention")), true);
  assert.equal(isDown(app("notes", "Installing")), false);
  assert.deepEqual(hero([stuck], [], title), { lines: ["Notes needs a look.", "A change did not go through; it still runs."], mood: "you" });
  assert.deepEqual(hero([app("wiki", "Installing")], [], title), { lines: ["Wiki is installing.", "The platform is on it."], mood: "busy" });
  assert.deepEqual(hero([app("wiki", "Restoring"), app("code", "Updating")], [change("x")], title), { lines: ["2 apps are changing.", "1 change waits for your OK."], mood: "you" });
});

test("a ready app that fails its health check is not called running", () => {
  const silent = app("chat", "Ready", [], "oidc", false);
  assert.deepEqual(hero([silent, app("wiki")], [], title), { lines: ["Chat does not answer.", "The platform keeps checking and acts if this goes on."], mood: "busy" });
  assert.equal(hero([silent], [change("chat")], title).mood, "you");
});

test("the app state comes from the health check where the phase is not enough", () => {
  assert.deepEqual(appState(app("chat", "Ready", [], "oidc", false)), { label: "Not answering", tone: "wait" });
  assert.deepEqual(appState(app("chat", "NeedsAttention")), { label: "Down", tone: "bad" });
  assert.deepEqual(appState(app("chat", "NeedsAttention", [], "oidc", true)).label, "Needs attention");
  assert.deepEqual(appState(app("chat")), { label: "Running", tone: "ok" });
});

test("two backups finished at the same time stay two lines", () => {
  const b = (name: string) => ({ name, app: "wiki", phase: "Completed", createdAt: "2026-11-27T02:00:00Z", completedAt: "2026-11-27T02:01:00Z" }) as never;
  const done = doneLately([], [b("wiki-1"), b("wiki-2")], title, () => "");
  assert.equal(new Set(done.map((d) => d.key)).size, 2);
});

test("a change still running is not done yet", () => {
  const c = (name: string, phase: string) => ({ name, app: "wiki", action: "restart", phase, requester: { kind: "Human", name: "a" }, approval: { required: true, at: null }, createdAt: "2026-11-27T02:00:00Z", startedAt: null, finishedAt: null }) as never;
  assert.deepEqual(doneLately([c("a", "Running"), c("b", "Succeeded")], [], title, () => "").map((d) => d.key), ["change/b"]);
});
