// The trips to the identity, with a fake browser: storage, location, fetch.
import { test } from "node:test";
import assert from "node:assert/strict";

const store = new Map<string, string>();
const went: string[] = [];
let answers: Array<(r: Response) => void> = [];
let full = false;
let locked = false;
Object.assign(globalThis, {
  sessionStorage: {
    getItem: (k: string) => {
      if (locked) throw new Error("SecurityError");
      return store.get(k) ?? null;
    },
    setItem: (k: string, v: string) => {
      if (full) throw new Error("QuotaExceededError");
      store.set(k, v);
    },
    removeItem: (k: string) => void store.delete(k),
  },
  location: { pathname: "/apps", search: "", assign: (u: URL) => went.push(String(u)) },
  fetch: () => new Promise<Response>((r) => answers.push(r)),
});
const { signIn, signOut } = await import("../src/lib/api/auth.ts");

const login = () =>
  new Response(
    JSON.stringify({
      authorizationEndpoint: "https://auth.test/authorize", tokenEndpoint: "https://auth.test/token", endSessionEndpoint: "https://auth.test/end",
      clientId: "cell-console", redirectUri: "https://console.test/", scope: "openid",
    }),
  );
const tick = () => new Promise((r) => setTimeout(r, 0));
const answer = async (i: number, r: Response) => (answers[i]!(r), await tick(), await tick());
// The redirect waits on the PKCE digest, which takes longer than a few ticks
// on a busy machine.
const redirected = async (n: number) => {
  for (let i = 0; i < 100 && went.length < n; i++) await new Promise((r) => setTimeout(r, 10));
  assert.equal(went.length, n);
};

test("everyone waiting on a failed login hears of it, also across a sign-out", { timeout: 2000 }, async () => {
  const poll = signIn().catch((e: Error) => e);
  const save = signIn().catch((e: Error) => e);
  await answer(0, new Response("down", { status: 503 }));
  assert.match(String(await poll), /HTTP 503/);
  assert.match(String(await save), /HTTP 503/);
  const waiting = signIn().catch((e: Error) => e);
  const out = signOut().catch((e: Error) => e);
  await answer(2, new Response("down", { status: 502 }));
  await answer(1, login()); // the superseded login comes back after the sign-out failed
  assert.match(String(await out), /HTTP 502/);
  assert.match(String(await waiting), /HTTP 502/);
  assert.equal(went.length, 0);
  answers = [];
});

test("a sign-in superseded by a sign-out and a retry keeps neither its state nor its redirect", { timeout: 2000 }, async () => {
  void signIn(); // a poll found no token; the identity answers slowly
  void signIn(); // a second poll joins the first trip
  assert.equal(answers.length, 1);
  const out = signOut().catch((e: Error) => e);
  await answer(1, new Response("down", { status: 503 }));
  assert.match(String(await out), /HTTP 503/);
  void signIn(); // "Sign in again"
  await answer(2, login());
  await redirected(1);
  const state = new URL(went[0]!).searchParams.get("state");
  await answer(0, login()); // the first lookup comes back late
  await new Promise((r) => setTimeout(r, 100));
  assert.equal(went.length, 1);
  assert.equal(JSON.parse(store.get("cell.login")!).state, state);
});

test("a login that cannot keep its state can be tried again", { timeout: 2000 }, async () => {
  answers = [];
  went.length = 0;
  full = true;
  // A fresh page: the last test's login left it.
  const { signIn } = await import("../src/lib/api/auth.ts?fresh");
  const first = signIn().catch((e: Error) => e);
  await answer(0, login());
  assert.match(String(await first), /QuotaExceeded/);
  full = false;
  void signIn();
  await answer(1, login());
  await redirected(1);
});

test("a sign-out the storage refuses is an error for the page, not a thrown click", { timeout: 2000 }, async () => {
  locked = true;
  const out = signOut();
  locked = false;
  assert.ok(out instanceof Promise);
  await assert.rejects(out, /SecurityError/);
});
