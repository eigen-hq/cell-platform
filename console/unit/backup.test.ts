// Unit checks of the backup target helpers, run by node (npm run check).
import { test } from "node:test";
import assert from "node:assert/strict";
import { checking, plainHttp, requestOf, shown, type TargetForm } from "../src/lib/backup.ts";

const form: TargetForm = { provider: "hetzner", region: "nbg1", endpoint: "https://ignored", otherRegion: "x", bucket: " bk ", accessKey: "AK", secretKey: "sk\n", prefix: "/a/b/", privateNetwork: true };

test("the form asks for Hetzner's endpoint of the region, or the one typed in", () => {
  assert.deepEqual(requestOf(form), { endpoint: "https://nbg1.your-objectstorage.com", region: "nbg1", bucket: "bk", accessKey: "AK", secretKey: "sk", prefix: "a/b" });
  const other = requestOf({ ...form, provider: "other", endpoint: " https://s3.test ", otherRegion: "", prefix: "" });
  assert.equal(other.endpoint, "https://s3.test");
  assert.equal(other.region, undefined);
  assert.equal(other.prefix, undefined);
  assert.equal(other.privateNetwork, undefined);
});

test("plain http is asked for only for another service's http endpoint", () => {
  const http = { ...form, provider: "other" as const, endpoint: " http://10.0.0.5:9000" };
  assert.equal(plainHttp(http), true);
  assert.equal(requestOf(http).privateNetwork, true);
  assert.equal(requestOf({ ...http, privateNetwork: false }).privateNetwork, undefined);
  assert.equal(plainHttp(form), false);
  assert.equal(requestOf({ ...http, endpoint: "https://10.0.0.5" }).privateNetwork, undefined);
});

test("a poll from before the operator took up the check does not end it", () => {
  const check = (state: "checking" | "passed" | "failed", at: string) => ({ state, at, target: "t", by: "ann" });
  const asked = { configured: false, check: check("checking", "2026-11-25T10:00:00.500Z") };
  const old = { configured: true, check: check("passed", "2026-11-25T10:00:00.400Z") };
  assert.equal(checking(shown(old, asked)), true);
  assert.equal(shown(old, asked)!.configured, true);
  const failed = { configured: true, check: check("failed", "2026-11-25T10:00:09Z") };
  assert.equal(shown(failed, asked), failed);
  assert.equal(shown(null, asked), asked);
  assert.equal(shown(old, null), old);
});
