import { expect, test } from "@playwright/test";
import { env, signIn, watchErrors } from "./helpers";

// The apps of the cell and one app in the drawer. Which apps the local cell
// runs differs, so the test opens whichever comes first.
test("the admin sees the apps, the catalog and one app in the drawer", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  await signIn(page, env("CONSOLE_ADMIN_USER"), env("CONSOLE_ADMIN_PASSWORD"), "/apps");
  await expect(page.getByRole("heading", { name: "Apps", level: 1 })).toBeVisible();
  await expect(page.getByRole("heading", { name: /^Installed/ })).toBeVisible();
  await expect(page.getByRole("heading", { name: /^Add an app/ })).toBeVisible();

  const rows = page.getByRole("table", { name: "Installed apps" }).locator("a.row");
  if ((await rows.count()) > 0) {
    await rows.first().click();
    await expect(page).toHaveURL(/\/apps\/[a-z0-9-]+$/);
    const drawer = page.getByRole("dialog");
    await expect(drawer.getByRole("heading", { name: /^Who can open it/ })).toBeVisible();
    await expect(drawer.getByRole("heading", { name: /^Backups/ })).toBeVisible();
    // Removing asks first.
    await drawer.getByRole("button", { name: "Remove and delete the data" }).click();
    await expect(drawer.getByRole("button", { name: "Yes, remove and delete the data" })).toBeVisible();
    await drawer.getByRole("button", { name: "Cancel" }).click();
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/apps$/);
  }
  expect(errors).toEqual([]);
});
