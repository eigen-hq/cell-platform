import { expect, test } from "@playwright/test";
import { env, signIn, watchErrors } from "./helpers";

// The change log. The local cell's log differs from run to run, so the test
// opens whichever change comes first and checks the filters and the export.
test("the admin sees the history, filters it and opens one change", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  await signIn(page, env("CONSOLE_ADMIN_USER"), env("CONSOLE_ADMIN_PASSWORD"), "/history");
  await expect(page.getByRole("heading", { name: "History", level: 1 })).toBeVisible();
  await expect(page.getByText("Agent tasks")).toBeVisible();

  await page.getByRole("tab", { name: "Not applied" }).click();
  await expect(page.getByRole("tab", { name: "Not applied" })).toHaveAttribute("aria-selected", "true");
  await page.getByRole("tab", { name: "All" }).click();

  const rows = page.getByRole("table", { name: "Changes" }).locator("a.row");
  if ((await rows.count()) > 0) {
    const download = page.waitForEvent("download");
    await page.getByRole("button", { name: "Export" }).click();
    expect((await download).suggestedFilename()).toMatch(/^changes-\d{4}-\d{2}-\d{2}\.csv$/);

    await rows.first().click();
    await expect(page).toHaveURL(/\/history\/[a-z0-9-]+$/);
    await expect(page.getByRole("dialog").getByRole("heading", { name: "Change request" })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/history$/);
  }
  expect(errors).toEqual([]);
});
