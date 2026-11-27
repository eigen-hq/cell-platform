import { expect, test } from "@playwright/test";
import { env, signIn, watchErrors } from "./helpers";

// The Changes tab: what waits for a decision and the change log. The local
// cell's changes differ from run to run, so the test works with whatever is
// there: with a waiting change it checks that Decline asks first, deciding
// nothing; with a logged one it exports the log and opens that change.
test("the admin sees waiting changes and the log, filters it and opens one change", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  // The old address of the change log leads to the Changes tab.
  await signIn(page, env("CONSOLE_ADMIN_USER"), env("CONSOLE_ADMIN_PASSWORD"), "/history");
  await expect(page).toHaveURL(/\/changes$/);
  await expect(page.getByRole("heading", { name: "Changes", level: 1 })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Waiting for a decision", level: 2 })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Change log", level: 2 })).toBeVisible();

  const slips = page.locator("article.slip");
  if ((await slips.count()) === 0) {
    await expect(page.getByText("Nothing waits for a decision.", { exact: false }).first()).toBeVisible();
  } else {
    const first = slips.first();
    await expect(first.getByRole("button", { name: "Approve" })).toBeVisible();
    await first.getByRole("button", { name: "Decline" }).click();
    await expect(first.getByRole("button", { name: "Yes, decline" })).toBeVisible();
    await first.getByRole("button", { name: "Keep it" }).click();
  }

  await page.getByRole("tab", { name: "Not applied" }).click();
  await expect(page.getByRole("tab", { name: "Not applied" })).toHaveAttribute("aria-selected", "true");
  await page.getByRole("tab", { name: "All" }).click();

  const rows = page.getByRole("list", { name: "Change log" }).getByRole("link");
  if ((await rows.count()) > 0) {
    const download = page.waitForEvent("download");
    await page.getByRole("button", { name: "Export" }).click();
    expect((await download).suggestedFilename()).toMatch(/^changes-\d{4}-\d{2}-\d{2}\.csv$/);

    await rows.first().click();
    await expect(page).toHaveURL(/\/changes\/[a-z0-9-]+$/);
    await expect(page.getByRole("heading", { name: "Where it stands", level: 2 })).toBeVisible();
    await expect(page.getByRole("list", { name: "Steps" })).toBeVisible();
    await page.getByRole("link", { name: "All changes" }).click();
    await expect(page).toHaveURL(/\/changes$/);
  }
  expect(errors).toEqual([]);
});

test("the old approvals address leads to the Changes tab", async ({ page }) => {
  await signIn(page, env("CONSOLE_ADMIN_USER"), env("CONSOLE_ADMIN_PASSWORD"), "/approvals");
  await expect(page).toHaveURL(/\/changes$/);
  await expect(page.getByRole("heading", { name: "Changes", level: 1 })).toBeVisible();
});
