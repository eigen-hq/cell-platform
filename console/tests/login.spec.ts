import { expect, test } from "@playwright/test";
import { env, signIn, watchErrors } from "./helpers";

test("an admin signs in and sees the console", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  const user = env("CONSOLE_ADMIN_USER");
  await signIn(page, user, env("CONSOLE_ADMIN_PASSWORD"));
  await page.waitForURL(`${baseURL}/`);
  await expect(page.getByRole("button", { name: `Account of ${user}` })).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Overview" })).toHaveAttribute("aria-current", "page");

  // The tone switch flips the page between night and paper and keeps the pick.
  const before = await page.evaluate(() => getComputedStyle(document.body).color);
  await page.getByRole("button", { name: `Account of ${user}` }).click();
  await page.getByRole("button", { name: /tone$/ }).click();
  await expect.poll(() => page.evaluate(() => getComputedStyle(document.body).color)).not.toBe(before);
  await page.reload();
  await expect(page.getByRole("button", { name: `Account of ${user}` })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.dataset.tone)).toMatch(/^(night|paper)$/);

  expect(errors).toEqual([]);
});

test("a token the API refuses right after login shows an error, not a login loop", async ({ page, baseURL }) => {
  await page.route("**/v0/whoami", (route) => route.fulfill({ status: 401, contentType: "application/json", body: '{"error":"invalid token: test"}' }));
  await signIn(page, env("CONSOLE_ADMIN_USER"), env("CONSOLE_ADMIN_PASSWORD"));
  await expect(page.getByRole("heading", { name: "Something went wrong" })).toBeVisible();
  await expect(page.getByText("The API refused the new login: invalid token: test")).toBeVisible();
  await page.waitForTimeout(2000);
  expect(page.url()).toBe(`${baseURL}/`);
});

test("signing out drops the console's token and ends its session at the identity", async ({ page, baseURL }) => {
  const user = env("CONSOLE_ADMIN_USER");
  await signIn(page, user, env("CONSOLE_ADMIN_PASSWORD"));
  await expect(page.getByRole("button", { name: `Account of ${user}` })).toBeVisible();
  expect(await page.evaluate(() => sessionStorage.getItem("cell.tokens"))).not.toBeNull();
  // Requests that go out while the sign-out looks up the identity find no
  // token: they must not turn the sign-out into a new login.
  await page.route("**/v0/login", async (route) => {
    await new Promise((r) => setTimeout(r, 1500));
    await route.continue();
  });
  await page.getByRole("button", { name: `Account of ${user}` }).click();
  await page.getByRole("button", { name: "Sign out" }).click();
  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Apps" }).click();
  await page.waitForURL((u) => u.origin !== baseURL);
  await page.unroute("**/v0/login");
  await expect(page.getByText(/logged out/i).first()).toBeVisible();
  // The identity's own single sign-on stays, as for every app of the cell (E-40).
  await page.goto("/");
  await expect(page.getByRole("button", { name: `Account of ${user}` })).toBeVisible();
});

test("a sign-out that cannot reach the login says so and keeps no token", async ({ page }) => {
  const user = env("CONSOLE_ADMIN_USER");
  await signIn(page, user, env("CONSOLE_ADMIN_PASSWORD"));
  await expect(page.getByRole("button", { name: `Account of ${user}` })).toBeVisible();
  await page.route("**/v0/login", (route) => route.fulfill({ status: 503, contentType: "application/json", body: '{"error":"down"}' }));
  await page.getByRole("button", { name: `Account of ${user}` }).click();
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByRole("heading", { name: "Something went wrong" })).toBeVisible();
  await expect(page.getByText("The console cannot find its login (HTTP 503).")).toBeVisible();
  expect(await page.evaluate(() => sessionStorage.getItem("cell.tokens"))).toBeNull();
});
