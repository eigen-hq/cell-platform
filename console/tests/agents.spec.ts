import { expect, test } from "@playwright/test";
import { env, signIn, watchErrors } from "./helpers";

// The agents, their work and one task with every step. The local cell may
// have no tasks yet; with one, the test opens its page from the board and
// goes back through the breadcrumb.
test("the admin sees the crew and the work board and opens a task", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  await signIn(page, env("CONSOLE_ADMIN_USER"), env("CONSOLE_ADMIN_PASSWORD"), "/agents");
  await expect(page.getByRole("heading", { name: "Agents", level: 1 })).toBeVisible();
  await expect(page.getByRole("heading", { name: "The crew" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Work board" })).toBeVisible();
  await expect(page.getByText("Ops", { exact: true })).toBeVisible();
  await expect(page.getByRole("form", { name: "Hand off to an agent" })).toBeVisible();

  const tasks = page.locator('a.row[href^="/agents/"]');
  if ((await tasks.count()) > 0) {
    await tasks.first().click();
    await expect(page).toHaveURL(/\/agents\/[a-z0-9-]+$/);
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await expect(page.getByRole("heading", { name: /^Every step/ })).toBeVisible();
    await page.getByRole("navigation", { name: "Breadcrumb" }).getByRole("link", { name: "Agents" }).click();
    await expect(page).toHaveURL(/\/agents$/);
    await expect(page.getByRole("heading", { name: "Work board" })).toBeVisible();
  }
  expect(errors).toEqual([]);
});
