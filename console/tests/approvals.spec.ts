import { expect, test } from "@playwright/test";
import { env, signIn, watchErrors } from "./helpers";

// Changes waiting for a yes. The local cell may have none; with one, the
// test checks the ticket and that Decline asks first, deciding nothing.
test("the admin sees a waiting change as a ticket and asks before declining", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  await signIn(page, env("CONSOLE_ADMIN_USER"), env("CONSOLE_ADMIN_PASSWORD"), "/approvals");
  await expect(page.getByRole("heading", { name: "Approvals", level: 1 })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Who decides" })).toBeVisible();

  const slips = page.locator("article.slip");
  if ((await slips.count()) === 0) {
    await expect(page.getByText("Nothing waits for a yes.")).toBeVisible();
  } else {
    const first = slips.first();
    await expect(first.getByRole("button", { name: "Approve" })).toBeVisible();
    await first.getByRole("button", { name: "Decline" }).click();
    await expect(first.getByRole("button", { name: "Yes, decline" })).toBeVisible();
    await first.getByRole("button", { name: "Keep it" }).click();
  }
  expect(errors).toEqual([]);
});
