import { expect, test, type Page } from "@playwright/test";

function env(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`${name} is not set`);
  return v;
}

// Every error in the console's own pages fails the test: CSP refusals show
// up only there. The identity's pages are not ours to judge.
function watchErrors(page: Page, origin: string): string[] {
  const errors: string[] = [];
  page.on("console", (m) => m.type() === "error" && m.location().url.startsWith(origin) && errors.push(m.text()));
  page.on("pageerror", (e) => page.url().startsWith(origin) && errors.push(e.message));
  return errors;
}

async function signIn(page: Page, username: string, password: string) {
  await page.goto("/");
  await page.waitForURL(/\/if\/flow\//);
  // authentik asks for the name, then the password, each as its own step.
  const uid = page.locator('input[name="uidField"]');
  await uid.fill(username);
  await uid.press("Enter");
  const pw = page.getByRole("textbox", { name: "Password" });
  await pw.fill(password);
  await pw.press("Enter");
}

test("an admin signs in and sees the console", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  const user = env("CONSOLE_ADMIN_USER");
  await signIn(page, user, env("CONSOLE_ADMIN_PASSWORD"));
  await page.waitForURL(`${baseURL}/`);
  await expect(page.getByRole("heading", { level: 1 })).toContainText(`Hello, ${user}.`);
  await expect(page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Home" })).toHaveAttribute("aria-current", "page");

  // The tone switch flips the page between night and paper and keeps the pick.
  const before = await page.evaluate(() => getComputedStyle(document.body).color);
  await page.getByRole("button", { name: /tone$/ }).click();
  await expect.poll(() => page.evaluate(() => getComputedStyle(document.body).color)).not.toBe(before);
  await page.reload();
  await expect(page.getByRole("heading", { level: 1 })).toContainText(`Hello, ${user}.`);
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
  await expect(page.getByRole("heading", { level: 1 })).toContainText(`Hello, ${user}.`);
  expect(await page.evaluate(() => sessionStorage.getItem("cell.tokens"))).not.toBeNull();
  await page.getByRole("button", { name: `Sign out ${user}` }).click();
  await page.waitForURL((u) => u.origin !== baseURL);
  await expect(page.getByText(/logged out/i).first()).toBeVisible();
  // The identity's own single sign-on stays, as for every app of the cell (E-40).
  await page.goto("/");
  await expect(page.getByRole("heading", { level: 1 })).toContainText(`Hello, ${user}.`);
});

test("a sign-out that cannot reach the login says so and keeps no token", async ({ page }) => {
  const user = env("CONSOLE_ADMIN_USER");
  await signIn(page, user, env("CONSOLE_ADMIN_PASSWORD"));
  await expect(page.getByRole("heading", { level: 1 })).toContainText(`Hello, ${user}.`);
  await page.route("**/v0/login", (route) => route.fulfill({ status: 503, contentType: "application/json", body: '{"error":"down"}' }));
  await page.getByRole("button", { name: `Sign out ${user}` }).click();
  await expect(page.getByRole("heading", { name: "Something went wrong" })).toBeVisible();
  await expect(page.getByText("The console cannot find its login (HTTP 503).")).toBeVisible();
  expect(await page.evaluate(() => sessionStorage.getItem("cell.tokens"))).toBeNull();
});
