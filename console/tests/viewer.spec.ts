import { expect, test, type Locator } from "@playwright/test";
import { env, signIn, watchErrors } from "./helpers";

// A viewer sees the state of the cell only (E-63). Every view a viewer can
// open loads without a refused call: a 403 shows up as a console error.
test("a viewer sees the state of the cell and nothing of people, agents or app internals", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  await signIn(page, env("CONSOLE_VIEWER_USER"), env("CONSOLE_ADMIN_PASSWORD"));
  await expect(page.getByRole("status").filter({ hasText: "You see the state of the cell" })).toBeVisible();
  const nav = page.getByRole("navigation");
  await expect(nav.getByRole("link", { name: "Organization" })).toHaveCount(0);
  await expect(nav.getByRole("link", { name: "Agents" })).toHaveCount(0);
  await expect(page.getByRole("heading", { name: /^Who works here/ })).toHaveCount(0);
  // Viewers read no people: Home counts none.
  await expect(page.getByText(/running\b/).first()).toBeVisible();
  await expect(page.getByText(/\d+ (person|people)\b|people can open/)).toHaveCount(0);

  // No action a viewer cannot perform shows up (the API refuses every write
  // of a viewer). Each view first shows something it renders only once its
  // data arrived, so an absent button is not just a view still loading.
  const none = async (...names: (string | RegExp)[]) => {
    for (const name of names) await expect(page.getByRole("button", { name })).toHaveCount(0);
  };
  await expect(page.getByRole("heading", { name: /^Activity/ })).toBeVisible();
  await none("Approve", "Decline");
  await expect(page.getByRole("link", { name: /^Add an app/ })).toHaveCount(0);
  const h2 = (name: RegExp) => page.getByRole("heading", { name, level: 2 });
  const views: [string, string, Locator, () => Promise<void>][] = [
    ["/approvals", "Approvals", h2(/^Waiting/), () => none("Approve", "Decline")],
    ["/history", "History", page.getByRole("tab", { name: "All" }), async () => {}],
    ["/machines", "Machines", h2(/^How it is reached/), () => none(/Add machine/)],
    ["/backups", "Backups", h2(/^Set a target|^Move the backups/), () => none("Check and use")],
    ["/apps", "Apps", h2(/^Installed/), async () => {
      await none("Install", "Try again", "Let Packer package it");
      await expect(h2(/^Add an app/)).toHaveCount(0);
    }],
  ];
  for (const [path, heading, loaded, check] of views) {
    await nav.getByRole("link", { name: heading, exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`${path}$`));
    await expect(page.getByRole("heading", { name: heading, level: 1 })).toBeVisible();
    await expect(loaded).toBeVisible();
    await check();
  }
  const rows = page.getByRole("table", { name: "Installed apps" }).locator("a.row");
  if ((await rows.count()) > 0) {
    await rows.first().click();
    const drawer = page.getByRole("dialog");
    await expect(drawer.getByText(/Pods, events and logs are for admins/)).toBeVisible();
    await expect(drawer.getByRole("heading", { name: /^What runs/ })).toHaveCount(0);
    // Closing is all a viewer can do in the drawer: no change, backup,
    // restore, diagnosis or removal.
    await expect(drawer.getByRole("heading", { name: /^Backups/ })).toBeVisible();
    await expect(drawer.getByRole("button", { name: "Close" })).toBeVisible();
    await expect(drawer.getByRole("button")).toHaveCount(1);
    await page.keyboard.press("Escape");
  }
  // A typed address of an admin view leads home.
  await page.goto("/organization");
  await expect(page.getByRole("status").filter({ hasText: "You see the state of the cell" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Organization", level: 1 })).toHaveCount(0);
  expect(errors).toEqual([]);
});
