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
  await expect(page.getByRole("heading", { name: /^Agents/ })).toHaveCount(0);
  await expect(page.getByRole("textbox", { name: "What should Ops do?" })).toHaveCount(0);
  // Viewers read no people: Home counts apps, not who can open them.
  await expect(page.getByText(/of \d+ apps? answers?/).first()).toBeVisible();
  await expect(page.getByText(/\d+ (person|people)\b|people can open/)).toHaveCount(0);

  // No action a viewer cannot perform shows up (the API refuses every write
  // of a viewer). Each view first shows something it renders only once its
  // data arrived, so an absent button is not just a view still loading.
  const main = page.getByRole("main");
  const none = async (...names: (string | RegExp)[]) => {
    for (const name of names) await expect(main.getByRole("button", { name })).toHaveCount(0);
  };
  await expect(page.getByRole("heading", { name: /^Your cell/ })).toBeVisible();
  await none("Approve", "Decline");
  await expect(page.getByRole("link", { name: /^Add a machine/ })).toHaveCount(0);
  const h2 = (name: RegExp) => page.getByRole("heading", { name, level: 2 });
  const views: [string, string, Locator, () => Promise<void>][] = [
    ["/changes", "Changes", page.getByRole("tab", { name: "All" }), () => none("Approve", "Decline")],
    ["/machines", "Machines", h2(/^What runs where/), () => none("Add a machine")],
    ["/backups", "Backups", h2(/^Where the copies go/), () => none(/^Set a target|^Move the backups/)],
    ["/apps", "Apps", h2(/^Installed/), () => none("Add an app")],
  ];
  for (const [path, heading, loaded, check] of views) {
    await nav.getByRole("link", { name: heading, exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`${path}$`));
    await expect(page.getByRole("heading", { name: heading, level: 1 })).toBeVisible();
    await expect(loaded).toBeVisible();
    await check();
  }
  const rows = page.getByRole("list", { name: "Installed apps" }).getByRole("link");
  if ((await rows.count()) > 0) {
    await rows.first().click();
    await expect(page.getByText(/Pods, events and logs are for admins/)).toBeVisible();
    await expect(h2(/^Backups/)).toBeVisible();
    for (const name of [/^What runs/, /^Events/, /^Actions/]) await expect(h2(name)).toHaveCount(0);
    // Reading is all a viewer can do on an app's page: no change, backup,
    // restore, diagnosis or removal. Opening a plan only shows it.
    await expect(main.getByRole("button", { name: /^(?!See the plan|Hide the plan)/ })).toHaveCount(0);
  }
  // A typed address of an admin view leads home.
  await page.goto("/organization");
  await expect(page.getByRole("status").filter({ hasText: "You see the state of the cell" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Organization", level: 1 })).toHaveCount(0);
  expect(errors).toEqual([]);
});
