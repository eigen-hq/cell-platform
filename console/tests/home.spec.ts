import { expect, test } from "@playwright/test";
import { env, signIn, watchErrors } from "./helpers";

// The overview of the cell: a headline from the cell's state, the ask box,
// what agents do and what waits for a yes, the machines with their apps,
// four numbers and what was done lately.
test("home shows the state of the cell and links to the views", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  await signIn(page, env("CONSOLE_ADMIN_USER"), env("CONSOLE_ADMIN_PASSWORD"));
  await page.waitForURL(`${baseURL}/`);
  await expect(page.getByRole("heading", { level: 1 })).toContainText(/down|look|is |are changing|Everything is running|The cell is ready/);
  // The page follows the cell's event stream.
  await expect(page.getByText("Live", { exact: true })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "What should Ops do?" })).toBeVisible();
  for (const name of [/^Agents/, /^Your cell/, /^Done lately/]) {
    await expect(page.getByRole("heading", { level: 2, name })).toBeVisible();
  }
  await page.getByRole("link", { name: "Change log", exact: true }).click();
  await expect(page).toHaveURL(/\/changes$/);
  expect(errors).toEqual([]);
});

// A half-made decision belongs to its request: when a poll brings a newer
// request to the top, the "Yes, decline" of the older one must not stay.
test("a newer request does not inherit the decline confirmation", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  const request = (name: string, reason: string) => ({
    name, app: "notes", action: "update", requester: { kind: "Agent", name: "ops" }, reason, risk: "low",
    patch: { version: "2" }, approval: { required: true, at: null }, phase: "WaitingForApproval",
    createdAt: new Date().toISOString(), startedAt: null, finishedAt: null,
  });
  let changes = [request("cr-a", "first request")];
  await page.route("**/v0/changes", (route) => route.fulfill({ json: changes }));
  // Each connection to the event stream brings one change event and ends;
  // the page reconnects every two seconds and reloads on each event.
  await page.route("**/v0/events", (route) =>
    route.fulfill({ contentType: "text/event-stream", body: 'event: changerequest\ndata: {"type":"MODIFIED","kind":"ChangeRequest","name":"cr-b"}\n\n' }),
  );
  await page.route("**/v0/changes/*/reject", (route) => route.fulfill({ status: 500, json: { error: "must not be called" } }));
  await signIn(page, env("CONSOLE_ADMIN_USER"), env("CONSOLE_ADMIN_PASSWORD"));
  await expect(page.getByText("first request")).toBeVisible();
  await page.getByRole("button", { name: "Decline" }).click();
  await expect(page.getByRole("button", { name: "Yes, decline" })).toBeVisible();
  changes = [request("cr-b", "second request"), changes[0]!].map((c, i) => ({ ...c, createdAt: new Date(Date.now() + 1000 - i * 1000).toISOString() }));
  await expect(page.getByText("second request")).toBeVisible({ timeout: 10000 });
  // Each request keeps its own decision: the newer one starts fresh.
  const newer = page.getByRole("article").filter({ hasText: "second request" });
  await expect(newer.getByRole("button", { name: "Decline" })).toBeVisible();
  await expect(newer.getByRole("button", { name: "Yes, decline" })).toHaveCount(0);
  expect(errors).toEqual([]);
});
