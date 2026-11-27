import { expect, test } from "@playwright/test";
import { env, signIn, watchErrors } from "./helpers";

// People, groups and agent accounts. The test account is in cell-admins, so
// that group is always there; adding people and groups is left to the API
// tests, since the identity keeps what a run creates.
test("the admin sees the groups and people and opens a group", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  await signIn(page, env("CONSOLE_ADMIN_USER"), env("CONSOLE_ADMIN_PASSWORD"), "/organization");
  await expect(page.getByRole("heading", { name: "Organization", level: 1 })).toBeVisible();
  await expect(page.getByText(env("CONSOLE_ADMIN_USER")).first()).toBeVisible();

  await page.getByRole("link", { name: /cell-admins/ }).click();
  await expect(page).toHaveURL(/\/organization\/cell-admins$/);
  const drawer = page.getByRole("dialog");
  await expect(drawer.getByRole("heading", { name: /^Members/ })).toBeVisible();
  await expect(drawer.getByRole("button", { name: "Save members" })).toBeDisabled();
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/\/organization$/);

  await expect(page.getByRole("heading", { name: "People", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Groups", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Add a person" }).click();
  await expect(page.getByRole("dialog").getByLabel("Username")).toBeVisible();
  await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByLabel("Username")).toBeHidden();
  expect(errors).toEqual([]);
});

// Each app password request makes a credential; only the one on screen can
// be copied, so a second request waits for the first. The answer is faked
// here, so the run leaves no credential behind.
test("app passwords are made one at a time", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  let answer!: () => void;
  const held = new Promise<void>((r) => (answer = r));
  await page.route("**/v0/users/*/tokens", async (route) => {
    await held;
    await route.fulfill({ status: 201, contentType: "application/json", body: JSON.stringify({ token: "fake-app-password" }) });
  });
  await signIn(page, env("CONSOLE_ADMIN_USER"), env("CONSOLE_ADMIN_PASSWORD"), "/organization");
  const buttons = page.getByRole("button", { name: "App password" });
  await buttons.first().click();
  for (const b of await buttons.all()) await expect(b).toBeDisabled();
  answer();
  await expect(page.getByText("fake-app-password")).toBeVisible();
  for (const b of await buttons.all()) await expect(b).toBeEnabled();
  expect(errors).toEqual([]);
});
