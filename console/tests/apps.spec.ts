import { expect, test } from "@playwright/test";
import { env, signIn, watchErrors } from "./helpers";

// The apps of the cell and one app on its own page. Which apps the local
// cell runs differs, so the test opens whichever comes first.
test("the admin sees the apps and one app on its page", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  await signIn(page, env("CONSOLE_ADMIN_USER"), env("CONSOLE_ADMIN_PASSWORD"), "/apps");
  await expect(page.getByRole("heading", { name: "Apps", level: 1 })).toBeVisible();
  await expect(page.getByRole("heading", { name: /^Installed/ })).toBeVisible();
  await expect(page.getByRole("button", { name: "Add an app" })).toBeVisible();

  const rows = page.getByRole("list", { name: "Installed apps" }).getByRole("link");
  if ((await rows.count()) > 0) {
    await rows.first().click();
    await expect(page).toHaveURL(/\/apps\/[a-z0-9-]+$/);
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await expect(page.getByRole("heading", { name: /^Who can open it/ })).toBeVisible();
    await expect(page.getByRole("heading", { name: /^Backups/ })).toBeVisible();
    await expect(page.getByRole("heading", { name: /^What runs/ })).toBeVisible();
    // A new version is asked for like any change; the form opens in a drawer.
    await page.getByRole("button", { name: "Change version" }).click();
    const drawer = page.getByRole("dialog");
    await expect(drawer.getByLabel("New version")).toBeVisible();
    await drawer.getByLabel("New version").fill("");
    await expect(drawer.getByRole("button", { name: "Ask for the update" })).toBeDisabled();
    await drawer.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByLabel("New version")).toHaveCount(0);
    await page.getByRole("button", { name: "Log", exact: true }).first().click();
    await expect(page.getByText(/the last 300 lines/)).toBeVisible();
    // Restarting and removing ask first.
    await page.getByRole("button", { name: "Restart", exact: true }).click();
    await expect(page.getByRole("button", { name: "Yes, restart" })).toBeVisible();
    await page.getByRole("button", { name: "Cancel" }).click();
    await page.getByRole("button", { name: "Remove and delete the data" }).click();
    await expect(page.getByRole("button", { name: "Yes, remove and delete the data" })).toBeVisible();
    await page.getByRole("button", { name: "Cancel" }).click();
    await page.getByRole("navigation", { name: "Breadcrumb" }).getByRole("link", { name: "Apps" }).click();
    await expect(page).toHaveURL(/\/apps$/);
  }
  expect(errors).toEqual([]);
});

// Packaging asks Packer; the form checks the name before anything is sent.
test("the package form checks the name and needs a Compose file", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  await signIn(page, env("CONSOLE_ADMIN_USER"), env("CONSOLE_ADMIN_PASSWORD"), "/apps");
  await page.getByRole("button", { name: "Add an app" }).click();
  const drawer = page.getByRole("dialog", { name: "Add an app" });
  await expect(drawer.getByRole("heading", { name: /^From a Compose file or a manifest/ })).toBeVisible();
  const submit = drawer.getByRole("button", { name: "Let Packer package it" });
  await drawer.getByLabel("Name of the app").fill("Bad Name");
  await expect(drawer.getByText("Starts with a letter; lowercase letters, digits and dashes, up to 40.")).toBeVisible();
  await drawer.getByLabel("Name of the app").fill("e2e-wiki");
  await expect(submit).toBeDisabled();
  await drawer.getByLabel("or paste it").fill("services:\n  wiki:\n    image: ghcr.io/requarks/wiki:2.5\n");
  await expect(submit).toBeEnabled();
  expect(errors).toEqual([]);
});
