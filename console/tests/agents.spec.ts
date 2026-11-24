import { expect, test } from "@playwright/test";
import { env, signIn, watchErrors } from "./helpers";

// The agents, their work and one task with every step. The local cell may
// have no tasks yet; with one, the test opens it from the board.
test("the admin sees who did the work and the work board", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  await signIn(page, env("CONSOLE_ADMIN_USER"), env("CONSOLE_ADMIN_PASSWORD"), "/agents");
  await expect(page.getByRole("heading", { name: "Agents", level: 1 })).toBeVisible();
  await expect(page.getByRole("heading", { name: /^Who did the work/ })).toBeVisible();
  await expect(page.getByRole("heading", { name: /^Work board/ })).toBeVisible();
  await expect(page.getByText("Ops", { exact: true })).toBeVisible();

  const cards = page.locator('a.card[href^="/agents/"]');
  if ((await cards.count()) > 0) {
    await cards.first().click();
    await expect(page).toHaveURL(/\/agents\/[a-z0-9-]+$/);
    await expect(page.getByRole("dialog").getByRole("heading", { name: /^Every step/ })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/agents$/);
  }
  expect(errors).toEqual([]);
});
